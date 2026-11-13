package logsink

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Rotator appends lines to <dir>/<name>.log and rotates by size: when the
// next line would exceed MaxBytes, name.log becomes name.log.1, .1 becomes
// .2, and so on; at most Files files exist in total (the live one plus
// Files-1 rotated ones). Disk use is bounded by about MaxBytes*Files.
type Rotator struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	files    int
	f        *os.File
	size     int64
}

func NewRotator(dir, name string, maxBytes int64, files int) *Rotator {
	if files < 1 {
		files = 1
	}
	return &Rotator{path: filepath.Join(dir, name+".log"), maxBytes: maxBytes, files: files}
}

func (r *Rotator) open() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *Rotator) rotate() error {
	if r.f != nil {
		r.f.Close()
		r.f = nil
	}
	if r.files == 1 {
		return os.Remove(r.path)
	}
	os.Remove(fmt.Sprintf("%s.%d", r.path, r.files-1))
	for i := r.files - 2; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	return os.Rename(r.path, r.path+".1")
}

// WriteLine appends line plus a newline. line must not contain a newline
// (callers Escape first); one embedded would break the one-record-per-line
// guarantee, so any is replaced defensively.
func (r *Rotator) WriteLine(line string) error {
	if strings.ContainsAny(line, "\n\r") {
		line = Escape(line)
	}
	b := []byte(line + "\n")
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.open(); err != nil {
			return err
		}
	}
	if r.size > 0 && r.size+int64(len(b)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			return err
		}
		if err := r.open(); err != nil {
			return err
		}
	}
	n, err := r.f.Write(b)
	r.size += int64(n)
	if err != nil {
		r.f.Close()
		r.f = nil // reopen on the next write
	}
	return err
}

func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
