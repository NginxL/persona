package volc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/extwire"
)

// credentials are what the speech service is called with: the new console's API Key (X-Api-Key), or with appID the
// old console's App ID and Access Token (key is then the token). The two services name the old console's App ID with
// different headers (recognition X-Api-App-Key, synthesis X-Api-App-Id).
type credentials struct {
	key, appID string
}

func (c credentials) set() bool { return strings.TrimSpace(c.key) != "" }

func (c credentials) asrHeaders(h http.Header) {
	if c.appID != "" {
		h.Set("X-Api-App-Key", c.appID)
		h.Set("X-Api-Access-Key", c.key)
		return
	}
	h.Set("X-Api-Key", c.key)
}

func (c credentials) ttsHeaders(h http.Header) {
	if c.appID != "" {
		h.Set("X-Api-App-Id", c.appID)
		h.Set("X-Api-Access-Key", c.key)
		return
	}
	h.Set("X-Api-Key", c.key)
}

// The kinds of failure the owner can act on (docs/extensions/volcengine-speech.md "Connection test"): each has its own words in
// the settings check and on the extension's page, and its transcription code.
const (
	failForbidden  = "forbidden"   // the key was refused
	failNotEnabled = "not_enabled" // the key's project has not turned this model on
	failVoice      = "voice"       // the voice is not one this account may use (or not of the synthesis model)
	failBusy       = "busy"        // the quota is used up, or every concurrent session is taken
	failNetwork    = "network"     // the service could not be reached
	failFailed     = "failed"      // anything else
)

// failure is a call to the service that did not go: what kind, which service, and what it said (diagnostics only:
// the log, never a reply; it never carries the key, which the service does not echo).
type failure struct {
	kind    string
	service string // "asr" or "tts"
	status  int    // the HTTP status (handshake or request), 0 for none
	code    int    // the service's own code, 0 for none
	logid   string // X-Tt-Logid: what the service's support asks for
	msg     string
	err     error // a network error
}

func (f *failure) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", f.service, f.kind)
	if f.status != 0 {
		fmt.Fprintf(&b, ", status %d", f.status)
	}
	if f.code != 0 {
		fmt.Fprintf(&b, ", code %d", f.code)
	}
	if f.msg != "" {
		fmt.Fprintf(&b, ": %s", cut(f.msg, 200))
	}
	if f.err != nil {
		fmt.Fprintf(&b, ": %v", f.err)
	}
	if f.logid != "" {
		fmt.Fprintf(&b, " (logid %s)", f.logid)
	}
	return b.String()
}

func (f *failure) Unwrap() error { return f.err }

// kindOf is err's kind, failFailed for an error that is not a failure.
func kindOf(err error) string {
	var f *failure
	if errors.As(err, &f) {
		return f.kind
	}
	return failFailed
}

// transcriptionCode is the transcription point's code for a failure (extwire): a refused key, a model not turned on
// or a voice not allowed is upstream_forbidden; a busy service (quota or concurrency used up) upstream_busy; no
// network upstream_unavailable; the rest upstream_failed. The manager passes the kind on to the phone (#13446).
func transcriptionCode(err error) string {
	switch kindOf(err) {
	case failForbidden, failNotEnabled, failVoice:
		return extwire.TEUpstreamForbidden
	case failBusy:
		return extwire.TEUpstreamBusy
	case failNetwork:
		return extwire.TEUpstreamUnavailable
	}
	return extwire.TEUpstreamFailed
}

// classify reads what the service answered (an HTTP status, its code, its words) as a kind. The service documents
// few of its refusals, so the words are read too: "not granted" and the like mean a model the project has not turned
// on; quota and concurrency, busy.
func classify(status, code int, msg string) string {
	m := strings.ToLower(msg)
	switch {
	case code == 45000030, strings.Contains(m, "not granted"), strings.Contains(m, "not open"), strings.Contains(m, "未开通"),
		strings.Contains(m, "not activated"), strings.Contains(m, "resource id"):
		return failNotEnabled
	case strings.Contains(m, "speaker"), strings.Contains(m, "voice_type"), strings.Contains(m, "voice type"),
		code == 45000000 && strings.Contains(m, "voice"):
		return failVoice
	case status == http.StatusTooManyRequests, code == 55000031, strings.Contains(m, "quota"), strings.Contains(m, "concurren"),
		strings.Contains(m, "exceeded"), strings.Contains(m, "insufficient"), strings.Contains(m, "balance"):
		return failBusy
	case status == http.StatusUnauthorized, status == http.StatusForbidden, strings.Contains(m, "unauthorized"),
		strings.Contains(m, "invalid api key"), strings.Contains(m, "invalid key"), strings.Contains(m, "authenticate"),
		strings.Contains(m, "access denied"), strings.Contains(m, "auth"):
		return failForbidden
	case status == http.StatusServiceUnavailable, status == http.StatusBadGateway, status == http.StatusGatewayTimeout:
		return failBusy
	}
	return failFailed
}

// answerOf reads what a refusal's body says: its code and message, from the shapes the service answers in
// ({"code", "message"}, {"header": {"code", "message"}}, {"error": "…"}), else the text as it is.
func answerOf(body io.Reader) (code int, msg string) {
	if body == nil {
		return 0, ""
	}
	raw, _ := io.ReadAll(io.LimitReader(body, 2048))
	var v struct {
		Code    json.Number `json:"code"`
		Message string      `json:"message"`
		Error   any         `json:"error"`
		Header  struct {
			Code    json.Number `json:"code"`
			Message string      `json:"message"`
		} `json:"header"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return 0, strings.TrimSpace(string(raw))
	}
	n, _ := v.Code.Int64()
	if n == 0 {
		n, _ = v.Header.Code.Int64()
	}
	msg = v.Message
	if msg == "" {
		msg = v.Header.Message
	}
	if msg == "" && v.Error != nil {
		msg = fmt.Sprint(v.Error)
	}
	return int(n), msg
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
