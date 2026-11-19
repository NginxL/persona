package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

// lineSource is what Serve reads lines from: a lineReader, or a frameReader once the other side's output is framed.
type lineSource interface {
	Scan() bool
	Bytes() []byte
	Owned() bool
	Err() error
	// aheadActivity: input already read after the current line holds a reply, or the start of one (PingActivity).
	aheadActivity() bool
	// holding: a long line whose middle counted is still arriving (always false for lines: one arrives at a time).
	holding() bool
}

func (s *lineReader) aheadActivity() bool { return bufferedActivity(s.buffered()) }
func (s *lineReader) holding() bool       { return false }

// lanesMarker is the params of the asking side's last line.
var lanesMarker = json.RawMessage(fmt.Sprintf(`{"version":%d}`, lanesVersion))

// EnableLanes lets this peer frame its connection (lanes.go): it answers the other side's rpc.lanes itself (Serve, before
// OnRequest), and UpgradeLanes may ask for it. control and bulk name the methods (a request and its answer) and events
// that go on those lanes; other requests and answers go interactive, other events on the events lane, rpc.cancel on
// control. Call it before Serve or any call. Without it rpc.lanes is an ordinary request, and nothing ever changes on
// the wire: extensions, connectors and runtimes speak this protocol too.
func (p *Peer) EnableLanes(control, bulk []string) {
	p.lanes = newLaneTable(control, bulk)
	p.lanesUp = make(chan struct{})
}

// UpgradeLanes asks the other side to frame the connection, and returns once both directions are framed. An error
// says it did not happen: the other side refused (an older peer does not know rpc.lanes), or ctx ended or the
// connection closed first; lines go on as before. The other side's answer switches this side whenever it comes, its
// caller gone or not, so an answer after ctx ended still frames the connection.
func (p *Peer) UpgradeLanes(ctx context.Context) error {
	if p.lanes == nil {
		return errors.New("rpc: lanes not enabled")
	}
	id := fmt.Sprintf("%s%d", p.prefix, p.nextID.Add(1))
	ch := make(chan message, 1)
	p.mu.Lock()
	if !p.unnegotiatedLocked() {
		p.mu.Unlock()
		return errors.New("rpc: lanes already negotiated")
	}
	p.lanesCall = id
	p.pending[id] = ch
	p.methods[id] = lanesMethod
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		delete(p.methods, id)
		p.mu.Unlock()
	}()
	req := lanesRequest{Versions: []int{lanesVersion}}
	p.mu.Lock()
	if p.resume != nil {
		req.Resume = 1 // resume.go: the other side grants it with its answer, or ignores it
	}
	p.mu.Unlock()
	params, err := marshal(req)
	if err != nil {
		return err
	}
	if _, err := p.writeContextObserved(ctx, message{ID: id, Method: lanesMethod, Params: params}, controlRoute); err != nil {
		p.mu.Lock()
		if p.lanesCall == id {
			p.lanesCall = ""
		}
		p.mu.Unlock()
		return err
	}
	select {
	case m := <-ch:
		if !lanesAccepted(m) {
			if m.Error != nil {
				return m.Error
			}
			return fmt.Errorf("rpc: unexpected answer to %s: %s", lanesMethod, m.Result)
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closed:
		return ErrClosed
	}
	select {
	case <-p.lanesUp:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closed:
		return ErrClosed
	}
}

// Framed: this side's output is framed (lanes.go).
func (p *Peer) Framed() bool { return p.out.Load() != nil }

// LaneStats is the frame writer's counters; zero (Framed false) while the output is lines.
func (p *Peer) LaneStats() LaneStats {
	if lw := p.out.Load(); lw != nil {
		return lw.stats()
	}
	return LaneStats{}
}

// answerLanes answers the other side's rpc.lanes: refused, or accepted with this side's last line, after which its
// output is framed. Run from Serve in a goroutine of its own: the answer waits for a line being written.
func (p *Peer) answerLanes(m message) {
	var req lanesRequest
	ok := json.Unmarshal(m.Params, &req) == nil && slices.Contains(req.Versions, lanesVersion)
	p.mu.Lock()
	// Once only, and not while this side asks itself: both asking at once keep lines.
	ok = ok && p.unnegotiatedLocked()
	answer := lanesAnswer{Version: lanesVersion}
	if ok {
		p.lanesAsked = true
		if token := p.grantResumeLocked(req.Resume == 1); token != "" {
			answer.Resume = &resumeGrant{Token: token}
		}
	}
	p.mu.Unlock()
	if !ok {
		_ = p.write(message{ID: m.ID, Error: &Error{Message: "rpc: lanes refused", Code: CodeLanesRefused}}, controlRoute)
		return
	}
	result, _ := marshal(answer)
	p.switchOutput(message{ID: m.ID, Result: result})
}

// unnegotiatedLocked: neither side asked for lanes yet, nor did either direction switch (p.mu held).
func (p *Peer) unnegotiatedLocked() bool { return p.lanesCall == "" && !p.lanesAsked && p.lw == nil }

// answeredLanes: this side accepted the other side's rpc.lanes, so its marker line is the last before its frames.
func (p *Peer) answeredLanes() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lanesAsked
}

// frameWriter is the connection's frame writer, made when the first direction switches: the reader takes
// acknowledgements and asks for them on it before this side's output is framed (the asking side reads frames first).
func (p *Peer) frameWriter() *laneWriter {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lw == nil {
		p.lw = newLaneWriter(p.w, p.closed, p.Close, p.LineFilter)
		p.lw.wrote = &p.lastWrite
		p.lw.keep = p.resumeOnLocked() // resume.go: agreed before either side's first frame
		p.lw.lose = p.lose
	}
	return p.lw
}

// switchOutput writes last, this side's last line, and frames everything written after it. It holds the writer while it
// does, so a line is either before last, whole, or a writer waiting behind it finds the output framed (writeContextObserved).
func (p *Peer) switchOutput(last message) {
	b, err := encodeLine(last)
	if err != nil {
		p.Close(err)
		return
	}
	select {
	case p.wsem <- struct{}{}:
	case <-p.closed:
		return
	}
	defer func() { <-p.wsem }()
	if p.out.Load() != nil {
		return
	}
	select {
	case <-p.closed:
		return
	default:
	}
	if err := p.writeLine(b); err != nil {
		p.Close(err)
		return
	}
	lw := p.frameWriter()
	lw.start()
	p.out.Store(lw)
	close(p.lanesUp)
	if p.OnLanes != nil {
		p.OnLanes()
	}
}

// framedInput is the reader for what follows the other side's last line: the bytes lr read past it, then the rest.
func (p *Peer) framedInput(lr *lineReader, act *readActivity, maximum int) *frameReader {
	var rest io.Reader = act
	if lr.err != nil { // lr read to the end already: what is left is what it buffered, then its error
		rest = errReader{lr.err}
	}
	lw := p.frameWriter()
	fr := newFrameReader(io.MultiReader(bytes.NewReader(bytes.Clone(lr.buffered())), rest), maximum)
	fr.ack = lw.ack
	fr.acked = lw.acked
	fr.middle = lr.middle
	p.mu.Lock()
	if rs := p.resume; rs != nil && rs.on && lr.err == nil {
		// resume.go: what follows is read through a transport a resume replaces, every frame acknowledged.
		rs.sr, rs.fr = newSwapReader(p, act.r), fr
		act.r = rs.sr
		fr.ackAll = lw.ackAll
		go p.watchStale(resumeStale, resumeCheck)
	}
	p.mu.Unlock()
	return fr
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// writeFramed queues line b on lane and waits until all of it is written. Until its first frame goes out it can be
// withdrawn (NotSent); once begun, it is written to its end.
func (p *Peer) writeFramed(ctx context.Context, lw *laneWriter, b []byte, r route) (writeObservation, error) {
	var observation writeObservation
	if err := ctx.Err(); err != nil {
		return observation, NotSent(err)
	}
	l := &outLine{b: b, lane: r.lane, credit: r.credit, queued: time.Now(), done: make(chan error, 1)}
	observation, err := lw.enqueue(l)
	if err != nil {
		return observation, NotSent(err)
	}
	select {
	case err = <-l.done:
	case <-ctx.Done():
		if lw.withdraw(l) {
			return observation, NotSent(ctx.Err())
		}
		err = <-l.done
	case <-p.closed:
		if lw.withdraw(l) {
			return observation, NotSent(ErrClosed)
		}
		err = <-l.done
	}
	// Once begun, a line's fate is its writes', as a line's on the one writer was: the writer tells it when its last
	// frame is written, or when the writer stops (the peer closed, a write failed), so an accepted update's answer that
	// went out whole while the peer closed is still told to LineWritten.
	if err == nil && p.LineWritten != nil {
		p.LineWritten(b)
	}
	if deadline, ok := ctx.Deadline(); ok {
		observation.remaining = time.Until(deadline)
	}
	return observation, err
}
