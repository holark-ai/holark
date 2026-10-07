package claudecode

import (
	"errors"
	"os"
	"sync"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
)

type vnodeWatch struct {
	fd   int
	file *os.File
	info os.FileInfo
}

// Unlike fsnotify's kqueue backend, this watcher registers directory vnodes
// without listing or opening their children. Callers explicitly add selected
// files and revalidate watches before reading, including after replacement.
type fileWatcher struct {
	Events  chan fsnotify.Event
	Errors  chan error
	mu      sync.Mutex
	queue   int
	wake    [2]int
	paths   map[string]vnodeWatch
	names   map[int]string
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func newFileWatcher() (*fileWatcher, error) {
	queue, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(queue)
	w := &fileWatcher{queue: queue, paths: make(map[string]vnodeWatch), names: make(map[int]string),
		Events: make(chan fsnotify.Event, 32), Errors: make(chan error, 1), done: make(chan struct{}), stopped: make(chan struct{})}
	if err = unix.Pipe(w.wake[:]); err != nil {
		unix.Close(queue)
		return nil, err
	}
	for _, fd := range w.wake {
		unix.CloseOnExec(fd)
	}
	change := unix.Kevent_t{Ident: uint64(w.wake[0]), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_CLEAR}
	if _, err = unix.Kevent(queue, []unix.Kevent_t{change}, nil, nil); err != nil {
		unix.Close(w.wake[0])
		unix.Close(w.wake[1])
		unix.Close(queue)
		return nil, err
	}
	go w.run()
	return w, nil
}

func (w *fileWatcher) Add(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.done:
		return os.ErrClosed
	default:
	}
	info, err := os.Stat(path)
	old, exists := w.paths[path]
	if err == nil && exists && os.SameFile(info, old.info) {
		return nil
	}
	if exists {
		// Closing removes the old vnode registration, even after a rename.
		old.file.Close()
		delete(w.paths, path)
		delete(w.names, old.fd)
	}
	if err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_EVTONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err = file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	change := unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_VNODE, Flags: unix.EV_ADD | unix.EV_CLEAR,
		Fflags: unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_ATTRIB | unix.NOTE_LINK | unix.NOTE_RENAME | unix.NOTE_DELETE | unix.NOTE_REVOKE}
	if _, err = unix.Kevent(w.queue, []unix.Kevent_t{change}, nil, nil); err != nil {
		file.Close()
		return err
	}
	// Retain the os.File so its finalizer cannot close the registered descriptor.
	w.paths[path] = vnodeWatch{fd: fd, file: file, info: info}
	w.names[fd] = path
	return nil
}

func (w *fileWatcher) run() {
	defer close(w.stopped)
	defer close(w.Events)
	defer close(w.Errors)
	var events [16]unix.Kevent_t
	for {
		n, err := unix.Kevent(w.queue, nil, events[:], nil)
		select {
		case <-w.done:
			return
		default:
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			w.Errors <- err
			return
		}
		for _, event := range events[:n] {
			if event.Filter == unix.EVFILT_VNODE && event.Fflags&unix.NOTE_REVOKE != 0 {
				w.Errors <- errors.New("Claude file watch was revoked")
				return
			}
			if event.Flags&unix.EV_ERROR != 0 {
				w.Errors <- unix.Errno(event.Data)
				return
			}
			w.mu.Lock()
			name := w.names[int(event.Ident)]
			w.mu.Unlock()
			if name == "" {
				continue
			}
			// A descriptor reused while kevent returns can cause an extra wakeup;
			// Add always verifies the current inode before the next content read.
			select {
			case w.Events <- fsnotify.Event{Name: name}:
			case <-w.done:
				return
			}
		}
	}
}

func (w *fileWatcher) Close() error {
	w.once.Do(func() {
		close(w.done)
		_, _ = unix.Write(w.wake[1], []byte{1})
		<-w.stopped
		w.mu.Lock()
		defer w.mu.Unlock()
		for _, watch := range w.paths {
			watch.file.Close()
		}
		unix.Close(w.wake[0])
		unix.Close(w.wake[1])
		unix.Close(w.queue)
	})
	return nil
}
