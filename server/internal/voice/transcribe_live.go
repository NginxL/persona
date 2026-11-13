package voice

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extwire"
)

// A live job (docs/assistant/meeting-minutes.md "The built-in adapter", "During the meeting"): its sessions as slots,
// its windows, the captions, the choice of each window's text, and what the sessions say.

// trSlot is one of a live job's sessions as a role: its session now, those it replaced while they finish their
// windows (a rollover), and one being opened.
type trSlot struct {
	kind    string
	cur     *trSession
	old     []*trSession
	pending *trSession
	drops   int // windows in a row it was not trusted in
	tries   int // reconnects in a row
	retryAt time.Time
	rollAt  time.Time
	dead    bool
}

// responsible is the slot's session that writes for window n.
func (sl *trSlot) responsible(n int) *trSession {
	for _, s := range append([]*trSession{sl.cur}, sl.old...) {
		if s != nil && s.started && !s.lost && s.window <= n && (s.last < 0 || n <= s.last) {
			return s
		}
	}
	return nil
}

// trWin is a live window: [from, to) in recording samples, to -1 while it is open.
type trWin struct {
	n        int
	from, to int64
	closedAt time.Time
	// forced: closed by a flush, the end, a hole or a stall, after which the sessions get trFlushSilence of silence
	// rather than more audio.
	forced bool
}

// openWin is the window audio goes into now (nil once the job ends).
func (j *trJob) openWin() *trWin {
	if n := len(j.windows); n > 0 && j.windows[n-1].to < 0 {
		return j.windows[n-1]
	}
	return nil
}

// closeWin closes the open window at at, and opens the next one from there unless the job ends.
func (j *trJob) closeWin(at int64) {
	w := j.openWin()
	if w == nil {
		return
	}
	w.to, w.closedAt = at, time.Now()
	if !j.ending {
		j.windows = append(j.windows, &trWin{n: j.nextWin, from: at, to: -1})
		j.nextWin++
	}
}

// firstOpen is the number of the first window not sent yet.
func (j *trJob) firstOpen() int {
	if len(j.windows) > 0 {
		return j.windows[0].n
	}
	return j.nextWin
}

// liveAudio: audio came; the open window closes at the first silence after trWindow (at the latest trWindowMost).
func (j *trJob) liveAudio() {
	if w := j.openWin(); w != nil {
		length := j.edge - w.from
		if (length >= trSamples(trWindow) && j.vad.quiet(j.edge) >= trSamples(trWindowGap)) || length >= trSamples(trWindowMost) {
			j.closeWin(j.edge)
		}
	}
	if j.fake != nil {
		j.fakeInterim()
	}
	j.finalize()
}

// forceClosed marks the closed windows as ended by silence the job adds (forced).
func (j *trJob) forceClosed() {
	for _, w := range j.windows {
		if w.to >= 0 {
			w.forced = true
		}
	}
}

// silenceAll has every live session hear trFlushSilence of silence once its audio reached at.
func (j *trJob) silenceAll(at int64) {
	for _, s := range j.all {
		if s.slot != nil {
			s.addSilence(at, trSamples(trFlushSilence))
		}
	}
}

func (j *trJob) flush() {
	if j.mode == modeReplay {
		j.send(extwire.TranscriptionFrame{Type: extwire.TFFlushed, At: secs(j.nextFrom)})
		return
	}
	a := j.edge
	if w := j.openWin(); w != nil && w.from < a {
		j.closeWin(a)
	}
	j.forceClosed()
	j.silenceAll(a)
	j.flushes = append(j.flushes, a)
	j.finalize()
}

func (j *trJob) endJob() {
	if j.ending {
		return
	}
	j.ending = true
	if j.mode == modeReplay {
		j.replayStep()
		return
	}
	if w := j.openWin(); w != nil {
		if w.from < j.edge {
			j.closeWin(j.edge)
		} else {
			j.windows = j.windows[:len(j.windows)-1]
		}
	}
	j.forceClosed()
	j.silenceAll(j.edge)
	j.finalize()
}

// open starts a session for sl that writes from window n on. A verbatim session that takes over from another (a
// reconnect, a replacement, a rollover) is told the last sentences sent, as the final pass's windows are: it hears
// the audio only from its first window on, so that each of its replies is of a window it answers for.
func (j *trJob) open(sl *trSlot, n int, why string) {
	s := newTrSession(j, sl.kind, sl, n, why)
	sl.pending = s
	var previous []string
	if why != "first" {
		previous = j.prev
	}
	s.connect(j.ctx, j.instructions(sl.kind, previous))
}

func (j *trJob) sessionUp(u trUp) {
	s := u.s
	if s.slot == nil {
		j.replayUp(u)
		return
	}
	sl := s.slot
	if sl.pending != s || sl.dead || j.finishing || !slices.Contains(j.slots, sl) {
		if u.up != nil {
			_ = u.up.CloseNow()
		}
		return
	}
	sl.pending = nil
	if u.err != nil {
		j.startFailed(sl, s, u.err)
		return
	}
	if s.kind == trCaption && !u.captions {
		_ = u.up.CloseNow()
		j.memo().captions.Store(trNotGranted)
		j.noCaptions(fmt.Sprintf("session %d (caption): the upstream did not grant interim captions", s.no))
		return
	}
	if s.kind == trCaption {
		j.memo().captions.Store(trGranted)
	}
	// It writes from the first window still to be sent at or after the one it was opened for, as far back as the
	// audio is kept. A caption session opened again (a rollover, a reconnect) starts at the live edge instead, for the
	// next window: captions break faster than real time, so it is never given a backlog to catch up on; the open
	// window keeps the session it rolls over (or, after a reconnect, has no captions).
	feed := j.edge
	if s.kind == trCaption && s.why != "first" {
		s.window = j.nextWin
	} else {
		for _, w := range j.windows {
			if w.n >= s.window {
				s.window, feed = w.n, w.from
				break
			}
		}
		if len(j.windows) == 0 || j.windows[len(j.windows)-1].n < s.window {
			s.window = j.nextWin
		}
	}
	if oldest := j.buf.oldest(); feed < oldest {
		feed = oldest
	}
	s.started, s.activity = true, time.Now()
	s.run(u.up, feed)
	j.all = append(j.all, s)
	j.started++
	if old := sl.cur; old != nil {
		old.last, old.retireAt = s.window-1, time.Now().Add(trRollOverlap)
		sl.old = append(sl.old, old)
	}
	sl.cur = s
	j.logf("session %d (%s) started in %v (%s), from %.1f s", s.no, s.kind, logDur(time.Since(s.began)), s.why, trSeconds(feed))
	j.closeRetired()
	j.checkReady()
}

// startFailed: a session of sl could not be started.
func (j *trJob) startFailed(sl *trSlot, s *trSession, err error) {
	j.logf("session %d (%s, %s) did not start: %s", s.no, s.kind, s.why, failure(err))
	captionsRefused := errors.Is(err, errNoCaptions)
	if captionsRefused {
		j.memo().captions.Store(trNotGranted)
	}
	switch {
	case !j.readySent && sl.kind == trCaption:
		j.noCaptions(fmt.Sprintf("session %d (caption) did not start", s.no))
		return
	case sl.kind == trCaption && captionsRefused:
		j.noCaptions(fmt.Sprintf("session %d (caption): the upstream refused interim captions", s.no))
		return
	case s.why == "rollover":
		sl.rollAt = time.Now().Add(trRollRetry) // the session it rolls over carries on
		return
	}
	// A verbatim session that did not start is tried again as one that dropped is, before ready too (a transient
	// failure at the job's start would otherwise leave it one session short for the whole meeting); a refused key
	// is not tried again.
	if !j.readySent && j.firstErr == nil {
		j.firstErr = err
	}
	sl.tries++
	if sl.tries > maxReconnects || keyRefused(err) {
		if !j.readySent {
			sl.dead = true
			if j.verbatimLeft() == 0 {
				j.fatalErr(j.firstErr)
			}
			return
		}
		j.slotDead(sl)
		return
	}
	sl.retryAt = time.Now().Add(trReconnectWaits[min(sl.tries-1, len(trReconnectWaits)-1)])
}

// keyRefused: the service refused the key (401, 403): opening the session again would not help.
func keyRefused(err error) bool {
	var hs *httpStatusError
	return errors.As(err, &hs) && (hs.status == 401 || hs.status == 403)
}

func (j *trJob) verbatimLeft() int {
	n := 0
	for _, sl := range j.slots {
		if sl.kind == trVerbatim && !sl.dead {
			n++
		}
	}
	return n
}

// checkReady sends ready once the caption session (if any) and a verbatim session started.
func (j *trJob) checkReady() {
	if j.readySent {
		return
	}
	captions, verbatim := j.noInterim, false
	for _, sl := range j.slots {
		if sl.cur == nil {
			continue
		}
		if sl.kind == trCaption {
			captions = true
		} else {
			verbatim = true
		}
	}
	if !captions || !verbatim {
		return
	}
	j.readySent = true
	features := []string{extwire.TranscriptionLive}
	if !j.noInterim {
		features = append(features, extwire.TranscriptionInterim)
	}
	if !j.send(extwire.TranscriptionFrame{Type: extwire.TFReady, Features: features}) {
		return
	}
	if j.noInterim {
		j.warn(extwire.TWNoInterim, "the upstream gives no interim captions: three verbatim sessions")
	}
}

// noCaptions goes on without captions: the caption session closed, a third verbatim session.
func (j *trJob) noCaptions(why string) {
	if j.noInterim {
		return
	}
	j.noInterim = true
	j.logf("%s: no interim captions, a third verbatim session", why)
	for i, sl := range j.slots {
		if sl.kind == trCaption {
			sl.dead = true
			for _, s := range append([]*trSession{sl.cur, sl.pending}, sl.old...) {
				if s != nil {
					s.close(false)
				}
			}
			j.slots = slices.Delete(j.slots, i, i+1)
			break
		}
	}
	sl := &trSlot{kind: trVerbatim}
	j.slots = append(j.slots, sl)
	j.open(sl, j.firstOpen(), "for the captions")
	if j.readySent {
		j.warn(extwire.TWNoInterim, "the upstream gives no interim captions: three verbatim sessions")
	} else {
		j.checkReady()
	}
}

// slotDead: sl's session could not be opened again.
func (j *trJob) slotDead(sl *trSlot) {
	sl.dead = true
	j.logf("a %s session could not be opened again after %d tries", sl.kind, maxReconnects)
	if sl.kind == trCaption {
		j.noCaptions("the caption session is gone")
		return
	}
	if j.verbatimLeft() == 0 {
		j.fatal(codeUpstreamFailed, "the transcription sessions dropped and could not be opened again")
	}
}

func (j *trJob) fromSession(r trRead) {
	s := r.s
	if r.err != nil {
		j.sessionGone(s, r.err)
		return
	}
	for _, e := range r.events {
		switch e.kind {
		case tevReply:
			j.replyBegins(s, r.pos)
		case tevText:
			if s.kind == trVerbatim {
				if s.cur == nil {
					j.replyBegins(s, r.pos)
				}
				s.cur.text = append(s.cur.text, e.text...)
			}
		case tevTextDone:
			if s.kind == trVerbatim {
				if s.cur == nil {
					j.replyBegins(s, r.pos)
				}
				if e.text != "" {
					s.cur.whole = e.text
				}
			}
		case tevReplyEnd:
			if s.cur != nil {
				s.cur.done = true
				if trHasText(s.cur.String()) {
					s.replies = append(s.replies, s.cur)
				}
				s.cur = nil
			}
			s.activity = time.Now()
		case tevCaption:
			j.caption(s, e, r.pos, r.clock)
		case tevUsage:
			s.usage = e.seconds
		case tevClosed:
			if e.hasSeconds {
				s.usage = e.seconds
			}
			if !s.closing.Load() {
				j.logf("session %d (%s): the service closed it (%s)", s.no, s.kind, firstNonEmpty(e.reason, "no reason"))
			}
		case tevError:
			j.logf("session %d (%s): the service's error (%s)", s.no, s.kind, firstNonEmpty(e.code, "no code"))
		}
	}
}

func (j *trJob) replyBegins(s *trSession, pos int64) {
	if s.kind != trVerbatim {
		return
	}
	s.activity, s.lastAt = time.Now(), pos
	n := s.window
	if s.slot != nil {
		n = j.assign(s, pos)
	}
	s.cur = &trReply{window: n, at: pos}
}

// assign is the window of a reply that began when the session's audio was at pos. A reply writes one turn: the first
// speech it had not covered yet that was over when it began, with the speech after it within a turn's gap
// (trWindowGap: the service hears one turn there). A reply follows the sentence it writes at once or seconds later:
// the measured service's came 0.05-0.3 s after it with the measured wording, 1.5-3 s with other wording. Over when it
// began is ended by pos, or at most trAssignAhead after it (the job's speech detection hears the audio before the
// session is sent it, and may hear a sentence run on a moment past where the service ended it); so a reply right
// after its sentence is of that sentence, never of the one before it, which would leave the session a sentence behind
// and, across a window's end, its text in the window before. Speech that ended more than trReplyWait before pos is
// covered too: a reply that late is not coming, so what is left of it was in this one or never had one. The reply is
// of the window of the first speech it covers; with none, of the window of what it covered last; -1 when that window
// was sent already (a reply too late for it, never used). When none of its replies was of speech the adapter heard (a
// quiet room its detection misses), a reply is of the window its audio was in.
func (j *trJob) assign(s *trSession, pos int64) int {
	limit := pos + trSamples(trAssignAhead)
	stale := pos - trSamples(trReplyWait)
	n, turnEnd := -1, int64(-1)
claim:
	for _, w := range j.windows {
		if w.n < s.window {
			continue
		}
		end := w.to
		if end < 0 {
			end = j.edge
		}
		for _, sp := range j.vad.speech(w.from, end) {
			switch {
			case sp[1] <= s.claimed:
				continue
			case sp[1] > limit: // not over before the reply began
				break claim
			case sp[1] > stale: // its turn: the first speech it could be of, and what follows within a turn's gap
				if turnEnd >= 0 && sp[0]-turnEnd >= trSamples(trWindowGap) {
					break claim // the next turn, another reply's
				}
				turnEnd = sp[1]
			}
			s.claimed = sp[1]
			if n < 0 {
				n = w.n
			}
		}
	}
	if n >= 0 {
		return n
	}
	if len(j.windows) > 0 && s.claimed == s.feed && len(j.vad.speech(j.windows[0].from, max(pos, j.windows[0].from))) == 0 {
		// None of its replies was of speech the adapter heard: a quiet room its detection misses. The reply is of the
		// window its audio was in.
		if n := j.windowAt(max(pos, j.windows[0].from)); n >= s.window {
			return n
		}
		return -1
	}
	for _, w := range j.windows {
		if s.claimed >= w.from && (w.to < 0 || s.claimed <= w.to) {
			if w.n < s.window {
				return -1
			}
			return w.n
		}
	}
	if len(j.windows) > 0 && s.claimed < j.windows[0].from {
		return -1
	}
	return max(s.window, j.nextWin-1)
}

// caption takes an interim caption of a caption session that came when the session's audio was at pos (recording
// samples) and clock (session samples). The service's start_ms is never a recording time: the measured service
// counts it from the session's start and gives every caption of a session the same one, since it writes the whole
// meeting as one item (captionOf). A caption is of the window it began in; one that goes on growing past that
// window's end once the window settled (one caption can run over a long stretch) is of the next window from there.
// It goes to the manager as interim, with its caption's start, while the session keeps up with the audio.
func (j *trJob) caption(s *trSession, e tevent, pos, clock int64) {
	if s.kind != trCaption || s.slot == nil {
		return
	}
	s.activity = time.Now()
	it := j.captionOf(s, e, pos)
	if it == nil {
		return
	}
	if last := it.parts[len(it.parts)-1]; it.text != "" && runeLen(e.text) > runeLen(it.text) && last.window >= 0 {
		switch w := j.win(last.window); {
		case w == nil && last.window < j.firstOpen(): // its window was sent: what it adds is of the first one not sent
			it.parts = append(it.parts, trPart{window: j.firstOpen(), from: runeLen(it.text)})
		case w != nil && w.to >= 0 && j.captionPast(s, w, clock):
			it.parts = append(it.parts, trPart{window: last.window + 1, from: runeLen(it.text)})
		}
	}
	it.text = e.text // a final one with no text withdraws it
	if e.final {
		it.closed = true
		if s.capCur == it {
			s.capCur = nil
		}
	}
	if s != s.slot.cur || !j.readySent || j.buf.edgeNow()-s.pos.Load() > trSamples(trInterimLag) {
		return
	}
	last := it.parts[len(it.parts)-1]
	rs := []rune(it.text)
	start := it.start
	if last.from > 0 {
		if w := j.win(last.window); w != nil {
			start = max(start, w.from)
		}
	}
	j.send(extwire.TranscriptionFrame{Type: extwire.TFInterim, Start: secs(start), Text: extwire.F(string(rs[min(last.from, len(rs)):]))})
}

// captionOf is the caption a caption frame that came when the session's audio was at pos is of. The measured service
// writes a caption session's whole meeting as one item, never final, the next sentence's text written over the last
// one's, so a caption is told apart by its text: a frame of the caption being written that revises it (trRevises:
// one within trCaptionFresh of its first frame, or one that grew it, its first or last words maybe revised) is of
// it; one that does not begins a new caption, and the one before it keeps its last text. A final frame of its item is
// of it whatever its text, before any of that (a service that marks a sentence's last caption final may write it in
// another form: "twenty five", then "25."): it replaces the caption's text and closes it; one without text withdraws
// it. A service that splits its captions by sentence gives each its own item: a frame of another item is of an
// earlier caption of that item still open (one written after the next began), else it begins a new caption. A new caption begins where the session's audio was when its first frame came
// (captionStart); nil: a frame with no text that withdraws nothing.
func (j *trJob) captionOf(s *trSession, e tevent, pos int64) *trItem {
	if !e.final && !trHasText(e.text) {
		return nil
	}
	if cur := s.capCur; cur != nil && e.item == cur.id {
		if e.final || !trHasText(e.text) || trRevises(cur.text, e.text, pos-cur.at <= trSamples(trCaptionFresh)) {
			return cur
		}
		cur.closed = true // the next sentence, written over this one's text
	} else {
		for i := len(s.items) - 1; i >= 0; i-- {
			if it := s.items[i]; it.id == e.item && !it.closed {
				return it
			}
		}
	}
	if !trHasText(e.text) {
		return nil
	}
	start := j.captionStart(s, pos)
	it := &trItem{id: e.item, start: start, at: pos, parts: []trPart{{window: j.windowAt(start)}}}
	s.items = append(s.items, it)
	s.capCur = it
	return it
}

// captionStart is where a caption began whose first frame came when caption session s's audio was at pos: its first
// words come a moment after its sentence began (trCaptionLag), so it is the start of the speech going on then, or of
// the next speech when that moment was in a silence (one before a window's end, say), never before the window that
// moment is in nor before the session's first audio.
func (j *trJob) captionStart(s *trSession, pos int64) int64 {
	at := max(pos-trSamples(trCaptionLag), s.feed)
	start := j.vad.speechStart(at, pos)
	for _, w := range j.windows {
		if w.from <= at && (w.to < 0 || at < w.to) {
			start = max(start, w.from)
		}
	}
	return start
}

// windowAt is the number of the window not sent yet that position at is in (-1: one sent already).
func (j *trJob) windowAt(at int64) int {
	for _, w := range j.windows {
		if at < w.from {
			break
		}
		if w.to < 0 || at < w.to {
			return w.n
		}
	}
	if len(j.windows) > 0 && at < j.windows[0].from {
		return -1
	}
	return j.nextWin - 1
}

// win is window n while it is not sent yet.
func (j *trJob) win(n int) *trWin {
	for _, w := range j.windows {
		if w.n == n {
			return w
		}
	}
	return nil
}

// captionPast: a caption that came when caption session s was at session sample clock came after window w settled:
// the session had trCaptionSettle of audio past its end, and trSettleWall of time (one that is behind gets its audio
// faster than real time).
func (j *trJob) captionPast(s *trSession, w *trWin, clock int64) bool {
	c, ok := s.clockAt(w.to)
	passed, seen := s.passed[w.n]
	return ok && seen && clock >= c+trSamples(trCaptionSettle) && time.Since(passed) >= trSettleWall
}

// captionText is caption session s's captions of window w; those of the windows before it are forgotten, but for the
// caption being written (one of w may still grow).
func (j *trJob) captionText(s *trSession, w *trWin) string {
	var b strings.Builder
	keep := s.items[:0]
	for _, it := range s.items {
		rs := []rune(it.text)
		for k, p := range it.parts {
			if p.window != w.n {
				continue
			}
			to := len(rs)
			if k+1 < len(it.parts) {
				to = min(it.parts[k+1].from, len(rs))
			}
			if p.from < to {
				b.WriteString(string(rs[p.from:to]))
			}
		}
		if it.parts[len(it.parts)-1].window >= w.n || it == s.capCur {
			keep = append(keep, it)
		}
	}
	clear(s.items[len(keep):])
	s.items = keep
	return b.String()
}

// settled: s has had window w's audio, and enough after it to have answered for it.
func (j *trJob) settled(s *trSession, w *trWin, now time.Time) bool {
	if s.pos.Load() < w.to {
		return false
	}
	passed, ok := s.passed[w.n]
	if !ok {
		s.passed[w.n] = now
		return false
	}
	after := trSettle
	if w.forced {
		after = min(after, trFlushSilence)
	}
	c, ok := s.clockAt(w.to)
	if !ok || s.clock.Load()-c < trSamples(after) || now.Sub(passed) < trSettleWall {
		return false
	}
	if s.cur != nil && s.cur.window <= w.n && s.cur.window >= 0 {
		return false // its reply for the window is still coming
	}
	return !j.replyOwed(s, w) || now.Sub(passed) >= trReplyWait
}

// replyOwed: verbatim session s has not begun a reply to window w's last speech yet (a reply comes 1.5 s, up to 3 s,
// after its sentence ends: a window decided before it would lose that sentence).
func (j *trJob) replyOwed(s *trSession, w *trWin) bool {
	if s.kind != trVerbatim {
		return false
	}
	sp := j.vad.speech(w.from, w.to)
	return len(sp) > 0 && s.claimed < sp[len(sp)-1][1]
}

// finalize sends the windows that are done, in order.
func (j *trJob) finalize() {
	if j.mode != modeLive || j.over {
		return
	}
	now := time.Now()
	for _, w := range j.windows {
		if w.to < 0 {
			break
		}
		for _, s := range j.all {
			if _, ok := s.passed[w.n]; !ok && s.slot != nil && s.pos.Load() >= w.to {
				s.passed[w.n] = now
			}
		}
	}
	for len(j.windows) > 0 && j.windows[0].to >= 0 && !j.over {
		w := j.windows[0]
		if !j.finalizeOne(w) {
			break
		}
		j.windows = j.windows[1:]
		j.windowsDone++
		next := j.edge
		if len(j.windows) > 0 {
			next = j.windows[0].from
		}
		j.vad.prune(next)
		for _, s := range j.all {
			delete(s.passed, w.n)
			s.pruneMarks(next)
			s.replies = slices.DeleteFunc(s.replies, func(r *trReply) bool { return r.window <= w.n })
		}
	}
	j.checkFlushes()
	j.closeRetired()
	if j.ending && len(j.windows) == 0 && !j.finishing {
		j.finish()
	}
}

// finalizeOne chooses window w's text and sends its sentences, once its sessions settled or it waited trWindowWait;
// false: not yet.
func (j *trJob) finalizeOne(w *trWin) bool {
	now := time.Now()
	sp := j.vad.speech(w.from, w.to)
	speech := make([][2]float64, len(sp))
	for i, s := range sp {
		speech[i] = [2]float64{trSeconds(s[0]), trSeconds(s[1])}
	}
	if j.fake != nil {
		j.emit(j.fake.sentences(w.from, w.to, sp))
		return true
	}
	// A window the adapter's speech detection found no speech in is chosen too: a quiet room (a phone on the table, no
	// voice processing) may be below its gate while the sessions hear it. Its sessions are checked against each other,
	// not the captions (its cuts fall anywhere, so captions and replies split a sentence differently), the captions'
	// text is sent only when no session wrote any, its sentences are spread over the whole window, and no session is
	// counted dropped in it.
	quietWin := len(sp) == 0
	waited := now.Sub(w.closedAt) >= trWindowWait
	var caption *trSession
	type cand struct {
		s  *trSession
		sl *trSlot
	}
	var cands []cand
	behind, late := 0, 0
	for _, sl := range j.slots {
		if sl.dead {
			continue
		}
		s := sl.responsible(w.n)
		if sl.kind == trCaption && (s == nil || s.skipped(w.from, w.to)) {
			// No caption session heard all of this window (one opened again starts at the live edge, one that fell
			// behind skipped to it): no captions. What it has of the window is forgotten as a window's captions are.
			if s != nil {
				j.captionText(s, w)
			}
			continue
		}
		if s == nil || !j.settled(s, w, now) {
			if !waited {
				return false
			}
			behind++
			continue
		}
		if j.replyOwed(s, w) {
			late++ // taken after trReplyWait without a reply to the window's last speech
		}
		if sl.kind == trCaption {
			caption = s
		} else {
			cands = append(cands, cand{s, sl})
		}
	}
	capText := ""
	if caption != nil {
		capText = j.captionText(caption, w)
	}
	texts := make([]string, len(cands))
	pieces := make([][]string, len(cands))
	for i, c := range cands {
		var b strings.Builder
		for _, r := range c.s.replies {
			if r.window == w.n && r.done {
				t := r.String()
				b.WriteString(t)
				pieces[i] = append(pieces[i], splitSentences(t)...)
			}
		}
		texts[i] = b.String()
	}
	check := capText
	if quietWin {
		check = "" // windows cut without speech to cut at split sentences unlike the captions do: the sessions agree among themselves
	}
	pick, dropped := pickLive(check, texts)
	var out []string
	from := "sent nothing"
	switch {
	case pick >= 0:
		out, from = pieces[pick], fmt.Sprintf("sent session %d's text", cands[pick].s.no)
	case trHasText(capText):
		out, from = splitSentences(capText), "sent the captions' text"
	}
	sentences := spreadSentences(out, speech, trSeconds(w.from), trSeconds(w.to))
	j.emit(sentences)
	var dist []string
	for i, c := range cands {
		d := fmt.Sprintf("session %d: %d characters", c.s.no, runeLen(texts[i]))
		if trHasText(capText) {
			d += fmt.Sprintf(", distance %.2f", normDistance(texts[i], capText))
		}
		switch {
		case dropped[i]:
			d += " (dropped)"
			if !quietWin {
				c.sl.drops++
			}
		case !quietWin:
			c.sl.drops = 0
		}
		dist = append(dist, d)
	}
	captions := "no captions"
	if caption != nil {
		captions = fmt.Sprintf("captions %d characters", runeLen(capText))
	}
	j.logf("window %d [%.1f-%.1f s, %.1f s of speech]: %s; %s; %s, %d sentences%s", w.n, trSeconds(w.from), trSeconds(w.to),
		j.vad.speechIn(w.from, w.to), captions, firstNonEmpty(strings.Join(dist, ", "), "no verbatim session"), from, len(sentences),
		behindNote(behind+late))
	if behind+late > 0 && now.Sub(j.behindAt) >= trBehindEvery {
		j.behindAt = now
		j.warn(extwire.TWBehind, fmt.Sprintf("%d sessions had not answered for a window in time", behind+late))
	}
	for _, c := range cands {
		if c.sl.drops >= 2 {
			j.replace(c.sl, w)
		}
	}
	return true
}

func behindNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d sessions behind)", n)
}

func (j *trJob) emit(sentences []trSentence) {
	for _, s := range sentences {
		start, end := math.Round(s.start*1000)/1000, math.Round(s.end*1000)/1000
		if !j.send(extwire.TranscriptionFrame{Type: extwire.TFSentence, Start: &start, End: &end, Text: extwire.F(s.text)}) {
			return
		}
		j.sentences++
		if j.prev = append(j.prev, s.text); len(j.prev) > trPreviousMost {
			j.prev = j.prev[len(j.prev)-trPreviousMost:]
		}
	}
}

// replace closes sl's session, not trusted in two windows in a row (an invented role persists in its context), and
// opens a fresh one from the next window.
func (j *trJob) replace(sl *trSlot, w *trWin) {
	old := sl.cur
	if old == nil || sl.pending != nil {
		return
	}
	sl.cur, sl.drops = nil, 0
	old.close(false)
	j.replaced++
	j.logf("session %d (%s) was not trusted in two windows in a row: replaced", old.no, old.kind)
	j.warn(extwire.TWSessionDropped, "a verbatim session was replaced: its text matched neither the captions nor the others twice in a row")
	j.open(sl, w.n+1, fmt.Sprintf("replacing session %d", old.no))
}

// closeRetired closes the sessions a rollover replaced once their last window is sent and they ran beside the new one
// for trRollOverlap.
func (j *trJob) closeRetired() {
	first, now := j.firstOpen(), time.Now()
	for _, sl := range j.slots {
		sl.old = slices.DeleteFunc(sl.old, func(s *trSession) bool {
			if s.last < first && (!now.Before(s.retireAt) || j.ending) {
				s.close(false)
				return true
			}
			return false
		})
	}
}

func (j *trJob) checkFlushes() {
	keep := j.flushes[:0]
	for _, a := range j.flushes {
		waiting := false
		for _, w := range j.windows {
			if w.to >= 0 && w.to <= a {
				waiting = true
				break
			}
		}
		if waiting {
			keep = append(keep, a)
			continue
		}
		j.send(extwire.TranscriptionFrame{Type: extwire.TFFlushed, At: secs(a)})
	}
	j.flushes = keep
}

// sessionGone: s's socket is gone (closed by the job, or lost).
func (j *trJob) sessionGone(s *trSession, err error) {
	if s.lost {
		return
	}
	s.lost = true
	s.end()
	j.usageDone += s.usage
	j.all = slices.DeleteFunc(j.all, func(x *trSession) bool { return x == s })
	if s.closing.Load() {
		if j.finishing && len(j.all) == 0 {
			j.complete()
		}
		return
	}
	j.logf("session %d (%s) lost after %v: %s", s.no, s.kind, logDur(time.Since(s.began)), sessionLost(err))
	if s.slot == nil {
		j.replayLost(s)
		return
	}
	sl := s.slot
	if sl.cur != s {
		sl.old = slices.DeleteFunc(sl.old, func(x *trSession) bool { return x == s })
		return
	}
	sl.cur = nil
	if time.Since(s.began) >= stableAfter {
		sl.tries = 0
	}
	sl.tries++
	j.reconnects++
	j.warn(extwire.TWSessionDropped, "a transcription session dropped; opening it again")
	if sl.tries > maxReconnects {
		j.slotDead(sl)
		return
	}
	sl.retryAt = time.Now().Add(trReconnectWaits[min(sl.tries-1, len(trReconnectWaits)-1)])
}

func sessionLost(err error) string {
	if s := websocket.CloseStatus(err); s != -1 {
		return fmt.Sprintf("the service closed the socket (%d)", s)
	}
	return transportError(err)
}
