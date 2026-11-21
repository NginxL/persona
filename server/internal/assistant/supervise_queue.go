package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/nwverse/persona/server/internal/prompts"
)

func encodeDigestSignals(list []superviseSignal) string {
	raw, _ := json.Marshal(list)
	return string(raw)
}

// Keep the same rendering for initial dispatch and a queue that has waited while tasks changed.
func (s *Service) renderQueuedDigest(ctx context.Context, to string, list []prompts.SuperviseEntry, originalFresh ...bool) string {
	here := s.settings(ctx)
	background := to != here.Main && to == here.BackgroundSupervisor
	leader := background || to != here.Main && s.isLeader(ctx, to)
	fresh := background && !backgroundKeepsContext(here)
	if len(originalFresh) > 0 {
		fresh = originalFresh[0]
	}
	return prompts.RenderSupervise(prompts.Supervise{Entries: sortEntries(list), Leader: leader, Background: background,
		Fresh: fresh, Side: to != here.Main && !leader, Escalated: escalatedIn(list),
		BackgroundEscalated: slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigEscalated && e.Background }),
		LeaderStuck:         slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigLeaderStuck }),
		Overdue:             slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigOverdue }),
		ParkedDue:           slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigParkedDue }),
		ParkDecided:         slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigParkDecided }),
		Decisions: slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool {
			return e.Kind == sigRecheck || e.Kind == sigUndecidable || e.Kind == sigOwnerCannot
		}),
		Inbox: slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigInbox }),
		Asked: askedIn(list), AskMinutes: int(askHoldFor.Minutes()), PushGrading: askedIn(list) && s.quietModeOn(ctx)})
}

// Recheck structured queue entries at the last delivery boundary. Unknown task state keeps the original queue.
// Legacy text-only entries retain their behavior; their task facts cannot safely be reconstructed from prose.
func (s *Service) recheckQueuedDigest(ctx context.Context, to string, msg relayMsg) (relayMsg, bool, bool) {
	var signals []superviseSignal
	if msg.Signals == "" {
		return msg, false, false
	}
	if err := json.Unmarshal([]byte(msg.Signals), &signals); err != nil || len(signals) == 0 {
		return msg, false, true
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return msg, false, true
	}
	var retry []superviseSignal
	ctx, signals, retry = s.allStoppedBatch(ctx, signals)
	if len(retry) > 0 {
		return msg, false, true
	}
	here, web := s.settings(ctx), s.webBase(ctx)
	groups := map[string][]superviseSignal{}
	entries := map[string][]prompts.SuperviseEntry{}
	for _, g := range signals {
		if t, ok := s.todoNow(g.Todo); ok && t.DoneAt == nil && g.Kind == sigNeedsYou {
			if _, known := s.taskNeedsRecheck(ctx, t); !known {
				return msg, false, true
			}
		}
		if e, ok := s.digestEntry(ctx, g, web); ok {
			// A worker reassigned to an Agent needs the ordinary dispatcher, not assistant.send to its old conversation.
			if _, handled, _ := s.leaderRoute(g); handled {
				return msg, false, true
			}
			target := s.digestTarget(ctx, here, g)
			if target == "" {
				return msg, false, true
			}
			groups[target] = append(groups[target], g)
			entries[target] = append(entries[target], e)
		}
	}
	if len(groups) == 0 {
		return msg, true, false
	}
	next := msg
	next.OnceID = relayMsgID(to, msg)
	next.Signals = encodeDigestSignals(groups[to])
	if len(groups[to]) > 0 {
		next.Text = s.renderQueuedDigest(ctx, to, entries[to], prompts.IsSuperviseRound(msg.Text))
		next.Urgent = anyDecision(groups[to])
	}
	// Render before taking the queue lock: rendering may read other service state.
	moved := map[string]relayMsg{}
	for target, list := range groups {
		if target == to {
			continue
		}
		m := msg
		m.OnceID = "" // another destination is a separate delivery
		m.Text = s.renderQueuedDigest(ctx, target, entries[target])
		m.Signals, m.Urgent = encodeDigestSignals(list), anyDecision(list)
		moved[target] = m
	}
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.st.Queue[to]
	if len(q) == 0 || q[0] != msg {
		return msg, false, true
	}
	// Do not change the identity or destination of a send that may already have landed.
	if !msg.TriedAt.IsZero() && (next.Text != msg.Text || len(groups) > 1 || len(groups[to]) == 0) {
		return msg, false, true
	}
	state := b.st
	state.Queue = maps.Clone(b.st.Queue)
	for target, m := range moved {
		state.Queue[target] = append(slices.Clone(state.Queue[target]), m)
	}
	if len(groups[to]) > 0 {
		state.Queue[to] = slices.Clone(q)
		state.Queue[to][0] = next
	} else {
		state.Queue[to] = slices.Clone(q[1:])
		if len(state.Queue[to]) == 0 {
			delete(state.Queue, to)
		}
	}
	// Save moves and the corresponding source removal together, before anything can send the new payload.
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, relayKey, state); err != nil {
			return msg, false, true
		}
	}
	b.st = state
	return next, len(groups[to]) == 0, false
}

// Read the payload that is actually queued, rather than the last in-memory digest (which may have drained).
func (s *Service) queuedDigestSignals(to string) []superviseSignal {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return nil
	}
	for _, msg := range b.st.Queue[to] {
		if msg.Tool != superviseTool {
			continue
		}
		var list []superviseSignal
		if msg.Signals != "" {
			_ = json.Unmarshal([]byte(msg.Signals), &list)
			return list
		}
		s.supervise.mu.Lock()
		list = slices.Clone(s.supervise.queued[to])
		s.supervise.mu.Unlock()
		return list
	}
	return nil
}
