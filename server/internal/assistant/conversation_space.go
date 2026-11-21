package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The space each of the assistant's conversations is in, as the manager noted it (#10941, docs/extensions/extensions.md
// "Never shown to extensions"): what keeps the health space's words from an extension (todo_scope.go, feed_scope.go)
// tells a conversation by its space, and a computer lists a quiet assistant conversation only for a week and only its
// 100 newest, and nothing before it is back after a restart. So the space is noted when the manager starts a
// conversation (startConversationOn) and whenever a computer lists one (SessionsChanged), kept in the store
// (db.NoteConversationSpaces), and read when no computer lists it (conversationSpaces). A conversation noted in the
// health space stays there. One a to-do names that was quiet since before spaces were noted is listed again for it to
// be noted (conversation_space_backfill.go); one never noted is still not told (its transcript gone): the rule fails
// closed for it.

// conversationSpaceStore keeps the conversations' spaces (assistant_conversation_spaces).
type conversationSpaceStore interface {
	NoteConversationSpaces(ctx context.Context, rows []db.ConversationSpace) error
	ConversationSpaces(ctx context.Context, sessions []string) (map[string]string, error)
}

// conversationDeletionsStore reads the records of many deleted conversations at once.
type conversationDeletionsStore interface {
	ConversationDeletions(ctx context.Context, machine string, sessions []string) (map[string]json.RawMessage, error)
}

// The database keeps both: a store that is it but lacked one would tell no quiet conversation.
var (
	_ conversationSpaceStore     = (*db.DB)(nil)
	_ conversationDeletionsStore = (*db.DB)(nil)
)

// conversationSpaceTimeout bounds noting the spaces of one list or start.
const conversationSpaceTimeout = 5 * time.Second

// convSpaceBook is what the manager knows of its conversations' spaces. noted: what it wrote, by computer and session,
// so a list noted once is not written again; known: by session, what it read of the store and every health space it
// noted (the health space's when either says so), so a read asks the store only of a conversation it has not read.
// A conversation noted outside the space is read back from the store, which tells it by session on whichever computer
// noted it, rather than taken from the note. For a manager without such a store (in memory) the book is all there is,
// and what it notes is what it knows.
type convSpaceBook struct {
	mu    sync.Mutex
	noted map[[2]string]string
	known map[string]string
	// pending: what the computers' lists left to write, writing: a writer is on it (writeListedSpaces).
	pending []db.ConversationSpace
	writing bool
}

// learnLocked takes space for session id into known: the health space's stays. b.mu is held.
func (b *convSpaceBook) learnLocked(id, space string) {
	if b.known == nil {
		b.known = map[string]string{}
	}
	if was, ok := b.known[id]; !ok || was != SpaceHealth {
		b.known[id] = space
	}
}

// noteConversationSpaces notes rows (each a conversation's computer, session and space): in the store, and once
// written in the book. One noted already as it is now, or in the health space, is not written again. A failed write is
// tried again with the next list; a health space it noted is known all the same, since that only hides.
func (s *Service) noteConversationSpaces(ctx context.Context, rows []db.ConversationSpace) {
	b := &s.convSpaces
	b.mu.Lock()
	var fresh []db.ConversationSpace
	for _, r := range rows {
		if r.SessionID == "" || r.MachineID == "" || !validSpace(r.Space) {
			continue
		}
		if was, ok := b.noted[[2]string{r.MachineID, r.SessionID}]; ok && (was == r.Space || was == SpaceHealth) {
			continue
		}
		fresh = append(fresh, r)
	}
	b.mu.Unlock()
	if len(fresh) == 0 {
		return
	}
	st, durable := s.store().(conversationSpaceStore)
	written := true
	if durable {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), conversationSpaceTimeout)
		defer cancel()
		if err := st.NoteConversationSpaces(ctx, fresh); err != nil {
			s.logf("conversation spaces: note %d: %v", len(fresh), err)
			written = false
		}
	}
	b.mu.Lock()
	if b.noted == nil {
		b.noted = map[[2]string]string{}
	}
	var ids []string
	for _, r := range fresh {
		if written {
			if k := [2]string{r.MachineID, r.SessionID}; b.noted[k] != SpaceHealth {
				b.noted[k] = r.Space
			}
			ids = append(ids, r.SessionID)
		}
		if r.Space == SpaceHealth || !durable {
			b.learnLocked(r.SessionID, r.Space)
		}
	}
	b.mu.Unlock()
	s.spaceBackfillNoted(ids) // noted: no computer need list them for it any more
}

// listedSpace is the space of assistant conversation c as a computer lists it (its own space or its directory's, a
// directory inside the health space counting as the space), and false for a row that is no conversation of this
// manager's assistant, or a deleted one's.
func (s *Service) listedSpace(c proto.Session) (string, bool) {
	if c.SessionID == "" || c.IsDeleted() || !s.isAssistantRow(c) {
		return "", false
	}
	if healthRun(c) {
		return SpaceHealth, true
	}
	return spaceOf(c), true
}

// conversationSpacesListed notes the space of each assistant conversation in machine's list (SessionsChanged). A health
// space is known at once; the store is written off the list's way (writeListedSpaces), so a store that does not answer
// holds up no list: while it is written, a conversation listed is told by the list.
func (s *Service) conversationSpacesListed(machine string, list []proto.Session) {
	if machine == "" {
		return
	}
	var rows []db.ConversationSpace
	var already []string // noted before: one the backfill keeps listed is done with
	backfill := s.spaceFill.active.Load()
	if backfill {
		s.spaceBackfillListed(list) // listed: never given up, whether or not its note is written now
	}
	b := &s.convSpaces
	b.mu.Lock()
	for _, c := range list {
		space, ok := s.listedSpace(c)
		if !ok {
			continue
		}
		if was, seen := b.noted[[2]string{machine, c.SessionID}]; seen && (was == space || was == SpaceHealth) {
			if backfill {
				already = append(already, c.SessionID)
			}
			continue // the usual list: nothing new, nothing to copy
		}
		rows = append(rows, db.ConversationSpace{MachineID: machine, SessionID: c.SessionID, Space: space})
	}
	if len(rows) == 0 {
		b.mu.Unlock()
		s.spaceBackfillNoted(already)
		return
	}
	defer s.spaceBackfillNoted(already)
	if _, durable := s.store().(conversationSpaceStore); !durable {
		b.mu.Unlock()
		s.noteConversationSpaces(context.Background(), rows) // the book alone: nothing to wait for
		return
	}
	for _, r := range rows {
		if r.Space == SpaceHealth {
			b.learnLocked(r.SessionID, r.Space)
		}
	}
	b.pending = append(b.pending, rows...)
	start := !b.writing
	b.writing = true
	b.mu.Unlock()
	if start {
		go s.writeListedSpaces()
	}
}

// writeListedSpaces notes what the lists left to write (conversationSpacesListed), one write at a time, until none is
// left.
func (s *Service) writeListedSpaces() {
	b := &s.convSpaces
	for {
		b.mu.Lock()
		rows := b.pending
		b.pending = nil
		if len(rows) == 0 {
			b.writing = false
			b.mu.Unlock()
			return
		}
		b.mu.Unlock()
		s.noteConversationSpaces(s.runCtx(), rows)
	}
}

// listedSpacesWritten says whether no list waits to be written to the store (tests wait for it).
func (s *Service) listedSpacesWritten() bool {
	b := &s.convSpaces
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.writing
}

// notedSpaces are the spaces noted of conversations ids, by session id: from the book, and those it has not seen read
// from the store in one query. One noted nowhere is left out, and so are those the store could not be read for.
func (s *Service) notedSpaces(ctx context.Context, ids []string) map[string]string {
	out, err := s.readNotedSpaces(ctx, ids)
	if err != nil {
		s.logf("conversation spaces: read %d: %v", len(ids), err)
	}
	return out
}

// readNotedSpaces is notedSpaces with the store's error: what the book knows is answered all the same.
func (s *Service) readNotedSpaces(ctx context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	var miss []string
	b := &s.convSpaces
	b.mu.Lock()
	for _, id := range ids {
		if space, ok := b.known[id]; ok {
			out[id] = space
		} else {
			miss = append(miss, id)
		}
	}
	b.mu.Unlock()
	st, ok := s.store().(conversationSpaceStore)
	if !ok || len(miss) == 0 {
		return out, nil
	}
	got, err := st.ConversationSpaces(ctx, miss)
	if err != nil {
		return out, err
	}
	b.mu.Lock()
	for id, space := range got {
		b.learnLocked(id, space)
		out[id] = b.known[id]
	}
	b.mu.Unlock()
	return out, nil
}

// spaceTold is a conversation's space ("" the assistant's own) and whether it could be told.
type spaceTold struct {
	space string
	known bool
}

// conversationSpaces are the spaces of the conversations ids a to-do or a feed names ("" the assistant's own, as for
// no conversation), each told once: a task leader's agent (task_leader.go, whose workers' to-dos it opens and
// supervises) as its run's (leaderSpace); an assistant conversation as the manager noted it (from its book, and one
// read of the store for those it has not seen), which is what its computer listed or what it was started in, so the
// computer is not asked; one never noted as the assistant's computer lists it (one call for all those), or, when that
// list cannot be read, as the computer answers for each, else as the computer its origin record names lists it (one
// call a computer: the Lark agent's, the Inbox AI twin's); the main conversation as the computer it is routed to lists
// it (Settings.mainRouteMachine: one kept on the assistant's former computer); else as the record of its deletion
// keeps it (one read for all). A row listed under the id tells it only as the assistant's conversation (isAssistantRow,
// as a note is taken: listedSpace) or as one in the health space (which only hides): a session started under the id
// in a terminal is not the chat the id names. One none of them tells (quiet since before its space was noted, the
// store unreadable) is not told.
func (s *Service) conversationSpaces(ctx context.Context, ids []string) map[string]spaceTold {
	out := make(map[string]spaceTold, len(ids))
	plain := map[string]bool{}
	for _, c := range ids {
		if _, ok := out[c]; ok || plain[c] {
			continue
		}
		if c == "" {
			out[c] = spaceTold{known: true}
			continue
		}
		if machine, sid, ok := parseLeaderRef(c); ok && s.isLeaderSession(machine, sid) {
			space, known := s.leaderSpace(ctx, s.canonical(machine), sid)
			out[c] = spaceTold{space, known}
			continue
		}
		plain[c] = true
	}
	if len(plain) == 0 {
		return out
	}
	rest := make([]string, 0, len(plain))
	for c := range plain {
		rest = append(rest, c)
	}
	slices.Sort(rest)
	noted := s.notedSpaces(ctx, rest)
	rest = slices.DeleteFunc(rest, func(c string) bool {
		space, ok := noted[c]
		if ok {
			out[c] = spaceTold{space, true}
		}
		return ok
	})
	if len(rest) == 0 {
		return out
	}
	set := s.settings(ctx)
	told := func(id string, l proto.Session) bool {
		switch {
		case out[id].known && out[id].space == SpaceHealth: // a row of the space under the id: whatever else is listed
		case healthRun(l):
			out[id] = spaceTold{SpaceHealth, true}
		case s.isAssistantRow(l):
			out[id] = spaceTold{spaceOf(l), true}
		default:
			return false
		}
		return true
	}
	listed := func(machine string, want map[string]bool) error {
		list, err := s.todoRunSessions(ctx, machine, want)
		for _, l := range list {
			if want[l.SessionID] {
				told(l.SessionID, l)
			}
		}
		return err
	}
	wanted := func(ids []string) map[string]bool {
		want := make(map[string]bool, len(ids))
		for _, c := range ids {
			want[c] = true
		}
		return want
	}
	listErr := listed(set.MachineID, wanted(rest))
	rest = slices.DeleteFunc(rest, func(c string) bool { return out[c].known })
	// The list could not be read: each on its own, as the computer lists it (selfSpace).
	if listErr != nil {
		rest = slices.DeleteFunc(rest, func(c string) bool {
			l, ok := s.session(ctx, set.MachineID, c)
			return ok && told(c, l)
		})
	}
	if len(rest) > 0 {
		far := map[string][]string{}
		for c, m := range s.originMachines(ctx, rest) {
			if m != set.MachineID {
				far[m] = append(far[m], c)
			}
		}
		for _, m := range slices.Sorted(maps.Keys(far)) {
			_ = listed(m, wanted(far[m])) // one that cannot be read tells none
		}
		rest = slices.DeleteFunc(rest, func(c string) bool { return out[c].known })
	}
	if routed := set.mainRouteMachine(); routed != set.MachineID && slices.Contains(rest, set.Main) {
		if c, ok := s.session(ctx, routed, set.Main); ok && told(set.Main, c) {
			rest = slices.DeleteFunc(rest, func(c string) bool { return c == set.Main })
		}
	}
	deleted := s.deletedConversationsOf(ctx, set.MachineID, rest)
	for _, c := range rest {
		if row, ok := deleted[c]; ok {
			out[c] = spaceTold{row.Space, true}
		} else {
			out[c] = spaceTold{}
		}
	}
	return out
}

// deletedConversationsOf are the records of machine's conversations ids that were deleted, by id: in one read where the
// store can, else one at a time. An unreadable one is left out.
func (s *Service) deletedConversationsOf(ctx context.Context, machine string, ids []string) map[string]deletedConversation {
	out := map[string]deletedConversation{}
	if len(ids) == 0 {
		return out
	}
	if st, ok := s.store().(conversationDeletionsStore); ok {
		raws, err := st.ConversationDeletions(ctx, machine, ids)
		if err != nil {
			s.logf("conversation spaces: deleted conversations: %v", err)
			return out
		}
		for id, raw := range raws {
			var row deletedConversation
			if json.Unmarshal(raw, &row) == nil {
				out[id] = row
			}
		}
		return out
	}
	for _, id := range ids {
		if row, ok, err := s.deletedConversation(ctx, machine, id); err == nil && ok {
			out[id] = row
		}
	}
	return out
}
