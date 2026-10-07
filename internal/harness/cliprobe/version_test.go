package cliprobe

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// This exercises the generic subprocess cache, without impersonating an agent
// CLI or relying on an installed agent's version, credentials, or startup time.
func TestVersionSharesExecutionsAndInvalidatesChangedExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "version-tool")
	script := "#!/bin/sh\ncd \"$(dirname \"$0\")\"\nprintf 'called\\n' >> calls\nprintf 'first\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			output, err := Version(t.Context(), path)
			if err != nil || output != "first\n" {
				t.Errorf("version = %q, %v", output, err)
			}
		}()
	}
	wg.Wait()
	if output, err := version(t.Context(), path, func() time.Time { return time.Now().Add(24 * time.Hour) }); err != nil || output != "first\n" {
		t.Fatalf("cached version after a day = %q, %v", output, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Version(ctx, path); err != context.Canceled {
		t.Fatalf("cancelled check = %v", err)
	}
	// Replacing the executable must bypass even a still-fresh cached result.
	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte(strings.ReplaceAll(script, "first", "second")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	output, err := Version(t.Context(), path)
	if err != nil || output != "second\n" {
		t.Fatalf("replacement version = %q, %v", output, err)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil || string(calls) != "called\ncalled\n" {
		t.Fatalf("executions = %q, %v", calls, err)
	}
}

func TestVersionBacksOffFailedExecutions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "version-tool")
	script := "#!/bin/sh\ncd \"$(dirname \"$0\")\"\nif [ ! -f ready ]; then touch ready; exit 1; fi\nprintf 'ready\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Version(t.Context(), path); err == nil {
		t.Fatal("expected the first execution to fail")
	}
	if _, err := Version(t.Context(), path); err == nil {
		t.Fatal("expected the failure to remain cached during backoff")
	}
	output, err := version(t.Context(), path, func() time.Time { return time.Now().Add(time.Minute) })
	if err != nil || output != "ready\n" {
		t.Fatalf("retry = %q, %v", output, err)
	}
}
