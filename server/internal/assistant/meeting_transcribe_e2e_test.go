package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/builtinext"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/modules/voice/voicetest"
)

// Hub transcription end to end (docs/assistant/meeting-minutes.md "Phase 5: hub transcription"): the phone's audio
// socket, the manager, the built-in voice extension's real adapter (its gpt-live contribution) and a scripted stand-in
// for the GPT-Live-compatible upstream (voicetest), then the final pass, the minutes and the audio's deletion. The
// adapter's own times are its defaults (its windows of about 15 s), so this runs for some seconds. Names and texts are
// placeholders; the sentences mix Chinese and English as people say them, and must come out as said.

// e2eScript is the meeting: Chinese, English and both in one sentence.
var e2eScript = []voicetest.Line{
	{Start: 0.5, End: 3.0, Text: "我们这周先把 export cap 保持在 4 MB。"}, // i18n-ignore
	{Start: 3.8, End: 5.6, Text: "Bo owns the vendor follow-up."},
	{Start: 6.4, End: 9.6, Text: "下周三之前 Alice 要把 pricing 的 proposal 发出来。"},     // i18n-ignore
	{Start: 10.6, End: 12.4, Text: "Let's revisit pricing next week."},         // said while the role is off
	{Start: 13.2, End: 16.0, Text: "这个 release 先只发 iOS，Android 下个 sprint 再跟。"}, // i18n-ignore; Persona's own voice heard back
	{Start: 17.0, End: 19.0, Text: "Can we get the dashboard numbers before Friday?"},
}

const e2eLength = 21.0

// e2eAnswer is what Persona says over the fifth sentence: that sentence, its echo, is part of it.
var e2eAnswer = "好的。" + e2eScript[4].Text // i18n-ignore

// The timeline of the meeting: the role off over the fourth sentence, Persona speaking over the fifth (its answer).
var e2eTimeline = map[string]any{
	"roles":    []map[string]any{{"at": 0, "attendance": "listener"}, {"at": 10.2, "attendance": "off"}, {"at": 12.8, "attendance": "listener"}},
	"speaking": []map[string]any{{"start": 13.0, "end": 16.2, "text": e2eAnswer}},
}

// adapterTranscribers reaches the voice extension's transcription contribution over HTTP, as the extensions layer
// does over the extension's socket.
type adapterTranscribers struct {
	srv *httptest.Server
	cid string // the voice extension's transcription contribution
}

func (a adapterTranscribers) TranscriptionInfo(ctx context.Context, ref string) (extwire.TranscriptionInfo, error) {
	var info extwire.TranscriptionInfo
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.srv.URL+"/transcription/"+a.cid+"/info", nil)
	res, err := a.srv.Client().Do(req)
	if err != nil {
		return info, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return info, fmt.Errorf("info: %s", res.Status)
	}
	return info, json.NewDecoder(res.Body).Decode(&info)
}

func (a adapterTranscribers) TranscriptionDial(ctx context.Context, ref string) (*websocket.Conn, func(), error) {
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(a.srv.URL, "http")+"/transcription/"+a.cid+"/ws", nil)
	if err != nil {
		return nil, nil, err
	}
	c.SetReadLimit(1 << 20)
	return c, func() {}, nil
}

func (a adapterTranscribers) IsTranscriptionContribution(ref string) bool { return ref == "" }

func (a adapterTranscribers) TranscriptionInstall(string) string { return "" }

// lockedBuf is a log both sides write into.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// minutesCapture is the minutes model: it answers reply and keeps what it was given.
type minutesCapture struct {
	mu     sync.Mutex
	inputs []string
	reply  string
}

func (m *minutesCapture) JSONMax(_ context.Context, _, user string, _ int, out any) error {
	m.mu.Lock()
	m.inputs = append(m.inputs, user)
	m.mu.Unlock()
	return json.Unmarshal([]byte(m.reply), out)
}

// e2eFixture is a hub fixture whose contribution is the real adapter on the stand-in upstream s.
type e2eFixture struct {
	*hubFixture
	live     *voicetest.Live
	adapter  *lockedBuf
	manager  *lockedBuf
	minutes  *minutesCapture
	sentence []string // the script's texts
}

func newE2EFixture(t *testing.T, s voicetest.Script) *e2eFixture {
	t.Helper()
	shortHubTimes(t)
	jobReadyWait, jobDoneWait = 20*time.Second, time.Minute // and the recording module's below (recTimings)
	voice, ok := builtinext.VoiceExtension()
	if !ok {
		t.Skip("the voice module, the built-in transcription these tests run, is not in this build")
	}
	live := voicetest.NewLive(t, s)
	adapterLog := &lockedBuf{}
	cfg, _ := json.Marshal(map[string]any{"providers": []map[string]any{{"id": "t", "url": live.URL(), "dialect": "live", "key": "k",
		"default_model": "m1"}}, "default": "t/m1"})
	handler, err := voice.Handler(cfg, "http://127.0.0.1:1", "hubtoken", log.New(adapterLog, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	ext := httptest.NewServer(handler)
	t.Cleanup(ext.Close)

	managerLog := &lockedBuf{}
	f := newFakeHub().withMachine()
	s2 := kbServiceOn(t, f, newKBStore(), t.TempDir(), func(format string, args ...any) {
		fmt.Fprintf(managerLog, format+"\n", args...)
	})
	needsRecordingModule(t)
	applyShortRec(t, s2)
	recTimings(t, s2, map[string]time.Duration{"jobReadyWait": 20 * time.Second, "audioEndWait": 25 * time.Second, "jobDoneWait": time.Minute}, nil)
	setSettings(t, s2, func(v *Settings) { v.Main = "c-main" })
	s2.meetingModel = &meetingModelFake{reply: `{"question":"","answer":"We kept the cap.","sources":[]}`}
	feed := trackAttendFeed(s2)
	srv := httptest.NewServer(s2)
	m := &meetingFixture{t: t, s: s2, f: f, srv: srv, model: s2.meetingModel.(*meetingModelFake)}
	t.Cleanup(func() {
		srv.Close()
		feed.stop()
	})
	s2.trans = adapterTranscribers{srv: ext, cid: voice.TranscriptionContribution}
	s2.transcriptionReadinessChanged()
	sockets := httptest.NewServer(http.HandlerFunc(s2.ServeSocket))
	h := &hubFixture{meetingFixture: m, sockets: sockets}
	t.Cleanup(func() {
		sockets.Close()
		h.stopRuns()
	})
	e := &e2eFixture{hubFixture: h, live: live, adapter: adapterLog, manager: managerLog,
		minutes: &minutesCapture{reply: `{"title":"Design review","decisions":[{"text":"Keep the export cap","line":1}]}`}}
	for _, l := range s.Sentences {
		e.sentence = append(e.sentence, l.Text)
	}
	return e
}

// stream sends 100 ms frames at at most ten times real time, with role and speaking frames at their moments,
// then end. The normal caption case waits for the independent fake peer to consume each frame; scheduler delays
// must not turn this content check into the adapter's separately tested backlog/skip scenario.
func (e *e2eFixture) stream(p *testPhone, s voicetest.Script, total float64, paceCaptions bool) {
	e.t.Helper()
	pcm := voicetest.PCM(s, total)
	controls := []struct {
		at float64
		f  map[string]any
	}{
		{10.2, map[string]any{"type": "role", "at": 10.2, "attendance": "off"}},
		{12.8, map[string]any{"type": "role", "at": 12.8, "attendance": "listener"}},
		{13.0, map[string]any{"type": "speaking", "on": true, "at": 13.0, "text": e2eAnswer}},
		{16.2, map[string]any{"type": "speaking", "on": false, "at": 16.2}},
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	const frame = kb.AudioRate / 10 * 2
	for off := 0; off < len(pcm); off += frame {
		at := float64(off/2) / kb.AudioRate
		for len(controls) > 0 && controls[0].at <= at+0.1 {
			p.send(controls[0].f)
			controls = controls[1:]
		}
		<-tick.C
		var consumed int64
		if paceCaptions {
			consumed, _ = e.live.CaptionSamples() // the first frame may start the upstream job
		}
		end := min(off+frame, len(pcm))
		if err := p.c.Write(context.Background(), websocket.MessageBinary, pcm[off:end]); err != nil {
			e.t.Fatalf("audio: %v", err)
		}
		if paceCaptions {
			want := consumed + int64((end-off)/2)
			waitLong(e.t, "the caption peer to consume audio", 5*time.Second, func() bool {
				heard, ready := e.live.CaptionSamples()
				return ready && heard >= want
			})
		}
	}
	p.send(map[string]any{"type": "end", "at": total})
}

// waitLong is waitFor with a deadline of d.
func waitLong(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); <-tick.C {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The whole meeting: live lines verbatim and in order (a rogue session's invented replies nowhere, Persona's echo and the
// role-off sentence in the hub's transcript only, the echo marked), captions before lines; then the final pass, the
// transcript of record the hub's (the echo marked), the minutes from it without the echo and the audio gone after them.
func TestHubTranscriptionEndToEnd(t *testing.T) {
	script := voicetest.Script{Sentences: e2eScript, CaptionNoise: 0.1, Rogue: map[int]int{1: 0}}
	e := newE2EFixture(t, script)
	id := meetingRec
	e.upload(id, 1, map[string]any{"transcription": "hub", "attendance": "listener", "language": "zh-CN", "title": "Design review",
		"timeline": map[string]any{"roles": []map[string]any{{"at": 0, "attendance": "listener"}}}})
	p, ready := e.open(id, 0, map[string]any{"roles": []map[string]any{{"at": 0, "attendance": "listener"}}})
	if ready["from"] != 0.0 {
		t.Fatalf("ready %v", ready)
	}
	e.stream(p, script, e2eLength, true)
	waitLong(t, "the socket's end", 40*time.Second, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.closed
	})
	if p.status != websocket.StatusNormalClosure {
		t.Fatalf("closed %v", p.status)
	}
	want := []string{e.sentence[0], e.sentence[1], e.sentence[2], e.sentence[5]}
	if got := p.texts(); !slices.Equal(got, want) {
		t.Fatalf("lines:\n%s\nwant:\n%s\nadapter log:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"), e.adapter)
	}
	firstInterim, firstLine := -1, -1
	for i, f := range p.frames {
		switch f["type"] {
		case "interim":
			if firstInterim < 0 {
				firstInterim = i
			}
		case "line":
			if firstLine < 0 {
				firstLine = i
			}
		}
	}
	for n, f := range p.of("line") {
		if f["n"] != float64(n) {
			t.Errorf("line positions: %v", p.of("line"))
		}
	}
	if firstInterim < 0 || firstInterim > firstLine {
		t.Errorf("interim %d, first line %d", firstInterim, firstLine)
	}
	if got := lineTexts(e.lines(id)); !slices.Equal(got, want) {
		t.Fatalf("live log %q", got)
	}
	hub := e.hubTexts(id)
	if !slices.Contains(hub, e.sentence[3]) || !slices.Contains(hub, e.sentence[4]) {
		t.Fatalf("the hub's live transcript %q", hub)
	}
	for i, echo := range e.hubEcho(id) {
		if echo != (hub[i] == e.sentence[4]) {
			t.Fatalf("the hub's live transcript's echo marks %v (%q)", e.hubEcho(id), hub)
		}
	}
	for _, r := range voicetest.InventedReplies {
		if slices.Contains(hub, r) {
			t.Fatalf("an invented reply: %q", r)
		}
	}

	e.upload(id, 2, map[string]any{"transcription": "hub", "attendance": "listener", "language": "zh-CN", "title": "Design review",
		"complete": true, "duration": e2eLength, "ended_at": time.Now().UTC().Format(time.RFC3339), "timeline": e2eTimeline})
	// The final passes' loop, looking every 100 ms (finalEvery): as the manager runs it.
	recTimings(t, e.s, map[string]time.Duration{"finalEvery": 100 * time.Millisecond}, nil)
	fctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go e.s.finalPasses(fctx)
	waitLong(t, "the final pass", time.Minute, func() bool { return e.transcription(id)["state"] == "final" })
	wantFinal := e.sentence // Persona's echo (the fifth) among them, marked
	d, err := e.engine().Get(kb.KindRecording, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, sg := range d.Segments {
		if sg.Source != kb.SourceHub || sg.Echo != (sg.Text == e.sentence[4]) {
			t.Errorf("segment %+v", sg)
		}
		got = append(got, sg.Text)
	}
	if !slices.Equal(got, wantFinal) {
		t.Fatalf("detail.segments:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantFinal, "\n"))
	}
	md, err := os.ReadFile(filepath.Join(e.s.kb.dir, kb.KindRecording, id, "transcript.md"))
	if err != nil || !strings.Contains(string(md), e.sentence[2]) || !strings.Contains(string(md), "[Persona 回声] "+e.sentence[4]) {
		t.Fatalf("transcript.md %s %v", md, err)
	}
	if len(e.f.liveEventsOf(id)) == 0 || !slices.ContainsFunc(e.f.liveEventsOf(id), func(ev map[string]any) bool {
		return slices.Contains(ev["changed"].([]any), any(kb.PartTranscription))
	}) || !slices.ContainsFunc(e.f.liveEventsOf(id), func(ev map[string]any) bool {
		return slices.Contains(ev["changed"].([]any), any(kb.PartLines))
	}) {
		t.Fatalf("meeting_live events %v", e.f.liveEventsOf(id))
	}

	eng := e.engine()
	eng.Model, eng.ModelFor = e.minutes, func(string) kb.Model { return e.minutes }
	if n := eng.WriteMinutes(context.Background(), 5); n < 1 {
		t.Fatal("no minutes")
	}
	e.minutes.mu.Lock()
	input := strings.Join(e.minutes.inputs, "\n")
	e.minutes.mu.Unlock()
	for i, s := range wantFinal {
		if in := strings.Contains(input, s); in != (i != 4) {
			t.Errorf("the minutes' input has %q: %v", s, in)
		}
	}
	if _, err := os.Stat(filepath.Join(e.s.kb.dir, kb.KindRecording, id, "attachments", "audio")); !os.IsNotExist(err) {
		t.Fatalf("the audio after the minutes: %v", err)
	}
	logs := e.manager.String() + e.adapter.String()
	if !strings.Contains(e.manager.String(), "transcription usage") {
		t.Errorf("no usage in the manager's log:\n%s", e.manager)
	}
	for _, s := range e.sentence {
		if strings.Contains(logs, s) {
			t.Errorf("the log has %q", s)
		}
	}
}

// Without captions the hub says no_interim and the lines still come.
func TestHubTranscriptionEndToEndWithoutCaptions(t *testing.T) {
	script := voicetest.Script{Sentences: e2eScript[:3], NoCaptions: true}
	e := newE2EFixture(t, script)
	e.upload(meetingRec, 1, map[string]any{"transcription": "hub", "attendance": "listener", "language": "zh-CN"})
	p, _ := e.open(meetingRec, 0, nil)
	e.stream(p, script, 10, false)
	waitLong(t, "the socket's end", 40*time.Second, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.closed
	})
	if f := p.of("problem"); len(f) == 0 || f[0]["code"] != problemNoInterim {
		t.Fatalf("problems %v", f)
	}
	if got := p.texts(); !slices.Equal(got, e.sentence) {
		t.Fatalf("lines %q", got)
	}
}

// An upstream that fails for good: upstream_failed, and the phone's uploads fill the live log (source device).
func TestHubTranscriptionEndToEndUpstreamFails(t *testing.T) {
	script := voicetest.Script{Sentences: e2eScript[:3], FailAfter: 300 * time.Millisecond}
	e := newE2EFixture(t, script)
	e.upload(meetingRec, 1, map[string]any{"transcription": "hub", "attendance": "listener", "language": "zh-CN"})
	p, _ := e.open(meetingRec, 0, nil)
	pcm := voicetest.PCM(script, 10)
	if err := p.c.Write(context.Background(), websocket.MessageBinary, pcm[:3200]); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "upstream_failed", 40*time.Second, func() bool {
		return slices.ContainsFunc(p.of("problem"), func(f map[string]any) bool { return f["code"] == problemUpstreamFailed })
	})
	waitLong(t, "the fallback", 10*time.Second, func() bool { return e.transcription(meetingRec)["fallback"] == true })
	e.upload(meetingRec, 2, map[string]any{"transcription": "hub", "attendance": "listener", "language": "zh-CN",
		"segments": phoneSegs(fakeLine{0.5, 3, "Phone text one."}, fakeLine{3.8, 5.6, "Phone text two."})})
	segs := e.lines(meetingRec)
	if got := lineTexts(segs); !slices.Equal(got, []string{"Phone text one.", "Phone text two."}) || segs[0].Source != kb.SourceDevice {
		t.Fatalf("live log %+v", segs)
	}
}
