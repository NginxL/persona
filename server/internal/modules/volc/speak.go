package volc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Reading a text aloud (the `speech` point, docs/extensions/extensions.md "Speech"): POST /speech/{cid}/speak takes the
// voice extension's read-aloud request ({text, voice?, lang?}, docs/assistant/voice.md "Reading a text aloud") and
// answers the same way: nothing before the first audio, then 200 audio/pcm with X-Persona-Audio-Rate, the audio as it
// comes. The iPhone says a meeting answer this way, and falls back to its own voice on any error status.

const (
	speakMostRunes  = 1000
	headerAudioRate = "X-Persona-Audio-Rate"
	setupOff        = "off" // /speech/{cid}/info: reading answers is turned off in the settings
)

// speakMost bounds one read-aloud (a variable: tests shorten it).
var speakMost = 150 * time.Second

// SpeechInfo is GET /speech/{cid}/info.
type SpeechInfo struct {
	Protocol int    `json:"protocol"`
	Rate     int    `json:"rate"`
	Setup    string `json:"setup,omitempty"`
}

// SpeechProtocol is the speech point's version.
const SpeechProtocol = 1

func (pl *Plugin) speechCaller(w http.ResponseWriter, r *http.Request) bool {
	if cid := r.PathValue("cid"); cid != SpeechContribution || !pl.on("speech", cid) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such speech contribution", "code": "not_found"})
		return false
	}
	return fullCaller(w, r)
}

// serveSpeechInfo is GET /speech/{cid}/info: {protocol, rate, setup?}: setup "key" before a key is saved, "off" while
// reading answers is turned off.
func (pl *Plugin) serveSpeechInfo(w http.ResponseWriter, r *http.Request) {
	if !pl.speechCaller(w, r) {
		return
	}
	info := SpeechInfo{Protocol: SpeechProtocol, Rate: ttsRate}
	switch {
	case !pl.cred.set():
		info.Setup = setupKey
	case !pl.form.readAnswers():
		info.Setup = setupOff
	}
	writeJSON(w, http.StatusOK, info)
}

func speakError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": msg, "code": code})
}

// serveSpeak is POST /speech/{cid}/speak.
func (pl *Plugin) serveSpeak(w http.ResponseWriter, r *http.Request) {
	if !pl.speechCaller(w, r) {
		return
	}
	var req struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
		Lang  string `json:"lang"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		speakError(w, http.StatusBadRequest, "bad_request", "the body is JSON: {text, voice?, lang?}")
		return
	}
	text := strings.TrimSpace(req.Text)
	n := utf8.RuneCountInString(text)
	switch {
	case n == 0:
		speakError(w, http.StatusBadRequest, "bad_request", "no text to read")
		return
	case n > speakMostRunes:
		speakError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("the text is longer than %d characters", speakMostRunes))
		return
	case !pl.cred.set():
		speakError(w, http.StatusServiceUnavailable, "no_upstream", "no Volcengine key is saved")
		return
	case !pl.form.readAnswers():
		speakError(w, http.StatusServiceUnavailable, "off", "reading answers aloud is turned off in the settings")
		return
	}
	voice := pl.form.voice()
	if v := strings.TrimSpace(req.Voice); v != "" && slices.Contains(Voices, v) {
		voice = v // only one of the voices offered: a device's pick of another extension's voice is not ours
	}
	id := pl.speaks.Add(1)
	began := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), speakMost)
	defer cancel()
	bytes, first := 0, time.Duration(0)
	err := synthesize(ctx, pl.client, pl.cred, voice, text, func(pcm []byte) error {
		if bytes == 0 {
			first = time.Since(began)
			h := w.Header()
			h.Set("Content-Type", "audio/pcm")
			h.Set(headerAudioRate, strconv.Itoa(ttsRate))
			h.Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
		}
		if _, err := w.Write(pcm); err != nil {
			return errHungUp
		}
		_ = http.NewResponseController(w).Flush()
		bytes += len(pcm)
		return nil
	})
	outcome := "done"
	switch {
	case errors.Is(err, errHungUp), err != nil && r.Context().Err() != nil:
		outcome = "the app hung up"
	case err != nil && bytes > 0:
		outcome = "cut off: " + kindOf(err)
		pl.board.set(serviceTTS, err, 0, sourceUse)
	case err != nil:
		status, code := speakStatus(err)
		outcome = fmt.Sprintf("failed, %d %s", status, code)
		pl.board.set(serviceTTS, err, 0, sourceUse)
		speakError(w, status, code, "Volcengine did not read it: "+kindOf(err))
	case bytes == 0:
		outcome = "done (quiet)"
		speakError(w, http.StatusGatewayTimeout, "no_audio", "Volcengine sent no audio")
	default:
		pl.board.set(serviceTTS, nil, first, sourceUse)
	}
	pl.logf("speak %d: voice %s, %d characters: %s; %d bytes at %d Hz in %v (first audio %v)%s", id, voice, n, outcome, bytes,
		ttsRate, since(began), first.Round(time.Millisecond), errNote(err))
}

var errHungUp = errors.New("the app hung up")

// speakStatus is the read-aloud's error status for a failure: the voice extension's codes (docs/assistant/voice.md
// "Reading a text aloud").
func speakStatus(err error) (int, string) {
	switch kindOf(err) {
	case failForbidden, failNotEnabled, failVoice:
		return http.StatusBadGateway, "upstream_forbidden"
	case failBusy, failNetwork:
		return http.StatusServiceUnavailable, "upstream_unavailable"
	}
	return http.StatusBadGateway, "upstream_failed"
}

func errNote(err error) string {
	if err == nil || errors.Is(err, errHungUp) {
		return ""
	}
	return ": " + err.Error()
}
