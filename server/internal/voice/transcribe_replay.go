package voice

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
)

// The final pass (docs/assistant/meeting-minutes.md "The built-in adapter", "The final pass"): the stored audio at
// the job's speed, in windows of fresh verbatim sessions checked against the reference.

// trReplayWin is a final-pass window.
type trReplayWin struct {
	n        int
	from     int64
	cap      int64 // its sessions get no audio past this until to is known
	edgeCut  bool  // cap is where it ends (a reference's edge, a hole)
	to       int64 // -1 until known
	covered  bool  // the reference covers its speech
	aside    bool  // given more sessions, chosen as without a reference: too short to check against, too few agreed
	sessions []*trSession
	opening  int
	state    int
	doneAt   time.Time // every session had its audio
}

const (
	trOpening = iota
	trRunning
	trTail
)

type trRef struct {
	from, to int64
	text     string
}

// replayStep moves the final pass on: a window starts at the next speech, is cut, waits for its sessions, is chosen.
func (j *trJob) replayStep() {
	for !j.over && !j.finishing {
		if j.rw == nil {
			if !j.replayStart() {
				return
			}
		}
		rw := j.rw
		if rw.to < 0 {
			j.replayCut(rw)
		}
		switch rw.state {
		case trOpening:
			return
		case trRunning:
			if rw.to < 0 {
				return
			}
			if j.fake == nil {
				if rw.opening > 0 {
					return
				}
				for _, s := range rw.sessions {
					if s.started && !s.lost && !s.written.Load() {
						return
					}
				}
			} else if j.edge < rw.to {
				return
			}
			rw.state, rw.doneAt = trTail, time.Now()
			fallthrough
		case trTail:
			if !j.replayQuiet(rw) {
				return
			}
			if !j.replayPick(rw) {
				continue // more sessions for the window
			}
			j.rw = nil
		}
	}
}

// replayStart starts the next window at the next speech: the silence before it is a window of its own with nothing
// said, which keeps what the hub has. False: nothing to start yet.
func (j *trJob) replayStart() bool {
	var first [2]int64
	var from, limit int64
	var covered, edge bool
	for {
		found := false
		for _, s := range j.vad.speech(j.nextFrom, j.edge) {
			first, found = s, true
			break
		}
		if !found {
			if j.ending {
				if j.edge > j.nextFrom {
					j.sendWindow(j.nextFrom, j.edge, true)
					j.nextFrom = j.edge
				}
				j.finish()
				return false
			}
			j.buf.drop(j.edge - trSamples(trReplayLead))
			return false
		}
		from = max(j.nextFrom, first[0]-trSamples(trReplayLead))
		for _, h := range j.holes {
			if h[1] > j.nextFrom && h[1] <= first[0] {
				from = max(from, h[1])
			}
		}
		bare := j.bare()
		covered = !inSpans(bare, from)
		limit, edge = j.replayCap(from, covered, bare)
		if !edge || limit > first[0] {
			break
		}
		// The reference starts or stops covering before the speech does: nothing is said up to there.
		j.sendWindow(j.nextFrom, limit, true)
		j.nextFrom = limit
	}
	if from > j.nextFrom {
		j.sendWindow(j.nextFrom, from, true)
		j.nextFrom = from
	}
	rw := &trReplayWin{n: j.replayN, from: from, to: -1, covered: covered, cap: limit, edgeCut: edge}
	j.replayN++
	j.rw = rw
	if j.fake != nil {
		rw.state = trRunning
		return true
	}
	n := trReplayNoRef
	if rw.covered {
		n = trReplayWithRef
	}
	j.replayOpen(rw, n)
	return true
}

// replayOpen opens n fresh sessions for window rw, each told the previous window's last sentences.
func (j *trJob) replayOpen(rw *trReplayWin, n int) {
	ins := j.instructions(trVerbatim, j.prev)
	for range n {
		s := newTrSession(j, trVerbatim, nil, rw.n, "replay")
		rw.sessions = append(rw.sessions, s)
		s.connect(j.ctx, ins)
	}
	rw.opening += n
}

// bare are the spans the reference does not cover, trRefGap or longer, in order (the last one has no end).
func (j *trJob) bare() [][2]int64 {
	var covered [][2]int64
	pad := trSamples(trRefPad)
	for _, r := range j.refs {
		if trHasText(r.text) {
			covered = append(covered, [2]int64{r.from - pad, r.to + pad})
		}
	}
	sort.Slice(covered, func(a, b int) bool { return covered[a][0] < covered[b][0] })
	var out [][2]int64
	at := int64(math.MinInt64 / 2)
	for _, c := range covered {
		if c[0]-at >= trSamples(trRefGap) {
			out = append(out, [2]int64{at, c[0]})
		}
		at = max(at, c[1])
	}
	return append(out, [2]int64{at, math.MaxInt64})
}

func inSpans(spans [][2]int64, at int64) bool {
	for _, s := range spans {
		if s[0] <= at && at < s[1] {
			return true
		}
	}
	return false
}

// replayCap is where a window from from stops for now: trReplayWindow on, or before it where the reference starts or
// stops covering speech or the audio has a hole (edge: it ends there).
func (j *trJob) replayCap(from int64, covered bool, bare [][2]int64) (int64, bool) {
	limit, edge := from+trSamples(trReplayWindow), false
	for _, b := range bare {
		switch {
		case covered && b[0] > from && b[0] < limit:
			limit, edge = b[0], true
		case !covered && b[0] <= from && from < b[1] && b[1] < limit:
			limit, edge = b[1], true
		}
	}
	for _, h := range j.holes {
		if h[0] > from && h[0] < limit {
			limit, edge = h[0], true
		}
	}
	return limit, edge
}

// replayCut decides where window rw ends: at its edge, else at the first silence after its length, at the latest
// trReplayOver after it (or trReplayWindowMost), or where the audio ends.
func (j *trJob) replayCut(rw *trReplayWin) {
	gap := trSamples(trWindowGap)
	switch {
	case rw.edgeCut && j.edge >= rw.cap:
		rw.to = rw.cap
	case !rw.edgeCut && j.edge >= rw.cap:
		if at := j.vad.silenceAfter(rw.cap, gap, j.edge); at >= 0 {
			rw.to = at + gap/2
		} else if hard := min(rw.from+trSamples(trReplayWindowMost), rw.cap+trSamples(trReplayOver)); j.edge >= hard {
			rw.to = hard
		}
	}
	if rw.to < 0 && j.ending {
		rw.to = j.edge
	}
	if rw.to >= 0 {
		for _, s := range rw.sessions {
			s.stop.Store(rw.to)
			s.wake()
		}
	}
}

func (j *trJob) replayUp(u trUp) {
	rw, s := j.rw, u.s
	if rw == nil || !slices.Contains(rw.sessions, s) || j.finishing {
		if u.up != nil {
			_ = u.up.CloseNow()
		}
		return
	}
	rw.opening--
	if u.err != nil {
		j.logf("session %d (replay window %d) did not start: %s", s.no, rw.n, failure(u.err))
		s.lost = true
		if j.firstErr == nil {
			j.firstErr = u.err
		}
		if rw.opening == 0 && !slices.ContainsFunc(rw.sessions, func(x *trSession) bool { return x.started }) {
			j.fatalErr(j.firstErr)
		}
		return
	}
	s.started, s.activity = true, time.Now()
	s.limit.Store(rw.cap)
	s.stop.Store(rw.to)
	s.run(u.up, rw.from)
	j.all = append(j.all, s)
	j.started++
	note := refNote(rw.covered)
	if rw.aside {
		note = "another session: the reference is too short to check against"
	}
	j.logf("session %d (replay window %d from %.1f s, %s) started in %v", s.no, rw.n, trSeconds(rw.from), note,
		logDur(time.Since(s.began)))
	if rw.opening == 0 || rw.state == trOpening {
		rw.state = trRunning
	}
	j.replayStep()
}

func refNote(covered bool) string {
	if covered {
		return "with a reference"
	}
	return "without a reference"
}

// replayLost: a session of the window dropped; with none left the pass failed (the manager tries it again).
func (j *trJob) replayLost(s *trSession) {
	rw := j.rw
	if rw == nil || !slices.Contains(rw.sessions, s) {
		return
	}
	if s.written.Load() {
		return
	}
	for _, x := range rw.sessions {
		if x.started && !x.lost {
			return
		}
	}
	if rw.opening == 0 {
		j.fatal(codeUpstreamFailed, "the transcription sessions dropped")
	}
}

// replayQuiet: every session of rw had time to answer for the window's audio.
func (j *trJob) replayQuiet(rw *trReplayWin) bool {
	now := time.Now()
	if now.Sub(rw.doneAt) >= trReplayTailMost || j.fake != nil {
		return true
	}
	for _, s := range rw.sessions {
		if !s.started || s.lost {
			continue
		}
		if s.cur != nil || now.Sub(s.activity) < trReplayQuiet || now.Sub(rw.doneAt) < trReplayQuiet {
			return false
		}
	}
	return true
}

// replayPick chooses window rw's text, sends its sentences and its window frame, and closes its sessions; false: the
// window was given more sessions instead (its reference too short to check against and fewer than two sessions
// agreeing: one session is never taken uncorroborated), and is chosen again once they answered.
func (j *trJob) replayPick(rw *trReplayWin) bool {
	sp := j.vad.speech(rw.from, rw.to)
	speech := make([][2]float64, len(sp))
	for i, s := range sp {
		speech[i] = [2]float64{trSeconds(s[0]), trSeconds(s[1])}
	}
	var ref *string
	var refs []string
	for _, r := range j.refs {
		if mid := (r.from + r.to) / 2; mid >= rw.from && mid < rw.to && trHasText(r.text) {
			refs = append(refs, r.text)
		}
	}
	if rw.covered && len(refs) > 0 {
		ref = extwire.F(strings.Join(refs, ""))
	}
	var sentences []trSentence
	kept := false
	how := ""
	if j.fake != nil {
		sentences = j.fake.sentences(rw.from, rw.to, sp)
		how = "the fake transcript"
	} else {
		texts := make([]string, len(rw.sessions))
		stopped := make([]bool, len(rw.sessions))
		pieces := make([][]string, len(rw.sessions))
		var dist []string
		for i, s := range rw.sessions {
			for _, r := range s.replies {
				t := r.String()
				texts[i] += t
				pieces[i] = append(pieces[i], splitSentences(t)...)
			}
			stopped[i] = !s.started || (s.lost && !s.written.Load()) || j.vad.speechIn(s.lastAt, rw.to) > trStopEarly.Seconds()
			d := fmt.Sprintf("session %d: %d characters", s.no, runeLen(texts[i]))
			if ref != nil {
				d += fmt.Sprintf(", distance %.2f", normDistance(texts[i], *ref))
			}
			if stopped[i] {
				d += " (stopped early)"
			}
			dist = append(dist, d)
		}
		pick, k, use := pickReplay(ref, rw.aside, texts, stopped, j.vad.speechIn(rw.from, rw.to))
		if more := trReplayNoRef - len(rw.sessions); use == trRefShort && k && !rw.aside && more > 0 {
			// As a window without a reference has: three sessions, two of which must agree.
			rw.aside, rw.state = true, trRunning // it waits for them as for its first ones
			j.replayOpen(rw, more)
			j.logf("replay window %d [%.1f-%.1f s, %.1f s of speech, %s]: %s; the reference (%d characters) is too short to check the sessions "+
				"against and fewer than two of them agree: %d more session(s) for the window", rw.n, trSeconds(rw.from), trSeconds(rw.to),
				j.vad.speechIn(rw.from, rw.to), refNote(ref != nil), strings.Join(dist, ", "), runeLen(*ref), more)
			return false
		}
		kept = k
		if pick >= 0 {
			sentences = spreadSentences(pieces[pick], speech, trSeconds(rw.from), trSeconds(rw.to))
			how = fmt.Sprintf("sent session %d's text", rw.sessions[pick].no)
		} else {
			how = "none trusted: the hub keeps its text"
		}
		switch use {
		case trRefShort:
			how = fmt.Sprintf("the reference (%d characters) is too short to check the sessions against; %s", runeLen(*ref), how)
		case trRefOutvoted:
			how = "the sessions agree with each other, not with the reference; " + how
		}
		how = strings.Join(dist, ", ") + "; " + how
	}
	j.emit(sentences)
	j.sendWindow(rw.from, rw.to, kept)
	j.windowsDone++
	j.logf("replay window %d [%.1f-%.1f s, %.1f s of speech, %s]: %s, %d sentences", rw.n, trSeconds(rw.from), trSeconds(rw.to),
		j.vad.speechIn(rw.from, rw.to), refNote(ref != nil), how, len(sentences))
	if kept && len(refs) > 0 { // the hub's text stays: what the next window follows
		j.prev = append([]string(nil), refs[max(0, len(refs)-trPreviousMost):]...)
	}
	for _, s := range rw.sessions {
		s.close(false)
	}
	j.nextFrom = rw.to
	j.buf.drop(rw.to)
	j.vad.prune(rw.to)
	j.ack(rw.to)
	return true
}

func (j *trJob) sendWindow(from, to int64, kept bool) {
	j.send(extwire.TranscriptionFrame{Type: extwire.TFWindow, From: secs(from), To: secs(to), Kept: kept})
}

// replayAck: the audio its sessions took (the manager keeps at most a minute ahead of it), or all of it while there is
// no window.
func (j *trJob) replayAck() {
	at := j.edge
	if rw := j.rw; rw != nil {
		at = rw.from
		if rw.state == trRunning && j.fake == nil {
			at = math.MaxInt64
			for _, s := range rw.sessions {
				if s.started && !s.lost {
					at = min(at, s.pos.Load())
				}
			}
			if at == math.MaxInt64 {
				at = rw.from
			}
		} else if rw.state != trOpening {
			at = max(rw.from, min(rw.to, j.edge))
		}
		if j.fake != nil {
			at = j.edge
		}
	}
	if at-j.acked >= trSamples(trAckEvery) || (at > j.acked && at >= j.edge) {
		j.ack(at)
	}
}
