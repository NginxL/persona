package volc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The connection test (docs/extensions/volcengine-speech.md "Connection test"): one tiny recognition (0.6 s of
// silence) and, while answers are read aloud, one tiny synthesis (two characters), on the settings about to be saved
// (settings.check) or on the saved ones (the page's Test Connection). The last outcome of each service, by a test or
// by real use, is on the extension's page; it is kept in the extension's data directory for the settings it was for,
// so the result of the check that saved a key is there once the extension restarts with it.

const (
	serviceASR = "asr"
	serviceTTS = "tts"
	sourceTest = "test"
	sourceUse  = "use"

	// StatusPage is the extension_page contribution's id.
	StatusPage = "status"

	statusFile = "status.json"
)

// Bounds of a test (variables: tests shorten them).
var (
	checkMost    = 9 * time.Second
	probeAudio   = 3 // packets of silence
	testInterval = 10 * time.Second
)

// probeText is what the synthesis test says (two characters are billed).
const probeText = "你好" // i18n-ignore: data sent to the service, not shown

// serviceState is how a service last went.
type serviceState struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Kind    string    `json:"kind,omitempty"`
	Latency int64     `json:"latency_ms,omitempty"`
	Source  string    `json:"source"`
}

// board keeps each service's last outcome, for one set of settings (print).
type board struct {
	mu    sync.Mutex
	path  string
	print string
	asr   *serviceState
	tts   *serviceState
	last  time.Time // the last test the page ran
}

// fingerprint names the settings a result is for, without the key itself.
func fingerprint(cred credentials, f Form) string {
	h := sha256.Sum256([]byte(strings.Join([]string{cred.key, cred.appID, f.asrModel(), f.voice()}, "\x00")))
	return hex.EncodeToString(h[:8])
}

// load reads the outcomes kept for print (none for other settings).
func (b *board) load(dir, print string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.print = print
	if dir == "" {
		return
	}
	b.path = filepath.Join(dir, statusFile)
	raw, err := os.ReadFile(b.path)
	if err != nil {
		return
	}
	var kept struct {
		Print string        `json:"print"`
		ASR   *serviceState `json:"asr"`
		TTS   *serviceState `json:"tts"`
	}
	if json.Unmarshal(raw, &kept) == nil && kept.Print == print {
		b.asr, b.tts = kept.ASR, kept.TTS
	}
}

// set records how service went (err nil: well), for the settings in effect.
func (b *board) set(service string, err error, latency time.Duration, source string) {
	b.record(b.currentPrint(), service, err, latency, source)
}

func (b *board) currentPrint() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.print
}

// record keeps an outcome for print: the settings in effect, or the ones a settings check tried (kept for when the
// extension restarts with them).
func (b *board) record(print, service string, err error, latency time.Duration, source string) {
	st := &serviceState{At: time.Now().UTC(), OK: err == nil, Latency: latency.Milliseconds(), Source: source}
	if err != nil {
		st.Kind = kindOf(err)
	}
	b.put(print, service, st)
}

// put keeps st as service's outcome for print.
func (b *board) put(print, service string, st *serviceState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if print != b.print { // another set of settings: a check's, kept for the run that will have them
		b.save(print, map[string]*serviceState{service: st})
		return
	}
	if service == serviceASR {
		b.asr = st
	} else {
		b.tts = st
	}
	b.save(print, map[string]*serviceState{serviceASR: b.asr, serviceTTS: b.tts})
}

func (b *board) save(print string, states map[string]*serviceState) {
	if b.path == "" {
		return
	}
	var kept struct {
		Print string        `json:"print"`
		ASR   *serviceState `json:"asr,omitempty"`
		TTS   *serviceState `json:"tts,omitempty"`
	}
	if raw, err := os.ReadFile(b.path); err == nil {
		_ = json.Unmarshal(raw, &kept)
	}
	if kept.Print != print {
		kept.ASR, kept.TTS = nil, nil
	}
	kept.Print = print
	if st, ok := states[serviceASR]; ok {
		kept.ASR = st
	}
	if st, ok := states[serviceTTS]; ok {
		kept.TTS = st
	}
	raw, _ := json.Marshal(kept)
	tmp := b.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, b.path)
	}
}

func (b *board) get() (asr, tts *serviceState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.asr, b.tts
}

// probeASR opens a session, sends 0.6 s of silence and waits for the final result: the key, the model and the quota
// are all tried. A service that calls the silence empty audio counts as having answered.
func probeASR(ctx context.Context, cred credentials, model string) (time.Duration, error) {
	began := time.Now()
	s, err := dialASR(ctx, cred, model, endpointLive, nil)
	if err != nil {
		return 0, err
	}
	defer s.close()
	lat := time.Since(began)
	silence := make([]byte, packetBytes)
	for range probeAudio {
		if err := s.send(ctx, silence); err != nil {
			return lat, err
		}
	}
	if err := s.finish(ctx); err != nil {
		return lat, err
	}
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return lat, nil
			}
			var f *failure
			if ev.err != nil && errors.As(ev.err, &f) && f.code == 45000002 { // empty audio: it heard the silence as nothing
				return lat, nil
			}
			if ev.err != nil {
				return lat, ev.err
			}
			if ev.last {
				return lat, nil
			}
		case <-ctx.Done():
			return lat, &failure{service: "asr", kind: failNetwork, err: ctx.Err()}
		}
	}
}

// probeTTS has the voice say two characters; the time is to the first audio.
func probeTTS(ctx context.Context, client *http.Client, cred credentials, voice string) (time.Duration, error) {
	began := time.Now()
	var first time.Duration
	err := synthesize(ctx, client, cred, voice, probeText, func(pcm []byte) error {
		if first == 0 {
			first = time.Since(began)
		}
		return nil
	})
	if err == nil && first == 0 {
		err = &failure{service: "tts", kind: failFailed, msg: "no audio"}
	}
	return first, err
}

type probeResult struct {
	asrErr, ttsErr error
	asrLat, ttsLat time.Duration
	tts            bool // synthesis was tried
}

// probe tries recognition and, with tts, synthesis, side by side.
func probe(ctx context.Context, client *http.Client, cred credentials, f Form, tts bool) probeResult {
	r := probeResult{tts: tts}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.asrLat, r.asrErr = probeASR(ctx, cred, f.asrModel())
	}()
	if tts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.ttsLat, r.ttsErr = probeTTS(ctx, client, cred, f.voice())
		}()
	}
	wg.Wait()
	return r
}

// checkSettings is settings.check: the settings about to be saved, tried. No key yet: nothing to try. Recognition
// must work; synthesis too while answers are read aloud. A refusal says what to do next, in the owner's words.
func (pl *Plugin) checkSettings(ctx context.Context, f Form, key string, client *http.Client) wire.SettingsCheckResult {
	cred := f.cred(key)
	if !cred.set() {
		return wire.SettingsCheckResult{Ok: true}
	}
	// What the check would try and the run in effect already did well is not tried again (a voice changed: only
	// synthesis; the switch for reading answers turned off: nothing).
	asr, tts := pl.board.get()
	same := cred == pl.cred
	needASR := !(same && f.asrModel() == pl.form.asrModel() && asr != nil && asr.OK)
	needTTS := f.readAnswers() && !(same && f.voice() == pl.form.voice() && tts != nil && tts.OK)
	print := fingerprint(cred, f)
	if !needASR && !needTTS {
		pl.board.put(print, serviceASR, asr)
		pl.board.put(print, serviceTTS, tts)
		return wire.SettingsCheckResult{Ok: true}
	}
	var r probeResult
	if needASR {
		r = probe(ctx, client, cred, f, needTTS)
	} else {
		r.tts = true
		r.ttsLat, r.ttsErr = probeTTS(ctx, client, cred, f.voice())
	}
	if needASR {
		pl.board.record(print, serviceASR, r.asrErr, r.asrLat, sourceTest)
	} else {
		pl.board.put(print, serviceASR, asr)
	}
	if r.tts {
		pl.board.record(print, serviceTTS, r.ttsErr, r.ttsLat, sourceTest)
	} else if f.readAnswers() {
		pl.board.put(print, serviceTTS, tts)
	}
	pl.logf("settings check: recognition %s, synthesis %s", map[bool]string{true: outcomeOf(r.asrErr, r.asrLat), false: "not tried"}[needASR],
		map[bool]string{true: outcomeOf(r.ttsErr, r.ttsLat), false: "not tried"}[r.tts])
	if why := pl.failureText(pl.lang, f, r); why != "" {
		return wire.SettingsCheckResult{Ok: false, Error: why}
	}
	return wire.SettingsCheckResult{Ok: true}
}

func outcomeOf(err error, lat time.Duration) string {
	if err != nil {
		return "failed (" + err.Error() + ")"
	}
	return "ok in " + lat.Round(time.Millisecond).String()
}

// failureText is what a probe's failures mean to the owner, with the next step: one sentence when the key itself was
// refused, else one per service that failed.
func (pl *Plugin) failureText(l i18n.Lang, f Form, r probeResult) string {
	if r.asrErr != nil && kindOf(r.asrErr) == failForbidden && (!r.tts || r.ttsErr != nil && kindOf(r.ttsErr) == failForbidden) {
		return i18n.T(l, "volc-check.why_forbidden")
	}
	var parts []string
	if r.asrErr != nil {
		parts = append(parts, i18n.T(l, "volc-check.failed_with", i18n.Args{"service": i18n.T(l, "volc-check.asr"), "why": whyText(l, serviceASR, f, r.asrErr)}))
	}
	if r.tts && r.ttsErr != nil {
		parts = append(parts, i18n.T(l, "volc-check.failed_with", i18n.Args{"service": i18n.T(l, "volc-check.tts"), "why": whyText(l, serviceTTS, f, r.ttsErr)}))
	}
	return strings.Join(parts, " ")
}

// whyText is one service's failure with the next step.
func whyText(l i18n.Lang, service string, f Form, err error) string {
	switch kindOf(err) {
	case failForbidden:
		return i18n.T(l, "volc-check.why_forbidden")
	case failNotEnabled:
		if service == serviceTTS {
			return i18n.T(l, "volc-check.why_tts_not_enabled", i18n.Args{"model": modelName(l, service, f)})
		}
		return i18n.T(l, "volc-check.why_not_enabled", i18n.Args{"model": modelName(l, service, f)})
	case failVoice:
		return i18n.T(l, "volc-check.why_voice")
	case failBusy:
		return i18n.T(l, "volc-check.why_busy")
	case failNetwork:
		return i18n.T(l, "volc-check.why_network")
	}
	return i18n.T(l, "volc-check.why_failed")
}

// modelName is the model as the service's console names it, to turn it on there.
func modelName(l i18n.Lang, service string, f Form) string {
	if service == serviceTTS {
		if ttsResource(f.voice()) == "seed-tts-2.0" {
			return i18n.T(l, "volc-check.model_tts2")
		}
		return i18n.T(l, "volc-check.model_tts1")
	}
	if strings.HasPrefix(f.asrModel(), "volc.seedasr.") {
		return i18n.T(l, "volc-check.model_asr2")
	}
	return i18n.T(l, "volc-check.model_asr1")
}

// consoleURL is the service's console (its new one), where models are turned on and keys copied.
const consoleURL = "https://console.volcengine.com/speech/new"

// renderStatus draws the extension's page: each service's last outcome, Test Connection, and the way to a key.
func (pl *Plugin) renderStatus(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
	return pl.statusView(pl.uiLang(r)), nil
}

func (pl *Plugin) uiLang(r *ext.UIRequest) i18n.Lang {
	if l := i18n.Resolve(r.Locale); l != "" {
		return l
	}
	return pl.lang
}

func (pl *Plugin) statusView(l i18n.Lang) *wire.View {
	t := func(key string, args ...i18n.Args) string { return i18n.T(l, "volc-check."+key, args...) }
	v := &wire.View{}
	if !pl.cred.set() {
		v.Sections = []wire.Section{{Items: []wire.Component{
			{Type: "text", Key: "need_key", Text: t("need_key")},
			{Type: "text", Key: "steps", Text: t("steps"), Style: "secondary"},
			{Type: "link", Key: "console", Title: t("console"), URL: consoleURL},
		}}}
		return v
	}
	asr, tts := pl.board.get()
	rows := []wire.Component{pl.stateRow(l, "asr", serviceASR, asr, true)}
	rows = append(rows, pl.stateRow(l, "tts", serviceTTS, tts, pl.form.readAnswers()))
	rows = append(rows, wire.Component{Type: "button", Key: "test", Title: t("test"), Action: &wire.Action{ID: "test"}})
	v.Sections = []wire.Section{
		{Key: "connection", Title: t("section_connection"), Footer: t("footer"), Items: rows},
		{Key: "help", Items: []wire.Component{{Type: "link", Key: "console", Title: t("console"), URL: consoleURL}}},
	}
	return v
}

func (pl *Plugin) stateRow(l i18n.Lang, key, service string, st *serviceState, on bool) wire.Component {
	t := func(k string, args ...i18n.Args) string { return i18n.T(l, "volc-check."+k, args...) }
	row := wire.Component{Type: "row", Key: key, Title: t(service)}
	switch {
	case !on:
		row.Value = t("off")
	case st == nil:
		row.Value = t("untested")
	case st.OK:
		// The badge says it worked; the value only adds how long the check took.
		if st.Latency > 0 {
			row.Value = t("latency", i18n.Args{"ms": st.Latency})
		}
		row.Badge = &wire.Badge{Text: t("ok"), Tone: "positive"}
	default:
		row.Subtitle = whyText(l, service, pl.form, &failure{kind: st.Kind})
		row.Badge = &wire.Badge{Text: t("failed"), Tone: "danger"}
	}
	return row
}

// statusAction is the page's Test Connection: the saved settings, tried (at most once in testInterval).
func (pl *Plugin) statusAction(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
	l := pl.uiLang(r)
	if r.Action != "test" || !pl.cred.set() {
		return &wire.ActionResult{View: pl.statusView(l)}, nil
	}
	pl.board.mu.Lock()
	recent := time.Since(pl.board.last) < testInterval
	if !recent {
		pl.board.last = time.Now()
	}
	pl.board.mu.Unlock()
	if recent {
		return &wire.ActionResult{View: pl.statusView(l)}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, checkMost)
	defer cancel()
	res := probe(ctx, pl.client, pl.cred, pl.form, pl.form.readAnswers())
	n := pl.tests.Add(1)
	pl.board.set(serviceASR, res.asrErr, res.asrLat, sourceTest)
	if res.tts {
		pl.board.set(serviceTTS, res.ttsErr, res.ttsLat, sourceTest)
	}
	pl.logf("test %d: recognition %s, synthesis %s", n, outcomeOf(res.asrErr, res.asrLat), map[bool]string{true: outcomeOf(res.ttsErr, res.ttsLat), false: "not tried"}[res.tts])
	toast := i18n.T(l, "volc-check.toast_ok")
	switch why := pl.failureText(l, pl.form, res); {
	case why != "":
		toast = i18n.T(l, "volc-check.toast_failed", i18n.Args{"why": why})
	case !res.tts:
		toast = i18n.T(l, "volc-check.toast_asr_ok")
	}
	return &wire.ActionResult{View: pl.statusView(l), Toast: toast}, nil
}
