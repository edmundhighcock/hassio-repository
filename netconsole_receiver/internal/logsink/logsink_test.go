package logsink

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEscape(t *testing.T) {
	cases := map[string]string{
		"plain text":             "plain text",
		"a\nb":                   `a\nb`,
		"a\r\nb\tc":              `a\r\nb\tc`,
		"nul\x00bell\x07del\x7f": `nul\x00bell\x07del\x7f`,
		"bad\xffbyte":            `bad\xffbyte`,
		"utf8 é ok":              "utf8 é ok",
		"c1\u0085ctl":            `c1\xc2\x85ctl`,
		`kernel \x41 stays`:      `kernel \x41 stays`,
		"line sep":               `line\xe2\x80\xa8sep`,
	}
	for in, want := range cases {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(Escape(in), "\n\r") {
			t.Errorf("Escape(%q) still contains a newline", in)
		}
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	r := NewRotator(dir, "box", 100, 3)
	defer r.Close()
	line := strings.Repeat("x", 39) // 40 bytes with newline: 2 lines per file
	for i := 0; i < 10; i++ {
		if err := r.WriteLine(fmt.Sprintf("%02d%s", i, line[:37])); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := filepath.Glob(filepath.Join(dir, "box.log*"))
	if len(ents) != 3 {
		t.Fatalf("want 3 files (live + 2 rotated), got %v", ents)
	}
	for _, e := range ents {
		st, _ := os.Stat(e)
		if st.Size() > 100 {
			t.Errorf("%s is %d bytes, over the 100 limit", e, st.Size())
		}
	}
	live, _ := os.ReadFile(filepath.Join(dir, "box.log"))
	if !strings.HasPrefix(string(live), "08") {
		t.Fatalf("live file should hold the newest lines, got %q", live)
	}
	old, _ := os.ReadFile(filepath.Join(dir, "box.log.2"))
	if !strings.HasPrefix(string(old), "04") {
		t.Fatalf("oldest kept file should start at line 04, got %q", old)
	}
	if _, err := os.Stat(filepath.Join(dir, "box.log.3")); err == nil {
		t.Fatal("more files than log_files kept")
	}
}

func TestRotationSingleFileAndReopen(t *testing.T) {
	dir := t.TempDir()
	r := NewRotator(dir, "one", 50, 1)
	for i := 0; i < 5; i++ {
		r.WriteLine(strings.Repeat("y", 30))
	}
	r.Close()
	b, _ := os.ReadFile(filepath.Join(dir, "one.log"))
	if len(b) > 50 || len(b) == 0 {
		t.Fatalf("size %d", len(b))
	}
	// a new Rotator on the same dir appends (container recreated)
	r2 := NewRotator(dir, "one", 1<<20, 1)
	r2.WriteLine("again")
	r2.Close()
	b2, _ := os.ReadFile(filepath.Join(dir, "one.log"))
	if !strings.HasSuffix(string(b2), "again\n") || len(b2) <= len(b) {
		t.Fatalf("not appended: %q", b2)
	}
}

func TestWriteLineNeverWritesTwoLines(t *testing.T) {
	dir := t.TempDir()
	r := NewRotator(dir, "n", 1<<20, 2)
	r.WriteLine("a\nb")
	r.Close()
	b, _ := os.ReadFile(filepath.Join(dir, "n.log"))
	if strings.Count(string(b), "\n") != 1 {
		t.Fatalf("%q", b)
	}
}
