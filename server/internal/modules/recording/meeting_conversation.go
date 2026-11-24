package recording

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The meeting conversation's routes (docs/assistant/meeting-minutes.md "Phase 1: the meeting conversation"): the
// private mark (PUT …/private), Just me (POST …/just-me, meeting_justme.go) and the follow-up (POST …/followup,
// meeting_followup.go). Full credentials only, as …/live and …/ask; their errors in the contract's order. Just me and
// the follow-up keep a receipt in client_ops derived from the body's id, at the hub (hub/meeting_receipts.go): a
// repeat of a call gets its answer without anything sent again.

// The bodies these routes read at most.
const (
	maxPrivateBody      = 1 << 10
	maxMeetingSendBody  = 32 << 20
	maxMeetingTextRunes = 8000
	maxMeetingImages    = 4
	maxMeetingImage     = 3 << 20
)

func (s *Recordings) meetingConversationRoutes() {
	s.handle("PUT /api/v1/recordings/{id}/private", func(w http.ResponseWriter, r *http.Request) {
		if !meetingCaller(w, r) {
			return
		}
		e := s.meetingEngine(w, r)
		if e == nil {
			return
		}
		raw, ok := meetingBody(w, r, maxPrivateBody, "1 KiB")
		if !ok {
			return
		}
		id := r.PathValue("id")
		if _, _, ok := meetingRecording(w, e, id); !ok {
			return
		}
		var in struct {
			Private json.RawMessage `json:"private"`
		}
		var private bool
		// null is not a boolean: read as false it would make the meeting material for later ones.
		if json.Unmarshal(raw, &in) != nil || !given(in.Private) || json.Unmarshal(in.Private, &private) != nil {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_private"))
			return
		}
		if !s.meetingWritable(r.Context(), e) {
			apierr.WriteCode(w, http.StatusForbidden, assistantwire.CodeKBReadOnly, i18n.Err("assistant-kb_routes.read_only"))
			return
		}
		if _, err := e.SetPrivate(id, private); err != nil {
			if errors.Is(err, kb.ErrNotMeeting) {
				apierr.WriteCode(w, http.StatusConflict, codeNotMeeting, i18n.Err("assistant-meeting.not_meeting"))
				return
			}
			writeMeetingError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"private": private})
	})
	s.handle("POST /api/v1/recordings/{id}/just-me", s.justMeRoute)
	s.handle("POST /api/v1/recordings/{id}/followup", s.followupRoute)
}

// meetingMessage is a Just me or follow-up body: the call's id (lowercase), the owner's text and images.
type meetingMessage struct {
	ID     string
	Text   string
	Images []proto.Image
}

// parseMeetingMessage reads a Just me or follow-up body: 400 for an id that is not a UUID, neither text nor images, a
// text over maxMeetingTextRunes, more than maxMeetingImages images, or one over maxMeetingImage or not base64.
func parseMeetingMessage(raw []byte) (meetingMessage, error) {
	var in struct {
		ID     json.RawMessage `json:"id"`
		Text   json.RawMessage `json:"text"`
		Images json.RawMessage `json:"images"`
	}
	var m meetingMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		return m, i18n.Err("assistant-meeting.bad_json", i18n.Args{"error": err.Error()})
	}
	var id string
	if json.Unmarshal(in.ID, &id) != nil {
		return m, i18n.Err("assistant-meeting.bad_ask_id")
	}
	var ok bool
	if m.ID, ok = kb.RecordingID(id); !ok {
		return m, i18n.Err("assistant-meeting.bad_ask_id")
	}
	bad := func(field string) error {
		return i18n.Err("assistant-meeting.bad_ask_field", i18n.Args{"field": field})
	}
	if given(in.Text) && json.Unmarshal(in.Text, &m.Text) != nil {
		return m, bad("text")
	}
	m.Text = strings.TrimSpace(m.Text)
	if utf8.RuneCountInString(m.Text) > maxMeetingTextRunes {
		return m, i18n.Err("assistant-meeting.text_too_long", i18n.Args{"max": maxMeetingTextRunes})
	}
	if given(in.Images) {
		var imgs []struct {
			Mime string `json:"mime"`
			Data string `json:"data"`
		}
		if json.Unmarshal(in.Images, &imgs) != nil {
			return m, bad("images")
		}
		if len(imgs) > maxMeetingImages {
			return m, i18n.Err("assistant-meeting.too_many_images", i18n.Args{"max": maxMeetingImages})
		}
		for _, img := range imgs {
			n, err := base64.StdEncoding.DecodeString(img.Data)
			if err != nil || len(n) == 0 || !strings.HasPrefix(img.Mime, "image/") {
				return m, bad("images")
			}
			if len(n) > maxMeetingImage {
				return m, i18n.Err("assistant-meeting.image_too_large", i18n.Args{"size": "3 MiB"})
			}
			m.Images = append(m.Images, proto.Image{Mime: img.Mime, Data: img.Data})
		}
	}
	if m.Text == "" && len(m.Images) == 0 {
		return m, i18n.Err("assistant-meeting.no_text")
	}
	return m, nil
}

// writeSendError answers a send's failure as POST /api/v1/sessions/{mid}/{sid}/messages would: a refusal the
// manager knows (bad request, offline) with its status, anything else as the node's error.
func writeSendError(w http.ResponseWriter, err error) {
	var re *rpc.Error
	switch {
	case errors.As(err, &re) && re.Code == CodeBadRequest:
		apierr.Write(w, http.StatusBadRequest, errors.New(re.Message))
	case errors.Is(err, ErrNoDevice), offlineErr(err):
		apierr.WriteCode(w, http.StatusServiceUnavailable, CodeMachineOffline, err)
	default:
		apierr.Write(w, http.StatusBadGateway, err)
	}
}

// meetingLocks serialize what makes a meeting's conversations, per recording (the follow-up's creation).
type meetingLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (l *meetingLocks) lock(id string) func() {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*sync.Mutex{}
	}
	mu := l.m[id]
	if mu == nil {
		mu = &sync.Mutex{}
		l.m[id] = mu
	}
	l.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// timeInMeeting is how long recording r had been running at now, as a clock (m:ss, h:mm:ss).
func timeInMeeting(r kb.Recording, now time.Time) string {
	return meetingClock(now.Sub(r.StartedAt).Seconds())
}

// meetingSendCtx is a context for a send that carries on when the phone stops waiting.
func meetingSendCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
}
