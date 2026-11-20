package assistant

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the supervision message ----

// superviseTool marks a queued supervision message (relayMsg.Tool).
const superviseTool = "supervise"

// sendDigest puts the due signals into their supervisors' conversations, one supervision message each: now, or queued
// behind its turn (a message still queued takes these in too). A signal goes to its task's leader, to the background
// supervisor for a task no leader supervises (made here on first use, supervise_background.go), or to the main
// conversation (digestTo, supervise_leader.go); while no main conversation is set, what would go to it goes to the
// conversation that dispatched its task (routeTo). A signal that no longer holds (answered, done, reviewed meanwhile) is
// dropped.
func (s *Service) sendDigest(ctx context.Context) {
	b := &s.supervise
	batch := s.supervisionBatchInterval(ctx)
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	b.mu.Lock()
	b.timer, b.timerAt = nil, time.Time{}
	list := make([]superviseSignal, 0, len(b.pending))
	for _, g := range b.pending {
		list = append(list, g)
	}
	now := time.Now()
	if wait, ok := digestWaitWithBatch(now, b.last, list, s.superviseTimings(), batch); !ok || wait > time.Second {
		if ok {
			s.scheduleDigestLocked(now)
		}
		b.mu.Unlock()
		return
	}
	if batch > 0 {
		list = slices.DeleteFunc(list, func(g superviseSignal) bool { return supervisionBatchDeferred(g, now) })
	}
	if len(list) == 0 {
		s.scheduleDigestLocked(now)
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	here := s.settings(ctx)
	gone := map[string]bool{} // conversations other than the main one that cannot be told now, as this digest found them
	there := func(to string) bool {
		if _, seen := gone[to]; !seen {
			if to == here.BackgroundSupervisor {
				// One just started, or whose machine has not listed yet, is there: its message queues (backgroundThere).
				gone[to] = !s.backgroundThere(ctx, here, to)
			} else {
				c, ok := s.session(ctx, here.MachineID, to)
				gone[to] = !ok || !c.Assistant
			}
		}
		return !gone[to]
	}
	// What waits for a main conversation: everything while there is no machine (the hub's hello while the bus is down);
	// while no main conversation is set, only what the conversation that dispatched its task cannot be told either
	// (#10126): the rest goes there now.
	waits := map[string]bool{}
	for _, g := range list {
		if here.MachineID == "" || here.Main == "" && !s.toldWithoutMain(ctx, here, g, there) {
			waits[g.id()] = true
		}
	}
	if len(waits) > 0 {
		// Nowhere to tell them now. They stay pending and untold, and go out on a later try (#5420).
		// Said once when it starts and then hourly: the watch's next pass tries again, and logging each try filled
		// the hub's log with a line every 30 seconds while the main conversation was away. What went out meanwhile
		// is not counted: only what still waits keeps this going.
		b.mu.Lock()
		first := b.noMainSince.IsZero()
		if first {
			b.noMainSince = now
		}
		logIt := first || now.Sub(b.noMainLogged) >= superviseNoMainLogEvery
		if logIt {
			b.noMainLogged = now
		}
		since, waiting := now.Sub(b.noMainSince), len(waits)
		s.retryDigestLocked(s.superviseTimings().urgent)
		b.mu.Unlock()
		switch {
		case first:
			s.logf("supervise: no main conversation; %d signal(s) wait for one", waiting)
		case logIt:
			s.logf("supervise: still no main conversation after %s; %d signal(s) waiting", since.Round(time.Second), waiting)
		}
		if len(waits) == len(list) {
			return
		}
	} else {
		b.mu.Lock()
		away := b.noMainSince
		b.noMainSince, b.noMainLogged = time.Time{}, time.Time{}
		b.mu.Unlock()
		switch {
		case away.IsZero():
		case here.Main != "":
			s.logf("supervise: main conversation found after %s; telling what waited", now.Sub(away).Round(time.Second))
		default:
			s.logf("supervise: what waited %s goes to the conversations that dispatched it", now.Sub(away).Round(time.Second))
		}
	}
	b.mu.Lock()
	list = list[:0]
	held := map[string]superviseSignal{}
	for id, g := range b.pending { // as they are now: one may have come or gone meanwhile
		if waits[id] || batch > 0 && supervisionBatchDeferred(g, now) {
			held[id] = g
			continue
		}
		list = append(list, g)
	}
	if len(list) == 0 {
		if len(held) > 0 {
			s.scheduleDigestLocked(now)
		}
		b.mu.Unlock()
		return
	}
	b.pending = held
	prev := b.last
	b.last = now
	b.mu.Unlock()
	if batch > 0 {
		defer s.rescheduleSupervision() // an early screening return must not strand another recipient's deferred batch
	}

	list = s.screenMechanicalSignals(ctx, list, now, prev)
	if len(list) == 0 {
		return
	}
	web := s.webBase(ctx)
	groups := map[string][]superviseSignal{}
	var order []string
	leaders := map[[2]string][]superviseSignal{} // leader agent (machine, session) → the signals of its workers (task_leader.go)
	var leaderOrder []TodoAgent
	var wait []superviseSignal     // with no main conversation, what nobody else can be told either (routeTo)
	s.rehomeLeaderlessWorkers(ctx) // a worker whose leader's task closed gets a supervisor that can act, before routing
	if s.backgroundNeeded(ctx, here, list) {
		// The background supervisor is made the first time a signal is for it, and again when it is gone. One that
		// cannot start leaves here as it was: the gone check below sends its signals to the main conversation.
		id, was := s.ensureBackground(ctx)
		if id != "" {
			here = s.settings(ctx)
		}
		if lost := s.backgroundLost(ctx, was, cmp.Or(id, here.Main), web); len(lost) > 0 { // what waited for the one that is gone goes out with these
			list = mergeSignals(lost, list)
		}
	}
	var retry []superviseSignal
	ctx, list, retry = s.allStoppedBatch(ctx, list)
	if len(retry) > 0 {
		s.restoreDigest(retry, now, prev) // unknown is retried, never marked sent or told
	}
	if len(list) == 0 {
		return
	}
	b.mu.Lock()
	for _, g := range list {
		b.sent[g.id()] = g.Key
	}
	b.last = now
	b.mu.Unlock()
	for _, g := range list {
		if lead, handled, reachable := s.leaderRoute(g); handled { // never digestTo: a session is no conversation
			if reachable {
				key := [2]string{lead.MachineID, lead.SessionID}
				if _, seen := leaders[key]; !seen {
					leaderOrder = append(leaderOrder, lead)
				}
				leaders[key] = append(leaders[key], g)
				continue
			}
			// Unreachable, not dropped: it becomes a decision for the conversation above the leader (escalateLeaderSignal),
			// which has its own delivery target below.
			worker := g
			g = s.escalateLeaderSignal(ctx, g)
			if g.Todo == "" { // nobody above it: it is told again next time
				if _, open := s.todoNow(worker.Todo); open && here.Main == "" {
					wait = append(wait, worker) // nobody above it since the check (toldWithoutMain): it waits as that one would
				}
				continue
			}
		}
		to := s.routeTo(ctx, here, g, there)
		if to == "" { // no main conversation, and the one that dispatched it cannot be told either: it waits (#10126)
			wait = append(wait, g)
			continue
		}
		if _, ok := groups[to]; !ok {
			order = append(order, to)
		}
		groups[to] = append(groups[to], g)
	}
	if batch > 0 {
		order = s.gateDigestBatches(order, groups, now, batch)
		b.mu.Lock()
		s.scheduleDigestLocked(now)
		b.mu.Unlock()
	}
	delivered := false
	for _, lead := range leaderOrder {
		delivered = s.deliverToLeaderAgent(ctx, lead, leaders[[2]string{lead.MachineID, lead.SessionID}], web, now, prev) || delivered
	}
	for _, to := range order {
		delivered = s.deliverDigest(ctx, here, to, groups[to], web, now, prev) || delivered
	}
	if delivered && (len(order) > 1 || batch > 0) {
		// One message went out: what another left undone (put the last message back to prev) does not undo it.
		b.mu.Lock()
		if b.last.Before(now) {
			b.last = now
		}
		b.mu.Unlock()
	} else if batch > 0 && !delivered {
		// A held or moot batch sent nothing. Do not invent a legacy last-message time in case batching is switched off.
		b.mu.Lock()
		if b.last.Equal(now) {
			b.last = prev
		}
		b.mu.Unlock()
	}
	if len(wait) > 0 {
		at := now // nothing went out: the last message is prev again
		if delivered {
			at = time.Time{} // what went out to the others stays the last message
		}
		s.restoreDigest(wait, at, prev)
	}
}

// routeTo is the conversation signal g goes to: where digestTo sends it, the main conversation instead of one that
// cannot be told now (there), and while no main conversation is set, the conversation that dispatched its task
// (dispatcherOf, #10126). "" when none of them can be told: it waits for a main conversation (#5420). With a main
// conversation set it is never "", and nothing goes anywhere else than before.
func (s *Service) routeTo(ctx context.Context, here Settings, g superviseSignal, there func(string) bool) string {
	to := s.digestTarget(ctx, here, g)
	if to != here.Main && !there(to) {
		to = here.Main
	}
	if to == "" {
		to = s.dispatcherOf(g.Todo, there)
	}
	return to
}

// toldWithoutMain: signal g can be told now while no main conversation is set: its task leader's agent takes it
// (leaderRoute: never the main conversation's); with that agent unreachable, the conversation above the leader does
// (supervisorAboveTask, the decision escalateLeaderSignal makes, routed as sendDigest routes it); or a conversation does
// (routeTo). A leader's signal with nobody above the leader to tell waits, as every signal did before (#5420).
func (s *Service) toldWithoutMain(ctx context.Context, here Settings, g superviseSignal, there func(string) bool) bool {
	if _, handled, reachable := s.leaderRoute(g); handled {
		if reachable {
			return true
		}
		t, ok := s.todoNow(g.Todo)
		if !ok {
			return true // its task went meanwhile: nothing is told of it
		}
		up, _ := s.supervisorAboveTask(ctx, t)
		return up != "" && s.routeTo(ctx, here, superviseSignal{Todo: g.Todo, Kind: sigDecide, To: up}, there) != ""
	}
	return s.routeTo(ctx, here, g, there) != ""
}

// dispatcherOf is the conversation that dispatched task id, while it can be told now (there): the one its ledger names
// as its supervisor (the side conversation or leader that handed it out with task_plan), else the one it was planned
// in (Todo.Origin). "" when neither can, and for a ledger that names a task leader's agent (leaderRef), which is no
// conversation.
func (s *Service) dispatcherOf(id string, there func(string) bool) string {
	t, ok := s.todoNow(id)
	if !ok {
		return ""
	}
	var sup string
	if t.Supervision != nil {
		sup = t.Supervision.Supervisor
	}
	for _, c := range []string{sup, t.Origin} {
		if _, _, ref := parseLeaderRef(c); c != "" && !ref && there(c) {
			return c
		}
	}
	return ""
}

// deliverDigest puts the signals list into conversation to (the main conversation, a leader or the background
// supervisor) as one supervision message; now is when sendDigest marked it as the last message and prev the last
// message before. True when it went in or was queued.
func (s *Service) deliverDigest(ctx context.Context, here Settings, to string, list []superviseSignal, web string, now, prev time.Time) bool {
	b := &s.supervise
	var entries []prompts.SuperviseEntry
	var kept, moot []superviseSignal
	s.remedyGoesTo(to, list) // a failed start is ended by this conversation's turn, no other's (start_remedy.go)
	for _, g := range list {
		if e, ok := s.digestEntry(ctx, g, web); ok {
			entries = append(entries, e)
			kept = append(kept, g)
		} else {
			moot = append(moot, g)
		}
	}
	s.turnsTold(ctx, moot) // what no longer holds is not told after a restart either (task_turns.go)
	if len(entries) == 0 {
		return false
	}
	holds := kept
	entries, kept, rollback, err := s.reserveSupervision(ctx, entries, kept, now)
	if err != nil {
		s.logf("supervise: deduplication unavailable; retrying: %v", err)
		s.restoreDigest(kept, now, prev)
		return false
	}
	if len(entries) == 0 {
		s.agentDecisionRepliesTold(ctx, holds)
		s.turnsTold(ctx, holds) // said the same within the window: told
		b.mu.Lock()
		if b.last.Equal(now) {
			b.last = prev
		}
		b.mu.Unlock()
		return false
	}
	accepted := false
	defer func() {
		if !accepted {
			rollback()
			return
		}
		s.parkedDueTold(ctx, kept) // told for good: not again after a restart (parked.go)
		s.parkDecidedTold(ctx, kept)
		s.agentDecisionRepliesTold(ctx, kept)
		s.turnsTold(ctx, holds) // and how a turn ended: kept went, the rest of them was said the same within the window
	}()
	background := to != here.Main && to == here.BackgroundSupervisor // the leader's message, told that the tasks are not its own (supervise_background.go)
	render := func(list []prompts.SuperviseEntry) string { return s.renderQueuedDigest(ctx, to, list) }
	queued := s.queuedDigestSignals(to)
	msg := relayMsg{From: superviseTool, FromTitle: "督办", Tool: superviseTool, At: now.UTC()}
	if len(queued) > 0 && !slices.ContainsFunc(kept, func(g superviseSignal) bool {
		return g.Kind == sigPRConflict || g.Kind == sigDraftPR || g.Kind == sigDecisionReply ||
			g.Kind == sigInbox && g.Inbox != nil && g.Inbox.ToAgent == "unhandled"
	}) {
		// The last message may still wait in the queue: this one takes its place with both.
		all := mergeSignals(queued, kept)
		var allEntries []prompts.SuperviseEntry
		for _, g := range all {
			if e, ok := s.digestEntry(ctx, g, web); ok {
				allEntries = append(allEntries, e)
			}
		}
		msg.Text, msg.Urgent = render(allEntries), anyDecision(all)
		msg.Signals = encodeDigestSignals(all)
		if s.replaceQueuedSupervise(to, msg) {
			accepted = true
			b.mu.Lock()
			b.setQueuedLocked(to, all)
			b.mu.Unlock()
			s.publish(ctx)
			return true
		}
	}
	msg.Text, msg.Urgent = render(entries), anyDecision(kept)
	msg.Signals = encodeDigestSignals(kept)
	msg.Interject = slices.ContainsFunc(kept, func(g superviseSignal) bool { return g.Kind == sigDecisionReply })
	if background {
		s.logf("supervise: %d signal(s) to the background supervisor %s", len(entries), short(to))
	} else if to != here.Main {
		s.logf("supervise: %d signal(s) to conversation %s", len(entries), short(to))
	} else {
		s.logf("supervise: %d signal(s) to the main conversation", len(entries))
	}
	c, ok := s.session(ctx, here.MachineID, to)
	if !ok || c.Starting || s.relayReplacing(to) || !msg.Interject && (c.Status == proto.StatusWorking || s.relayWaits(to, msg)) {
		if !s.queueDigest(to, msg, kept, now, prev) {
			return false
		}
		accepted = true
		s.publish(ctx)
		if ok && c.Status != proto.StatusWorking && !c.Starting {
			s.drainRelays(here.MachineID, []proto.Session{c})
		}
		return true
	}
	if err := s.tryDirectRelay(ctx, here.MachineID, to, &msg); err != nil {
		if s.refusedDirect(ctx, here.MachineID, to, msg, err) {
			accepted = true // retire this delivery while keeping the task available for review
			return true
		}
		s.logf("supervise: %v; queued", err)
		if s.queueDigest(to, msg, kept, now, prev) {
			accepted = true
			s.publish(ctx)
		}
		return accepted
	}
	accepted = true
	if !msg.Interject { // an in-turn decision reply can pass an older supervision relay; keep that relay tracked
		b.mu.Lock()
		b.setQueuedLocked(to, nil)
		b.mu.Unlock()
	}
	return true
}

// setQueuedLocked keeps list as the signals of the supervision message still queued for conversation to (none: nil);
// b.mu is held.
func (b *superviseBook) setQueuedLocked(to string, list []superviseSignal) {
	if len(list) == 0 {
		delete(b.queued, to)
		return
	}
	if b.queued == nil {
		b.queued = map[string][]superviseSignal{}
	}
	b.queued[to] = list
}

// queueDigest queues msg, the message of signals kept, behind conversation to's turn; at is when sendDigest
// marked it as the last message and prev the last message before. When the relay queue can't be read nothing is
// queued (false): the signals wait again, no longer marked told (a newer one of the same task and kind keeps its
// place), the message still queued for it (supervise.queued) stays as it was, the last message is prev again (nothing went
// out), and the digest is tried again in the urgent gap, or later when the gap after prev has not passed yet.
func (s *Service) queueDigest(to string, msg relayMsg, kept []superviseSignal, at, prev time.Time) bool {
	if slices.ContainsFunc(kept, func(g superviseSignal) bool {
		return g.Kind == sigPRConflict || g.Kind == sigDraftPR || g.Kind == sigDecisionReply ||
			g.Kind == sigInbox && g.Inbox != nil && g.Inbox.ToAgent == "unhandled"
	}) {
		// Rendered relays cannot recheck PR facts, a source acknowledgment or a decision round. Keep these signals pending, so the next
		// dispatch revalidates them; deliverDigest releases the unaccepted dedup reservation.
		s.restoreDigest(kept, at, prev)
		return false
	}
	b := &s.supervise
	if s.queueRelay(to, msg) > 0 {
		b.mu.Lock()
		b.setQueuedLocked(to, kept)
		b.mu.Unlock()
		return true
	}
	retry := s.superviseTimings().urgent
	s.logf("supervise: the relay queue can't be read; %d signal(s) wait, trying again in %s", len(kept), retry)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if b.last.Equal(at) { // no later message went out meanwhile
		b.last = prev
	}
	for _, g := range kept {
		id := g.id()
		if b.sent[id] != g.Key {
			continue // a newer one of its kind went out meanwhile
		}
		delete(b.sent, id)
		if cur, ok := b.pending[id]; !ok {
			b.pending[id] = g
		} else if g.Kind == sigReport && cur.Key != g.Key {
			b.pending[id] = joinReports(g, cur) // both reports are told, this one first
		} else if g.Kind == sigInbox && cur.Key != g.Key {
			b.pending[id] = joinInbox(g, cur) // the newer item, with the messages of both
		}
	}
	s.retryDigestLocked(retry)
	return false
}

// mergeSignals is a with b's signals over its own (one per task and kind), in a's order then b's.
func mergeSignals(a, b []superviseSignal) []superviseSignal {
	out := slices.Clone(a)
	for _, g := range b {
		if i := slices.IndexFunc(out, func(x superviseSignal) bool { return x.id() == g.id() }); i >= 0 {
			if g.Kind == sigReport && out[i].Key != g.Key {
				g = joinReports(out[i], g)
			}
			if g.Kind == sigInbox && out[i].Key != g.Key {
				g = joinInbox(out[i], g)
			}
			out[i] = g
		} else {
			out = append(out, g)
		}
	}
	return out
}

// replaceQueuedSupervise: a supervision message still waits in the queue: msg takes its place (true).
func (s *Service) replaceQueuedSupervise(to string, msg relayMsg) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return false
	}
	q := b.st.Queue[to]
	for i := range q {
		if q[i].Tool == superviseTool && (i > 0 || !b.draining[to]) {
			q[i] = msg.replacing(q[i], time.Now()) // it waited since then (relay_presence.go)
			s.saveRelayLocked()
			return true
		}
	}
	return false
}

// digestEntry is signal g as the message lists it; false when it no longer holds.
func (s *Service) digestEntry(ctx context.Context, g superviseSignal, web string) (prompts.SuperviseEntry, bool) {
	t, ok := s.todoNow(g.Todo)
	if !ok || t.Supervision == nil || t.DoneAt != nil && g.Kind != sigFailed {
		return prompts.SuperviseEntry{}, false
	}
	v := t.Supervision
	st := v.state(t)
	var replyAsk acceptAsk
	var draft *TodoPRStatus
	switch g.Kind {
	case sigDraftPR:
		if t.PRURL != g.PRURL && !slices.Contains(t.PRs, g.PRURL) {
			return prompts.SuperviseEntry{}, false
		}
		draft = prFacts(t, g.PRURL)
		if draft == nil || draft.State != "open" || !draft.Draft || !draft.Own || draft.HeadOID == "" ||
			g.Key != draft.URL+"@"+draft.HeadOID || draft.CheckedAt.IsZero() || !s.stageFollows(t.ID, draft.URL) {
			return prompts.SuperviseEntry{}, false
		}
		if age := time.Since(draft.CheckedAt); age < 0 || age > triagePRStatusFreshFor {
			return prompts.SuperviseEntry{}, false
		}
	case sigDecisionReply:
		a, ok := s.askCopy(t.ID)
		if st != reviewNeedsUser || !ok || !a.AcceptedAt.IsZero() || a.AgentReply == nil ||
			!a.AgentReply.ToldAt.IsZero() || agentDecisionReplyKey(a) != g.Key || acceptAskStale(&a, t) || acceptAskEnded(&a, t) {
			return prompts.SuperviseEntry{}, false
		}
		replyAsk = a
	case sigPRConflict:
		p := t.PRStatus
		if p == nil || p.URL != t.PRURL || p.State != "open" || !p.Conflicts || p.HeadOID == "" ||
			g.Key != p.URL+"@"+p.HeadOID || s.stagePR(t.ID) != p.URL {
			return prompts.SuperviseEntry{}, false // resolved, closed, or replaced while the signal waited
		}
	case sigReview:
		if st != reviewPending {
			return prompts.SuperviseEntry{}, false
		}
	case sigNeedsYou:
		if !s.taskWaits(ctx, t) {
			return prompts.SuperviseEntry{}, false // answered meanwhile
		}
	case sigDecide:
		if g.Unreachable {
			// The leader could not be reached: the worker's own state is not what decides this (the leader's task is still open).
		} else if st != reviewNeedsUser {
			return prompts.SuperviseEntry{}, false // decided meanwhile
		}
	case sigEscalated:
		if st != reviewNeedsUser && len(g.Cards) == 0 { // a leader's escalated cards are told as they were
			return prompts.SuperviseEntry{}, false // decided meanwhile
		}
	case sigOverdue:
		if b, ok := s.briefOfTodo(t.ID); !ok || b.Overdue == nil || st == reviewNeedsUser || st == reviewParked {
			return prompts.SuperviseEntry{}, false // re-scoped, with the owner, or parked meanwhile
		}
	case sigAllStopped:
		fresh, holds := s.checkedAllStopped(ctx, t, g)
		if !holds {
			return prompts.SuperviseEntry{}, false // a member resumed or gained a wake/card while queued
		}
		g.Detail, g.Minutes = fresh.Detail, fresh.Minutes
	case sigNoProgress:
		if st != "" || !s.noProgressHolds(g) {
			return prompts.SuperviseEntry{}, false // progress came, or it was parked or reviewed, meanwhile
		}
	case sigIdle, sigUnjudged, sigStalled:
		if st == reviewParked {
			return prompts.SuperviseEntry{}, false // parked meanwhile: it waits on purpose (parked.go)
		}
	case sigCheck:
		if g.Report == checkNoProgressReport && st == reviewParked {
			return prompts.SuperviseEntry{}, false // parked meanwhile: it waits on purpose (parked.go)
		}
	case sigParkedDue:
		if r := v.parking(); st != reviewParked || r == nil || parkKey(r) != g.Key {
			return prompts.SuperviseEntry{}, false // its agent works again, or it was reviewed again, meanwhile
		}
	case sigParkDecided:
		if r := v.decidedPark(); r == nil || parkDecidedKey(r) != g.Key {
			return prompts.SuperviseEntry{}, false // told by another message meanwhile (a later decision's)
		}
	case sigOrphaned:
		if o, ok := s.orphanOf(t.ID); !ok || o.key != g.Key {
			return prompts.SuperviseEntry{}, false // an agent is back at it, or it is another stretch by now
		}
	case sigUndecidable:
		if st != reviewNeedsUser {
			return prompts.SuperviseEntry{}, false // decided or reviewed again meanwhile
		}
	case sigRecheck:
		if a, ok := s.askCopy(t.ID); st != reviewNeedsUser || !ok || !a.AcceptedAt.IsZero() || a.AgentReply != nil ||
			"recheck@"+a.AskedAt.UTC().Format(time.RFC3339Nano) != g.Key {
			return prompts.SuperviseEntry{}, false // answered, or asked again, meanwhile
		}
	case sigInbox:
		if g.Inbox == nil || !s.inboxSignalHolds(t) || !s.inboxSignalsOn(ctx) {
			return prompts.SuperviseEntry{}, false // waits for the owner's decision, or the switch went off, meanwhile
		}
		if g.Inbox.ToAgent == "unhandled" && !s.taskInboxUnhandled(ctx, g.Key) {
			return prompts.SuperviseEntry{}, false // the source event was answered while its notice waited
		}
	}
	var asks []prompts.SuperviseAsk
	if g.Kind == sigAsk {
		if asks = s.heldAsks(t.ID, g.To); len(asks) == 0 {
			return prompts.SuperviseEntry{}, false // answered, or gone up a rung, meanwhile
		}
	}
	e := prompts.SuperviseEntry{ID: t.ID, Title: t.Title, Kind: g.Kind, Detail: g.Detail, Minutes: g.Minutes, DoneWhen: v.DoneWhen,
		Round: v.Round, Link: todoLink(web, t.ID), Quiet: t.Quiet}
	if draft != nil {
		e.Detail = prompts.RenderDraftPR(prompts.DraftPR{Language: answerLanguage(string(s.botLang(ctx))),
			URL: draft.URL, Head: draft.HeadOID})
	}
	// The last review, shown only in a Fresh message (supervise_round.go): a decision reply's round confirms the decision
	// that review put to the owner.
	e.LastReview = s.lastReviewEntry(ctx, v)
	if g.Kind == sigDecisionReply {
		e.AgentReply = &prompts.DecisionAgentReply{Language: answerLanguage(string(s.botLang(ctx))),
			Machine: replyAsk.AgentReply.MachineID, Session: replyAsk.AgentReply.SessionID}
		return e, true // the reply's exact session is in AgentReply; no arbitrary last Agent is evidence
	}
	if a := v.AutoReview; g.Kind == sigReview && a != nil && a.Round == v.Round && (a.State == autoClean || a.State == autoBlocking || a.State == autoFailed) {
		e.Review = &prompts.SuperviseReviewed{State: a.State, Count: a.Count, Blocking: a.Blocking, Findings: a.Findings, Error: a.Error}
	}
	s.briefEntry(ctx, t.ID, &e) // what was asked, in the owner's words (task_brief_digest.go)
	if g.Kind == sigEscalated {
		e.Recommend = g.Ask // the leader's recommendation (escalate)
		e.Asks = g.Cards
		e.Background = !s.isLeader(ctx, v.Supervisor) // only the background supervisor escalates a task no leader supervises
	}
	if g.Kind == sigDecide {
		e.Recommend = g.Ask // the task leader's recommendation
		e.LeaderGone = g.Unreachable
	}
	if g.Kind == sigAsk {
		e.Asks, e.Decisions = asks, s.todoDecisionsContext(ctx, t.ID) // what was settled already, not to be gone against
	}
	if g.Kind == sigReport { // what it said is the report itself, not a reason in brackets
		e.Detail, e.Report, e.Said, e.Evidence, e.Step, e.Steps = "", g.Report, g.Detail, g.Evidence, g.Step, g.Steps
	}
	// Progress reports stay quiet. Their persisted evidence accompanies the next
	// real supervision event, even after restart or a report of a different kind.
	for i, c := range v.Criteria {
		if c.At != nil {
			e.Evidence = append(e.Evidence, fmt.Sprintf("criterion %d [%s] %s — %s (%s at %s)",
				i+1, c.State, c.Criterion, c.Evidence, c.By, c.At.UTC().Format(time.RFC3339)))
		}
	}
	if g.Kind == sigOrphaned {
		e.Orphan = g.Orphan
	}
	if g.Kind == sigNoProgress {
		e.Calls, e.Waits = g.Calls, g.Waits
	}
	if g.Kind == sigRecheck || g.Kind == sigOwnerCannot {
		e.Decision = g.Decision
	}
	if g.Kind == sigParkedDue { // what it waited for is the signal, not a reason in brackets
		e.Detail, e.Parked = "", s.parkedEntry(ctx, v.parking())
	}
	if g.Kind == sigParkDecided && st == reviewParked { // parked again before this went: what it waits for now
		e.Parked = s.parkedEntry(ctx, v.parking())
	}
	if g.Kind == sigParkedDue || g.Kind == sigParkDecided {
		// What was decided on the task meanwhile: the answer it waited for may be there (#8044).
		e.Decisions = s.todoDecisionsContext(ctx, t.ID)
	}
	if g.Kind == sigOwnerCannot { // the owner's answer is the signal, not a reason in brackets
		e.Detail, e.Response = "", g.Detail
	}
	if g.Kind == sigCheck {
		e.Report = g.Report
		if g.Report == checkDecidedReport { // the option the owner took, and the decision as it was asked
			e.Detail, e.Response, e.Decision = "", g.Detail, g.Decision
		}
	}
	if g.Kind == sigFailed {
		s.remedyEntry(ctx, t, &e) // its kept tab, and the turn that hears of it (start_remedy.go)
	}
	if g.Kind == sigInbox {
		in := *g.Inbox
		e.Inbox = &in
	}
	b := &s.supervise
	b.mu.Lock()
	e.Ask = cmp.Or(g.Ask, b.asks[t.ID])
	b.mu.Unlock()
	if g.Kind != sigNeedsYou {
		e.Ask = ""
	}
	if g.Kind == sigAllStopped {
		return e, true // Detail covers every participant; no arbitrary last agent in the fingerprint
	}
	if g.Kind == sigInbox && g.Inbox.ToAgent == "unhandled" {
		// This failure belongs to the delivered turn, not a successor's current reply.
		e.AgentID, e.Session, e.Machine = g.Session, short(g.Session), g.Machine
		if m, ok := s.device(ctx, g.Machine); ok {
			e.Machine = m.Name
		}
		return e, true
	}
	// Where its agent is and what it said last: the whole last answer when it stopped, a line while it works.
	agents := s.taskAgents(ctx, t)
	var last proto.Session
	for _, c := range agents {
		if g.Session != "" && c.SessionID == g.Session || g.Session == "" && c.UpdatedAt.After(last.UpdatedAt) {
			last = c
		}
	}
	if last.SessionID != "" {
		e.Session = short(last.SessionID)
		e.AgentID = last.SessionID
		if m, ok := s.device(ctx, last.MachineID); ok {
			e.Machine = m.Name
		}
		text := strings.TrimSpace(last.LastText)
		if g.Kind == sigReport {
			text = "" // it said what it had to in the report
		} else if g.Kind == sigReview || g.Kind == sigIdle || g.Kind == sigUnjudged {
			if answer := s.lastTurnAnswer(ctx, last.MachineID, last.SessionID); answer != "" {
				text = answer
			}
		}
		// The files it handed over, copied to this conversation's computer (task_files.go, #10511); the receipt path
		// usually did it already, and a copy here only fills in after a restart.
		s.deliverTaskFiles(ctx, t, last.MachineID, s.conversationMachine(ctx, s.taskReceiver(ctx, t), ""), text)
		e.Files = taskFilePrompts(s.taskFilesNow(t.ID))
		if c, arts, ok := closeout.Parse(text); ok {
			e.Closeout = &prompts.SuperviseCloseout{Status: c.Status, Waiting: c.Waiting, Merged: c.Merged, Deployed: c.Deployed,
				Did: c.Did, Verified: c.Verified, Missing: c.Missing}
			if s.checkCloseout(ctx, &c, arts); c.Checked != nil {
				e.Closeout.CheckedMerged, e.Closeout.CheckedDeployed = c.Checked.Merged, c.Checked.Deployed
				e.Closeout.LocalDeployment = c.Checked.LocalDeployment
			}
			for _, a := range arts {
				e.Closeout.Artifacts = append(e.Closeout.Artifacts, a.Kind+" "+a.Ref)
			}
			text = closeout.Strip(text) // told above, in a form that cannot be cut in the middle
		}
		e.Last = clipMiddle(text, 600)
	} else if g.Machine != "" {
		e.Machine = g.Machine
	}
	return e, true
}

// sortEntries puts what waits on a decision first.
func sortEntries(list []prompts.SuperviseEntry) []prompts.SuperviseEntry {
	rank := map[string]int{sigNeedsYou: 0, sigAsk: 0, sigEscalated: 0, sigDecide: 0, sigOwnerCannot: 0, sigUndecidable: 1, sigRecheck: 1, sigParkedDue: 2, sigParkDecided: 2, sigIdle: 2, sigUnjudged: 2, sigAllStopped: 2, sigFailed: 3, sigStarted: 3, sigInterrupted: 4, sigOrphaned: 4, sigReview: 5, sigStalled: 6, sigNoProgress: 6, sigLeaderStuck: 6, sigOverdue: 6, sigInbox: 6, sigReport: 7}
	at := func(e prompts.SuperviseEntry) int {
		switch {
		case e.Kind == sigCheck && e.Report == checkDecidedReport: // the owner's answer
			return rank[sigOwnerCannot]
		case e.Kind == sigCheck:
			return rank[sigStalled]
		case e.Kind != sigReport:
		case e.Report == upReportQuestion || e.Report == upReportBlocked: // waits on a decision too
			return 1
		case e.Report == upReportDone: // with the work waiting for review
			return rank[sigReview]
		}
		return rank[e.Kind]
	}
	sort.SliceStable(list, func(i, j int) bool { return at(list[i]) < at(list[j]) })
	return list
}

// ---- the main conversation's pushes ----

// holdPushes: a supervision message went into conversation id: its turn's normal finish is not pushed (the hub and the
// channels read it off the snapshot, SessionView.Supervising) until the user or anything else speaks there.
func (s *Service) holdPushes(ctx context.Context, id string) {
	b := &s.supervise
	b.mu.Lock()
	b.init()
	b.hold[id] = time.Now()
	delete(b.visible, id)
	b.mu.Unlock()
	s.publish(ctx)
}

// holdPushesParam marks an assistant.send whose supervision turn holds the conversation's pushes (holdPushes).
// Any other send releases them (call); scheduled inputs carry their own persisted push mode.
const holdPushesParam = "hold_pushes"

// releasePushes: someone else speaks in conversation id: its turns are pushed as ever.
func (s *Service) releasePushes(ctx context.Context, id string) {
	b := &s.supervise
	b.mu.Lock()
	_, held := b.hold[id]
	delete(b.hold, id)
	delete(b.visible, id)
	b.mu.Unlock()
	if held {
		s.publish(ctx)
	}
}

// supervising: conversation id's current turn was started by a supervision message.
func (s *Service) supervising(id string) bool {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.hold[id]
	return ok
}

func (s *Service) heldConversations() []string {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.hold))
	for id := range b.hold {
		out = append(out, id)
	}
	return out
}
