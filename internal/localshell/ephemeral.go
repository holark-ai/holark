package localshell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/filelock"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

const ephemeralCleanupTimeout = 30 * time.Second

type ephemeralRun struct {
	lease           *filelock.Lock
	baseDirectory   string
	commonDirectory string
}

func runEphemeral(ctx context.Context, options Options) (err error) {
	ephemeral, err := prepareEphemeralRun(ctx, options.RepositoryPath)
	if err != nil {
		return err
	}
	defer func() {
		cleanupErr := ephemeral.Close()
		if cleanupErr == nil {
			return
		}
		if errors.Is(err, context.Canceled) {
			err = cleanupErr
			return
		}
		err = errors.Join(err, cleanupErr)
	}()
	options.HomeDirectory = filepath.Join(ephemeral.baseDirectory, "home")
	options.RuntimeDirectory = filepath.Join(ephemeral.baseDirectory, "runtime")
	restoreEnvironment, err := setEphemeralEnvironment(options.HomeDirectory, options.RuntimeDirectory)
	if err != nil {
		return err
	}
	defer restoreEnvironment()
	return run(ctx, options)
}

func setEphemeralEnvironment(homeDirectory, runtimeDirectory string) (func(), error) {
	type previousValue struct {
		value string
		set   bool
	}
	previous := map[string]previousValue{}
	values := map[string]string{"HOLARK_HOME": homeDirectory, "HOLARK_RUNTIME_DIR": runtimeDirectory}
	for name, value := range values {
		old, set := os.LookupEnv(name)
		previous[name] = previousValue{value: old, set: set}
		if err := os.Setenv(name, value); err != nil {
			for restoreName, restore := range previous {
				if restore.set {
					_ = os.Setenv(restoreName, restore.value)
				} else {
					_ = os.Unsetenv(restoreName)
				}
			}
			return nil, fmt.Errorf("configure ephemeral environment: %w", err)
		}
	}
	return func() {
		for name, restore := range previous {
			if restore.set {
				_ = os.Setenv(name, restore.value)
			} else {
				_ = os.Unsetenv(name)
			}
		}
	}, nil
}

func prepareEphemeralRun(ctx context.Context, repositoryPath string) (*ephemeralRun, error) {
	repository, err := gitadapter.Open(ctx, repositoryPath)
	if err != nil {
		return nil, err
	}
	descriptor := repository.Descriptor()
	temporaryDirectory := os.TempDir()
	if canonical, canonicalErr := filepath.EvalSymlinks(temporaryDirectory); canonicalErr == nil {
		temporaryDirectory = canonical
	}
	baseDirectory := filepath.Join(temporaryDirectory, fmt.Sprintf("holark-%d", os.Getuid()), "ephemeral", descriptor.ID)
	if err := os.MkdirAll(baseDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create ephemeral directory: %w", err)
	}
	if err := os.Chmod(baseDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("secure ephemeral directory: %w", err)
	}
	// Serialize discovery and creation so a new directory cannot be collected
	// before its owner has acquired its lease. Keep this lock file in place.
	registry, err := acquireEphemeralRegistry(ctx, filepath.Join(baseDirectory, "lease.lock"))
	if err != nil {
		return nil, err
	}
	defer registry.Close()
	// Also collect state left by the previous, single-instance layout.
	if err := cleanupEphemeralState(ctx, descriptor.CommonDir, baseDirectory); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(baseDirectory)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "run-") {
			continue
		}
		directory := filepath.Join(baseDirectory, entry.Name())
		lease, err := filelock.Acquire(filepath.Join(directory, "lease.lock"))
		if errors.Is(err, filelock.ErrLocked) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("acquire abandoned ephemeral lease: %w", err)
		}
		abandoned := &ephemeralRun{lease: lease, baseDirectory: directory, commonDirectory: descriptor.CommonDir}
		if err := abandoned.Close(); err != nil {
			return nil, err
		}
	}
	directory, err := os.MkdirTemp(baseDirectory, "run-")
	if err != nil {
		return nil, fmt.Errorf("create ephemeral instance: %w", err)
	}
	lease, err := filelock.Acquire(filepath.Join(directory, "lease.lock"))
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(directory))
	}
	return &ephemeralRun{
		lease: lease, baseDirectory: directory, commonDirectory: descriptor.CommonDir,
	}, nil
}

func acquireEphemeralRegistry(ctx context.Context, path string) (*filelock.Lock, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lease, err := filelock.Acquire(path)
		if !errors.Is(err, filelock.ErrLocked) {
			return lease, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (run *ephemeralRun) Close() error {
	if run == nil {
		return nil
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), ephemeralCleanupTimeout)
	defer cancel()
	err := cleanupEphemeralState(cleanupContext, run.commonDirectory, run.baseDirectory)
	// Preserve failed cleanup for the next launch to retry.
	if err == nil {
		err = removeEphemeralDirectory(run.baseDirectory)
	}
	return errors.Join(err, run.lease.Close())
}

func cleanupEphemeralState(ctx context.Context, commonDirectory, baseDirectory string) error {
	homeDirectory := filepath.Join(baseDirectory, "home")
	worktrees := filepath.Join(homeDirectory, "worktrees")
	if err := gitadapter.CleanupWorktreeRoot(ctx, commonDirectory, worktrees); err != nil {
		return err
	}
	return errors.Join(
		removeEphemeralDirectory(homeDirectory),
		removeEphemeralDirectory(filepath.Join(baseDirectory, "runtime")),
	)
}

func removeEphemeralDirectory(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove ephemeral directory %q: %w", filepath.Base(path), err)
	}
	return nil
}
