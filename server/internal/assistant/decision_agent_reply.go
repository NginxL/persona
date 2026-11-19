package assistant

import (
	"context"
	"strings"
	"time"
	"unicode"
)

type decisionAgentReply struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
	ToldAt    time.Time `json:"told_at,omitzero"`
}

// agentDecisionSpoke settles only an exact owner choice; anything else pauses for the supervisor.
// The same action lock as the deadline's final check fences a default already collected by a sweep.
func (s *Service) agentDecisionSpoke(ctx context.Context, r SessionRef, at time.Time, text string) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		s.logf("decision: agent reply: load tasks: %v", err)
		return
	}
	id := s.activeTodoOf(r.MachineID, r.SessionID)
	if id == "" {
		return
	}
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	t, ok := s.todoNow(id)
	if !ok || t.DoneAt != nil || t.Supervision == nil || t.Supervision.State != reviewNeedsUser {
		return
	}
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return
	}
	a := b.asks[id]
	if a == nil || !decideAsk(a) || a.AskedAt.IsZero() || !a.AcceptedAt.IsZero() ||
		!at.After(a.AskedAt) || acceptAskStale(a, t) || acceptAskEnded(a, t) || !a.RecheckAt.IsZero() || ownerDecisionReturned(t.Supervision) {
		b.mu.Unlock()
		return
	}
	label := exactDecisionOption(text, a.Options)
	if a.AgentReply != nil && label == "" {
		b.mu.Unlock()
		return
	}
	var err error
	if a.AgentReply == nil {
		a.AgentReply = &decisionAgentReply{MachineID: r.MachineID, SessionID: r.SessionID, At: at.UTC()}
		a.DueAt = at.UTC()
		// Even a failed write must not let this process take the default. The sweep retries persistence.
		err = s.saveAcceptErrLocked(ctx)
	}
	cp := *a
	b.mu.Unlock()
	if err != nil {
		s.logf("task %s: decision: persist agent reply: %v", id, err)
	}
	if label != "" && err == nil {
		taken, err := s.decisionToldLocked(ctx, id, label, r.MachineID, r.SessionID, text)
		if err == nil && taken {
			return
		}
		if err != nil {
			s.logf("task %s: decision: record exact agent reply: %v", id, err)
		}
	}
	s.askChanged(ctx)
	s.signalAgentDecisionReply(id, cp)
	// Card updates can involve the network; never wait for them on a user's send or a node hook.
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		s.updateAskCards(ctx, id, cp)
	}()
}

// Letters refer to displayed order; labels match in full, ignoring only whitespace and punctuation.
// A letter and a normalized label pointing at different options are ambiguous, not a choice.
func exactDecisionOption(text string, options []DecisionOption) string {
	normalize := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || unicode.IsPunct(r) {
				return -1
			}
			return r
		}, s)
	}
	answer := normalize(text)
	if answer == "" {
		return ""
	}
	chosen := -1
	if len(answer) == 1 {
		c := answer[0]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c >= 'A' && int(c-'A') < len(options) {
			chosen = int(c - 'A')
		}
	}
	for i, option := range options {
		if normalize(option.Label) == answer {
			if chosen >= 0 && chosen != i {
				return ""
			}
			chosen = i
		}
	}
	if chosen < 0 {
		return ""
	}
	return options[chosen].Label
}

func agentDecisionReplyKey(a acceptAsk) string {
	return "agent-reply@" + a.AskedAt.UTC().Format(time.RFC3339Nano)
}

func (s *Service) signalAgentDecisionReply(id string, a acceptAsk) {
	r := a.AgentReply
	if r == nil || !r.ToldAt.IsZero() || !a.AcceptedAt.IsZero() {
		return
	}
	s.signal(superviseSignal{Todo: id, Kind: sigDecisionReply, Key: agentDecisionReplyKey(a),
		At: r.At, Machine: r.MachineID, Session: r.SessionID})
}

// Recover the supervisor handoff from the persisted round after a manager restart.
func (s *Service) sweepAgentDecisionReplies() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return
	}
	pending := map[string]acceptAsk{}
	for id, a := range b.asks {
		if a.AgentReply != nil && a.AgentReply.ToldAt.IsZero() && a.AcceptedAt.IsZero() {
			pending[id] = *a
		}
	}
	if len(pending) > 0 {
		if err := s.saveAcceptErrLocked(ctx); err != nil {
			s.logf("decision: persist pending agent replies: %v", err)
		}
	}
	b.mu.Unlock()
	for id, a := range pending {
		s.signalAgentDecisionReply(id, a)
	}
}

func (s *Service) agentDecisionRepliesTold(ctx context.Context, signals []superviseSignal) {
	b := &s.accept
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, g := range signals {
		if g.Kind != sigDecisionReply {
			continue
		}
		a := b.asks[g.Todo]
		if a == nil || a.AgentReply == nil || agentDecisionReplyKey(*a) != g.Key || !a.AgentReply.ToldAt.IsZero() {
			continue
		}
		cp := *a.AgentReply
		cp.ToldAt = time.Now().UTC()
		prev := a.AgentReply
		a.AgentReply = &cp
		if err := s.saveAcceptErrLocked(ctx); err != nil {
			a.AgentReply = prev
			s.logf("task %s: decision: save supervisor handoff: %v", g.Todo, err)
		}
	}
}

func (s *Service) agentDecisionPrompt(machine, session string, p map[string]any) {
	if p["event"] != "UserPromptSubmit" || p["prompt_source"] != promptSourceUser {
		return
	}
	at := time.Now()
	if raw, ok := p["spooled_at"].(string); ok {
		var err error
		if at, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			return
		}
	}
	// acceptTryAs holds this todo's action lock across session.send. Waiting for it here would deadlock the read loop
	// with that send, its reverse calls and every reply. Queue the decision work instead: the send completes first, then
	// the hook acquires the lock in event order.
	s.inOrder(func() {
		s.agentDecisionSpoke(context.Background(), SessionRef{MachineID: machine, SessionID: session}, at, "")
	})
}
