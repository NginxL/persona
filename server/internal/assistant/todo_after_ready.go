package assistant

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Deterministic todo.after dependency auto-advance (#12606).
//
// When a prerequisite to-do completes (todo_complete / task_review accepted / autoDoneTodo) or its bound delivery PRs
// transition to MERGED (applyPRStatus), the hub deterministically checks open, unstarted dependent to-dos whose after
// list names that prerequisite. When every prerequisite in a dependent's after list is satisfied, the hub emits one
// durable, idempotent todo_ready supervision signal to the dependent's owning supervisor conversation (Main chat or
// Leader). It never auto-starts unapproved work silently.

const todoReadyWakesKey = "assistant.todo_ready_wakes"

// todoReadyWakeMarker records that dependent to-do TodoID was already woken for canonical prerequisite set Key.
type todoReadyWakeMarker struct {
	Key    string    `json:"key"`
	Target string    `json:"target,omitempty"`
	At     time.Time `json:"at"`
}

// todoHasActiveAgent reports whether any Agent is currently active on to-do t.
func todoHasActiveAgent(t Todo) bool {
	return slices.ContainsFunc(t.Agents, func(a TodoAgent) bool { return a.active() })
}

// normalizePRKey returns a canonical comparison key for a PR URL across stage records and repository bindings.
func normalizePRKey(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if ciPRURL.MatchString(u) {
		return triageNormalizePRURL(u)
	}
	return strings.ToLower(strings.TrimRight(u, "/"))
}

// todoDeliveryPRs returns the deduplicated set of bound delivery PR URLs for to-do t using local state only (no
// network calls). Saved repository links with role "reference" are excluded even if they also appeared in t.PRURL or
// t.PRs. Any repository read error other than genuine not-found (including store_unavailable) is returned so callers
// fail closed rather than falsely satisfying a merge prerequisite.
func (s *Service) todoDeliveryPRs(ctx context.Context, t Todo) ([]string, error) {
	refPRs := map[string]bool{}
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		key := normalizePRKey(u)
		if key == "" || refPRs[key] || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, u)
	}

	if _, hasRepoStore := s.store().(todoRepositoryStore); hasRepoStore {
		set, err := s.savedTodoRepositories(ctx, t.ID)
		if err != nil {
			var problem repository.Problem
			if !errors.Is(err, db.ErrTodoNotFound) && (!errors.As(err, &problem) || problem != "not_found") {
				s.logf("todo_ready: read repositories for prerequisite %s: %v", t.ID, err)
				return nil, err
			}
		} else {
			for _, row := range set.Repositories {
				for _, l := range row.Links {
					if l.Kind != "pull_request" {
						continue
					}
					u := strings.TrimSpace(l.CanonicalURL)
					if u == "" {
						continue
					}
					role := l.Role
					if role == "" {
						role = repository.LinkRoleDelivery
					}
					if role == repository.LinkRoleReference {
						refPRs[normalizePRKey(u)] = true
						continue
					}
					add(u)
				}
			}
		}
	}

	for _, u := range append([]string{t.PRURL}, t.PRs...) {
		u = strings.TrimSpace(u)
		if u == "" || !ciPRURL.MatchString(u) && !otherReviewLink(u) {
			continue
		}
		add(u)
	}
	return out, nil
}

// todoObservedPRState finds the latest observed PR status for url on to-do t, matching exact or normalized URLs.
func todoObservedPRState(t Todo, url string) (TodoPRStatus, bool) {
	if f := prFacts(t, url); f != nil {
		return *f, true
	}
	want := normalizePRKey(url)
	if want == "" {
		return TodoPRStatus{}, false
	}
	if t.PRStatus != nil && normalizePRKey(t.PRStatus.URL) == want {
		return *t.PRStatus, true
	}
	for _, f := range t.OtherPRs {
		if normalizePRKey(f.URL) == want {
			return f, true
		}
	}
	return TodoPRStatus{}, false
}

// todoObservedMergedPRs returns the merged PR URLs known for prerequisite t, preserving delivery order and never
// fabricating merged status for an open or unmerged-closed PR.
func todoObservedMergedPRs(t Todo, delivery []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		key := normalizePRKey(u)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, u)
	}
	for _, u := range delivery {
		if f, ok := todoObservedPRState(t, u); ok && strings.EqualFold(f.State, "merged") {
			add(u)
		}
	}
	if t.PRStatus != nil && strings.EqualFold(t.PRStatus.State, "merged") {
		add(t.PRStatus.URL)
	}
	for _, f := range t.OtherPRs {
		if strings.EqualFold(f.State, "merged") {
			add(f.URL)
		}
	}
	if t.DoneAt != nil && t.PRURL != "" && t.PRStatus == nil &&
		t.Delivery != nil && (t.Delivery.State == deliveryMerged || t.Delivery.State == deliveryLive) {
		add(t.PRURL)
	}
	return out
}

// prerequisiteSatisfied reports whether prerequisite dep counts as satisfied under exact-delivery rules:
//   - Cancelled or failed-start to-dos never satisfy a dependency.
//   - Under a RequireAcceptedAfter root, dep must be accepted and closed via task_review accepted.
//   - Otherwise, dep is satisfied when it is done, or when it has at least one bound delivery PR and every bound
//     delivery PR is observed as MERGED (merely opening or closing without merging never satisfies).
//
// A non-nil error indicates a transient store read failure while verifying an open prerequisite's delivery PRs.
func (s *Service) prerequisiteSatisfied(ctx context.Context, dep Todo, requireAccepted bool) (prompts.SupervisePrerequisite, bool, error) {
	if dep.Cancelled() || dep.StartError != "" {
		return prompts.SupervisePrerequisite{}, false, nil
	}
	delivery, deliveryErr := s.todoDeliveryPRs(ctx, dep)
	if requireAccepted {
		if dep.DoneAt == nil || dep.DoneBy != db.DoneByReview || dep.Supervision == nil || dep.Supervision.state(dep) != reviewAccepted {
			return prompts.SupervisePrerequisite{}, false, nil
		}
		return prompts.SupervisePrerequisite{
			ID:        dep.ID,
			Title:     dep.Title,
			Reason:    "done",
			MergedPRs: todoObservedMergedPRs(dep, delivery),
		}, true, nil
	}
	if dep.DoneAt != nil {
		return prompts.SupervisePrerequisite{
			ID:        dep.ID,
			Title:     dep.Title,
			Reason:    "done",
			MergedPRs: todoObservedMergedPRs(dep, delivery),
		}, true, nil
	}
	if deliveryErr != nil {
		return prompts.SupervisePrerequisite{}, false, deliveryErr
	}
	if len(delivery) == 0 {
		return prompts.SupervisePrerequisite{}, false, nil
	}
	for _, u := range delivery {
		f, ok := todoObservedPRState(dep, u)
		if !ok || !strings.EqualFold(f.State, "merged") {
			return prompts.SupervisePrerequisite{}, false, nil
		}
	}
	return prompts.SupervisePrerequisite{
		ID:        dep.ID,
		Title:     dep.Title,
		Reason:    "pr_merged",
		MergedPRs: delivery,
	}, true, nil
}

// checkTodoAfterReady checks whether candidate is an open, unstarted dependent to-do whose after prerequisites are
// all satisfied, distinguishing a transient store read failure (err != nil) from a genuinely unready/ineligible to-do.
func (s *Service) checkTodoAfterReady(ctx context.Context, candidate Todo) ([]prompts.SupervisePrerequisite, bool, error) {
	if candidate.DoneAt != nil || candidate.Cancelled() || candidate.StartError != "" ||
		candidate.isConcern() || isAskTodo(candidate.ID) {
		return nil, false, nil
	}
	afterIDs := cleanIDs(candidate.After)
	if len(afterIDs) == 0 {
		return nil, false, nil
	}
	if todoHasActiveAgent(candidate) || s.requestStartPending(ctx, candidate) || s.workspaceQueuePending(ctx, candidate) {
		return nil, false, nil
	}

	s.todos.mu.RLock()
	requireAccepted := candidate.PartOf != "" && s.hasRequestGateLocked(candidate.PartOf)
	deps := make([]Todo, 0, len(afterIDs))
	missing := false
	for _, id := range afterIDs {
		dep, ok := s.todos.todos[id]
		if !ok {
			missing = true
			break
		}
		deps = append(deps, copyTodoLocked(dep))
	}
	s.todos.mu.RUnlock()
	if missing || len(deps) == 0 {
		return nil, false, nil
	}

	out := make([]prompts.SupervisePrerequisite, 0, len(deps))
	for _, dep := range deps {
		p, ok, err := s.prerequisiteSatisfied(ctx, dep, requireAccepted)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, nil
		}
		out = append(out, p)
	}
	return out, true, nil
}

// todoAfterReadyState is checkTodoAfterReady when callers only need a boolean readiness verdict.
func (s *Service) todoAfterReadyState(ctx context.Context, candidate Todo) ([]prompts.SupervisePrerequisite, bool) {
	prereqs, ready, err := s.checkTodoAfterReady(ctx, candidate)
	return prereqs, ready && err == nil
}

// clearSentTodoReady removes in-memory sent markers for sigTodoReady signals that were dropped before delivery, so a
// later recovery or re-completion is never permanently suppressed until restart. Delivered wakes remain deduplicated
// by assistant.todo_ready_wakes and assistant.supervision_dedup.
func (s *Service) clearSentTodoReady(list []superviseSignal) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, g := range list {
		if g.Kind == sigTodoReady && b.sent[g.id()] == g.Key {
			delete(b.sent, g.id())
		}
	}
}

// resetTodoReadyWake clears pending/sent in-memory todo_ready signals, the durable assistant.todo_ready_wakes marker,
// and the task-bound todo_ready supervision dedup fingerprint for todoID when that to-do is reopened, so a subsequent
// readiness transition can wake its owner once.
func (s *Service) resetTodoReadyWake(ctx context.Context, todoID string) {
	todoID = strings.TrimSpace(todoID)
	if todoID == "" {
		return
	}
	sigID := superviseSignal{Todo: todoID, Kind: sigTodoReady}.id()
	b := &s.supervise
	b.mu.Lock()
	delete(b.pending, sigID)
	delete(b.sent, sigID)
	b.mu.Unlock()

	tb := &s.todos
	tb.readyWakeMu.Lock()
	if err := s.loadTodoReadyWakesLocked(ctx); err != nil {
		s.logf("todo_ready: load wake markers for reset (%s): %v", todoID, err)
	} else if _, ok := tb.readyWakes[todoID]; ok {
		next := maps.Clone(tb.readyWakes)
		delete(next, todoID)
		if st := s.store(); st != nil {
			if err := st.PutSetting(ctx, todoReadyWakesKey, next); err != nil {
				s.logf("todo_ready: clear wake marker for %s: %v", todoID, err)
			} else {
				tb.readyWakes = next
			}
		} else {
			tb.readyWakes = next
		}
	}
	tb.readyWakeMu.Unlock()

	s.clearTodoReadyDedup(ctx, todoID)
}

// todoReadyKey is the canonical idempotency key for candidate's current after prerequisite set and reopen generation.
// It is independent of prerequisite order and of whether a prerequisite was satisfied first via PR merge and later
// marked done. Truncating ReopenedAt to microsecond precision keeps keys stable across Postgres round-trips.
func todoReadyKey(candidate Todo) string {
	ids := cleanIDs(candidate.After)
	sort.Strings(ids)
	key := "after:" + strings.Join(ids, ",")
	if candidate.ReopenedAt != nil && !candidate.ReopenedAt.IsZero() {
		key += "@reopen:" + candidate.ReopenedAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	}
	return key
}

// todoReadySupervisor resolves the owning supervisor conversation (Leader or Main chat) for an unstarted dependent
// to-do. A to-do owned by a task leader agent returns "" so leaderRoute delivers to that agent; otherwise Main-owned
// to-dos go directly to cfg.Main (never to the background supervisor).
func (s *Service) todoReadySupervisor(ctx context.Context, cfg Settings, candidate Todo) string {
	isLiveConversation := func(id string) bool {
		if id == "" || id == cfg.Main || id == cfg.BackgroundSupervisor || s.isLeaderRef(id) {
			return false
		}
		if s.isLeader(ctx, id) {
			return true
		}
		if cfg.MachineID != "" {
			if c, ok := s.session(ctx, cfg.MachineID, id); ok && c.Assistant {
				return true
			}
		}
		return false
	}

	if sup := supervisorOf(candidate); sup != "" {
		if s.isLeaderRef(sup) {
			return ""
		}
		if isLiveConversation(sup) {
			return sup
		}
		if sup == cfg.Main {
			return cfg.Main
		}
	}
	if orig := candidate.Origin; orig != "" {
		if isLiveConversation(orig) {
			return orig
		}
		if orig == cfg.Main {
			return cfg.Main
		}
	}
	if candidate.PartOf != "" {
		if root, ok := s.todoNow(candidate.PartOf); ok {
			if sup := supervisorOf(root); s.isLeader(ctx, sup) {
				return sup
			}
			if s.isLeader(ctx, root.Origin) {
				return root.Origin
			}
		}
	}
	// If all prerequisites belong to one leader conversation, wake that leader.
	var sharedLeader string
	ambiguous := false
	for _, depID := range cleanIDs(candidate.After) {
		dep, ok := s.todoNow(depID)
		if !ok {
			continue
		}
		lead := ""
		if sup := supervisorOf(dep); s.isLeader(ctx, sup) {
			lead = sup
		} else if s.isLeader(ctx, dep.Origin) {
			lead = dep.Origin
		}
		if lead == "" {
			continue
		}
		if sharedLeader == "" {
			sharedLeader = lead
		} else if sharedLeader != lead {
			ambiguous = true
			break
		}
	}
	if sharedLeader != "" && !ambiguous {
		return sharedLeader
	}
	return cfg.Main
}

// loadTodoReadyWakesLocked loads durable wake markers from the settings store once per Service lifetime.
// s.todos.readyWakeMu must be held.
func (s *Service) loadTodoReadyWakesLocked(ctx context.Context) error {
	b := &s.todos
	if b.readyWakesLoaded {
		if b.readyWakes == nil {
			b.readyWakes = map[string]todoReadyWakeMarker{}
		}
		return nil
	}
	if st := s.store(); st != nil {
		loaded := map[string]todoReadyWakeMarker{}
		if _, err := st.Setting(ctx, todoReadyWakesKey, &loaded); err != nil {
			return err
		}
		b.readyWakes = loaded
	} else if b.readyWakes == nil {
		b.readyWakes = map[string]todoReadyWakeMarker{}
	}
	b.readyWakesLoaded = true
	return nil
}

// todoReadyWakeRecorded reports whether dependent todoID already has a persisted wake marker for key.
func (s *Service) todoReadyWakeRecorded(ctx context.Context, todoID, key string) (bool, error) {
	b := &s.todos
	b.readyWakeMu.Lock()
	defer b.readyWakeMu.Unlock()
	if err := s.loadTodoReadyWakesLocked(ctx); err != nil {
		return false, err
	}
	m, ok := b.readyWakes[todoID]
	return ok && m.Key == key, nil
}

// todoReadyTold persists idempotent wake markers for every sigTodoReady signal that was delivered or durably queued,
// so restarts and repeated sweeps never re-emit duplicate wakes.
func (s *Service) todoReadyTold(ctx context.Context, list []superviseSignal) {
	var ready []superviseSignal
	for _, g := range list {
		if g.Kind == sigTodoReady && g.Todo != "" && g.Key != "" {
			ready = append(ready, g)
		}
	}
	if len(ready) == 0 {
		return
	}
	b := &s.todos
	b.readyWakeMu.Lock()
	defer b.readyWakeMu.Unlock()
	if err := s.loadTodoReadyWakesLocked(ctx); err != nil {
		s.logf("todo_ready: load wake markers: %v", err)
		return
	}
	next := maps.Clone(b.readyWakes)
	if next == nil {
		next = map[string]todoReadyWakeMarker{}
	}
	now := time.Now().UTC()
	changed := false
	for _, g := range ready {
		if cur, ok := next[g.Todo]; ok && cur.Key == g.Key {
			continue
		}
		next[g.Todo] = todoReadyWakeMarker{Key: g.Key, Target: g.To, At: now}
		changed = true
	}
	if !changed {
		return
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, todoReadyWakesKey, next); err != nil {
			s.logf("todo_ready: persist wake markers: %v", err)
			return
		}
	}
	b.readyWakes = next
}

// pruneTodoReadyWakes removes wake markers for to-dos that have been completed or deleted so the stored map stays
// bounded by open dependent to-dos.
func (s *Service) pruneTodoReadyWakes(ctx context.Context) {
	b := &s.todos
	b.readyWakeMu.Lock()
	defer b.readyWakeMu.Unlock()
	if err := s.loadTodoReadyWakesLocked(ctx); err != nil {
		s.logf("todo_ready: load wake markers for prune: %v", err)
		return
	}
	if len(b.readyWakes) == 0 {
		return
	}
	var remove []string
	b.mu.RLock()
	if b.loaded {
		for id := range b.readyWakes {
			if t := b.todos[id]; t == nil || t.DoneAt != nil {
				remove = append(remove, id)
			}
		}
	}
	b.mu.RUnlock()
	if len(remove) == 0 {
		return
	}
	next := maps.Clone(b.readyWakes)
	for _, id := range remove {
		delete(next, id)
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, todoReadyWakesKey, next); err != nil {
			s.logf("todo_ready: prune wake markers: %v", err)
			return
		}
	}
	b.readyWakes = next
}

// maybeEmitTodoReady evaluates candidate and, if ready and not already woken for its current after set, queues a
// todo_ready supervision signal for its owning supervisor conversation.
func (s *Service) maybeEmitTodoReady(ctx context.Context, candidate Todo) {
	if _, ready := s.todoAfterReadyState(ctx, candidate); !ready {
		return
	}
	key := todoReadyKey(candidate)
	recorded, err := s.todoReadyWakeRecorded(ctx, candidate.ID, key)
	if err != nil {
		s.logf("todo_ready: check wake marker for %s: %v", candidate.ID, err)
		return
	}
	if recorded {
		return
	}
	cfg := s.settings(ctx)
	to := s.todoReadySupervisor(ctx, cfg, candidate)
	s.signal(superviseSignal{
		Todo: candidate.ID,
		Kind: sigTodoReady,
		Key:  key,
		To:   to,
	})
}

// evaluateTodoAfterDependents deterministically checks every open dependent to-do whose after list names prereqID.
func (s *Service) evaluateTodoAfterDependents(ctx context.Context, prereqID string) {
	prereqID = strings.TrimSpace(prereqID)
	if prereqID == "" {
		return
	}
	s.todos.mu.RLock()
	var prereq Todo
	var hasPrereq bool
	if p, ok := s.todos.todos[prereqID]; ok {
		prereq, hasPrereq = copyTodoLocked(p), true
	}
	var candidates []Todo
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && slices.Contains(t.After, prereqID) {
			candidates = append(candidates, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	if len(candidates) == 0 {
		return
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for _, c := range candidates {
		s.maybeEmitTodoReady(ctx, c)
		if hasPrereq && needsUserWakeEligible(c) {
			s.todos.mu.RLock()
			requireAccepted := c.PartOf != "" && s.hasRequestGateLocked(c.PartOf)
			s.todos.mu.RUnlock()
			if p, ok, err := s.prerequisiteSatisfied(ctx, prereq, requireAccepted); err == nil && ok {
				summary := "Prerequisite " + p.Title + " (" + p.ID + ") is now " + p.Reason
				if len(p.MergedPRs) > 0 {
					summary += " (" + strings.Join(p.MergedPRs, ", ") + ")"
				}
				s.emitNeedsUserExternalInfo(ctx, c, "prerequisite "+p.ID, summary, "prereq:"+p.ID+":"+p.Reason)
			}
		}
	}
}

// sweepTodoAfterReady reconciles open dependent to-dos on startup / supervision sweeps so wakes lost before delivery
// are recovered while already-delivered wakes stay suppressed by their persisted markers.
func (s *Service) sweepTodoAfterReady(ctx context.Context) {
	if s.ensureTodosLoaded(ctx) != nil {
		return
	}
	s.pruneTodoReadyWakes(ctx)
	s.todos.mu.RLock()
	var candidates []Todo
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && len(t.After) > 0 {
			candidates = append(candidates, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	if len(candidates) == 0 {
		return
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for _, c := range candidates {
		s.maybeEmitTodoReady(ctx, c)
	}
}
