package volc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modules/volc/volctest"
)

// fake is a stand-in service the package's clients reach for the test.
func fake(t *testing.T) *volctest.Service {
	t.Helper()
	s := volctest.New(t)
	oldASR, oldTTS := asrBase, ttsURL
	asrBase, ttsURL = s.ASRBase(), s.TTSURL()
	t.Cleanup(func() { asrBase, ttsURL = oldASR, oldTTS })
	return s
}

// line is a sentence of a script for the stand-in: its number, seconds of speech, seconds of silence after it.
func line(n int, speech, silence float64) volctest.Line {
	return volctest.Line{N: n, Speech: speech, Silence: silence}
}

// fast shortens the job's waits for a test.
func fast(t *testing.T) {
	t.Helper()
	old := []any{liveQuietEnd, jobTick, asrFinalWait, readyWait, retryDelays, liveSpeed, asrAnswerWait}
	liveQuietEnd, jobTick, asrFinalWait, readyWait = 400*time.Millisecond, 10*time.Millisecond, 3*time.Second, 3*time.Second
	retryDelays = []time.Duration{0, 30 * time.Millisecond, 60 * time.Millisecond}
	liveSpeed, asrAnswerWait = 1000, 3*time.Second
	t.Cleanup(func() {
		liveQuietEnd, jobTick, asrFinalWait, readyWait = old[0].(time.Duration), old[1].(time.Duration), old[2].(time.Duration), old[3].(time.Duration)
		retryDelays, liveSpeed, asrAnswerWait = old[4].([]time.Duration), old[5].(float64), old[6].(time.Duration)
	})
}

func testPlugin(t *testing.T, form Form, key string) *Plugin {
	t.Helper()
	pl := New(form, key, i18n.ZhHans, log.New(io.Discard, "", 0))
	if testing.Verbose() {
		pl.logger = log.New(&testLog{t}, "", 0)
	}
	return pl
}

type testLog struct{ t *testing.T }

func (l *testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// manager is the manager's side of one job.
type manager struct {
	t      *testing.T
	c      *websocket.Conn
	frames chan extwire.TranscriptionFrame
}

func dialJob(t *testing.T, pl *Plugin) *manager {
	t.Helper()
	srv := httptest.NewServer(pl.Handler())
	t.Cleanup(srv.Close)
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/transcription/volc/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	m := &manager{t: t, c: c, frames: make(chan extwire.TranscriptionFrame, 1024)}
	go func() {
		defer close(m.frames)
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			var f extwire.TranscriptionFrame
			if err := json.Unmarshal(b, &f); err != nil {
				t.Errorf("frame %s: %v", b, err)
				return
			}
			m.frames <- f
		}
	}()
	return m
}

func (m *manager) send(f extwire.TranscriptionFrame) {
	m.t.Helper()
	if err := m.write(f); err != nil {
		m.t.Fatal(err)
	}
}

func (m *manager) write(f extwire.TranscriptionFrame) error {
	m.t.Helper()
	b, _ := json.Marshal(f)
	return m.c.Write(context.Background(), websocket.MessageText, b)
}

// audio sends pcm in frames of 100 ms.
func (m *manager) audio(pcm []byte) {
	m.t.Helper()
	if err := m.writeAudio(pcm); err != nil {
		m.t.Fatal(err)
	}
}

func (m *manager) writeAudio(pcm []byte) error {
	m.t.Helper()
	for len(pcm) > 0 {
		n := min(len(pcm), bytesPerSec/10)
		if err := m.c.Write(context.Background(), websocket.MessageBinary, pcm[:n]); err != nil {
			return err
		}
		pcm = pcm[n:]
	}
	return nil
}

// next reads the next frame, failing after a while.
func (m *manager) next() extwire.TranscriptionFrame {
	m.t.Helper()
	select {
	case f, ok := <-m.frames:
		if !ok {
			m.t.Fatal("the job closed")
		}
		return f
	case <-time.After(10 * time.Second):
		m.t.Fatal("no frame in 10 s")
	}
	return extwire.TranscriptionFrame{}
}

// until reads frames until one of type typ (returned with those before it), failing after a while.
func (m *manager) until(typ string) (got []extwire.TranscriptionFrame, last extwire.TranscriptionFrame) {
	m.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case f, ok := <-m.frames:
			if !ok {
				m.t.Fatalf("the job closed before %s; frames: %s", typ, frameList(got))
			}
			if f.Type == typ {
				return got, f
			}
			got = append(got, f)
		case <-timeout:
			m.t.Fatalf("no %s; frames: %s", typ, frameList(got))
		}
	}
}

func frameList(fs []extwire.TranscriptionFrame) string {
	var b strings.Builder
	for _, f := range fs {
		raw, _ := json.Marshal(f)
		b.Write(raw)
		b.WriteByte(' ')
	}
	return b.String()
}

func sentencesOf(fs []extwire.TranscriptionFrame) []extwire.TranscriptionFrame {
	return slices.DeleteFunc(slices.Clone(fs), func(f extwire.TranscriptionFrame) bool { return f.Type != extwire.TFSentence })
}

func texts(fs []extwire.TranscriptionFrame) []string {
	var out []string
	for _, f := range sentencesOf(fs) {
		out = append(out, *f.Text)
	}
	return out
}

func near(a, b float64) bool { return a-b < 0.05 && b-a < 0.05 }

func TestInfo(t *testing.T) {
	srv := httptest.NewServer(testPlugin(t, Form{}, "").Handler())
	defer srv.Close()
	get := func(path, role string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if role != "" {
			req.Header.Set(extwire.HeaderRole, role)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v map[string]any
		_ = json.NewDecoder(res.Body).Decode(&v)
		return res.StatusCode, v
	}
	if code, v := get("/transcription/volc/info", ""); code != 200 || v["setup"] != "key" || v["protocol"] != 1.0 {
		t.Errorf("info without a key: %d %v", code, v)
	}
	if code, _ := get("/transcription/gpt-live/info", ""); code != 404 {
		t.Errorf("another contribution: %d", code)
	}
	if code, _ := get("/transcription/volc/info", "answer"); code != 403 {
		t.Errorf("an answer-only device: %d", code)
	}
	if code, v := get("/speech/volc/info", "full"); code != 200 || v["setup"] != "key" || v["rate"] != 24000.0 {
		t.Errorf("speech info without a key: %d %v", code, v)
	}

	off := false
	srv2 := httptest.NewServer(testPlugin(t, Form{ReadAnswers: &off}, "k").Handler())
	defer srv2.Close()
	res, _ := http.Get(srv2.URL + "/speech/volc/info")
	var v map[string]any
	_ = json.NewDecoder(res.Body).Decode(&v)
	res.Body.Close()
	if v["setup"] != "off" {
		t.Errorf("reading answers off: %v", v)
	}
	res, _ = http.Get(srv2.URL + "/transcription/volc/info")
	v = nil
	_ = json.NewDecoder(res.Body).Decode(&v)
	res.Body.Close()
	if _, ok := v["setup"]; ok || !slices.Contains(toStrings(v["features"]), "interim") {
		t.Errorf("transcription with a key: %v", v)
	}

	// A contribution switched off is not there.
	pl := testPlugin(t, Form{}, "k")
	pl.active = map[string][]string{"transcription": {"volc"}}
	srv3 := httptest.NewServer(pl.Handler())
	defer srv3.Close()
	res, _ = http.Get(srv3.URL + "/speech/volc/info")
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("speech switched off: %d", res.StatusCode)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// A live job: ready once a session started, captions while a sentence is said, each sentence once it ends, in
// recording seconds from start.at; a flush hands on what was said before it; end finishes with done.
func TestLiveJob(t *testing.T) {
	fast(t)
	fv := fake(t)
	pl := testPlugin(t, Form{}, "test-key")
	m := dialJob(t, pl)
	m.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: "live", Language: "zh-CN", At: extwire.F(100.0),
		Glossary: []string{"季度复盘会议（销售部）", "张三", "Persona", "张三"}})
	_, ready := m.until(extwire.TFReady)
	if !slices.Equal(ready.Features, []string{"live", "interim"}) {
		t.Errorf("ready features %v", ready.Features)
	}
	m.audio(volctest.PCM(line(1, 1.0, 0.6), line(2, 0.8, 0.6)))
	got, _ := m.until(extwire.TFSentence)
	got2, second := m.until(extwire.TFSentence)
	got = append(got, got2...)
	if s := sentencesOf(append(got, second)); len(s) != 1 || *second.Text != volctest.Sentence(2) { // i18n-ignore: test data
		t.Errorf("second sentence %v (before: %s)", *second.Text, frameList(got))
	}
	if !near(*second.Start, 101.6) || !near(*second.End, 102.4) {
		t.Errorf("second sentence at %v–%v, want 101.6–102.4", *second.Start, *second.End)
	}
	var interim int
	for _, f := range got {
		if f.Type == extwire.TFInterim {
			interim++
		}
	}
	if interim == 0 {
		t.Errorf("no interim caption: %s", frameList(got))
	}
	m.audio(volctest.PCM(line(3, 0.6, 0)))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFFlush})
	before, flushed := m.until(extwire.TFFlushed)
	if texts(before)[0] != volctest.Sentence(3) || !near(*flushed.At, 103.6) { // i18n-ignore: test data
		t.Errorf("flush: %s then flushed at %v", frameList(before), *flushed.At)
	}
	m.audio(volctest.PCM(line(4, 0.6, 0.2)))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	rest, _ := m.until(extwire.TFDone)
	if !slices.Equal(texts(rest), []string{volctest.Sentence(4)}) { // i18n-ignore: test data
		t.Errorf("after the flush: %s", frameList(rest))
	}
	var usage *extwire.TranscriptionFrame
	for i := range rest {
		if rest[i].Type == extwire.TFUsage {
			usage = &rest[i]
		}
	}
	if usage == nil || usage.Seconds < 4 || usage.Sessions != 2 {
		t.Errorf("usage %+v", usage)
	}
	// What the service was asked.
	s := fv.Session(0)
	if s.Endpoint != endpointLive || s.Header.Get("X-Api-Key") != "test-key" || s.Header.Get("X-Api-Resource-Id") != "volc.seedasr.sauc.duration" ||
		s.Header.Get("X-Api-Connect-Id") == "" || s.Header.Get("X-Api-App-Key") != "" {
		t.Errorf("headers %v on %s", s.Header, s.Endpoint)
	}
	r := s.Request.Request
	if !r.EnableNonstream || !r.ShowUtterances || r.ResultType != "single" || s.Request.Audio.Rate != 16000 || s.Request.Audio.Format != "pcm" {
		t.Errorf("request %+v", s.Request)
	}
	if r.Corpus == nil || r.Corpus.Context != `{"hotwords":[{"word":"张三"},{"word":"Persona"},{"word":"季度复盘会议（销售部）"}]}` { // i18n-ignore: test data
		t.Errorf("hotwords %+v", r.Corpus)
	}
	if !s.Last {
		t.Error("the first session was not finished with a last packet")
	}
}

// A live session that drops is replaced: the new one is sent the audio from the end of the last sentence handed on,
// nothing is said twice and the order holds.
func TestLiveJobReconnects(t *testing.T) {
	fast(t)
	liveQuietEnd = 10 * time.Second // waiting for the drop below must not end the session at a pause
	fv := fake(t)
	fv.Set(func(s *volctest.Script) { s.DropAfter, s.DropN = 12, 1 }) // the first session's socket drops after 2.4 s of audio
	m := dialJob(t, testPlugin(t, Form{}, "test-key"))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: "live", At: extwire.F(0.0)})
	m.until(extwire.TFReady)
	// The rest of the audio and end only after the drop, so it comes while the session takes audio, not while it
	// finishes (a live session dropped while finishing is not replaced), however slow the machine.
	m.audio(volctest.PCM(line(1, 1.0, 0.6), line(2, 1.2, 0.6)))
	early, w := m.until(extwire.TFWarning)
	m.audio(volctest.PCM(line(3, 0.8, 0.6)))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	got, _ := m.until(extwire.TFDone)
	got = append(append(early, w), got...)
	want := []string{volctest.Sentence(1), volctest.Sentence(2), volctest.Sentence(3)}
	if !slices.Equal(texts(got), want) {
		t.Errorf("sentences %v, want %v: %s", texts(got), want, frameList(got))
	}
	var dropped bool
	for _, f := range got {
		dropped = dropped || f.Type == extwire.TFWarning && f.Code == extwire.TWSessionDropped
	}
	if !dropped || fv.Count() < 2 {
		t.Errorf("no session_dropped warning or no second session (%d): %s", fv.Count(), frameList(got))
	}
	if s := sentencesOf(got); len(s) == 3 && !near(*s[2].Start, 3.4) {
		t.Errorf("third sentence starts at %v, want 3.4", *s[2].Start)
	}
}

// A replay job with no transcript of record (an extension's) gives a failing window two retries and then sends it as a
// gap (a kept window) and goes on; failures that run on into the next window end it; a final pass with a reference
// still ends at a window's second failure (#8887).
func TestReplayJobGaps(t *testing.T) {
	fast(t)
	// Service events provide synchronization; keep the production response deadlines so CPU starvation in a release
	// validation run does not become a different test failure.
	asrFinalWait, asrAnswerWait = 15*time.Second, 10*time.Second
	old, oldMost := replayWindow, replayWindowMost
	replayWindow, replayWindowMost = 2*time.Second, 3*time.Second
	t.Cleanup(func() { replayWindow, replayWindowMost = old, oldMost })
	windows := func(fs []extwire.TranscriptionFrame) (out []extwire.TranscriptionFrame) {
		for _, f := range fs {
			if f.Type == extwire.TFWindow {
				out = append(out, f)
			}
		}
		return out
	}
	// run: a replay job (with a reference when ref), the first two windows' audio, then, once the second window's
	// outcome is in (a window or an error), the third's and end. The frames up to done or error.
	var fv *volctest.Service
	run := func(drop []int, ref bool) (got []extwire.TranscriptionFrame, last extwire.TranscriptionFrame) {
		fv = fake(t)
		firstWindow := make(chan struct{})
		// The second window covers 2.1 s; drop only after those bytes actually arrived and the first window was emitted.
		fv.Set(func(s *volctest.Script) { s.DropLines, s.DropLinesAfter, s.DropGate = drop, 2100, firstWindow })
		m := dialJob(t, testPlugin(t, Form{}, "k"))
		m.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: "replay", Speed: 1000, At: extwire.F(0.0)})
		m.until(extwire.TFReady)
		if ref {
			m.send(extwire.TranscriptionFrame{Type: extwire.TFReference, From: extwire.F(0.5), To: extwire.F(1.5), Text: extwire.F("第一句的实时字幕")}) // i18n-ignore: test data
		}
		m.audio(volctest.PCM(line(1, 1.4, 0.8), line(2, 1.4, 0.8)))
		for {
			f := m.next()
			got = append(got, f)
			if f.Type == extwire.TFWindow && len(windows(got)) == 1 {
				close(firstWindow)
			}
			if f.Type == extwire.TFError || f.Type == extwire.TFWindow && len(windows(got)) == 2 {
				break
			}
		}
		if got[len(got)-1].Type == extwire.TFError {
			return got, got[len(got)-1]
		}
		m.audio(volctest.PCM(line(3, 1.4, 0.8)))
		m.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
		for {
			f := m.next()
			if f.Type == extwire.TFDone || f.Type == extwire.TFError {
				return got, f
			}
			got = append(got, f)
		}
	}

	// Every session of the second window drops: after three, a gap between the first window and the third.
	got, last := run([]int{2}, false)
	w := windows(got)
	if last.Type != extwire.TFDone || len(w) < 3 || w[0].Kept || !w[1].Kept || slices.ContainsFunc(w[2:], func(f extwire.TranscriptionFrame) bool { return f.Kept }) {
		t.Fatalf("windows %s, then %s", frameList(w), frameList([]extwire.TranscriptionFrame{last}))
	}
	if !near(*w[1].From, *w[0].To) || !near(*w[2].From, *w[1].To) || *w[1].To < 4 {
		t.Errorf("the gap does not sit between its neighbours: %s", frameList(w))
	}
	if !slices.Equal(texts(got), []string{volctest.Sentence(1), volctest.Sentence(3)}) {
		t.Errorf("sentences %s", frameList(got))
	}
	tried := 0 // sessions that were sent the second line's speech (PCM writes it as ±1200)
	for i := range fv.Count() {
		pcm := fv.Session(i).Audio
		for k := 0; k+1 < len(pcm); k += 2 {
			if v := int16(binary.LittleEndian.Uint16(pcm[k:])); v == 1200 || v == -1200 {
				tried++
				break
			}
		}
	}
	if tried != replayGapTries {
		t.Errorf("the second window was tried %d times, want %d", tried, replayGapTries)
	}

	// The service goes down after the first window: the second becomes a gap at most, and the failures that run on
	// end the job (no window got through in between to say the service works).
	fv = fake(t)
	m := dialJob(t, testPlugin(t, Form{}, "k"))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: "replay", Speed: 1000, At: extwire.F(0.0)})
	m.until(extwire.TFReady)
	m.audio(volctest.PCM(line(1, 1.4, 0.8))) // the first window ends at 2.0; the second takes what follows
	if _, w := m.until(extwire.TFWindow); w.Kept {
		t.Fatalf("the first window %s", frameList([]extwire.TranscriptionFrame{w}))
	}
	fv.Set(func(s *volctest.Script) { s.DropAll = true })
	m.audio(volctest.PCM(line(2, 1.4, 0.8)))
	// Either the second window's failures end the job at once (its next window failing alongside), or it becomes a gap
	// and the next window's failures end the job. Never done, never a window that got through.
	got = nil
	for last = m.next(); last.Type != extwire.TFError; last = m.next() {
		got = append(got, last)
		if last.Type == extwire.TFWindow {
			// The next window may already have failed too; its fatal frame, not a client write race, is the outcome.
			_ = m.writeAudio(volctest.PCM(line(3, 1.4, 0.8)))
			_ = m.write(extwire.TranscriptionFrame{Type: extwire.TFEnd})
		}
		if last.Type == extwire.TFDone {
			break
		}
	}
	w = windows(got)
	if !last.Fatal || last.Code != extwire.TEUpstreamUnavailable || len(w) > 1 || len(w) == 1 && !w[0].Kept {
		t.Errorf("a service that went down: %s, then %s", frameList(w), frameList([]extwire.TranscriptionFrame{last}))
	}

	// A final pass (it has a reference): a window's second failure ends the job, as before.
	got, last = run([]int{2}, true)
	if last.Type != extwire.TFError || !last.Fatal || len(windows(got)) != 1 {
		t.Errorf("a final pass: %s", frameList(append(got, last)))
	}
}

// Audio that arrives while a bounded window waits for its retry belongs to the next window at that bound. An
// unbounded retry still owns the arriving audio, so no overlapping session may open.
func TestReplayAudioOpensAtBoundedRetryEnd(t *testing.T) {
	for _, test := range []struct {
		name    string
		until   float64
		covered float64
		want    float64
		open    bool
	}{
		{name: "bounded", until: 4.1, want: 4.1, open: true},
		{name: "bounded after a later window", until: 4.1, covered: 4.4, want: 4.4, open: true},
		{name: "unbounded", until: -1, open: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake(t)
			ctx, cancel := context.WithCancel(context.Background())
			j := &job{pl: testPlugin(t, Form{}, "k"), ctx: ctx, ups: make(chan dialed, 1),
				mode: extwire.TranscriptionReplay, speed: 1000, started: true, ready: true,
				ring: ring{most: 10 * bytesPerSec}, retry: []*sess{{at: 2, until: test.until}}}
			if test.covered > 0 {
				j.sessions = []*sess{{at: test.until, until: test.covered, closed: true}}
			}
			t.Cleanup(func() {
				if j.n > 0 {
					select {
					case d := <-j.ups:
						if d.s != nil {
							d.s.close()
						}
					case <-time.After(time.Second):
						t.Error("the opened session did not finish dialing")
					}
				}
				cancel()
			})
			j.ring.add(volctest.PCM(line(1, 1.4, 0.8), line(2, 1.4, 0.8)))
			j.inPos = j.ring.end()
			j.onAudio(volctest.PCM(volctest.Line{N: 3, Silence: 0.1}))
			if !test.open {
				if j.dialing != nil {
					t.Fatalf("an unbounded retry opened an overlapping session at %.1f", j.dialing.at)
				}
				return
			}
			if j.dialing == nil || !near(j.dialing.at, test.want) {
				t.Fatalf("next session at %+v, want %.1f (n=%d, retry=%+v, inPos=%.1f, ringEnd=%.1f)", j.dialing, test.want, j.n, j.retry, j.inPos, j.ring.end())
			}
		})
	}
}

// A refused key ends the job before ready with upstream_forbidden; no key at all, with no_upstream.
func TestLiveJobRefused(t *testing.T) {
	fast(t)
	fv := fake(t)
	fv.Set(func(s *volctest.Script) {
		s.RefuseASR, s.RefuseBody = http.StatusUnauthorized, `{"code":45000010,"message":"invalid api key"}`
	})
	m := dialJob(t, testPlugin(t, Form{}, "wrong"))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: "live"})
	got, e := m.until(extwire.TFError)
	if e.Code != extwire.TEUpstreamForbidden || !e.Fatal || len(got) != 0 {
		t.Errorf("error %+v after %s", e, frameList(got))
	}
	if strings.Contains(e.Message, "wrong") {
		t.Errorf("the message carries the key: %q", e.Message)
	}

	fv.Set(func(s *volctest.Script) {
		s.RefuseASR, s.RefuseRequest, s.RefuseMessage = 0, 45000030, "requested resource not granted"
	})
	m = dialJob(t, testPlugin(t, Form{}, "k"))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: "live"})
	if _, e := m.until(extwire.TFError); e.Code != extwire.TEUpstreamForbidden {
		t.Errorf("a model not turned on: %+v", e)
	}

	m = dialJob(t, testPlugin(t, Form{}, ""))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: "live"})
	if _, e := m.until(extwire.TFError); e.Code != extwire.TENoUpstream {
		t.Errorf("no key: %+v", e)
	}
}

// A hole (seek forward) ends the session; the next starts after the hole, its times from there.
func TestLiveJobHole(t *testing.T) {
	fast(t)
	fv := fake(t)
	m := dialJob(t, testPlugin(t, Form{}, "k"))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: "live"})
	m.until(extwire.TFReady)
	m.audio(volctest.PCM(line(1, 1.0, 0.2)))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFSeek, At: extwire.F(60.0)})
	m.audio(volctest.PCM(line(2, 1.0, 0.6)))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	got, _ := m.until(extwire.TFDone)
	s := sentencesOf(got)
	if len(s) != 2 || !near(*s[0].Start, 0) || !near(*s[1].Start, 60) || !near(*s[1].End, 61) {
		t.Errorf("sentences %s", frameList(s))
	}
	if fv.Count() != 2 {
		t.Errorf("%d sessions, want 2", fv.Count())
	}
}

// A pause ends the session (the service ends one after 8 s with no packet); speech after it opens the next.
func TestLiveJobPause(t *testing.T) {
	fast(t)
	fv := fake(t)
	m := dialJob(t, testPlugin(t, Form{}, "k"))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: "live"})
	m.until(extwire.TFReady)
	m.audio(volctest.PCM(line(1, 1.0, 0.6)))
	m.until(extwire.TFSentence)
	// The quiet end comes liveQuietEnd after the last audio: the session's last packet, well within 10 of them.
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for deadline := time.Now().Add(10 * liveQuietEnd); ; <-tick.C {
		if s := fv.Session(0); s != nil && s.Last {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session was not finished at the pause")
		}
	}
	m.audio(volctest.PCM(line(2, 1.0, 0.6)))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	got, _ := m.until(extwire.TFDone)
	if s := sentencesOf(got); len(s) != 1 || !near(*s[0].Start, 1.6) {
		t.Errorf("after the pause: %s", frameList(got))
	}
}

// The final pass: windows of the streaming-input endpoint, each with its sentences then window; a window with no
// text where the transcript of record had some is kept.
func TestReplayJob(t *testing.T) {
	fast(t)
	old, oldMost := replayWindow, replayWindowMost
	replayWindow, replayWindowMost = 2*time.Second, 3*time.Second
	t.Cleanup(func() { replayWindow, replayWindowMost = old, oldMost })
	fv := fake(t)
	m := dialJob(t, testPlugin(t, Form{}, "k"))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: "replay", Speed: 1000, At: extwire.F(10.0), Glossary: []string{"张三"}}) // i18n-ignore: test data
	_, ready := m.until(extwire.TFReady)
	if !slices.Equal(ready.Features, []string{"replay"}) {
		t.Errorf("ready %v", ready.Features)
	}
	m.send(extwire.TranscriptionFrame{Type: extwire.TFReference, From: extwire.F(13.5), To: extwire.F(14.5), Text: extwire.F("这一段在实时里有字幕")}) // i18n-ignore: test data
	m.audio(volctest.PCM(line(1, 1.0, 0.6), line(2, 1.0, 0.8)))
	m.audio(make([]byte, secToBytes(1.5)))
	m.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	got, _ := m.until(extwire.TFDone)
	var windows []extwire.TranscriptionFrame
	for _, f := range got {
		if f.Type == extwire.TFWindow {
			windows = append(windows, f)
		}
	}
	if len(windows) != 2 || !near(*windows[0].From, 10) || windows[0].Kept || !windows[1].Kept {
		t.Errorf("windows %s", frameList(windows))
	}
	if !slices.Equal(texts(got), []string{volctest.Sentence(1), volctest.Sentence(2)}) {
		t.Errorf("sentences %s", frameList(got))
	}
	// Order: the first window's sentences, then its window frame.
	if got[0].Type != extwire.TFSentence && got[0].Type != extwire.TFAck {
		t.Errorf("first frame %s", frameList(got[:1]))
	}
	s := fv.Session(0)
	if s.Endpoint != endpointReplay || s.Request.Request.EnableNonstream || s.Request.Request.Corpus == nil {
		t.Errorf("replay session %s %+v", s.Endpoint, s.Request.Request)
	}
}

// Old console credentials: the App ID and the Access Token, under each service's own header names.
func TestOldConsoleHeaders(t *testing.T) {
	fast(t)
	fv := fake(t)
	f := Form{Advanced: Advanced{AppID: "1234", ASRModel: "volc.bigasr.sauc.concurrent"}}
	pl := testPlugin(t, f, "token")
	r := probe(context.Background(), pl.client, pl.cred, f, true)
	if r.asrErr != nil || r.ttsErr != nil {
		t.Fatalf("probe %v %v", r.asrErr, r.ttsErr)
	}
	h := fv.Session(0).Header
	if h.Get("X-Api-App-Key") != "1234" || h.Get("X-Api-Access-Key") != "token" || h.Get("X-Api-Key") != "" ||
		h.Get("X-Api-Resource-Id") != "volc.bigasr.sauc.concurrent" {
		t.Errorf("recognition headers %v", h)
	}
	th := fv.Syntheses()[0].Header
	if th.Get("X-Api-App-Id") != "1234" || th.Get("X-Api-Access-Key") != "token" || th.Get("X-Api-Resource-Id") != "seed-tts-2.0" {
		t.Errorf("synthesis headers %v", th)
	}
}

func TestHotwords(t *testing.T) {
	g := []string{"二〇二六年第四季度产品复盘", "Weekly Sync", "张三", "李四", "Kubernetes", "张三", " 王 五 ", "一个特别特别特别特别特别特别特别特别长的术语短语"} // i18n-ignore: test data
	live := hotwords(g, true)
	want := []string{"张三", "李四", "Kubernetes", "王 五", "二〇二六年第四季度产品复盘", "Weekly Sync"} // i18n-ignore: test data
	if !slices.Equal(live, want) {
		t.Errorf("live hotwords %q, want %q", live, want)
	}
	var many []string
	for i := range 80 {
		many = append(many, fmt.Sprintf("术语%02d", i)) // i18n-ignore: test data
	}
	live = hotwords(many, true)
	total := 0
	for _, w := range live {
		total += tokens(w)
	}
	if total > hotwordBudgetLive || len(live) < 20 {
		t.Errorf("%d terms, %d tokens", len(live), total)
	}
	if replay := hotwords(many, false); len(replay) != 80 {
		t.Errorf("replay takes them all: %d", len(replay))
	}
	if tokens("Persona") != 2 || tokens("张三") != 2 || tokens("A1") != 1 { // i18n-ignore: test data
		t.Errorf("tokens %d %d %d", tokens("Persona"), tokens("张三"), tokens("A1")) // i18n-ignore: test data
	}
}

func TestFrames(t *testing.T) {
	// A final result as the service sends it: header, sequence (negative), size, gzipped JSON.
	z := gzipBytes([]byte(`{"result":{"text":"x"}}`))
	raw := append([]byte{0x11, 0x93, 0x11, 0, 0xff, 0xff, 0xff, 0xf9}, byte(len(z)>>24), byte(len(z)>>16), byte(len(z)>>8), byte(len(z)))
	raw = append(raw, z...)
	f, err := parseServerFrame(raw)
	if err != nil || f.msgType != msgFullServer || f.seq != -7 || !f.last || string(f.payload) != `{"result":{"text":"x"}}` {
		t.Errorf("result frame %+v %v", f, err)
	}
	// An error: code, size, the message as text.
	f, err = parseServerFrame([]byte{0x11, 0xf0, 0x10, 0, 0x02, 0xae, 0xa5, 0x41, 0, 0, 0, 3, 'b', 'a', 'd'})
	if err != nil || f.msgType != msgError || f.code != 45000001 || string(f.payload) != "bad" {
		t.Errorf("error frame %+v %v", f, err)
	}
	if _, err := parseServerFrame(raw[:9]); err == nil {
		t.Error("a cut frame read")
	}
	if _, err := parseServerFrame(raw[:len(raw)-1]); err == nil {
		t.Error("a frame shorter than its size read")
	}
	b := clientFrame(msgAudioOnly, -5, []byte{1, 2})
	if b[0] != 0x11 || b[1] != msgAudioOnly<<4|flagNegSeq || b[2] != serNone<<4|compGzip || b[4] != 0xff || b[7] != 0xfb {
		t.Errorf("last audio frame header %x", b[:8])
	}
	if b := clientFrame(msgFullClient, 1, []byte(`{}`)); b[1] != msgFullClient<<4|flagPosSeq || b[2] != serJSON<<4|compGzip || b[7] != 1 {
		t.Errorf("request frame header %x", b[:8])
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		status, code int
		msg, want    string
	}{
		{401, 0, "", failForbidden},
		{403, 0, `requested resource not granted`, failNotEnabled},
		{0, 45000030, "", failNotEnabled},
		{0, 45000000, "quota exceeded for types: concurrency", failBusy},
		{0, 45000000, "speaker permission denied: voice not authorized", failVoice},
		{429, 0, "", failBusy},
		{0, 55000031, "server busy", failBusy},
		{0, 45000001, "invalid params", failFailed},
		{503, 0, "", failBusy},
	}
	for _, c := range cases {
		if got := classify(c.status, c.code, c.msg); got != c.want {
			t.Errorf("classify(%d, %d, %q) = %s, want %s", c.status, c.code, c.msg, got, c.want)
		}
	}
}

// Each kind's transcription code (#13446): what the manager passes on to the phone, which says why and what to do.
// A busy service is told apart from one out of reach.
func TestTranscriptionCodeKeepsTheKind(t *testing.T) {
	for kind, want := range map[string]string{
		failForbidden:  extwire.TEUpstreamForbidden,
		failNotEnabled: extwire.TEUpstreamForbidden,
		failVoice:      extwire.TEUpstreamForbidden,
		failBusy:       extwire.TEUpstreamBusy,
		failNetwork:    extwire.TEUpstreamUnavailable,
		failFailed:     extwire.TEUpstreamFailed,
	} {
		if got := transcriptionCode(&failure{kind: kind, service: "asr"}); got != want {
			t.Errorf("transcriptionCode(%s) = %s, want %s", kind, got, want)
		}
	}
	if got := transcriptionCode(io.EOF); got != extwire.TEUpstreamFailed {
		t.Errorf("an error that is no failure: %s", got)
	}
}
