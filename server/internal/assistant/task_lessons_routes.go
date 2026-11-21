package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/kb"
)

// The lessons of a project's page (#12295, docs/protocol/protocol.md "Project lessons"): the owner sees, corrects and
// deletes what accepted tasks taught (task_lessons.go). For the owner's apps only: the routes are not in
// extwire.RouteScopes (an extension's token is turned away at the hub) and a role without full access is refused here.
//
//	GET    /api/v1/kb/projects/{id}/lessons            {"enabled", "lessons": [...]}, the most repeated first, stale last
//	PUT    /api/v1/kb/projects/{id}/lessons/{lesson}   {"text"}: the owner's correction, a record that supersedes it
//	DELETE /api/v1/kb/projects/{id}/lessons/{lesson}   a tombstone: gone, and not learned again
//
// A lesson that was taught again or changed since the page read it answers 409 lesson_changed; one that is not there
// 404. Both answer with the lessons as they are now.

var (
	errLessonGone    = errors.New("no such lesson")
	errLessonChanged = errors.New("the lesson changed since it was read: look at it again")
	errLessonEmpty   = errors.New("a lesson needs text: delete it instead")
)

// lessonView is one lesson as the apps show it.
type lessonView struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Text        string         `json:"text"`
	Seen        int            `json:"seen"`
	FirstSeenAt string         `json:"first_seen_at,omitempty"`
	LastSeenAt  string         `json:"last_seen_at,omitempty"`
	Stale       bool           `json:"stale"`  // not taught again for lessonFresh: no longer handed out
	Edited      bool           `json:"edited"` // the owner corrected its text
	Sources     []lessonSource `json:"sources"`
}

// lessonSource is a to-do that taught a lesson, the newest first, with its title while the assistant still has it.
type lessonSource struct {
	TodoID string `json:"todo_id"`
	Title  string `json:"title,omitempty"`
}

// lessonsView is a project's lessons for its page: whether new ones are learned (the switch), and each live lesson,
// the fresh ones in lessonsInOrder, then the stale ones in the same order.
func (s *Service) lessonsView(r *http.Request, e *kb.Engine, slug string) map[string]any {
	now := time.Now()
	ordered := lessonsInOrder(e.ProjectLessons(slug).Live)
	var fresh, stale []lessonView
	for _, l := range ordered {
		v := lessonView{ID: l.ID, Kind: l.Kind, Text: l.Conclusion, Seen: max(l.Seen, 1), FirstSeenAt: l.FirstSeenAt,
			LastSeenAt: l.LastSeenAt, Stale: lessonStale(l, now), Edited: l.Verification == kb.ProjectMemoryOwner,
			Sources: []lessonSource{}}
		for i := len(l.Sources) - 1; i >= 0; i-- {
			src := lessonSource{TodoID: l.Sources[i]}
			if t, ok := s.todoNow(src.TodoID); ok {
				src.Title = oneLine(t.Title)
			}
			v.Sources = append(v.Sources, src)
		}
		if v.Stale {
			stale = append(stale, v)
		} else {
			fresh = append(fresh, v)
		}
	}
	list := append(fresh, stale...)
	if list == nil {
		list = []lessonView{}
	}
	return map[string]any{"enabled": s.taskLessons(r.Context()), "lessons": list}
}

func (s *Service) taskLessonRoutes(engine func(http.ResponseWriter, *http.Request) *kb.Engine) {
	project := func(w http.ResponseWriter, r *http.Request) (*kb.Engine, string, bool) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("a project's lessons are for the owner's apps"))
			return nil, "", false
		}
		e := engine(w, r)
		if e == nil {
			return nil, "", false
		}
		p := e.FindProject(r.PathValue("id"))
		if p == nil {
			apierr.Write(w, http.StatusNotFound, kb.ErrNotFound)
			return nil, "", false
		}
		return e, p.Slug, true
	}
	// change writes what plan decides of the lesson the path names, as it is now, and answers the lessons after it.
	change := func(w http.ResponseWriter, r *http.Request, plan func(old kb.ProjectMemoryRecord, now string) (kb.ProjectMemoryRecord, bool)) {
		e, slug, ok := project(w, r)
		if !ok {
			return
		}
		id := r.PathValue("lesson")
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err := e.UpdateProjectLessons(slug, func(cur kb.ProjectLessons) ([]kb.ProjectMemoryRecord, error) {
			old, live := cur.Find(id)
			switch {
			case live:
			case cur.IDs[id]:
				return nil, errLessonChanged
			default:
				return nil, errLessonGone
			}
			if rec, write := plan(old, now); write {
				return []kb.ProjectMemoryRecord{rec}, nil
			}
			return nil, nil
		})
		switch {
		case errors.Is(err, errLessonGone):
			apierr.Write(w, http.StatusNotFound, err)
		case errors.Is(err, errLessonChanged), errors.Is(err, kb.ErrLessonChanged):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			out := s.lessonsView(r, e, slug)
			out["error"], out["code"] = errLessonChanged.Error(), "lesson_changed"
			_ = json.NewEncoder(w).Encode(out)
		case errors.Is(err, kb.ErrReferenceOnly):
			apierr.Write(w, http.StatusConflict, err)
		case err != nil:
			apierr.Write(w, http.StatusInternalServerError, err)
		default:
			apierr.WriteJSON(w, s.lessonsView(r, e, slug))
		}
	}
	s.handle("GET /api/v1/kb/projects/{id}/lessons", func(w http.ResponseWriter, r *http.Request) {
		if e, slug, ok := project(w, r); ok {
			apierr.WriteJSON(w, s.lessonsView(r, e, slug))
		}
	})
	s.handle("PUT /api/v1/kb/projects/{id}/lessons/{lesson}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		text := clip(oneLine(in.Text), lessonTextMax)
		if strings.TrimSpace(text) == "" {
			apierr.Write(w, http.StatusBadRequest, errLessonEmpty)
			return
		}
		change(w, r, func(old kb.ProjectMemoryRecord, now string) (kb.ProjectMemoryRecord, bool) {
			if old.Conclusion == text {
				return old, false // unchanged: nothing to write
			}
			rec := old
			rec.ID, rec.SupersedesID, rec.Conclusion = "lesson-edit-"+randomHex(8), old.ID, text
			rec.Verification, rec.RecordedAt, rec.LastSeenAt = kb.ProjectMemoryOwner, now, now
			return rec, true
		})
	})
	s.handle("DELETE /api/v1/kb/projects/{id}/lessons/{lesson}", func(w http.ResponseWriter, r *http.Request) {
		change(w, r, func(old kb.ProjectMemoryRecord, now string) (kb.ProjectMemoryRecord, bool) {
			return kb.ProjectMemoryRecord{ID: "lesson-retired-" + randomHex(8), Type: kb.ProjectMemoryLessonRetired,
				SupersedesID: old.ID, Kind: old.Kind, Conclusion: old.Conclusion, TodoID: old.TodoID,
				Verification: kb.ProjectMemoryOwner, RecordedAt: now}, true
		})
	})
}
