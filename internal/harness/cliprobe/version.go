// Package cliprobe shares version checks between capability discovery and launch.
package cliprobe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/commandprefix"
)

type entry struct {
	info       os.FileInfo
	output     string
	retryAfter time.Time
	err        error
	done       chan struct{}
}

var versions = struct {
	sync.Mutex
	entries map[string]*entry
}{entries: make(map[string]*entry)}

// Version caches successful executions until the executable changes, invalidating immediately
// when the executable is replaced or modified. Concurrent callers share a check;
// cancelled callers can stop waiting without cancelling another caller's check.
// Failed executions are cached for one minute to avoid repeated process launches.
func Version(ctx context.Context, executable string) (string, error) {
	return version(ctx, executable, time.Now)
}

func version(ctx context.Context, executable string, now func() time.Time, arguments ...string) (string, error) {
	path, err := exec.LookPath(executable)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	key := versionKey(path, arguments)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		versions.Lock()
		cached := versions.entries[key]
		if cached != nil && cached.done != nil {
			done := cached.done
			versions.Unlock()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-done:
				continue
			}
		}
		if cached != nil && (cached.err == nil || now().Before(cached.retryAfter)) &&
			os.SameFile(info, cached.info) && info.Size() == cached.info.Size() &&
			info.ModTime() == cached.info.ModTime() && info.Mode() == cached.info.Mode() {
			versions.Unlock()
			return cached.output, cached.err
		}
		pending := &entry{info: info, done: make(chan struct{})}
		versions.entries[key] = pending
		versions.Unlock()

		probeContext, cancel := context.WithTimeout(ctx, 3*time.Second)
		output, err := commandprefix.New(path, arguments...).CommandContext(probeContext, "--version").CombinedOutput()
		cancel()
		versions.Lock()
		close(pending.done)
		pending.done = nil
		if ctx.Err() == nil {
			pending.output = string(output)
			pending.err = err
			if err != nil {
				pending.retryAfter = now().Add(time.Minute)
			}
		} else {
			if versions.entries[key] == pending {
				delete(versions.entries, key)
			}
		}
		versions.Unlock()
		return string(output), err
	}
}

func VersionPrefix(ctx context.Context, prefix commandprefix.Prefix) (string, error) {
	return version(ctx, prefix.Executable(), time.Now, prefix.Arguments()...)
}
func versionKey(path string, arguments []string) string {
	if len(arguments) == 0 {
		return path
	}
	return commandprefix.New(path, arguments...).Key()
}
func Invalidate(prefix commandprefix.Prefix) {
	path, err := exec.LookPath(prefix.Executable())
	if err != nil {
		return
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return
	}
	versions.Lock()
	delete(versions.entries, versionKey(path, prefix.Arguments()))
	versions.Unlock()
}
