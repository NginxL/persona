package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/voice/voicetest"
)

// dictationTestPlugin has a function-tool-capable upstream so a dictation session that accidentally inherits an
// ordinary call's tools is visible in its start. The hub likewise records any attempt to bind a conversation.
func dictationTestPlugin(t *testing.T, live *voicetest.Live, inputRate ...int) (*Plugin, *fakeHub) {
	t.Helper()
	hub := newFakeHub(t)
	provider := Provider{ID: "rt", URL: live.URL(), Dialect: DialectLive, Key: "k", Models: []string{"m1"}, Tools: new(true)}
	if len(inputRate) > 0 {
		provider.InputRate = inputRate[0]
	}
	pl := testPlugin(t, Config{Default: "rt/m1", Providers: []Provider{provider}}, hub.server(t))
	return pl, hub
}

func dictationTestStart() map[string]any {
	return map[string]any{"type": "start", "protocol": 2, "mode": "dictation", "model": "rt/m1"}
}

func dictationTestReady(t *testing.T, pl *Plugin, header ...http.Header) (*app, map[string]any) {
	t.Helper()
	a := dialApp(t, pl, dictationTestStart(), header...)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case f, ok := <-a.frames:
			if !ok {
				t.Fatal("the plugin closed dictation before ready")
			}
			switch f["type"] {
			case "ready":
				return a, f
			case "error":
				t.Fatalf("dictation negotiation/start failed: %v", f)
			default:
				t.Fatalf("unexpected frame before dictation ready: %v", f)
			}
		case audio := <-a.audio:
			t.Fatalf("dictation returned %d bytes of audio before ready", len(audio))
		case <-timer.C:
			t.Fatal("timed out waiting for dictation ready")
		}
	}
}

type dictationTestInfoFrame struct {
	Protocol int      `json:"protocol"`
	Features []string `json:"features"`
}

func dictationTestInfo(t *testing.T, pl *Plugin) dictationTestInfoFrame {
	t.Helper()
	srv := httptest.NewServer(pl.Handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/info", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /info: %s", res.Status)
	}
	var out dictationTestInfoFrame
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func dictationTestFeatureNames(t *testing.T, v any) []string {
	t.Helper()
	values, ok := v.([]any)
	if !ok {
		t.Fatalf("features is %T (%v), not a JSON array", v, v)
	}
	out := make([]string, len(values))
	for i, value := range values {
		name, ok := value.(string)
		if !ok {
			t.Fatalf("feature %d is %T (%v), not a string", i, value, value)
		}
		out[i] = name
	}
	return out
}

// dictationTestAssertShape checks the parts of the public live-dialect session start dictated by the mode. A
// fallback start has wantTextOnly false and must omit output_modalities altogether.
func dictationTestAssertShape(t *testing.T, sess map[string]any, wantTextOnly bool) {
	t.Helper()
	modalities, hasModalities := sess["output_modalities"]
	if wantTextOnly {
		values, ok := modalities.([]any)
		if !hasModalities || !ok || len(values) != 1 || values[0] != "text" {
			t.Errorf("output_modalities = %v, want exactly [text]", modalities)
		}
	} else if hasModalities {
		t.Errorf("fallback start still has output_modalities: %v", modalities)
	}
	if tools, ok := sess["tools"]; ok {
		t.Errorf("dictation declared function tools: %v", tools)
	}
	if delegation, ok := sess["delegation"]; ok {
		t.Errorf("dictation declared delegation: %v", delegation)
	}
}

func dictationTestWriteAudio(t *testing.T, a *app, pcm []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := a.c.Write(ctx, websocket.MessageBinary, pcm); err != nil {
		t.Fatalf("write dictation audio: %v", err)
	}
}

// dictationTestNextUserTranscript accepts only user transcript frames. In particular, reaching a later scripted
// caption is also a barrier proving that all earlier generated replies were discarded: upstream WebSocket frames are
// ordered, and any assistant transcript, audio, tool/run/call event, or fatal error fails here.
func dictationTestNextUserTranscript(t *testing.T, a *app) map[string]any {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case f, ok := <-a.frames:
			if !ok {
				t.Fatal("the plugin closed dictation before the expected transcript")
			}
			if f["type"] != "transcript" {
				t.Fatalf("dictation emitted a reply-driven call frame: %v", f)
			}
			if f["role"] != "user" {
				t.Fatalf("dictation forwarded a generated transcript: %v", f)
			}
			id, idOK := f["id"].(string)
			text, textOK := f["text"].(string)
			if !idOK || id == "" || !textOK || strings.TrimSpace(text) == "" {
				t.Fatalf("bad user transcript: %v", f)
			}
			return f
		case audio := <-a.audio:
			t.Fatalf("dictation forwarded %d bytes of generated audio", len(audio))
		case <-timer.C:
			t.Fatal("timed out waiting for a dictation user transcript")
		}
	}
}

// dictationTestFinal requires at least one interim frame followed by a whole final frame for the same upstream item.
// Every intervening generated reply event must remain invisible to the app.
func dictationTestFinal(t *testing.T, a *app, item, wantText string) map[string]any {
	t.Helper()
	sawInterim := false
	for {
		f := dictationTestNextUserTranscript(t, a)
		if f["id"] != item {
			t.Fatalf("transcript id = %v, want stable upstream item %s", f["id"], item)
		}
		final, ok := f["final"].(bool)
		if !ok {
			t.Fatalf("transcript final = %v (%T), want a boolean", f["final"], f["final"])
		}
		if !final {
			sawInterim = true
			continue
		}
		if !sawInterim {
			t.Fatalf("final transcript arrived without a prior interim frame: %v", f)
		}
		if f["text"] != wantText {
			t.Fatalf("final transcript text = %q, want %q", f["text"], wantText)
		}
		return f
	}
}

func dictationTestFatal(t *testing.T, a *app, code string) map[string]any {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case f, ok := <-a.frames:
			if !ok {
				t.Fatal("the plugin closed dictation without an error frame")
			}
			if f["type"] == "ready" {
				t.Fatalf("dictation became ready despite upstream failure: %v", f)
			}
			if f["type"] != "error" {
				t.Fatalf("unexpected frame while waiting for upstream failure: %v", f)
			}
			message, _ := f["message"].(string)
			if f["fatal"] != true || f["code"] != code || strings.TrimSpace(message) == "" {
				t.Fatalf("fatal error = %v, want code %s, fatal=true and a message", f, code)
			}
			return f
		case audio := <-a.audio:
			t.Fatalf("dictation returned %d bytes of audio after upstream failure", len(audio))
		case <-timer.C:
			t.Fatal("timed out waiting for a dictation upstream failure")
		}
	}
}

func dictationTestClosed(t *testing.T, a *app) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case f, ok := <-a.frames:
			if !ok {
				return
			}
			t.Fatalf("frame after fatal dictation error: %v", f)
		case audio := <-a.audio:
			t.Fatalf("audio after fatal dictation error: %d bytes", len(audio))
		case <-timer.C:
			t.Fatal("the plugin did not close dictation after its fatal error")
		}
	}
}

// The speaker starts before the app's start is read. Let its 100 ms tick run before negotiation, then check the mode
// negotiation under the race detector (go test -race): the write and every speaker read must be synchronized.
func TestDictationStartSynchronizesSpeaker(t *testing.T) {
	live := voicetest.NewLive(t, voicetest.Script{})
	pl, _ := dictationTestPlugin(t, live)
	a := dialApp(t, pl, nil)
	time.AfterFunc(250*time.Millisecond, func() { a.send(dictationTestStart()) })
	ready := a.until(ofType("ready"))
	if ready["protocol"] != float64(2) {
		t.Fatalf("dictation ready = %v", ready)
	}
}

func TestDictationInfo(t *testing.T) {
	live := voicetest.NewLive(t, voicetest.Script{})
	pl, _ := dictationTestPlugin(t, live)
	info := dictationTestInfo(t, pl)
	if !slices.Contains(info.Features, "dictation") {
		t.Fatalf("GET /info features = %v, want dictation", info.Features)
	}
}

func TestDictationNegotiation(t *testing.T) {
	live := voicetest.NewLive(t, voicetest.Script{})
	pl, hub := dictationTestPlugin(t, live)
	want := dictationTestInfo(t, pl).Features
	_, ready := dictationTestReady(t, pl)
	got := dictationTestFeatureNames(t, ready["features"])
	if !slices.Equal(got, want) {
		t.Fatalf("ready features = %v, GET /info features = %v", got, want)
	}
	if !slices.Contains(got, "dictation") {
		t.Fatalf("ready features = %v, want dictation", got)
	}
	if target := ready["target"]; target != nil {
		t.Fatalf("dictation bound a conversation target: %v", target)
	}
	if looks := hub.lookCount(); looks != 0 {
		t.Fatalf("dictation looked up a conversation target %d times", looks)
	}
}

func TestDictationSessionShape(t *testing.T) {
	live := voicetest.NewLive(t, voicetest.Script{})
	pl, hub := dictationTestPlugin(t, live, 24000)
	_, ready := dictationTestReady(t, pl)
	starts := live.Starts()
	if len(starts) != 1 {
		t.Fatalf("dictation made %d upstream starts, want 1", len(starts))
	}
	dictationTestAssertShape(t, starts[0], true)
	if ready["input_rate"] != float64(24000) {
		t.Errorf("ready input_rate = %v, want the provider's 24000", ready["input_rate"])
	}
	audio, _ := starts[0]["audio"].(map[string]any)
	format, _ := audio["format"].(map[string]any)
	if format["rate"] != float64(24000) {
		t.Errorf("upstream input rate = %v, want the provider's 24000", format["rate"])
	}
	if ready["target"] != nil || hub.lookCount() != 0 {
		t.Fatalf("dictation used conversation binding: target=%v lookups=%d", ready["target"], hub.lookCount())
	}
	if captions, verbatim := live.Sessions(); captions != 0 || verbatim != 1 {
		t.Fatalf("started sessions = %d caption, %d verbatim; want 0, 1", captions, verbatim)
	}
}

func TestDictationTranscriptsSuppressReplies(t *testing.T) {
	script := voicetest.Script{FinalCaptions: true, StandardCaptions: true, ReplyAudio: true, ReplyCalls: true, ReplyCancelled: true, Sentences: []voicetest.Line{
		{Start: 0.2, End: 1.4, Text: "Alpha first dictated sentence."},
		{Start: 2.0, End: 3.2, Text: "Bravo second dictated sentence."},
		{Start: 3.8, End: 5.0, Text: "Charlie third dictated sentence."},
	}}
	live := voicetest.NewLive(t, script)
	pl, hub := dictationTestPlugin(t, live)
	a, _ := dictationTestReady(t, pl)
	dictationTestWriteAudio(t, a, voicetest.PCM(script, 5.8))

	dictationTestFinal(t, a, "item-1-1", "Alpha first dictated sentence.")
	dictationTestFinal(t, a, "item-1-2", "Bravo second dictated sentence.")
	dictationTestFinal(t, a, "item-1-3", "Charlie third dictated sentence.")

	// Each final caption follows that turn's interim captions. The second and third are also barriers after earlier
	// response audio, client delegation, function call, text, and cancellation frames. The transcript reader above
	// would have failed on any generated reply reaching the app, including audio and tool/run/clear frames.
	if messages := hub.messages(); len(messages) != 0 {
		t.Fatalf("dictation sent generated work to a conversation: %+v", messages)
	}
	if subscribes, _ := hub.subscribes("main1"); subscribes != 0 {
		t.Fatalf("dictation subscribed to the main conversation %d times", subscribes)
	}
	if starts := live.Starts(); len(starts) != 1 {
		t.Fatalf("dictation used %d upstream sessions while forwarding transcripts, want 1", len(starts))
	}
}

func TestDictationOneSessionPerStart(t *testing.T) {
	live := voicetest.NewLive(t, voicetest.Script{})
	pl, _ := dictationTestPlugin(t, live)
	dictationTestReady(t, pl)
	dictationTestReady(t, pl)

	starts := live.Starts()
	if len(starts) != 2 {
		t.Fatalf("two dictations made %d upstream starts, want exactly 2", len(starts))
	}
	for i, start := range starts {
		t.Run(fmt.Sprintf("session %d", i+1), func(t *testing.T) {
			dictationTestAssertShape(t, start, true)
		})
	}
	if captions, verbatim := live.Sessions(); captions != 0 || verbatim != 2 {
		t.Fatalf("two dictations have %d caption and %d verbatim sessions, want 0 and 2", captions, verbatim)
	}
	if open := live.Open(); open != 2 {
		t.Fatalf("two active dictations have %d open upstream sessions, want 2", open)
	}
}

func TestDictationCaptionRefusalUsesStandardTranscripts(t *testing.T) {
	script := voicetest.Script{RefuseCaptions: true, StandardCaptions: true, Sentences: []voicetest.Line{
		{Start: 0.2, End: 1.4, Text: "Standard transcript after caption refusal."},
	}}
	live := voicetest.NewLive(t, script)
	pl, _ := dictationTestPlugin(t, live)
	a, _ := dictationTestReady(t, pl)

	starts := live.Starts()
	if len(starts) != 2 {
		t.Fatalf("caption refusal caused %d starts, want the refused start and one standard-transcript fallback", len(starts))
	}
	for i, start := range starts {
		dictationTestAssertShape(t, start, true)
		if _, asked := start["extensions"]; asked != (i == 0) {
			t.Errorf("start %d extensions = %v, asked %v", i+1, start["extensions"], i == 0)
		}
	}
	dictationTestWriteAudio(t, a, voicetest.PCM(script, 2.2))
	dictationTestFinal(t, a, "item-2-1", "Standard transcript after caption refusal.")
	if got := trMemoOf(&pl.cfg.Providers[0]).captions.Load(); got != trUnknown {
		t.Fatalf("dictation changed meeting transcription's caption memory to %d", got)
	}
}

func TestDictationRequiresLiveUpstream(t *testing.T) {
	live := voicetest.NewLive(t, voicetest.Script{})
	hub := newFakeHub(t)
	pl := testPlugin(t, Config{Default: "codex/m1", Providers: []Provider{{
		ID: "codex", URL: live.URL(), Dialect: DialectCodex, Key: "k", Models: []string{"m1"},
	}}}, hub.server(t))
	a := dialApp(t, pl, dictationTestStart())
	dictationTestFatal(t, a, codeNoUpstream)
	dictationTestClosed(t, a)
	if handshakes := live.Handshakes(); handshakes != 0 {
		t.Fatalf("dictation without a live upstream made %d handshakes, want 0", handshakes)
	}
}

func TestDictationTextOnlyRefusal(t *testing.T) {
	script := voicetest.Script{FinalCaptions: true, TextOnlyRefused: true, Sentences: []voicetest.Line{
		{Start: 0.2, End: 1.4, Text: "Fallback first dictated sentence."},
		{Start: 2.0, End: 3.2, Text: "Fallback second dictated sentence."},
	}}
	live := voicetest.NewLive(t, script)
	pl, _ := dictationTestPlugin(t, live)
	a, _ := dictationTestReady(t, pl)

	starts := live.Starts()
	if len(starts) != 2 {
		t.Fatalf("text-only refusal caused %d starts, want the refused start and one fallback", len(starts))
	}
	dictationTestAssertShape(t, starts[0], true)
	dictationTestAssertShape(t, starts[1], false)
	if captions, verbatim := live.Sessions(); captions != 0 || verbatim != 1 {
		t.Fatalf("fallback has %d caption and %d compatible sessions, want 0 and 1", captions, verbatim)
	}

	dictationTestWriteAudio(t, a, voicetest.PCM(script, 4.0))
	dictationTestFinal(t, a, "item-2-1", "Fallback first dictated sentence.")
	dictationTestFinal(t, a, "item-2-2", "Fallback second dictated sentence.")
}

func TestDictationUpstreamFailure(t *testing.T) {
	for _, tt := range []struct {
		name   string
		script voicetest.Script
		code   string
		starts int
	}{
		{name: "connection unavailable", script: voicetest.Script{Reject: http.StatusServiceUnavailable}, code: codeUpstreamUnavailable, starts: 0},
		{name: "session start unavailable", script: voicetest.Script{RefuseVerbatim: 1}, code: codeUpstreamUnavailable, starts: 1},
		{name: "generic startup refusal", script: voicetest.Script{Reject: http.StatusBadRequest}, code: codeUpstreamFailed, starts: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			live := voicetest.NewLive(t, tt.script)
			pl, hub := dictationTestPlugin(t, live)
			a := dialApp(t, pl, dictationTestStart())
			dictationTestFatal(t, a, tt.code)
			dictationTestClosed(t, a)
			if starts := live.Starts(); len(starts) != tt.starts {
				t.Fatalf("upstream starts = %d, want %d (no silent retry sessions)", len(starts), tt.starts)
			}
			if handshakes := live.Handshakes(); handshakes != 1 {
				t.Fatalf("upstream handshakes = %d, want exactly 1 (including rejected handshakes)", handshakes)
			}
			if captions, verbatim := live.Sessions(); captions != 0 || verbatim != 0 {
				t.Fatalf("failure left %d caption and %d verbatim sessions, want none", captions, verbatim)
			}
			if looks := hub.lookCount(); looks != 0 {
				t.Fatalf("failed dictation looked up a conversation %d times", looks)
			}
		})
	}
}

func TestDictationConnectionLossDoesNotReconnect(t *testing.T) {
	for _, tt := range []struct{ lang, message string }{
		{"en", "The dictation connection was lost. Start dictation again"},
		{"zh-Hans", "听写连接断了，请重新开始听写"},
		{"ja", "音声入力の接続が切れました。音声入力を再開してください"},
	} {
		t.Run(tt.lang, func(t *testing.T) {
			script := voicetest.Script{DropAfter: map[int]float64{1: 0.2}}
			live := voicetest.NewLive(t, script)
			pl, _ := dictationTestPlugin(t, live)
			a, _ := dictationTestReady(t, pl, http.Header{extwire.HeaderLang: {tt.lang}})
			dictationTestWriteAudio(t, a, voicetest.PCM(script, 0.4))

			failure := dictationTestFatal(t, a, codeUpstreamFailed)
			message, _ := failure["message"].(string)
			if !strings.HasPrefix(message, tt.message) || len(message) <= len(tt.message) {
				t.Fatalf("dictation loss message = %q, want localized prefix %q and upstream reason", message, tt.message)
			}
			dictationTestClosed(t, a)
			if starts := live.Starts(); len(starts) != 1 {
				t.Fatalf("upstream starts after connection loss = %d, want exactly 1", len(starts))
			}
			if handshakes := live.Handshakes(); handshakes != 1 {
				t.Fatalf("upstream handshakes after connection loss = %d, want exactly 1 (no reconnect)", handshakes)
			}
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case <-live.Done(1):
			case <-timer.C:
				t.Fatal("the upstream session did not finish closing after the dictation ended")
			}
			if open := live.Open(); open != 0 {
				t.Fatalf("open upstream sessions after fatal connection loss = %d, want 0", open)
			}
		})
	}
}

// The new mode must not change an ordinary call's session or consume its generated reply. This control uses the same
// scripted upstream as the dictation tests; the existing meeting and speak suites cover those separate behaviors.
func TestDictationOrdinaryCallUnchanged(t *testing.T) {
	script := voicetest.Script{Sentences: []voicetest.Line{{Start: 0.1, End: 0.7, Text: "An ordinary assistant reply."}}}
	live := voicetest.NewLive(t, script)
	pl, _ := dictationTestPlugin(t, live)
	a := dialApp(t, pl, map[string]any{"type": "start", "model": "rt/m1"})
	a.until(ofType("ready"))
	starts := live.Starts()
	if len(starts) != 1 {
		t.Fatalf("ordinary call made %d starts, want 1", len(starts))
	}
	if _, ok := starts[0]["output_modalities"]; ok {
		t.Fatalf("ordinary call unexpectedly requested text-only output: %v", starts[0]["output_modalities"])
	}
	if delegation := starts[0]["delegation"]; delegation == nil {
		t.Fatal("ordinary call lost its delegation declaration")
	}
	dictationTestWriteAudio(t, a, voicetest.PCM(script, 1.5))
	a.until(finalLine("assistant", "An ordinary assistant reply."))
}
