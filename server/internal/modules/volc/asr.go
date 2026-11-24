package volc

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// Streaming recognition (docs/extensions/volcengine-speech.md "Recognition"): one WebSocket session of the service per
// stretch of audio. Live jobs use the optimized two-way endpoint with the second pass on (bigmodel_async with
// enable_nonstream: interim text as it is heard, and each sentence recognized again by the non-streaming model once it
// ends, marked definite); the final pass uses the streaming-input endpoint (bigmodel_nostream, more accurate, results
// once the audio ends).

// asrBase is the service's recognition address; tests point it at a fake.
var asrBase = "wss://openspeech.bytedance.com/api/v3/sauc/"

const (
	endpointLive   = "bigmodel_async"
	endpointReplay = "bigmodel_nostream"
)

// Audio the service takes: PCM16 little-endian, mono, 16 kHz, in packets of 200 ms (it asks for 100 to 200 ms).
const (
	sampleRate   = 16000
	bytesPerSec  = sampleRate * 2
	packetBytes  = bytesPerSec / 5
	lastPacketMS = 100 // the silence the last packet carries (a last packet with no audio may be refused as empty)
)

// asrAnswerWait bounds the wait for the service's answer to the request, and asrFinalWait for its final result once
// the last packet went (variables: tests shorten them).
var (
	asrAnswerWait = 10 * time.Second
	asrFinalWait  = 15 * time.Second
)

// hotwordBudget is what the two-way endpoints take as hotwords (100 tokens); the streaming-input one takes 5000 words.
const (
	hotwordBudgetLive  = 100
	hotwordTermsReplay = 5000
	hotwordLongestLive = 20 // a longer term is a phrase (a title), no help as a hotword
	titleRunes         = 8  // a glossary's first terms longer than this are its titles: they go last
)

type asrRequest struct {
	User    asrUser   `json:"user"`
	Audio   asrAudio  `json:"audio"`
	Request asrParams `json:"request"`
}

type asrUser struct {
	UID string `json:"uid"`
}

type asrAudio struct {
	Format  string `json:"format"`
	Codec   string `json:"codec"`
	Rate    int    `json:"rate"`
	Bits    int    `json:"bits"`
	Channel int    `json:"channel"`
}

type asrParams struct {
	ModelName       string     `json:"model_name"`
	EnableITN       bool       `json:"enable_itn"`
	EnablePunc      bool       `json:"enable_punc"`
	ShowUtterances  bool       `json:"show_utterances"`
	ResultType      string     `json:"result_type,omitempty"`
	EnableNonstream bool       `json:"enable_nonstream,omitempty"`
	EndWindowSize   int        `json:"end_window_size,omitempty"`
	Corpus          *asrCorpus `json:"corpus,omitempty"`
}

type asrCorpus struct {
	Context string `json:"context,omitempty"`
}

// asrResult is one result of a session: the audio it has heard (ms) and its utterances, in session milliseconds.
type asrResult struct {
	AudioInfo struct {
		Duration int `json:"duration"`
	} `json:"audio_info"`
	Result struct {
		Text       string      `json:"text"`
		Utterances []utterance `json:"utterances"`
	} `json:"result"`
}

type utterance struct {
	Text     string `json:"text"`
	Start    int    `json:"start_time"`
	End      int    `json:"end_time"`
	Definite bool   `json:"definite"`
}

// asrEvent is what a session's reader hands on: a result, or the error that ended the session; last marks the
// service's final result.
type asrEvent struct {
	res  *asrResult
	err  error
	last bool
}

// asrSession is one recognition session. send and finish are the writer's (one goroutine); events is read by one
// reader and closed once the session is over.
type asrSession struct {
	c      *websocket.Conn
	logid  string
	seq    int32
	events chan asrEvent
	quit   chan struct{} // closed by close: the reader stops handing on

	mu       sync.Mutex
	finished bool
	closed   bool
}

// asrDial is how a session's socket is opened (tests may replace it).
var asrDial = func(ctx context.Context, url string, h http.Header) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
}

// dialASR opens a session on endpoint with resource, sends the request and waits for the service's answer to it: a
// refused key, a model not turned on or a bad request comes back here as a failure.
func dialASR(ctx context.Context, cred credentials, resource, endpoint string, hotwords []string) (*asrSession, error) {
	h := http.Header{}
	cred.asrHeaders(h)
	h.Set("X-Api-Resource-Id", resource)
	id := newID()
	h.Set("X-Api-Request-Id", id)
	h.Set("X-Api-Connect-Id", id)
	dctx, cancel := context.WithTimeout(ctx, asrAnswerWait)
	defer cancel()
	c, res, err := asrDial(dctx, asrBase+endpoint, h)
	if err != nil {
		f := &failure{service: "asr", kind: failNetwork, err: err}
		if res != nil {
			f.status = res.StatusCode
			f.logid = res.Header.Get("X-Tt-Logid")
			f.code, f.msg = answerOf(res.Body)
			f.kind, f.err = classify(f.status, f.code, f.msg), nil
		}
		return nil, f
	}
	c.SetReadLimit(frameMost + 64)
	s := &asrSession{c: c, events: make(chan asrEvent, 64), quit: make(chan struct{})}
	if res != nil {
		s.logid = res.Header.Get("X-Tt-Logid")
	}
	req := asrRequest{User: asrUser{UID: "persona"}, Audio: asrAudio{Format: "pcm", Codec: "raw", Rate: sampleRate, Bits: 16, Channel: 1},
		Request: asrParams{ModelName: "bigmodel", EnableITN: true, EnablePunc: true, ShowUtterances: true}}
	if endpoint == endpointLive {
		req.Request.ResultType = "single"
		req.Request.EnableNonstream = true // the second pass: each sentence again by the non-streaming model, marked definite
	}
	if len(hotwords) > 0 {
		req.Request.Corpus = &asrCorpus{Context: hotwordContext(hotwords)}
	}
	body, _ := json.Marshal(req)
	s.seq = 1
	if err := c.Write(dctx, websocket.MessageBinary, clientFrame(msgFullClient, s.seq, body)); err != nil {
		c.CloseNow()
		return nil, &failure{service: "asr", kind: failNetwork, err: err, logid: s.logid}
	}
	// The service answers the request before any audio: a refusal comes as an error frame.
	f, err := s.read(dctx)
	if err != nil {
		c.CloseNow()
		return nil, err
	}
	if f.msgType == msgError {
		c.CloseNow()
		return nil, s.refusal(f)
	}
	go s.reader(context.WithoutCancel(ctx))
	return s, nil
}

func (s *asrSession) read(ctx context.Context) (serverFrame, error) {
	_, b, err := s.c.Read(ctx)
	if err != nil {
		return serverFrame{}, &failure{service: "asr", kind: failNetwork, err: err, logid: s.logid}
	}
	f, err := parseServerFrame(b)
	if err != nil {
		return f, &failure{service: "asr", kind: failFailed, err: err, logid: s.logid}
	}
	return f, nil
}

func (s *asrSession) refusal(f serverFrame) error {
	msg := string(f.payload)
	return &failure{service: "asr", kind: classify(0, int(f.code), msg), code: int(f.code), msg: msg, logid: s.logid}
}

// reader hands each result on until the final one, an error frame or the socket's end.
func (s *asrSession) reader(ctx context.Context) {
	defer close(s.events)
	for {
		f, err := s.read(ctx)
		if err != nil {
			s.mu.Lock()
			finished := s.finished
			s.mu.Unlock()
			if !finished || !isClosed(err) {
				s.emit(asrEvent{err: err})
			}
			return
		}
		switch f.msgType {
		case msgError:
			s.emit(asrEvent{err: s.refusal(f)})
			s.c.CloseNow()
			return
		case msgFullServer:
			var r asrResult
			if len(f.payload) > 0 {
				if err := json.Unmarshal(f.payload, &r); err != nil {
					s.emit(asrEvent{err: &failure{service: "asr", kind: failFailed, err: err, logid: s.logid}})
					s.c.CloseNow()
					return
				}
			}
			if !s.emit(asrEvent{res: &r, last: f.last}) {
				return
			}
			if f.last {
				s.c.Close(websocket.StatusNormalClosure, "")
				return
			}
		}
	}
}

// emit hands ev on, unless the session was closed (nobody reads any more).
func (s *asrSession) emit(ev asrEvent) bool {
	select {
	case s.events <- ev:
		return true
	case <-s.quit:
		return false
	}
}

func isClosed(err error) bool {
	var f *failure
	if errors.As(err, &f) && f.err != nil {
		err = f.err
	}
	return websocket.CloseStatus(err) != -1 || errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "closed")
}

// send sends one packet of audio.
func (s *asrSession) send(ctx context.Context, pcm []byte) error {
	s.seq++
	if err := s.c.Write(ctx, websocket.MessageBinary, clientFrame(msgAudioOnly, s.seq, pcm)); err != nil {
		return &failure{service: "asr", kind: failNetwork, err: err, logid: s.logid}
	}
	return nil
}

// finish sends the last packet (a moment of silence): the service then sends its final result and ends the session.
func (s *asrSession) finish(ctx context.Context) error {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return nil
	}
	s.finished = true
	s.mu.Unlock()
	s.seq++
	silence := make([]byte, bytesPerSec*lastPacketMS/1000)
	if err := s.c.Write(ctx, websocket.MessageBinary, clientFrame(msgAudioOnly, -s.seq, silence)); err != nil {
		return &failure{service: "asr", kind: failNetwork, err: err, logid: s.logid}
	}
	return nil
}

// close ends the session at once (its results not waited for).
func (s *asrSession) close() {
	s.mu.Lock()
	s.finished = true
	if !s.closed {
		s.closed = true
		close(s.quit)
	}
	s.mu.Unlock()
	s.c.CloseNow()
}

// hotwordContext is the request's corpus.context: the terms as the service's direct hotwords.
func hotwordContext(terms []string) string {
	type word struct {
		Word string `json:"word"`
	}
	v := struct {
		Hotwords []word `json:"hotwords"`
	}{}
	for _, t := range terms {
		v.Hotwords = append(v.Hotwords, word{t})
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// hotwords are the terms of a job's glossary the service is given, in order of use: the glossary as the manager sends
// it (the meeting's title and calendar title, its attendees, then the owner's glossary) with its titles moved last,
// since names and the owner's own terms matter most and a two-way session takes only about 100 tokens. Live sessions
// take what fits in that budget, leaving out phrases (longer than 20 characters); the streaming-input endpoint takes
// up to 5000 terms. The glossary is data: spellings only.
func hotwords(glossary []string, live bool) []string {
	var front, titles []string
	seen := map[string]bool{}
	for i, t := range glossary {
		t = strings.Join(strings.Fields(t), " ")
		k := strings.ToLower(t)
		if t == "" || seen[k] {
			continue
		}
		seen[k] = true
		if i < 2 && (utf8.RuneCountInString(t) > titleRunes || strings.Contains(t, " ")) {
			titles = append(titles, t)
			continue
		}
		front = append(front, t)
	}
	all := append(front, titles...)
	if !live {
		if len(all) > hotwordTermsReplay {
			all = all[:hotwordTermsReplay]
		}
		return all
	}
	var out []string
	budget := hotwordBudgetLive
	for _, t := range all {
		if utf8.RuneCountInString(t) > hotwordLongestLive {
			continue
		}
		n := tokens(t)
		if n > budget {
			continue // a shorter one later may still fit
		}
		budget -= n
		out = append(out, t)
	}
	return out
}

// tokens estimates what a term costs of the service's token budget: a Chinese, Japanese or Korean character each,
// a word in Latin letters or digits one per four characters started.
func tokens(t string) int {
	n, run := 0, 0
	flush := func() {
		n += (run + 3) / 4
		run = 0
	}
	for _, r := range t {
		switch {
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r), unicode.Is(unicode.Hangul, r):
			flush()
			n++
		case unicode.IsLetter(r), unicode.IsDigit(r):
			run++
		default:
			flush()
		}
	}
	flush()
	if n == 0 {
		n = 1
	}
	return n
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
