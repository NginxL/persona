package volc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modules/volc/volctest"
)

func speak(t *testing.T, pl *Plugin, body, role string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(pl.Handler())
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/speech/volc/speak", strings.NewReader(body))
	if role != "" {
		req.Header.Set(extwire.HeaderRole, role)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func errCode(t *testing.T, res *http.Response) string {
	t.Helper()
	var v struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(res.Body).Decode(&v)
	return v.Code
}

// Read-aloud: the voice extension's contract (audio/pcm, its rate, the audio as it comes) in the voice set in the
// settings; a refusal is an error status before any audio.
func TestSpeak(t *testing.T) {
	fv := fake(t)
	pl := testPlugin(t, Form{Voice: "zh_male_m191_uranus_bigtts"}, "test-key")
	res := speak(t, pl, `{"text":"  会议在周五。 ","lang":"zh-CN"}`, "full") // i18n-ignore: test data
	audio, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "audio/pcm" || res.Header.Get("X-Persona-Audio-Rate") != "24000" ||
		res.Header.Get("Cache-Control") != "no-store" || len(audio) != 6*480 {
		t.Errorf("speak: %d %v, %d bytes", res.StatusCode, res.Header, len(audio))
	}
	got := fv.Syntheses()[0]
	if got.Text != "会议在周五。" || got.Speaker != "zh_male_m191_uranus_bigtts" || got.Format != "pcm" || got.Rate != 24000 || // i18n-ignore: test data
		got.Header.Get("X-Api-Key") != "test-key" || got.Header.Get("X-Api-Resource-Id") != "seed-tts-2.0" {
		t.Errorf("request %+v", got)
	}
	// A voice the device picked for another extension is not ours: the settings' voice.
	speak(t, pl, `{"text":"好","voice":"alloy"}`, "full") // i18n-ignore: test data
	if v := fv.Syntheses()[1].Speaker; v != "zh_male_m191_uranus_bigtts" {
		t.Errorf("voice %s", v)
	}

	for _, c := range []struct {
		name, body, role string
		form             Form
		key              string
		status           int
		code             string
	}{
		{"answer-only", `{"text":"x"}`, "answer", Form{}, "k", 403, "forbidden"},
		{"no text", `{"text":"  "}`, "full", Form{}, "k", 400, "bad_request"},
		{"too long", `{"text":"` + strings.Repeat("长", 1001) + `"}`, "full", Form{}, "k", 400, "bad_request"}, // i18n-ignore: test data
		{"no key", `{"text":"x"}`, "full", Form{}, "", 503, "no_upstream"},
		{"off", `{"text":"x"}`, "full", Form{ReadAnswers: new(bool)}, "k", 503, "off"},
	} {
		res := speak(t, testPlugin(t, c.form, c.key), c.body, c.role)
		if code := errCode(t, res); res.StatusCode != c.status || code != c.code {
			t.Errorf("%s: %d %s, want %d %s", c.name, res.StatusCode, code, c.status, c.code)
		}
	}

	fv.Set(func(s *volctest.Script) {
		s.TTSStatus, s.TTSBody = 401, `{"code":45000010,"message":"invalid api key"}`
	})
	if res := speak(t, pl, `{"text":"x"}`, ""); res.StatusCode != 502 || errCode(t, res) != "upstream_forbidden" {
		t.Errorf("refused key: %d", res.StatusCode)
	}
	fv.Set(func(s *volctest.Script) {
		s.TTSStatus, s.TTSLineCode, s.TTSLineMessage = 0, 45000000, "quota exceeded for types: concurrency"
	})
	if res := speak(t, pl, `{"text":"x"}`, ""); res.StatusCode != 503 || errCode(t, res) != "upstream_unavailable" {
		t.Errorf("busy: %d", res.StatusCode)
	}
	if asr, tts := pl.board.get(); asr != nil || tts == nil || tts.OK || tts.Kind != failBusy {
		t.Errorf("board after a failed read-aloud: %+v %+v", asr, tts)
	}
}

func TestTTSPieces(t *testing.T) {
	long := strings.Repeat("这是一句话。", 120) // 720 characters // i18n-ignore: test data
	pieces := ttsPieces(long)
	if len(pieces) != 3 || strings.Join(pieces, "") != long {
		t.Fatalf("%d pieces", len(pieces))
	}
	for _, p := range pieces {
		if n := len([]rune(p)); n > ttsChunkRunes || !strings.HasSuffix(p, "。") { // i18n-ignore: test data
			t.Errorf("a piece of %d ending %q", n, p[len(p)-3:])
		}
	}
	if p := ttsPieces(strings.Repeat("a", 650)); len(p) != 3 || len(p[0]) != ttsChunkRunes {
		t.Errorf("no breaks: %d pieces", len(p))
	}
	if ttsResource("zh_female_cancan_mars_bigtts") != "seed-tts-1.0" || ttsResource("zh_female_vv_uranus_bigtts") != "seed-tts-2.0" {
		t.Error("resource by voice")
	}
}

// settings.check: nothing to try without a key; a refused key, or a model not turned on, refuses the save with what
// to do next; synthesis is tried only while answers are read aloud.
func TestSettingsCheck(t *testing.T) {
	fast(t)
	fv := fake(t)
	pl := testPlugin(t, Form{}, "")
	check := func(f Form, key string) wire.SettingsCheckResult {
		t.Helper()
		params, _ := json.Marshal(wire.SettingsCheckParams{Config: mustJSON(f), Secrets: map[string]string{SecretKey: key}})
		out, err := pl.settings(context.Background(), "check", params)
		if err != nil {
			t.Fatal(err)
		}
		return out.(wire.SettingsCheckResult)
	}
	if r := check(Form{}, ""); !r.Ok || fv.Count() != 0 {
		t.Errorf("no key: %+v, %d sessions", r, fv.Count())
	}
	if r := check(Form{}, "good-key"); !r.Ok || fv.Count() != 1 || len(fv.Syntheses()) != 1 {
		t.Errorf("good key: %+v, %d sessions, %d syntheses", r, fv.Count(), len(fv.Syntheses()))
	}
	if s := fv.Session(0); s.Packets != probeAudio+1 || !s.Last {
		t.Errorf("the probe sent %d packets", s.Packets)
	}
	off := false
	if r := check(Form{ReadAnswers: &off}, "good-key"); !r.Ok || len(fv.Syntheses()) != 1 {
		t.Errorf("reading off: %+v, %d syntheses", r, len(fv.Syntheses()))
	}

	fv.Set(func(s *volctest.Script) {
		s.RefuseASR, s.RefuseBody = 401, `{"code":45000010,"message":"invalid api key"}`
		s.TTSStatus, s.TTSBody = 401, `{"code":45000010,"message":"invalid api key"}`
	})
	r := check(Form{}, "wrong-key-123456")
	if r.Ok || r.Error != i18n.T(i18n.ZhHans, "volc-check.why_forbidden") || strings.Contains(r.Error, "wrong-key") {
		t.Errorf("refused key: %+v", r)
	}

	fv.Set(func(s *volctest.Script) {
		s.RefuseASR, s.TTSStatus, s.RefuseRequest, s.RefuseMessage = 0, 0, 45000030, "requested resource not granted"
	})
	r = check(Form{}, "k")
	if r.Ok || !strings.Contains(r.Error, i18n.T(i18n.ZhHans, "volc-check.model_asr2")) || !strings.Contains(r.Error, i18n.T(i18n.ZhHans, "volc-check.asr")) {
		t.Errorf("recognition not turned on: %+v", r)
	}
	fv.Set(func(s *volctest.Script) {
		s.RefuseRequest, s.TTSLineCode, s.TTSLineMessage = 0, 45000000, "speaker permission denied"
	})
	r = check(Form{Voice: Voices[2]}, "k")
	if r.Ok || r.Error != i18n.T(i18n.ZhHans, "volc-check.failed_with", i18n.Args{"service": i18n.T(i18n.ZhHans, "volc-check.tts"), "why": i18n.T(i18n.ZhHans, "volc-check.why_voice")}) {
		t.Errorf("voice not allowed: %+v", r)
	}
	fv.Set(func(s *volctest.Script) { s.TTSLineCode = 0 })

	// An all-silent probe the service calls empty audio still passes.
	fv.Set(func(s *volctest.Script) { s.EmptyAudio = true })
	if r := check(Form{}, "k"); !r.Ok {
		t.Errorf("empty audio: %+v", r)
	}

	// The run's own key, with both services known good: another voice tries synthesis only; the meetings switch
	// (or turning reading off) tries nothing.
	run := testPlugin(t, Form{}, "k")
	run.board.set(serviceASR, nil, 0, sourceTest)
	run.board.set(serviceTTS, nil, 0, sourceTest)
	sessions, syntheses := fv.Count(), len(fv.Syntheses())
	if r := run.checkSettings(context.Background(), Form{Voice: Voices[3]}, "k", run.client); !r.Ok || fv.Count() != sessions || len(fv.Syntheses()) != syntheses+1 {
		t.Errorf("another voice: %+v, %d sessions, %d syntheses", r, fv.Count()-sessions, len(fv.Syntheses())-syntheses)
	}
	if r := run.checkSettings(context.Background(), Form{Meetings: true, ReadAnswers: &off}, "k", run.client); !r.Ok || fv.Count() != sessions || len(fv.Syntheses()) != syntheses+1 {
		t.Errorf("the switches: %+v, %d sessions, %d syntheses", r, fv.Count()-sessions, len(fv.Syntheses())-syntheses)
	}
	if r := run.checkSettings(context.Background(), Form{}, "another-key", run.client); !r.Ok || fv.Count() != sessions+1 {
		t.Errorf("another key: %+v, %d sessions", r, fv.Count()-sessions)
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// The page: without a key, how to get one; with one, each service's last outcome and Test Connection, which tries the
// saved settings and says how it went. A check's outcome is kept for the settings it tried, for the run that has them.
func TestStatusPage(t *testing.T) {
	fast(t)
	fv := fake(t)
	dir := t.TempDir()
	render := func(pl *Plugin, action string) (*wire.View, string) {
		t.Helper()
		r := &ext.UIRequest{UIParams: wire.UIParams{Contribution: StatusPage, Action: action, Locale: "en"}}
		if action == "" {
			v, err := pl.renderStatus(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			return v, ""
		}
		res, err := pl.statusAction(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		return res.View, res.Toast
	}
	none := testPlugin(t, Form{}, "")
	v, _ := render(none, "")
	if items := v.Sections[0].Items; items[0].Text != i18n.T(i18n.En, "volc-check.need_key") || items[2].URL != consoleURL {
		t.Errorf("without a key: %+v", items)
	}

	// The check that saves a key keeps its outcome for the run with that key.
	old := testPlugin(t, Form{}, "")
	old.board.load(dir, fingerprint(old.cred, old.form))
	if r := old.checkSettings(context.Background(), Form{}, "new-key", old.client); !r.Ok {
		t.Fatalf("check %+v", r)
	}
	pl := testPlugin(t, Form{}, "new-key")
	pl.board.load(dir, fingerprint(pl.cred, pl.form))
	v, _ = render(pl, "")
	rows := v.Sections[0].Items
	if rows[0].Badge == nil || rows[0].Badge.Tone != "positive" || rows[1].Badge == nil || rows[2].Action == nil || rows[2].Action.ID != "test" {
		t.Errorf("after the check: %+v", rows)
	}
	for _, row := range rows[:2] {
		if row.Badge != nil && strings.Contains(row.Value, row.Badge.Text) {
			t.Errorf("%s says its state twice: %q and the badge %q", row.Key, row.Value, row.Badge.Text)
		}
	}
	other := testPlugin(t, Form{}, "another-key")
	other.board.load(dir, fingerprint(other.cred, other.form))
	if v, _ := render(other, ""); v.Sections[0].Items[0].Value != i18n.T(i18n.En, "volc-check.untested") {
		t.Errorf("another key shows the old outcome: %+v", v.Sections[0].Items[0])
	}

	// Test Connection.
	fv.Set(func(s *volctest.Script) { s.TTSStatus, s.TTSBody = 403, `{"message":"requested resource not granted"}` })
	v, toast := render(other, "test")
	if !strings.Contains(toast, i18n.T(i18n.En, "volc-check.model_tts2")) || v.Sections[0].Items[1].Badge.Tone != "danger" || v.Sections[0].Items[0].Badge.Tone != "positive" {
		t.Errorf("test: %q %+v", toast, v.Sections[0].Items)
	}
	if !bytes.Contains(mustRead(t, dir), []byte(fingerprint(other.cred, other.form))) {
		t.Error("the outcome was not kept")
	}
	before := fv.Count()
	render(other, "test") // within testInterval: not tried again
	if fv.Count() != before {
		t.Error("a second test within the interval")
	}
}

func mustRead(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, statusFile))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
