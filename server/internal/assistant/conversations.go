package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The assistant manages its own conversations (docs/assistant/assistant.md "Managing its own conversations", the user's ask on 2026-11-01):
// list, read, rename, archive, start one and message another. Archiving only hides a conversation (the app's "Archived"
// lists it, nothing is deleted); the set is the assistant's (settings assistant.archived), so Claude and Codex
// conversations are archived alike. Messages between conversations are queued by the assistant while the receiver
// runs and carry where they come from; a chain of them is cut after a few hops and a few per pair in ten minutes, so
// two conversations can't keep each other going. A hop is a request: its answer adds none (hopLocked, #7391).
//
// The main conversation is the owner's dispatch center (#10099): a request from it is never refused for its hop, it
// goes at the last hop (the receiver may answer, not go further), and what it sends to a leader conversation counts
// only per pair, not against its per-sender cap. A scheduled run that wakes a conversation starts a new chain, at most
// once per conversation per relayWindow (scheduledWake). conversation_new has its own budget, and a start that fails
// gives its place back.

const (
	archivedKey = "assistant.archived"
	relayKey    = "assistant.relay"

	relayMaxHops    = 3                // hops from the user's own message; an answer adds none
	relayPairMax    = 20               // messages from one conversation to another …
	relayFromMax    = 30               // … and from one conversation to any (a cost cap: past it they queue, #10400) …
	relayWindow     = 10 * time.Minute // … in this long
	relayHopMemory  = time.Hour        // a hop count older than this no longer counts (the user has moved on)
	relayNewMax     = 3                // conversations one conversation opens in relayWindow …
	relayNewMainMax = 10               // … and the main conversation, the owner's dispatch center
	// Loops are counted as round trips (#10400): two conversations that turn back to each other more than
	// relayRoundsMax times in relayRoundWindow are cut, until the owner speaks in one of them.
	relayRoundsMax   = 6
	relayRoundWindow = time.Hour
	relayQueueMax    = 5     // messages from one conversation to another waiting for the rate; past it a send is refused
	relayNewKey      = "new" // the (from, relayNewKey) pair counts conversation_new
)

// relayRetryGap is how long a conversation that did not take a message (relayBook.failedAt) waits for the next try.
var relayRetryGap = 30 * time.Second

type convArchived struct {
	MachineID string    `json:"machine_id"`
	At        time.Time `json:"at"`
	By        string    `json:"by"` // user | assistant
}

type archiveBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]*convArchived // by session id
}

// archivedLocked reads the set once; b.mu is held. A failed read leaves it unloaded (nothing is saved over it).
func (s *Service) archivedLocked(ctx context.Context) bool {
	b := &s.archived
	if b.loaded {
		return true
	}
	m := map[string]*convArchived{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, archivedKey, &m); err != nil {
			s.logf("load archived conversations: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]*convArchived{}
	}
	b.m, b.loaded = m, true
	return true
}

// archivedSet is the archived conversations' machines by id; false while the set could not be read.
func (s *Service) archivedSet() (map[string]string, bool) {
	b := &s.archived
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.archivedLocked(context.Background()) {
		return nil, false
	}
	out := make(map[string]string, len(b.m))
	for id, a := range b.m {
		out[id] = a.MachineID
	}
	return out, true
}

func (s *Service) isArchived(id string) bool {
	set, _ := s.archivedSet()
	_, ok := set[id]
	return ok
}

var errArchiveMain = i18n.Err("assistant-conversations.archive_main")

// errArchiveBackground: the background supervisor is built in and stays listed (supervise_background.go).
var errArchiveBackground = i18n.Err("assistant-conversations.archive_background_supervisor")

// setArchived archives (on) or brings back a conversation; by is who did it. It reports whether anything changed.
func (s *Service) setArchived(ctx context.Context, machine, id string, on bool, by string) (bool, error) {
	if err := s.conversationAvailable(ctx, machine, id); err != nil {
		return false, err
	}
	if on && s.settings(ctx).Main == id {
		return false, errArchiveMain
	}
	if on && s.isBackground(ctx, id) {
		return false, errArchiveBackground
	}
	st := s.store()
	if st == nil {
		return false, errors.New("no database")
	}
	b := &s.archived
	b.mu.Lock()
	if !s.archivedLocked(ctx) {
		b.mu.Unlock()
		return false, errors.New("archived conversations could not be read")
	}
	_, was := b.m[id]
	if was == on {
		b.mu.Unlock()
		return false, nil
	}
	if on {
		b.m[id] = &convArchived{MachineID: machine, At: time.Now().UTC(), By: by}
	} else {
		delete(b.m, id)
	}
	err := st.PutSetting(ctx, archivedKey, b.m)
	if err != nil { // keep memory as the database has it
		if on {
			delete(b.m, id)
		} else {
			b.m[id] = &convArchived{MachineID: machine, At: time.Now().UTC(), By: by}
		}
	}
	b.mu.Unlock()
	if err != nil {
		return false, err
	}
	s.publish(ctx) // the apps get the list with the flag now
	return true, nil
}

// unarchiveOnSend brings an archived conversation back when something is sent to it (the user from the app,
// another conversation): what happens in it should be in the list.
func (s *Service) unarchiveOnSend(machine, id string) bool {
	if !s.isArchived(id) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	changed, err := s.setArchived(ctx, machine, id, false, titleByUser)
	if err != nil {
		s.logf("unarchive %s: %v", short(id), err)
	}
	return changed
}

// archiveRoutes: POST /api/v1/assistant/conversations/{id}/archive and …/unarchive.
func (s *Service) archiveRoutes() {
	for _, op := range []struct {
		path string
		on   bool
	}{{"archive", true}, {"unarchive", false}} {
		s.handle("POST /api/v1/assistant/conversations/{id}/"+op.path, func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			if !validSessionID(id) {
				apierr.Write(w, http.StatusBadRequest, errors.New("invalid conversation id"))
				return
			}
			machine := s.settings(r.Context()).MachineID
			if _, err := s.setArchived(r.Context(), machine, id, op.on, titleByUser); err != nil {
				status := http.StatusInternalServerError
				if errors.Is(err, errArchiveMain) || errors.Is(err, errArchiveBackground) {
					status = http.StatusBadRequest
				}
				apierr.Write(w, status, err)
				return
			}
			apierr.WriteJSON(w, map[string]any{"ok": true, "archived": op.on})
		})
	}
}

var sessionIDRe = lazyre.New(`^[A-Za-z0-9._-]{1,128}$`)

func validSessionID(id string) bool { return sessionIDRe.MatchString(id) }

// --- messages between conversations ---

type relayMsg struct {
	// OnceID preserves a supervision relay's delivery identity when its queued text is refreshed.
	OnceID string `json:"once_id,omitempty"`
	// Signals is the JSON supervision payload, kept comparable with the relay and rechecked after restart.
	Signals   string    `json:"signals,omitempty"`
	From      string    `json:"from"`
	FromTitle string    `json:"from_title"`
	Tool      string    `json:"tool"`
	Text      string    `json:"text"`
	Hop       int       `json:"hop"`
	At        time.Time `json:"at"`
	// State is a task notice's state (quiet.go: done | needs | interrupted | failed); "" for other relays.
	State string `json:"state,omitempty"`
	// Card: the card a quiet task's needs notice stands for (quietCard), pushed if the notice is refused for good. A
	// version that does not know it drops it on its next save: the task's newest waiting card is pushed then.
	Card string `json:"card,omitempty"`
	// Urgent: a supervision message with a signal that waits on a decision (relay_presence.go). A version that does
	// not know it drops it on its next save: the message then ranks and waits as any other supervision message.
	Urgent bool `json:"urgent,omitempty"`
	// Interject: this supervision message contains a validated decision_reply for a pending round; the node may bring
	// it into a running assistant turn and move foreground work to the background. It is never persisted in the relay queue.
	Interject bool `json:"interject,omitempty"`
	// QueuedAt and Depth are when it was first queued and how long the queue was with it then (the queue wait,
	// availability.go): kept by a message that takes its place (queuedAs).
	QueuedAt time.Time `json:"queued_at,omitzero"`
	Depth    int       `json:"depth,omitempty"`
	// DecisionAt is when it began to wait on a decision, where that is later than QueuedAt: it took the place of a
	// queued message that waited on none (replacing, relay_presence.go).
	DecisionAt time.Time `json:"decision_at,omitzero"`
	// Owner: what the owner themselves just did, their answer on the card of a question asked without waiting
	// (ask_later.go). It is not held for their presence (holdLocked): it is what they are waiting to see taken up. A
	// version that does not know it drops it on its next save: the answer is then held as any late answer is.
	Owner bool `json:"owner,omitempty"`
	// Private renews Just me when the message is actually dispatched, including
	// a result that waited in the durable queue or across a manager restart.
	Private bool `json:"private,omitempty"`
	// Answer: it answers a request the receiver sent (hopLocked, #7391), so it starts no exchange of its own: what
	// the receiver sends next costs a hop.
	Answer bool `json:"answer,omitempty"`
	// Due: past the relay rate it waits for the window (relayAdmit, #10400) and goes in no sooner than this, once.
	Due time.Time `json:"due,omitzero"`
	// TriedAt is the start of the earliest try that may have gone in. Save it before sending from the queue.
	// Looks and LookAfter keep the transcript check budget and schedule across manager restarts.
	TriedAt   time.Time `json:"tried_at,omitzero"`
	Looks     int       `json:"looks,omitempty"`
	LookAfter time.Time `json:"look_after,omitzero"`
	// Quote: the owner's message the sender carried (conversation_send / conversation_new quote), as the hub verified
	// it (relay_quote.go, #3822). A version that does not know it drops it on its next save: the message then goes
	// without the owner's words, as before.
	Quote *relayRequest `json:"quote,omitempty"`
	// Chat: what a chat_reply tells (chat_reply.go, #10777), so a later batch of the same chat merges into it. A version
	// that does not know it drops it on its next save: the message then goes as it is, and the next batch queues its own.
	Chat *chatReply `json:"chat,omitempty"`
	// Handover: the hand-over of the owner's words this conversation_send is, and Answers: the one it answers
	// (relay_handover.go, #11699). A version that does not know them drops them on its next save: the message then ranks
	// as before, its hand-over stays queued until it expires, and the one it answers stays delivered.
	Handover string `json:"handover,omitempty"`
	Answers  string `json:"answers,omitempty"`
}

type relayHop struct {
	Hop int       `json:"hop"`
	At  time.Time `json:"at"`
	// From: the conversation whose request this one works on and has not answered yet (#7391); "" when its work is the
	// user's, an answer, or a request it answered already. A version that does not know it drops it on its next
	// save: the answer then costs a hop, as every message did.
	From string `json:"from,omitempty"`
}

// relayState is what is saved: the queued messages and how far each conversation's current work is from the user.
type relayState struct {
	Queue map[string][]relayMsg `json:"queue,omitempty"` // by receiver
	Hops  map[string]relayHop   `json:"hops,omitempty"`  // by conversation: the hop of the message it is working on
	// Spawned: the conversations conversation_new opened, by id, until they report back or their parent is told
	// (spawned.go, #4669).
	Spawned map[string]spawnedConv `json:"spawned,omitempty"`
	// Spoke: when the owner last sent each conversation a message, kept for ownerPresentFor (relay_presence.go).
	Spoke map[string]time.Time `json:"spoke,omitempty"`
}

type relayBook struct {
	mu       sync.Mutex
	loaded   bool
	st       relayState
	sent     map[[2]string][]time.Time // (from, to) -> when; in memory: a restart only resets the rate
	draining map[string]bool
	holds    map[string]*time.Timer // by conversation: drains its queue again when the hold for the owner ends
	stuck    map[string]relayMsg    // by conversation: the message that could not go in at the last drain (pickLocked)
	// failedAt: by conversation, when a delivery into it last failed because it did not take the message (it did not
	// start, its computer was away): that message, when nothing else may go, waits relayRetryGap for its next try, so a
	// conversation that cannot start is not cold-started again on every session list (#10194). In memory: a restart
	// tries at once.
	failedAt map[string]time.Time
	// ownerTurns: the conversations whose round the owner is on, in memory only (#6394). While one is set, every
	// queued message waits, whatever its rank: the owner's own message always goes before the queue. A restart falls
	// back on the persisted Spoke presence window (relay_presence.go).
	ownerTurns map[string]ownerTurn
	// replacing: the background supervisors being replaced (supervise_round.go backgroundOutgrown), in memory: nothing
	// starts going into one, and what would go in straight queues, until what waits for it moved to its replacement or
	// the replacement did not start.
	replacing map[string]bool
	// woke: by conversation, when a scheduled run last started it a new chain (scheduledWake), in memory like sent.
	woke map[string]time.Time
	// quoted: by target and quoted owner message, when a send carrying it went without counting (#10400): only the
	// first such send to a target is free. roundsFrom: by conversation, when the owner last spoke in it; round trips
	// before that are not counted. In memory like sent.
	quoted     map[string]time.Time
	roundsFrom map[string]time.Time
}

// ownerTurn is one conversation's owner-turn gate (#6394).
type ownerTurn struct {
	at      time.Time // when the owner last spoke during it; a later message refreshes the safety limits
	working bool      // a session list has shown the conversation working since the owner spoke
}

func (s *Service) relayLocked(ctx context.Context) bool {
	b := &s.relays
	if b.loaded {
		return true
	}
	rs := relayState{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, relayKey, &rs); err != nil {
			s.logf("load conversation relay: %v", err)
			return false
		}
	}
	if rs.Queue == nil {
		rs.Queue = map[string][]relayMsg{}
	}
	if rs.Hops == nil {
		rs.Hops = map[string]relayHop{}
	}
	if rs.Spawned == nil {
		rs.Spawned = map[string]spawnedConv{}
	}
	b.st, b.loaded = rs, true
	b.sent, b.draining, b.replacing, b.holds = map[[2]string][]time.Time{}, map[string]bool{}, map[string]bool{}, map[string]*time.Timer{}
	b.woke = map[string]time.Time{}
	b.quoted, b.roundsFrom = map[string]time.Time{}, map[string]time.Time{}
	b.stuck = map[string]relayMsg{}
	b.failedAt = map[string]time.Time{}
	b.ownerTurns = map[string]ownerTurn{}
	return true
}

func (s *Service) saveRelayLocked() error {
	st := s.store()
	if st == nil {
		return nil
	}
	for id, hp := range s.relays.st.Hops {
		if time.Since(hp.At) > relayHopMemory {
			delete(s.relays.st.Hops, id)
		}
	}
	for id, at := range s.relays.st.Spoke {
		// One with messages still queued is drainHeldRelays' to forget, after it handed them on.
		if time.Since(at) >= ownerPresentFor && len(s.relays.st.Queue[id]) == 0 {
			delete(s.relays.st.Spoke, id)
		}
	}
	for id, c := range s.relays.st.Spawned {
		if time.Since(c.At) > spawnedKeep {
			delete(s.relays.st.Spawned, id)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := st.PutSetting(ctx, relayKey, s.relays.st)
	if err != nil {
		s.logf("save conversation relay: %v", err)
	}
	return err
}

// hopLocked is the hop a message from → to would be, and whether it is an answer: the first message back to the
// conversation whose request from works on keeps the hop from works at (#7391). Every other message is one hop
// further from the user: a new request, one to a third conversation, a second one back, and the one after an answer.
// An answer is owed only for a request, and a request costs a hop, so a chain counts its exchanges, not its
// messages: a conversation can go back and forth with another a few times, and two that keep writing to each other
// are cut after 2 × relayMaxHops messages, however slowly they write. b.mu is held, the state loaded.
func (b *relayBook) hopLocked(from, to string, now time.Time) (int, bool) {
	hp, ok := b.st.Hops[from]
	if !ok || now.Sub(hp.At) > relayHopMemory { // the user's own work (hop 0), or work they have moved on from
		return 1, false
	}
	if hp.From != "" && hp.From == to {
		return hp.Hop, true
	}
	return hp.Hop + 1, false
}

// userSpoke: the user sent a message to the conversation, which works for them again (hop 0); what the system has
// for it waits while they talk (relay_presence.go).
func (s *Service) userSpoke(id string) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return
	}
	b.ownerSpokeLocked(id, time.Now())
	delete(b.st.Hops, id)
	b.roundsFrom[id] = time.Now() // the owner is in it again: its round trips count from here (#10400)
	s.saveRelayLocked()
}

// relayRoute is who is on each end of a message, as the settings name them (#10099): the main conversation and the
// leader conversations. The zero route is an ordinary message between two other conversations.
type relayRoute struct {
	fromMain bool     // from is the main conversation
	leaders  []string // the leader conversations, counted per pair only when the main conversation writes to them
	// quote: the owner's message the hub verified this send carries (conversation/message id), written within
	// relayHopMemory; "" for none. The first send of it to a target is the owner's own request: no rate, no round
	// trip, hop 1 (#10400).
	quote string
	// queue: past the rate the message waits for the window instead of being refused (relayAdmit's due).
	queue bool
}

// relayAdmission is what relayAdmit decided for a message.
type relayAdmission struct {
	hop    int
	answer bool
	due    time.Time // past the rate: when the window has room for it (zero: it may go now)
	free   bool      // it carries the owner's words and counted nothing
	count  int       // the count that put it past the rate, what it counts, and its limit
	what   string
	limit  int
}

// relaySentKeep is how long a sent message is remembered: the rates' window and the round trips'.
func relaySentKeep() time.Duration { return max(relayWindow, relayRoundWindow) }

// relayRouteFor reads the route of a message from self from the settings.
func (s *Service) relayRouteFor(ctx context.Context, self string) relayRoute {
	cfg := s.settings(ctx)
	if cfg.Main == "" || cfg.Main != self {
		return relayRoute{}
	}
	return relayRoute{fromMain: true, leaders: slices.Clone(cfg.Leaders)}
}

// relayAllowed checks the hop and rate limits for a message from → to and counts it when allowed. It returns the
// message's hop and whether it is an answer (hopLocked); an answer that goes uses up from's one free answer.
func (s *Service) relayAllowed(from, to string, now time.Time) (int, bool, error) {
	return s.relayAllowedOn(relayRoute{}, from, to, now)
}

// relayAllowedOn is relayAllowed on a route (#10099). The main conversation is never refused for its hop: its request
// goes at relayMaxHops, so the receiver may answer it but not go further. Its messages to a leader conversation count
// against the per-pair cap only. to == relayNewKey is a conversation_new, with its own per-sender budget.
func (s *Service) relayAllowedOn(r relayRoute, from, to string, now time.Time) (int, bool, error) {
	a, err := s.relayAdmit(r, from, to, now)
	return a.hop, a.answer, err
}

// relayAdmit checks a message from → to and counts it when it may go (#10400):
//   - the owner's verified words (r.quote) go free, once per quoted message and target;
//   - two conversations that turn back to each other more than relayRoundsMax times in relayRoundWindow are cut;
//   - past the per-pair or per-sender rate, a message on a queue route gets the time the window has room for it
//     (due, its place counted then), at most relayQueueMax waiting per pair; on any other route it is refused.
func (s *Service) relayAdmit(r relayRoute, from, to string, now time.Time) (relayAdmission, error) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return relayAdmission{}, errors.New("the relay state could not be read")
	}
	for k, ts := range b.sent {
		kept := ts[:0]
		for _, t := range ts {
			if now.Sub(t) < relaySentKeep() {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(b.sent, k)
		} else {
			b.sent[k] = kept
		}
	}
	if r.quote != "" && to != relayNewKey {
		for k, at := range b.quoted {
			if now.Sub(at) > relayHopMemory {
				delete(b.quoted, k)
			}
		}
		key := to + "|" + r.quote
		if _, used := b.quoted[key]; !used {
			b.quoted[key] = now
			return relayAdmission{hop: 1, free: true}, nil
		}
	}
	hop, answer := b.hopLocked(from, to, now)
	if hop > relayMaxHops {
		if !r.fromMain {
			return relayAdmission{}, fmt.Errorf("not sent: this would be hop %d from the user's own message (at most %d). Tell the user what you have instead", hop, relayMaxHops)
		}
		hop = relayMaxHops
	}
	if to != relayNewKey {
		if n := b.turnsLocked(from, to, now); n > 2*relayRoundsMax {
			return relayAdmission{}, fmt.Errorf("not sent: this conversation and that one have gone back and forth more than %d times in the last %d minutes (no word from the user in between). Stop the exchange and tell the user where it stands", relayRoundsMax, int(relayRoundWindow.Minutes()))
		}
	}
	var pairTs, fromTs []time.Time
	for k, ts := range b.sent {
		if k[0] != from {
			continue
		}
		for _, t := range ts {
			if now.Sub(t) >= relayWindow {
				continue // a place counted for later (due) is in the window too
			}
			if !r.fromMain || !slices.Contains(r.leaders, k[1]) {
				fromTs = append(fromTs, t)
			}
			if k[1] == to {
				pairTs = append(pairTs, t)
			}
		}
	}
	a := relayAdmission{hop: hop, answer: answer}
	if to == relayNewKey {
		limit := relayNewMax
		if r.fromMain {
			limit = relayNewMainMax
		}
		if len(pairTs) >= limit {
			return relayAdmission{}, fmt.Errorf("not sent: %d conversations started from this one in the last %d minutes already (at most %d)", len(pairTs), int(relayWindow.Minutes()), limit)
		}
	} else {
		over := func(ts []time.Time, limit int) time.Time { // when the window has room again; zero: it has now
			if len(ts) < limit {
				return time.Time{}
			}
			slices.SortFunc(ts, time.Time.Compare)
			return ts[len(ts)-limit].Add(relayWindow)
		}
		due := over(pairTs, relayPairMax)
		a.count, a.limit = len(pairTs), relayPairMax
		what := "messages to that conversation"
		if !(r.fromMain && slices.Contains(r.leaders, to)) {
			if d := over(fromTs, relayFromMax); d.After(due) {
				due = d
				a.count, a.limit = len(fromTs), relayFromMax
				what = "messages to other conversations"
			}
		}
		if !due.IsZero() && due.After(now) {
			if !r.queue {
				return relayAdmission{}, fmt.Errorf("not sent: %d %s in the last %d minutes already (at most %d)", a.count, what, int(relayWindow.Minutes()), a.limit)
			}
			waiting := 0
			for _, m := range b.st.Queue[to] {
				if m.From == from && m.Due.After(now) {
					waiting++
				}
			}
			if waiting >= relayQueueMax {
				return relayAdmission{}, fmt.Errorf("not sent: %d messages to that conversation already wait for the rate (at most %d). Wait for them to go in, or put what you have together in one message", waiting, relayQueueMax)
			}
			a.due, a.what = due, what
		}
	}
	at := now
	if !a.due.IsZero() {
		at = a.due // its place in the window is when it goes in
	}
	b.sent[[2]string{from, to}] = append(b.sent[[2]string{from, to}], at)
	if answer {
		hp := b.st.Hops[from]
		hp.From = "" // answered: another message back is a new request
		b.st.Hops[from] = hp
		s.saveRelayLocked()
	}
	return a, nil
}

// turnsLocked is how many times messages between from and to changed direction in relayRoundWindow, since the owner
// last spoke in either, counting the message from → to about to go (a round trip is two turns). Messages that carried
// the owner's words are not counted (relayAdmit does not keep them). b.mu is held.
func (b *relayBook) turnsLocked(from, to string, now time.Time) int {
	since := now.Add(-relayRoundWindow)
	for _, id := range []string{from, to} {
		if at, ok := b.roundsFrom[id]; ok && at.After(since) {
			since = at
		}
	}
	type sent struct {
		at   time.Time
		back bool // to → from
	}
	var all []sent
	for _, t := range b.sent[[2]string{from, to}] {
		if t.After(since) {
			all = append(all, sent{t, false})
		}
	}
	for _, t := range b.sent[[2]string{to, from}] {
		if t.After(since) {
			all = append(all, sent{t, true})
		}
	}
	slices.SortStableFunc(all, func(x, y sent) int { return x.at.Compare(y.at) })
	all = append(all, sent{now, false})
	turns := 0
	for i := 1; i < len(all); i++ {
		if all[i].back != all[i-1].back {
			turns++
		}
	}
	return turns
}

// relayRefund gives back the place a message from → to counted at took when it did not go (a conversation_new that
// did not start, #10099).
func (s *Service) relayRefund(from, to string, at time.Time) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	k := [2]string{from, to}
	if i := slices.Index(b.sent[k], at); i >= 0 {
		b.sent[k] = slices.Delete(b.sent[k], i, i+1)
	}
}

// scheduledWake: a scheduled run (a follow-up or routine) went into conversation id, which starts a new chain, as a
// routine run in a fresh conversation does (#10099). Only once per conversation per relayWindow: a conversation
// cut off by the hops can't schedule itself a minute ahead again and again to keep a loop going. The per-pair and
// per-sender rates hold either way.
func (s *Service) scheduledWake(id string, now time.Time) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return
	}
	if _, ok := b.st.Hops[id]; !ok {
		return // on the user's own chain already
	}
	if at, ok := b.woke[id]; ok && now.Sub(at) < relayWindow {
		return
	}
	b.woke[id] = now
	delete(b.st.Hops, id)
	s.saveRelayLocked()
}

// relayText is the message as the receiver gets it (prompts/assistant/relay.md).
func relayText(msg relayMsg) string {
	return prompts.RenderRelay(prompts.Relay{FromID: msg.From, FromTitle: msg.FromTitle, Tool: msg.Tool, Hop: msg.Hop,
		MaxHops: relayMaxHops, Text: msg.Text, Quote: relayQuoteOf(msg.Quote)})
}

// queueRelay keeps a message for a conversation until it may go in: its turn ended and the owner is not talking in it
// (relay_presence.go). The queue is kept in the order the messages came; drainRelays picks by rank. A turn held by an
// ask_user waiting for the owner lets go of it after a while (ask_release.go).
func (s *Service) queueRelay(to string, msg relayMsg) int {
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(context.Background()) {
		b.mu.Unlock()
		return 0
	}
	msg.QueuedAt, msg.Depth = time.Now().UTC(), len(b.st.Queue[to])+1
	b.st.Queue[to] = append(b.st.Queue[to], msg)
	s.saveRelayLocked()
	n := len(b.st.Queue[to])
	b.mu.Unlock()
	s.handoverSet(msg.Handover, handoverQueued, "", nil) // a hand-over of the owner's words waits (relay_handover.go)
	go s.releaseHeldAsks(to)
	return n
}

// queueRelayDurable is the supervision delivery boundary. Unlike best-effort conversation relays, a supervision
// signal is accepted only after its queued message is saved. A failed write rolls the in-memory append back so the
// durable failed-start outbox and the ordinary pending scheduler can retry the same generation.
func (s *Service) queueRelayDurable(to string, msg relayMsg) (int, error) {
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(context.Background()) {
		b.mu.Unlock()
		return 0, errors.New("the relay queue cannot be read")
	}
	previous := b.st.Queue
	b.st.Queue = maps.Clone(previous)
	msg.QueuedAt, msg.Depth = time.Now().UTC(), len(previous[to])+1
	b.st.Queue[to] = append(slices.Clone(previous[to]), msg)
	if err := s.saveRelayLocked(); err != nil {
		b.st.Queue = previous
		b.mu.Unlock()
		return 0, err
	}
	n := len(b.st.Queue[to])
	b.mu.Unlock()
	go s.releaseHeldAsks(to)
	return n, nil
}

// queuedRelays are the texts waiting for a conversation (shown above its composer, like typed-ahead input); false
// while they could not be read.
func (s *Service) queuedRelays() (map[string][]string, bool) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return nil, false
	}
	if len(b.st.Queue) == 0 {
		return nil, true
	}
	out := map[string][]string{}
	for id, q := range b.st.Queue {
		for _, msg := range q {
			if msg.Tool == noticeTool || msg.Tool == askAnswerTool || msg.Tool == spawnNoticeTool || msg.Tool == taskEndTool || msg.Tool == relayRefusedTool { // a quiet task's notice (quiet.go), a question's answer (ask_todo.go): its title is enough
				out[id] = append(out[id], msg.FromTitle+"的通知")
				continue
			}
			if msg.Tool == superviseTool { // the tasks to follow up (supervise_digest.go)
				out[id] = append(out[id], "督办消息")
				continue
			}
			if msg.Tool == chatReplyTool { // a chat's new messages (chat_reply.go): their words stay out of the label
				out[id] = append(out[id], msg.FromTitle+"的新消息") // i18n-ignore: the queue's label, as the task notices'
				continue
			}
			out[id] = append(out[id], "来自「"+msg.FromTitle+"」："+msg.Text)
		}
	}
	return out, true
}

func (msg relayMsg) source() *proto.MessageSource {
	switch msg.Tool {
	case "conversation_send", "conversation_new":
		return &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: msg.From, Title: msg.FromTitle}
	case superviseTool:
		return &proto.MessageSource{Kind: proto.SourceSupervision}
	default:
		return nil
	}
}

// deliverRelay sends one message into an assistant conversation and notes the hop it is working on. A quiet task's
// notice (quiet.go) is its own text, and the work it starts is not a hop of any chain.
func (s *Service) deliverRelay(ctx context.Context, machine, to string, msg relayMsg) error {
	if msg.Tool == superviseTool && s.runtimeWithoutSupervisionPolicy(ctx, machine, to) {
		return &rpc.Error{Code: assistantwire.CodeSupervisionRuntime,
			Message: i18n.T(i18n.From(ctx), "rpc-errors.assistant_supervision_runtime")}
	}
	text := relayDeliveryText(msg)
	source, onceID := msg.source(), relayMsgID(to, msg)
	if strings.TrimSpace(text) != "" && relayWords(text) == "" {
		text = prompts.RelayMessageText(text, source, onceID)
	}
	params := map[string]any{"session_id": to, "text": text, "message_source": source, "resume_only": true, holdPushesParam: msg.Tool == superviseTool,
		"once_id": onceID} // every try of it under one id: the node sends it once (relay_once.go)
	if msg.Interject {
		params["interject_background"] = true
	}
	pick, kept, err := s.readModelPick(ctx, machine, to)
	if err != nil {
		return err // unknown is not permission to use a process that still has an older ordinary connection
	}
	if !kept && proto.IsExternalLauncher(pick.Launcher) {
		return i18n.Err("external-adapter.scope_required") // a kept choice on another machine is not a fallback on this one
	}
	if kept {
		if err := s.relayOnPick(ctx, machine, to, params, pick); err != nil {
			return err
		}
	} else if s.isBackground(ctx, to) {
		s.backgroundModel(ctx, params) // its own model purpose, on every message as the main chat's is (supervise_background.go)
	}
	s.availInput(machine, to, relayKind(msg), time.Now()) // a notice has no message source of its own
	if msg.Tool == superviseTool {
		// Register only after admission, but before dispatch: a fast MCP call may precede assistant.send's answer.
		s.holdPushes(ctx, to)
	}
	// A supervision message rendered as a round starts from the first turn (supervise_round.go): what the text says
	// and the switch now decide, also for one queued before a restart or a change of the switch, and whatever model runs it.
	round := s.isBackground(ctx, to) && msg.Tool == superviseTool && !backgroundKeepsContext(s.settings(ctx)) && prompts.IsSuperviseRound(msg.Text)
	if round {
		params[superviseRoundParam] = true
	}
	if msg.Private {
		// Just me covers the original turn, not a later terminal-result turn.
		// Renew it at this actual-dispatch boundary so queued and restarted
		// deliveries retain the same protection as immediate ones.
		s.justMeSent(machine, to)
	}
	var res superviseRoundReply
	if err := s.call(ctx, machine, DeviceAssistantSend, params, &res); err != nil {
		if msg.Tool == superviseTool && !relayInDoubt(err) { // in doubt, drainRelays lets go once it knows it did not go in
			s.releasePushes(ctx, to)
		}
		return err
	}
	if round {
		s.superviseRoundAnswered(to, res)
	}
	s.relayDelivered(ctx, to, msg)
	return nil
}

// relayDeliveryText is what a relayed message says in the conversation: a notice, a supervision message and an answer
// are their own text; a message from another conversation says where it comes from.
func relayDeliveryText(msg relayMsg) string {
	if msg.Tool == noticeTool || msg.Tool == superviseTool || msg.Tool == askAnswerTool || msg.Tool == spawnNoticeTool || msg.Tool == taskEndTool ||
		msg.Tool == chatReplyTool || msg.Tool == relayRefusedTool {
		return msg.Text
	}
	return relayText(msg)
}

// relayDelivered notes a message that went into conversation to: the hop it is working on, or the report turn a
// notice starts.
func (s *Service) relayDelivered(ctx context.Context, to string, msg relayMsg) {
	if msg.Tool == superviseTool {
		return
	}
	if msg.Tool == taskEndTool || msg.Tool == relayRefusedTool {
		return // a record for the conversation that planned the task, or sent the message: no hop of any chain, no push of its own
	}
	if msg.Tool == chatReplyTool {
		s.chatReplyDelivered(to, msg) // others' words: no hop of any chain; the next one about that chat waits its gap
		return
	}
	if msg.Tool == noticeTool || msg.Tool == askAnswerTool {
		s.noteReportTurn(to) // the answer to a task's notice (or to the owner's late call) is pushed however quick it was (#3438)
		return
	}
	s.handoverWentIn(to, msg) // the hand-over of the owner's words it is, or answers (relay_handover.go)
	now := time.Now().UTC()
	b := &s.relays
	b.mu.Lock()
	if s.relayLocked(ctx) {
		hp := relayHop{Hop: msg.Hop, At: now, From: msg.From} // a request: its first message back is the answer (hopLocked)
		if msg.Answer {
			// The answer to a question of its own: a request it works on is still to be answered.
			hp.From = ""
			if old, ok := b.st.Hops[to]; ok && now.Sub(old.At) <= relayHopMemory {
				hp.From = old.From
			}
		}
		b.st.Hops[to] = hp
		s.saveRelayLocked()
	}
	b.mu.Unlock()
}

// drainRelays delivers the next queued message to every conversation of the machine that is not running (called
// whenever its list changes: a turn that just ended, or the hub starting with messages still queued). Next is the
// best-ranked message the owner's presence does not hold (relay_presence.go); it is moved to the head of the queue,
// where the message going in is. A conversation whose messages are all held is drained again when the first hold
// ends.
func (s *Service) drainRelays(machine string, list []proto.Session) {
	leads := s.queueLeads(context.Background())
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(context.Background()) || len(b.st.Queue) == 0 {
		b.mu.Unlock()
		return
	}
	var due []string
	held := map[string]time.Duration{}
	now := time.Now()
	for _, c := range list {
		if len(b.st.Queue[c.SessionID]) > 0 && c.Status != proto.StatusWorking && !c.Starting && !b.draining[c.SessionID] && !b.replacing[c.SessionID] {
			i, wait := b.pickLocked(c.SessionID, leads[c.SessionID], now)
			if last, stuck := b.stuck[c.SessionID]; stuck && i >= 0 && b.st.Queue[c.SessionID][i] == last {
				// Only the message it failed to take is left to go: tried again after a gap, not on every list.
				if gap := relayRetryGap - now.Sub(b.failedAt[c.SessionID]); gap > 0 {
					held[c.SessionID] = gap
					continue
				}
			}
			if i < 0 {
				held[c.SessionID] = wait
				continue
			}
			b.frontLocked(c.SessionID, i)
			b.draining[c.SessionID] = true
			due = append(due, c.SessionID)
		}
	}
	b.mu.Unlock()
	for id, wait := range held {
		s.drainAfter(machine, id, wait)
	}
	for _, id := range due {
		go func(id string) {
			b.mu.Lock()
			q := b.st.Queue[id]
			if len(q) == 0 {
				delete(b.draining, id)
				b.mu.Unlock()
				return
			}
			msg := q[0]
			original := msg
			b.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			// A needs notice may have been answered on the phone after it queued while the agent's node still
			// reported blocked (#4332): ask the task once more right before it would interrupt. A node that cannot
			// be reached is not an answer, so its notice stays queued for a later drain.
			stale, keep := false, false
			if msg.Tool == noticeTool && msg.State == "needs" {
				// The loading read covers a hub restart whose first drain runs before the todos were read in:
				// an unreadable todo is unknown, so the notice is delivered as without the recheck.
				if t, ok, _ := s.todo(ctx, msg.From); ok && t.Quiet {
					if waits, known := s.taskNeedsRecheck(ctx, t); !known {
						keep = true
					} else if !waits {
						stale = true
					}
				}
			}
			var err error
			var retry time.Duration
			landed := false
			if !stale && !keep && !msg.TriedAt.IsZero() {
				landed = s.lookForRelay(machine, id, &msg)
				if !landed && msg.Looks < relayLookLimit {
					keep, retry = true, relayLookGap
				}
			}
			if !stale && !keep && !landed && msg.Tool == superviseTool {
				var checked relayMsg
				checked, stale, keep = s.recheckQueuedDigest(ctx, id, original)
				if !stale && !keep {
					original, msg = checked, checked
				}
			}
			if !stale && !keep && !landed {
				msg, retry, err = s.tryQueuedRelay(ctx, machine, id, &original)
				keep = retry > 0
			}
			cancel()
			// What the failure says (relayFailureOf): refused for good, it leaves the queue once unless its kind keeps it
			// (relayRefused), and then is what follows; gone, it leaves; any other, it is tried again after a gap.
			var failure relayFailure
			refused, stays := false, false
			var then func(context.Context)
			if err != nil && !keep {
				failure = relayFailureOf(err)
				if failure == relayFailedRefused {
					refused = true
					stays, then = s.relayRefused(machine, id, msg, err)
				}
			}
			leaves := stale || refused && !stays || !keep && (err == nil || failure == relayFailedGone)
			if leaves && err != nil && failure == relayFailedGone {
				// Dropped: a hand-over it carried will not be tried again, marked before the queue without it is saved.
				s.handoverEnded(msg.Handover, handoverGone)
			}
			b.mu.Lock()
			delete(b.draining, id)
			delete(b.stuck, id)
			delete(b.failedAt, id)
			if err != nil && !keep && !leaves && failure != relayFailedInDoubt {
				b.failedAt[id] = time.Now()
			}
			taken := false
			if leaves {
				if q := b.st.Queue[id]; len(q) > 0 && q[0] == original {
					b.st.Queue[id] = q[1:]
				}
				if len(b.st.Queue[id]) == 0 {
					delete(b.st.Queue, id)
				}
				s.saveRelayLocked()
				taken = true
			} else {
				if q := b.st.Queue[id]; len(q) > 0 && q[0] == original && q[0] != msg {
					q[0] = msg
					s.saveRelayLocked()
				}
				b.stuck[id] = msg // the next drain takes another one first (pickLocked)
			}
			if retry > 0 {
				s.drainAfterLocked(machine, id, retry)
			}
			b.mu.Unlock()
			if taken && !stale && err == nil {
				s.availQueueDelivered(machine, id, msg, time.Now())
			}
			if taken {
				s.publish(context.Background()) // it no longer shows waiting
			}
			if then != nil {
				then(context.Background())
			}
			if stale || refused && !stays {
				if list, err := s.sessions(context.Background(), machine); err == nil {
					s.drainRelays(machine, list) // a message queued behind the stale or refused one is due as well
				}
			} else if !keep && err != nil && !refused { // relayRefused logged a refusal
				s.logf("relay to %s: %v", short(id), err)
			}
		}(id)
	}
}

// --- the tools (definitions in prompts/mcp/assistant) ---

// assistantConversation finds an assistant conversation by full id or prefix on the assistant's machine.
func (s *Service) assistantConversation(ctx context.Context, id string) (proto.Session, error) {
	return s.spaceConversation(ctx, id, nil)
}

// spaceConversation is assistantConversation among the conversations of one space only (in == nil: any): a
// conversation's tools reach only the conversations of its own space (docs/assistant/health.md "How directories and memory are isolated").
func (s *Service) spaceConversation(ctx context.Context, id string, in *string) (proto.Session, error) {
	list, err := s.sessions(ctx, s.settings(ctx).MachineID)
	if errors.Is(err, ErrNoDevice) {
		return proto.Session{}, errors.New("the assistant's machine is not configured")
	}
	if err != nil {
		return proto.Session{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return proto.Session{}, errors.New("id is required")
	}
	var found []proto.Session
	for _, c := range list {
		if !c.Assistant || in != nil && spaceOf(c) != *in {
			continue
		}
		if c.SessionID == id {
			return c, nil
		}
		if len(id) >= 8 && strings.HasPrefix(c.SessionID, id) {
			found = append(found, c)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return proto.Session{}, fmt.Errorf("no conversation %q (conversations_list has the ids)", id)
	}
	return proto.Session{}, fmt.Errorf("%q is the start of %d conversations: give more of the id", id, len(found))
}

// conversations are the assistant's conversations of space ("" its own) on its machine (archived ones too when
// asked) and the main one.
func (s *Service) conversations(ctx context.Context, withArchived bool, space string) (list []proto.Session, main string) {
	here := s.settings(ctx)
	all, _ := s.sessions(ctx, here.MachineID) // none on no machine
	for _, c := range all {
		if c.Assistant && !c.IsDeleted() && spaceOf(c) == space && (withArchived || !c.Archived) {
			list = append(list, c)
		}
	}
	return list, here.Main
}

// conversationName is how a conversation is called in tool results and relayed messages.
func conversationName(c proto.Session, main string) string {
	if c.SessionID == main {
		return "主对话"
	}
	// Not named yet: the node's title is then the directory's name ("assistant") or Claude Code's default.
	if t := strings.TrimSpace(c.Title); t != "" && t != "Claude Code" && t != path.Base(c.Cwd) {
		return t
	}
	return "新对话"
}

func (s *Service) toolConversationsList(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		Query           string `json:"query"`
		Tag             string `json:"tag"`
		IncludeArchived bool   `json:"include_archived"`
		Limit           int    `json:"limit"`
	}
	_ = json.Unmarshal(args, &a)
	if a.Limit <= 0 {
		a.Limit = 30
	}
	a.Limit = min(a.Limit, 100)
	space, err := s.knownSpace(ctx, self)
	if err != nil {
		return "", err
	}
	list, main := s.conversations(ctx, a.IncludeArchived, space)
	sort.SliceStable(list, func(i, j int) bool { return list[i].UpdatedAt.After(list[j].UpdatedAt) })
	q := strings.ToLower(strings.TrimSpace(a.Query))
	tag := strings.TrimSpace(a.Tag)
	loc := s.location(ctx)
	var b strings.Builder
	n := 0
	for _, c := range list {
		name := conversationName(c, main)
		if q != "" && !strings.Contains(strings.ToLower(name+"\n"+c.LastText), q) {
			continue
		}
		if tag != "" && !hasTag(c, tag) {
			continue
		}
		if n >= a.Limit {
			n++
			continue
		}
		n++
		var marks []string
		if c.SessionID == main {
			marks = append(marks, "main")
		}
		if c.SessionID == self {
			marks = append(marks, "this conversation")
		}
		if c.Status == proto.StatusWorking || c.Starting {
			marks = append(marks, "running")
		}
		if c.Archived {
			marks = append(marks, "archived")
		}
		if c.Agent == "codex" {
			marks = append(marks, "codex")
		}
		fmt.Fprintf(&b, "- %s 「%s」", c.SessionID, name)
		if len(marks) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(marks, ", "))
		}
		if c.Tags != nil && len(*c.Tags) > 0 {
			fmt.Fprintf(&b, " · tags: %s", strings.Join(*c.Tags, ", "))
		}
		if !c.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, " · last active %s (%s)", c.UpdatedAt.In(loc).Format("01-02 15:04"), ago(c.UpdatedAt))
		}
		if t := oneLine(c.LastText); t != "" {
			fmt.Fprintf(&b, "\n  last: %s", clip(t, 160))
		}
		b.WriteString("\n")
	}
	if n == 0 {
		var filters []string
		if q != "" {
			filters = append(filters, fmt.Sprintf("query %q", a.Query))
		}
		if tag != "" {
			filters = append(filters, fmt.Sprintf("tag %q", tag))
		}
		if len(filters) > 0 {
			return "No conversation matches " + strings.Join(filters, " and ") + ".", nil
		}
		return "No conversations.", nil
	}
	head := fmt.Sprintf("%d conversation(s), newest activity first", n)
	if !a.IncludeArchived {
		head += " (archived ones left out)"
	}
	if tag != "" {
		head += fmt.Sprintf(" (tag %q)", tag)
	}
	if n > a.Limit {
		head += fmt.Sprintf("; the first %d", a.Limit)
	}
	return head + ":\n" + b.String(), nil
}

func (s *Service) toolConversationRead(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		ID    string `json:"id"`
		Turns int    `json:"turns"`
	}
	_ = json.Unmarshal(args, &a)
	c, err := s.ownSpaceConversation(ctx, self, a.ID)
	if err != nil {
		return "", err
	}
	if a.Turns <= 0 {
		a.Turns = 2
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, c.MachineID, DeviceSessionItems, map[string]any{"session_id": c.SessionID, "turns": min(a.Turns, 10)}, &res); err != nil {
		return "", err
	}
	head := fmt.Sprintf("「%s」 %s (%s)", conversationName(c, s.settings(ctx).Main), c.SessionID, stateNames[c.State])
	return transcriptText(head, sessionLink(s.webBase(ctx), c.MachineID, c.SessionID, true), res.Items), nil
}

func (s *Service) toolConversationRename(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		Topic     string `json:"topic"`
		UserAsked bool   `json:"user_asked"`
	}
	_ = json.Unmarshal(args, &a)
	c, err := s.ownSpaceConversation(ctx, self, a.ID)
	if err != nil {
		return "", err
	}
	if c.SessionID == s.settings(ctx).Main {
		return "", errors.New("the main conversation is always shown as 主对话: it has no title to change")
	}
	if s.isLeader(ctx, c.SessionID) {
		if d := s.directionOf(c.SessionID); d != nil && d.NameBy == directionByUser && !a.UserAsked {
			return "", errors.New("the user named this direction themselves: rename it only when they ask you to, and then pass user_asked")
		}
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = strings.TrimSpace(a.Topic) // compatible with older callers that supplied kind/topic
		}
		if name == "" {
			return "", errors.New("name is required for a direction: use its project-supervisor name without a date or kind prefix")
		}
		raw, _ := json.Marshal(map[string]string{"id": c.SessionID, "name": name})
		return s.toolDirectionName(ctx, self, raw) // retains the existing main-only direction-name permission
	}
	if !slices.Contains(titleKinds, a.Kind) {
		return "", fmt.Errorf("kind must be one of %s", strings.Join(titleKinds, " / "))
	}
	topic := titleTopic(a.Topic)
	if topic == "" {
		return "", errors.New("topic is empty")
	}
	by, started := s.titleOf(c.SessionID)
	if by == titleByUser && !a.UserAsked {
		return "", fmt.Errorf("the user named this conversation 「%s」 themselves: rename it only when they ask you to, and then pass user_asked", c.Title)
	}
	if started.IsZero() { // started before titles were kept: today's day is the best guess
		started = time.Now()
	}
	title := titleDay(started, s.location(ctx)) + " | " + a.Kind + " | " + topic
	who := titleByAssistant
	if a.UserAsked {
		who = titleByUser // the user's choice, said in words: it stays like one typed in the app
	}
	if err := s.renameConversation(ctx, c, title, who); err != nil {
		return "", err
	}
	if c.SessionID == self {
		return fmt.Sprintf("Renamed this conversation to 「%s」.", title), nil
	}
	return fmt.Sprintf("Renamed %s to 「%s」.", c.SessionID, title), nil
}

func (s *Service) toolConversationArchive(ctx context.Context, self string, args json.RawMessage, on bool) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(args, &a)
	c, err := s.ownSpaceConversation(ctx, self, a.ID)
	if err != nil {
		if !on { // an archived conversation may be long out of the node's list: the id alone will do
			if id := strings.TrimSpace(a.ID); s.isArchived(id) {
				_, err := s.setArchived(ctx, s.settings(ctx).MachineID, id, false, titleByAssistant)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Unarchived %s.", id), nil
			}
		}
		return "", err
	}
	name := conversationName(c, s.settings(ctx).Main)
	changed, err := s.setArchived(ctx, c.MachineID, c.SessionID, on, titleByAssistant)
	if err != nil {
		return "", err
	}
	switch {
	case !changed && on:
		return fmt.Sprintf("「%s」 was archived already.", name), nil
	case !changed:
		return fmt.Sprintf("「%s」 is not archived.", name), nil
	case !on:
		return fmt.Sprintf("Unarchived 「%s」: it is back in the list.", name), nil
	case c.SessionID == self:
		return "Archived this conversation: it is out of the list (under 已归档). This turn finishes as usual; a new message from the user brings it back.", nil
	}
	return fmt.Sprintf("Archived 「%s」 (under 已归档 in the app; nothing is deleted).", name), nil
}

func (s *Service) toolConversationNew(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		Text  string   `json:"text"`
		Tags  []string `json:"tags"`
		Quote string   `json:"quote"`
	}
	_ = json.Unmarshal(args, &a)
	if strings.TrimSpace(a.Text) == "" {
		return "", errors.New("text is empty")
	}
	// Validate the tags before anything is started: an over-long or over-count call must not leave a conversation
	// behind it.
	tags, err := normalizeConversationTags(a.Tags)
	if err != nil {
		return "", err
	}
	if err := s.dispatchNewConversationGuard(ctx, self); err != nil {
		return "", err
	}
	from, err := s.relayFrom(ctx, self)
	if err != nil {
		return "", err
	}
	quote, err := s.resolveQuote(ctx, self, a.Quote) // before relayAllowed: a refused quote uses up no rate
	if err != nil {
		return "", err
	}
	counted := time.Now()
	hop, _, err := s.relayAllowedOn(s.relayRouteFor(ctx, self), self, relayNewKey, counted) // an opening message is a request, never an answer
	if err != nil {
		return "", err
	}
	started := false
	defer func() {
		if !started {
			s.relayRefund(self, relayNewKey, counted) // nothing started: it costs no place (#10099)
		}
	}()
	msg := relayMsg{From: self, FromTitle: from, Tool: "conversation_new", Text: a.Text, Hop: hop, At: time.Now().UTC(), Quote: quote}
	if quote != nil {
		s.keepQuote(ctx, self, *quote) // before it starts: its first turn may hand out work at once
	}
	sctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	space, err := s.knownSpace(ctx, self)
	if err != nil {
		return "", err
	}
	c, err := s.startConversation(sctx, startParams{Text: relayText(msg), Source: msg.source(), Space: space, Origin: OriginSpawned, OriginRef: self})
	if err != nil {
		return "", err
	}
	started = true
	s.titleNew(c.MachineID, c.SessionID, time.Now())
	tagNote := quoteNote(quote)
	if len(tags) > 0 {
		// The conversation already exists: a tag write that fails must not be reported as a failed start.
		if _, err := s.setConversationTags(sctx, c.MachineID, c.SessionID, tags, titleByAssistant); err != nil {
			return fmt.Sprintf("Started conversation %s; it is working on the message now and is named after its first turn. It shows up in the app's sidebar. Its tags could not be saved: %v", c.SessionID, err), nil
		}
		tagNote += " It is born with tags: " + strings.Join(tags, ", ") + "."
	}
	b := &s.relays
	b.mu.Lock()
	if s.relayLocked(ctx) {
		b.st.Hops[c.SessionID] = relayHop{Hop: hop, At: time.Now().UTC(), From: self} // its report back is the answer
		// Followed until it reports back: a turn that fails or ends without a word tells self (spawned.go, #4669).
		b.st.Spawned[c.SessionID] = spawnedConv{Parent: self, At: time.Now().UTC()}
		s.saveRelayLocked()
	}
	b.mu.Unlock()
	return fmt.Sprintf("Started conversation %s; it is working on the message now and is named after its first turn. It shows up in the app's sidebar.%s", c.SessionID, tagNote), nil
}

func (s *Service) toolConversationSend(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		ID    string `json:"id"`
		Text  string `json:"text"`
		Quote string `json:"quote"`
	}
	_ = json.Unmarshal(args, &a)
	if strings.TrimSpace(a.Text) == "" {
		return "", errors.New("text is empty")
	}
	c, err := s.ownSpaceConversation(ctx, self, a.ID)
	if err != nil {
		return "", err
	}
	if c.SessionID == self {
		return "", errors.New("that is this conversation: you can't send a message to yourself")
	}
	// The dispatch policy runs after the target is known and before relayAllowed, whose rate-limit bookkeeping a
	// refused send must not consume.
	if err := s.dispatchSendGuard(ctx, self, c.MachineID, c.SessionID, a.Text); err != nil {
		return "", err
	}
	from, err := s.relayFrom(ctx, self)
	if err != nil {
		return "", err
	}
	quote, err := s.resolveQuote(ctx, self, a.Quote) // before relayAllowed: a refused quote uses up no rate
	if err != nil {
		return "", err
	}
	route := s.relayRouteFor(ctx, self)
	route.queue = true
	now := time.Now()
	if quote != nil && quote.MessageID != "" && now.Sub(quote.At) <= relayHopMemory {
		route.quote = quote.Conversation + "/" + quote.MessageID // the owner's own request: free once per target (#10400)
	}
	adm, err := s.relayAdmit(route, self, c.SessionID, now)
	if err != nil {
		return "", err
	}
	hop, answer := adm.hop, adm.answer
	msg := relayMsg{From: self, FromTitle: from, Tool: "conversation_send", Text: a.Text, Hop: hop, Answer: answer, At: time.Now().UTC(), Quote: quote}
	if !adm.due.IsZero() {
		msg.Due = adm.due.UTC()
	}
	if quote != nil {
		s.keepQuote(ctx, self, *quote) // before it goes in: the receiver may hand out work in the turn it starts
	}
	name := conversationName(c, s.settings(ctx).Main)
	// The hand-over of the owner's words this is, and the one it answers (relay_handover.go, #11699).
	var forget, release func()
	msg.Handover, forget = s.handoverFor(ctx, self, c.SessionID, name, quote)
	defer forget()
	msg.Answers, release = s.handoverToAnswer(self, c.SessionID)
	defer release()
	back := quoteNote(quote)
	if s.unarchiveOnSend(c.MachineID, c.SessionID) {
		back = " It was archived; it is back in the list."
	}
	if !msg.Due.IsZero() {
		n := s.queueRelay(c.SessionID, msg)
		if n == 0 {
			return "", errors.New("not sent: the relay queue could not be read; try again shortly")
		}
		s.publish(ctx)                                 // the receiver shows it waiting above its composer
		s.drainRelays(c.MachineID, []proto.Session{c}) // a timer takes it in when its time comes
		at := msg.Due.In(s.location(ctx))
		return fmt.Sprintf("Queued for %q: %d %s in the last %d minutes already (at most %d). It goes in at about %s (in %d min), once, after what waits before it (%d waiting); no need to send it again.%s",
			name, adm.count, adm.what, int(relayWindow.Minutes()), adm.limit, at.Format("15:04"), int(math.Ceil(time.Until(msg.Due).Minutes())), n, back), nil
	}
	if running := c.Status == proto.StatusWorking || c.Starting; running || s.relayWaits(c.SessionID, msg) {
		n := s.queueRelay(c.SessionID, msg)
		if n > 0 {
			s.spawnReported(self, c.SessionID) // a conversation conversation_new opened reports back (spawned.go)
		}
		s.publish(ctx) // the receiver shows it waiting above its composer
		if running {
			return fmt.Sprintf("%q is running: the message is queued (%d waiting) and goes in when its current turn ends.%s", name, n, back), nil
		}
		s.drainRelays(c.MachineID, []proto.Session{c}) // what may go goes; the rest when the hold for the user ends
		return fmt.Sprintf("%q has messages waiting, or the user is talking in it: the message is queued (%d waiting) and goes in after them.%s", name, n, back), nil
	}
	if err := s.tryDirectRelay(ctx, c.MachineID, c.SessionID, &msg); err != nil {
		if f := relayFailureOf(err); f != relayFailedInDoubt && f != relayFailedNotNow {
			// Back to the sender, nothing queued: a refusal for good, a conversation that is gone, a setup the owner
			// fixes, the manager's own refusal (its model or connection gone, a pick that cannot be read), and, until
			// #11393's decision 1 says otherwise, a conversation at work or away (relayFailedBusy). Fail closed.
			return "", err
		}
		// It could not take the message now (it did not start, its computer is away, the answer did not come): kept
		// in its queue and tried again, under the same once_id, until it goes in (#10194). Never dropped.
		b := &s.relays
		b.mu.Lock()
		if b.failedAt != nil && !relayInDoubt(err) {
			b.failedAt[c.SessionID] = time.Now()
		}
		b.mu.Unlock()
		n := s.queueRelay(c.SessionID, msg)
		if n == 0 {
			return "", err
		}
		s.spawnReported(self, c.SessionID)
		s.publish(ctx)
		s.drainAfter(c.MachineID, c.SessionID, relayRetryGap)
		return fmt.Sprintf("%q could not take the message now (%v): it is queued (%d waiting) and goes in once it can.%s", name, err, n, back), nil
	}
	s.spawnReported(self, c.SessionID)
	return fmt.Sprintf("Sent to 「%s」 (%s).%s", name, c.SessionID, back), nil
}

// relayFrom is the sending conversation's name (its message's hop is relayAllowed's).
func (s *Service) relayFrom(ctx context.Context, self string) (string, error) {
	if self == "" {
		return "", errors.New("only a Persona conversation can do this")
	}
	name := "另一段对话"
	if c, err := s.assistantConversation(ctx, self); err == nil {
		name = conversationName(c, s.settings(ctx).Main)
	}
	return name, nil
}

func (s *Service) queuedFor(id string) int {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return 0
	}
	return len(b.st.Queue[id])
}

// selfSpace is the space of conversation self ("" the assistant's own, and for a caller that is no conversation), and
// whether it could be told (the assistant's machine lists it).
func (s *Service) selfSpace(ctx context.Context, self string) (string, bool) {
	if self == "" {
		return "", true
	}
	c, ok := s.session(ctx, s.settings(ctx).MachineID, self)
	if !ok {
		return "", false
	}
	return spaceOf(c), true
}

// knownSpace is selfSpace for the tools that reach other conversations. One that can't be told is taken for the
// assistant's own: those tools then reach the assistant's conversations only, never the health space's, so health
// conversations stay out of reach either way (the worst case is a health conversation seeing the assistant's list).
func (s *Service) knownSpace(ctx context.Context, self string) (string, error) {
	space, _ := s.selfSpace(ctx, self)
	return space, nil
}

// ownSpaceConversation finds conversation id among those of self's space: the health space's conversations and the
// assistant's own don't read, rename, archive or message each other.
func (s *Service) ownSpaceConversation(ctx context.Context, self, id string) (proto.Session, error) {
	space, err := s.knownSpace(ctx, self)
	if err != nil {
		return proto.Session{}, err
	}
	if row, ok, err := s.deletedConversation(ctx, s.settings(ctx).MachineID, id); err != nil {
		return proto.Session{}, err
	} else if ok && row.Space == space {
		return proto.Session{}, s.conversationAvailable(ctx, row.Machine, id)
	}
	return s.spaceConversation(ctx, id, &space)
}
