package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// A transcription job (docs/assistant/meeting-minutes.md "The `transcription` extension point", "The built-in
// adapter"): the manager's frames in (start, audio, seek, flush, reference, end), sentences out, in order and in
// recording seconds.
//
// Live: one caption session and two verbatim sessions (three when there are no captions). The audio is cut into
// windows of about trWindow, closed at the first silence after it (at once on flush). Once every session has had the
// window's audio and a little more (its replies come about 1.5 s after a sentence ends), the window's text is chosen
// (pickLive) and its sentences are sent, spread over its speech. A session dropped in two windows in a row is replaced;
// one whose connection drops is opened again, up to three times in a row, and given the audio from the first window
// still to be sent; one near the upstream's session limit is rolled over.
//
// Replay (the final pass): the stored audio at the job's speed through fresh verbatim sessions per window of at most
// trReplayWindowMost, cut at silences and where the reference starts or stops covering speech; each window's
// sessions are told the previous window's last sentences. The window's text is checked against the reference
// (pickReplay), then its sentences and a window frame are sent.

// Job modes.
const (
	modeLive   = extwire.TranscriptionLive
	modeReplay = extwire.TranscriptionReplay
)

// The job's numbers (variables so tests can shorten them). Audio lengths are of recording time.
var (
	trStartWait = 10 * time.Second // the manager's start frame
	trReadyWait = 30 * time.Second // live: a caption and a verbatim session started
	trTick      = 50 * time.Millisecond
	trAckEvery  = 5 * time.Second // of audio taken
	trUsageMost = 30 * time.Second

	trWindow        = 15 * time.Second // live: a window closes at the first silence after this much audio
	trWindowMost    = 45 * time.Second // and at the latest after this much
	trWindowGap     = 500 * time.Millisecond
	trSettle        = 3 * time.Second         // of a session's audio after a window's end before the window is its
	trSettleWall    = 1500 * time.Millisecond // and that long after it got the window's end
	trCaptionSettle = 1500 * time.Millisecond // captions settle about 1.5 s after a sentence ends
	trWindowWait    = 12 * time.Second        // the most a closed window waits for a session that is behind
	trReplyWait     = 5 * time.Second         // and at most this long for a session's reply to its last speech
	trStall         = time.Second             // no audio this long while a window waits: silence ends the sessions' turns
	trFlushSilence  = 1500 * time.Millisecond
	trSessionMost   = 25 * time.Minute // rolled over before the upstream's session limit
	trExpiryMargin  = 2 * time.Minute  // and at the latest this long before the session ends (its expires_at)
	trRollOverlap   = 10 * time.Second // and the session rolled over runs beside the new one at least this long
	trRollRetry     = 30 * time.Second
	trAssignAhead   = 300 * time.Millisecond // a reply may be of speech that ended this long after its position (assign)
	trInterimLag    = time.Second            // a caption session this far behind the audio sends no interim captions
	trCaptionLag    = 500 * time.Millisecond // a caption's first frame comes about this long after its sentence began
	trCaptionFresh  = time.Second            // a caption's frames this soon after its first one revise it (captionOf)
	trBehindEvery   = time.Minute
	// trReconnectWaits are the waits before the reconnects of a session in a row (maxReconnects of them).
	trReconnectWaits = []time.Duration{0, 2 * time.Second, 4 * time.Second}

	trReplayWindow     = 5 * time.Minute // replay: a window is cut at the first silence after this
	trReplayWindowMost = 6 * time.Minute // and at the latest after this,
	trReplayOver       = 30 * time.Second
	trRefGap           = 10 * time.Second // speech with no reference this long makes a window of its own
	trRefPad           = time.Second
	trReplayLead       = time.Second      // of silence a window starts with before its speech
	trReplayTail       = 2 * time.Second  // of silence after a window's audio: its last turn's end
	trReplayQuiet      = 3 * time.Second  // a session that said nothing this long after its audio is done
	trReplayTailMost   = 30 * time.Second // the most a window waits for its sessions after its audio
	trStopEarly        = 10 * time.Second // of speech after a session's last reply: it stopped early
	trReplayWithRef    = 2
	trReplayNoRef      = 3
	trPreviousMost     = 3 // sentences of the previous window a replay window's sessions are told
)

// The glossary's limits (docs/assistant/meeting-minutes.md "The `transcription` extension point").
const (
	trGlossaryMost     = 256
	trGlossaryTermMost = 80
)

// trIn is one frame from the manager.
type trIn struct {
	bin []byte
	f   extwire.TranscriptionFrame
	bad string // not a frame: why
	err error  // the socket is gone
}

// trJob is one job socket.
type trJob struct {
	pl     *Plugin
	id     int64
	c      *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	began  time.Time
	uiLang i18n.Lang

	in    chan trIn
	ups   chan trUp
	reads chan trRead

	p        *Provider
	model    string
	fake     *trFake
	mode     string
	speed    float64
	lang     string // the instructions' language
	textLang string // the recording's language tag: a hint
	glossary []string

	buf     *trBuffer
	vad     trVAD
	startAt int64
	at      int64 // where the next binary frame goes
	edge    int64 // the end of the audio taken
	any     bool
	heardAt time.Time
	stalled bool
	acked   int64

	// live
	slots     []*trSlot
	windows   []*trWin // not sent yet, in order; the last is open while audio comes
	nextWin   int
	flushes   []int64
	readySent bool
	noInterim bool
	firstErr  error
	behindAt  time.Time

	// replay
	refs     []trRef
	holes    [][2]int64
	rw       *trReplayWin
	nextFrom int64
	replayN  int
	prev     []string

	all       []*trSession // started and not over
	opened    int
	started   int
	usageDone float64
	usageSent float64
	usageAt   time.Time

	sentences, windowsDone, reconnects, replaced int
	ending, finishing                            bool
	finishBy                                     time.Time
	over                                         bool
	outcome                                      string
}

func (j *trJob) logf(format string, args ...any) {
	j.pl.logf("transcription %d: "+format, append([]any{j.id}, args...)...)
}

func (j *trJob) memo() *trMemo { return trMemoOf(j.p) }

// run is the job's loop: everything but the sessions' sockets happens here.
func (j *trJob) run() {
	defer j.cleanup()
	go j.readManager()
	first, ok := j.firstFrame()
	if !ok {
		return
	}
	if !j.begin(first) {
		return
	}
	tick := time.NewTicker(trTick)
	defer tick.Stop()
	for !j.over {
		select {
		case m := <-j.in:
			j.fromManager(m)
		case u := <-j.ups:
			j.sessionUp(u)
		case r := <-j.reads:
			j.fromSession(r)
		case <-tick.C:
			j.tick()
		}
	}
}

func (j *trJob) cleanup() {
	j.cancel()
	for _, s := range j.all {
		s.close(true)
	}
	for _, sl := range j.slots {
		for _, s := range append([]*trSession{sl.cur, sl.pending}, sl.old...) {
			if s != nil {
				s.close(true)
			}
		}
	}
	if j.rw != nil {
		for _, s := range j.rw.sessions {
			s.close(true)
		}
	}
	_ = j.c.CloseNow()
	usage := j.usageDone
	for _, s := range j.all {
		usage += s.usage
	}
	j.logf("over after %v (%s): windows %d, sentences %d, sessions %d, reconnects %d, replaced %d, usage %.1f s",
		logDur(time.Since(j.began)), firstNonEmpty(j.outcome, "ended"), j.windowsDone, j.sentences, j.started, j.reconnects,
		j.replaced, usage)
}

// readManager passes the manager's frames to the loop.
func (j *trJob) readManager() {
	for {
		typ, b, err := j.c.Read(j.ctx)
		var m trIn
		switch {
		case err != nil:
			m.err = err
		case typ == websocket.MessageBinary:
			m.bin = b
		default:
			if json.Unmarshal(b, &m.f) != nil || m.f.Type == "" {
				m.bad = "a text frame that is not a JSON frame"
			}
		}
		select {
		case j.in <- m:
		case <-j.ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// firstFrame waits for the manager's start.
func (j *trJob) firstFrame() (extwire.TranscriptionFrame, bool) {
	t := time.NewTimer(trStartWait)
	defer t.Stop()
	select {
	case m := <-j.in:
		switch {
		case m.err != nil:
			j.outcome = "the manager hung up before start"
			return m.f, false
		case m.bad != "" || m.bin != nil || m.f.Type != extwire.TFStart:
			j.fatal(extwire.TEBadFrame, "the first frame is start")
			return m.f, false
		}
		return m.f, true
	case <-t.C:
		j.fatal(extwire.TEBadFrame, "no start frame in time")
		return extwire.TranscriptionFrame{}, false
	}
}

// begin takes the start frame: the mode, where the audio starts, the hints; then the upstream, and the sessions.
func (j *trJob) begin(f extwire.TranscriptionFrame) bool {
	switch f.Mode {
	case modeLive, modeReplay:
		j.mode = f.Mode
	default:
		j.fatal(extwire.TEBadFrame, "start.mode is live or replay")
		return false
	}
	if f.At != nil && *f.At > 0 && !math.IsInf(*f.At, 0) {
		j.startAt = int64(math.Round(*f.At * trRate))
	}
	j.at, j.edge, j.nextFrom, j.acked = j.startAt, j.startAt, j.startAt, j.startAt
	if j.mode == modeReplay {
		j.speed = 4
		if f.Speed > 0 && !math.IsInf(f.Speed, 0) {
			j.speed = f.Speed
		}
	}
	if langTag.MatchString(f.Language) {
		j.textLang = f.Language
	}
	j.glossary = cleanGlossary(f.Glossary)
	j.lang = string(trInstructionsLang(j.textLang, j.uiLang, j.pl.link.language()))

	ctx, cancel := context.WithTimeout(j.ctx, startTimeout)
	p, model, err := j.pl.transcriber(ctx)
	cancel()
	if err != nil {
		j.logf("opened (%s): no upstream: %s", j.mode, fetchFailure(err))
		j.fatalErr(err)
		return false
	}
	j.p, j.model = p, model
	hint := "none"
	if j.textLang != "" {
		hint = j.textLang
	}
	j.logf("opened (%s, from %.1f s, language hint %s, %d glossary terms): %s/%s (%s dialect)", j.mode, trSeconds(j.startAt), hint,
		len(j.glossary), p.ID, model, p.Dialect)
	if p.Dialect == DialectFake {
		j.fake = loadTrFake(j.logf)
	}
	j.heardAt = time.Now()
	j.usageAt = time.Now()
	if j.mode == modeReplay {
		j.buf = newTrBuffer(0)
		features := []string{extwire.TranscriptionReplay}
		return j.send(extwire.TranscriptionFrame{Type: extwire.TFReady, Features: features})
	}
	j.buf = newTrBuffer(trSamples(trKeep))
	j.windows = []*trWin{{n: 0, from: j.startAt, to: -1}}
	j.nextWin = 1
	if j.fake != nil {
		j.readySent = true
		return j.send(extwire.TranscriptionFrame{Type: extwire.TFReady,
			Features: []string{extwire.TranscriptionLive, extwire.TranscriptionInterim}})
	}
	verbatim := 2
	if j.memo().captions.Load() == trNotGranted {
		j.noInterim = true
		verbatim = 3
		j.logf("captions: the upstream gives none (known): %d verbatim sessions", verbatim)
	} else {
		sl := &trSlot{kind: trCaption}
		j.slots = append(j.slots, sl)
		j.open(sl, 0, "first")
	}
	for range verbatim {
		sl := &trSlot{kind: trVerbatim}
		j.slots = append(j.slots, sl)
		j.open(sl, 0, "first")
	}
	return true
}

// trInstructionsLang is the language of a job's instructions: the recording's (tag) when it is Chinese, English or
// Japanese, since the meeting's language is what the model hears, else the hub's (the caller's, else the hub link's),
// else Chinese.
func trInstructionsLang(tag string, caller, hub i18n.Lang) i18n.Lang {
	if prompts.ChineseScript(tag) != "" {
		return i18n.ZhHans
	}
	for _, l := range []i18n.Lang{i18n.Match(tag), caller, hub} {
		if l != "" {
			return l
		}
	}
	return i18n.ZhHans
}

// cleanGlossary is the start's glossary as the instructions take it: each term on one line, trimmed and cut to
// trGlossaryTermMost, blanks and repeats (case-insensitive) left out, at most trGlossaryMost.
func cleanGlossary(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		t = strings.Join(strings.Fields(t), " ")
		if r := []rune(t); len(r) > trGlossaryTermMost {
			t = strings.TrimSpace(string(r[:trGlossaryTermMost]))
		}
		k := strings.ToLower(t)
		if t == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
		if len(out) == trGlossaryMost {
			break
		}
	}
	return out
}

// instructions are a session's: the caption session's, or a verbatim one's with the glossary and (replay) the
// previous sentences, the glossary's last terms left out as far as it takes to keep within instructionsMost.
func (j *trJob) instructions(kind string, previous []string) string {
	d := prompts.VoiceTranscribe{Lang: j.lang, TextLang: j.textLang, Glossary: j.glossary, Previous: previous}
	if kind == trCaption {
		return prompts.RenderVoiceTranscribeCaption(d)
	}
	s := prompts.RenderVoiceTranscribeVerbatim(d)
	for runeLen(s) > instructionsMost && (len(d.Glossary) > 0 || len(d.Previous) > 0) {
		if len(d.Glossary) > 0 {
			d.Glossary = d.Glossary[:len(d.Glossary)*9/10]
		} else {
			d.Previous = d.Previous[1:]
		}
		s = prompts.RenderVoiceTranscribeVerbatim(d)
	}
	return s
}

// send writes f to the manager; false (and the job is over) when that fails.
func (j *trJob) send(f extwire.TranscriptionFrame) bool {
	if j.over {
		return false
	}
	ctx, cancel := context.WithTimeout(j.ctx, upWriteTimeout)
	defer cancel()
	if err := writeJSON(ctx, j.c, f); err != nil {
		j.over = true
		j.outcome = "writing to the manager failed"
		return false
	}
	return true
}

func (j *trJob) warn(code, msg string) {
	j.send(extwire.TranscriptionFrame{Type: extwire.TFWarning, Code: code, Message: msg})
}

// fatal sends a fatal error and ends the job.
func (j *trJob) fatal(code, msg string) {
	j.logf("fatal error %s: %s", code, msg)
	j.send(extwire.TranscriptionFrame{Type: extwire.TFError, Code: code, Message: msg, Fatal: true})
	status := websocket.StatusInternalError
	if code == extwire.TEBadFrame {
		status = websocket.StatusPolicyViolation
	}
	_ = j.c.Close(status, code)
	j.over = true
	j.outcome = "fatal error " + code
}

// fatalErr ends the job with err's code: no_upstream, upstream_forbidden, upstream_unavailable, else upstream_failed.
// The message never has the service's words (they may quote what was sent).
func (j *trJob) fatalErr(err error) {
	code := errCode(err)
	var hs *httpStatusError
	if errors.As(err, &hs) {
		switch hs.status {
		case 401, 403:
			code = codeUpstreamForbidden
		case 503:
			code = codeUpstreamUnavailable
		}
	}
	if errors.Is(err, errNoLiveUpstream) {
		code = codeNoUpstream
	}
	var msg string
	switch code {
	case codeNoUpstream:
		msg = "no GPT-Live-compatible upstream is set up"
	case codeUpstreamForbidden:
		msg = "the transcription service refused the key"
	case codeUpstreamUnavailable:
		msg = "the transcription service is unavailable for the moment"
	default:
		code, msg = codeUpstreamFailed, "the transcription service failed"
	}
	j.fatal(code, msg)
}

// secs is n samples as the frames' seconds, to the millisecond.
func secs(n int64) *float64 { return extwire.F(math.Round(float64(n)/trRate*1000) / 1000) }

func (j *trJob) fromManager(m trIn) {
	switch {
	case m.err != nil:
		j.over = true
		if s := websocket.CloseStatus(m.err); s != -1 {
			j.outcome = fmt.Sprintf("the manager hung up (%d)", s)
		} else {
			j.outcome = "the manager's connection dropped"
		}
		return
	case m.bad != "":
		j.fatal(extwire.TEBadFrame, m.bad)
		return
	case m.bin != nil:
		if len(m.bin)%2 != 0 {
			j.fatal(extwire.TEBadFrame, "a binary frame of an odd length")
			return
		}
		at := j.at
		j.at += int64(len(m.bin) / 2)
		j.take(at, m.bin)
		return
	}
	f := m.f
	switch f.Type {
	case extwire.TFSeek:
		if f.At != nil && *f.At >= 0 && !math.IsInf(*f.At, 0) {
			j.at = int64(math.Round(*f.At * trRate))
		}
	case extwire.TFFlush:
		j.flush()
	case extwire.TFReference:
		if f.From != nil && f.To != nil && f.Text != nil && *f.To >= *f.From {
			j.refs = append(j.refs, trRef{from: int64(*f.From * trRate), to: int64(*f.To * trRate), text: *f.Text})
		}
	case extwire.TFEnd:
		j.endJob()
	}
}

// take stores audio from at on; what the job already has is left out, and a jump over a hole ends the window before it.
func (j *trJob) take(at int64, pcm []byte) {
	if j.ending {
		return
	}
	if j.any && at < j.edge {
		skip := j.edge - at
		if skip*2 >= int64(len(pcm)) {
			return
		}
		pcm, at = pcm[skip*2:], j.edge
	}
	if j.any && at > j.edge {
		j.hole(j.edge, at)
	} else if !j.any && at > j.startAt {
		j.hole(j.startAt, at)
	}
	j.buf.add(at, pcm)
	j.vad.add(at, pcm)
	j.edge = at + int64(len(pcm)/2)
	j.any, j.heardAt, j.stalled = true, time.Now(), false
	if j.mode == modeLive {
		j.liveAudio()
		if j.edge-j.acked >= trSamples(trAckEvery) {
			j.ack(j.edge)
		}
		return
	}
	j.replayStep()
}

func (j *trJob) ack(at int64) {
	if at > j.acked {
		j.acked = at
		j.send(extwire.TranscriptionFrame{Type: extwire.TFAck, At: secs(at)})
	}
}

// hole: the audio jumps from from to to.
func (j *trJob) hole(from, to int64) {
	if j.mode == modeReplay {
		j.holes = append(j.holes, [2]int64{from, to})
		if rw := j.rw; rw != nil && rw.to < 0 && from > rw.from && from < rw.cap {
			rw.cap, rw.edgeCut = from, true
			for _, s := range rw.sessions {
				s.limit.Store(from)
				s.wake()
			}
		}
		return
	}
	w := j.openWin()
	if w == nil {
		return
	}
	if w.from < from {
		j.closeWin(from)
		j.forceClosed()
		j.silenceAll(from)
		w = j.openWin()
	}
	if w != nil {
		w.from = to
	}
}

func (j *trJob) tick() {
	now := time.Now()
	if j.finishing {
		if now.After(j.finishBy) {
			j.complete()
		}
		return
	}
	if j.mode == modeReplay {
		j.replayStep()
		j.replayAck()
	} else {
		if !j.readySent && now.Sub(j.began) >= trReadyWait {
			j.fatal(codeUpstreamUnavailable, "no transcription session started in time")
			return
		}
		closed := len(j.windows) > 0 && j.windows[0].to >= 0
		if closed && j.fake == nil && !j.stalled && now.Sub(j.heardAt) >= trStall {
			j.stalled = true
			j.forceClosed()
			j.silenceAll(j.edge)
		}
		for _, sl := range j.slots {
			switch {
			case sl.dead || sl.pending != nil || j.ending:
			case sl.cur == nil && !sl.retryAt.IsZero() && !now.Before(sl.retryAt):
				sl.retryAt = time.Time{}
				j.open(sl, j.firstOpen(), fmt.Sprintf("reconnect %d", sl.tries))
			case sl.cur != nil && sl.cur.rollDue(now) && !now.Before(sl.rollAt):
				j.open(sl, j.nextWin-1, "rollover")
			}
		}
		j.finalize()
	}
	if !j.over && now.Sub(j.usageAt) >= trUsageMost {
		j.sendUsage(false)
	}
}

// sendUsage sends the upstream's billed seconds so far, of every session of the job.
func (j *trJob) sendUsage(always bool) {
	total := j.usageDone
	for _, s := range j.all {
		total += s.usage
	}
	j.usageAt = time.Now()
	if !always && total == j.usageSent {
		return
	}
	j.usageSent = total
	j.send(extwire.TranscriptionFrame{Type: extwire.TFUsage, Seconds: math.Round(total*10) / 10, Sessions: j.started})
}

// finish closes the sessions politely (their usage comes with session.closed), then complete.
func (j *trJob) finish() {
	j.finishing, j.finishBy = true, time.Now().Add(3*time.Second)
	for _, s := range j.all {
		s.close(false)
	}
	for _, sl := range j.slots {
		if sl.pending != nil {
			sl.pending = nil
		}
	}
	if len(j.all) == 0 {
		j.complete()
	}
}

// complete: every sentence is sent; usage, done, and the socket closes.
func (j *trJob) complete() {
	if j.over {
		return
	}
	j.sendUsage(true)
	if j.send(extwire.TranscriptionFrame{Type: extwire.TFDone}) {
		_ = j.c.Close(websocket.StatusNormalClosure, "")
	}
	j.over = true
	j.outcome = "done"
}
