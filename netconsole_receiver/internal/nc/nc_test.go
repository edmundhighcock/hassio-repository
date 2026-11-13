package nc

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseExtendedHeader(t *testing.T) {
	r := Parse([]byte("12,607,22085407756,-;This is a message"))
	if !r.Extended || r.Level != 12 || r.Seq != 607 || r.TimeUS != 22085407756 || r.Flag != '-' || r.Release != "" {
		t.Fatalf("bad parse: %+v", r)
	}
	if r.Text != "This is a message" || r.Fragmented {
		t.Fatalf("bad text/frag: %+v", r)
	}
}

func TestParseReleasePrefix(t *testing.T) {
	r := Parse([]byte("6.4.0,6,444,501151268,-;netconsole: network logging started"))
	if !r.Extended || r.Release != "6.4.0" || r.Level != 6 || r.Seq != 444 || r.TimeUS != 501151268 {
		t.Fatalf("bad parse: %+v", r)
	}
	if r.Message() != "netconsole: network logging started" {
		t.Fatalf("text %q", r.Text)
	}
}

func TestParseContinuationAndDictionary(t *testing.T) {
	r := Parse([]byte("13,434,54928466,c;This is message #1\n foo=bar\n msgid=7\n taskname=echo"))
	if r.Flag != 'c' {
		t.Fatalf("flag %c", r.Flag)
	}
	if r.Message() != "This is message #1" {
		t.Fatalf("message %q", r.Message())
	}
	ud := r.UserData()
	if len(ud) != 3 || ud[0] != [2]string{"foo", "bar"} || ud[2] != [2]string{"taskname", "echo"} {
		t.Fatalf("userdata %v", ud)
	}
	if id, ok := r.MsgID(); !ok || id != 7 {
		t.Fatalf("msgid %d %v", id, ok)
	}
}

func TestParsePlainLines(t *testing.T) {
	for _, in := range []string{
		"just a plain line\n",
		"a;b;c",
		"x,1,2,-;not numeric level",
		"1,2;too few fields",
		"1,2,3,q;bad flag",
	} {
		r := Parse([]byte(in))
		if r.Extended {
			t.Errorf("%q parsed as extended: %+v", in, r)
		}
		if r.Text != strings.TrimRight(in, "\r\n") {
			t.Errorf("%q stored as %q", in, r.Text)
		}
	}
}

func TestParseNcfragHeader(t *testing.T) {
	r := Parse([]byte("6,416,1758426,-,ncfrag=16/31; the 2nd chunk."))
	if !r.Extended || !r.Fragmented || r.FragOff != 16 || r.FragTotal != 31 || r.Text != " the 2nd chunk." {
		t.Fatalf("%+v", r)
	}
	// forged huge total is ignored rather than trusted
	r = Parse([]byte("6,416,1758426,-,ncfrag=0/99999999999;x"))
	if r.Fragmented {
		t.Fatalf("huge total accepted: %+v", r)
	}
	// release + ncfrag together
	r = Parse([]byte("6.4.0,6,416,1758426,-,ncfrag=0/31;abc"))
	if !r.Fragmented || r.Release != "6.4.0" || r.Seq != 416 {
		t.Fatalf("%+v", r)
	}
}

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func TestReassemblyInOrderAndOutOfOrder(t *testing.T) {
	for _, order := range [][]int{{0, 1}, {1, 0}} {
		ra := NewReassembler(3 * time.Second)
		frags := []string{
			"6,416,1758426,-,ncfrag=0/31;the first chunk,",
			"6,416,1758426,-,ncfrag=16/31; the 2nd chunk.",
		}
		var got Record
		var done bool
		for n, i := range order {
			got, done = ra.Add("1.2.3.4:5", Parse([]byte(frags[i])), t0)
			if done != (n == 1) {
				t.Fatalf("order %v step %d done=%v", order, n, done)
			}
		}
		if got.Text != "the first chunk, the 2nd chunk." || got.Fragmented || got.Seq != 416 {
			t.Fatalf("order %v: %+v", order, got)
		}
		if ra.Pending() != 0 {
			t.Fatal("pending not cleared")
		}
	}
}

func TestReassemblyKeepsDictionaryAfterTotal(t *testing.T) {
	ra := NewReassembler(time.Second)
	ra.Add("s", Parse([]byte("6,9,100,-,ncfrag=0/10;01234")), t0)
	got, done := ra.Add("s", Parse([]byte("6,9,100,-,ncfrag=5/10;56789\n msgid=3")), t0)
	if !done || got.Text != "0123456789\n msgid=3" {
		t.Fatalf("%v %q", done, got.Text)
	}
	if id, ok := got.MsgID(); !ok || id != 3 {
		t.Fatal("msgid lost")
	}
}

func TestReassemblySeparatesSources(t *testing.T) {
	ra := NewReassembler(time.Second)
	ra.Add("a", Parse([]byte("6,1,1,-,ncfrag=0/4;ab")), t0)
	if _, done := ra.Add("b", Parse([]byte("6,1,1,-,ncfrag=2/4;cd")), t0); done {
		t.Fatal("fragments of different sources were joined")
	}
}

func TestReassemblyTimeout(t *testing.T) {
	ra := NewReassembler(3 * time.Second)
	ra.Add("s", Parse([]byte("6,5,50,-,ncfrag=0/20;first half ")), t0)
	if x := ra.Expire(t0.Add(2 * time.Second)); len(x) != 0 {
		t.Fatalf("expired early: %v", x)
	}
	x := ra.Expire(t0.Add(3 * time.Second))
	if len(x) != 1 || x[0].Src != "s" || x[0].Have != 11 || x[0].Total != 20 || !strings.Contains(x[0].Rec.Text, "first half ") ||
		!strings.Contains(x[0].Rec.Text, "[ncfrag-missing]") {
		t.Fatalf("%+v", x)
	}
	if ra.Pending() != 0 || len(ra.Expire(t0.Add(time.Hour))) != 0 {
		t.Fatal("expired message not forgotten")
	}
}

func TestReassemblyBoundedPerSource(t *testing.T) {
	ra := NewReassembler(time.Hour)
	for i := 0; i < maxPending+10; i++ {
		ra.Add("s", Parse([]byte("6,1,"+strconv.Itoa(i)+",-,ncfrag=0/10;ab")), t0.Add(time.Duration(i)*time.Millisecond))
	}
	if ra.Pending() > maxPending {
		t.Fatalf("pending %d", ra.Pending())
	}
	if x := ra.Expire(t0); len(x) != 10 {
		t.Fatalf("evicted %d, want 10", len(x))
	}
}
