package assistant

import (
	"context"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// Deterministic parent to-do completion wake (#13100).
//
// When a part_of child to-do completes (todo_complete / task_review accepted / autoDoneTodo), the hub checks its open
// parent to-do. When the parent is still open, has no active Agent, and every real part_of child (at least one) is done,
// the hub emits one durable, idempotent parent_closeout wake to the parent's owning conversation (Main chat or Leader,
// never the background supervisor), listing the completed children and their delivery/merged PRs. Plain personal to-dos
// are never silently auto-completed.

const parentCloseoutWakesKey = "assistant.parent_closeout_wakes"

// parentCloseoutWakeMarker records that open parent to-do TodoID was already woken for canonical children set Key.
type parentCloseoutWakeMarker struct {
	Key    string    `json:"key"`
	Target string    `json:"target,omitempty"`
	At     time.Time `json:"at"`
}

func isOutsideDecisionTodo(id string) bool {
	return strings.HasPrefix(id, "todo_od_")
}

// isPlainPersonalTodo reports whether t has no owning conversation, plan, or supervision ledger (made by hand).
func isPlainPersonalTodo(t Todo) bool {
	return strings.TrimSpace(t.Origin) == "" && strings.TrimSpace(t.PlanID) == "" && t.Supervision == nil
}

// parentCloseoutKey is the canonical idempotency key for parent's completed child set and reopen generation.
func parentCloseoutKey(parent Todo, children []Todo) string {
	ids := make([]string, 0, len(children))
	for _, c := range children {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	key := "children:" + strings.Join(ids, ",")
	if parent.ReopenedAt != nil && !parent.ReopenedAt.IsZero() {
		key += "@reopen:" + parent.ReopenedAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	}
	return key
}

// childDeliveryPRs returns the deduplicated merged/delivery PR URLs for a completed child to-do.
func (s *Service) childDeliveryPRs(ctx context.Context, child Todo) ([]string, error) {
	delivery, err := s.todoDeliveryPRs(ctx, child)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		k := normalizePRKey(u)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, u)
	}
	for _, u := range todoObservedMergedPRs(child, delivery) {
		add(u)
	}
	for _, u := range delivery {
		add(u)
	}
	return out, nil
}

// checkParentCloseoutReady reports whether parent is an open to-do with no active Agent whose real part_of children
// (at least one) are all done without cancellation or start failure.
// A non-nil error indicates a transient repository-store read failure while collecting child delivery PRs.
func (s *Service) checkParentCloseoutReady(ctx context.Context, parent Todo) ([]Todo, []prompts.ParentCloseoutChild, bool, error) {
	if parent.DoneAt != nil || parent.Cancelled() || parent.StartError != "" ||
		parent.isConcern() || isAskTodo(parent.ID) || isOutsideDecisionTodo(parent.ID) {
		return nil, nil, false, nil
	}
	if todoHasActiveAgent(parent) {
		return nil, nil, false, nil
	}

	s.todos.mu.RLock()
	var children []Todo
	for _, t := range s.todos.todos {
		if t.PartOf != parent.ID || t.ID == parent.ID || isAskTodo(t.ID) || isOutsideDecisionTodo(t.ID) {
			continue
		}
		children = append(children, copyTodoLocked(t))
	}
	s.todos.mu.RUnlock()
	if len(children) == 0 {
		return nil, nil, false, nil
	}
	sort.Slice(children, func(i, j int) bool {
		if !children[i].CreatedAt.Equal(children[j].CreatedAt) {
			return children[i].CreatedAt.Before(children[j].CreatedAt)
		}
		return children[i].ID < children[j].ID
	})
	for _, c := range children {
		if c.DoneAt == nil || c.Cancelled() || c.StartError != "" {
			return nil, nil, false, nil
		}
	}

	out := make([]prompts.ParentCloseoutChild, 0, len(children))
	for _, c := range children {
		prs, err := s.childDeliveryPRs(ctx, c)
		if err != nil {
			return nil, nil, false, err
		}
		out = append(out, prompts.ParentCloseoutChild{
			ID:    c.ID,
			Title: c.Title,
			PRs:   prs,
		})
	}
	return children, out, true, nil
}

// parentCloseoutSupervisor resolves the owning conversation (Main chat or Leader, never the background supervisor)
// for parent's closeout wake. A parent owned by a task leader agent returns "" so leaderRoute delivers to that agent.
func (s *Service) parentCloseoutSupervisor(ctx context.Context, cfg Settings, parent Todo, children []Todo) string {
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

	if sup := supervisorOf(parent); sup != "" {
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
	if orig := parent.Origin; orig != "" {
		if isLiveConversation(orig) {
			return orig
		}
		if orig == cfg.Main {
			return cfg.Main
		}
	}
	var sharedLeader string
	ambiguous := false
	for _, c := range children {
		lead := ""
		if sup := supervisorOf(c); s.isLeader(ctx, sup) {
			lead = sup
		} else if s.isLeader(ctx, c.Origin) {
			lead = c.Origin
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

// loadParentCloseoutWakesLocked loads durable wake markers once per Service lifetime.
// s.todos.parentCloseoutMu must be held.
func (s *Service) loadParentCloseoutWakesLocked(ctx context.Context) error {
	b := &s.todos
	if b.parentCloseoutWakesLoaded {
		if b.parentCloseoutWakes == nil {
			b.parentCloseoutWakes = map[string]parentCloseoutWakeMarker{}
		}
		return nil
	}
	if st := s.store(); st != nil {
		loaded := map[string]parentCloseoutWakeMarker{}
		if _, err := st.Setting(ctx, parentCloseoutWakesKey, &loaded); err != nil {
			return err
		}
		b.parentCloseoutWakes = loaded
	} else if b.parentCloseoutWakes == nil {
		b.parentCloseoutWakes = map[string]parentCloseoutWakeMarker{}
	}
	b.parentCloseoutWakesLoaded = true
	return nil
}

// parentCloseoutWakeRecorded reports whether parent todoID already has a persisted wake marker for key.
func (s *Service) parentCloseoutWakeRecorded(ctx context.Context, todoID, key string) (bool, error) {
	b := &s.todos
	b.parentCloseoutMu.Lock()
	defer b.parentCloseoutMu.Unlock()
	if err := s.loadParentCloseoutWakesLocked(ctx); err != nil {
		return false, err
	}
	m, ok := b.parentCloseoutWakes[todoID]
	return ok && m.Key == key, nil
}

// parentCloseoutTold persists idempotent wake markers for delivered or durably queued sigParentCloseout signals.
func (s *Service) parentCloseoutTold(ctx context.Context, list []superviseSignal) {
	var wakes []superviseSignal
	for _, g := range list {
		if g.Kind == sigParentCloseout && g.Todo != "" && g.Key != "" {
			wakes = append(wakes, g)
		}
	}
	if len(wakes) == 0 {
		return
	}
	b := &s.todos
	b.parentCloseoutMu.Lock()
	defer b.parentCloseoutMu.Unlock()
	if err := s.loadParentCloseoutWakesLocked(ctx); err != nil {
		s.logf("parent_closeout: load wake markers: %v", err)
		return
	}
	next := maps.Clone(b.parentCloseoutWakes)
	if next == nil {
		next = map[string]parentCloseoutWakeMarker{}
	}
	now := time.Now().UTC()
	changed := false
	for _, g := range wakes {
		if cur, ok := next[g.Todo]; ok && cur.Key == g.Key {
			continue
		}
		next[g.Todo] = parentCloseoutWakeMarker{Key: g.Key, Target: g.To, At: now}
		changed = true
	}
	if !changed {
		return
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, parentCloseoutWakesKey, next); err != nil {
			s.logf("parent_closeout: persist wake markers: %v", err)
			return
		}
	}
	b.parentCloseoutWakes = next
}

// pruneParentCloseoutWakes removes wake markers for closed or deleted parents.
func (s *Service) pruneParentCloseoutWakes(ctx context.Context) {
	b := &s.todos
	b.parentCloseoutMu.Lock()
	defer b.parentCloseoutMu.Unlock()
	if err := s.loadParentCloseoutWakesLocked(ctx); err != nil {
		s.logf("parent_closeout: load wake markers for prune: %v", err)
		return
	}
	if len(b.parentCloseoutWakes) == 0 {
		return
	}
	var remove []string
	b.mu.RLock()
	if b.loaded {
		for id := range b.parentCloseoutWakes {
			if t := b.todos[id]; t == nil || t.DoneAt != nil {
				remove = append(remove, id)
			}
		}
	}
	b.mu.RUnlock()
	if len(remove) == 0 {
		return
	}
	next := maps.Clone(b.parentCloseoutWakes)
	for _, id := range remove {
		delete(next, id)
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, parentCloseoutWakesKey, next); err != nil {
			s.logf("parent_closeout: prune wake markers: %v", err)
			return
		}
	}
	b.parentCloseoutWakes = next
}

// clearSentParentCloseout removes in-memory sent markers for sigParentCloseout signals dropped before delivery.
func (s *Service) clearSentParentCloseout(list []superviseSignal) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, g := range list {
		if g.Kind == sigParentCloseout && b.sent[g.id()] == g.Key {
			delete(b.sent, g.id())
		}
	}
}

// resetParentCloseoutWake clears pending/sent in-memory parent_closeout signals, the durable marker, and the
// task-bound supervision dedup fingerprint when parent todoID is reopened.
func (s *Service) resetParentCloseoutWake(ctx context.Context, todoID string) {
	todoID = strings.TrimSpace(todoID)
	if todoID == "" {
		return
	}
	sigID := superviseSignal{Todo: todoID, Kind: sigParentCloseout}.id()
	b := &s.supervise
	b.mu.Lock()
	delete(b.pending, sigID)
	delete(b.sent, sigID)
	b.mu.Unlock()

	tb := &s.todos
	tb.parentCloseoutMu.Lock()
	if err := s.loadParentCloseoutWakesLocked(ctx); err != nil {
		s.logf("parent_closeout: load wake markers for reset (%s): %v", todoID, err)
	} else if _, ok := tb.parentCloseoutWakes[todoID]; ok {
		next := maps.Clone(tb.parentCloseoutWakes)
		delete(next, todoID)
		if st := s.store(); st != nil {
			if err := st.PutSetting(ctx, parentCloseoutWakesKey, next); err != nil {
				s.logf("parent_closeout: clear wake marker for %s: %v", todoID, err)
			} else {
				tb.parentCloseoutWakes = next
			}
		} else {
			tb.parentCloseoutWakes = next
		}
	}
	tb.parentCloseoutMu.Unlock()

	s.clearParentCloseoutDedup(ctx, todoID)
}

// maybeEmitParentCloseout evaluates parent and, if all part_of children are complete and not already woken for this
// child set, queues one sigParentCloseout supervision signal for its owning conversation.
func (s *Service) maybeEmitParentCloseout(ctx context.Context, parent Todo) {
	children, _, ready, err := s.checkParentCloseoutReady(ctx, parent)
	if err != nil || !ready {
		return
	}
	key := parentCloseoutKey(parent, children)
	recorded, err := s.parentCloseoutWakeRecorded(ctx, parent.ID, key)
	if err != nil {
		s.logf("parent_closeout: check wake marker for %s: %v", parent.ID, err)
		return
	}
	if recorded {
		return
	}
	cfg := s.settings(ctx)
	to := s.parentCloseoutSupervisor(ctx, cfg, parent, children)
	s.signal(superviseSignal{
		Todo: parent.ID,
		Kind: sigParentCloseout,
		Key:  key,
		To:   to,
	})
}

// evaluateParentCloseoutForChild checks the part_of parent of completed childID.
func (s *Service) evaluateParentCloseoutForChild(ctx context.Context, childID string) {
	childID = strings.TrimSpace(childID)
	if childID == "" || isAskTodo(childID) || isOutsideDecisionTodo(childID) {
		return
	}
	child, ok := s.todoNow(childID)
	if !ok || strings.TrimSpace(child.PartOf) == "" || child.PartOf == child.ID {
		return
	}
	parent, ok := s.todoNow(child.PartOf)
	if !ok {
		return
	}
	s.maybeEmitParentCloseout(ctx, parent)
}

// sweepParentCloseout reconciles open parent to-dos on startup / supervision sweeps so wakes lost before delivery
// are recovered while already-delivered wakes stay suppressed by their persisted markers.
func (s *Service) sweepParentCloseout(ctx context.Context) {
	if s.ensureTodosLoaded(ctx) != nil {
		return
	}
	s.pruneParentCloseoutWakes(ctx)
	s.todos.mu.RLock()
	parentIDs := map[string]bool{}
	for _, t := range s.todos.todos {
		if t.PartOf != "" && t.PartOf != t.ID && !isAskTodo(t.ID) && !isOutsideDecisionTodo(t.ID) {
			parentIDs[t.PartOf] = true
		}
	}
	var parents []Todo
	for id := range parentIDs {
		if p := s.todos.todos[id]; p != nil && p.DoneAt == nil {
			parents = append(parents, copyTodoLocked(p))
		}
	}
	s.todos.mu.RUnlock()
	if len(parents) == 0 {
		return
	}
	sort.Slice(parents, func(i, j int) bool { return parents[i].ID < parents[j].ID })
	for _, p := range parents {
		s.maybeEmitParentCloseout(ctx, p)
	}
}

// parentCloseoutDigestEntry builds and revalidates a sigParentCloseout supervision entry at delivery time.
func (s *Service) parentCloseoutDigestEntry(ctx context.Context, g superviseSignal, web string) (prompts.SuperviseEntry, bool, bool) {
	t, ok := s.todoNow(g.Todo)
	if !ok || t.DoneAt != nil {
		return prompts.SuperviseEntry{}, false, false
	}
	children, promptChildren, ready, err := s.checkParentCloseoutReady(ctx, t)
	if err != nil {
		return prompts.SuperviseEntry{}, false, true
	}
	if !ready || parentCloseoutKey(t, children) != g.Key {
		return prompts.SuperviseEntry{}, false, false
	}
	var doneWhen []string
	if t.Supervision != nil {
		doneWhen = t.Supervision.DoneWhen
	}
	lang := answerLanguage(string(s.botLang(ctx)))
	if g.To == "" && t.Supervision != nil && s.isLeaderRef(t.Supervision.Supervisor) {
		lang = "English"
	}
	wakeText := prompts.RenderParentCloseoutWake(prompts.ParentCloseoutWake{
		Language: lang,
		Personal: isPlainPersonalTodo(t),
		Children: promptChildren,
		Notes:    clip(strings.TrimSpace(t.Notes), 1000),
	})
	return prompts.SuperviseEntry{
		ID:             t.ID,
		Title:          t.Title,
		Kind:           sigParentCloseout,
		DoneWhen:       doneWhen,
		Link:           todoLink(web, t.ID),
		Quiet:          t.Quiet,
		ParentCloseout: wakeText,
	}, true, false
}
