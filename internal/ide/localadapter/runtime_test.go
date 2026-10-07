package localadapter

import (
	"context"
	"github.com/holark-ai/holark/internal/ide"
	"github.com/holark-ai/holark/internal/ideinstall"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeInstallsOnlyOnExplicitStartAndServesManagedWorktree(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "code")
	command := exec.Command("go", "build", "-o", binary, "./testdata/fake-code")
	if output, e := command.CombinedOutput(); e != nil {
		t.Fatalf("build fixture: %v: %s", e, output)
	}
	calls := 0
	runtime := New(Options{DataDirectory: t.TempDir(), ReadinessTimeout: 3 * time.Second, ShutdownTimeout: time.Second, Resolver: func(context.Context, ideinstall.Options) (ideinstall.Result, error) {
		calls++
		return ideinstall.Result{Executable: binary}, nil
	}})
	if calls != 0 {
		t.Fatal("resolver called before explicit start")
	}
	worktree := t.TempDir()
	start := ide.Start{IDEID: "ide-one", HolonID: "holon-one", WorktreePath: worktree, BasePath: ide.BasePath("holon-one", "ide-one")}
	if e := runtime.Start(t.Context(), start); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatalf("resolver calls=%d", calls)
	}
	target, ok := runtime.Target("ide-one")
	if !ok || target == "" {
		t.Fatalf("target=%q ok=%v", target, ok)
	}
	if e := runtime.Stop(t.Context(), "ide-one"); e != nil {
		t.Fatal(e)
	}
	if _, ok = runtime.Target("ide-one"); ok {
		t.Fatal("closed IDE still has target")
	}
}

func TestProvisioningFailureIsLoggedBeforeProcessStarts(t *testing.T) {
	t.Setenv("PATH", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>Download unavailable</html>"))
	}))
	defer server.Close()
	runtime := New(Options{DataDirectory: t.TempDir(), Resolver: func(ctx context.Context, options ideinstall.Options) (ideinstall.Result, error) {
		options.UpdateBaseURL = server.URL
		return ideinstall.Resolve(ctx, options)
	}})
	err := runtime.Start(t.Context(), ide.Start{IDEID: "failed", HolonID: "holon", WorktreePath: t.TempDir(), BasePath: ide.BasePath("holon", "failed")})
	if err == nil {
		t.Fatal("expected provisioning failure")
	}
	data, readErr := os.ReadFile(runtime.LogPath("failed"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, diagnostic := range []string{"Downloading Code CLI", "Extracting Code CLI", "IDE startup failed", "not a ZIP or gzip archive"} {
		if !strings.Contains(string(data), diagnostic) {
			t.Fatalf("missing %q in log: %s", diagnostic, data)
		}
	}
	if _, ok := runtime.Target("failed"); ok {
		t.Fatal("failed provisioning has a running process")
	}
}
