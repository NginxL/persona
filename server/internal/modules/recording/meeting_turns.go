package recording

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Meeting turns (docs/assistant/meeting-minutes.md "Feeding the attending session"): the turn a meeting batch starts
// in the attending session is a side exchange until it ends or the user sends a message of their own. Nothing that
// looks at turns takes it for one of the user's: no finished push and no change to whether the other turns are pushed
// (fleet_sessions.go keeps it out of the turn tracker), no Agents triage or work state, never the progress, report or
// completion of the session's task, no project-timeline line, nothing mirrored to the Lark bot or the channels. While
// it runs the hub serves the session only reading tools (meetingRefuses).
//
// A mark per session says so, kept in the assistant's store (assistant.meeting_turns) so it survives a restart:
// set when the batch is sent, started when the session is next seen working, ended when it stops again, and dropped
// when the user speaks during the turn or the next turn starts. The post-turn hooks run after the end, some of them a
// minute later, and still find the ended mark. A mark whose turn was never seen starting is the batch's for
// meetingTurnStartWithin; a turn that starts on a mark the hub could not follow (it restarted meanwhile) is taken for
// the meeting turn, as the contract asks of a turn the hub cannot tell. A turn that ended where the hub could not see
// the end (during a restart, on an interrupt, after a card) is ended once a list shows the session stopped
// (meetingTurnStopped), and none runs longer than meetingTurnRunMax.
//
// The user speaking: the hub says so (UserSpoke) only for assistant conversations; an Agent's own hooks say it
// (meetingTurnPrompt: a UserPromptSubmit whose prompt carries no message source).

const meetingTurnsKey = "assistant.meeting_turns"

const (
	// meetingTurnStartWithin is how long after its batch was sent a turn that starts is still the batch's.
	meetingTurnStartWithin = 10 * time.Minute
	// meetingTurnKeep is how long an ended mark stays when no turn follows it: the post-turn hooks ran long before.
	meetingTurnKeep = 24 * time.Hour
	// meetingTurnRunMax: a meeting turn not seen ending this long after its batch was sent is taken to have ended (a
	// batch's turn takes minutes); a mark stuck running would keep the attendee busy for the rest of the meeting.
	meetingTurnRunMax = 3 * time.Hour
)

// meetingTurnTools are the hub's tools a meeting turn may call: they only read.
var meetingTurnTools = []string{"kb_search", "kb_list", "kb_get", "todo_list", "todo_get", "todo_repositories_get", "agents_list", "agent_read",
	"conversations_list", "conversation_read", "machines_list", "list_followups"}

// meetingTurn is one session's mark.
type meetingTurn struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	Recording string    `json:"recording"`
	SentAt    time.Time `json:"sent_at"`
	Started   bool      `json:"started,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	// Persona: the session is an assistant conversation (a Persona chat), whose MCP calls always name it. Absent
	// (false) for an Agent, and on a mark written before it was kept: either may call with no session of its own.
	Persona bool `json:"persona,omitempty"`
}

type meetingTurnBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]*meetingTurn // by meetingTurnKey
	now    func() time.Time        // nil: time.Now (tests set it)
}

var errMeetingTurnsUnreadable = errors.New("meeting turns unreadable")

// meetingTurnKey is machine's session's key in the book: the machine as the devices name it.
func (s *Recordings) meetingTurnKey(machine, session string) string {
	machine = s.canonical(machine)
	return machine + "/" + session
}

func (b *meetingTurnBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// meetingTurnsLocked reads the book once and drops what has run out; b.mu is held.
func (s *Recordings) meetingTurnsLocked(ctx context.Context) error {
	b := &s.attend.turns
	if !b.loaded {
		m := map[string]*meetingTurn{}
		if st := s.store(); st != nil {
			if _, err := st.Setting(ctx, meetingTurnsKey, &m); err != nil {
				return fmt.Errorf("%w: %v", errMeetingTurnsUnreadable, err)
			}
		}
		if m == nil {
			m = map[string]*meetingTurn{}
		}
		b.m, b.loaded = m, true
	}
	now := b.clock()
	for k, t := range b.m {
		if !t.Started && now.Sub(t.SentAt) > meetingTurnStartWithin || !t.EndedAt.IsZero() && now.Sub(t.EndedAt) > meetingTurnKeep {
			delete(b.m, k)
		} else if t.EndedAt.IsZero() && now.Sub(t.SentAt) > meetingTurnRunMax {
			t.EndedAt = now // its end went unseen; written with the book's next change
		}
	}
	return nil
}

// saveMeetingTurnsLocked writes the book; b.mu is held. A failed write keeps the marks in memory.
func (s *Recordings) saveMeetingTurnsLocked() {
	st := s.store()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := st.PutSetting(ctx, meetingTurnsKey, s.attend.turns.m); err != nil {
		s.logf("save meeting turns: %v", err)
	}
}

// meetingTurnSent marks the turn a batch of recording is about to start in machine's session (before the send: a
// fast tool call can come before it returns), persona when the session is an assistant conversation; the time it is
// marked with. An error while the book cannot be read: the batch waits, since its turn could not be told from the
// user's.
func (s *Recordings) meetingTurnSent(machine, session, recording string, persona bool) (time.Time, error) {
	b := &s.attend.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.meetingTurnsLocked(context.Background()); err != nil {
		return time.Time{}, err
	}
	now := b.clock()
	b.m[s.meetingTurnKey(machine, session)] = &meetingTurn{MachineID: machine, SessionID: session, Recording: recording, SentAt: now,
		Persona: persona}
	s.saveMeetingTurnsLocked()
	return now, nil
}

// meetingTurnUnsent takes back the mark sent at at: the send was refused, no turn comes of it.
func (s *Recordings) meetingTurnUnsent(machine, session string, at time.Time) {
	b := &s.attend.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	k := s.meetingTurnKey(machine, session)
	if t := b.m[k]; t != nil && !t.Started && t.SentAt.Equal(at) {
		delete(b.m, k)
		s.saveMeetingTurnsLocked()
	}
}

// meetingTurnStarted: machine's session is seen working, a new turn when newTurn (it was seen stopped before);
// whether this turn is a meeting turn. Called once per start, where starts are observed (setSessions, TurnStarted).
func (s *Recordings) meetingTurnStarted(machine, session string, newTurn bool) bool {
	if s.isRoom(session) {
		return true // a meeting's room: every turn of it is a side exchange (meeting_room.go)
	}
	b := &s.attend.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.meetingTurnsLocked(context.Background()) != nil {
		return false
	}
	k := s.meetingTurnKey(machine, session)
	t := b.m[k]
	switch {
	case t == nil:
		return false
	case !t.Started:
		t.Started = true
	case t.EndedAt.IsZero() && !newTurn:
		return true // the same turn, seen again (a reconnect)
	default: // a turn after the meeting turn: the mark has done its work
		delete(b.m, k)
		s.saveMeetingTurnsLocked()
		return false
	}
	s.saveMeetingTurnsLocked()
	return true
}

// meetingTurnEnded: machine's session stopped; whether the turn that ended is a meeting turn. A mark whose turn was
// not seen starting is left as it is: the end is an earlier turn's. Called again for the same end, it says the same.
func (s *Recordings) meetingTurnEnded(machine, session string) bool {
	if s.isRoom(session) {
		return true
	}
	b := &s.attend.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.meetingTurnsLocked(context.Background()) != nil {
		return false
	}
	t := b.m[s.meetingTurnKey(machine, session)]
	if t == nil || !t.Started {
		return false
	}
	if t.EndedAt.IsZero() {
		t.EndedAt = b.clock()
		s.saveMeetingTurnsLocked()
	}
	return true
}

// meetingTurnStopped: a list shows machine's session as row, stopped where the end of a turn was not seen (a turn
// that ended while the hub was away, so restarted: the session was not in the list before; an interrupt; a card
// answered by stopping). A meeting turn seen starting ends there when the row is from after its batch was sent (or
// after a restart, whenever it stopped): it is not running any more, and the attendee must not wait on it for the rest
// of the meeting. Whether it ended a meeting turn.
func (s *Recordings) meetingTurnStopped(machine, session string, row proto.Session, restarted bool) bool {
	switch row.Status {
	case proto.StatusDone, proto.StatusIdle, proto.StatusInterrupted, proto.StatusOffline:
	default:
		return false
	}
	b := &s.attend.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.meetingTurnsLocked(context.Background()) != nil {
		return false
	}
	t := b.m[s.meetingTurnKey(machine, session)]
	if t == nil || !t.Started || !t.EndedAt.IsZero() || !restarted && !row.UpdatedAt.After(t.SentAt) {
		return false
	}
	t.EndedAt = b.clock()
	s.saveMeetingTurnsLocked()
	return true
}

// meetingTurnPrompt takes an Agent's hook event (hook.event): a UserPromptSubmit whose prompt the node says is the
// user's own (prompt_source user: no message source, no task's report) while a meeting turn of an Agent runs or is
// about to is the user speaking (meetingTurnUserSpoke); the hub says so only for assistant conversations (UserSpoke),
// and an Agent takes a message sent while it works into the running turn, so no turn start ends the mark. A replayed
// event from before the batch was sent, a node too old to say, or any other prompt (the batch itself, a relay, a task)
// leaves the mark.
func (s *Recordings) meetingTurnPrompt(machine, session string, p map[string]any) {
	if event, _ := p["event"].(string); event != "UserPromptSubmit" {
		return
	}
	if src, _ := p["prompt_source"].(string); src != promptSourceUser {
		return
	}
	b := &s.attend.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.meetingTurnsLocked(context.Background()) != nil {
		return
	}
	k := s.meetingTurnKey(machine, session)
	t := b.m[k]
	if t == nil || t.Persona || !t.EndedAt.IsZero() {
		return
	}
	if at, ok := p["spooled_at"].(string); ok {
		if when, err := time.Parse(time.RFC3339Nano, at); err != nil || !when.After(t.SentAt) {
			return
		}
	}
	delete(b.m, k)
	s.saveMeetingTurnsLocked()
}

// promptSourceUser is a hook event's prompt_source for the user's own words (node/hooks.go promptSource).
const promptSourceUser = "user"

// meetingTurnUserSpoke: the user sent machine's session a message of their own: a meeting turn under way is theirs
// from here on. An ended one stays for the hooks of its end, until the user's turn starts.
func (s *Recordings) meetingTurnUserSpoke(machine, session string) {
	b := &s.attend.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.meetingTurnsLocked(context.Background()) != nil {
		return
	}
	k := s.meetingTurnKey(machine, session)
	if t := b.m[k]; t != nil && t.EndedAt.IsZero() {
		delete(b.m, k)
		s.saveMeetingTurnsLocked()
	}
}

// meetingTurnNow: the turn of machine's session that runs or just ended is a meeting turn, for what looks at a turn
// (pushes, triage, tasks, mirrors). A batch sent whose turn has not been seen starting is not: the hooks of the turn
// before it may still be running.
func (s *Recordings) meetingTurnNow(machine, session string) bool {
	if s.isRoom(session) {
		return true
	}
	b := &s.attend.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.meetingTurnsLocked(context.Background()) != nil {
		return false
	}
	t := b.m[s.meetingTurnKey(machine, session)]
	return t != nil && t.Started
}

// meetingTurnRunning: a meeting turn may be running in machine's session now (sent and not seen ending); an error
// when the book cannot be read.
func (s *Recordings) meetingTurnRunning(ctx context.Context, machine, session string) (bool, error) {
	if session == "" {
		return false, nil
	}
	b := &s.attend.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.meetingTurnsLocked(ctx); err != nil {
		return false, err
	}
	t := b.m[s.meetingTurnKey(machine, session)]
	return t != nil && t.EndedAt.IsZero(), nil
}

// meetingTurnAgentRunning: a meeting turn of an Agent (not an assistant conversation) may be running on machine now;
// an error when the book cannot be read.
func (s *Recordings) meetingTurnAgentRunning(ctx context.Context, machine string) (bool, error) {
	b := &s.attend.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.meetingTurnsLocked(ctx); err != nil {
		return false, err
	}
	prefix := s.meetingTurnKey(machine, "")
	for k, t := range b.m {
		if strings.HasPrefix(k, prefix) && !t.Persona && t.EndedAt.IsZero() {
			return true, nil
		}
	}
	return false, nil
}

// meetingRefuses refuses a hub tool other than the reading ones during a meeting turn of the calling session (and any
// call while the hub cannot tell, as guestRefuses does): other people's words started the turn, and only the user's
// own message may have Persona act. An Agent's MCP server has no session of its own (the node names it only for the
// tools that need their caller), so a call that names none and is not an assistant conversation's (assistant) is
// refused while a meeting turn of any Agent on that machine runs: it may be that Agent's.
func (s *Recordings) meetingRefuses(ctx context.Context, machine, session, name string, assistant bool) (proto.ToolResult, bool) {
	var running bool
	var err error
	if session == "" && !assistant {
		running, err = s.meetingTurnAgentRunning(ctx, machine)
	} else {
		running, err = s.meetingTurnRunning(ctx, machine, session)
	}
	switch {
	case err != nil:
		s.logf("mcp call %s: %v", name, err)
		return proto.TextResult("tool unavailable for the moment: "+name, true), true
	case !running || slices.Contains(meetingTurnTools, compat.Tool(name)):
		return proto.ToolResult{}, false
	}
	return proto.TextResult(i18n.T(s.lang(ctx), "assistant-meeting_feed.tool_needs_user", i18n.Args{"tool": name}), true), true
}

// meetingSource: a send's params carry a meeting's batch (message source meeting).
func meetingSource(p map[string]any) bool {
	src, _ := p["message_source"].(*proto.MessageSource)
	return src != nil && src.Kind == proto.SourceMeeting
}
