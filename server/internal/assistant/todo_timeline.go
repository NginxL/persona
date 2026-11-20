package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/todo/timeline"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// A to-do's timeline (#10465, docs/assistant/todo-status-sop.md "The to-do's timeline"): its conversation grouped into
// rounds, its results and what changed since the owner looked, worked out by the pure todo/timeline (its rules:
// spec/fixtures/todo-rounds) from the stores the manager already keeps. This file is the adapter: once per read it
// reads the to-do as GET /api/v1/todos/{id} lists it, the feed's items about it, its decisions, its runs' sessions and
// the owner's zone, and hands them over as timeline.Input. GET /api/v1/todos/{id}/timeline pages through the rounds;
// GET /api/v1/todos/{id} carries the ones the paper shows (todo_view_routes.go). Neither has a push of its own:
// {"type":"todos"} comes on a decision, a supervision or a scope change, and assistant_feed with every feed item,
// which clients match to the to-do by its ref. The manager names CapabilityTodoTimeline in its hello.
//
// An extension's token (todos.read) reads less (docs/extensions/extensions.md "Never shown to extensions"): nothing of
// a to-do whose history may hold the health space's words (todoHistoryHidden), and no answered cards, whose records
// hold what the owner typed into a card's fields and do not say whose card it was (a Just me card, a health run's).

const (
	// todoTimelineFeedCap is how many of a to-do's feed items a read takes, the newest: a to-do has a few hundred at
	// most, and the cap keeps a runaway one's read bounded. A to-do past it serves the rounds that began within them
	// (windowTimeline).
	todoTimelineFeedCap = 1000
	// todoTimelinePage is how many rounds GET …/timeline answers when it is not told, todoTimelineMaxPage at most.
	todoTimelinePage    = 20
	todoTimelineMaxPage = 100
)

func (s *Service) todoTimelineRoutes() {
	// GET /api/v1/todos/{id}/timeline?before=<round id>&limit=<n>: the to-do's conversation → assistantwire.TodoTimeline,
	// its latest limit rounds (todoTimelinePage when not given, at most todoTimelineMaxPage) before the round named
	// before (none: up to the latest), oldest first, with more while earlier ones are left. An unknown id 404
	// todo_not_found, and so does a to-do an extension is not shown the history of; a before that names no round (one
	// read from rows the stores no longer keep, or never one) 409 round_not_found, and the client reads the latest page
	// again; the to-dos or their history unreadable 503.
	s.handle("GET /api/v1/todos/{id}/timeline", func(w http.ResponseWriter, r *http.Request) {
		ctx, id, q := r.Context(), r.PathValue("id"), r.URL.Query()
		ext := r.Header.Get(RoleHeader) == roleExtension
		list, err := s.listedTodosOf(ctx, false, map[string]bool{id: true}) // loads the to-dos first
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		if len(list) == 0 {
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
			return
		}
		sessions := s.todoFactsOf(ctx, list)[0].Sessions
		list[0].Todo = s.withBrief(list[0].Todo) // as the item route lists it
		raw, err := json.Marshal(list[0])
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		d, hidden, err := s.todoTimelineOf(ctx, raw, sessions, ext)
		if err != nil {
			s.logf("todo %s: timeline: %v", id, err)
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		if hidden {
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
			return
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 {
			limit = todoTimelinePage
		}
		rounds, more, ok := timelinePage(d.Rounds, q.Get("before"), min(limit, todoTimelineMaxPage))
		if !ok {
			apierr.WriteCode(w, http.StatusConflict, "round_not_found", i18n.Err("assistant-todos.round_not_found"))
			return
		}
		apierr.WriteJSON(w, assistantwire.TodoTimeline{Rounds: rounds, More: more})
	})
}

// todoHistoryHidden: an extension's token is not shown to-do id's history (docs/extensions/extensions.md "Never shown
// to extensions") when it may hold the health space's words: a conversation it came from or was written in is a
// health conversation (its origin, its supervisor, the conversations of its feed items, conversations), or one of
// the runs it ever had works there, its link released, dismissed (which the apps' row leaves out) or done included.
// It is decided from the to-do as the manager keeps it, never only from what is live now (a done to-do's or a released
// link's session is not read for its view), and it fails closed as /interactions/history does (extensionHiddenCards):
// a conversation whose space cannot be told (not listed, and no deletion record keeps it), or a run whose link keeps
// no directory and whose session cannot be read, hides it, and so does a to-do no longer kept. sessions are its active
// runs' sessions as its view read them (todoFactsOf).
func (s *Service) todoHistoryHidden(ctx context.Context, id string, sessions []proto.Session, conversations []string) bool {
	t, ok := s.todoNow(id)
	if !ok {
		return true
	}
	read := make(map[[2]string]proto.Session, len(sessions))
	for _, c := range sessions {
		read[[2]string{c.MachineID, c.SessionID}] = c
	}
	unread := map[string]map[string]bool{} // by computer, the runs whose link keeps no directory and the view did not read
	for _, a := range t.Agents {
		if identity.InHealthSpace(a.Cwd) { // where the run started, as its link keeps it: its space for good
			return true
		}
		if c, ok := read[[2]string{a.MachineID, a.SessionID}]; ok {
			if healthRun(c) {
				return true
			}
		} else if a.Cwd == "" {
			if unread[a.MachineID] == nil {
				unread[a.MachineID] = map[string]bool{}
			}
			unread[a.MachineID][a.SessionID] = true
		}
	}
	told := map[string]bool{}
	for _, c := range append([]string{t.Origin, supervisorOf(t)}, conversations...) {
		if c == "" || told[c] {
			continue
		}
		told[c] = true
		if space, known := s.conversationSpace(ctx, c); !known || space == SpaceHealth {
			return true
		}
	}
	for machine, ids := range unread {
		list, err := s.todoRunSessions(ctx, machine, ids)
		if err != nil {
			return true
		}
		found := map[string]bool{}
		for _, c := range list {
			if ids[c.SessionID] {
				if healthRun(c) {
					return true
				}
				found[c.SessionID] = true
			}
		}
		if len(found) < len(ids) {
			return true
		}
	}
	return false
}

// healthRun: session c works in the health space, by its space or its directory (identity.InHealthSpace: this only
// hides, so a subfolder counts).
func healthRun(c proto.Session) bool {
	return spaceOf(c) == SpaceHealth || identity.InHealthSpace(c.Cwd)
}

// supervisorOf is the conversation that follows to-do t up ("" none).
func supervisorOf(t Todo) string {
	if t.Supervision == nil {
		return ""
	}
	return t.Supervision.Supervisor
}

// conversationSpace is the space of the conversation a to-do names, id ("" the assistant's own, as for no
// conversation), and whether it could be told. An assistant conversation's as the assistant's machine lists it
// (selfSpace), else as the record of its deletion keeps it; a task leader's agent (task_leader.go, whose workers' to-dos
// it opens and supervises) as its run's (leaderSpace). One neither knows (the node not connected yet, the call failed,
// the conversation no longer listed) is not told.
func (s *Service) conversationSpace(ctx context.Context, id string) (string, bool) {
	if machine, sid, ok := parseLeaderRef(id); ok && s.isLeaderSession(machine, sid) {
		return s.leaderSpace(ctx, s.canonical(machine), sid)
	}
	if space, ok := s.selfSpace(ctx, id); ok {
		return space, true
	}
	if row, ok, err := s.deletedConversation(ctx, s.settings(ctx).MachineID, id); err == nil && ok {
		return row.Space, true
	}
	return "", false
}

// leaderSpace is the space of task leader agent machine/sid, and whether it could be told: by the directory its link
// keeps, else as its computer lists it.
func (s *Service) leaderSpace(ctx context.Context, machine, sid string) (string, bool) {
	s.todos.mu.RLock()
	cwd := ""
	for _, t := range s.todos.todos {
		for _, a := range t.Agents {
			if a.MachineID == machine && a.SessionID == sid && a.Cwd != "" {
				cwd = a.Cwd
			}
		}
	}
	s.todos.mu.RUnlock()
	if identity.InHealthSpace(cwd) { // a subfolder counts: this only hides
		return SpaceHealth, true
	} else if cwd != "" {
		return "", true
	}
	list, err := s.todoRunSessions(ctx, machine, map[string]bool{sid: true})
	if err != nil {
		return "", false
	}
	for _, c := range list {
		if c.SessionID == sid {
			if healthRun(c) {
				return SpaceHealth, true
			}
			return "", true
		}
	}
	return "", false
}

// timelinePage is the page GET …/timeline answers of rounds (oldest first): the latest limit before the round whose id
// is before ("": up to the latest), in the same order, and whether earlier ones are left; ok false when no round has
// that id. The page is never nil, so an empty one writes [].
func timelinePage(rounds []assistantwire.TodoRound, before string, limit int) (page []assistantwire.TodoRound, more, ok bool) {
	end := len(rounds)
	if before != "" {
		if end = slices.IndexFunc(rounds, func(r assistantwire.TodoRound) bool { return r.ID == before }); end < 0 {
			return nil, false, false
		}
	}
	start := max(0, end-limit)
	return append([]assistantwire.TodoRound{}, rounds[start:end]...), start > 0, true
}

// todoItemDetail is what GET /api/v1/todos/{id} answers beside to-do t's row raw, whose active runs' sessions are
// sessions (assistantwire.TodoItemDetail): the rounds the paper shows (timeline.Latest), how many there are in all, its
// results and what changed since the owner looked; for an extension (ext) as todoTimelineOf reads it. Nothing when its
// history cannot be read, or is not an extension's to see (todoHistoryHidden): the row still answers, and the paper
// draws what it has as from an older manager.
func (s *Service) todoItemDetail(ctx context.Context, t Todo, raw json.RawMessage, sessions []proto.Session, ext bool) assistantwire.TodoItemDetail {
	d, hidden, err := s.todoTimelineOf(ctx, raw, sessions, ext)
	if err != nil {
		s.logf("todo %s: timeline: %v", t.ID, err)
		return assistantwire.TodoItemDetail{}
	}
	if hidden {
		return assistantwire.TodoItemDetail{}
	}
	total, rounds := len(d.Rounds), timeline.Latest(d.Rounds, timeline.PaperRounds)
	if rounds == nil {
		rounds = []assistantwire.TodoRound{} // a to-do with no rounds yet: [], told apart from an older manager's none
	}
	return assistantwire.TodoItemDetail{Rounds: rounds, RoundsTotal: &total, Recap: d.Recap, Results: d.Results}
}

// todoTimelineOf works out the timeline of the to-do whose row is raw, as GET /api/v1/todos/{id} lists it (with its
// brief and lineage; its view, when there, is not read), with sessions its active runs' sessions as its view reads them
// (todoFactsOf). For an extension (ext) it leaves out the answered cards, and is hidden, with no timeline, when the
// to-do's history is not an extension's to see (todoHistoryHidden, which also reads the conversations its feed items
// were written in). The row is read from its JSON, as the shared cases give it, so the timeline sees exactly what the
// apps do: an open ask's id is formed from asked_at as listed, as the view forms it. An error when the feed's items
// about it or its decisions cannot be read.
func (s *Service) todoTimelineOf(ctx context.Context, raw json.RawMessage, sessions []proto.Session, ext bool) (d timeline.Derived, hidden bool, err error) {
	in := timeline.Input{Sessions: sessions, Loc: s.location(ctx)}
	if err := json.Unmarshal(raw, &in.Todo); err != nil {
		return timeline.Derived{}, false, err
	}
	var conversations []string
	if in.Feed, conversations, err = s.todoFeed(ctx, in.Todo.ID, ext); err != nil {
		return timeline.Derived{}, false, err
	}
	if ext && s.todoHistoryHidden(ctx, in.Todo.ID, sessions, conversations) {
		return timeline.Derived{}, true, nil
	}
	if in.Decisions, err = s.todoTimelineDecisions(ctx, in.Todo.ID); err != nil {
		return timeline.Derived{}, false, err
	}
	in.Machines = s.todoMachineNames(ctx, in.Feed)
	if len(in.Feed) < todoTimelineFeedCap {
		return timeline.Derive(in), false, nil
	}
	return windowTimeline(in), false, nil
}

// windowTimeline is the timeline of a to-do whose feed items in were cut at todoTimelineFeedCap: the rounds that began
// at or after the oldest item kept, worked out only from what is no older than that item (its decisions, brief log and
// ledger reviews are cut at the same time as its feed), so that every round served is whole and a round read again
// after newer items is the same round. Earlier rounds are not served, so rounds_total and more count from there, and
// a recap that began in a round left out starts at the first one served.
func windowTimeline(in timeline.Input) timeline.Derived {
	cut := in.Feed[0].At
	for _, f := range in.Feed {
		if f.At.Before(cut) {
			cut = f.At
		}
	}
	in.Decisions = slices.DeleteFunc(in.Decisions, func(d timeline.Decision) bool { return d.CreatedAt.Before(cut) })
	if b := in.Todo.Brief; b != nil {
		b.Log = slices.DeleteFunc(b.Log, func(e timeline.BriefEvent) bool { return e.At.Before(cut) })
	}
	if v := in.Todo.Supervision; v != nil {
		v.Reviews = slices.DeleteFunc(v.Reviews, func(r timeline.Review) bool { return r.At.Before(cut) })
	}
	d := timeline.Derive(in)
	d.Rounds = slices.DeleteFunc(d.Rounds, func(r assistantwire.TodoRound) bool { return r.At.Before(cut) })
	if d.Recap != nil && !slices.ContainsFunc(d.Rounds, func(r assistantwire.TodoRound) bool { return r.ID == d.Recap.Round }) {
		if len(d.Rounds) == 0 {
			d.Recap = nil
		} else {
			d.Recap.Round = d.Rounds[0].ID
		}
	}
	return d
}

// todoFeed are the feed's items about to-do id of the kinds the timeline reads, in every conversation (the origin's,
// a supervisor's, "" for a hand-written to-do's relays), the newest todoTimelineFeedCap of them, newest first: from
// the store (FeedByRef), else from memory; and the conversations they were written in, each once. For an extension
// (ext) without the answered cards.
func (s *Service) todoFeed(ctx context.Context, id string, ext bool) ([]timeline.FeedItem, []string, error) {
	kinds := timeline.FeedKinds()
	if ext {
		kinds = slices.DeleteFunc(kinds, func(k string) bool { return k == feedAnswered })
	}
	var items []FeedItem
	if st := s.store(); st != nil {
		rows, err := st.FeedByRef(ctx, id, kinds, todoTimelineFeedCap)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			items = append(items, FeedItem{ID: r.ID, Conversation: r.Conversation, At: r.At, Kind: r.Kind, Body: r.Body})
		}
	} else {
		s.tasks.mu.Lock()
		for _, list := range s.tasks.feed {
			for _, it := range list {
				if it.Ref == id && slices.Contains(kinds, it.Kind) {
					items = append(items, it)
				}
			}
		}
		s.tasks.mu.Unlock()
		slices.SortFunc(items, func(a, b FeedItem) int { return cmp.Or(b.At.Compare(a.At), strings.Compare(b.ID, a.ID)) })
		items = items[:min(len(items), todoTimelineFeedCap)]
	}
	out := make([]timeline.FeedItem, len(items))
	var conversations []string
	for i, it := range items {
		out[i] = timeline.FeedItem{ID: it.ID, At: it.At, Kind: it.Kind, Body: it.Body}
		if !slices.Contains(conversations, it.Conversation) {
			conversations = append(conversations, it.Conversation)
		}
	}
	return out, conversations, nil
}

// todoTimelineDecisions are to-do id's decisions, superseded ones included; none without the hub's database, which
// alone keeps them.
func (s *Service) todoTimelineDecisions(ctx context.Context, id string) ([]timeline.Decision, error) {
	st := s.todoDB()
	if st == nil {
		return nil, nil
	}
	rows, err := st.ListTodoDecisions(ctx, id, true)
	if err != nil {
		return nil, err
	}
	out := make([]timeline.Decision, len(rows))
	for i, d := range rows {
		out[i] = timeline.Decision{ID: d.ID, Question: d.Question, Choice: d.Choice, Reason: d.Reason, Source: d.Source,
			SourceMachine: d.SourceMachine, SourceSession: d.SourceSession, SourceMessage: d.SourceMessage,
			SupersededBy: d.SupersededBy, CreatedAt: d.CreatedAt}
	}
	return out, nil
}

// todoMachineNames are the computers' names by id, which a run's receipt written before #10465 names its computer by
// (as the hub names it, finishTurn); nil when every turn's receipt in feed names its run, as the timeline reads it
// (both ids), so only an older history needs them. From the fleet this process carries, else asked of the hub.
func (s *Service) todoMachineNames(ctx context.Context, feed []timeline.FeedItem) map[string]string {
	older := slices.ContainsFunc(feed, func(f timeline.FeedItem) bool {
		var b struct {
			State     string `json:"state"`
			MachineID string `json:"machine_id"`
			SessionID string `json:"session_id"`
		}
		return f.Kind == feedReceipt && json.Unmarshal(f.Body, &b) == nil && b.State != "failed" &&
			(b.MachineID == "" || b.SessionID == "")
	})
	if !older {
		return nil
	}
	names := map[string]string{}
	if s.fleetOn() {
		for _, m := range s.fleetMachines() {
			names[m.ID] = m.Name
		}
		return names
	}
	for _, d := range s.devices(ctx) {
		names[d.ID] = d.Name
	}
	return names
}
