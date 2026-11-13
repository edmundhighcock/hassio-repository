package nc

import (
	"sort"
	"strings"
	"time"
)

// MaxFragTotal bounds the total length a fragment header may claim, so a
// forged ncfrag header cannot make the receiver hold large buffers.
const MaxFragTotal = 1 << 20

// maxPending bounds the number of partially received messages per source.
const maxPending = 64

type fragKey struct {
	src string
	seq uint64
	ts  int64
}

type pending struct {
	first   time.Time
	hdr     Record
	total   int
	frags   map[int]string // byte offset -> fragment text
	arrived int
}

// Expired is a message whose remaining fragments never arrived.
type Expired struct {
	Src    string
	Rec    Record // Text holds the fragments that did arrive, gaps marked
	Have   int    // contiguous-or-not bytes received
	Total  int
	Missed bool
}

// Reassembler joins ncfrag fragments per source. Not safe for concurrent
// use; the caller serialises.
type Reassembler struct {
	Timeout  time.Duration
	pend     map[fragKey]*pending
	perSrc   map[string]int
	overflow []Expired
}

func NewReassembler(timeout time.Duration) *Reassembler {
	return &Reassembler{Timeout: timeout, pend: map[fragKey]*pending{}, perSrc: map[string]int{}}
}

// Pending returns the number of incomplete messages held.
func (r *Reassembler) Pending() int { return len(r.pend) }

// Add offers a fragment (rec.Fragmented must be true). When this fragment
// completes the message, the assembled record (header of the first
// fragment seen, Fragmented=false, Text joined) is returned with done=true.
func (r *Reassembler) Add(src string, rec Record, now time.Time) (Record, bool) {
	k := fragKey{src, rec.Seq, rec.TimeUS}
	p := r.pend[k]
	if p == nil {
		if r.perSrc[src] >= maxPending {
			r.evictOldest(src)
		}
		p = &pending{first: now, hdr: rec, total: rec.FragTotal, frags: map[int]string{}}
		r.pend[k] = p
		r.perSrc[src]++
	}
	if _, dup := p.frags[rec.FragOff]; !dup {
		p.arrived += len(rec.Text)
	}
	p.frags[rec.FragOff] = rec.Text
	if joined, ok := p.join(); ok {
		delete(r.pend, k)
		r.perSrc[src]--
		out := p.hdr
		out.Fragmented = false
		out.Text = joined
		return out, true
	}
	return Record{}, false
}

// join concatenates the fragments in offset order. ok is true when they
// cover [0, total) without a hole. Fragment text may extend past total
// (the last fragment carries the dictionary after the message text).
func (p *pending) join() (string, bool) {
	offs := make([]int, 0, len(p.frags))
	for o := range p.frags {
		offs = append(offs, o)
	}
	sort.Ints(offs)
	var sb strings.Builder
	pos := 0
	for _, o := range offs {
		t := p.frags[o]
		if o > pos {
			return "", false
		}
		if o+len(t) > pos {
			sb.WriteString(t[pos-o:])
			pos = o + len(t)
		}
	}
	return sb.String(), pos >= p.total
}

func (p *pending) partial() string {
	offs := make([]int, 0, len(p.frags))
	for o := range p.frags {
		offs = append(offs, o)
	}
	sort.Ints(offs)
	var sb strings.Builder
	pos := 0
	for _, o := range offs {
		t := p.frags[o]
		if o > pos {
			sb.WriteString("[ncfrag-missing]")
			pos = o
		}
		if o+len(t) > pos {
			sb.WriteString(t[pos-o:])
			pos = o + len(t)
		}
	}
	if pos < p.total {
		sb.WriteString("[ncfrag-missing]")
	}
	return sb.String()
}

func (r *Reassembler) expire(k fragKey, p *pending) Expired {
	delete(r.pend, k)
	r.perSrc[k.src]--
	out := p.hdr
	out.Fragmented = false
	out.Text = p.partial()
	return Expired{Src: k.src, Rec: out, Have: p.arrived, Total: p.total, Missed: true}
}

func (r *Reassembler) evictOldest(src string) {
	var ok fragKey
	var op *pending
	for k, p := range r.pend {
		if k.src == src && (op == nil || p.first.Before(op.first)) {
			ok, op = k, p
		}
	}
	if op != nil {
		r.overflow = append(r.overflow, r.expire(ok, op))
	}
}

// Expire returns (and forgets) every pending message older than Timeout,
// in arrival order.
func (r *Reassembler) Expire(now time.Time) []Expired {
	out := r.overflow
	r.overflow = nil
	type kp struct {
		k fragKey
		p *pending
	}
	var old []kp
	for k, p := range r.pend {
		if now.Sub(p.first) >= r.Timeout {
			old = append(old, kp{k, p})
		}
	}
	sort.Slice(old, func(i, j int) bool { return old[i].p.first.Before(old[j].p.first) })
	for _, x := range old {
		out = append(out, r.expire(x.k, x.p))
	}
	return out
}
