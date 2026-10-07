package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/agentsettings"
	settingshttp "github.com/holark-ai/holark/internal/agentsettings/httpapi"
	"github.com/holark-ai/holark/internal/agentsettings/sqliteadapter"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/protocol"
)

func TestLaunchCommandsMergeAcrossRepositories(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "settings.sqlite")
	var repositories []*http.ServeMux
	for range 2 {
		db, err := database.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		store, err := sqliteadapter.New(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		settingshttp.RegisterCommandRoutes(func(path string, handler http.HandlerFunc) { mux.HandleFunc(path, handler) }, agentsettings.NewLaunchCommands(store, nil))
		repositories = append(repositories, mux)
	}
	put := func(repository int, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		repositories[repository].ServeHTTP(response, httptest.NewRequest("PUT", "/api/v1/settings/agent-launch-commands", strings.NewReader(body)))
		return response
	}
	assertCommands := func(want agentsettings.Commands) {
		t.Helper()
		for _, repository := range repositories {
			response := httptest.NewRecorder()
			repository.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/settings/agent-launch-commands", nil))
			var result struct {
				Commands agentsettings.Commands `json:"commands"`
			}
			if response.Code != http.StatusOK {
				t.Fatalf("get commands: %d %s", response.Code, response.Body.String())
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Commands, want) {
				t.Fatalf("commands = %v; want %v", result.Commands, want)
			}
		}
	}
	want := agentsettings.Commands{protocol.HarnessCodex: "", protocol.HarnessClaudeCode: "", protocol.HarnessOpenCode: ""}
	assertCommands(want)
	for i, body := range []string{`{"commands":{"codex":"custom-codex"}}`, `{"commands":{"claude-code":"npx claude"}}`} {
		if response := put(i, body); response.Code != http.StatusNoContent {
			t.Fatalf("save commands: %d %s", response.Code, response.Body.String())
		}
	}
	want[protocol.HarnessCodex] = "custom-codex"
	want[protocol.HarnessClaudeCode] = "npx claude"
	assertCommands(want)

	// Validate the entire patch before changing any saved command.
	if response := put(0, `{"commands":{"codex":"replacement","opencode":"./invalid"}}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid save: %d %s", response.Code, response.Body.String())
	}
	assertCommands(want)

	// Independent connections must merge concurrent changes, including resets.
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i, body := range []string{`{"commands":{"codex":""}}`, `{"commands":{"opencode":"custom-opencode"}}`} {
		go func() {
			<-start
			responses <- put(i, body)
		}()
	}
	close(start)
	for range 2 {
		if response := <-responses; response.Code != http.StatusNoContent {
			t.Errorf("concurrent save: %d %s", response.Code, response.Body.String())
		}
	}
	want[protocol.HarnessCodex] = ""
	want[protocol.HarnessOpenCode] = "custom-opencode"
	assertCommands(want)
}

func TestLaunchCommandsRequireAbsoluteExecutablePaths(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "settings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := sqliteadapter.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	service := agentsettings.NewLaunchCommands(store, nil)
	mux := http.NewServeMux()
	settingshttp.RegisterCommandRoutes(func(path string, handler http.HandlerFunc) { mux.HandleFunc(path, handler) }, service)
	absolute := filepath.Join(t.TempDir(), "wrapper with spaces")
	for _, command := range []string{"", "claude", "npx claude", `"` + absolute + `" --flag`, "./tools/claude-wrapper", "../claude-wrapper", "tools/claude-wrapper", `"./tools/wrapper with spaces" --flag`} {
		t.Run(command, func(t *testing.T) {
			values := agentsettings.Commands{protocol.HarnessCodex: "", protocol.HarnessClaudeCode: command, protocol.HarnessOpenCode: ""}
			body, err := json.Marshal(map[string]any{"commands": values})
			if err != nil {
				t.Fatal(err)
			}
			before, err := service.Current(ctx)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest("PUT", "/api/v1/settings/agent-launch-commands", strings.NewReader(string(body))))
			want := http.StatusNoContent
			relative := strings.Contains(command, "./") || strings.HasPrefix(command, "tools/")
			if relative {
				want = http.StatusBadRequest
			}
			if response.Code != want {
				t.Fatalf("response: %d %s; want %d", response.Code, response.Body.String(), want)
			}
			after, err := service.Current(ctx)
			if err != nil {
				t.Fatal(err)
			}
			expected := command
			if relative {
				expected = before[protocol.HarnessClaudeCode]
			}
			if after[protocol.HarnessClaudeCode] != expected {
				t.Fatalf("saved command = %q; want %q", after[protocol.HarnessClaudeCode], expected)
			}
			// Older settings must also be rejected before probing or launching.
			if err := store.SaveCommands(ctx, values); err != nil {
				t.Fatal(err)
			}
			_, err = service.ResolveCommand(ctx, protocol.HarnessClaudeCode)
			if relative && !errors.Is(err, agentsettings.ErrInvalid) || !relative && err != nil {
				t.Fatalf("resolve command: %v", err)
			}
		})
	}
}

func TestLaunchCommandsResolveSavedArgumentsAndResetDefaults(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "settings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := sqliteadapter.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	service := agentsettings.NewLaunchCommands(store, nil)
	mux := http.NewServeMux()
	settingshttp.RegisterCommandRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service)

	for _, harness := range []struct {
		kind       protocol.HarnessType
		executable string
	}{
		{protocol.HarnessCodex, "codex"},
		{protocol.HarnessClaudeCode, "claude"},
		{protocol.HarnessOpenCode, "opencode"},
	} {
		t.Run(string(harness.kind), func(t *testing.T) {
			assertResolved := func(executable string, arguments []string) {
				t.Helper()
				prefix, err := service.ResolveCommand(ctx, harness.kind)
				if err != nil {
					t.Fatal(err)
				}
				if prefix.Executable() != executable || !reflect.DeepEqual(prefix.Arguments(), arguments) {
					t.Fatalf("resolved command = %q %q; want %q %q", prefix.Executable(), prefix.Arguments(), executable, arguments)
				}
			}
			assertResolved(harness.executable, nil)
			wrapper := filepath.Join(t.TempDir(), "wrapper with spaces")
			custom := `"` + wrapper + `" --label 'two words' "" '$HOME' 'a;b'`
			for _, command := range []string{custom, " \t\n "} {
				body, err := json.Marshal(map[string]any{"commands": agentsettings.Commands{harness.kind: command}})
				if err != nil {
					t.Fatal(err)
				}
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings/agent-launch-commands", strings.NewReader(string(body))))
				if response.Code != http.StatusNoContent {
					t.Fatalf("save command: %d %s", response.Code, response.Body.String())
				}
				values, err := service.Current(ctx)
				if err != nil {
					t.Fatal(err)
				}
				wantSaved := custom
				if command == custom {
					assertResolved(wrapper, []string{"--label", "two words", "", "$HOME", "a;b"})
				} else {
					wantSaved = ""
					assertResolved(harness.executable, nil)
				}
				if values[harness.kind] != wantSaved {
					t.Fatalf("saved command = %q; want %q", values[harness.kind], wantSaved)
				}
			}
		})
	}
}
