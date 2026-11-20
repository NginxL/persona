package rpc

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// frameReader reads a framed connection's lines (lanes.go) for Serve, as a lineReader reads plain ones: Scan, Bytes,
// Owned (a line put together from several frames is in a buffer of its own), Err (nil at io.EOF on a frame's edge).
// Acknowledgements are handed to the writer as they come (acked); before it waits for more input, the reader asks the
// writer to acknowledge the frames with credit it has read (ack, creditLane), so a reader held up by its own side stops
// the other side's interactive and bulk frames once its window is full.
type frameReader struct {
	r          io.Reader
	max        int
	buf        []byte
	start, end int
	err        error
	seq        uint64               // the last data frame read
	seqAt      uint32               // when it was read (clockMS)
	lastRead   atomic.Uint64        // seq, for a resume asked from another goroutine (resume.go)
	resumed    bool                 // the transport was replaced: frames read already come again, and are dropped
	creditRead bool                 // a frame with credit was read since the last ack asked for
	creditSeq  uint64               // the last of them
	creditAt   uint32               // when it was read (clockMS)
	readAt     uint32               // when the latest read came in (clockMS)
	partial    map[uint32]*partLine // lines in progress, by lane<<16 | slot
	held       int                  // their bytes
	reads      int                  // reads made so far
	middleLine uint64               // the first frame of the line whose middle counted last (holding)
	marks      []int                // where in buf frames read and not yet taken show an answer ahead (aheadActivity)
	token      []byte
	owned      bool
	ack        func(seq uint64, at uint32)       // ask for an acknowledgement of every frame read through seq, read at at
	acked      func(seq uint64, at uint32) error // the other side read every frame this side sent through seq, the last at at
	ackAll     func(seq uint64, at uint32)       // resuming: ask for an acknowledgement of every frame read through seq
	// middle, when set, is told of each read that went on with a line begun before it, did not finish it and has
	// already grown past pingFrameMiddleBytes, as lineReader.middle is (PingActivity).
	middle func()
}

// maxPartialLines bounds the lines a reader puts together at once: a writer has at most laneSlots per lane.
const maxPartialLines = int(laneCount) * laneSlots

// frameReadBuf holds two whole frames: a read rarely stops at a frame's edge.
const frameReadBuf = 2 * frameMax

func newFrameReader(r io.Reader, maximum int) *frameReader {
	return &frameReader{r: r, max: maximum, buf: make([]byte, frameReadBuf), partial: map[uint32]*partLine{}}
}

func (s *frameReader) Bytes() []byte { return s.token }
func (s *frameReader) Owned() bool   { return s.owned }

func (s *frameReader) Err() error {
	if s.err == io.EOF {
		return nil
	}
	return s.err
}

func (s *frameReader) setErr(err error) {
	if s.err == nil || s.err == io.EOF {
		s.err = err
	}
}

// Scan finds the next whole line.
func (s *frameReader) Scan() bool {
	s.token, s.owned = nil, false
	for {
		if s.end-s.start >= frameHeader {
			h, err := parseHead(s.buf[s.start:])
			if err != nil {
				s.setErr(err)
				return false
			}
			if s.end-s.start >= frameHeader+h.size {
				payload := s.buf[s.start+frameHeader : s.start+frameHeader+h.size]
				s.start += frameHeader + h.size
				line, ok, err := s.frame(h, payload)
				if err != nil {
					s.setErr(err)
					return false
				}
				if ok {
					s.token = line
					return true
				}
				continue
			}
		}
		if s.err != nil {
			if s.err == io.EOF && s.end > s.start {
				s.err = fmt.Errorf("%w: the connection ended inside a frame", io.ErrUnexpectedEOF)
			}
			s.token = nil
			return false
		}
		s.fill()
	}
}

// frame takes one frame; a line it finishes is returned, owned when it was put together.
func (s *frameReader) frame(h frameHead, payload []byte) ([]byte, bool, error) {
	if h.kind == frameAck {
		if s.acked == nil {
			return nil, false, nil
		}
		return nil, false, s.acked(h.seq, binary.BigEndian.Uint32(payload))
	}
	if s.resumed && h.seq <= s.seq {
		return nil, false, nil // written again after a resume, read already over the old transport
	}
	if h.seq != s.seq+1 {
		return nil, false, fmt.Errorf("%w: frame %d after %d", ErrFrame, h.seq, s.seq)
	}
	s.resumed = false
	s.seq, s.seqAt = h.seq, s.readAt
	s.lastRead.Store(h.seq)
	if s.ackAll != nil {
		// Resuming: as each frame is taken, not before the next read, so a read loop slow over its lines (a handler
		// that blocks it) is not taken for a silent transport by the other side (watchStale).
		s.ackAll(s.seq, s.seqAt)
	}
	if creditLane(h.lane) {
		s.creditRead, s.creditSeq, s.creditAt = true, h.seq, s.readAt
	}
	key := frameKey(h)
	part, going := s.partial[key]
	// A line as long as the limit allows, and the lines in progress beside it at most as much again together: with one
	// long line at a time on each lane (pickLocked), lines that size seldom overlap.
	if going && part.n+h.size > s.max || !going && h.size > s.max || !h.fin && s.held+h.size > 2*s.max {
		return nil, false, bufio.ErrTooLong
	}
	if !going {
		if h.fin {
			return payload, true, nil // in the read buffer: valid until the next Scan, as a short line's is
		}
		if len(s.partial) >= maxPartialLines {
			return nil, false, fmt.Errorf("%w: more than %d lines in progress", ErrFrame, maxPartialLines)
		}
		part = &partLine{first: h.seq, read: s.reads, reply: replyEnvelope(payload)}
		s.partial[key] = part
	}
	if h.fin {
		delete(s.partial, key)
		s.held -= part.n
		s.owned = true
		return part.join(payload), true, nil
	}
	part.add(payload)
	s.held += h.size
	return nil, false, nil
}

func frameKey(h frameHead) uint32 { return uint32(h.lane)<<16 | uint32(h.slot) }

// partLine is a line being put together from its frames, in pieces taken from grownBufs and joined once in a buffer of
// the line's size, which is the line's own (as lineReader puts a long line together): a line grown by append would be
// copied each time it outgrew its buffer, and keep up to as much again of room for as long as its params are kept.
type partLine struct {
	pieces [][]byte
	n      int
	first  uint64 // its first frame's sequence number: which line it is, a slot carrying one line after another
	read   int    // the read its first frame came in
	// reply: its first frame opens an answer (replyEnvelope), which aheadActivity takes for the whole line rather than
	// put the line together to decode it (a deadline, the one answer that does not count, never takes two frames).
	reply bool
}

// replyEnvelope: first, the start of a line, opens an answer to a call: an id and then a result or an error, in the
// order message's fields put them. The deadline answer that does not count (deadlineMessage) is never told apart here:
// it is one short frame (no code, its message alone), so a line of several frames is not one.
func replyEnvelope(first []byte) bool {
	if e := envelopeOf(first); e != envUnknown {
		return e == envResult || e == envError
	}
	dec := jsontext.NewDecoder(bytes.NewReader(first))
	if t, err := dec.ReadToken(); err != nil || t.Kind() != '{' {
		return false
	}
	if t, err := dec.ReadToken(); err != nil || t.String() != "id" {
		return false
	}
	if _, err := dec.ReadValue(); err != nil {
		return false
	}
	t, err := dec.ReadToken()
	return err == nil && (t.String() == "result" || t.String() == "error")
}

// partPiece is the size of a partLine's pieces: grownBufs' smallest.
const partPiece = lineStart << 2

func (p *partLine) add(b []byte) {
	for len(b) > 0 {
		k := len(p.pieces) - 1
		if k < 0 || len(p.pieces[k]) == cap(p.pieces[k]) {
			p.pieces = append(p.pieces, takeGrown(partPiece)[:0])
			k++
		}
		c := min(len(b), cap(p.pieces[k])-len(p.pieces[k]))
		p.pieces[k] = append(p.pieces[k], b[:c]...)
		p.n += c
		b = b[c:]
	}
}

// join is the whole line, tail its last piece; the pieces go back to grownBufs.
func (p *partLine) join(tail []byte) []byte {
	line := make([]byte, 0, p.n+len(tail))
	for _, piece := range p.pieces {
		line = append(line, piece...)
		giveGrown(piece[:cap(piece)])
	}
	p.pieces = nil
	return append(line, tail...)
}

// fill reads more input, after asking for an acknowledgement of the frames with credit read.
func (s *frameReader) fill() {
	carried := s.end > s.start // a frame begun in an earlier read: this one goes on with it
	if s.creditRead && s.ack != nil {
		// Through the last frame with credit, not every frame read: its time is what the other side measures by.
		s.ack(s.creditSeq, s.creditAt)
		s.creditRead = false
	}
	if s.start > 0 && (s.start == s.end || len(s.buf)-s.end < frameMax) {
		copy(s.buf, s.buf[s.start:s.end])
		for i := range s.marks { // they move with the bytes; the ones behind start go negative and drop out
			s.marks[i] -= s.start
		}
		s.end -= s.start
		s.start = 0
	}
	for loop := 0; ; {
		n, err := s.r.Read(s.buf[s.end:])
		if n < 0 || len(s.buf)-s.end < n {
			s.setErr(bufio.ErrBadReadCount)
			return
		}
		if err == errResumed {
			// A new transport: the part of a frame read from the old one comes again whole, after what this side read.
			s.end = s.start
			s.marks = s.marks[:0]
			s.resumed, carried = true, false
			continue
		}
		s.end += n
		if n > 0 { // a read that ends the input may bring the last frames too
			s.reads++
			s.readAt = clockMS(time.Now())
			s.scan(carried)
		}
		if err != nil {
			s.setErr(err)
			return
		}
		if n > 0 {
			return
		}
		loop++
		if loop > 100 {
			s.setErr(io.ErrNoProgress)
			return
		}
	}
}

// aheadLine is what is known of a line from the frames read and not yet taken (frameReader.ahead).
type aheadLine struct {
	key   uint32
	start uint64 // its first frame's sequence number
	size  int    // its bytes so far, those before these frames included
	first bool   // its first frame is among these
	lead  bool   // the first of these frames is its
	ended bool   // its last frame is among these, whole
	reply bool   // its first frame opens an answer (replyEnvelope)
}

// envelope is what a line is, as its first bytes say.
type envelope uint8

const (
	envUnknown envelope = iota
	envEvent
	envRequest
	envResult
	envError
)

var (
	envEventHead, envIDHead                 = []byte(`{"event":`), []byte(`{"id":"`)
	envMethodKey, envResultKey, envErrorKey = []byte(`,"method":`), []byte(`,"result":`), []byte(`,"error":`)
)

// envelopeOf reads what a line is from its first bytes as message's encoder writes them, with no decoding: an event
// begins {"event":, a request, a result and an error {"id":"…", then "method", "result" or "error". Anything else
// (another encoder's spacing, an id with escapes, bytes cut before the second key) is envUnknown, for a decoder to say.
func envelopeOf(b []byte) envelope {
	if bytes.HasPrefix(b, envEventHead) {
		return envEvent
	}
	if !bytes.HasPrefix(b, envIDHead) {
		return envUnknown
	}
	rest := b[len(envIDHead):]
	i := bytes.IndexByte(rest, '"')
	if i < 0 || bytes.IndexByte(rest[:i], '\\') >= 0 {
		return envUnknown
	}
	switch rest = rest[i+1:]; {
	case bytes.HasPrefix(rest, envMethodKey):
		return envRequest
	case bytes.HasPrefix(rest, envResultKey):
		return envResult
	case bytes.HasPrefix(rest, envErrorKey):
		return envError
	}
	return envUnknown
}

// wholeLineActivity: a whole line read ahead shows activity: an answer but not a deadline, or a line that does not
// decode (bufferedActivity's rule). Events and requests are told by their envelope; only an error, or a line not of
// this package's writing, is decoded.
func wholeLineActivity(b []byte) bool {
	switch envelopeOf(b) {
	case envEvent, envRequest:
		return false
	case envResult:
		return true
	}
	var m message
	return json.Unmarshal(b, &m) != nil || replyMessage(m) && !deadlineMessage(m)
}

// startsAnswer: the first piece of a line opens an answer with a result (bufferedActivity's rule for a line not
// whole yet: an error may still be a deadline).
func startsAnswer(b []byte) bool {
	switch envelopeOf(b) {
	case envResult:
		return true
	case envUnknown:
		return bufferedActivity(b)
	}
	return false
}

// ahead walks the frames read and not yet taken (s.buf[s.start:s.end]), the last perhaps in part, calling f, when set,
// for each data frame with where it begins in buf, the part of its payload there and its line as known so far; it
// returns those lines. A piece of a line is never taken for a line of its own: the line of a piece that comes after
// its first one is the one begun before. Lines are few (one long line a lane, short ones end in their frame), so a
// slice searched from the end does.
func (s *frameReader) ahead(f func(at int, h frameHead, payload []byte, l *aheadLine)) []aheadLine {
	var lines []aheadLine
	at := s.start
	for lead := true; s.end-at >= frameHeader; lead = false {
		b := s.buf[at:s.end]
		h, err := parseHead(b)
		if err != nil {
			break
		}
		have := min(len(b)-frameHeader, h.size)
		if h.kind == frameData {
			key := frameKey(h)
			i, seen := -1, false
			for j := len(lines) - 1; j >= 0; j-- {
				if lines[j].key == key {
					seen = true
					if !lines[j].ended {
						i = j
					}
					break
				}
			}
			if i < 0 {
				l := aheadLine{key: key, start: h.seq, first: true, lead: lead, reply: replyEnvelope(b[frameHeader : frameHeader+have])}
				// A slot whose line ended among these frames carries a new one; otherwise it goes on with the line in
				// progress there.
				if part, ok := s.partial[key]; ok && !seen {
					l.start, l.first, l.size, l.reply = part.first, false, part.n, part.reply
				}
				lines = append(lines, l)
				i = len(lines) - 1
			}
			l := &lines[i]
			l.size += have
			l.ended = have == h.size && h.fin
			if f != nil {
				f(at, h, b[frameHeader:frameHeader+have], l)
			}
		}
		if have < h.size {
			break
		}
		at += frameHeader + h.size
	}
	return lines
}

// scan looks at what was read and not yet taken once a read came in, once: the frames that show an answer ahead of the
// lines before them (marks, for aheadActivity), and whether this read (carried: it went on with a frame begun before
// it) went on with a line begun before it, did not finish it, and found it past pingFrameMiddleBytes: lineReader's
// middle, read for read, whatever frames the line came in.
func (s *frameReader) scan(carried bool) {
	s.marks = s.marks[:0]
	lines := s.ahead(func(at int, h frameHead, payload []byte, l *aheadLine) {
		var mark bool
		switch {
		case !l.first || l.size != len(payload): // a later piece: the line counts once whole, by its first frame
			mark = l.ended && l.reply
		case l.ended: // a whole line in one frame
			mark = wholeLineActivity(payload)
		default: // the first piece of a longer line
			mark = startsAnswer(payload)
		}
		if mark {
			s.marks = append(s.marks, at)
		}
	})
	if s.middle == nil {
		return
	}
	for _, l := range lines {
		if (!l.first || l.lead && carried) && !l.ended && l.size > pingFrameMiddleBytes {
			s.middleLine = l.start
			s.middle()
			return
		}
	}
}

// aheadActivity is bufferedActivity for frames: among the frames already read after the current line, a whole line
// that is a reply but not a deadline (or that does not decode), or the first piece of a line whose envelope names a
// result and no method or event; for a line in several frames that is all its first frame says (a deadline is never
// that long), and its later pieces say nothing of their own. What scan found when the read came in, less what was
// taken since.
func (s *frameReader) aheadActivity() bool {
	for _, p := range s.partial {
		if p.reply && p.read == s.reads {
			return true // an answer begun in this read, taken in already: with lines its start would be ahead
		}
	}
	for len(s.marks) > 0 && s.marks[0] < s.start {
		s.marks = s.marks[1:]
	}
	return len(s.marks) > 0
}

// holding: the line whose middle counted last is still arriving, so that activity stands (Serve). That line only: one
// held back (by credit the other side never gets) must not keep the middles of lines that finished alive.
func (s *frameReader) holding() bool {
	if s.middleLine == 0 {
		return false
	}
	for _, p := range s.partial {
		if p.first == s.middleLine {
			return true
		}
	}
	if s.end-s.start >= frameHeader { // its first frame still coming in
		h, err := parseHead(s.buf[s.start:])
		return err == nil && h.kind == frameData && h.seq == s.middleLine
	}
	return false
}
