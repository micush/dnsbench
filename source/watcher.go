package main

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ConfigWatcher reloads the config file when it changes, using inotify
// (no polling).  It watches the file itself for in-place writes and the
// directory for create/rename-over, which is how most editors save.
type ConfigWatcher struct {
	path     string
	onChange func()

	mu     sync.Mutex
	f      *os.File
	fd     int
	wdFile int
	wdDir  int
	timer  *time.Timer
}

const (
	inModify     = syscall.IN_MODIFY
	inCloseWrite = syscall.IN_CLOSE_WRITE
	inMovedTo    = syscall.IN_MOVED_TO
	inCreate     = syscall.IN_CREATE
)

func NewConfigWatcher(path string, onChange func()) *ConfigWatcher {
	return &ConfigWatcher{path: path, onChange: onChange, wdFile: -1, wdDir: -1}
}

func (w *ConfigWatcher) Start(ctx context.Context) error {
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return err
	}
	w.fd = fd
	w.f = os.NewFile(uintptr(fd), "inotify")
	dir := filepath.Dir(w.path)
	if w.wdDir, err = syscall.InotifyAddWatch(fd, dir, inMovedTo|inCreate|inCloseWrite); err != nil {
		w.f.Close()
		return err
	}
	w.addFileWatch()
	debugf("inotify watching %s (file) + %s (dir)", w.path, dir)
	go w.loop()
	go func() {
		<-ctx.Done()
		w.f.Close()
	}()
	return nil
}

func (w *ConfigWatcher) addFileWatch() {
	if wd, err := syscall.InotifyAddWatch(w.fd, w.path, inModify|inCloseWrite); err == nil {
		w.wdFile = wd
	}
}

func (w *ConfigWatcher) loop() {
	name := filepath.Base(w.path)
	buf := make([]byte, 4096)
	for {
		n, err := w.f.Read(buf)
		if err != nil {
			return
		}
		triggered := false
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			wd := int(int32(binary.NativeEndian.Uint32(buf[off:])))
			nameLen := int(binary.NativeEndian.Uint32(buf[off+12:]))
			ev := string(trimNul(buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+nameLen]))
			off += syscall.SizeofInotifyEvent + nameLen
			switch {
			case wd == w.wdFile:
				triggered = true
			case wd == w.wdDir && ev == name:
				triggered = true
				w.addFileWatch() // new inode after rename/create
			}
		}
		if triggered {
			w.debounce()
		}
	}
}

// debounce coalesces a burst of events (editors emit several per save) and
// waits a moment so a half-written file isn't parsed.
func (w *ConfigWatcher) debounce() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(50*time.Millisecond, w.onChange)
}

func trimNul(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}
