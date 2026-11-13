package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/voice/voicetest"
)

// trScript is a meeting of six sentences in Chinese, English and both in one sentence, with names and terms.
var trScript = []voicetest.Line{
	{Start: 0.5, End: 3.0, Text: "我们这周先把 export cap 保持在 4 MB。"},
	{Start: 3.8, End: 5.6, Text: "Bo owns the vendor follow-up."},
	{Start: 6.4, End: 9.6, Text: "下周三之前 Alice 要把 pricing 的 proposal 发出来。"},
	{Start: 10.6, End: 12.4, Text: "Let's revisit pricing next week."},
	{Start: 13.2, End: 16.0, Text: "这个 release 先只发 iOS，Android 下个 sprint 再跟。"},
	{Start: 17.0, End: 19.0, Text: "Can we get the dashboard numbers before Friday?"},
}

const trScriptLength = 21.0

// trFast shortens the job's numbers for a test: windows of 4 s, settled after 1 s of audio.
func trFast(t *testing.T) {
	t.Helper()
	setVar(t, &trTick, 10*time.Millisecond)
	setVar(t, &trWindow, 4*time.Second)
	setVar(t, &trWindowMost, 12*time.Second)
	setVar(t, &trSettle, time.Second)
	setVar(t, &trSettleWall, 80*time.Millisecond)
	setVar(t, &trCaptionSettle, 500*time.Millisecond)
	setVar(t, &trWindowWait, 4*time.Second)
	setVar(t, &trReplyWait, time.Second)
	setVar(t, &trStall, 300*time.Millisecond)
	setVar(t, &trFlushSilence, time.Second)
	setVar(t, &trCatchUp, 200.0)
	setVar(t, &trCaptionJump, time.Minute) // the tests stream faster than real time: the caption session never skips
	setVar(t, &trReconnectWaits, []time.Duration{0, 20 * time.Millisecond, 20 * time.Millisecond})
	setVar(t, &trUsageMost, 200*time.Millisecond)
	setVar(t, &trReplayWindow, 4*time.Second)
	setVar(t, &trReplayWindowMost, 8*time.Second)
	setVar(t, &trReplayOver, 2*time.Second)
	setVar(t, &trRefGap, 3*time.Second)
	setVar(t, &trRefPad, 500*time.Millisecond)
	setVar(t, &trReplayTail, time.Second)
	setVar(t, &trReplayQuiet, 100*time.Millisecond)
	setVar(t, &trReplayTailMost, 3*time.Second)
	setVar(t, &trStopEarly, 2*time.Second)
}

// trProduction keeps the production settle times for a test that streams at real time against a stand-in whose
// replies come seconds after a sentence: only the tick and the waits before reconnects are shorter.
func trProduction(t *testing.T) {
	t.Helper()
	setVar(t, &trTick, 10*time.Millisecond)
	setVar(t, &trReconnectWaits, []time.Duration{0, 20 * time.Millisecond, 20 * time.Millisecond})
	setVar(t, &trUsageMost, 200*time.Millisecond)
}

// trLive is a plugin whose one upstream is the stand-in for script s.
func trLive(t *testing.T, s voicetest.Script) (*Plugin, *httptest.Server, *lockedLog, *voicetest.Live) {
	t.Helper()
	live := voicetest.NewLive(t, s)
	pl, srv, logs := speakPlugin(t, Config{Providers: []Provider{{ID: "t", URL: live.URL(), Dialect: DialectLive, Key: "k",
		DefaultModel: "m1"}}, Default: "t/m1"})
	return pl, srv, logs, live
}

// bareTrPlugin serves a plugin of cfg as it is (one without upstreams is not valid to Config.validate).
func bareTrPlugin(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	pl := New(cfg, "http://127.0.0.1:1", "hubtoken", log.New(io.Discard, "", 0))
	srv := httptest.NewServer(pl.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// trClient is the manager's side of a job.
type trClient struct {
	t      *testing.T
	c      *websocket.Conn
	frames chan extwire.TranscriptionFrame
	mu     sync.Mutex
	got    []extwire.TranscriptionFrame
	closed chan struct{}
	status websocket.StatusCode
}

func dialTr(t *testing.T, srv *httptest.Server, cid string, header http.Header) (*trClient, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/transcription/" + cid + "/ws"
	c, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, res, err
	}
	tc := &trClient{t: t, c: c, frames: make(chan extwire.TranscriptionFrame, 4096), closed: make(chan struct{})}
	go func() {
		defer close(tc.closed)
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				tc.status = websocket.CloseStatus(err)
				return
			}
			var f extwire.TranscriptionFrame
			if json.Unmarshal(b, &f) != nil {
				t.Errorf("not a frame: %s", b)
				continue
			}
			tc.mu.Lock()
			tc.got = append(tc.got, f)
			tc.mu.Unlock()
			tc.frames <- f
		}
	}()
	t.Cleanup(func() { _ = c.CloseNow() })
	return tc, res, nil
}

// startTr opens a job and sends its start.
func startTr(t *testing.T, srv *httptest.Server, start extwire.TranscriptionFrame) *trClient {
	t.Helper()
	tc, _, err := dialTr(t, srv, TranscriptionContribution, http.Header{extwire.HeaderRole: {roleFull}})
	if err != nil {
		t.Fatal(err)
	}
	tc.send(start)
	return tc
}

func (tc *trClient) send(f extwire.TranscriptionFrame) {
	b, _ := json.Marshal(f)
	_ = tc.c.Write(context.Background(), websocket.MessageText, b)
}

// stream sends pcm in 100 ms frames at speed times real time.
func (tc *trClient) stream(pcm []byte, speed float64) {
	tick := time.NewTicker(time.Duration(float64(100*time.Millisecond) / speed))
	defer tick.Stop()
	for off := 0; off < len(pcm); off += 3200 {
		<-tick.C
		if err := tc.c.Write(context.Background(), websocket.MessageBinary, pcm[off:min(off+3200, len(pcm))]); err != nil {
			return
		}
	}
}

// next is the next frame of type typ (the ones before it are skipped).
func (tc *trClient) next(typ string) extwire.TranscriptionFrame {
	tc.t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case f := <-tc.frames:
			if f.Type == typ {
				return f
			}
		case <-deadline:
			tc.t.Fatalf("no %s frame came", typ)
		}
	}
}

// done waits for the job's socket to close and is every frame it sent.
func (tc *trClient) done() []extwire.TranscriptionFrame {
	tc.t.Helper()
	select {
	case <-tc.closed:
	case <-time.After(20 * time.Second):
		tc.t.Fatal("the job never ended")
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return append([]extwire.TranscriptionFrame(nil), tc.got...)
}

func trOfType(frames []extwire.TranscriptionFrame, typ string) []extwire.TranscriptionFrame {
	var out []extwire.TranscriptionFrame
	for _, f := range frames {
		if f.Type == typ {
			out = append(out, f)
		}
	}
	return out
}

func trTexts(frames []extwire.TranscriptionFrame) []string {
	var out []string
	for _, f := range trOfType(frames, extwire.TFSentence) {
		out = append(out, *f.Text)
	}
	return out
}

func scriptTexts(lines []voicetest.Line) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

// sameTexts fails unless the job sent exactly the script's sentences, byte for byte, in order, each at about its time.
func sameTexts(t *testing.T, frames []extwire.TranscriptionFrame, want []voicetest.Line) {
	t.Helper()
	got := trOfType(frames, extwire.TFSentence)
	if strings.Join(trTexts(frames), "\n") != strings.Join(scriptTexts(want), "\n") {
		t.Fatalf("sentences:\n%s\nwant:\n%s", strings.Join(trTexts(frames), "\n"), strings.Join(scriptTexts(want), "\n"))
	}
	for i, f := range got {
		if *f.Start < want[i].Start-1 || *f.End > want[i].End+1 || *f.End < *f.Start {
			t.Errorf("sentence %d at %.2f-%.2f, said at %.1f-%.1f", i, *f.Start, *f.End, want[i].Start, want[i].End)
		}
	}
}

func liveStart(glossary ...string) extwire.TranscriptionFrame {
	return extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: extwire.TranscriptionLive, Language: "zh-CN",
		Glossary: glossary, At: extwire.F(0.0)}
}

// noMeetingText fails when the log has a sentence, a glossary term or a reference.
func noMeetingText(t *testing.T, logs string, words ...string) {
	t.Helper()
	for _, w := range words {
		if strings.Contains(logs, w) {
			t.Errorf("the log has %q:\n%s", w, logs)
		}
	}
}

// The six sentences come out byte for byte, in order and at their times, from one caption and two verbatim sessions
// with noisy captions; interim captions before them, acks, the usage of every session, done. The sessions start as
// transcription sessions (text only, captions asked on the caption session, no language setting), and the log has
// nothing that was said and no glossary term.
func TestTranscriptionLiveSentences(t *testing.T) {
	trFast(t)
	_, srv, logs, live := trLive(t, voicetest.Script{Sentences: trScript, CaptionNoise: 0.1})
	tc := startTr(t, srv, liveStart("Alice", "export cap"))
	ready := tc.next(extwire.TFReady)
	if strings.Join(ready.Features, ",") != "live,interim" {
		t.Fatalf("ready %+v", ready)
	}
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	sameTexts(t, frames, trScript)
	firstInterim, firstSentence := -1, -1
	for i, f := range frames {
		if f.Type == extwire.TFInterim && firstInterim < 0 {
			firstInterim = i
		}
		if f.Type == extwire.TFSentence && firstSentence < 0 {
			firstSentence = i
		}
	}
	if firstInterim < 0 || firstInterim > firstSentence {
		t.Errorf("interim %d, first sentence %d", firstInterim, firstSentence)
	}
	if len(trOfType(frames, extwire.TFAck)) < 3 {
		t.Errorf("acks: %v", trOfType(frames, extwire.TFAck))
	}
	usage := trOfType(frames, extwire.TFUsage)
	if len(usage) == 0 || usage[len(usage)-1].Sessions != 3 || usage[len(usage)-1].Seconds < 3*trScriptLength {
		t.Errorf("usage %+v", usage)
	}
	if last := frames[len(frames)-1]; last.Type != extwire.TFDone || tc.status != websocket.StatusNormalClosure {
		t.Errorf("last frame %+v, close %d", last, tc.status)
	}
	starts := live.Starts()
	if len(starts) != 3 {
		t.Fatalf("starts %d", len(starts))
	}
	captions := 0
	for _, s := range starts {
		b, _ := json.Marshal(s)
		if mods, _ := json.Marshal(s["output_modalities"]); string(mods) != `["text"]` || strings.Contains(string(b), "language") ||
			s["voice"] != nil || s["tools"] != nil {
			t.Errorf("session start %s", b)
		}
		if ext, _ := json.Marshal(s["extensions"]); string(ext) != `{"input_transcript_partial":"v1"}` {
			t.Errorf("a session did not ask for the caption extension: %s", b)
		}
		ins, _ := s["instructions"].(string)
		switch {
		case ins == prompts.RenderVoiceTranscribeCaption(prompts.VoiceTranscribe{Lang: "zh-Hans"}):
			captions++
		case !strings.Contains(ins, "- Alice") || !strings.Contains(ins, "zh-CN"):
			t.Errorf("verbatim instructions: %s", ins)
		}
	}
	if captions != 1 {
		t.Errorf("%d caption sessions", captions)
	}
	noMeetingText(t, logs.String(), append(scriptTexts(trScript), "export cap", "Alice", "MB")...)
	if !strings.Contains(logs.String(), ": window 0 [0.0-6.1 s, 4.2 s of speech]: captions") || strings.Contains(logs.String(), "(dropped)") {
		t.Errorf("log:\n%s", logs)
	}
}

// Captions as the measured service writes them: the caption session's whole meeting is one item whose start_ms is the
// first speech's, counted from the session's start, and never changes; no caption is final; the next sentence's text
// replaces the last one's in the item. Each caption is told apart by its text and placed where the session's audio
// was when it came: every window has its own sentences' captions, no session is dropped against them, and every
// interim caption carries its sentence's start. A service that splits its captions by sentence (an item each, a final
// one at its end) gets the same.
func TestTranscriptionCaptionsAsMeasured(t *testing.T) {
	for name, items := range map[string]bool{"one item": false, "an item per sentence": true} {
		t.Run(name, func(t *testing.T) {
			trFast(t)
			_, srv, logs, _ := trLive(t, voicetest.Script{Sentences: trScript, CaptionItems: items})
			tc := startTr(t, srv, liveStart())
			tc.next(extwire.TFReady)
			tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
			tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
			frames := tc.done()
			defer func() {
				if t.Failed() {
					t.Log(logs.String())
				}
			}()
			sameTexts(t, frames, trScript)
			windows := 0
			for _, line := range strings.Split(logs.String(), "\n") {
				if !strings.Contains(line, ": window ") || strings.Contains(line, " 0.0 s of speech") {
					continue
				}
				windows++
				if strings.Contains(line, "captions 0 characters") || strings.Contains(line, "no captions") ||
					strings.Contains(line, "(dropped)") || strings.Contains(line, "the captions' text") {
					t.Errorf("window without its captions: %s", line)
				}
			}
			if windows < 3 {
				t.Errorf("%d windows", windows)
			}
			interim := 0
			for _, f := range trOfType(frames, extwire.TFInterim) {
				if *f.Text == "" {
					continue
				}
				interim++
				said := -1
				for i, l := range trScript {
					if strings.HasPrefix(l.Text, *f.Text) {
						said = i
					}
				}
				switch {
				case said < 0:
					t.Errorf("interim %q is the beginning of no sentence", *f.Text)
				case math.Abs(*f.Start-trScript[said].Start) > 0.6:
					t.Errorf("interim %q starts at %.2f, its sentence at %.1f", *f.Text, *f.Start, trScript[said].Start)
				}
			}
			if interim < len(trScript) {
				t.Errorf("%d interim captions", interim)
			}
		})
	}
}

// A caption session that revises each caption's first word at once, as the measured service did: the revised
// hypothesis is part of its caption, never a caption of its own. With both verbatim sessions inventing (dropped,
// then replaced) the first windows send the captions' text, which is what was said, once.
func TestTranscriptionCaptionRevisions(t *testing.T) {
	for name, items := range map[string]bool{"one item": false, "an item per sentence": true} {
		t.Run(name, func(t *testing.T) {
			trFast(t)
			_, srv, logs, _ := trLive(t, voicetest.Script{Sentences: trScript, CaptionItems: items, CaptionRevise: true,
				Rogue: map[int]int{1: 0, 2: 0}})
			tc := startTr(t, srv, liveStart())
			tc.next(extwire.TFReady)
			tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
			tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
			frames := tc.done()
			defer func() {
				if t.Failed() {
					t.Log(logs.String())
				}
			}()
			sameTexts(t, frames, trScript)
			if !strings.Contains(logs.String(), "sent the captions' text") {
				t.Error("no window sent the captions' text: the test checks nothing")
			}
		})
	}
}

// A verbatim session that invents replies is dropped for its windows (the other's text is sent) and replaced after
// two of them; the fresh session is trusted again.
func TestTranscriptionRogueSessionIsReplaced(t *testing.T) {
	trFast(t)
	_, srv, logs, live := trLive(t, voicetest.Script{Sentences: trScript, CaptionNoise: 0.1, Rogue: map[int]int{1: 0}})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	sameTexts(t, frames, trScript)
	if _, verbatim := live.Sessions(); verbatim != 3 {
		t.Errorf("%d verbatim sessions: the rogue one was not replaced", verbatim)
	}
	warned := false
	for _, f := range trOfType(frames, extwire.TFWarning) {
		warned = warned || f.Code == extwire.TWSessionDropped
	}
	if !warned || !strings.Contains(logs.String(), "replaced") || !strings.Contains(logs.String(), "(dropped)") {
		t.Errorf("warnings %v, log:\n%s", trOfType(frames, extwire.TFWarning), logs)
	}
	for _, r := range voicetest.InventedReplies {
		noMeetingText(t, strings.Join(trTexts(frames), "\n"), r)
	}
}

// Without the caption extension (not granted, or refused) a job runs three verbatim sessions and says no_interim; the
// sentences still come byte for byte, and the next job asks for no captions.
func TestTranscriptionWithoutCaptions(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "not granted", true: "refused"}[refuse], func(t *testing.T) {
			trFast(t)
			_, srv, logs, live := trLive(t, voicetest.Script{Sentences: trScript, NoCaptions: !refuse, RefuseCaptions: refuse})
			tc := startTr(t, srv, liveStart())
			ready := tc.next(extwire.TFReady)
			if strings.Join(ready.Features, ",") != "live" {
				t.Fatalf("ready %+v", ready)
			}
			if w := tc.next(extwire.TFWarning); w.Code != extwire.TWNoInterim {
				t.Fatalf("warning %+v", w)
			}
			tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
			tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
			frames := tc.done()
			sameTexts(t, frames, trScript)
			if len(trOfType(frames, extwire.TFInterim)) != 0 {
				t.Error("interim without captions")
			}
			if _, verbatim := live.Sessions(); verbatim != 3 {
				t.Errorf("%d verbatim sessions", verbatim)
			}
			if refuse != strings.Contains(logs.String(), "refused the interim-caption extension: started again without it") {
				t.Errorf("log:\n%s", logs)
			}
			asked := len(live.Starts())
			tc = startTr(t, srv, liveStart())
			if ready := tc.next(extwire.TFReady); strings.Join(ready.Features, ",") != "live" {
				t.Fatalf("second ready %+v", ready)
			}
			tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
			tc.done()
			for _, s := range live.Starts()[asked:] {
				if _, ok := s["extensions"]; ok {
					t.Error("the next job asked for captions again")
				}
			}
		})
	}
}

// Verbatim sessions ask for the caption extension too (and do not use its captions): with it the measured service ends
// a turn at each sentence and replies to each, where without it (and a prompt it does not follow sentence by
// sentence) it wrote several sentences in one late reply. Every sentence comes in its window, none left to the
// captions.
func TestTranscriptionVerbatimSessionsAskForCaptions(t *testing.T) {
	trFast(t)
	_, srv, logs, live := trLive(t, voicetest.Script{Sentences: trScript, CaptionNoise: 0.1, Bursts: 3})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	defer func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	}()
	sameTexts(t, frames, trScript)
	if strings.Contains(logs.String(), "(dropped)") || strings.Contains(logs.String(), "the captions' text") {
		t.Error("a verbatim session was dropped")
	}
	for _, s := range live.Starts() {
		if _, ok := s["extensions"]; !ok {
			t.Errorf("a session did not ask for the caption extension: %v", s["instructions"])
		}
	}
}

// Replies that come 0.1 and 0.2 s after their sentence ends, as the measured service's per-sentence replies did
// (0.05-0.3 s), and the stand-in's usual 0.5 s: each reply is of its own sentence's window, none a sentence behind,
// so no sentence is sent twice or left to the captions and no healthy session is dropped or replaced. Accelerated
// with the short settle times, and at real time with the production ones.
func TestTranscriptionFastReplies(t *testing.T) {
	gaps := []float64{0.1, 0.2, 0.5}
	check := func(t *testing.T, frames []extwire.TranscriptionFrame, logs *lockedLog) {
		t.Helper()
		defer func() {
			if t.Failed() {
				t.Log(logs.String())
			}
		}()
		sameTexts(t, frames, trScript)
		for _, bad := range []string{"(dropped)", "the captions' text", ": replaced"} {
			if strings.Contains(logs.String(), bad) {
				t.Errorf("the log has %q", bad)
			}
		}
	}
	for _, gap := range gaps {
		t.Run(fmt.Sprintf("accelerated, %.1f s", gap), func(t *testing.T) {
			trFast(t)
			_, srv, logs, _ := trLive(t, voicetest.Script{Sentences: trScript, CaptionNoise: 0.1, TurnGap: gap})
			tc := startTr(t, srv, liveStart())
			tc.next(extwire.TFReady)
			tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
			tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
			check(t, tc.done(), logs)
		})
	}
	t.Run("real time", func(t *testing.T) {
		trProduction(t)
		for _, gap := range gaps {
			t.Run(fmt.Sprintf("%.1f s", gap), func(t *testing.T) {
				t.Parallel()
				_, srv, logs, _ := trLive(t, voicetest.Script{Sentences: trScript, CaptionNoise: 0.08, TurnGap: gap})
				tc := startTr(t, srv, liveStart())
				tc.next(extwire.TFReady)
				tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 1)
				tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
				check(t, tc.done(), logs)
			})
		}
	})
}

// The instructions are in the recording's language when it is Chinese, English or Japanese (the language the model
// hears), else in the hub's (the caller's).
func TestTranscriptionInstructionsLanguage(t *testing.T) {
	trFast(t)
	_, srv, _, live := trLive(t, voicetest.Script{Sentences: trScript})
	opening := map[string]string{"zh-Hans": "你是", "en": "You are", "ja": "あなたは"}
	for _, tt := range []struct{ tag, hub, want string }{
		{"en-US", "zh-Hans", "en"},
		{"ja-JP", "en", "ja"},
		{"zh-TW", "en", "zh-Hans"},
		{"yue-HK", "en", "zh-Hans"},
		{"ko-KR", "ja", "ja"},
		{"", "en", "en"},
	} {
		asked := len(live.Starts())
		tc, _, err := dialTr(t, srv, TranscriptionContribution, http.Header{extwire.HeaderRole: {roleFull}, extwire.HeaderLang: {tt.hub}})
		if err != nil {
			t.Fatal(err)
		}
		tc.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: extwire.TranscriptionLive, Language: tt.tag, At: extwire.F(0.0)})
		tc.next(extwire.TFReady)
		tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
		tc.done()
		starts := live.Starts()[asked:]
		if len(starts) == 0 {
			t.Fatalf("%q: no session", tt.tag)
		}
		for _, s := range starts {
			if ins, _ := s["instructions"].(string); !strings.HasPrefix(ins, opening[tt.want]) {
				t.Errorf("%q (hub %s): instructions not in %s:\n%s", tt.tag, tt.hub, tt.want, ins)
			}
		}
	}
}

// A flush closes the window at once: flushed comes after every sentence that ended before it, before the next.
func TestTranscriptionFlush(t *testing.T) {
	trFast(t)
	setVar(t, &trWindow, 10*time.Second) // no window would close by itself before the flush
	_, srv, _, _ := trLive(t, voicetest.Script{Sentences: trScript})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	pcm := voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength)
	tc.stream(pcm[:2*16000*6], 20) // up to 6.0 s: two sentences said
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFFlush})
	flushed := tc.next(extwire.TFFlushed)
	if flushed.At == nil || *flushed.At != 6.0 {
		t.Fatalf("flushed %+v", flushed)
	}
	tc.mu.Lock()
	before := trTexts(tc.got)
	tc.mu.Unlock()
	if strings.Join(before, "\n") != strings.Join(scriptTexts(trScript[:2]), "\n") {
		t.Fatalf("before flushed: %q", before)
	}
	tc.stream(pcm[2*16000*6:], 20)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	sameTexts(t, tc.done(), trScript)
}

// A verbatim session whose connection drops is opened again and given the audio from the first window not sent yet:
// no sentence is lost or sent twice.
func TestTranscriptionReconnectReplaysTheBuffer(t *testing.T) {
	trFast(t)
	_, srv, logs, live := trLive(t, voicetest.Script{Sentences: trScript, DropAfter: map[int]float64{1: 8}})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	sameTexts(t, frames, trScript)
	if _, verbatim := live.Sessions(); verbatim != 3 {
		t.Errorf("%d verbatim sessions", verbatim)
	}
	if !strings.Contains(logs.String(), "lost after") || !strings.Contains(logs.String(), "(reconnect 1)") {
		t.Errorf("log:\n%s", logs)
	}
}

// Sessions are rolled over before the upstream's limit: the new one starts from the open window, the old one finishes
// the windows before it; nothing is lost or doubled.
func TestTranscriptionRollover(t *testing.T) {
	trFast(t)
	setVar(t, &trSessionMost, 400*time.Millisecond)
	setVar(t, &trRollOverlap, 200*time.Millisecond)
	_, srv, logs, live := trLive(t, voicetest.Script{Sentences: trScript})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	defer func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	}()
	sameTexts(t, frames, trScript)
	if strings.Contains(logs.String(), "(dropped)") || strings.Contains(logs.String(), "the captions' text") {
		t.Error("a healthy session was dropped across a rollover")
	}
	if caption, verbatim := live.Sessions(); caption < 2 || verbatim < 4 {
		t.Errorf("sessions %d %d: no rollover", caption, verbatim)
	}
	if !strings.Contains(logs.String(), "(rollover)") {
		t.Errorf("log:\n%s", logs)
	}
	if live.Open() != 0 {
		waitFor(t, "every session to close", func() bool { return live.Open() == 0 })
	}
}

// The upstream's failures end the job with the four codes.
func TestTranscriptionUpstreamFailures(t *testing.T) {
	trFast(t)
	for _, tt := range []struct {
		name string
		s    voicetest.Script
		code string
	}{
		{"refused key", voicetest.Script{Reject: http.StatusUnauthorized}, codeUpstreamForbidden},
		{"down", voicetest.Script{Reject: http.StatusServiceUnavailable}, codeUpstreamUnavailable},
		{"dropped for good", voicetest.Script{Sentences: trScript, FailAfter: 300 * time.Millisecond}, codeUpstreamFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, srv, _, _ := trLive(t, tt.s)
			tc := startTr(t, srv, liveStart())
			if tt.code == codeUpstreamFailed {
				tc.next(extwire.TFReady)
				go tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 5)
			}
			e := tc.next(extwire.TFError)
			if e.Code != tt.code || !e.Fatal || strings.Contains(e.Message, "refused by the script") {
				t.Fatalf("error %+v", e)
			}
			tc.done()
		})
	}
	for name, cfg := range map[string]Config{
		"no upstream":      {Setup: SetupKey},
		"no live upstream": {Providers: []Provider{{ID: "c", URL: "wss://example.com/v1/realtime", Dialect: DialectCodex, Key: "k"}}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := bareTrPlugin(t, cfg)
			tc := startTr(t, srv, liveStart())
			if e := tc.next(extwire.TFError); e.Code != codeNoUpstream || !e.Fatal {
				t.Fatalf("error %+v", e)
			}
			tc.done()
		})
	}
}

// A malformed job is refused with bad_frame.
func TestTranscriptionBadFrames(t *testing.T) {
	trFast(t)
	_, srv, _, _ := trLive(t, voicetest.Script{Sentences: trScript})
	for name, send := range map[string]func(tc *trClient){
		"audio first":  func(tc *trClient) { _ = tc.c.Write(context.Background(), websocket.MessageBinary, make([]byte, 320)) },
		"another mode": func(tc *trClient) { tc.send(extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: "batch"}) },
		"odd audio": func(tc *trClient) {
			tc.send(liveStart())
			tc.next(extwire.TFReady)
			_ = tc.c.Write(context.Background(), websocket.MessageBinary, make([]byte, 321))
		},
	} {
		t.Run(name, func(t *testing.T) {
			tc, _, err := dialTr(t, srv, TranscriptionContribution, nil)
			if err != nil {
				t.Fatal(err)
			}
			send(tc)
			if e := tc.next(extwire.TFError); e.Code != extwire.TEBadFrame || !e.Fatal {
				t.Fatalf("error %+v", e)
			}
			tc.done()
			if tc.status != websocket.StatusPolicyViolation {
				t.Errorf("closed with %d", tc.status)
			}
		})
	}
}

// replayStart is a final pass's start with the script's text as the reference, by sentence.
func replayStart(t *testing.T, srv *httptest.Server, speed float64, refs []voicetest.Line) *trClient {
	t.Helper()
	tc := startTr(t, srv, extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: extwire.TranscriptionReplay, Speed: speed,
		Language: "zh-CN", At: extwire.F(0.0)})
	if ready := tc.next(extwire.TFReady); strings.Join(ready.Features, ",") != "replay" {
		t.Fatalf("ready %+v", ready)
	}
	for _, r := range refs {
		tc.send(extwire.TranscriptionFrame{Type: extwire.TFReference, From: extwire.F(r.Start), To: extwire.F(r.End), Text: extwire.F(r.Text)})
	}
	return tc
}

// windowsCover fails unless the window frames cover the audio from 0 to its end, in order, without overlaps.
func windowsCover(t *testing.T, frames []extwire.TranscriptionFrame, end float64) []extwire.TranscriptionFrame {
	t.Helper()
	ws := trOfType(frames, extwire.TFWindow)
	at := 0.0
	for _, w := range ws {
		if *w.From != at || *w.To <= *w.From {
			t.Fatalf("windows %s", windowList(ws))
		}
		at = *w.To
	}
	if at != end {
		t.Fatalf("windows end at %.2f, not %.2f: %s", at, end, windowList(ws))
	}
	return ws
}

func windowList(ws []extwire.TranscriptionFrame) string {
	var b strings.Builder
	for _, w := range ws {
		b.WriteString(strings.TrimSpace(strings.Join([]string{" [", ftoa(*w.From), "-", ftoa(*w.To), map[bool]string{true: " kept", false: ""}[w.Kept], "]"}, "")))
	}
	return b.String()
}

func ftoa(f float64) string { b, _ := json.Marshal(f); return string(b) }

// The final pass goes through the audio at the job's speed in recording time, in fresh sessions per window (two with
// a reference), each told the window before's last sentences; its sentences are the script's and its windows cover
// the recording.
func TestTranscriptionReplay(t *testing.T) {
	trFast(t)
	_, srv, logs, live := trLive(t, voicetest.Script{Sentences: trScript})
	tc := replayStart(t, srv, 4, trScript[:2])
	pcm := voicetest.PCM(voicetest.Script{Sentences: trScript[:2]}, 6.5)
	began := time.Now()
	tc.stream(pcm, 1000)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	if took := time.Since(began); took < 1500*time.Millisecond {
		t.Errorf("6.5 s of audio went through at speed 4 in %v", took)
	}
	sameTexts(t, frames, trScript[:2])
	ws := windowsCover(t, frames, 6.5)
	if ws[0].Kept {
		t.Errorf("windows %s", windowList(ws))
	}
	if _, verbatim := live.Sessions(); verbatim != 2 {
		t.Errorf("%d sessions for one window with a reference", verbatim)
	}
	noMeetingText(t, logs.String(), scriptTexts(trScript)...)

	// The whole script at a higher speed: several windows, the later ones told the sentences before them.
	tc = replayStart(t, srv, 40, trScript)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 1000)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames = tc.done()
	sameTexts(t, frames, trScript)
	if ws := windowsCover(t, frames, trScriptLength); len(ws) < 3 {
		t.Errorf("windows %s", windowList(ws))
	}
	told := false
	for _, s := range live.Starts() {
		ins, _ := s["instructions"].(string)
		told = told || strings.Contains(ins, trScript[1].Text)
	}
	if !told {
		t.Error("no window's sessions were told the sentences before it")
	}
}

// With a reference, a window none of whose sessions is near it keeps the reference (kept, no sentences); a session
// that stops early is dropped.
func TestTranscriptionReplayKeepsTheReference(t *testing.T) {
	trFast(t)
	_, srv, logs, _ := trLive(t, voicetest.Script{Sentences: trScript, Rogue: map[int]int{1: 0, 2: 0}, Mute: map[int]int{3: 2}})
	tc := replayStart(t, srv, 40, trScript)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 1000)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	ws := windowsCover(t, frames, trScriptLength)
	if !ws[0].Kept || ws[1].Kept {
		t.Fatalf("windows %s", windowList(ws))
	}
	texts := trTexts(frames)
	if len(texts) == 0 || texts[0] == trScript[0].Text || texts[0] == trScript[1].Text {
		t.Fatalf("sentences %q", texts)
	}
	for _, r := range voicetest.InventedReplies {
		noMeetingText(t, strings.Join(texts, "\n"), r)
	}
	if !strings.Contains(logs.String(), "(stopped early)") {
		t.Errorf("log:\n%s", logs)
	}
}

// A poor reference does not keep a window's text from the sessions. The live check's opening window had as its
// reference a live caption that caught a few words of it (22 characters over 14 s of speech) while both sessions
// heard all of it: a reference much shorter than the sessions' text is not checked against, and the sessions, which
// agree, are trusted. Sessions that agree with each other are trusted too over a reference of their length none of
// them is near (another sentence's caption).
func TestTranscriptionReplayPoorReference(t *testing.T) {
	trFast(t)
	_, srv, logs, _ := trLive(t, voicetest.Script{Sentences: trScript})
	for _, tt := range []struct {
		name string
		refs []voicetest.Line
		log  string
	}{
		{"too short", []voicetest.Line{{Start: 0.5, End: 5.6, Text: "Ford cap 这词还是"}}, "too short to check the sessions against"},
		{"another sentence", []voicetest.Line{{Start: 0.5, End: 3.0, Text: trScript[4].Text}, {Start: 3.8, End: 5.6, Text: trScript[3].Text}},
			"the sessions agree with each other, not with the reference"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tc := replayStart(t, srv, 40, tt.refs)
			tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript[:2]}, 6.5), 1000)
			tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
			frames := tc.done()
			sameTexts(t, frames, trScript[:2])
			if ws := windowsCover(t, frames, 6.5); ws[0].Kept {
				t.Errorf("windows %s", windowList(ws))
			}
			if !strings.Contains(logs.String(), tt.log) {
				t.Errorf("log:\n%s", logs)
			}
		})
	}
}

// The live check's opening window as measured: a reference too short to check against (a few words of a live caption)
// and of the two sessions one heard all of it while the other said nothing. One session is never taken uncorroborated:
// the window gets a third session, as a window without a reference has, and is chosen as without one.
func TestTranscriptionReplayPoorReferenceOneSession(t *testing.T) {
	trFast(t)
	_, srv, logs, live := trLive(t, voicetest.Script{Sentences: trScript, Mute: map[int]int{2: 0}})
	tc := replayStart(t, srv, 40, []voicetest.Line{{Start: 0.5, End: 5.6, Text: "Ford cap 这词还是"}})
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript[:2]}, 6.5), 1000)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	defer func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	}()
	sameTexts(t, frames, trScript[:2])
	if ws := windowsCover(t, frames, 6.5); ws[0].Kept {
		t.Errorf("windows %s", windowList(ws))
	}
	if _, verbatim := live.Sessions(); verbatim != 3 {
		t.Errorf("%d sessions for the window", verbatim)
	}
	if !strings.Contains(logs.String(), "too short to check the sessions against") {
		t.Error("the log does not say the reference was too short")
	}
}

// Without a reference, three sessions per window and two of them must agree; the reference's edges cut windows.
func TestTranscriptionReplayWithoutAReference(t *testing.T) {
	trFast(t)
	_, srv, _, live := trLive(t, voicetest.Script{Sentences: trScript, Rogue: map[int]int{1: 0}})
	// The phone's text covers the first two and the last two sentences: the middle (5.6 s to 13.2 s) has none.
	tc := replayStart(t, srv, 40, []voicetest.Line{trScript[0], trScript[1], trScript[4], trScript[5]})
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 1000)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	sameTexts(t, frames, trScript)
	ws := windowsCover(t, frames, trScriptLength)
	edges := map[float64]bool{}
	for _, w := range ws {
		edges[*w.From] = true
	}
	if !edges[6.1] || !edges[12.7] {
		t.Errorf("windows %s are not cut where the reference stops and starts again", windowList(ws))
	}
	if _, verbatim := live.Sessions(); verbatim < 2+3+2 {
		t.Errorf("%d sessions", verbatim)
	}

	// Two of three sessions invent: no two agree, the window keeps the hub's text.
	_, srv, _, _ = trLive(t, voicetest.Script{Sentences: trScript, Rogue: map[int]int{1: 0, 2: 0}})
	tc = replayStart(t, srv, 40, nil)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript[:2]}, 6.5), 1000)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames = tc.done()
	if ws := windowsCover(t, frames, 6.5); !ws[0].Kept || len(trTexts(frames)) != 0 {
		t.Fatalf("windows %s, sentences %q", windowList(ws), trTexts(frames))
	}
}

// The fake dialect transcribes with no upstream: the script of PERSONA_VOICE_FAKE_TRANSCRIPT, or its own lines.
func TestTranscriptionFakeDialect(t *testing.T) {
	trFast(t)
	script, _ := json.Marshal([]map[string]any{{"start": 0.5, "end": 3.0, "text": trScript[0].Text}, {"start": 3.8, "end": 5.6, "text": trScript[1].Text}})
	t.Setenv(fakeTranscriptEnv, string(script))
	_, srv, _ := speakPlugin(t, Config{Providers: []Provider{{ID: "dev", Dialect: DialectFake}}})
	tc := startTr(t, srv, liveStart())
	if ready := tc.next(extwire.TFReady); strings.Join(ready.Features, ",") != "live,interim" {
		t.Fatalf("ready %+v", ready)
	}
	pcm := voicetest.PCM(voicetest.Script{Sentences: trScript[:2]}, 7)
	tc.stream(pcm, 50)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	got := trOfType(frames, extwire.TFSentence)
	if len(got) != 2 || *got[0].Text != trScript[0].Text || *got[0].Start != 0.5 || *got[1].End != 5.6 {
		t.Fatalf("sentences %v", trTexts(frames))
	}
	if len(trOfType(frames, extwire.TFInterim)) == 0 {
		t.Error("no interim captions")
	}

	tc = replayStart(t, srv, 40, nil)
	tc.stream(pcm, 1000)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames = tc.done()
	windowsCover(t, frames, 7)
	if len(trTexts(frames)) != 2 {
		t.Fatalf("replay sentences %q", trTexts(frames))
	}

	t.Setenv(fakeTranscriptEnv, "")
	tc = startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(pcm, 50)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	if got := trTexts(tc.done()); len(got) != 2 || got[0] != fakeMeeting[0] || got[1] != fakeMeeting[1] {
		t.Fatalf("built-in sentences %q", got)
	}
}

// A session that starts as its job ends is closed, never handed to a loop that is gone (its socket would stay open,
// billed, until the service dropped it).
func TestTranscriptionSessionStartedAsTheJobEndsIsClosed(t *testing.T) {
	trFast(t)
	pl, _, _, live := trLive(t, voicetest.Script{Sentences: trScript})
	ctx, cancel := context.WithCancel(context.Background())
	j := newTrJob(pl, nil, ctx, cancel, "")
	p, model, err := pl.transcriber(ctx)
	if err != nil {
		t.Fatal(err)
	}
	j.p, j.model = p, model
	started, ended := make(chan struct{}), make(chan struct{})
	setVar(t, &trStartedHook, func() {
		close(started)
		<-ended // the job ends while the session is on its way to the loop
	})
	newTrSession(j, trVerbatim, nil, 0, "first").connect(j.ctx, j.instructions(trVerbatim, nil))
	<-started
	cancel() // the loop is gone: nothing takes the session
	close(ended)
	waitFor(t, "the session closed", func() bool { return live.Open() == 0 })
}

// A verbatim session that fails to start before ready is tried again, as one that dropped is: the job does not run
// the whole meeting one session short.
func TestTranscriptionVerbatimStartTriedAgain(t *testing.T) {
	trFast(t)
	_, srv, logs, live := trLive(t, voicetest.Script{Sentences: trScript, RefuseVerbatim: 1})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	sameTexts(t, tc.done(), trScript)
	if _, verbatim := live.Sessions(); verbatim != 2 {
		t.Errorf("%d verbatim sessions started, log:\n%s", verbatim, logs)
	}
	if !strings.Contains(logs.String(), "did not start") || !strings.Contains(logs.String(), "(reconnect 1)") {
		t.Errorf("log:\n%s", logs)
	}
}

// Sessions are rolled over before the end session.started's expires_at says, when that comes before trSessionMost:
// the service never closes a session the job still writes with (no session_dropped, no reconnect), and nothing is
// lost.
func TestTranscriptionRolloverBeforeItExpires(t *testing.T) {
	trFast(t)
	setVar(t, &trExpiryMargin, 1500*time.Millisecond)
	setVar(t, &trRollOverlap, 200*time.Millisecond)
	_, srv, logs, _ := trLive(t, voicetest.Script{Sentences: trScript, ExpiresIn: 3 * time.Second})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 5)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	sameTexts(t, frames, trScript)
	for _, f := range trOfType(frames, extwire.TFWarning) {
		if f.Code == extwire.TWSessionDropped {
			t.Errorf("a session expired under the job, log:\n%s", logs)
		}
	}
	if !strings.Contains(logs.String(), "(rollover)") || strings.Contains(logs.String(), "(reconnect") {
		t.Errorf("log:\n%s", logs)
	}
}

// trScript4 are the script's first four sentences, 13.4 s with the silence after them.
var trScript4 = trScript[:4]

// A flush right after a sentence, against a service whose replies come 2 s after a sentence ends, with the
// production settle times at real time: the window it closes waits for every session's reply to its last speech, so
// flushed comes after every sentence said before it, none left to the captions.
func TestTranscriptionFlushWaitsForSlowReplies(t *testing.T) {
	trProduction(t)
	_, srv, logs, _ := trLive(t, voicetest.Script{Sentences: trScript4, CaptionNoise: 0.08, ReplyDelay: 2 * time.Second})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript4}, 12.6), 1) // flushed right after "Let's revisit pricing next week."
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFFlush})
	tc.next(extwire.TFFlushed)
	tc.mu.Lock()
	before := trTexts(tc.got)
	tc.mu.Unlock()
	if strings.Join(before, "\n") != strings.Join(scriptTexts(trScript4), "\n") {
		t.Fatalf("before flushed: %q\nlog:\n%s", before, logs)
	}
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	sameTexts(t, tc.done(), trScript4)
	if strings.Contains(logs.String(), "(dropped)") || strings.Contains(logs.String(), "the captions' text") {
		t.Errorf("a healthy session was dropped, log:\n%s", logs)
	}
}

// Replies that come 3.2 s after their sentence, past the settle of a window that closes right after it: each window
// waits for them, so no sentence is lost or moved to the wrong window.
func TestTranscriptionLateRepliesAcrossWindows(t *testing.T) {
	trProduction(t)
	setVar(t, &trWindow, 4*time.Second) // a window closes after nearly every sentence
	_, srv, logs, _ := trLive(t, voicetest.Script{Sentences: trScript4, CaptionNoise: 0.08, ReplyDelay: 3200 * time.Millisecond})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript4}, 13.4), 1)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	defer func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	}()
	sameTexts(t, frames, trScript4)
	if strings.Contains(logs.String(), "(dropped)") {
		t.Error("a healthy session was dropped")
	}
}

// A caption session rolled over starts at the live edge and is never given audio faster than real time (captions
// break faster than that): healthy verbatim sessions are never dropped against its captions.
func TestTranscriptionCaptionRolloverAtTheLiveEdge(t *testing.T) {
	trProduction(t)
	setVar(t, &trWindow, 4*time.Second)
	setVar(t, &trSessionMost, 3*time.Second)
	setVar(t, &trRollOverlap, time.Second)
	_, srv, logs, live := trLive(t, voicetest.Script{Sentences: trScript4, CaptionNoise: 0.05, CaptionNoiseFast: 1})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript4}, 13.4), 1)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	defer func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	}()
	sameTexts(t, frames, trScript4)
	if caption, _ := live.Sessions(); caption < 2 {
		t.Errorf("%d caption sessions: no rollover", caption)
	}
	if strings.Contains(logs.String(), "(dropped)") || strings.Contains(logs.String(), "the captions' text") {
		t.Error("a healthy session was dropped against a rolled-over caption session's captions")
	}
}

// Audio that comes in a burst after ready (the manager's backlog, a resend after a reconnect) leaves the caption
// session seconds behind the live edge. It skips to the edge rather than staying behind for good at real time, so
// interim captions come again at once; the window whose audio it skipped has no captions, the windows after it do,
// and every sentence comes.
func TestTranscriptionCaptionSkipsABurst(t *testing.T) {
	trProduction(t)
	setVar(t, &trWindow, 4*time.Second)
	_, srv, logs, _ := trLive(t, voicetest.Script{Sentences: trScript4, CaptionNoise: 0.05})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	pcm := voicetest.PCM(voicetest.Script{Sentences: trScript4}, 13.4)
	// The first 5 s in one frame: more than trCaptionJump behind, as a backlog sent back to back may be.
	burst := 2 * 16000 * 5
	if err := tc.c.Write(context.Background(), websocket.MessageBinary, pcm[:burst]); err != nil {
		t.Fatal(err)
	}
	tc.stream(pcm[burst:], 1)
	tc.mu.Lock()
	streamed := len(tc.got)
	tc.mu.Unlock()
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	defer func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	}()
	sameTexts(t, frames, trScript4)
	live := 0 // interim captions of the sentences said after the burst, while they were said
	for _, f := range trOfType(frames[:streamed], extwire.TFInterim) {
		if *f.Start >= 5.5 {
			live++
		}
	}
	if live < 10 {
		t.Errorf("%d interim captions after the burst", live)
	}
	var first, later string
	for _, line := range strings.Split(logs.String(), "\n") {
		switch {
		case strings.Contains(line, "window 0 ["):
			first = line
		case strings.Contains(line, "window ") && strings.Contains(line, "]: captions "):
			later = line
		}
	}
	if !strings.Contains(logs.String(), "skipped to it") || !strings.Contains(first, "no captions") || later == "" {
		t.Errorf("the caption session did not skip the burst, or the windows' captions are wrong:\n%s\n%s", first, later)
	}
	if strings.Contains(logs.String(), "(dropped)") {
		t.Error("a healthy session was dropped")
	}
}

// A quiet room: the sessions hear the sentences while the adapter's speech detection finds none. The windows are
// chosen all the same, each sentence once, in order.
func TestTranscriptionQuietRoom(t *testing.T) {
	trFast(t)
	quiet := voicetest.Script{Sentences: trScript, Level: 0.1}
	_, srv, logs, _ := trLive(t, quiet)
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(voicetest.PCM(quiet, trScriptLength), 20)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	frames := tc.done()
	if got := strings.Join(trTexts(frames), "\n"); got != strings.Join(scriptTexts(trScript), "\n") {
		t.Fatalf("sentences:\n%s\nlog:\n%s", got, logs)
	}
	if !strings.Contains(logs.String(), "0.0 s of speech") {
		t.Errorf("the detection heard speech: the test is not of a quiet room\n%s", logs)
	}
}

// A reply that writes two sentences (a service that answers both in one turn) is sent as its two sentences.
func TestTranscriptionMergedReply(t *testing.T) {
	trFast(t)
	setVar(t, &trWindow, 30*time.Second)
	setVar(t, &trWindowMost, 30*time.Second)
	_, srv, logs, _ := trLive(t, voicetest.Script{Sentences: trScript, Merge: map[int]bool{1: true, 3: true}})
	tc := startTr(t, srv, liveStart())
	tc.next(extwire.TFReady)
	tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
	tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
	if got := strings.Join(trTexts(tc.done()), "\n"); got != strings.Join(scriptTexts(trScript), "\n") {
		t.Fatalf("sentences:\n%s\nlog:\n%s", got, logs)
	}
}

// A service that refuses text-only output: its sessions start again without it (remembered for the provider), and
// the sentences come as before; one that also refuses the caption extension gets its verbatim sessions without both.
func TestTranscriptionTextOnlyRefused(t *testing.T) {
	for name, captions := range map[string]bool{"text only": false, "and the extension": true} {
		t.Run(name, func(t *testing.T) {
			trFast(t)
			_, srv, logs, live := trLive(t, voicetest.Script{Sentences: trScript, TextOnlyRefused: true, RefuseCaptions: captions})
			tc := startTr(t, srv, liveStart())
			tc.next(extwire.TFReady)
			tc.stream(voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength), 20)
			tc.send(extwire.TranscriptionFrame{Type: extwire.TFEnd})
			sameTexts(t, tc.done(), trScript)
			if !strings.Contains(logs.String(), "refused text-only output") {
				t.Errorf("log:\n%s", logs)
			}
			starts := live.Starts()
			if _, textOnly := starts[len(starts)-1]["output_modalities"]; textOnly {
				t.Error("a session started with text-only output after the service refused it")
			}
			if _, verbatim := live.Sessions(); captions && verbatim != 3 {
				t.Errorf("%d verbatim sessions", verbatim)
			}
		})
	}
}
