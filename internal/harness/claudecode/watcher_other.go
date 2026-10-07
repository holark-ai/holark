//go:build !darwin

package claudecode

import "github.com/fsnotify/fsnotify"

type fileWatcher = fsnotify.Watcher

func newFileWatcher() (*fileWatcher, error) { return fsnotify.NewWatcher() }
