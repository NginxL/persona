package volc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Synthesis (docs/extensions/volcengine-speech.md "Synthesis"): the service's one-way HTTP endpoint, which takes a
// whole text and streams its audio back as lines of JSON, each with a piece of base64 audio, until code 20000000.

// ttsURL is the service's synthesis address; tests point it at a fake.
var ttsURL = "https://openspeech.bytedance.com/api/v3/tts/unidirectional"

// ttsRate is the sample rate asked for: mono PCM16 little-endian.
const ttsRate = 24000

// ttsChunkRunes is the longest piece of text sent in one request: the service documents no limit for this endpoint
// (its older one takes 1024 bytes), so a long text goes in pieces cut at sentence ends.
const ttsChunkRunes = 300

// ttsLineMost bounds one line of the answer (a piece of base64 audio).
const ttsLineMost = 4 << 20

// ttsFirstAudio bounds the wait for a request's answer (a variable: tests shorten it).
var ttsFirstAudio = 15 * time.Second

const ttsCodeDone = 20000000

type ttsRequest struct {
	User      ttsUser   `json:"user"`
	ReqParams ttsParams `json:"req_params"`
}

type ttsUser struct {
	UID string `json:"uid"`
}

type ttsParams struct {
	Text        string         `json:"text"`
	Speaker     string         `json:"speaker"`
	AudioParams ttsAudioParams `json:"audio_params"`
}

type ttsAudioParams struct {
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
}

// ttsResource is the synthesis model a voice is of: the 2.0 voices (named *_uranus_*) need seed-tts-2.0; the 1.0
// voices seed-tts-1.0, and so does any other voice id the owner typed.
func ttsResource(voice string) string {
	if strings.Contains(voice, "_uranus_") || strings.HasPrefix(voice, "saturn_") {
		return "seed-tts-2.0"
	}
	return "seed-tts-1.0"
}

// synthesize reads text aloud in voice and hands its audio on as it comes (24 kHz PCM16). A text longer than a request
// takes goes piece by piece. It returns once all of it was said or at the first failure; emit's error stops it.
func synthesize(ctx context.Context, client *http.Client, cred credentials, voice, text string, emit func([]byte) error) error {
	for _, piece := range ttsPieces(text) {
		if err := synthesizeOne(ctx, client, cred, voice, piece, emit); err != nil {
			return err
		}
	}
	return nil
}

func synthesizeOne(ctx context.Context, client *http.Client, cred credentials, voice, text string, emit func([]byte) error) error {
	body, _ := json.Marshal(ttsRequest{User: ttsUser{UID: "persona"},
		ReqParams: ttsParams{Text: text, Speaker: voice, AudioParams: ttsAudioParams{Format: "pcm", SampleRate: ttsRate}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ttsURL, bytes.NewReader(body))
	if err != nil {
		return &failure{service: "tts", kind: failFailed, err: err}
	}
	cred.ttsHeaders(req.Header)
	req.Header.Set("X-Api-Resource-Id", ttsResource(voice))
	req.Header.Set("X-Api-Request-Id", newID())
	req.Header.Set("Content-Type", "application/json")
	// Only the wait for the answer's first line is bounded here: the audio of a long piece keeps coming after it.
	answered := make(chan struct{})
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-time.After(ttsFirstAudio):
			select {
			case <-answered:
			default:
				cancel()
			}
		case <-rctx.Done():
		}
	}()
	res, err := client.Do(req.WithContext(rctx))
	if err != nil {
		return &failure{service: "tts", kind: failNetwork, err: err}
	}
	defer res.Body.Close()
	logid := res.Header.Get("X-Tt-Logid")
	if res.StatusCode != http.StatusOK {
		code, msg := answerOf(res.Body)
		return &failure{service: "tts", kind: classify(res.StatusCode, code, msg), status: res.StatusCode, code: code, msg: msg, logid: logid}
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), ttsLineMost)
	first := true
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var v struct {
			Code    int     `json:"code"`
			Message string  `json:"message"`
			Data    *string `json:"data"`
		}
		if err := json.Unmarshal(line, &v); err != nil {
			return &failure{service: "tts", kind: failFailed, err: err, logid: logid}
		}
		if first {
			first = false
			close(answered)
		}
		switch {
		case v.Code == ttsCodeDone:
			return nil
		case v.Code != 0:
			return &failure{service: "tts", kind: classify(0, v.Code, v.Message), code: v.Code, msg: v.Message, logid: logid}
		case v.Data != nil && *v.Data != "":
			pcm, err := base64.StdEncoding.DecodeString(*v.Data)
			if err != nil {
				return &failure{service: "tts", kind: failFailed, err: err, logid: logid}
			}
			if err := emit(pcm); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return &failure{service: "tts", kind: failNetwork, err: err, logid: logid}
	}
	return nil // the stream ended without its last line: what came was said
}

// ttsPieces cuts text into pieces of at most ttsChunkRunes, at the last sentence end (or else comma, else space)
// before the limit.
func ttsPieces(text string) []string {
	text = strings.TrimSpace(text)
	var out []string
	for utf8.RuneCountInString(text) > ttsChunkRunes {
		r := []rune(text)
		cut := -1
		for _, marks := range []string{"。！？!?.\n", "，,；;、", " "} {
			for i := ttsChunkRunes - 1; i > ttsChunkRunes/3; i-- {
				if strings.ContainsRune(marks, r[i]) {
					cut = i + 1
					break
				}
			}
			if cut > 0 {
				break
			}
		}
		if cut <= 0 {
			cut = ttsChunkRunes
		}
		if p := strings.TrimSpace(string(r[:cut])); p != "" {
			out = append(out, p)
		}
		text = strings.TrimSpace(string(r[cut:]))
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}
