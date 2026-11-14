// Package logfile is a small stdlib-only size-rotating file writer, so the
// add-on keeps its own log history on disk (the HAOS journal is size-capped
// and shared).
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	// DefaultPath is where the add-on keeps its log.
	DefaultPath = "/data/logs/virtual_sensors.log"
	// DefaultMaxSize is the size above which the file is rotated.
	DefaultMaxSize = 5 << 20
	// DefaultBackups is the number of rotated files kept (.1 ... .N).
	DefaultBackups = 5
)

// Writer is an io.Writer that rotates path to path.1 ... path.N once it
// exceeds maxSize. Safe for concurrent use.
type Writer struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	backups int
	f       *os.File
	size    int64
}

// New opens (creating directories as needed) path for appending.
func New(path string, maxSize int64, backups int) (*Writer, error) {
	if maxSize <= 0 || backups < 1 {
		return nil, fmt.Errorf("logfile: invalid maxSize %d or backups %d", maxSize, backups)
	}
	w := &Writer{path: path, maxSize: maxSize, backups: backups}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// NewDefault is New with the add-on defaults.
func NewDefault() (*Writer, error) {
	return New(DefaultPath, DefaultMaxSize, DefaultBackups)
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	return nil
}

// Write appends p, rotating first if the file already exceeds the limit.
// It never returns an error for a failed rotation/reopen: logging must not
// take the process down (the write is dropped instead).
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size > w.maxSize || w.f == nil {
		w.rotate()
	}
	if w.f == nil {
		return len(p), nil
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	if err != nil {
		return len(p), nil
	}
	return n, nil
}

func (w *Writer) rotate() {
	if w.f != nil {
		w.f.Close()
		w.f = nil
		os.Remove(fmt.Sprintf("%s.%d", w.path, w.backups))
		for i := w.backups - 1; i >= 1; i-- {
			os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
		}
		os.Rename(w.path, w.path+".1")
	}
	_ = w.open()
}

// Close closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
