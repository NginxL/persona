// Package volctest stands in for Volcengine's speech service in tests (docs/extensions/volcengine-speech.md "Tests"):
// streaming recognition over its binary WebSocket protocol and synthesis over its HTTP lines, written from the
// service's documentation independently of the client in package volc.
//
// Its recognition hears sentences in the audio by their level (PCM): a 20 ms frame at level 1000+100·n is sentence n's
// speech, a quiet one silence; a sentence ends after 0.4 s of silence. On the two-pass live endpoint (bigmodel_async)
// it answers every packet, with each sentence definite once it ended and the one being said as interim text; on the
// streaming-input endpoint (bigmodel_nostream) it answers once, at the last packet. Times are session milliseconds.
// Synthesis answers 10 ms of silence (480 bytes at 24 kHz) per character of the text.
package volctest

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// EnvService names the stand-in's address for the program (volc.EnvService, which a test of the module keeps equal):
// a core test sets it without importing the module (docs/architecture/core-and-modules.md "Guards").
const EnvService = "PERSONA_VOLC_SERVICE"

// Script is how the stand-in misbehaves; Set changes it while it runs.
type Script struct {
	RefuseASR      int             // an HTTP status refusing every recognition handshake (0: none), with RefuseBody
	RefuseBody     string          //
	DropLines      []int           // a session sent speech of one of these lines (Line.N) drops at once: a window that keeps failing
	DropLinesAfter int             // if positive, DropLines waits until this many session milliseconds have arrived
	DropGate       <-chan struct{} // if non-nil, a DropLines failure waits for this test event
	DropAll        bool            // every session drops at its next audio packet: the service is down
	RefuseRequest  uint32          // an error frame's code answering the request (0: none), with RefuseMessage
	RefuseMessage  string          //
	DropAfter      int             // the socket of each of the first DropN sessions closes after this many audio packets
	DropN          int             //
	EmptyAudio     bool            // a session that heard only silence ends with error 45000002 (empty audio)
	TTSStatus      int             // an HTTP status refusing synthesis (0: none), with TTSBody
	TTSBody        string          //
	TTSLineCode    int             // a line with this code in place of the audio (0: none), with TTSLineMessage
	TTSLineMessage string          //
}

// Session is one recognition session as the service saw it.
type Session struct {
	Endpoint string
	Header   http.Header
	Request  Request
	Audio    []byte
	Packets  int
	Last     bool // it got the last packet
}

// Request is a session's request as sent.
type Request struct {
	User struct {
		UID string `json:"uid"`
	} `json:"user"`
	Audio struct {
		Format  string `json:"format"`
		Codec   string `json:"codec"`
		Rate    int    `json:"rate"`
		Bits    int    `json:"bits"`
		Channel int    `json:"channel"`
	} `json:"audio"`
	Request struct {
		ModelName       string `json:"model_name"`
		EnableITN       bool   `json:"enable_itn"`
		EnablePunc      bool   `json:"enable_punc"`
		ShowUtterances  bool   `json:"show_utterances"`
		ResultType      string `json:"result_type"`
		EnableNonstream bool   `json:"enable_nonstream"`
		Corpus          *struct {
			Context string `json:"context"`
		} `json:"corpus"`
	} `json:"request"`
}

// Synthesis is one synthesis request as sent.
type Synthesis struct {
	Header  http.Header
	Text    string
	Speaker string
	Format  string
	Rate    int
}

// Service is the stand-in. URL is its address (http://…): recognition is under /api/v3/sauc/<endpoint>, synthesis
// at /api/v3/tts/unidirectional.
type Service struct {
	URL string
	t   testing.TB

	mu       sync.Mutex
	script   Script
	sessions []*Session
	tts      []Synthesis
}

// New starts a stand-in, closed when the test ends.
func New(t testing.TB) *Service {
	s := &Service{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/sauc/", s.serveASR)
	mux.HandleFunc("/api/v3/tts/unidirectional", s.serveTTS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// ASRBase is the recognition address (ws://…/api/v3/sauc/), endpoints after it.
func (s *Service) ASRBase() string { return "ws" + strings.TrimPrefix(s.URL, "http") + "/api/v3/sauc/" }

// TTSURL is the synthesis address.
func (s *Service) TTSURL() string { return s.URL + "/api/v3/tts/unidirectional" }

// Set changes the script.
func (s *Service) Set(f func(*Script)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.script)
}

// Count is how many recognition sessions began.
func (s *Service) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// Session is a copy of session i (nil when there is none).
func (s *Service) Session(i int) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.sessions) {
		return nil
	}
	c := *s.sessions[i]
	c.Audio = append([]byte(nil), c.Audio...)
	return &c
}

// Syntheses are the synthesis requests so far.
func (s *Service) Syntheses() []Synthesis {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Synthesis(nil), s.tts...)
}

// The protocol's numbers (the service's documentation): header byte 0 is version 1 and a 4-byte header.
const (
	fullClient = 0x1
	audioOnly  = 0x2
	fullServer = 0x9
	errorMsg   = 0xF
	gzipped    = 0x1
	json1      = 0x1
)

func gz(p []byte) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write(p)
	_ = w.Close()
	return b.Bytes()
}

func gunzip(p []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(p))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// result is a full server response: sequence seq (negative and flagged last on the final one).
func result(seq int32, last bool, payload []byte) []byte {
	var b bytes.Buffer
	flags := byte(0x1)
	if last {
		flags, seq = 0x3, -abs(seq)
	}
	b.Write([]byte{0x11, fullServer<<4 | flags, json1<<4 | gzipped, 0})
	_ = binary.Write(&b, binary.BigEndian, seq)
	z := gz(payload)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(z)))
	b.Write(z)
	return b.Bytes()
}

func abs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func errorFrame(code uint32, msg string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x11, errorMsg << 4, json1 << 4, 0})
	_ = binary.Write(&b, binary.BigEndian, code)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(msg)))
	b.WriteString(msg)
	return b.Bytes()
}

// clientFrame reads a client's frame: its type, sequence number and payload (unzipped).
func clientFrame(b []byte) (typ byte, seq int32, payload []byte, err error) {
	if len(b) < 12 || b[0] != 0x11 {
		return 0, 0, nil, fmt.Errorf("a frame of %d bytes starting %x", len(b), b[:min(4, len(b))])
	}
	typ, flags := b[1]>>4, b[1]&0x0f
	if b[2]&0x0f != gzipped {
		return 0, 0, nil, fmt.Errorf("not gzipped: %x", b[2])
	}
	if flags&0x1 == 0 {
		return 0, 0, nil, fmt.Errorf("no sequence number: flags %x", flags)
	}
	seq = int32(binary.BigEndian.Uint32(b[4:]))
	if (flags == 0x3) != (seq < 0) {
		return 0, 0, nil, fmt.Errorf("flags %x with sequence %d", flags, seq)
	}
	n := binary.BigEndian.Uint32(b[8:])
	if int(n) != len(b)-12 {
		return 0, 0, nil, fmt.Errorf("payload size %d of %d", n, len(b)-12)
	}
	payload, err = gunzip(b[12:])
	return typ, seq, payload, err
}

func (s *Service) serveASR(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	sc := s.script
	s.mu.Unlock()
	w.Header().Set("X-Tt-Logid", "test-logid")
	if sc.RefuseASR != 0 {
		w.WriteHeader(sc.RefuseASR)
		_, _ = w.Write([]byte(sc.RefuseBody))
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 22)
	ctx := r.Context()
	sess := &Session{Endpoint: strings.TrimPrefix(r.URL.Path, "/api/v3/sauc/"), Header: r.Header.Clone()}
	s.mu.Lock()
	s.sessions = append(s.sessions, sess)
	n := len(s.sessions)
	s.mu.Unlock()
	_, raw, err := c.Read(ctx)
	if err != nil {
		return
	}
	typ, seq, payload, err := clientFrame(raw)
	if err != nil || typ != fullClient || seq != 1 {
		s.t.Errorf("volctest: the first frame: type %d, sequence %d: %v", typ, seq, err)
		return
	}
	s.mu.Lock()
	err = json.Unmarshal(payload, &sess.Request)
	s.mu.Unlock()
	if err != nil {
		s.t.Errorf("volctest: the request: %v", err)
		return
	}
	if sc.RefuseRequest != 0 {
		_ = c.Write(ctx, websocket.MessageBinary, errorFrame(sc.RefuseRequest, sc.RefuseMessage))
		return
	}
	_ = c.Write(ctx, websocket.MessageBinary, result(1, false, []byte(`{"result":{"text":""}}`)))
	live := sess.Endpoint == "bigmodel_async"
	var h hearing
	handed := map[int]bool{}
	dropLineHeard := false
	answer := func(seq int32, last bool) error {
		all := h.utterances(last)
		var out []map[string]any
		var text strings.Builder
		for _, u := range all {
			text.WriteString(u.text)
			if u.definite && handed[u.start] {
				continue
			}
			if u.definite {
				handed[u.start] = true
			}
			out = append(out, map[string]any{"text": u.text, "start_time": u.start, "end_time": u.end, "definite": u.definite})
		}
		raw, _ := json.Marshal(map[string]any{"audio_info": map[string]any{"duration": h.ms()},
			"result": map[string]any{"text": text.String(), "utterances": out}})
		return c.Write(ctx, websocket.MessageBinary, result(seq, last, raw))
	}
	for {
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		typ, seq, pcm, err := clientFrame(raw)
		if err != nil || typ != audioOnly {
			s.t.Errorf("volctest: an audio frame: type %d: %v", typ, err)
			return
		}
		s.mu.Lock()
		sess.Audio = append(sess.Audio, pcm...)
		sess.Packets++
		packets := sess.Packets
		s.mu.Unlock()
		h.add(pcm)
		s.mu.Lock()
		down := s.script.DropAll // read at each packet: a test turns it on while sessions run
		s.mu.Unlock()
		dropLineHeard = dropLineHeard || slices.ContainsFunc(sc.DropLines, func(line int) bool { return speaks(pcm, line) })
		dropLine := dropLineHeard && (sc.DropLinesAfter <= 0 || h.ms() >= sc.DropLinesAfter)
		if down || n <= sc.DropN && packets >= sc.DropAfter || dropLine {
			if dropLine && sc.DropGate != nil {
				select {
				case <-sc.DropGate:
				case <-ctx.Done():
					return
				}
			}
			c.CloseNow()
			return
		}
		if seq < 0 {
			s.mu.Lock()
			sess.Last = true
			s.mu.Unlock()
			if sc.EmptyAudio && h.loud == 0 {
				_ = c.Write(ctx, websocket.MessageBinary, errorFrame(45000002, "empty audio"))
				return
			}
			if answer(seq, true) == nil {
				c.Close(websocket.StatusNormalClosure, "")
			}
			return
		}
		if live && answer(seq, false) != nil {
			return
		}
	}
}

type utterance struct {
	text       string
	start, end int
	definite   bool
}

// hearing hears PCM's sentences, by 20 ms frames.
type hearing struct {
	frames []int // a sentence's number per frame, -1 for silence
	carry  []byte
	loud   int
}

const frameBytes = 640 // 20 ms at 16 kHz

func level(pcm []byte) float64 {
	var sum float64
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(pcm)/2))
}

func (h *hearing) add(pcm []byte) {
	pcm = append(h.carry, pcm...)
	for len(pcm) >= frameBytes {
		n := -1
		if l := level(pcm[:frameBytes]); l > 500 {
			n = int(math.Round((l - 1000) / 100))
			h.loud++
		}
		h.frames = append(h.frames, n)
		pcm = pcm[frameBytes:]
	}
	h.carry = append([]byte(nil), pcm...)
}

func (h *hearing) ms() int { return len(h.frames) * 20 }

// utterances are the runs of one sentence's frames, definite once 0.4 s of silence follow them (or at the last packet).
func (h *hearing) utterances(last bool) []utterance {
	var out []utterance
	for i := 0; i < len(h.frames); {
		n := h.frames[i]
		if n < 0 {
			i++
			continue
		}
		j := i
		for j < len(h.frames) && h.frames[j] == n {
			j++
		}
		quiet := 0
		for k := j; k < len(h.frames) && h.frames[k] < 0; k++ {
			quiet++
		}
		u := utterance{text: Sentence(n), start: i * 20, end: j * 20, definite: last || quiet >= 20}
		if !u.definite {
			u.text = strings.TrimSuffix(u.text, "。") // i18n-ignore: test data
		}
		out = append(out, u)
		i = j
	}
	return out
}

// Sentence is the text the stand-in hears for sentence n.
func Sentence(n int) string { return fmt.Sprintf("第%d句。", n) } // i18n-ignore: test data

// Line is a sentence of a script: its number, then seconds of its speech and of silence after it.
type Line struct {
	N               int
	Speech, Silence float64
}

// PCM is the audio of a script (PCM16 little-endian, mono, 16 kHz): each sentence a square wave at its level.
func PCM(lines ...Line) []byte {
	var b []byte
	for _, l := range lines {
		n := int(math.Round(l.Speech * 16000))
		v := int16(1000 + 100*l.N)
		for i := range n {
			s := v
			if i%2 == 1 {
				s = -v
			}
			b = binary.LittleEndian.AppendUint16(b, uint16(s))
		}
		b = append(b, make([]byte, int(math.Round(l.Silence*16000))*2)...)
	}
	return b
}

// speaks: pcm has a sample of line n's speech (PCM writes it as ±(1000+100n)).
func speaks(pcm []byte, n int) bool {
	v := int16(1000 + 100*n)
	for i := 0; i+1 < len(pcm); i += 2 {
		if x := int16(binary.LittleEndian.Uint16(pcm[i:])); x == v || x == -v {
			return true
		}
	}
	return false
}

func (s *Service) serveTTS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReqParams struct {
			Text        string `json:"text"`
			Speaker     string `json:"speaker"`
			AudioParams struct {
				Format     string `json:"format"`
				SampleRate int    `json:"sample_rate"`
			} `json:"audio_params"`
		} `json:"req_params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	s.tts = append(s.tts, Synthesis{Header: r.Header.Clone(), Text: req.ReqParams.Text, Speaker: req.ReqParams.Speaker,
		Format: req.ReqParams.AudioParams.Format, Rate: req.ReqParams.AudioParams.SampleRate})
	sc := s.script
	s.mu.Unlock()
	w.Header().Set("X-Tt-Logid", "test-logid")
	if sc.TTSStatus != 0 {
		w.WriteHeader(sc.TTSStatus)
		_, _ = w.Write([]byte(sc.TTSBody))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if sc.TTSLineCode != 0 {
		fmt.Fprintf(w, "{\"code\":%d,\"message\":%q}\n", sc.TTSLineCode, sc.TTSLineMessage)
		return
	}
	fl, _ := w.(http.Flusher)
	for range []rune(req.ReqParams.Text) {
		fmt.Fprintf(w, "{\"code\":0,\"data\":%q}\n", base64.StdEncoding.EncodeToString(make([]byte, 480)))
		if fl != nil {
			fl.Flush()
		}
		time.Sleep(time.Millisecond)
	}
	fmt.Fprint(w, "{\"code\":20000000,\"message\":\"ok\",\"data\":null}\n")
}
