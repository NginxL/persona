package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

// Audio from the phone (docs/assistant/meeting-minutes.md "Audio from the phone", "Gaps and the final pass"): the
// audio socket GET /api/v1/recordings/{id}/audio, which the hub relays over a relay stream (assistantwire.SocketRoutes,
// the extensions layer hands it here), and the gap uploads PUT /api/v1/recordings/{id}/audio?at=. Full credentials
// only. Before the upgrade the socket is refused only for the caller (403) and the knowledge base (503); the
// recording's own refusals come as the first frame after it, a fatal error, so the phone can read them.

// Codes of the audio socket's fatal errors and of the gap uploads' refusals (the contract's; an open set).
const (
	codeBadFrame           = "bad_frame"
	codeReplaced           = "replaced"
	codeAudioFull          = "audio_full"
	codeNotHub             = "not_hub"
	codeTranscriptionFinal = "transcription_final"
	codeHubTranscribes     = "hub_transcribes"
	codeBadAudio           = "bad_audio"
	codeHubFailed          = "hub_failed" // the hub's own failure (closed 1011)
)

// Limits of the audio socket and the gap uploads.
const (
	maxAudioFrame = 64 << 10 // a binary frame: whole samples
	// A text frame: a whole timeline fits, kb.MaxTimelineSpans spans each with up to 4000 bytes of answer and
	// kb.MaxTimelineRoles role changes. The answers are about 4 MB as UTF-8; JSON may escape them to twice that
	// (quotes and backslashes, or a sender that writes \u escapes for non-ASCII text), so the limit takes 8 MB of
	// text and the JSON around it.
	maxAudioText    = 8704 << 10
	audioReadMost   = 9 << 20  // what the socket reads of one frame at all (more closes it)
	maxAudioBody    = 10 << 20 // PUT …/audio
	audioSilentMost = 60 * time.Second
	audioSendQueue  = 1024
)

// socketRoutes are the assistant's WebSocket routes (assistantwire.SocketRoutes), served by ServeSocket.
var socketRoutes = []struct {
	pattern string
	serve   func(s *Service, w http.ResponseWriter, r *http.Request)
}{
	{"GET /api/v1/recordings/{id}/audio", (*Service).audioSocket},
	{assistantwire.A2ARPC, (*Service).a2aRPC},
	{assistantwire.A2ACard, (*Service).a2aCard},
}

// ServeSocket serves the assistant's WebSocket routes (assistantwire.SocketRoutes), which the hub relays to the
// manager's extensions layer and that hands here (extmgr.Options.AssistantSocket).
func (s *Service) ServeSocket(w http.ResponseWriter, r *http.Request) {
	s.sockets.Do(func() {
		s.socketMux = http.NewServeMux()
		for _, sr := range socketRoutes {
			s.socketMux.HandleFunc(sr.pattern, func(w http.ResponseWriter, r *http.Request) { sr.serve(s, w, r) })
		}
	})
	s.socketMux.ServeHTTP(w, r)
}

// phoneFrame is a text frame from the phone.
type phoneFrame struct {
	Type       string          `json:"type"`
	At         *float64        `json:"at"`
	Attendance string          `json:"attendance"`
	On         *bool           `json:"on"`
	Text       string          `json:"text"` // speaking on: the answer Persona says
	Timeline   json.RawMessage `json:"timeline"`
}

// audioSocket is GET /api/v1/recordings/{id}/audio.
func (s *Service) audioSocket(w http.ResponseWriter, r *http.Request) {
	if !meetingCaller(w, r) {
		return
	}
	e := s.meetingEngine(w, r)
	if e == nil {
		return
	}
	heard := &atomic.Int64{}
	note := func() { heard.Store(time.Now().UnixNano()) }
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true,
		OnPingReceived: func(context.Context, []byte) bool { note(); return true },
		OnPongReceived: func(context.Context, []byte) { note() }})
	if err != nil {
		return
	}
	note()
	c.SetReadLimit(audioReadMost)
	ctx := context.WithoutCancel(r.Context())
	s.hubTr.wg.Add(1)
	defer s.hubTr.wg.Done()
	var helpers sync.WaitGroup // the socket's own goroutines, ended before it returns
	defer helpers.Wait()
	p := newPhoneSocket(c)
	defer p.wait()
	id := r.PathValue("id")
	aw, err := e.AudioOpen(id)
	if code := s.audioRefusal(ctx, e, err); code != "" {
		if aw != nil {
			_ = aw.Close()
		}
		p.fail(code)
		return
	}
	defer aw.Close()
	rid, _ := kb.RecordingID(id)
	run := s.hubRunFor(e, rid)
	if run == nil {
		p.fail(codeTranscriptionFinal) // its live pass is over
		return
	}
	if code := run.attach(p); code != "" {
		p.fail(code)
		return
	}
	ended := false
	defer func() { run.detach(p, ended) }()
	helpers.Add(1)
	go func() {
		defer helpers.Done()
		p.keepAlive(heard)
	}()

	// start, within audioStartWait; no audio before ready.
	var start phoneFrame
	timer := time.AfterFunc(audioStartWait, func() { p.fail(codeBadFrame) })
	for {
		typ, raw, err := c.Read(ctx)
		if err != nil {
			timer.Stop()
			return
		}
		note()
		if typ != websocket.MessageText || len(raw) > maxAudioText || json.Unmarshal(raw, &start) != nil {
			timer.Stop()
			p.fail(codeBadFrame)
			return
		}
		if start.Type == "start" {
			break
		}
	}
	if !timer.Stop() {
		return // too late: refused
	}
	var tl *kb.Timeline
	if t, ok := kb.ParseTimeline(start.Timeline); ok {
		tl = &t
	}
	from := aw.HeldTo()
	held, _ := e.AudioHeld(rid)
	gaps := kb.GapsIn(held, 0, from)
	if gaps == nil {
		gaps = [][2]float64{}
	}
	run.begin(p, tl, from, gaps) // ready, then the standing problem

	// The audio and the frames, acknowledged about every audioAckEvery.
	pos := int64(math.Round(from * kb.AudioRate))
	var wrote atomic.Bool
	stopAcks := make(chan struct{})
	defer close(stopAcks)
	helpers.Add(1)
	go func() {
		defer helpers.Done()
		t := time.NewTicker(audioAckEvery)
		defer t.Stop()
		for {
			select {
			case <-stopAcks:
				return
			case <-p.done:
				return
			case <-t.C:
			}
			if !wrote.Swap(false) {
				continue
			}
			at, err := aw.Sync()
			if err != nil {
				p.fail(audioWriteCode(err))
				return
			}
			p.send(map[string]any{"type": "ack", "at": at})
		}
	}()
	for {
		typ, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		note()
		if typ == websocket.MessageBinary {
			if len(raw)%2 != 0 || len(raw) > maxAudioFrame {
				p.fail(codeBadFrame)
				return
			}
			if len(raw) == 0 {
				continue
			}
			at := float64(pos) / kb.AudioRate
			err := aw.Write(at, raw)
			if err != nil && !errors.Is(err, kb.ErrAudioFull) {
				p.fail(audioWriteCode(err))
				return
			}
			run.audio(at, raw)
			if err != nil {
				p.fail(codeAudioFull)
				return
			}
			pos += int64(len(raw) / 2)
			wrote.Store(true)
			continue
		}
		var f phoneFrame
		if len(raw) > maxAudioText || json.Unmarshal(raw, &f) != nil {
			p.fail(codeBadFrame)
			return
		}
		at, hasAt := -1.0, f.At != nil && finiteTime(*f.At)
		if hasAt {
			at = *f.At
		}
		switch f.Type {
		case "seek":
			if !hasAt {
				p.fail(codeBadFrame)
				return
			}
			pos = int64(math.Round(at * kb.AudioRate))
		case "role":
			if hasAt {
				run.roleFrame(at, f.Attendance)
			}
		case "speaking":
			if hasAt && f.On != nil {
				run.speaking(p, *f.On, at, f.Text)
			}
		case "end":
			if !hasAt {
				at = float64(pos) / kb.AudioRate
			}
			ended = true
			s.audioEnd(e, run, p, aw, rid, at)
			return
		}
	}
}

// finiteTime: t is a recording time (a number of 0 or more).
func finiteTime(t float64) bool { return !math.IsNaN(t) && !math.IsInf(t, 0) && t >= 0 }

// audioEnd is the phone's end at recording second at on socket p: the recording's end is stored, the audio
// acknowledged up to it, the job's last sentences sent as lines (within audioEndWait), then the socket closed 1000.
func (s *Service) audioEnd(e *kb.Engine, run *hubRun, p *phoneSocket, aw *kb.AudioWriter, id string, at float64) {
	if _, err := e.TranscriptionUpdate(id, func(t *kb.MeetingTranscription, _ kb.Recording) error {
		t.End = at
		return nil
	}); err != nil {
		s.logf("recording %s: transcription end: %v", id, err)
	}
	if _, err := aw.Sync(); err != nil {
		p.fail(audioWriteCode(err))
		return
	}
	p.send(map[string]any{"type": "ack", "at": at})
	run.finishLive(p, at)
	p.closeWith(websocket.StatusNormalClosure)
	s.kickFinal()
}

// finishLive: the phone on socket p sent end at recording second end: the live job is ended and its last sentences
// come (within audioEndWait); a speaking span still open counts as closed at end (expireSpanLocked: the sentences held
// for it are judged with [from, end] and their line frames go out on p before it closes), then the hub's transcript is
// written.
func (r *hubRun) finishLive(p *phoneSocket, end float64) {
	r.mu.Lock()
	if r.phone == p {
		r.ended = true
	}
	r.finishing = true // the final pass waits for the last sentences
	j := r.job
	if j != nil {
		r.endJobLocked(j)
	}
	r.mu.Unlock()
	deadline := time.NewTimer(audioEndWait)
	defer deadline.Stop()
	for j != nil {
		r.mu.Lock()
		if r.job != j || r.stopped {
			r.mu.Unlock()
			break
		}
		wake := r.wake
		r.mu.Unlock()
		select {
		case <-wake:
			continue
		case <-deadline.C:
		}
		r.mu.Lock()
		r.dropJobLocked(j)
		r.mu.Unlock()
		break
	}
	r.mu.Lock()
	if r.openFrom != nil && !r.stopped {
		r.expireSpanLocked(*r.openFrom, end)
	}
	r.putHubLocked()
	r.finishing = false
	r.mu.Unlock()
}

// audioRefusal is the code of the fatal error that refuses the socket for err (AudioOpen's), in the contract's order;
// "" when it is taken. A knowledge base read-only in the settings takes kb_read_only's place.
func (s *Service) audioRefusal(ctx context.Context, e *kb.Engine, err error) string {
	writable := s.meetingWritable(ctx, e)
	switch {
	case errors.Is(err, kb.ErrRecordingDeleted):
		return codeRecordingDeleted
	case errors.Is(err, kb.ErrNotFound):
		return codeRecordingNotFound
	case !writable, errors.Is(err, kb.ErrReadOnly):
		return assistantwire.CodeKBReadOnly
	case err == nil:
		return ""
	case errors.Is(err, kb.ErrNotHub):
		return codeNotHub
	case errors.Is(err, kb.ErrTranscriptExpired):
		return codeTranscriptExpired
	case errors.Is(err, kb.ErrTranscriptionFinal):
		return codeTranscriptionFinal
	}
	s.logf("audio socket: %v", err)
	return codeHubFailed
}

// audioWriteCode is the code of the fatal error for a write of the socket's audio that failed with err.
func audioWriteCode(err error) string {
	switch {
	case errors.Is(err, kb.ErrRecordingDeleted), errors.Is(err, kb.ErrNotFound):
		return codeRecordingDeleted
	case errors.Is(err, kb.ErrReadOnly):
		return assistantwire.CodeKBReadOnly
	case errors.Is(err, kb.ErrTranscriptExpired):
		return codeTranscriptExpired
	case errors.Is(err, kb.ErrTranscriptionFinal):
		return codeTranscriptionFinal
	case errors.Is(err, kb.ErrAudioFull):
		return codeAudioFull
	case errors.Is(err, kb.ErrBadAudio):
		return codeBadFrame
	case errors.Is(err, kb.ErrNotHub):
		return codeNotHub
	}
	return codeHubFailed
}

// ---- the socket's writes ----

// phoneSocket is one audio socket's sending side: frames go out in order through one writer, which closes it after
// the last one; a phone that does not keep up is closed (it reads what it missed with GET …/live?from=).
type phoneSocket struct {
	c      *websocket.Conn
	out    chan phoneOut
	done   chan struct{} // closed once the writer ended (the socket is closed)
	mu     sync.Mutex
	closed bool // the close is queued: nothing goes after it
}

// phoneOut is one frame to the phone, or the close (status) after the frames before it.
type phoneOut struct {
	frame  []byte
	close  bool
	status websocket.StatusCode
}

func newPhoneSocket(c *websocket.Conn) *phoneSocket {
	p := &phoneSocket{c: c, out: make(chan phoneOut, audioSendQueue), done: make(chan struct{})}
	go p.writeLoop()
	return p
}

func (p *phoneSocket) writeLoop() {
	defer close(p.done)
	for out := range p.out {
		if out.close {
			_ = p.c.Close(out.status, "")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), jobWriteWait)
		err := p.c.Write(ctx, websocket.MessageText, out.frame)
		cancel()
		if err != nil {
			_ = p.c.CloseNow()
			return
		}
	}
}

// send queues frame v (nothing once the socket closes); a socket too far behind is closed.
func (p *phoneSocket) send(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	select {
	case p.out <- phoneOut{frame: raw}:
	default:
		p.closeLocked(websocket.StatusPolicyViolation)
	}
}

// fail sends the fatal error code, then closes the socket: 1008, or 1011 for the hub's own failure.
func (p *phoneSocket) fail(code string) {
	status := websocket.StatusPolicyViolation
	if code == codeHubFailed {
		status = websocket.StatusInternalError
	}
	p.send(map[string]any{"type": "error", "code": code, "fatal": true})
	p.closeWith(status)
}

// closeWith closes the socket with status once the frames queued before are out.
func (p *phoneSocket) closeWith(status websocket.StatusCode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeLocked(status)
}

func (p *phoneSocket) closeLocked(status websocket.StatusCode) {
	if p.closed {
		return
	}
	p.closed = true
	select {
	case p.out <- phoneOut{close: true, status: status}:
	default:
		_ = p.c.CloseNow()
	}
	close(p.out)
}

// wait ends the socket (when nothing closed it yet) and waits a little for its last frames.
func (p *phoneSocket) wait() {
	p.closeWith(websocket.StatusNormalClosure)
	select {
	case <-p.done:
	case <-time.After(jobWriteWait):
		_ = p.c.CloseNow()
	}
}

// keepAlive pings the phone every audioPingEvery and closes a socket that sent nothing, not even a ping or a pong,
// for audioSilentMost.
func (p *phoneSocket) keepAlive(heard *atomic.Int64) {
	t := time.NewTicker(min(audioPingEvery, audioSilentMost/2))
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, heard.Load())) >= audioSilentMost {
			_ = p.c.CloseNow()
			return
		}
		wait := audioPingEvery
		go func() { // the pong is read by the socket's reader
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			defer cancel()
			_ = p.c.Ping(ctx)
		}()
	}
}

// ---- PUT …/audio ----

// audioPut is PUT /api/v1/recordings/{id}/audio?at=: audio the hub lacks, uploaded after the meeting (PCM16 16 kHz
// mono, at most maxAudioBody) → the gaps still left. Refusals in the contract's order: 403, 503, 413 too_large, 410,
// 404, 400 (at missing or not 0 or more, a body that is not whole samples), 403 kb_read_only, 409 not_hub, 409
// transcript_expired, 409 transcription_final; 413 audio_full past the 12 hours a recording may hold.
func (s *Service) audioPut(w http.ResponseWriter, r *http.Request) {
	if !meetingCaller(w, r) {
		return
	}
	e := s.meetingEngine(w, r)
	if e == nil {
		return
	}
	raw, ok := meetingBody(w, r, maxAudioBody, "10 MiB")
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, _, ok := meetingRecording(w, e, id); !ok {
		return
	}
	at, ok := audioAt(r.URL.Query().Get("at"))
	if !ok || len(raw)%2 != 0 {
		apierr.WriteCode(w, http.StatusBadRequest, codeBadAudio, i18n.Err("assistant-meeting.bad_audio"))
		return
	}
	if !s.meetingWritable(r.Context(), e) {
		apierr.WriteCode(w, http.StatusForbidden, assistantwire.CodeKBReadOnly, i18n.Err("assistant-kb_routes.read_only"))
		return
	}
	gaps, err := e.AudioPut(id, at, raw)
	if gaps == nil {
		gaps = [][2]float64{}
	}
	switch {
	case errors.Is(err, kb.ErrAudioFull):
		body := apierr.LocalizedBody(http.StatusRequestEntityTooLarge, codeAudioFull, i18n.Err("assistant-meeting.audio_full"), i18n.OfWriter(w))
		body["gaps"] = gaps
		apierr.WriteBody(w, http.StatusRequestEntityTooLarge, body)
		return
	case err != nil:
		writeMeetingError(w, err)
		return
	}
	if len(gaps) == 0 {
		s.kickFinal() // no gap left: the final pass may start
	}
	apierr.WriteJSON(w, map[string]any{"gaps": gaps})
}

// audioAt is PUT …/audio's at: a decimal number of seconds, 0 or more.
func audioAt(v string) (float64, bool) {
	if v == "" || strings.Trim(v, "0123456789.") != "" || strings.Count(v, ".") > 1 || v == "." {
		return 0, false
	}
	at, err := strconv.ParseFloat(v, 64)
	return at, err == nil && finiteTime(at)
}
