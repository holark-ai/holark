package holarkcli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/holark-ai/holark/internal/localconnection"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

func TestLocalConnectionIgnoresLegacyGlobalFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "connection.json"), []byte(`{"url":"http://127.0.0.1:43123","token":"private-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := commandRunner{env: func(key string) string {
		if key == "HOLARK_RUNTIME_DIR" {
			return directory
		}
		return ""
	}}
	runner.setDefaults()
	if connection, ok := runner.localConnection(t.Context()); ok || connection != (localConnection{}) {
		t.Fatalf("legacy connection = %+v, available = %t", connection, ok)
	}
}

func TestLocalConnectionFollowsCurrentRepository(t *testing.T) {
	directory := t.TempDir()
	firstRepository := t.TempDir()
	secondRepository := t.TempDir()
	git(t, firstRepository, "init")
	git(t, secondRepository, "init")
	firstID, err := gitadapter.RepositoryID(t.Context(), firstRepository)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := gitadapter.RepositoryID(t.Context(), secondRepository)
	if err != nil {
		t.Fatal(err)
	}
	first := localconnection.Connection{URL: "http://127.0.0.1:43123", Token: "first-token"}
	second := localconnection.Connection{URL: "http://127.0.0.1:43124", Token: "second-token"}
	cleanupFirst, err := localconnection.Publish(directory, firstID, first)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupFirst()
	cleanupSecond, err := localconnection.Publish(directory, secondID, second)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSecond()

	for _, test := range []struct {
		name       string
		repository string
		want       localconnection.Connection
	}{
		{name: "first", repository: firstRepository, want: first},
		{name: "second", repository: secondRepository, want: second},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(test.repository)
			runner := commandRunner{env: func(key string) string {
				if key == "HOLARK_RUNTIME_DIR" {
					return directory
				}
				return ""
			}}
			runner.setDefaults()
			client, err := runner.client(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if client.BaseURL != test.want.URL || client.BearerToken != test.want.Token {
				t.Fatalf("client = %+v", client)
			}
		})
	}
}

func TestIssueCreate(t *testing.T) {
	var requestBody struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/issues" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(issueJSON("issue-1", "Fix issue list", "Body text", "open", 0)))
	}))
	defer server.Close()

	stdout, stderr, code := runTestCommand([]string{
		"--server", server.URL,
		"issue", "create",
		"--title", "Fix issue list",
		"--body", "Body text",
	}, "")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if requestBody.Title != "Fix issue list" || requestBody.Body != "Body text" {
		t.Fatalf("requestBody = %#v", requestBody)
	}
	if !strings.Contains(stdout, "Created issue issue-1: Fix issue list") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestIssueCommandsUseAgentAuthToken(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[` + issueJSON("issue-1", "Open issue", "", "open", 0) + `]`))
	}))
	defer server.Close()

	_, stderr, code := runTestCommandWithEnv([]string{
		"--server", server.URL,
		"issue", "list",
	}, "", map[string]string{"HOLARK_CLI_AUTH_TOKEN": "agent-token"})
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if authorization != "Bearer agent-token" {
		t.Fatalf("authorization = %q, want bearer token", authorization)
	}
}

func TestServerEnvironmentOverrideUsesExplicitToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer legacy-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	_, stderr, code := runTestCommandWithEnv([]string{
		"issue", "list",
	}, "", map[string]string{
		"HOLARK_SERVER_URL":     server.URL,
		"HOLARK_CLI_AUTH_TOKEN": "legacy-token",
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestIssueCreateBodyFileStdinJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var requestBody struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		if requestBody.Body != "Detailed reproduction\n" {
			t.Fatalf("body = %q", requestBody.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(issueJSON("issue-1", "Fix issue list", requestBody.Body, "open", 0)))
	}))
	defer server.Close()

	stdout, stderr, code := runTestCommand([]string{
		"--server", server.URL,
		"issue", "create",
		"--title", "Fix issue list",
		"--body-file", "-",
		"--json",
	}, "Detailed reproduction\n")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, `"body": "Detailed reproduction\n"`) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestIssueCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_request","message":"The issue request is invalid."}`))
	}))
	defer server.Close()

	_, stderr, code := runTestCommand([]string{
		"--server", server.URL,
		"issue", "create",
		"--title", "Fix issue list",
	}, "")
	if code != 1 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "invalid_request: The issue request is invalid.") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestIssueCreateRequiresTitleAndRejectsRepositoryFlag(t *testing.T) {
	_, stderr, code := runTestCommand([]string{"issue", "create"}, "")
	if code != 2 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stderr, "--title is required") {
		t.Fatalf("stderr = %q", stderr)
	}

	_, stderr, code = runTestCommand([]string{"--server", "http://127.0.0.1:43123", "issue", "create", "--repository", "holark", "--title", "Title"}, "")
	if code != 2 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stderr, "flag provided but not defined: -repository") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestLabelCommandsHelp(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		usage string
	}{
		{name: "list labels", args: []string{"label", "list", "--help"}, usage: "holark label list"},
		{name: "create label", args: []string{"label", "create", "--help"}, usage: "holark label create"},
		{name: "add issue label", args: []string{"issue", "label", "add", "--help"}, usage: "holark issue label add"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, stderr, code := runTestCommand(test.args, "")
			if code != 0 {
				t.Fatalf("code = %d, stderr = %q", code, stderr)
			}
			if !strings.Contains(stderr, test.usage) {
				t.Fatalf("stderr = %q, want usage containing %q", stderr, test.usage)
			}
			if strings.Contains(stderr, flag.ErrHelp.Error()) {
				t.Fatalf("stderr = %q, want help without an error", stderr)
			}
		})
	}
}

func TestLabelCommandsUseCanonicalRoutesWithoutRepository(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"label-one","name":"bug","color":"d73a4a","description":""}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	for _, args := range [][]string{
		{"--server", server.URL, "label", "list"},
		{"--server", server.URL, "label", "create", "--name", "bug", "--color", "d73a4a"},
	} {
		_, stderr, code := runTestCommand(args, "")
		if code != 0 {
			t.Fatalf("args = %#v, code = %d, stderr = %s", args, code, stderr)
		}
	}
	want := []string{"GET /api/v1/labels", "POST /api/v1/labels"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestIssueListFiltersStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/issues" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			` + issueJSON("issue-1", "Open issue", "", "open", 12) + `,
			` + issueJSON("issue-2", "Closed issue", "", "closed", 0) + `
		]`))
	}))
	defer server.Close()

	stdout, stderr, code := runTestCommand([]string{
		"--server", server.URL,
		"issue", "list",
		"--status", "open",
	}, "")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "Open issue") {
		t.Fatalf("stdout = %q", stdout)
	}
	if strings.Contains(stdout, "Closed issue") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestIssueListIgnoresWorktreeGitConfiguration(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "holark.serverURL", "http://127.0.0.1:8080")
	t.Chdir(t.TempDir())

	_, stderr, code := runTestCommandWithEnv([]string{
		"issue", "list",
	}, "", map[string]string{"HOLARK_REPO_PATH": repo})
	if code != 1 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "start or restart Holark for this repository") || strings.Contains(stderr, "8080") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestServerOverrideReusesOnlyMatchingScopedToken(t *testing.T) {
	repository := t.TempDir()
	git(t, repository, "init")
	runtimeDirectory := t.TempDir()
	repositoryID, err := gitadapter.RepositoryID(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	cleanup, err := localconnection.Publish(runtimeDirectory, repositoryID, localConnection{URL: server.URL, Token: "scoped-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	_, stderr, code := runTestCommandWithEnv([]string{"--server", server.URL, "issue", "list"}, "", map[string]string{
		"HOLARK_RUNTIME_DIR":    runtimeDirectory,
		"HOLARK_REPO_PATH":      repository,
		"HOLARK_CLI_AUTH_TOKEN": "stale-token",
	})
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if authorization != "Bearer scoped-token" {
		t.Fatalf("authorization = %q", authorization)
	}
}

func TestServerOverrideRequiresTokenAndLoopback(t *testing.T) {
	_, stderr, code := runTestCommandWithEnv([]string{"--server", "http://127.0.0.1:43123", "issue", "list"}, "", nil)
	if code != 1 || !strings.Contains(stderr, "HOLARK_CLI_AUTH_TOKEN is required") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	for _, serverURL := range []string{
		"https://example.com",
		"http://127.0.0.1:43123?",
		"http://127.0.0.1:43123/?",
	} {
		t.Run(serverURL, func(t *testing.T) {
			_, stderr, code := runTestCommandWithEnv([]string{"--server", serverURL, "issue", "list"}, "", map[string]string{"HOLARK_CLI_AUTH_TOKEN": "secret"})
			if code != 1 || !strings.Contains(stderr, "loopback HTTP origin") || strings.Contains(stderr, "secret") {
				t.Fatalf("code = %d, stderr = %q", code, stderr)
			}
		})
	}
}

func TestEndpointIsResolvedOnlyOnce(t *testing.T) {
	repository := t.TempDir()
	git(t, repository, "init")
	runtimeDirectory := t.TempDir()
	repositoryID, err := gitadapter.RepositoryID(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	want := localConnection{URL: "http://127.0.0.1:43123", Token: "scoped-token"}
	cleanup, err := localconnection.Publish(runtimeDirectory, repositoryID, want)
	if err != nil {
		t.Fatal(err)
	}
	runner := commandRunner{env: func(name string) string {
		return map[string]string{"HOLARK_RUNTIME_DIR": runtimeDirectory, "HOLARK_REPO_PATH": repository}[name]
	}}
	runner.setDefaults()
	first, err := runner.client(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	second, err := runner.client(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseURL != want.URL || first.BearerToken != want.Token || first != second {
		t.Fatalf("clients = %+v, %+v", first, second)
	}
}

func TestIssueGetAndStateCommands(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(issueJSON("issue-1", "Fix issue list", "Body", "open", 0)))
	}))
	defer server.Close()

	for _, args := range [][]string{
		{"--server", server.URL, "issue", "get", "issue-1"},
		{"--server", server.URL, "issue", "close", "issue-1"},
		{"--server", server.URL, "issue", "reopen", "issue-1"},
	} {
		_, stderr, code := runTestCommand(args, "")
		if code != 0 {
			t.Fatalf("args = %#v, code = %d, stderr = %s", args, code, stderr)
		}
	}
	want := []string{
		"/api/v1/issues/issue-1",
		"/api/v1/issues/issue-1/close",
		"/api/v1/issues/issue-1/reopen",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %#v", paths)
	}
	for index := range want {
		if paths[index] != want[index] {
			t.Fatalf("paths = %#v, want %#v", paths, want)
		}
	}
}

func TestIssueGetAcceptsTrailingJSONFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/issues/issue-1" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(issueJSON("issue-1", "Fix trailing flags", "Body", "open", 0)))
	}))
	defer server.Close()

	stdout, stderr, code := runTestCommand([]string{
		"--server", server.URL,
		"issue", "get", "issue-1", "--json",
	}, "")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, `"id": "issue-1"`) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestIssueListAcceptsInterspersedValueFlags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/issues" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			` + issueJSON("issue-1", "Open issue", "", "open", 0) + `,
			` + issueJSON("issue-2", "Closed issue", "", "closed", 0) + `
		]`))
	}))
	defer server.Close()

	stdout, stderr, code := runTestCommand([]string{
		"--server", server.URL,
		"issue", "list", "--json", "--status", "open",
	}, "")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "Open issue") || strings.Contains(stdout, "Closed issue") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func runTestCommand(args []string, stdin string) (string, string, int) {
	return runTestCommandWithEnv(args, stdin, map[string]string{"HOLARK_CLI_AUTH_TOKEN": "test-token"})
}

func runTestCommandWithEnv(args []string, stdin string, env map[string]string) (string, string, int) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), Options{
		Args:   args,
		Stdin:  strings.NewReader(stdin),
		Stdout: &stdout,
		Stderr: &stderr,
		Env: func(name string) string {
			return env[name]
		},
	})
	return stdout.String(), stderr.String(), code
}

func issueJSON(id, title, body, status string, githubNumber int) string {
	return issueJSONWithProject(id, "holark", title, body, status, githubNumber)
}

func issueJSONWithProject(id, repositoryID, title, body, status string, githubNumber int) string {
	syncData := "{}"
	if githubNumber != 0 {
		syncData = `{"github":{"number":` + fmtInt(githubNumber) + `,"url":"https://github.com/holark-ai/holark/issues/` + fmtInt(githubNumber) + `"}}`
	}
	return `{
		"id":"` + id + `",
		"repository_id":"` + repositoryID + `",
		"title":"` + title + `",
		"body":"` + strings.ReplaceAll(body, "\n", `\n`) + `",
		"status":"` + status + `",
		"sync_data":` + syncData + `,
		"linked_pull_request_ids":[],
		"created_at":"2026-01-01T10:00:00Z",
		"updated_at":"2026-01-01T10:00:00Z"
	}`
}

func fmtInt(value int) string {
	return strconv.Itoa(value)
}

func git(t *testing.T, dir string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}
