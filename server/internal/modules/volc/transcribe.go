package volc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extwire"
)

// Meeting transcription (docs/assistant/meeting-minutes.md "The `transcription` extension point"; the Volcengine side
// in docs/extensions/volcengine-speech.md "Recognition"): one job per WebSocket from the manager, its audio in and its
// sentences out, in recording seconds. A live job streams to one session at a time of the two-pass endpoint: its
// interim text becomes `interim`, each sentence it marks definite a `sentence`. A session ends at a flush, a hole, a
// pause of a few seconds (the service ends a session that gets no packet for 8 s), after about 20 minutes, and when
// its socket drops (the next one replays the audio since its last sentence from the job's buffer). A replay job (the
// final pass, an extension's own audio) sends windows of about five minutes, cut at a pause, each to one session of
// the streaming-input endpoint, which answers once the window's audio ended; a window that fails is tried once more.
// Sentences go out in order: those of a session wait while an earlier one is still open.

// replayGapTries: a window of a replay job with no transcript of record is tried this often before it is a gap (#8887).
const replayGapTries = 3

// Times a job keeps (variables: tests shorten them).
var (
	liveQuietEnd     = 3 * time.Second  // no audio this long: the session is finished
	liveRollover     = 20 * time.Minute // a session this long rolls over at the next pause
	liveRolloverMost = 25 * time.Minute // and at the latest here
	replayWindow     = 5 * time.Minute  // a final-pass window this long ends at the next pause
	replayWindowMost = 5*time.Minute + 30*time.Second
	readyWait        = 30 * time.Second // the contract's: ready within this of start
	usageEvery       = 30 * time.Second
	retryDelays      = []time.Duration{0, 2 * time.Second, 4 * time.Second}
	jobTick          = 100 * time.Millisecond
)

// liveSpeed: a burst of audio (the manager's backlog after ready) goes to the service at most this fast (a variable:
// tests send faster).
var liveSpeed = 2.0

const (
	ackEvery     = 5.0 // seconds of audio between acks
	paceAhead    = 1.0 // seconds a session may be sent ahead of its pace
	liveRingSecs = 60
	replayPause  = 0.5 // seconds of quiet a window ends at
	livePause    = 0.3 // seconds of quiet a session rolls over at
	setupKey     = "key"
)

// serveTranscriptionInfo is GET /transcription/{cid}/info: {protocol, features, setup?}; setup "key" until a key is
// saved.
func (pl *Plugin) serveTranscriptionInfo(w http.ResponseWriter, r *http.Request) {
	if !pl.transcriptionCaller(w, r) {
		return
	}
	info := extwire.TranscriptionInfo{Protocol: extwire.TranscriptionProtocol,
		Features: []string{extwire.TranscriptionLive, extwire.TranscriptionReplay, extwire.TranscriptionInterim}}
	if !pl.cred.set() {
		info.Setup = setupKey
	}
	writeJSON(w, http.StatusOK, info)
}

// serveTranscriptionWS is GET /transcription/{cid}/ws: one job per connection.
func (pl *Plugin) serveTranscriptionWS(w http.ResponseWriter, r *http.Request) {
	if !pl.transcriptionCaller(w, r) {
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // the extension's own socket
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	j := &job{pl: pl, id: pl.jobs.Add(1), c: c, ctx: ctx, cancel: cancel, began: time.Now(),
		in: make(chan inMsg, 64), evs: make(chan sessEv, 64), ups: make(chan dialed)}
	j.run()
}

// transcriptionCaller answers a request for another contribution (404, also while this one is switched off) or from
// an app (403: only the manager dials it; the apps' forward refuses these paths anyway).
func (pl *Plugin) transcriptionCaller(w http.ResponseWriter, r *http.Request) bool {
	if cid := r.PathValue("cid"); cid != TranscriptionContribution || !pl.on("transcription", cid) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such transcription contribution", "code": "not_found"})
		return false
	}
	return fullCaller(w, r)
}

type inMsg struct {
	text []byte
	bin  []byte
	err  error
}

// sess is one session of the service in a job.
type sess struct {
	s       *asrSession
	n       int
	why     string
	at      float64 // recording second of its first sample
	sentTo  float64 // audio sent up to here
	until   float64 // its audio ends here (negative: the session audio goes to now)
	paceAt  float64 // sentTo when its pace began
	paceT   time.Time
	lastDef int // ms: the end of its last sentence handed on
	dur     int // ms the service says it heard
	finish  bool
	closed  bool
	failed  bool
	tries   int
	queue   []extwire.TranscriptionFrame
	final   *asrResult
	byT     time.Time // a finishing session not closed by then is closed
	due     time.Time // a session to open again: when
}

func (ss *sess) end(pos float64) float64 {
	if ss.until >= 0 {
		return ss.until
	}
	return pos
}

type sessEv struct {
	ss     *sess
	ev     asrEvent
	closed bool
}

type dialed struct {
	ss  *sess
	s   *asrSession
	err error
}

type job struct {
	pl     *Plugin
	id     int64
	c      *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	began  time.Time
	in     chan inMsg
	evs    chan sessEv
	ups    chan dialed

	mode    string
	speed   float64
	hot     []string
	started bool
	ready   bool
	ended   bool
	ring    ring
	q       quiet
	inPos   float64 // where the next audio from the manager goes
	refs    []ref

	sessions  []*sess // open (dialing included), oldest first
	cur       *sess   // the one new audio goes to
	dialing   *sess
	n         int
	fails     int     // dials or drops in a row
	retry     []*sess // sessions to open again (their at, until and tries), each when its due passes, in failure order
	lastAudio time.Time
	lastAck   float64
	flushes   []float64
	usageAt   time.Time
	usedMS    int
	sentences int
	warnings  int
	interim   string
	fatal     *extwire.TranscriptionFrame
}

type ref struct {
	from, to float64
	chars    int
}

// run is the job: one loop over the manager's frames, the sessions' results and a ticker. It writes every frame to
// the manager itself.
func (j *job) run() {
	defer j.cancel()
	defer j.closeAll()
	go j.read()
	tick := time.NewTicker(jobTick)
	defer tick.Stop()
	for {
		select {
		case m := <-j.in:
			if m.err != nil {
				j.logf("the manager closed the job (%s)", closeWhy(m.err))
				return
			}
			if m.bin != nil {
				j.onAudio(m.bin)
			} else {
				j.onFrame(m.text)
			}
		case e := <-j.evs:
			j.onEvent(e)
		case d := <-j.ups:
			j.onDialed(d)
		case <-tick.C:
			j.onTick()
		case <-j.ctx.Done():
			return
		}
		if j.fatal != nil {
			j.logf("ends: %s %s", j.fatal.Code, j.fatal.Message)
			_ = j.send(*j.fatal)
			j.c.Close(websocket.StatusNormalClosure, "")
			return
		}
		if j.ended && len(j.sessions) == 0 && j.dialing == nil && len(j.retry) == 0 {
			j.release()
			j.sendUsage()
			_ = j.send(extwire.TranscriptionFrame{Type: extwire.TFDone})
			j.logf("done after %v: %d sessions, %d sentences, %.1f s heard, %d warnings", since(j.began), j.n, j.sentences,
				float64(j.usedMS)/1000, j.warnings)
			j.c.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
}

func (j *job) logf(format string, args ...any) {
	j.pl.logf("transcription %d: %s", j.id, fmt.Sprintf(format, args...))
}

func closeWhy(err error) string {
	if s := websocket.CloseStatus(err); s != -1 {
		return fmt.Sprintf("close %d", s)
	}
	return "socket ended"
}

func (j *job) read() {
	for {
		typ, b, err := j.c.Read(j.ctx)
		if err != nil {
			select {
			case j.in <- inMsg{err: err}:
			case <-j.ctx.Done():
			}
			return
		}
		m := inMsg{text: b}
		if typ == websocket.MessageBinary {
			m = inMsg{bin: b}
			if b == nil {
				m.bin = []byte{}
			}
		}
		select {
		case j.in <- m:
		case <-j.ctx.Done():
			return
		}
	}
}

func (j *job) send(f extwire.TranscriptionFrame) error {
	b, _ := json.Marshal(f)
	ctx, cancel := context.WithTimeout(j.ctx, 10*time.Second)
	defer cancel()
	return j.c.Write(ctx, websocket.MessageText, b)
}

func (j *job) fail(code, msg string) {
	if j.fatal == nil {
		j.fatal = &extwire.TranscriptionFrame{Type: extwire.TFError, Code: code, Message: msg, Fatal: true}
	}
}

func (j *job) warn(code, msg string) {
	j.warnings++
	j.logf("warning %s: %s", code, msg)
	_ = j.send(extwire.TranscriptionFrame{Type: extwire.TFWarning, Code: code, Message: msg})
}

func (j *job) live() bool { return j.mode == extwire.TranscriptionLive }

// onFrame handles one text frame of the manager's.
func (j *job) onFrame(raw []byte) {
	var f extwire.TranscriptionFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		j.fail(extwire.TEBadFrame, "a text frame that is not JSON")
		return
	}
	switch f.Type {
	case extwire.TFStart:
		if j.started {
			return
		}
		j.onStart(f)
	case extwire.TFSeek:
		if f.At == nil || *f.At < 0 {
			j.fail(extwire.TEBadFrame, "a seek without at")
			return
		}
		j.onSeek(*f.At)
	case extwire.TFFlush:
		j.flushes = append(j.flushes, j.ring.end())
		j.endCurrent()
		j.release()
	case extwire.TFReference:
		if f.From != nil && f.To != nil && f.Text != nil {
			j.refs = append(j.refs, ref{from: *f.From, to: *f.To, chars: len([]rune(strings.TrimSpace(*f.Text)))})
		}
	case extwire.TFEnd:
		j.ended = true
		j.endCurrent()
		j.release()
	}
}

func (j *job) onStart(f extwire.TranscriptionFrame) {
	j.started = true
	j.mode = extwire.TranscriptionLive
	if f.Mode == extwire.TranscriptionReplay {
		j.mode = extwire.TranscriptionReplay
	}
	j.speed = liveSpeed
	if !j.live() {
		j.speed = max(f.Speed, 1)
	}
	at := 0.0
	if f.At != nil && *f.At > 0 {
		at = *f.At
	}
	j.inPos = at
	most := liveRingSecs * bytesPerSec
	if !j.live() {
		most = int(replayWindowMost.Seconds()+30) * bytesPerSec
	}
	j.ring = ring{at: at, most: most}
	j.hot = hotwords(f.Glossary, j.live())
	j.usageAt = time.Now()
	j.logf("%s at %.1f, language %s, %d glossary terms (%d as hotwords), model %s", j.mode, at, firstNonEmpty(f.Language, "-"),
		len(f.Glossary), len(j.hot), j.pl.form.asrModel())
	if !j.pl.cred.set() {
		j.fail(extwire.TENoUpstream, "no Volcengine key is saved")
		return
	}
	if !j.live() {
		j.ready = true
		_ = j.send(extwire.TranscriptionFrame{Type: extwire.TFReady, Features: []string{extwire.TranscriptionReplay}})
		return
	}
	// Live: the first session is opened now, so a refused key or a model not turned on is known before ready.
	j.open(&sess{at: at, until: -1, why: "first"}, true)
}

func (j *job) onSeek(at float64) {
	end := j.ring.end()
	if at <= end+0.001 {
		j.inPos = at // audio we hold already: dropped as it comes again
		return
	}
	// A hole: the session ends where the audio does; the next one starts after the hole.
	j.endCurrent()
	j.ring.reset(at)
	j.inPos = at
}

// onAudio takes one binary frame.
func (j *job) onAudio(pcm []byte) {
	if !j.started || len(pcm)%2 != 0 {
		j.fail(extwire.TEBadFrame, "audio before start, or of an odd length")
		return
	}
	end := j.ring.end()
	if j.inPos < end { // a repeat of audio already held
		skip := min(secToBytes(end-j.inPos), int64(len(pcm)))
		pcm = pcm[skip:]
		j.inPos += bytesToSec(skip)
	} else if j.inPos > end+0.001 { // the manager skipped ahead without a seek: a hole all the same
		j.onSeek(j.inPos)
	}
	if len(pcm) == 0 || j.ended {
		return
	}
	j.lastAudio = time.Now()
	from := j.ring.end()
	j.ring.add(pcm)
	j.q.add(pcm)
	j.inPos = j.ring.end()
	if j.cur == nil && j.dialing == nil {
		open := len(j.retry) == 0
		if !j.live() && len(j.retry) > 0 {
			// A pending unbounded retry still owns new audio. Otherwise start after every bounded retry and window,
			// including a window that opened and ended while a retry was waiting.
			open = true
			start := -1.0
			for _, r := range j.retry {
				if r.until < 0 {
					open = false
					break
				}
				start = max(start, r.until)
			}
			for _, ss := range j.sessions {
				if ss.until >= 0 {
					start = max(start, ss.until)
				}
			}
			if open && start <= from+0.001 {
				from = max(start, j.ring.at)
			} else {
				open = false
			}
		}
		if open {
			j.open(&sess{at: from, until: -1, why: j.nextWhy()}, false)
		}
	}
	if taking := cmpOr(j.cur, j.dialing); taking != nil && !j.live() {
		long := j.inPos - taking.at
		if (long >= replayWindow.Seconds() && j.q.run >= replayPause) || long >= replayWindowMost.Seconds() {
			j.endCurrent()
		}
	}
	j.pump()
}

func (j *job) nextWhy() string {
	if j.n == 0 {
		return "first"
	}
	if !j.live() {
		return "window"
	}
	return "next"
}

// open dials ss (in the background: audio keeps coming meanwhile) and puts it in order with the others.
func (j *job) open(ss *sess, first bool) {
	j.n++
	ss.n = j.n
	ss.sentTo = ss.at
	if ss.until < 0 {
		j.dialing = ss
	}
	j.inOrder(ss)
	endpoint := endpointLive
	if !j.live() {
		endpoint = endpointReplay
	}
	cred, model, hot := j.pl.cred, j.pl.form.asrModel(), j.hot
	go func() {
		s, err := dialASR(j.ctx, cred, model, endpoint, hot)
		select {
		case j.ups <- dialed{ss: ss, s: s, err: err}:
		case <-j.ctx.Done():
			if s != nil {
				s.close()
			}
		}
	}()
	_ = first
}

func (j *job) onDialed(d dialed) {
	ss := d.ss
	if j.dialing == ss {
		j.dialing = nil
	}
	if d.err != nil {
		j.remove(ss)
		j.logf("session %d (%s) at %.1f did not start: %v", ss.n, ss.why, ss.at, d.err)
		switch kindOf(d.err) {
		case failForbidden, failNotEnabled, failVoice:
			j.pl.board.set(serviceASR, d.err, 0, sourceUse)
			j.fail(extwire.TEUpstreamForbidden, d.err.Error())
			return
		}
		j.again(ss, d.err)
		return
	}
	ss.s = d.s
	ss.paceAt, ss.paceT = ss.sentTo, time.Now()
	if ss.n == 1 { // the page's word on recognition: it works (once a job: not on every session)
		j.pl.board.set(serviceASR, nil, 0, sourceUse)
	}
	j.logf("session %d (%s) at %.1f%s", ss.n, ss.why, ss.at, logidNote(d.s.logid))
	if ss.until < 0 {
		if j.cur != nil { // cannot happen: one session takes new audio at a time
			j.cur.until = j.ring.end()
		}
		j.cur = ss
	}
	go j.forward(ss)
	if !j.ready {
		j.ready = true
		_ = j.send(extwire.TranscriptionFrame{Type: extwire.TFReady, Features: []string{extwire.TranscriptionLive, extwire.TranscriptionInterim}})
	}
	j.pump()
}

func logidNote(id string) string {
	if id == "" {
		return ""
	}
	return " (logid " + id + ")"
}

// again opens ss's audio in a new session after a failure: at once, then after 2 and 4 seconds; a third failure in a
// row ends the job (live: the manager opens a new job; replay: the manager tries the pass again). A replay job with no
// transcript of record (an extension's: no `reference` before its audio) gives a window two retries, then sends it as
// a gap and goes on (#8887); failures that run on into the next window still end it.
func (j *job) again(ss *sess, err error) {
	j.fails++
	gaps := !j.live() && len(j.refs) == 0 && j.ready
	if gaps && j.fails < len(retryDelays)+1 && ss.tries >= replayGapTries-1 {
		j.gap(ss, err)
		return
	}
	if j.fails >= len(retryDelays)+1 || (!j.live() && !gaps && ss.tries >= 1) {
		code := transcriptionCode(err)
		if !j.ready && code == extwire.TEUpstreamFailed {
			code = extwire.TEUpstreamUnavailable
		}
		j.fail(code, err.Error())
		return
	}
	next := &sess{at: ss.at, until: ss.until, why: "again", tries: ss.tries + 1}
	if j.live() {
		next.at = max(ss.at+float64(ss.lastDef)/1000, j.ring.at)
	}
	if next.at < j.ring.at { // the ring lost the start of a window: what is left of it
		next.at = j.ring.at
	}
	next.due = time.Now().Add(retryDelays[min(j.fails-1, len(retryDelays)-1)])
	j.retry = append(j.retry, next) // never in place of another: two windows can fail at once
}

// gap gives up on ss's window: a kept window over its audio goes out in its turn (the extension's job reads it as a
// gap), and the job goes on. A window that was taking new audio ends where the audio is now; what comes next opens
// the next window.
func (j *job) gap(ss *sess, err error) {
	from, to := ss.at, ss.until
	if to < 0 {
		to = j.ring.end()
	}
	j.logf("window %.1f–%.1f: no session after %d tries (%v), a gap", from, to, ss.tries+1, err)
	j.inOrder(&sess{at: from, until: to, why: "gap", closed: true, failed: true,
		queue: []extwire.TranscriptionFrame{{Type: extwire.TFWindow, From: extwire.F(round3(from)), To: extwire.F(round3(to)), Kept: true}}})
	j.release()
}

// inOrder puts ss among the job's sessions by where its audio begins (one that begins where another does, after it).
func (j *job) inOrder(ss *sess) {
	j.sessions = append(j.sessions, ss)
	slices.SortStableFunc(j.sessions, func(a, b *sess) int {
		switch {
		case a.at < b.at:
			return -1
		case a.at > b.at:
			return 1
		}
		return 0
	})
}

func (j *job) remove(ss *sess) {
	j.sessions = slices.DeleteFunc(j.sessions, func(x *sess) bool { return x == ss })
	if j.cur == ss {
		j.cur = nil
	}
}

func (j *job) forward(ss *sess) {
	for ev := range ss.s.events {
		select {
		case j.evs <- sessEv{ss: ss, ev: ev}:
		case <-j.ctx.Done():
			return
		}
	}
	select {
	case j.evs <- sessEv{ss: ss, closed: true}:
	case <-j.ctx.Done():
	}
}

// endCurrent ends the session new audio goes to where the audio ends now: it is sent the rest and finished.
func (j *job) endCurrent() {
	end := j.ring.end()
	switch {
	case j.cur != nil:
		j.cur.until = end
		j.cur = nil
	case j.dialing != nil:
		j.dialing.until = end
		j.dialing = nil
	default:
		if i := slices.IndexFunc(j.retry, func(r *sess) bool { return r.until < 0 }); i >= 0 {
			j.retry[i].until = end
		}
	}
	j.pump()
}

// pump sends each open session the audio it is due, at its pace, and finishes those whose audio is all sent.
func (j *job) pump() {
	now := time.Now()
	end := j.ring.end()
	for _, ss := range j.sessions {
		if ss.s == nil || ss.finish {
			continue
		}
		if ss.sentTo < j.ring.at { // fell further behind than the ring holds: that audio is lost to this session
			j.warn(extwire.TWBehind, fmt.Sprintf("session %d skipped %.1f s it could not be sent in time", ss.n, j.ring.at-ss.sentTo))
			ss.sentTo = j.ring.at
		}
		stop := ss.end(end)
		pace := ss.paceAt + j.speed*now.Sub(ss.paceT).Seconds() + paceAhead
		for ss.sentTo < stop {
			next := ss.sentTo + bytesToSec(packetBytes)
			if next > stop {
				if ss.until < 0 {
					break // a part of a packet: wait for the rest
				}
				next = stop
			}
			if next > pace {
				break
			}
			if err := ss.s.send(j.ctx, j.ring.slice(ss.sentTo, next)); err != nil {
				break // the session's reader says how it ended
			}
			ss.sentTo = next
		}
		if ss.until >= 0 && ss.sentTo >= ss.until-0.0001 {
			ss.finish = true
			if ss.sentTo-ss.at < 0.05 { // it was sent no audio: closed, not finished (nothing to hear, nothing billed)
				ss.failed = true
				ss.s.close()
				continue
			}
			ss.byT = now.Add(asrFinalWait)
			_ = ss.s.finish(j.ctx)
		}
	}
	// ack: what the current session (or, with none, the last one) has been sent.
	acked := j.lastAck
	for _, ss := range j.sessions {
		if ss.s != nil {
			acked = max(acked, ss.sentTo)
		}
	}
	if acked-j.lastAck >= ackEvery || (j.ended && acked > j.lastAck) {
		j.lastAck = acked
		_ = j.send(extwire.TranscriptionFrame{Type: extwire.TFAck, At: extwire.F(round3(acked))})
	}
}

func (j *job) onEvent(e sessEv) {
	ss := e.ss
	if e.closed {
		ss.closed = true
		j.usedMS += ss.dur
		if ss == j.cur { // its socket ended without a final result: dropped
			j.cur = nil
			if !ss.failed {
				j.dropped(ss, errors.New("the session ended without its final result"))
			}
		} else if !ss.finish && !ss.failed && ss.until >= 0 {
			j.dropped(ss, errors.New("the session ended before its audio was sent"))
		}
		j.release()
		return
	}
	if e.ev.err != nil {
		if ss.failed { // already given up on (closed by the job): nothing more to say
			return
		}
		ss.failed = true
		ss.s.close()
		if ss.finish && j.live() {
			// The service dropped a session being finished: its last sentences are lost; the phone's text fills.
			j.warn(extwire.TWSessionDropped, fmt.Sprintf("session %d dropped while finishing: %v", ss.n, e.ev.err))
			return
		}
		switch kindOf(e.ev.err) {
		case failForbidden, failNotEnabled, failVoice:
			j.fail(extwire.TEUpstreamForbidden, e.ev.err.Error())
			return
		}
		j.dropped(ss, e.ev.err)
		return
	}
	r := e.ev.res
	ss.dur = max(ss.dur, r.AudioInfo.Duration)
	if j.live() {
		j.fails = 0
		j.takeLive(ss, r, e.ev.last)
	} else if e.ev.last {
		ss.final = r
		j.takeWindow(ss)
	}
	j.release()
	if j.live() && ss == j.cur && time.Since(ss.paceT) >= liveRollover && j.q.run >= livePause {
		j.endCurrent()
	}
}

// dropped: ss failed on its way; its audio goes to a new session (again). ss stays in order, failed, so the sentences
// it gave go out in their turn; the one that takes over comes after it.
func (j *job) dropped(ss *sess, err error) {
	j.logf("session %d dropped: %v", ss.n, err)
	ss.failed = true
	until := ss.until
	if j.cur == ss {
		j.cur, until = nil, -1
	}
	j.warn(extwire.TWSessionDropped, fmt.Sprintf("session %d dropped", ss.n))
	j.again(&sess{at: ss.at, until: until, lastDef: ss.lastDef, tries: ss.tries}, err)
}

// takeLive reads a live session's result: each definite utterance not handed on yet is a sentence; the last one not
// definite is the caption, while the session takes new audio. The final result hands on what is left as sentences.
func (j *job) takeLive(ss *sess, r *asrResult, last bool) {
	for _, u := range r.Result.Utterances {
		if u.End <= ss.lastDef && ss.lastDef > 0 {
			continue
		}
		text := strings.TrimSpace(u.Text)
		if !u.Definite && !last {
			continue
		}
		ss.lastDef = max(ss.lastDef, u.End)
		if text == "" {
			continue
		}
		ss.queue = append(ss.queue, extwire.TranscriptionFrame{Type: extwire.TFSentence,
			Start: extwire.F(round3(ss.at + float64(u.Start)/1000)), End: extwire.F(round3(ss.at + float64(u.End)/1000)), Text: extwire.F(text)})
		j.interim = ""
	}
	if ss != j.cur || last {
		return
	}
	for i := len(r.Result.Utterances) - 1; i >= 0; i-- {
		u := r.Result.Utterances[i]
		if u.Definite {
			break
		}
		if text := strings.TrimSpace(u.Text); text != "" && text != j.interim {
			j.interim = text
			_ = j.send(extwire.TranscriptionFrame{Type: extwire.TFInterim, Start: extwire.F(round3(ss.at + float64(u.Start)/1000)), Text: extwire.F(text)})
		}
		break
	}
}

// takeWindow reads a final-pass window's final result: its sentences, then the window. With no text where the
// transcript of record had some, the window is kept (the hub's text stays).
func (j *job) takeWindow(ss *sess) {
	from, to := ss.at, ss.end(ss.sentTo)
	chars := 0
	for _, u := range ss.final.Result.Utterances {
		text := strings.TrimSpace(u.Text)
		if text == "" {
			continue
		}
		chars += len([]rune(text))
		ss.queue = append(ss.queue, extwire.TranscriptionFrame{Type: extwire.TFSentence,
			Start: extwire.F(round3(ss.at + float64(u.Start)/1000)), End: extwire.F(round3(min(ss.at+float64(u.End)/1000, to))), Text: extwire.F(text)})
	}
	if chars == 0 { // no utterances: the result's whole text, over the window
		if text := strings.TrimSpace(ss.final.Result.Text); text != "" {
			chars = len([]rune(text))
			ss.queue = append(ss.queue, extwire.TranscriptionFrame{Type: extwire.TFSentence, Start: extwire.F(round3(from)), End: extwire.F(round3(to)), Text: extwire.F(text)})
		}
	}
	kept := false
	if chars == 0 {
		refChars := 0
		for _, r := range j.refs {
			if r.to > from && r.from < to {
				refChars += r.chars
			}
		}
		kept = refChars >= 6 && float64(refChars) >= (to-from)/5
	}
	ss.queue = append(ss.queue, extwire.TranscriptionFrame{Type: extwire.TFWindow, From: extwire.F(round3(from)), To: extwire.F(round3(to)), Kept: kept})
	j.fails = 0
	j.logf("window %.1f–%.1f (session %d): %d characters%s", from, to, ss.n, chars, map[bool]string{true: ", kept", false: ""}[kept])
}

// release hands on the sentences of the oldest open session, and of each after it once the one before is closed;
// then each flushed whose sessions are all done.
func (j *job) release() {
	for len(j.sessions) > 0 {
		head := j.sessions[0]
		if j.retried(head.at) { // an earlier one waits to be opened again: its turn comes first
			break
		}
		for _, f := range head.queue {
			j.emit(f)
		}
		head.queue = nil
		if !head.closed && !head.failed {
			break
		}
		j.sessions = j.sessions[1:]
	}
	for len(j.flushes) > 0 {
		at := j.flushes[0]
		if len(j.sessions) > 0 && j.sessions[0].at < at-0.0001 {
			break
		}
		if j.dialing != nil && j.dialing.at < at-0.0001 || j.retried(at) {
			break
		}
		j.flushes = j.flushes[1:]
		_ = j.send(extwire.TranscriptionFrame{Type: extwire.TFFlushed, At: extwire.F(round3(at))})
	}
}

// retried: a session whose audio begins before at waits to be opened again.
func (j *job) retried(at float64) bool {
	return slices.ContainsFunc(j.retry, func(r *sess) bool { return r.at < at-0.0001 })
}

func (j *job) emit(f extwire.TranscriptionFrame) {
	if f.Type == extwire.TFSentence {
		j.sentences++
	}
	_ = j.send(f)
}

func (j *job) onTick() {
	now := time.Now()
	for _, ss := range slices.Clone(j.retry) {
		if !now.Before(ss.due) {
			j.retry = slices.DeleteFunc(j.retry, func(r *sess) bool { return r == ss })
			j.open(ss, false)
		}
	}
	j.pump()
	if j.live() && j.cur != nil {
		last := j.lastAudio
		if last.Before(j.cur.paceT) {
			last = j.cur.paceT
		}
		if now.Sub(last) > liveQuietEnd {
			j.endCurrent() // a pause: the service would end the session after 8 s with no packet
		}
	}
	if j.live() && j.cur != nil && now.Sub(j.cur.paceT) > liveRolloverMost {
		j.endCurrent()
	}
	for _, ss := range slices.Clone(j.sessions) {
		if ss.finish && !ss.closed && !ss.failed && now.After(ss.byT) {
			ss.failed = true
			ss.s.close()
			if j.live() {
				j.warn(extwire.TWSessionDropped, fmt.Sprintf("session %d gave no final result in time", ss.n))
			} else {
				j.dropped(ss, errors.New("no final result in time"))
			}
		}
	}
	j.release()
	if j.live() && !j.ready && j.started && now.Sub(j.began) > readyWait && j.fatal == nil {
		j.fail(extwire.TEUpstreamUnavailable, "no session of the service started in time")
	}
	if now.Sub(j.usageAt) >= usageEvery && j.ready {
		j.sendUsage()
	}
}

func (j *job) sendUsage() {
	j.usageAt = time.Now()
	ms := j.usedMS
	for _, ss := range j.sessions {
		if !ss.closed {
			ms += ss.dur
		}
	}
	if j.n == 0 {
		return
	}
	_ = j.send(extwire.TranscriptionFrame{Type: extwire.TFUsage, Seconds: float64(ms) / 1000, Sessions: j.n})
}

func (j *job) closeAll() {
	for _, ss := range j.sessions {
		if ss.s != nil {
			ss.s.close()
		}
	}
	j.c.CloseNow()
}

func cmpOr(a, b *sess) *sess {
	if a != nil {
		return a
	}
	return b
}

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
