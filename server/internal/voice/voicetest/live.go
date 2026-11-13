// Package voicetest is a scripted stand-in for a GPT-Live-compatible upstream, for tests of the built-in voice
// extension's meeting transcription and of the manager that runs it (docs/assistant/voice.md "Transcription"). Its
// sessions hear the audio PCM makes of a script and answer with the frame shapes the measured service sends: interim
// captions on a caption session (one told only to listen, granted the caption extension) as the measured service
// writes them, one item for the whole session; one reply per sentence on a verbatim session (with the extension, also
// captions of its own, an item per reply); usage and the session's close.
//
// The audio carries the script: each 20 ms block of a sentence holds the sentence's number in its level, so a session
// knows which sentence it hears wherever its audio starts (a reconnect, a rollover, a window of the final pass).
package voicetest

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Line is one sentence of the script, in recording seconds.
type Line struct {
	Start, End float64
	Text       string
}

// Script is what the stand-in's sessions hear and how they behave.
type Script struct {
	Sentences []Line
	// CaptionNoise is the share of a caption's letters written wrong (0 none): captions are the noisier text.
	CaptionNoise float64
	// Rogue: verbatim session n (1-based, in the order verbatim sessions start) invents a reply of its own instead of
	// each sentence from the sentence numbered by the value (an index into Sentences) on, as a session that began to
	// play a participant does.
	Rogue map[int]int
	// Mute: verbatim session n says nothing from that sentence on.
	Mute map[int]int
	// NoCaptions: the caption extension is not granted (session.started echoes extensions {}).
	NoCaptions bool
	// RefuseCaptions: a session start that asks for it, caption or verbatim, is refused with an unsupported_extension
	// error (the socket stays open).
	RefuseCaptions bool
	// CaptionItems: each sentence's captions are an item of their own, with start_ms its start and is_final on its
	// last one (a service that splits its captions by sentence). Without it, as the measured service: a caption
	// session, which never replies, writes the whole meeting as one item, whose start_ms is the first speech's,
	// counted from the session's start (not its first audio), and never changes; no caption is ever final; the text
	// grows through a sentence and the next sentence's text replaces it.
	CaptionItems bool
	// Bursts: a verbatim session that was not granted the caption extension writes its replies only every this many
	// sentences, one sentence per line (and the ones left once it heard 2 s without speech), as the measured service
	// did with a prompt it did not follow sentence by sentence. 0: a reply per sentence. With the extension it
	// replies per sentence: the extension makes the service end a turn at each sentence.
	Bursts int
	// TextOnlyRefused: a session start with output_modalities is refused with an unknown_parameter error.
	TextOnlyRefused bool
	// ExpiresIn: each session ends this long after it started (session.started's expires_at says when; the service
	// then closes it); 0: far in the future, never.
	ExpiresIn time.Duration
	// RefuseVerbatim: the first n session starts of verbatim sessions (any but the caption session's instructions) are
	// refused with an upstream_unavailable error, as a service that is briefly overloaded does.
	RefuseVerbatim int
	// DropAfter: verbatim session n is closed by the service once it got this many seconds of audio.
	DropAfter map[int]float64
	// FailAfter: once this long has passed since the first session connected, every session is closed and every new
	// handshake is refused (503).
	FailAfter time.Duration
	// Reject refuses every handshake with this HTTP status (401, 403, 503), 0 none.
	Reject int
	// TurnGap is the silence that ends a turn, in seconds (0: 0.5).
	TurnGap float64
	// ReplyDelay is how long after a turn's end a verbatim session's reply begins (the measured service: about 1.5 s,
	// up to 3 s); ReplyJitter moves each reply by up to that much, earlier or later in turn. 0: at once.
	ReplyDelay, ReplyJitter time.Duration
	// Merge: sentence n's reply is held back and written with the next sentence's, as one reply (a service that
	// answers two sentences in one turn).
	Merge map[int]bool
	// CaptionNoiseFast is the share of a caption's letters written wrong while the caption session gets its audio
	// faster than one and a half times real time (captions break faster than real time); 0: no worse than CaptionNoise.
	CaptionNoiseFast float64
	// CaptionRevise: the caption session first writes each sentence's caption with its first word wrong and revises
	// it at once, in the same item, as the measured service did ("好人", then "how 人" at the same moment).
	CaptionRevise bool
	// Level scales the audio's levels (0: 1): below about 0.15 a sentence is quieter than the adapter's speech
	// detection hears, while the service still does (a phone on the table in a quiet room).
	Level float64
}

// InventedReplies are what rogue sessions write instead of a sentence, each its own (by its number): two rogue sessions
// never agree.
var InventedReplies = []string{
	"Sure, I think we should go with that plan and sort out the details together next week.",
	"Thanks everyone!",
	"Could you please repeat the last part, I could not follow what was decided?",
	"Okay.",
}

// listening are the caption session's instructions in every language. The measured service follows them: a session
// told them never replies, whatever extensions it asked for, and it is the stand-in's caption session.
var listening = func() map[string]bool {
	m := map[string]bool{}
	for _, l := range i18n.Langs {
		m[prompts.RenderVoiceTranscribeCaption(prompts.VoiceTranscribe{Lang: string(l)})] = true
	}
	return m
}()

const (
	rate      = 16000
	block     = rate / 50 // 20 ms
	levelBase = 2000      // a sentence's level: levelBase + levelStep × its number
	levelStep = 100
	heard     = levelBase - levelStep/2
)

// scale is the script's Level.
func (s Script) scale() float64 {
	if s.Level > 0 {
		return s.Level
	}
	return 1
}

// PCM is total seconds of the script's audio: 16 kHz PCM16 little-endian mono, silent except where a sentence is
// said. A sentence is 240 ms of its level and 60 ms of silence, over and over: speech with gaps between its words.
func PCM(s Script, total float64) []byte {
	n := int(math.Round(total * rate))
	out := make([]byte, 2*n)
	for i, l := range s.Sentences {
		level := int16(math.Round(float64(levelBase+levelStep*i) * s.scale()))
		from, to := int(math.Round(l.Start*rate)), min(int(math.Round(l.End*rate)), n)
		for k := from; k < to; k++ {
			if (k-from)%(rate*3/10) >= rate*24/100 {
				continue
			}
			v := level
			if k%2 == 1 {
				v = -level
			}
			binary.LittleEndian.PutUint16(out[2*k:], uint16(v))
		}
	}
	return out
}

// Live is the stand-in service: GET /v1/live is one session per WebSocket, GET /v1/live/models lists m1.
type Live struct {
	t   testing.TB
	s   Script
	srv *httptest.Server

	mu       sync.Mutex
	conns    []*conn
	starts   []map[string]any
	verbatim int
	captions int
	first    time.Time
	failed   bool
	refused  int // verbatim starts refused (RefuseVerbatim)
}

// NewLive starts the stand-in; it stops with the test.
func NewLive(t testing.TB, s Script) *Live {
	if s.TurnGap == 0 {
		s.TurnGap = 0.5
	}
	l := &Live{t: t, s: s}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/live/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"models":[{"id":"m1"}]}`)
	})
	mux.HandleFunc("GET /v1/live", l.serve)
	l.srv = httptest.NewServer(mux)
	t.Cleanup(l.srv.Close)
	return l
}

// URL is the service's session socket (a provider's url).
func (l *Live) URL() string { return "ws" + strings.TrimPrefix(l.srv.URL, "http") + "/v1/live" }

// Starts are the session objects of every session.start the service got, in order.
func (l *Live) Starts() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]map[string]any(nil), l.starts...)
}

// Sessions is how many caption and verbatim sessions have started.
func (l *Live) Sessions() (caption, verbatim int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.captions, l.verbatim
}

// Audio is the seconds of audio each session got, in the order they connected.
func (l *Live) Audio() []float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]float64, len(l.conns))
	for i, c := range l.conns {
		c.mu.Lock()
		out[i] = float64(c.samples) / rate
		c.mu.Unlock()
	}
	return out
}

// Open is how many sessions are connected now.
func (l *Live) Open() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.conns {
		c.mu.Lock()
		if !c.gone {
			n++
		}
		c.mu.Unlock()
	}
	return n
}

func (l *Live) serve(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	if l.first.IsZero() {
		l.first = time.Now()
		if l.s.FailAfter > 0 {
			time.AfterFunc(l.s.FailAfter, l.fail)
		}
	}
	failed := l.failed
	l.mu.Unlock()
	switch {
	case l.s.Reject != 0:
		http.Error(w, `{"error":{"message":"refused by the script"}}`, l.s.Reject)
		return
	case failed:
		http.Error(w, `{"error":{"message":"down"}}`, http.StatusServiceUnavailable)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20)
	l.mu.Lock()
	c := &conn{l: l, ws: ws, n: len(l.conns) + 1, replied: map[int]bool{}, done: make(chan struct{})}
	l.conns = append(l.conns, c)
	l.mu.Unlock()
	c.run()
}

// refuseVerbatim says whether this verbatim start is one RefuseVerbatim refuses.
func (l *Live) refuseVerbatim() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refused >= l.s.RefuseVerbatim {
		return false
	}
	l.refused++
	return true
}

func (l *Live) fail() {
	l.mu.Lock()
	l.failed = true
	conns := append([]*conn(nil), l.conns...)
	l.mu.Unlock()
	for _, c := range conns {
		_ = c.ws.Close(websocket.StatusGoingAway, "down")
	}
}

// conn is one session.
type conn struct {
	l  *Live
	ws *websocket.Conn
	n  int // its number among every session

	mu      sync.Mutex
	samples int64
	gone    bool
	sendMu  sync.Mutex    // the reader and the delayed replies both write
	done    chan struct{} // closed once the session is over
	queue   chan pendingReply
	recent  [][2]int64 // (wall time in ns, samples) of the last second's appends: how fast it gets its audio
	held    string     // a reply held back to be written with the next (Merge)
	// fastTurn: some of the turn's audio came faster than one and a half times real time (CaptionNoiseFast)
	fastTurn bool
	delayed  int // the replies delayed so far (ReplyJitter takes turns)

	vn       int  // its number among the verbatim sessions, 0 for a caption session
	caption  bool // a caption session whose extension was granted
	partials bool // a verbatim session whose extension was granted: captions of its own, an item per turn
	started  bool
	// startedAt is when session.started went out, lead how long after it the first audio came: the measured service's
	// start_ms counts from the session's start.
	startedAt time.Time
	lead      time.Duration
	heard     bool // any audio came
	part      []byte
	turn      bool
	from      int64 // the turn's start
	voice     int64 // the end of the turn's last voiced block
	idx       []int // the sentences heard in the turn, in order
	got       map[int]int64
	item      int
	partial   int64 // when the last caption went out
	usage     int64
	replied   map[int]bool
	replyNo   int
	burst     []string // a verbatim session's sentences held for its next reply (Bursts)
	// The caption session's caption: the item it writes, its start_ms, the sentence it is of, whether it has text
	// that has not been written whole yet.
	capItem  string
	capStart int64
	capIdx   int
	capOpen  bool
}

// pendingReply is a reply written once its time comes (ReplyDelay).
type pendingReply struct {
	due  time.Time
	text string
}

func (c *conn) run() {
	defer func() {
		c.mu.Lock()
		c.gone = true
		c.mu.Unlock()
		close(c.done)
	}()
	if c.l.s.ReplyDelay > 0 {
		c.queue = make(chan pendingReply, 256)
		go c.writeReplies()
	}
	for {
		_, b, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		var f struct {
			Type    string          `json:"type"`
			Audio   string          `json:"audio"`
			Session json.RawMessage `json:"session"`
		}
		if json.Unmarshal(b, &f) != nil {
			continue
		}
		switch f.Type {
		case "session.start":
			c.start(f.Session)
		case "session.input_audio.append":
			pcm, err := base64.StdEncoding.DecodeString(f.Audio)
			if err != nil || !c.started {
				continue
			}
			if !c.audio(pcm) {
				_ = c.ws.Close(websocket.StatusGoingAway, "dropped by the script")
				return
			}
		case "session.close":
			c.send(map[string]any{"type": "session.closed", "reason": "close_requested", "usage": map[string]any{"seconds": c.seconds()}})
			_ = c.ws.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
}

func (c *conn) start(raw json.RawMessage) {
	var sess map[string]any
	_ = json.Unmarshal(raw, &sess)
	ext, _ := sess["extensions"].(map[string]any)
	_, asks := ext["input_transcript_partial"]
	_, textOnly := sess["output_modalities"]
	ins, _ := sess["instructions"].(string)
	listener := listening[ins]
	l := c.l
	l.mu.Lock()
	l.starts = append(l.starts, sess)
	l.mu.Unlock()
	switch {
	case asks && l.s.RefuseCaptions:
		c.send(map[string]any{"type": "error", "error": map[string]any{"code": "unsupported_extension", "param": "session.extensions",
			"message": "unknown extension input_transcript_partial"}})
		return
	case textOnly && l.s.TextOnlyRefused:
		c.send(map[string]any{"type": "error", "error": map[string]any{"code": "unknown_parameter", "param": "session.output_modalities",
			"message": "unknown parameter"}})
		return
	case !listener && l.refuseVerbatim():
		c.send(map[string]any{"type": "error", "error": map[string]any{"code": "upstream_unavailable", "message": "overloaded"}})
		_ = c.ws.Close(websocket.StatusTryAgainLater, "overloaded")
		return
	}
	granted := map[string]any{}
	if asks && !l.s.NoCaptions {
		granted["input_transcript_partial"] = "v1"
	}
	l.mu.Lock()
	switch {
	case listener && len(granted) > 0:
		c.caption = true
		l.captions++
	case !listener:
		l.verbatim++
		c.vn = l.verbatim
		c.partials = len(granted) > 0
	}
	l.mu.Unlock()
	c.started, c.startedAt, c.capIdx = true, time.Now(), -1
	expires := any(int64(1) << 40)
	if d := l.s.ExpiresIn; d > 0 {
		expires = float64(time.Now().Add(d).UnixMilli()) / 1000
		time.AfterFunc(d, func() { _ = c.ws.Close(websocket.StatusGoingAway, "session expired") })
	}
	out := map[string]any{"extensions": granted, "expires_at": expires,
		"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": rate}}}
	if textOnly {
		out["output_modalities"] = []string{"text"}
	}
	c.send(map[string]any{"type": "session.started", "session": out})
}

// audio takes an append; false: the script drops the session now.
func (c *conn) audio(pcm []byte) bool {
	if !c.heard {
		c.heard, c.lead = true, time.Since(c.startedAt)
	}
	c.note(len(pcm) / 2)
	buf := append(c.part, pcm...)
	n := len(buf) / (2 * block) * (2 * block)
	gap := int64(c.l.s.TurnGap * rate)
	for off := 0; off < n; off += 2 * block {
		c.mu.Lock()
		at := c.samples
		c.samples += block
		c.mu.Unlock()
		peak := 0
		for k := off; k < off+2*block; k += 2 {
			v := int(int16(binary.LittleEndian.Uint16(buf[k:])))
			peak = max(peak, v, -v)
		}
		if float64(peak) >= heard*c.l.s.scale() {
			i := int(math.Round((float64(peak)/c.l.s.scale() - levelBase) / levelStep))
			if !c.turn {
				c.turn, c.from, c.idx, c.got, c.fastTurn = true, at, nil, map[int]int64{}, false
				c.item++
			}
			c.fastTurn = c.fastTurn || c.l.s.CaptionNoiseFast > 0 && c.fast() // a turn heard fast stays broken
			c.voice = at + block
			if _, ok := c.got[i]; !ok {
				c.idx = append(c.idx, i)
			}
			c.got[i] += block
			if c.caption && i != c.capIdx {
				c.nextSentence(i, at)
			}
		} else if c.turn && at+block-c.voice >= gap {
			c.endTurn()
		} else if !c.turn && len(c.burst) > 0 && at+block-c.voice >= 2*rate {
			c.later(strings.Join(c.burst, "\n"))
			c.burst = nil
		}
		if (c.caption || c.partials) && c.turn && at+block-c.partial >= rate/5 {
			c.partial = at + block
			if c.caption {
				c.sentenceCaption(false)
			} else {
				c.turnCaption()
			}
		}
		if at+block-c.usage >= rate {
			c.usage = at + block
			c.send(map[string]any{"type": "session.usage.updated", "usage": map[string]any{"seconds": c.seconds()},
				"context_window": map[string]any{"usage_ratio": 0.01}})
		}
		if most, ok := c.l.s.DropAfter[c.vn]; ok && c.vn > 0 && float64(at+block)/rate >= most {
			return false
		}
	}
	c.part = append([]byte(nil), buf[n:]...)
	return true
}

// note keeps the append of n samples now, for fast.
func (c *conn) note(n int) {
	now := time.Now().UnixNano()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recent = append(c.recent, [2]int64{now, int64(n)})
	for len(c.recent) > 0 && now-c.recent[0][0] > int64(time.Second) {
		c.recent = c.recent[1:]
	}
}

// fast says whether the session got more than one and a half seconds of audio in the last second.
func (c *conn) fast() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int64
	for _, r := range c.recent {
		n += r[1]
	}
	return n > rate*3/2
}

func (c *conn) seconds() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return float64(c.samples) / rate
}

// sessionMS is session sample at in the service's session time: milliseconds since the session started, its audio
// having begun lead after that.
func (c *conn) sessionMS(at int64) int64 { return c.lead.Milliseconds() + at*1000/rate }

// nextSentence: the caption session hears sentence i begin at at. The caption of the sentence before it, if not
// written whole yet, is (final when CaptionItems); then the caption is of sentence i: the same item, and the same
// start_ms, as the measured service writes them, or an item of its own from at with CaptionItems.
func (c *conn) nextSentence(i int, at int64) {
	if c.capOpen {
		c.sentenceCaption(true)
	}
	c.capIdx, c.capOpen = i, true
	switch {
	case c.l.s.CaptionItems:
		c.capItem, c.capStart = fmt.Sprintf("item-%d-%d", c.n, i), c.sessionMS(at)
	case c.capItem == "":
		c.capItem, c.capStart = fmt.Sprintf("item-%d", c.n), c.sessionMS(at)
	}
	if c.l.s.CaptionRevise {
		c.sendCaption(c.capItem, c.capStart, misheard(c.text(i)), false)
	}
	c.partial = 0 // its first words go out at once
}

// misheard is a caption's first hypothesis of text's beginning: its first two letters with the first one wrong.
func misheard(text string) string {
	rs := []rune(text)
	if len(rs) == 0 {
		return ""
	}
	wrong := 'x'
	if rs[0] == 'x' || rs[0] == 'X' {
		wrong = 'y'
	}
	return string(append([]rune{wrong}, rs[1:min(2, len(rs))]...))
}

// sentenceCaption sends the caption session's caption of its sentence so far (whole: all of it, and final with
// CaptionItems).
func (c *conn) sentenceCaption(whole bool) {
	i := c.capIdx
	text := c.text(i)
	if !whole {
		text = c.heardOf(i, text)
	} else {
		c.capOpen = false
	}
	c.sendCaption(c.capItem, c.capStart, text, whole && c.l.s.CaptionItems)
}

// turnCaption sends a verbatim session's caption of its turn so far: an item per turn, as the measured service's
// verbatim sessions with the extension had (an item per reply). The adapter does not use them.
func (c *conn) turnCaption() {
	var b strings.Builder
	for k, i := range c.idx {
		text := c.text(i)
		if k == len(c.idx)-1 {
			text = c.heardOf(i, text)
		}
		b.WriteString(text)
	}
	c.sendCaption(fmt.Sprintf("item-%d-%d", c.n, c.item), c.sessionMS(c.from), b.String(), false)
}

// heardOf is as much of sentence i's text as the turn has heard of it.
func (c *conn) heardOf(i int, text string) string {
	if i < 0 || i >= len(c.l.s.Sentences) {
		return ""
	}
	l := c.l.s.Sentences[i]
	rs := []rune(text)
	share := float64(c.got[i]) / rate / max(l.End-l.Start, 0.1)
	return string(rs[:min(len(rs), int(math.Ceil(share*float64(len(rs)))))])
}

func (c *conn) sendCaption(item string, startMS int64, text string, final bool) {
	noise := c.l.s.CaptionNoise
	if c.l.s.CaptionNoiseFast > noise && c.fastTurn {
		noise = c.l.s.CaptionNoiseFast
	}
	c.send(map[string]any{"type": "svc.input_transcript.partial", "item_id": item, "start_ms": startMS,
		"text": noisy(text, noise), "is_final": final})
}

func (c *conn) text(i int) string {
	if i < 0 || i >= len(c.l.s.Sentences) {
		return ""
	}
	return c.l.s.Sentences[i].Text
}

func (c *conn) endTurn() {
	c.turn = false
	if c.caption {
		if c.capOpen {
			c.sentenceCaption(true)
		}
		return
	}
	if c.vn == 0 {
		return
	}
	for _, i := range c.idx {
		if c.replied[i] || i < 0 || i >= len(c.l.s.Sentences) {
			continue
		}
		c.replied[i] = true
		if from, ok := c.l.s.Mute[c.vn]; ok && i >= from {
			continue
		}
		text := c.text(i)
		if from, ok := c.l.s.Rogue[c.vn]; ok && i >= from {
			text = InventedReplies[(c.vn-1)%len(InventedReplies)]
		}
		if c.held != "" {
			text, c.held = c.held+" "+text, ""
		}
		if c.l.s.Merge[i] {
			c.held = text
			continue
		}
		if c.l.s.Bursts > 0 && !c.partials {
			if c.burst = append(c.burst, text); len(c.burst) >= c.l.s.Bursts {
				c.later(strings.Join(c.burst, "\n"))
				c.burst = nil
			}
			continue
		}
		c.later(text)
	}
}

// later writes reply text now, or ReplyDelay (moved by ReplyJitter) after now, in order.
func (c *conn) later(text string) {
	if c.queue == nil {
		c.reply(text)
		return
	}
	delay := c.l.s.ReplyDelay + c.l.s.ReplyJitter*time.Duration(c.delayed%3-1)
	c.delayed++
	c.queue <- pendingReply{due: time.Now().Add(delay), text: text}
}

// writeReplies writes the delayed replies as their times come.
func (c *conn) writeReplies() {
	for {
		select {
		case <-c.done:
			return
		case r := <-c.queue:
			t := time.NewTimer(time.Until(r.due))
			select {
			case <-c.done:
				t.Stop()
				return
			case <-t.C:
			}
			c.reply(r.text)
		}
	}
}

// reply is one reply of the measured shape: created, the text in pieces, its whole text, completed.
func (c *conn) reply(text string) {
	c.replyNo++
	id := fmt.Sprintf("resp-%d-%d", c.n, c.replyNo)
	ev := func(e map[string]any) {
		c.send(map[string]any{"type": "response.event", "delegation_id": id, "event": e})
	}
	ev(map[string]any{"type": "response.created", "response": map[string]any{"id": id}})
	rs := []rune(text)
	for k := 0; k < len(rs); k += 7 {
		ev(map[string]any{"type": "response.output_text.delta", "delta": string(rs[k:min(k+7, len(rs))])})
	}
	ev(map[string]any{"type": "response.output_text.done", "text": text})
	ev(map[string]any{"type": "response.completed", "response": map[string]any{"id": id}})
}

func (c *conn) send(v any) {
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_ = c.ws.Write(ctx, websocket.MessageText, b)
}

// noisy writes every 1/share-th letter of s wrong.
func noisy(s string, share float64) string {
	if share <= 0 {
		return s
	}
	every := max(int(math.Round(1/share)), 1)
	rs, k := []rune(s), 0
	for i, r := range rs {
		if r == ' ' || strings.ContainsRune(",.?!", r) {
			continue
		}
		k++
		if k%every == 0 {
			rs[i] = 'x'
		}
	}
	return string(rs)
}
