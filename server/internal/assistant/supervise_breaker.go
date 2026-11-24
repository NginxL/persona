package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Circuit breaker and backoff for the background supervisor (#12650).
//
// If a background supervisor round hits assistant_denied / missing assistant authorization, or completes consecutive
// rounds on the same task's all_stopped signal with zero supervision progress (no task_review, agent_send,
// supervision_finish, or task state change), the manager stops re-waking it every few minutes: it backs off
// exponentially, falls back once to the main conversation with a clear note, and suppresses further background wakes
// during the backoff window so it cannot spin indefinitely or spam approval prompts.

var (
	// backgroundNoOpMax is how many consecutive no-op background supervisor rounds on a task's all_stopped signal
	// trip the circuit breaker (tests may override).
	backgroundNoOpMax = 2
	// backgroundBreakerBase and backgroundBreakerMax bound the exponential backoff after tripping.
	backgroundBreakerBase = 30 * time.Minute
	backgroundBreakerMax  = 2 * time.Hour
)

type allStoppedSnapshot struct {
	key       string
	reviewLen int
	state     string
	done      bool
	updatedAt time.Time
}

type backgroundActiveRound struct {
	seq         uint64
	deliveredAt time.Time
	allStopped  map[string]allStoppedSnapshot // todoID -> task state when the round was delivered
	progressed  bool                          // true once a supervision progress tool succeeded in this round
	authDenied  bool                          // true if assistant_denied occurred during this round
	deniedTool  string
}

type allStoppedBreakerState struct {
	noOps        int
	trips        int
	trippedUntil time.Time
	reason       string
	alerted      bool
}

type superviseBreaker struct {
	seq            uint64
	active         *backgroundActiveRound
	byTodo         map[string]*allStoppedBreakerState
	authTrips      int
	authUntil      time.Time
	authAlerted    bool
	lastDeniedTool string
}

func backgroundBreakerBackoff(trips int) time.Duration {
	if trips <= 1 {
		return backgroundBreakerBase
	}
	d := backgroundBreakerBase
	for i := 1; i < trips && d < backgroundBreakerMax; i++ {
		if d > backgroundBreakerMax/2 {
			return backgroundBreakerMax
		}
		d *= 2
	}
	return min(d, backgroundBreakerMax)
}

// backgroundProgressTools are the tools whose successful execution proves a supervision round made progress.
var backgroundProgressTools = []string{
	"task_review",
	"supervision_finish",
	"agent_send",
	"task_scope",
	"task_decided",
	"task_plan",
	"interaction_answer",
	"ask_escalate",
	"notify_user",
}

// backgroundRoundDelivered records that a supervision digest was delivered to the background supervisor.
// If a previous round was never closed by TurnEnded, it is evaluated first.
func (s *Service) backgroundRoundDelivered(ctx context.Context, to string, msg relayMsg, now time.Time) {
	if msg.Tool != superviseTool || !s.isBackground(ctx, to) {
		return
	}
	var signals []superviseSignal
	if msg.Signals != "" {
		_ = json.Unmarshal([]byte(msg.Signals), &signals)
	}
	snapshots := map[string]allStoppedSnapshot{}
	for _, g := range signals {
		if g.Kind != sigAllStopped || g.Todo == "" {
			continue
		}
		snap := allStoppedSnapshot{key: g.Key}
		if t, ok := s.todoNow(g.Todo); ok {
			snap.done = t.DoneAt != nil
			if t.Supervision != nil {
				snap.reviewLen = len(t.Supervision.Reviews)
				snap.state = t.Supervision.state(t)
				snap.updatedAt = t.Supervision.UpdatedAt
			}
		}
		snapshots[g.Todo] = snap
	}
	r := &s.background.rounds
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.breaker.active != nil {
		s.finalizeBackgroundRoundLocked(ctx, now)
	}
	r.breaker.seq++
	r.breaker.active = &backgroundActiveRound{
		seq:         r.breaker.seq,
		deliveredAt: now,
		allStopped:  snapshots,
	}
}

// noteAssistantDenied trips the background supervisor circuit breaker when the current background supervisor or one of
// its verified round forks hits assistant_denied so the hub stops re-waking it on a broken auth path. Unrelated or
// unrecognized sessions on the same machine are ignored so an ordinary agent cannot disable background supervision.
func (s *Service) noteAssistantDenied(ctx context.Context, machine, session, tool string) {
	cfg := s.settings(ctx)
	if cfg.BackgroundSupervisor == "" || session == "" || session == cfg.Main || s.isLeader(ctx, session) {
		return
	}
	bgMachine := cmp.Or(cfg.BackgroundSupervisorMachineID, cfg.MachineID)
	if machine != "" && bgMachine != "" && !s.sameMachine(machine, bgMachine) {
		return
	}
	isBg := session == cfg.BackgroundSupervisor
	if !isBg {
		_, isBg = s.backgroundForkBase(ctx, machine, session)
	}
	if !isBg {
		return
	}
	if sess, known := s.nodeSession(machine, session); known && sess.PaneID != "" {
		return
	}
	r := &s.background.rounds
	r.mu.Lock()
	defer r.mu.Unlock()
	br := &r.breaker
	now := time.Now()
	if br.active != nil {
		br.active.authDenied = true
		br.active.deniedTool = tool
	}
	if !br.authUntil.IsZero() && now.Before(br.authUntil) {
		return // already tripped for this window
	}
	br.authTrips++
	backoff := backgroundBreakerBackoff(br.authTrips)
	br.authUntil = now.Add(backoff)
	br.authAlerted = false
	br.lastDeniedTool = tool
	if br.active != nil {
		if br.byTodo == nil {
			br.byTodo = map[string]*allStoppedBreakerState{}
		}
		for todoID := range br.active.allStopped {
			st := br.byTodo[todoID]
			if st == nil {
				st = &allStoppedBreakerState{}
				br.byTodo[todoID] = st
			}
			st.trips++
			st.trippedUntil = br.authUntil
			st.reason = "assistant_denied"
			st.alerted = false
		}
	}
	s.logf("supervise: background supervisor hit assistant_denied on %s (session %s); backing off for %s", tool, short(session), backoff)
}

// noteBackgroundToolProgress records that a supervision progress tool succeeded for the background supervisor (or one
// of its forked rounds), clearing the active round's no-op flag and resetting any tripped auth/task breaker.
func (s *Service) noteBackgroundToolProgress(ctx context.Context, self, name string, args json.RawMessage) {
	if !slices.Contains(backgroundProgressTools, name) || !s.isBackgroundSession(ctx, self) {
		return
	}
	var a struct {
		TodoID    string `json:"todo_id"`
		Machine   string `json:"machine"`
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(args, &a)
	todoID := a.TodoID
	if todoID == "" && name == "agent_send" && a.SessionID != "" {
		if m, ok := s.machineByName(ctx, a.Machine); ok {
			todoID = s.activeTodoOf(m.ID, a.SessionID)
		} else if a.Machine != "" {
			todoID = s.activeTodoOf(a.Machine, a.SessionID)
		}
	}
	r := &s.background.rounds
	r.mu.Lock()
	defer r.mu.Unlock()
	br := &r.breaker
	if br.active != nil {
		br.active.progressed = true
		if name == "supervision_finish" {
			for id := range br.active.allStopped {
				delete(br.byTodo, id)
			}
		}
	}
	if todoID != "" {
		delete(br.byTodo, todoID)
	}
	br.authTrips = 0
	br.authUntil = time.Time{}
	br.authAlerted = false
	br.lastDeniedTool = ""
}

// resetBackgroundBreakerForTodo clears per-task all_stopped circuit-breaker state when the task makes real progress
// (agent starts working again, or task is reviewed/closed).
func (s *Service) resetBackgroundBreakerForTodo(todoID string) {
	if todoID == "" {
		return
	}
	r := &s.background.rounds
	r.mu.Lock()
	delete(r.breaker.byTodo, todoID)
	r.mu.Unlock()
}

// backgroundRoundTurnEnded evaluates the active background supervision round once the background supervisor's turn
// finishes.
func (s *Service) backgroundRoundTurnEnded(ctx context.Context, sid string, now time.Time) {
	if !s.isBackgroundSession(ctx, sid) {
		return
	}
	r := &s.background.rounds
	r.mu.Lock()
	defer r.mu.Unlock()
	s.finalizeBackgroundRoundLocked(ctx, now)
}

func (s *Service) todoStateProgressed(todoID string, snap allStoppedSnapshot) bool {
	t, ok := s.todoNow(todoID)
	if !ok || t.DoneAt != nil {
		return true
	}
	if t.Supervision == nil {
		return true
	}
	if len(t.Supervision.Reviews) != snap.reviewLen || t.Supervision.state(t) != snap.state {
		return true
	}
	if !snap.updatedAt.IsZero() && t.Supervision.UpdatedAt.After(snap.updatedAt) {
		return true
	}
	return false
}

// finalizeBackgroundRoundLocked settles the active background supervision round; r.mu is held.
func (s *Service) finalizeBackgroundRoundLocked(ctx context.Context, now time.Time) {
	br := &s.background.rounds.breaker
	active := br.active
	if active == nil {
		return
	}
	br.active = nil
	if active.progressed && !active.authDenied {
		for todoID := range active.allStopped {
			delete(br.byTodo, todoID)
		}
		br.authTrips = 0
		br.authUntil = time.Time{}
		br.authAlerted = false
		br.lastDeniedTool = ""
		return
	}
	for todoID, snap := range active.allStopped {
		if !active.authDenied && s.todoStateProgressed(todoID, snap) {
			delete(br.byTodo, todoID)
			continue
		}
		if br.byTodo == nil {
			br.byTodo = map[string]*allStoppedBreakerState{}
		}
		st := br.byTodo[todoID]
		if st == nil {
			st = &allStoppedBreakerState{}
			br.byTodo[todoID] = st
		}
		st.noOps++
		if !active.authDenied && st.noOps < backgroundNoOpMax {
			continue
		}
		if !st.trippedUntil.IsZero() && now.Before(st.trippedUntil) {
			continue // already tripped (e.g. by noteAssistantDenied during this same round)
		}
		st.trips++
		backoff := backgroundBreakerBackoff(st.trips)
		st.trippedUntil = now.Add(backoff)
		st.alerted = false
		if active.authDenied {
			st.reason = "assistant_denied"
		} else {
			st.reason = "repeated_noop"
		}
		s.logf("supervise: background supervisor circuit breaker tripped for task %s (%s after %d no-op round(s)); backing off for %s",
			todoID, st.reason, st.noOps, backoff)
	}
}

// applyBackgroundBreaker decides whether signal g routed to the background supervisor may wake it now, should fall
// back once to the main conversation, or should be suppressed during backoff (returns "" as destination).
func (s *Service) applyBackgroundBreaker(ctx context.Context, here Settings, to string, g superviseSignal, now time.Time) (string, superviseSignal) {
	if to == "" || to != here.BackgroundSupervisor || to == here.Main {
		return to, g
	}
	r := &s.background.rounds
	// If the background supervisor is already idle with an unfinalized round (e.g. TurnEnded was missed across a
	// reconnect), settle that round before deciding the next wake.
	r.mu.Lock()
	hasActive := r.breaker.active != nil
	r.mu.Unlock()
	if hasActive {
		if c, ok := s.session(ctx, here.MachineID, to); ok && !c.Starting && c.Status != proto.StatusWorking {
			r.mu.Lock()
			s.finalizeBackgroundRoundLocked(ctx, now)
			r.mu.Unlock()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	br := &r.breaker
	if !br.authUntil.IsZero() && now.Before(br.authUntil) {
		if !br.authAlerted && here.Main != "" {
			br.authAlerted = true
			if st := br.byTodo[g.Todo]; st != nil {
				st.alerted = true
			}
			g.To = here.Main
			g.Breaker = &breakerFallback{Kind: prompts.BreakerAuth, Tool: cmp.Or(br.lastDeniedTool, "assistant tool")}
			s.logf("supervise: background supervisor auth breaker open; falling back task %s (%s) once to main conversation", g.Todo, g.Kind)
			return here.Main, g
		}
		s.logf("supervise: background supervisor auth breaker open until %s; suppressing %s for task %s", br.authUntil.Format(time.RFC3339), g.Kind, g.Todo)
		return "", g
	}
	if g.Kind == sigAllStopped {
		if st := br.byTodo[g.Todo]; st != nil && !st.trippedUntil.IsZero() && now.Before(st.trippedUntil) {
			if !st.alerted && here.Main != "" {
				st.alerted = true
				g.To = here.Main
				g.Breaker = &breakerFallback{Kind: prompts.BreakerAllStopped, Rounds: st.noOps, Reason: st.reason}
				s.logf("supervise: background supervisor all_stopped breaker open for task %s (%s); falling back once to main conversation", g.Todo, st.reason)
				return here.Main, g
			}
			s.logf("supervise: background supervisor all_stopped breaker open for task %s until %s; suppressing wake", g.Todo, st.trippedUntil.Format(time.RFC3339))
			return "", g
		}
	}
	return to, g
}
