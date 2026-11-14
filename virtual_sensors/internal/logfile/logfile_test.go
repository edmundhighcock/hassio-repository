package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotationKeepsBackupsAndDropsOldest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "x.log")
	w, err := New(p, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 10; i++ {
		line := strings.Repeat(string(rune('a'+i)), 59) + "\n" // 60 bytes
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"", ".1", ".2", ".3"} {
		if _, err := os.Stat(p + s); err != nil {
			t.Errorf("missing %q: %v", s, err)
		}
	}
	if _, err := os.Stat(p + ".4"); err == nil {
		t.Error(".4 must not exist")
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "j") {
		t.Errorf("current file should hold newest line, got %q", b)
	}
	b, _ = os.ReadFile(p + ".3")
	if strings.HasPrefix(string(b), "a") {
		t.Error("oldest data should have been dropped")
	}
}

func TestAppendsToExistingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(p, []byte("old\n"), 0o644)
	w, err := New(p, 1000, 2)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("new\n"))
	w.Close()
	b, _ := os.ReadFile(p)
	if string(b) != "old\nnew\n" {
		t.Errorf("got %q", b)
	}
}

func TestOpenFailure(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	os.WriteFile(f, nil, 0o644)
	if _, err := New(filepath.Join(f, "dir", "x.log"), 100, 2); err == nil {
		t.Error("expected error")
	}
}

func TestConcurrentWrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.log")
	w, _ := New(p, 500, 2)
	defer w.Close()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				w.Write([]byte("0123456789 concurrent line\n"))
			}
		}()
	}
	wg.Wait()
}
