package assistant

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// The apps' knowledge-base routes (docs/protocol/protocol.md "Knowledge base API", docs/assistant/knowledge.md "App API"): the hub relays them
// here unchanged (assistantwire.Routes), the same shapes as when the hub answered them.

var (
	errKBRouteOff     = i18n.Err("assistant-kb_routes.disabled")
	errKBRouteNotOpen = i18n.Err("assistant-kb_routes.not_open")
)

func (s *Service) kbRoutes() {
	kinds := map[string]bool{kb.KindProject: true, kb.KindChat: true, kb.KindPerson: true, kb.KindDoc: true, kb.KindMeeting: true, kb.KindEvent: true, kb.KindRecording: true, kb.KindTopic: true}
	engine := func(w http.ResponseWriter, r *http.Request) *kb.Engine {
		if r.Method != http.MethodGet && s.kbReadOnly(r.Context()) {
			apierr.WriteCode(w, http.StatusForbidden, assistantwire.CodeKBReadOnly, i18n.Err("assistant-kb_routes.read_only"))
			return nil
		}
		e, err := s.kbGet(r.Context())
		switch {
		case errors.Is(err, errKBOff):
			apierr.WriteCode(w, http.StatusServiceUnavailable, assistantwire.CodeKBDisabled, errKBRouteOff)
		case err != nil:
			apierr.WriteCode(w, http.StatusServiceUnavailable, assistantwire.CodeKBUnavailable, errKBRouteNotOpen)
		}
		return e
	}
	s.recordingRoutes(engine)
	s.kbSelectionRoutes()
	s.taskLessonRoutes(engine)
	s.handle("GET /api/v1/kb/{kind}", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		kind := r.PathValue("kind")
		if e == nil {
			return
		}
		if !kinds[kind] {
			apierr.Write(w, http.StatusNotFound, errors.New("unknown kind"))
			return
		}
		// The tags offered are this kind's (all of them, not only the filtered list's): a tag from another kind
		// filtered to nothing.
		all := e.List(kind, "")
		list := all
		if tag := r.URL.Query().Get("tag"); tag != "" {
			list = e.List(kind, tag)
		}
		if list == nil {
			list = []kb.Entry{}
		}
		apierr.WriteJSON(w, map[string]any{"items": list, "tags": kb.KindTags(all, 30)})
	})
	// One archived Moments post (a project's related Moments): what the crawler kept, text and comments as Markdown.
	s.handle("GET /api/v1/kb/moments/{id}", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		p, md, ok := e.Store.MomentArchived(r.PathValue("id"))
		if !ok {
			apierr.Write(w, http.StatusNotFound, kb.ErrNotFound)
			return
		}
		apierr.WriteJSON(w, map[string]any{"post": p, "markdown": md})
	})
	s.handle("GET /api/v1/kb/{kind}/{id}", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		d, err := e.Get(r.PathValue("kind"), r.PathValue("id"), 20000)
		if errors.Is(err, kb.ErrNotFound) {
			apierr.Write(w, http.StatusNotFound, err)
			return
		} else if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		out := map[string]any{"detail": d}
		// Opened from a search: every place the query is in it, so the page can start there.
		if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
			m := e.Matches(d.Kind, d.ID, q, 40)
			if m == nil {
				m = []kb.Match{}
			}
			out["matches"] = m
		}
		// A project's Agents: the live sessions working in its code directories.
		if d.Kind == kb.KindProject {
			if p, ok := e.Store.Project(d.ID); ok && len(p.Dirs) > 0 {
				var agents []any
				for _, m := range s.devices(r.Context()) {
					list, _ := s.sessions(r.Context(), m.ID)
					for _, sess := range list {
						if sess.Assistant || sess.Status == proto.StatusOffline {
							continue
						}
						for _, pp := range e.ProjectsForDir(m.ID, sess.Cwd) {
							if pp.Slug == p.Slug {
								agents = append(agents, sess)
								break
							}
						}
					}
				}
				out["agents"] = agents
			}
		}
		apierr.WriteJSON(w, out)
	})
	// What the knowledge base knows about one chat, for a prompt about it (a plugin's /agent): the chat's summary and
	// its first three projects' (the same the inbox's AI twin starts with, kbContext). Read only.
	s.handle("GET /api/v1/kb/chats/{id}/context", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		c, ok := e.Store.Chat(r.PathValue("id"))
		if !ok {
			apierr.Write(w, http.StatusNotFound, kb.ErrNotFound)
			return
		}
		kc := kbContext(e, c)
		projects := []map[string]string{}
		for _, p := range kc.Projects {
			projects = append(projects, map[string]string{"name": p.Name, "summary": p.Summary})
		}
		apierr.WriteJSON(w, map[string]any{"chat_id": kc.ChatID, "chat_name": kc.ChatName, "kind": c.Kind, "summary": kc.Summary, "projects": projects})
	})
	// One day of a chat's archive, for reading a found message in place.
	s.handle("GET /api/v1/kb/chats/{id}/days/{day}", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		msgs, prev, next, err := e.Day(r.PathValue("id"), r.PathValue("day"))
		if err != nil {
			apierr.Write(w, http.StatusNotFound, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"day": r.PathValue("day"), "messages": msgs, "prev": prev, "next": next})
	})
	s.handle("PUT /api/v1/kb/{kind}/{id}", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		var u kb.UserUpdate
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&u); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if (u.ProjectMode != nil || u.SourceURLs != nil) && !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-kb_routes.source_settings_forbidden"))
			return
		}
		ignored := kb.UserUnsupported(r.PathValue("kind"), u) // not applied: said, not dropped silently
		id, err := e.ApplyUser(r.PathValue("kind"), r.PathValue("id"), u)
		if errors.Is(err, kb.ErrNotFound) {
			apierr.Write(w, http.StatusNotFound, err)
			return
		} else if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		out := map[string]any{"id": id}
		if len(ignored) > 0 {
			out["ignored"] = ignored
		}
		apierr.WriteJSON(w, out)
	})
	// A project's soft delete and its Undo (#4412, docs/assistant/knowledge.md "Deleting a project").
	s.handle("DELETE /api/v1/kb/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		entry, err := e.DeleteProject(r.PathValue("id"), time.Now())
		if errors.Is(err, kb.ErrNotFound) {
			apierr.Write(w, http.StatusNotFound, err)
			return
		} else if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, entry)
	})
	s.handle("GET /api/v1/kb-trash", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		apierr.WriteJSON(w, map[string]any{"items": e.Trash()})
	})
	s.handle("POST /api/v1/kb-trash/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		entry, err := e.RestoreProject(r.PathValue("id"))
		switch {
		case errors.Is(err, kb.ErrNotFound):
			apierr.Write(w, http.StatusNotFound, err)
		case errors.Is(err, kb.ErrConflict):
			apierr.Write(w, http.StatusConflict, err)
		case err != nil:
			apierr.Write(w, http.StatusInternalServerError, err)
		default:
			apierr.WriteJSON(w, entry)
		}
	})
	s.handle("GET /api/v1/kb-search", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		// kind: one kind only, as kb_search takes it (#3862 phase 6: recordings for meetings); an older hub ignores it.
		q, kind := r.URL.Query().Get("q"), r.URL.Query().Get("kind")
		if kind != "" && !kinds[kind] {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-kb_routes.unknown_kind"))
			return
		}
		// results=1: entries ranked, a few lines each (the app since 2026-10-31); otherwise the lines as found.
		if r.URL.Query().Get("results") == "1" {
			var res []kb.Result
			if kind == "" {
				res = e.Find(q, 80)
			} else { // ranked over every kind (Find's order does not depend on its limit), then only this one's
				res = slices.DeleteFunc(e.Find(q, math.MaxInt), func(x kb.Result) bool { return x.Entry.Kind != kind })
				res = res[:min(len(res), 80)]
			}
			if res == nil {
				res = []kb.Result{}
			}
			apierr.WriteJSON(w, map[string]any{"results": res})
			return
		}
		messages := r.URL.Query().Get("messages") != "0"
		var hits []kb.Hit
		if kind == "" {
			hits = e.Search(q, messages, 60)
		} else {
			hits = e.SearchKind(q, kind, messages, 60)
		}
		if hits == nil {
			hits = []kb.Hit{}
		}
		apierr.WriteJSON(w, map[string]any{"hits": hits})
	})
}
