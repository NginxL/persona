package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
)

// The read marks of a chat's answers (#13021, docs/protocol/protocol.md "Question reads"): the web's question rail draws
// a question unread until its answer was opened (on screen, or jumped to), and keeps the marks here so every device
// agrees. GET answers when marks began in the session and the questions marked; POST adds marks. A store without them
// (an older database, a unit test's) answers 404, and the client keeps its marks to itself.

// questionReadsPerPost bounds one POST's marks; questionReadMaxLen one mark (a message's entry id).
const (
	questionReadsPerPost = 200
	questionReadMaxLen   = 256
)

// questionReadStore is the part of the store that keeps the marks (db.DB).
type questionReadStore interface {
	QuestionReads(ctx context.Context, machine, session string) (time.Time, []string, error)
	MarkQuestionsRead(ctx context.Context, machine, session string, ids []string, at time.Time) error
}

var errNoQuestionReads = errors.New("this hub keeps no question read marks")

func (s *Service) questionReadStore() questionReadStore {
	if s.store == nil {
		return nil
	}
	st, _ := s.store().(questionReadStore)
	return st
}

// validQuestionReads are the marks of a POST body worth keeping: non-empty, bounded, each once.
func validQuestionReads(ids []string) ([]string, error) {
	if len(ids) > questionReadsPerPost {
		return nil, errors.New("too many ids")
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > questionReadMaxLen {
			return nil, errors.New("invalid id")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *Service) questionReadRoutes() {
	s.handle("GET /api/v1/sessions/{mid}/{sid}/question-reads", s.serveQuestionReads)
	s.handle("POST /api/v1/sessions/{mid}/{sid}/question-reads", s.serveMarkQuestionReads)
}

func (s *Service) serveQuestionReads(w http.ResponseWriter, r *http.Request) {
	m, ok := s.fleetMachine(r.PathValue("mid"))
	if !ok {
		apierr.Write(w, http.StatusNotFound, errors.New("unknown machine"))
		return
	}
	st := s.questionReadStore()
	if st == nil {
		apierr.Write(w, http.StatusNotFound, errNoQuestionReads)
		return
	}
	since, ids, err := st.QuestionReads(r.Context(), m.ID, r.PathValue("sid"))
	if err != nil {
		apierr.Call(w, err)
		return
	}
	if ids == nil {
		ids = []string{} // always a list
	}
	apierr.WriteJSON(w, map[string]any{"since": since.UTC(), "ids": ids})
}

func (s *Service) serveMarkQuestionReads(w http.ResponseWriter, r *http.Request) {
	m, ok := s.fleetMachine(r.PathValue("mid"))
	if !ok {
		apierr.Write(w, http.StatusNotFound, errors.New("unknown machine"))
		return
	}
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	ids, err := validQuestionReads(in.IDs)
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	st := s.questionReadStore()
	if st == nil {
		apierr.Write(w, http.StatusNotFound, errNoQuestionReads)
		return
	}
	if err := st.MarkQuestionsRead(r.Context(), m.ID, r.PathValue("sid"), ids, time.Now().UTC()); err != nil {
		apierr.Call(w, err)
		return
	}
	apierr.WriteJSON(w, map[string]any{"ok": true})
}
