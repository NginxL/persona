package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// Helpers of the hub transcription tests: a transcription contribution in the test process whose jobs a test scripts
// (fakeTranscriber, jobScript), the phone's side of the audio socket (testPhone), and a fixture with both
// (hubFixture). Every name and text is a placeholder.

// shortHubTimes makes hub transcription's times short for one test: the recording module's (shortRec, which the
// meeting fixture hands its service; recTimings otherwise) and those of the core's transcription for extensions.
func shortHubTimes(t *testing.T) {
	t.Helper()
	saved := []*time.Duration{&jobReadyWait, &jobDoneWait, &replayAhead}
	old := make([]time.Duration, len(saved))
	for i, p := range saved {
		old[i] = *p
	}
	t.Cleanup(func() {
		for i, p := range saved {
			*p = old[i]
		}
	})
	jobReadyWait, jobDoneWait, replayAhead = 2*time.Second, 5*time.Second, time.Minute
	pendingRec(t, map[string]time.Duration{
		"audioStartWait": time.Second, "audioAckEvery": 30 * time.Millisecond, "audioPingEvery": time.Minute, "audioEndWait": 3 * time.Second,
		"fallbackAfter": 300 * time.Millisecond, "jobRetry": 200 * time.Millisecond, "jobReadyWait": 2 * time.Second, "jobIdle": time.Minute,
		"jobDoneWait": 5 * time.Second, "jobAwayIdle": 10 * time.Second, "echoHoldMost": time.Minute, "hubSegmentsEvery": 50 * time.Millisecond,
		"hubTick": 10 * time.Millisecond, "interimShown": 10 * time.Second, "finalEvery": time.Hour, "finalGapWait": time.Hour,
		"askLiveWait": 3 * time.Second, "askLivePoll": 5 * time.Millisecond,
	}, map[string][]time.Duration{"finalRetries": {0, 0}, "finalUnreachableRetries": {0, 0, 0, 0, 0}})
}

// ---- the contribution ----

// fakeLine is a scripted sentence.
type fakeLine struct {
	start, end float64
	text       string
}

// fakeWindow is a scripted replay window.
type fakeWindow struct {
	from, to float64
	kept     bool
	lines    []fakeLine
}

// jobScript is what a fake job does.
type jobScript struct {
	features []string   // ready's (default live, replay, interim)
	refuse   string     // a fatal error with this code instead of ready
	noReady  bool       // never ready
	lines    []fakeLine // live: each sent once the audio passes its end (after an interim when interim)
	interim  bool
	held     bool // live: lines only on flush and end (a window still open)
	noAck    bool
	failAt   float64 // a fatal error once the audio passes this second (0: never), after the usage frame when usage
	failCode string  // failAt's code (default upstream_failed)
	dropAt   float64 // the socket dropped once the audio passes this second (0: never)
	usage    float64 // the usage frame's seconds at done
	windows  []fakeWindow
	endGate  chan struct{} // the end is answered once this is closed (nil: at once)
}

// fakeJob is one job the fake contribution ran.
type fakeJob struct {
	start  extwire.TranscriptionFrame
	mu     sync.Mutex
	frames []extwire.TranscriptionFrame // the manager's text frames after start
	audio  int64                        // samples received
	seeks  []float64
	ended  bool
	closed bool // its socket is over
}

// over says whether the job's socket is over.
func (j *fakeJob) over() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.closed
}

func (j *fakeJob) snapshot() ([]extwire.TranscriptionFrame, int64, []float64, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.frames), j.audio, slices.Clone(j.seeks), j.ended
}

// fakeTranscriber is a transcription contribution: /info, and jobs scripted by live and replay.
type fakeTranscriber struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	info     extwire.TranscriptionInfo
	infoErr  error
	infoHook func() // runs in each /info, outside the lock (a slow probe)
	dialErr  error
	install  string   // the installation of "acme/tx", the one other extension contributing (default acme@1)
	dials    []string // the refs jobs were dialed for
	live     jobScript
	replay   jobScript
	jobs     []*fakeJob
	refs     []string
}

func newFakeTranscriber(t *testing.T) *fakeTranscriber {
	f := &fakeTranscriber{t: t, info: extwire.TranscriptionInfo{Protocol: extwire.TranscriptionProtocol,
		Features: []string{extwire.TranscriptionLive, extwire.TranscriptionReplay, extwire.TranscriptionInterim}}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTranscriber) TranscriptionInfo(ctx context.Context, ref string) (extwire.TranscriptionInfo, error) {
	f.mu.Lock()
	f.refs = append(f.refs, ref)
	info, err, hook := f.info, f.infoErr, f.infoHook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return info, err
}

func (f *fakeTranscriber) TranscriptionDial(ctx context.Context, ref string) (*websocket.Conn, func(), error) {
	f.mu.Lock()
	err := f.dialErr
	f.dials = append(f.dials, ref)
	f.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.srv.URL, "http")+"/ws", nil)
	if err != nil {
		return nil, nil, err
	}
	c.SetReadLimit(1 << 20)
	return c, func() {}, nil
}

func (f *fakeTranscriber) IsTranscriptionContribution(ref string) bool {
	return ref == "" || ref == "acme/tx"
}

func (f *fakeTranscriber) TranscriptionInstall(ref string) string {
	if ref != "acme/tx" {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return cmp.Or(f.install, "acme@1")
}

// dialed are the refs jobs were dialed for so far.
func (f *fakeTranscriber) dialed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.dials)
}

func (f *fakeTranscriber) set(fn func(f *fakeTranscriber)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// jobsOf are the jobs run in mode.
func (f *fakeTranscriber) jobsOf(mode string) []*fakeJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fakeJob
	for _, j := range f.jobs {
		if j.start.Mode == mode {
			out = append(out, j)
		}
	}
	return out
}

// serve is one job.
func (f *fakeTranscriber) serve(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	ctx := r.Context()
	send := func(fr extwire.TranscriptionFrame) bool {
		raw, _ := json.Marshal(fr)
		return c.Write(ctx, websocket.MessageText, raw) == nil
	}
	typ, raw, err := c.Read(ctx)
	if err != nil || typ != websocket.MessageText {
		return
	}
	j := &fakeJob{}
	if json.Unmarshal(raw, &j.start) != nil || j.start.Type != extwire.TFStart {
		return
	}
	defer func() {
		j.mu.Lock()
		j.closed = true
		j.mu.Unlock()
	}()
	f.mu.Lock()
	f.jobs = append(f.jobs, j)
	script := f.live
	if j.start.Mode == extwire.TranscriptionReplay {
		script = f.replay
	}
	f.mu.Unlock()
	switch {
	case script.noReady:
		_, _, _ = c.Read(ctx)
		return
	case script.refuse != "":
		send(extwire.TranscriptionFrame{Type: extwire.TFError, Code: script.refuse, Fatal: true})
		return
	}
	features := script.features
	if features == nil {
		features = []string{extwire.TranscriptionLive, extwire.TranscriptionReplay, extwire.TranscriptionInterim}
	}
	if !send(extwire.TranscriptionFrame{Type: extwire.TFReady, Features: features}) {
		return
	}
	pos := int64(0)
	if j.start.At != nil {
		pos = int64(*j.start.At * kb.AudioRate)
	}
	sentLines, sentWindows, ackedTo := 0, 0, pos
	emit := func(upTo float64, all bool) bool {
		for sentLines < len(script.lines) && (all || script.lines[sentLines].end <= upTo) {
			l := script.lines[sentLines]
			if script.interim && !send(extwire.TranscriptionFrame{Type: extwire.TFInterim, Start: extwire.F(l.start), Text: extwire.F(l.text)}) {
				return false
			}
			if !send(extwire.TranscriptionFrame{Type: extwire.TFSentence, Start: extwire.F(l.start), End: extwire.F(l.end), Text: extwire.F(l.text)}) {
				return false
			}
			sentLines++
		}
		for sentWindows < len(script.windows) && (all || script.windows[sentWindows].to <= upTo) {
			win := script.windows[sentWindows]
			for _, l := range win.lines {
				if !send(extwire.TranscriptionFrame{Type: extwire.TFSentence, Start: extwire.F(l.start), End: extwire.F(l.end), Text: extwire.F(l.text)}) {
					return false
				}
			}
			if !send(extwire.TranscriptionFrame{Type: extwire.TFWindow, From: extwire.F(win.from), To: extwire.F(win.to), Kept: win.kept}) {
				return false
			}
			sentWindows++
		}
		return true
	}
	for {
		typ, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			pos += int64(len(raw) / 2)
			j.mu.Lock()
			j.audio += int64(len(raw) / 2)
			j.mu.Unlock()
			at := float64(pos) / kb.AudioRate
			if script.dropAt > 0 && at >= script.dropAt {
				return
			}
			if script.failAt > 0 && at >= script.failAt {
				if script.usage > 0 {
					send(extwire.TranscriptionFrame{Type: extwire.TFUsage, Seconds: script.usage, Sessions: 1})
				}
				send(extwire.TranscriptionFrame{Type: extwire.TFError, Code: cmp.Or(script.failCode, extwire.TEUpstreamFailed), Fatal: true})
				return
			}
			if !script.noAck && pos-ackedTo >= kb.AudioRate/2 {
				ackedTo = pos
				if !send(extwire.TranscriptionFrame{Type: extwire.TFAck, At: extwire.F(at)}) {
					return
				}
			}
			if !script.held && !emit(at, false) {
				return
			}
			continue
		}
		var fr extwire.TranscriptionFrame
		if json.Unmarshal(raw, &fr) != nil {
			return
		}
		j.mu.Lock()
		j.frames = append(j.frames, fr)
		j.mu.Unlock()
		at := float64(pos) / kb.AudioRate
		switch fr.Type {
		case extwire.TFSeek:
			if fr.At != nil {
				pos = int64(*fr.At * kb.AudioRate)
				j.mu.Lock()
				j.seeks = append(j.seeks, *fr.At)
				j.mu.Unlock()
			}
		case extwire.TFFlush:
			if !emit(at, false) || !send(extwire.TranscriptionFrame{Type: extwire.TFFlushed, At: extwire.F(at)}) {
				return
			}
		case extwire.TFEnd:
			j.mu.Lock()
			j.ended = true
			j.mu.Unlock()
			if script.endGate != nil {
				select {
				case <-script.endGate:
				case <-ctx.Done():
					return
				}
			}
			if !emit(at, j.start.Mode == extwire.TranscriptionReplay) {
				return
			}
			if script.usage > 0 {
				send(extwire.TranscriptionFrame{Type: extwire.TFUsage, Seconds: script.usage, Sessions: 3})
			}
			send(extwire.TranscriptionFrame{Type: extwire.TFDone})
			_ = c.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
}

// ---- the phone ----

// testPhone is the phone's side of one audio socket.
type testPhone struct {
	t      *testing.T
	c      *websocket.Conn
	mu     sync.Mutex
	frames []map[string]any
	closed bool
	status websocket.StatusCode
}

// dialPhone opens recording id's audio socket on srv.
func dialPhone(t *testing.T, srv *httptest.Server, id string) *testPhone {
	t.Helper()
	c, resp, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/recordings/"+id+"/audio", nil)
	if err != nil {
		t.Fatalf("audio socket: %v (%v)", err, resp)
	}
	p := &testPhone{t: t, c: c}
	go p.read()
	t.Cleanup(func() { _ = c.CloseNow() })
	return p
}

func (p *testPhone) read() {
	for {
		_, raw, err := p.c.Read(context.Background())
		if err != nil {
			p.mu.Lock()
			p.closed, p.status = true, websocket.CloseStatus(err)
			p.mu.Unlock()
			return
		}
		var f map[string]any
		_ = json.Unmarshal(raw, &f)
		p.mu.Lock()
		p.frames = append(p.frames, f)
		p.mu.Unlock()
	}
}

func (p *testPhone) send(v any) {
	p.t.Helper()
	raw, _ := json.Marshal(v)
	if err := p.c.Write(context.Background(), websocket.MessageText, raw); err != nil {
		p.t.Fatalf("send %s: %v", raw, err)
	}
}

// audio sends seconds of audio (silence) in 100 ms frames.
func (p *testPhone) audio(seconds float64) {
	p.t.Helper()
	frame := make([]byte, kb.AudioRate/10*2)
	for n := int(seconds * 10); n > 0; n-- {
		if err := p.c.Write(context.Background(), websocket.MessageBinary, frame); err != nil {
			p.t.Fatalf("audio: %v", err)
		}
	}
}

// of are the frames of type typ the hub sent so far.
func (p *testPhone) of(typ string) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []map[string]any
	for _, f := range p.frames {
		if f["type"] == typ {
			out = append(out, f)
		}
	}
	return out
}

// wait is the n-th frame (from 1) of type typ, once it came.
func (p *testPhone) wait(typ string, n int) map[string]any {
	p.t.Helper()
	var got map[string]any
	waitFor(p.t, fmt.Sprintf("%d %s frame(s)", n, typ), func() bool {
		fs := p.of(typ)
		if len(fs) >= n {
			got = fs[n-1]
			return true
		}
		return false
	})
	return got
}

// waitClosed is the close status once the hub closed the socket.
func (p *testPhone) waitClosed() websocket.StatusCode {
	p.t.Helper()
	waitFor(p.t, "the socket closed", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.closed
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// texts are the lines' texts the hub sent.
func (p *testPhone) texts() []string {
	var out []string
	for _, f := range p.of("line") {
		out = append(out, f["text"].(string))
	}
	return out
}

// ---- the fixture ----

// hubFixture is a meeting fixture whose manager reaches a fake transcription contribution, with the audio socket
// served.
type hubFixture struct {
	finalOnce sync.Once // finalState's loop
	*meetingFixture
	tr      *fakeTranscriber
	sockets *httptest.Server
}

func newHubFixture(t *testing.T) *hubFixture {
	t.Helper()
	shortHubTimes(t)
	m := newMeetingFixture(t)
	tr := newFakeTranscriber(t)
	m.s.trans = tr
	m.s.transcriptionReadinessChanged()
	sockets := httptest.NewServer(http.HandlerFunc(m.s.ServeSocket))
	h := &hubFixture{meetingFixture: m, tr: tr, sockets: sockets}
	t.Cleanup(func() {
		sockets.Close()
		h.stopRuns()
	})
	return h
}

// stopRuns ends every run (their clocks and jobs) and waits for hub transcription's goroutines.
func (h *hubFixture) stopRuns() { recWait(h.t, h.s) }

// hubUpload uploads recording id in hub mode (the role, more fields over the meeting fixture's).
func (h *hubFixture) hubUpload(id string, rev int, fields map[string]any) map[string]any {
	h.t.Helper()
	body := map[string]any{"transcription": "hub", "attendance": "listener"}
	for k, v := range fields {
		body[k] = v
	}
	return h.upload(id, rev, body)
}

// open opens recording id's socket and starts it at at: ready is its first frame.
func (h *hubFixture) open(id string, at float64, timeline any) (*testPhone, map[string]any) {
	h.t.Helper()
	p := dialPhone(h.t, h.sockets, id)
	start := map[string]any{"type": "start", "at": at}
	if timeline != nil {
		start["timeline"] = timeline
	}
	p.send(start)
	ready := p.wait("ready", 1)
	return p, ready
}

// transcription is recording id's transcription as GET …/live answers it.
func (h *hubFixture) transcription(id string) map[string]any {
	h.t.Helper()
	out := h.do("GET", livePath(id)+"?parts=transcription", "", nil, http.StatusOK)
	tr, _ := out["transcription"].(map[string]any)
	return tr
}

// lines are recording id's live log as texts and sources.
func (h *hubFixture) lines(id string) []kb.LiveSegment {
	h.t.Helper()
	l, _, err := h.engine().LiveRead(id)
	if err != nil {
		h.t.Fatal(err)
	}
	return l.Segments
}

func lineTexts(segs []kb.LiveSegment) []string {
	out := make([]string, 0, len(segs))
	for _, sg := range segs {
		out = append(out, sg.Text)
	}
	return out
}

// hubTexts are recording id's hub transcript's texts.
func (h *hubFixture) hubTexts(id string) []string {
	h.t.Helper()
	ht, err := h.engine().HubTranscript(id)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, sg := range ht.Segments {
		out = append(out, sg.Text)
	}
	return out
}

// hubEcho are the echo marks of recording id's hub transcript, in its order.
func (h *hubFixture) hubEcho(id string) []bool {
	h.t.Helper()
	ht, err := h.engine().HubTranscript(id)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []bool
	for _, sg := range ht.Segments {
		out = append(out, sg.Echo)
	}
	return out
}

// errDial is a contribution that cannot be reached.
var errDial = errors.New("no contribution running")

// liveEventsOf are the meeting_live events of recording id sent so far.
func (f *fakeHub) liveEventsOf(id string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, ev := range f.liveEvents {
		if ev["recording_id"] == id {
			out = append(out, ev)
		}
	}
	return out
}

// finalState waits until recording id's transcription is in state want: the final passes' loop runs, looking every
// 20 ms (finalEvery), as the manager runs it.
func (h *hubFixture) finalState(id, want string) {
	h.t.Helper()
	h.finalOnce.Do(func() {
		recTimings(h.t, h.s, map[string]time.Duration{"finalEvery": 20 * time.Millisecond}, nil)
		ctx, stop := context.WithCancel(context.Background())
		h.t.Cleanup(stop)
		go h.s.finalPasses(ctx)
	})
	waitFor(h.t, "state "+want, func() bool { return h.transcription(id)["state"] == want })
}
