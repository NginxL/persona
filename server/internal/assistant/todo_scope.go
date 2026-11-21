package assistant

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/todo/timeline"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// What an extension's token (todos.read, todos.write) is shown of the to-dos (#10941, docs/extensions/extensions.md
// "Never shown to extensions"): nothing of one whose history may hold the health space's words, as of a chat of the
// space. One rule (todosHidden) decides every way a to-do leaves the manager for an extension: GET /api/v1/todos leaves
// it out (extensionRows), every route of one to-do (/api/v1/todos/{id}…) answers 404 as for a to-do that does not
// exist, a write as a read (todoScoped), and neither todo.changed nor todo.decision is sent of it (todoChangedFlush,
// todoDecisionVerdict); one whose verdict cannot be told yet (todoUntold) waits for it instead. A to-do is hidden
// with any of its steps, whose asks and line its row's view carries, and with any to-do on its brief, whose log and
// lineage its row carries, so every row an extension is shown is a device's, byte for byte; the list's counts are a
// device's. Nor can an extension take a run in the space, or one off a to-do it is not shown, onto one
// of its own (todoTakeRefused), and what told a to-do's history stays: a link keeps a directory in the space
// (writeTodoAgentLink), and a space's unlink, move or void releases such a link rather than delete it, in the same
// write (healthLinksOf).

// feedConversationStore reads the conversations of every to-do's history in one query (db.FeedConversations).
type feedConversationStore interface {
	FeedConversations(ctx context.Context, refs, kinds []string) (map[string][]string, error)
}

// The database tells it: a store that is it but lacked it would hide every to-do from an extension.
var _ feedConversationStore = (*db.DB)(nil)

var errNoFeedConversations = errors.New("this store cannot tell the conversations of a to-do's history")

// todoNoticeTimeout bounds the reads that decide whether events are told (todoChangedFlush, todoDecisionVerdict).
const todoNoticeTimeout = 10 * time.Second

// todoTakeRetrySeconds is the Retry-After of an extension's take of a run its computer does not list yet: a computer
// lists a session it just started within a few seconds.
const todoTakeRetrySeconds = 2

// todoEvidence is what of one to-do todosHidden decides by, copied under the to-dos' lock.
type todoEvidence struct {
	conversations []string    // its origin and its supervisor
	links         []TodoAgent // every run it ever had: released, dismissed and done included
	open          bool        // its view reads its active runs (todoFactsOf): it is open, or its start failed
}

func evidenceOf(t *Todo) *todoEvidence {
	e := &todoEvidence{conversations: []string{t.Origin}, links: slices.Clone(t.Agents), open: t.DoneAt == nil || t.StartError != ""}
	if t.Supervision != nil {
		e.conversations = append(e.conversations, t.Supervision.Supervisor)
	}
	return e
}

// todoVerdict is whether an extension's token is shown a to-do (todoVerdicts).
type todoVerdict uint8

const (
	// todoShown: nothing of its history is in the health space.
	todoShown todoVerdict = iota
	// todoUntold: nothing says its history is in the space, but a conversation or a run of it cannot be told now (not
	// listed, never noted, unreadable). An extension is not shown it, and what would tell it of the to-do waits for
	// the verdict to be told (todoChangedFlush, todoSendEvents), its task's outcome line too (outcomeLog); a
	// conversation never noted is listed again for it to be (spaceBackfillWant).
	todoUntold
	// todoHidden: its history holds, or may hold, the space's words for good (a conversation or a run of the space, a
	// step or a to-do on its brief that does), or it is no longer kept to be told.
	todoHidden
)

// todosHidden are those of to-dos ids an extension's token is not shown, by id (other to-dos it had to decide too):
// todoVerdicts' that are not todoShown.
func (s *Service) todosHidden(ctx context.Context, ids []string, read map[[2]string]proto.Session) map[string]bool {
	v := s.todoVerdicts(ctx, ids, nil, read)
	hidden := make(map[string]bool, len(v))
	for id, verdict := range v {
		hidden[id] = verdict != todoShown
	}
	return hidden
}

// todoVerdicts are whether an extension's token is shown each of to-dos ids, by id (other to-dos it had to decide
// too), with gone the to-dos no longer kept as they last were (a deletion's todo.changed, a closed task's outcome). A
// to-do's own history may hold the health space's words when a conversation it came from or was written in is a
// health conversation (its origin, its supervisor, those of the feed's items about it, todoFeedConversations), or one
// of the runs it ever had works there, its link released, dismissed (which the apps' row leaves out) or done included;
// and a to-do is hidden with any of its steps and any to-do on its brief (above). It is decided from the to-dos as the
// manager keeps them, never only from what is live now, and it fails closed as /interactions/history does
// (extensionHiddenCards): a conversation whose space cannot be told (conversationSpaces: not listed, never noted, no
// deletion record), a history whose conversations cannot be read, or a run whose link keeps no directory and whose
// session cannot be read, makes it todoUntold, and a to-do asked about that is no longer kept todoHidden. read are
// sessions already read of the active runs of the open to-dos (todoFactsOf: a list's view), by computer and id; the
// others its views would read are read in one call a computer, so the verdict is the same whichever route asks, and
// the conversations are told at once.
func (s *Service) todoVerdicts(ctx context.Context, ids []string, gone map[string]Todo, read map[[2]string]proto.Session) map[string]todoVerdict {
	briefs := s.briefLinksNow()
	asked := make(map[string]bool, len(ids))
	for _, id := range ids {
		asked[id] = true
	}
	// The to-dos to decide: those asked about, and to each its steps and the to-dos on its brief, until none is new.
	ev := map[string]*todoEvidence{} // nil: one not kept
	steps := map[string][]string{}   // by the to-do they are steps of
	s.todos.mu.RLock()
	for id, t := range s.todos.todos {
		if t.PartOf != "" {
			steps[t.PartOf] = append(steps[t.PartOf], id)
		}
	}
	for queue := slices.Clone(ids); len(queue) > 0; queue = queue[1:] {
		id := queue[0]
		if _, ok := ev[id]; ok {
			continue
		}
		if t, ok := s.todos.todos[id]; ok {
			ev[id] = evidenceOf(t)
		} else if t, ok := gone[id]; ok {
			ev[id] = evidenceOf(&t)
		} else {
			ev[id] = nil
			continue
		}
		queue = append(queue, steps[id]...)
		for mate := range briefs.on[briefs.of[id]] {
			queue = append(queue, mate)
		}
	}
	s.todos.mu.RUnlock()
	decided := make([]string, 0, len(ev))
	for id := range ev {
		decided = append(decided, id)
	}
	slices.Sort(decided)

	// own: what each to-do's own history says, one for every to-do decided (a caller tells one decided from one it
	// did not ask about). A brief's to-do since deleted says nothing; one asked about is hidden.
	own := make(map[string]todoVerdict, len(ev))
	raise := func(id string, v todoVerdict) { own[id] = max(own[id], v) }
	for _, id := range decided {
		own[id] = todoShown
		e := ev[id]
		if e == nil {
			if asked[id] {
				own[id] = todoHidden
			}
			continue
		}
		// Where a run started, as its link keeps it, is its space for good.
		if slices.ContainsFunc(e.links, func(a TodoAgent) bool {
			c, seen := read[[2]string{a.MachineID, a.SessionID}]
			return identity.InHealthSpace(a.Cwd) || seen && healthRun(c)
		}) {
			own[id] = todoHidden
		}
	}
	// open are the to-dos nothing has hidden yet: those that are only untold are read on, for what would hide them.
	open := func() []string {
		return slices.DeleteFunc(slices.Clone(decided), func(id string) bool { return ev[id] == nil || own[id] == todoHidden })
	}
	// Then the conversations they came from and those of their history.
	if todos := open(); len(todos) > 0 {
		feed, err := s.todoFeedConversations(ctx, todos)
		if err != nil {
			s.logf("todos: the conversations of their history: %v", err)
		}
		var conversations []string
		for _, id := range todos {
			conversations = append(conversations, ev[id].conversations...)
			conversations = append(conversations, feed[id]...)
		}
		bad := s.unshownConversations(ctx, conversations)
		var untold []string
		for c, health := range bad {
			if !health {
				untold = append(untold, c)
			}
		}
		slices.Sort(untold)
		s.spaceBackfillWant(untold) // for their computers to list them, so they are noted (conversation_space_backfill.go)
		for _, id := range todos {
			if err != nil {
				raise(id, todoUntold)
			}
			for _, c := range append(slices.Clone(ev[id].conversations), feed[id]...) {
				if health, unshown := bad[c]; health {
					raise(id, todoHidden)
				} else if unshown {
					raise(id, todoUntold)
				}
			}
		}
	}
	// Last their runs, each computer asked once: the active runs of the open ones the read did not read, and every run
	// whose link keeps no directory. One whose link keeps a directory outside the space is told by it when its computer
	// does not list it; one that keeps none cannot be told then.
	todos, want := open(), map[string]map[string]bool{}
	for _, id := range todos {
		for _, a := range ev[id].links {
			if _, seen := read[[2]string{a.MachineID, a.SessionID}]; !seen && (a.Cwd == "" || ev[id].open && a.active()) {
				if want[a.MachineID] == nil {
					want[a.MachineID] = map[string]bool{}
				}
				want[a.MachineID][a.SessionID] = true
			}
		}
	}
	listed := map[[2]string]proto.Session{}
	for machine, sids := range want {
		list, err := s.todoRunSessions(ctx, machine, sids)
		if err != nil {
			continue
		}
		for _, c := range list {
			if sids[c.SessionID] {
				listed[[2]string{machine, c.SessionID}] = c
			}
		}
	}
	for _, id := range todos {
		for _, a := range ev[id].links {
			run := [2]string{a.MachineID, a.SessionID}
			if _, seen := read[run]; seen {
				continue // told above
			}
			switch c, ok := listed[run]; {
			case ok && healthRun(c):
				raise(id, todoHidden)
			case !ok && a.Cwd == "":
				raise(id, todoUntold)
			}
		}
	}

	// A to-do is hidden with its steps and the to-dos on its brief, as each of them is.
	verdicts := maps.Clone(own)
	for changed := true; changed; {
		changed = false
		for _, id := range decided {
			if ev[id] == nil {
				continue
			}
			v := verdicts[id]
			for _, c := range steps[id] {
				v = max(v, verdicts[c])
			}
			for mate := range briefs.on[briefs.of[id]] {
				v = max(v, verdicts[mate])
			}
			if v != verdicts[id] {
				verdicts[id], changed = v, true
			}
		}
	}
	return verdicts
}

// unshownConversations are those of conversations (a to-do's origin, supervisor or history; "" the assistant's own)
// that are health conversations (true) or whose space cannot be told (false), as conversationSpaces tells them all at
// once; those of the assistant's own space are left out.
func (s *Service) unshownConversations(ctx context.Context, conversations []string) map[string]bool {
	out := map[string]bool{}
	for c, t := range s.conversationSpaces(ctx, conversations) {
		if !t.known || t.space == SpaceHealth {
			out[c] = t.known
		}
	}
	return out
}

// todoFeedConversations are the conversations the feed's items about each of to-dos ids were written in, by to-do
// (every item of the kinds the timeline reads, not only the newest todoTimelineFeedCap): from the store in one query,
// else from memory. An error when the store cannot tell.
func (s *Service) todoFeedConversations(ctx context.Context, ids []string) (map[string][]string, error) {
	kinds := timeline.FeedKinds()
	if st := s.store(); st != nil {
		fc, ok := st.(feedConversationStore)
		if !ok {
			return nil, errNoFeedConversations
		}
		return fc.FeedConversations(ctx, ids, kinds)
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := map[string][]string{}
	s.tasks.mu.Lock()
	for _, list := range s.tasks.feed {
		for _, it := range list {
			if want[it.Ref] && slices.Contains(kinds, it.Kind) && !slices.Contains(out[it.Ref], it.Conversation) {
				out[it.Ref] = append(out[it.Ref], it.Conversation)
			}
		}
	}
	s.tasks.mu.Unlock()
	return out, nil
}

// extensionRows are the rows of list (GET /api/v1/todos, p its projection) an extension's token is shown: those of
// the to-dos todosHidden does not hide, each as a device reads it. The views' sessions are not read again.
func (s *Service) extensionRows(ctx context.Context, list []listedTodo, p todoProjection) []listedTodo {
	read := map[[2]string]proto.Session{}
	for _, f := range p.facts {
		for _, c := range f.Sessions {
			read[[2]string{c.MachineID, c.SessionID}] = c
		}
	}
	ids := make([]string, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	hidden := s.todosHidden(ctx, ids, read)
	out := make([]listedTodo, 0, len(list))
	for _, t := range list {
		if !hidden[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

// todoScoped is route fn of one to-do (/api/v1/todos/{id}…) as an extension's token reaches it: a to-do it is not
// shown answers 404 as one that does not exist, a write as a read, before the route reads or changes anything (a
// health run's title echoed by a write, a link that told its history taken off). s.handle puts every such route
// behind it, so a route added later is too.
func (s *Service) todoScoped(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(RoleHeader) == roleExtension {
			ctx, id := r.Context(), r.PathValue("id")
			if err := s.ensureTodosLoaded(ctx); err != nil {
				apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
				return
			}
			s.todos.mu.RLock()
			_, kept := s.todos.todos[id]
			s.todos.mu.RUnlock()
			if kept && s.todosHidden(ctx, []string{id}, nil)[id] {
				apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
				return
			}
		}
		fn(w, r)
	}
}

// todoDecisionVerdict is todoVerdicts' of the todo event n is a todo.decision of (todoShown for any other event): it
// goes only to extensions granted todos.read, so it is not sent of a todo they are not shown, and waits while that
// cannot be told. The queue's worker asks it, off the write that made the decision, and holds one whose todo has a
// decision held already, so a todo's decisions keep their order; todoChangedFlush judges the todo.changed it tells the
// same way.
func (s *Service) todoDecisionVerdict(n todoNotice) todoVerdict {
	p, ok := n.params.(assistantwire.TodoDecisionEvent)
	if !ok {
		return todoShown
	}
	e := &s.tevents
	e.qmu.Lock()
	behind := slices.ContainsFunc(e.held, func(h todoNotice) bool {
		d, ok := h.params.(assistantwire.TodoDecisionEvent)
		return ok && d.ID == p.ID
	})
	e.qmu.Unlock()
	if behind {
		return todoUntold
	}
	ctx, cancel := context.WithTimeout(s.runCtx(), todoNoticeTimeout)
	defer cancel()
	return s.todoVerdicts(ctx, []string{p.ID}, nil, nil)[p.ID]
}

// todoTakeRefused says whether an extension's token may not take run c of computer mid onto a to-do (PUT
// …/agents/{mid}/{sid}, and each run a PUT …/spaces brings): one in the health space, as /sessions answers 404 for a
// chat of the space, and one linked to a to-do it is not shown, since the link would move the run off it.
func (s *Service) todoTakeRefused(ctx context.Context, mid string, c proto.Session) bool {
	if healthRun(c) {
		return true
	}
	var holders []string
	s.todos.mu.RLock()
	for id, t := range s.todos.todos {
		if slices.ContainsFunc(t.Agents, func(a TodoAgent) bool {
			return a.MachineID == mid && a.SessionID == c.SessionID && a.ReleasedAt == nil
		}) {
			holders = append(holders, id)
		}
	}
	s.todos.mu.RUnlock()
	hidden := s.todosHidden(ctx, holders, nil)
	return slices.ContainsFunc(holders, func(id string) bool { return hidden[id] })
}

// spaceTakeRefused says whether an extension's token may not link space mid/space to to-do id: the space is another
// to-do's that it is not shown (the link would take it and its runs off that one), or a run in it now is one
// todoTakeRefused refuses.
func (s *Service) spaceTakeRefused(ctx context.Context, id, mid, space string) bool {
	key := spaceKey(mid, space)
	s.todos.mu.RLock()
	holder := s.todos.spacesByKey[key]
	s.todos.mu.RUnlock()
	if holder != "" && holder != id && s.todosHidden(ctx, []string{holder}, nil)[holder] {
		return true
	}
	list, _ := s.sessions(ctx, mid) // none read: the link takes none of them either (linkTodoSpace)
	return slices.ContainsFunc(list, func(c proto.Session) bool {
		return sessionSpaceKey(mid, c) == key && c.Status != proto.StatusOffline && s.todoTakeRefused(ctx, mid, c)
	})
}

// healthLinksOf are the links of to-do id through space key (computer machine) whose directory is in the health space,
// kept at at: a space's unlink, move or void, which deletes the others, keeps them, in its own write (db.KeptLinks), so
// what the to-do had stays told (todoVerdicts). The caller holds todoBook.writing.
func (s *Service) healthLinksOf(id, machine, key string, at time.Time) db.KeptLinks {
	keep := db.KeptLinks{At: at}
	s.todos.mu.RLock()
	if t := s.todos.todos[id]; t != nil {
		for _, a := range t.Agents {
			if a.Source == "space" && a.Via == key && a.MachineID == machine && identity.InHealthSpace(a.Cwd) {
				keep.Sessions = append(keep.Sessions, a.SessionID)
			}
		}
	}
	s.todos.mu.RUnlock()
	return keep
}

// keepLinksLocked takes to-do id's links keep names through space key off the space in memory once the store kept
// them: released and dismissed as a run taken off by hand is (the apps' row leaves them out, as it now has no such
// run). The caller holds the to-dos' lock.
func (s *Service) keepLinksLocked(id, machine, key string, keep db.KeptLinks) {
	t := s.todos.todos[id]
	if t == nil || len(keep.Sessions) == 0 {
		return
	}
	at := keep.At
	for i := range t.Agents {
		a := &t.Agents[i]
		if a.Source != "space" || a.Via != key || a.MachineID != machine || !slices.Contains(keep.Sessions, a.SessionID) {
			continue
		}
		a.Source, a.Via = "manual", ""
		if a.ReleasedAt == nil {
			a.ReleasedAt = &at
		}
		if a.DismissedAt == nil {
			a.DismissedAt = &at
		}
		if run := [2]string{machine, a.SessionID}; s.todos.activeBySession[run] == id {
			delete(s.todos.activeBySession, run)
		}
	}
}
