// Package nc parses Linux netconsole datagrams.
//
// Formats are taken from the kernel documentation,
// Documentation/networking/netconsole.rst
// (https://docs.kernel.org/networking/netconsole.html):
//
//   - Extended console ("+" prefix / configfs extended=1): the datagram is
//     "<level>,<sequnum>,<timestamp>,<contflag>[,<extra>...];<message text>".
//     The header is the same as /dev/kmsg's.
//   - With the release feature ("r" / configfs release=1) the kernel
//     version is PREPENDED to the header:
//     "6.4.0,6,444,501151268,-;netconsole: network logging started".
//   - Non-printable characters in the text are already escaped by the
//     kernel using "\xff" notation. If the message carries a dictionary
//     (userdata), a verbatim newline is the delimiter and every dictionary
//     line starts with a space: "12,607,22085407756,-;text\n foo=bar\n qux=baz".
//   - A message that does not fit the chunk size (currently 1000 bytes) is
//     split. Each fragment's header gets an extra "ncfrag=<byte-offset>/<total-bytes>"
//     field: "6,416,1758426,-,ncfrag=0/31;the first chunk," and
//     "6,416,1758426,-,ncfrag=16/31; the 2nd chunk.".
//   - The msgid_enabled userdata feature adds " msgid=<n>", a per-target
//     32-bit counter that wraps; unlike <sequnum> it counts only messages
//     actually transmitted by netconsole.
//   - A non-extended target sends bare text lines with no header.
package nc

import (
	"strconv"
	"strings"
)

// Record is one parsed datagram.
type Record struct {
	Extended bool   // true when a valid extended header was found
	Release  string // kernel release when the "r" feature is on, else ""
	Level    int
	Seq      uint64
	TimeUS   int64
	Flag     byte // '-', 'c' (continuation), '+', 'f'
	// Fragmented is true when the header carried ncfrag=<FragOff>/<FragTotal>.
	Fragmented bool
	FragOff    int
	FragTotal  int
	// Text is everything after the header's ';' (message text plus any
	// dictionary lines), or the whole datagram for plain lines.
	Text string
}

func isFlag(s string) bool {
	return len(s) == 1 && strings.IndexByte("-+cf", s[0]) >= 0
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Parse classifies one datagram. A datagram that does not start with a
// valid extended header is returned as a plain line (Extended=false) with
// trailing CR/LF trimmed and no sequence information.
func Parse(b []byte) Record {
	s := string(b)
	if i := strings.IndexByte(s, ';'); i > 0 {
		if rec, ok := parseHeader(s[:i]); ok {
			rec.Extended = true
			rec.Text = s[i+1:]
			return rec
		}
	}
	return Record{Text: strings.TrimRight(s, "\r\n")}
}

func parseHeader(h string) (Record, bool) {
	f := strings.Split(h, ",")
	var rec Record
	// Without release: level,seq,ts,flag[,extra...]
	// With release:    release,level,seq,ts,flag[,extra...]
	base := -1
	switch {
	case len(f) >= 4 && allDigits(f[0]) && allDigits(f[1]) && allDigits(f[2]) && isFlag(f[3]):
		base = 0
	case len(f) >= 5 && f[0] != "" && allDigits(f[1]) && allDigits(f[2]) && allDigits(f[3]) && isFlag(f[4]):
		base = 1
		rec.Release = f[0]
	default:
		return rec, false
	}
	lvl, _ := strconv.Atoi(f[base])
	seq, err1 := strconv.ParseUint(f[base+1], 10, 64)
	ts, err2 := strconv.ParseInt(f[base+2], 10, 64)
	if err1 != nil || err2 != nil {
		return rec, false
	}
	rec.Level, rec.Seq, rec.TimeUS, rec.Flag = lvl, seq, ts, f[base+3][0]
	for _, x := range f[base+4:] {
		if v, ok := strings.CutPrefix(x, "ncfrag="); ok {
			o, t, ok2 := strings.Cut(v, "/")
			off, e1 := strconv.Atoi(o)
			tot, e2 := strconv.Atoi(t)
			if ok2 && e1 == nil && e2 == nil && off >= 0 && tot > 0 && tot <= MaxFragTotal {
				rec.Fragmented, rec.FragOff, rec.FragTotal = true, off, tot
			}
		}
	}
	return rec, true
}

// Message is the first line of Text (the kernel message proper).
func (r Record) Message() string {
	if i := strings.IndexByte(r.Text, '\n'); i >= 0 {
		return r.Text[:i]
	}
	return r.Text
}

// UserData returns the dictionary lines (the lines after the first
// newline that start with a space) as key, value pairs in order.
func (r Record) UserData() [][2]string {
	if !r.Extended {
		return nil
	}
	i := strings.IndexByte(r.Text, '\n')
	if i < 0 {
		return nil
	}
	var out [][2]string
	for _, l := range strings.Split(r.Text[i+1:], "\n") {
		if !strings.HasPrefix(l, " ") {
			continue
		}
		k, v, ok := strings.Cut(l[1:], "=")
		if ok {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}

// MsgID returns the value of the LAST msgid=<n> userdata entry, if any.
func (r Record) MsgID() (uint64, bool) {
	var id uint64
	found := false
	for _, kv := range r.UserData() {
		if kv[0] == "msgid" {
			if n, err := strconv.ParseUint(kv[1], 10, 64); err == nil {
				id, found = n, true
			}
		}
	}
	return id, found
}
