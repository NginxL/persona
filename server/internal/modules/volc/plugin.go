package volc

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The built-in Volcengine speech extension, `personad volc` (docs/extensions/volcengine-speech.md): the owner's
// Volcengine (Doubao Speech) key, used for meeting transcription on the hub (a `transcription` contribution, which
// also serves extensions' own transcription) and for reading meeting answers aloud (a `speech` contribution). It
// stores nothing: the key comes from the hub, sealed there; the audio and text pass through.

// ExtensionID is the built-in extension's id, and the id of both its contributions.
const (
	ExtensionID               = "volc"
	TranscriptionContribution = "volc"
	SpeechContribution        = "volc"
)

// SecretKey is the secret holding the key: the new console's API Key, or the old console's Access Token when the
// advanced settings have an App ID.
const SecretKey = "key"

// The models and voices the form offers (extmgr's volcConfig), the defaults first.
var (
	// ASRModels are the recognition models by the service's resource id: 2.0 billed by the hour, 2.0 by concurrent
	// sessions, then 1.0 the same two ways.
	ASRModels = []string{"volc.seedasr.sauc.duration", "volc.seedasr.sauc.concurrent", "volc.bigasr.sauc.duration", "volc.bigasr.sauc.concurrent"}
	// Voices are synthesis 2.0 voices the service lists for everyone.
	Voices = []string{"zh_female_vv_uranus_bigtts", "zh_female_xiaohe_uranus_bigtts", "zh_male_m191_uranus_bigtts",
		"zh_male_taocheng_uranus_bigtts", "zh_female_qingxinnvsheng_uranus_bigtts", "zh_female_shuangkuaisisi_uranus_bigtts"}
)

// EnvService names a stand-in of the service (http://host:port) the program reaches in place of Volcengine's:
// volctest in the manager's tests, or a mock for a local run. The hub never sets it.
const EnvService = "PERSONA_VOLC_SERVICE"

// Form is the extension's settings as the hub saves them.
type Form struct {
	// Meetings: meetings transcribe with this extension. The manager keeps it as the assistant's choice says
	// (extmgr volc_extension.go); the program does not read it.
	Meetings bool `json:"meetings"`
	// ReadAnswers: meeting answers are read aloud with this extension (on unless turned off).
	ReadAnswers *bool    `json:"read_answers"`
	Voice       string   `json:"voice"`
	Advanced    Advanced `json:"advanced"`
}

// Advanced are the settings most owners never change.
type Advanced struct {
	ASRModel string `json:"asr_model"`
	VoiceID  string `json:"voice_id"` // any voice id, in place of Voice
	AppID    string `json:"app_id"`   // the old console's App ID: the key is then its Access Token
}

func (f Form) readAnswers() bool { return f.ReadAnswers == nil || *f.ReadAnswers }

func (f Form) voice() string {
	if v := strings.TrimSpace(f.Advanced.VoiceID); v != "" {
		return v
	}
	if slices.Contains(Voices, f.Voice) {
		return f.Voice
	}
	return Voices[0]
}

func (f Form) asrModel() string {
	if slices.Contains(ASRModels, f.Advanced.ASRModel) {
		return f.Advanced.ASRModel
	}
	return ASRModels[0]
}

func (f Form) cred(key string) credentials {
	return credentials{key: strings.TrimSpace(key), appID: strings.TrimSpace(f.Advanced.AppID)}
}

// ParseForm reads the settings (absent fields at their defaults).
func ParseForm(raw json.RawMessage) Form {
	var f Form
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &f)
	}
	return f
}

// Plugin is one run of the extension: its settings and key (fixed for the run: the hub restarts it on a change).
type Plugin struct {
	form   Form
	cred   credentials
	lang   i18n.Lang
	logger *log.Logger
	client *http.Client
	// active are the contributions on this run (initialize's active); nil: all of them.
	active map[string][]string
	board  board
	jobs   atomic.Int64
	speaks atomic.Int64
	tests  atomic.Int64
}

// New is a run with form and key, speaking lang to the owner.
func New(form Form, key string, lang i18n.Lang, logger *log.Logger) *Plugin {
	if logger == nil {
		logger = log.New(os.Stderr, "", 0)
	}
	return &Plugin{form: form, cred: form.cred(key), lang: lang.Or(i18n.Default), logger: logger, client: &http.Client{}}
}

func (pl *Plugin) logf(format string, args ...any) { pl.logger.Printf(format, args...) }

// on says whether contribution cid of point is on for this run.
func (pl *Plugin) on(point, cid string) bool {
	if pl.active == nil {
		return true
	}
	return slices.Contains(pl.active[point], cid)
}

// Handler serves the contributions' routes on the program's socket (GET /health is the SDK's).
func (pl *Plugin) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /transcription/{cid}/info", pl.serveTranscriptionInfo)
	mux.HandleFunc("GET /transcription/{cid}/ws", pl.serveTranscriptionWS)
	mux.HandleFunc("GET /speech/{cid}/info", pl.serveSpeechInfo)
	mux.HandleFunc("POST /speech/{cid}/speak", pl.serveSpeak)
	return mux
}

// fullCaller refuses a request from anyone but a full device or the manager itself (which names no role).
func fullCaller(w http.ResponseWriter, r *http.Request) bool {
	if role := r.Header.Get(extwire.HeaderRole); role != "" && role != "full" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "only a full device or the manager", "code": "forbidden"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// requestLang is the language a request's caller reads, else the hub's.
func (pl *Plugin) requestLang(r *http.Request) i18n.Lang {
	if l := i18n.Resolve(r.Header.Get(extwire.HeaderLang)); l != "" {
		return l
	}
	return pl.lang
}

// Run is `personad volc`: the program the hub starts, speaking the extension protocol on stdin / stdout and serving
// its contributions on the hub's socket. version is the build's: the hub started this same executable and registered
// the built-in under its version, which the program says back at initialize.
func Run(ctx context.Context, version string) error {
	if ext.Env().Socket == "" {
		return errors.New("PERSONA_EXTENSION_SOCKET is not set: this runs under the Persona hub as its built-in Volcengine speech extension (docs/extensions/volcengine-speech.md)")
	}
	logger := log.New(os.Stderr, "", 0)
	if base := strings.TrimRight(os.Getenv(EnvService), "/"); base != "" { // a stand-in of the service (tests, a mock hub)
		asrBase = "ws" + strings.TrimPrefix(base, "http") + "/api/v3/sauc/"
		ttsURL = base + "/api/v3/tts/unidirectional"
		logger.Printf("the service is %s", base)
	}
	pl := New(Form{}, "", i18n.Default, logger)
	ext.Main(ext.Program{
		ID: ExtensionID, Version: version,
		Init: func(ctx context.Context, init *ext.Init, hub *ext.Hub) (map[string]any, error) {
			pl.form = ParseForm(init.InitializeParams.Config)
			pl.cred = pl.form.cred(init.InitializeParams.Secrets[SecretKey])
			if l := i18n.Resolve(init.InitializeParams.Hub.Locale); l != "" {
				pl.lang = l
			}
			pl.active = init.InitializeParams.Active
			pl.board.load(ext.Env().Data, fingerprint(pl.cred, pl.form))
			switch {
			case !pl.cred.set():
				logger.Printf("no key yet: nothing transcribes or reads aloud until one is saved")
			default:
				logger.Printf("ready: recognition %s, voice %s, reading answers %v", pl.form.asrModel(), pl.form.voice(), pl.form.readAnswers())
			}
			return nil, nil
		},
		Namespaces: map[string]ext.Handler{"settings": pl.settings},
		UI:         map[string]ext.Page{StatusPage: {Render: pl.renderStatus, Action: pl.statusAction}},
		HTTP:       pl.Handler(),
	})
	return nil
}

// settings answers settings.check: the settings about to be saved are tried on the service (checkSettings).
func (pl *Plugin) settings(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method != "check" {
		return nil, ext.UnknownMethod("settings." + method)
	}
	var p wire.SettingsCheckParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &ext.Error{Code: "invalid_params", Message: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, checkMost)
	defer cancel()
	return pl.checkSettings(ctx, ParseForm(p.Config), p.Secrets[SecretKey], ext.SettingsCheckClient(p)), nil
}

// since is how long ago t was, rounded for a log line.
func since(t time.Time) time.Duration { return time.Since(t).Round(10 * time.Millisecond) }
