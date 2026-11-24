package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Backup launchers for a usage limit (#14435, docs/assistant/agent-work.md "Usage limits: a backup launcher"): a
// conversation the owner relies on that a subscription's usage limit stopped goes on with the first launcher of
// assistant.limit_fallbacks that can take it, and comes back once the limit resets. Both moves are conversation-switch
// batches (conversations_switch.go) like any other: durable before-image, epoch fence, busy queue and the logical
// segment switch with its summary of what went before. What this adds is when they start, that they are FirstTurn
// batches (the summary always travels with the target's first input, never dropped by an idle acceptance), the one
// message it sends (the hub's own continuation, the auto-continue source, queued behind the forward switch) and the
// restore at the reset. Nothing here interrupts a turn, and a switch made in between is never overwritten: the
// restore's fence leaves a conversation someone changed alone.

const (
	// limitFallbackMinWait: a limit that resets sooner is waited out as before (continued a minute after the reset):
	// a switch costs a summary and a new process, and would come straight back.
	limitFallbackMinWait = 10 * time.Minute
	// limitFallbackRecheck: how long a restore waits again while its original launcher still shows a failure with no
	// later reset known, or its plan could not be read.
	limitFallbackRecheck = 15 * time.Minute
	// limitFallbackContinueTries: how many times the continuation is handed over before it is left to the
	// conversation's next input (which then carries the summary). Its client_msg_id makes every try the same message.
	limitFallbackContinueTries = 5
	// limitFallbackKeep: how long a finished fallback and its restore stay in the switch state.
	limitFallbackKeep = 7 * 24 * time.Hour
	// maxLimitFallbacks: how many launchers assistant.limit_fallbacks may name.
	maxLimitFallbacks = 8
	// limitFallbackContinuePrefix starts the continuation's client_msg_id; the batch id follows.
	limitFallbackContinuePrefix = "limit-fallback:"
	// limitFallbackStartTimeout bounds a start: it reads the node's launchers and starts the switch.
	limitFallbackStartTimeout = 2 * time.Minute
)

// conversationSwitchFallback marks a batch the usage-limit fallback opened. It has one item, the conversation the
// limit stopped.
type conversationSwitchFallback struct {
	// ResetsAt is when the stopped launcher's limit resets, as the turn said; RestoreAt when the batch is restored
	// (later while the original launcher still shows a limit).
	ResetsAt  time.Time `json:"resets_at"`
	RestoreAt time.Time `json:"restore_at"`
	// ContinueID is the continuation's client_msg_id. Continued: it was handed over, the owner spoke first, or it
	// was given up after limitFallbackContinueTries tries.
	ContinueID    string `json:"continue_id"`
	Continued     bool   `json:"continued,omitempty"`
	ContinueTries int    `json:"continue_tries,omitempty"`
	// RestoreBatch is the batch that restores this one, once made. Done: nothing more to do (restore made, nothing
	// moved, or nothing left to restore).
	RestoreBatch string `json:"restore_batch,omitempty"`
	Done         bool   `json:"done,omitempty"`
}

// limitFallbacksOf is assistant.limit_fallbacks as it applies: trimmed, without repeats or blanks, in order.
func limitFallbacksOf(v Settings) []string {
	return cleanLimitFallbacks(v.LimitFallbacks)
}

func cleanLimitFallbacks(names []string) []string {
	var out []string
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// limitFallbackTurnSettled: machine's conversation sid ended a turn, which stayed still. When a subscription's usage
// limit stopped it, it resets at least limitFallbackMinWait later, and the conversation is one a backup carries on
// (limitFallbackEligible), the conversation moves to a backup launcher (limitFallbackStart). Every other turn, and
// every turn while assistant.limit_fallbacks is empty, returns at once.
func (s *Service) limitFallbackTurnSettled(ctx context.Context, machine, sid string) {
	if len(limitFallbacksOf(s.settings(ctx))) == 0 || s.store() == nil {
		return
	}
	// As the node lists it, when the fleet is here (launcherLimitTurnSettled): session.get costs a pass over the list.
	sess, ok := s.nodeSession(machine, sid)
	if !s.fleetOn() {
		sess, ok = s.session(ctx, machine, sid)
	}
	if !ok {
		return
	}
	lim := limitOfTurn(sess)
	reset, known := usageLimitReset(lim)
	now := time.Now()
	if !known || reset.Sub(now) < limitFallbackMinWait || !s.limitFallbackEligible(ctx, machine, sid, sess) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, limitFallbackStartTimeout)
	defer cancel()
	s.limitFallbackStart(ctx, machine, sid, reset, now)
}

// limitFallbackEligible: sess, machine's conversation sid, is one a backup launcher carries on. It is an Assistant
// conversation of the assistant's own space, not a guest's, not a reviewer, not running now; and it is the main
// conversation, a leader or the background supervisor (the conversations the manager continues after a limit, #7824),
// or a side conversation whose round that stopped was the owner's own message. Coding Agents, a space's
// conversations, and a round a schedule or another conversation started in a side conversation are left as they are.
func (s *Service) limitFallbackEligible(ctx context.Context, machine, sid string, sess proto.Session) bool {
	if !sess.Assistant || sess.PaneID != "" || sess.IsDeleted() || sess.HiddenByDirectory() || sess.Starting ||
		sess.Status == proto.StatusWorking || spaceOf(sess) != "" || s.isReviewer(machine, sid) || s.meetingTurnNow(machine, sid) {
		return false
	}
	if _, guest, err := s.guestOf(ctx, machine, sid); err != nil || guest {
		return false
	}
	if s.protectedModelConversation(ctx, machine, sid) {
		return true
	}
	round, ok := s.availLastRound(machine, sid)
	return ok && round.source == roundOwner
}

// limitFallbackBlocked says why machine's conversation sid does not fall back now: a fallback of it is not over yet
// (it is on a backup, or about to be: one level only), or one could not move it for a limit not reset yet (one try
// per limit). "" when neither.
func limitFallbackBlocked(state conversationSwitchState, machine, sid string, now time.Time) string {
	for _, b := range state.Batches {
		if b.Fallback == nil || len(b.Items) != 1 || b.Items[0].Machine != machine || b.Items[0].ConversationID != sid {
			continue
		}
		refused := b.Items[0].Outcome == conversationSwitchRefused
		switch {
		case !b.Fallback.Done && !refused:
			return "already on a backup launcher, or about to be: it waits for the reset"
		case refused && now.Before(b.Fallback.ResetsAt):
			return "a backup launcher could not take it for this limit: it waits for the reset"
		}
	}
	return ""
}

// limitFallbackTarget is the first launcher of assistant.limit_fallbacks machine's conversation sid, running on
// current, may go on with: not the one it runs on, not unusable by its launcher health there, and one the switch
// takes for it (a registered engine; a supervision role's policy). passed says what was passed over, for the log.
func (s *Service) limitFallbackTarget(ctx context.Context, machine, sid string, current conversationExecution) (launcher, passed string) {
	role, err := s.logicalRoleRequired(ctx, machine, sid)
	if err != nil {
		return "", err.Error()
	}
	var skipped []string
	for _, name := range limitFallbacksOf(s.settings(ctx)) {
		if name == current.Launcher {
			continue
		}
		if h, found := s.launcherHealth(ctx, current.Machine, name); found && h.unusable() {
			skipped = append(skipped, name+": "+cmp.Or(h.Kind, h.State))
			continue
		}
		target, err := s.logicalTarget(ctx, current.Machine, name)
		if err != nil {
			skipped = append(skipped, name+": "+err.Error())
			continue
		}
		if role && !target.SupervisionPolicy {
			skipped = append(skipped, name+": no supervision policy")
			continue
		}
		return name, strings.Join(skipped, "; ")
	}
	return "", strings.Join(skipped, "; ")
}

// limitFallbackStart moves machine's conversation sid, which a usage limit resetting at reset stopped, to a backup
// launcher: a one-item FirstTurn batch, durable before its switch starts, and the continuation queued behind the
// switch in the same pass (limitFallbacksLocked).
func (s *Service) limitFallbackStart(ctx context.Context, machine, sid string, reset, now time.Time) {
	st, ok := s.store().(logicalSwitchStore)
	if !ok {
		return
	}
	resolve := s.conversationSwitchResolver(machine)
	book := &s.conversationSwitches
	book.mu.Lock()
	defer book.mu.Unlock()
	state, _, err := s.loadConversationSwitches(ctx)
	if err != nil {
		s.logf("limit fallback %s/%s: switch state: %v", machine, short(sid), err)
		return
	}
	if s.runConversationSwitchesLocked(ctx, &state) {
		if err := s.saveConversationSwitches(ctx, state); err != nil {
			s.logf("limit fallback %s/%s: switch state: %v", machine, short(sid), err)
			return
		}
	}
	defer func() { book.settle(state) }()
	if why := limitFallbackBlocked(state, machine, sid, now); why != "" {
		s.logf("limit fallback %s/%s: %s", machine, short(sid), why)
		return
	}
	row, err := resolve(ctx, sid)
	if err != nil {
		return
	}
	c, err := st.LogicalConversation(ctx, machine, sid)
	if err != nil {
		s.logf("limit fallback %s/%s: %v", machine, short(sid), err)
		return
	}
	current := conversationExecutionNow(machine, row, c)
	launcher, passed := s.limitFallbackTarget(ctx, machine, sid, current)
	if launcher == "" {
		s.logf("limit fallback %s/%s: no backup launcher can take it (%s): it waits for the reset", machine, short(sid), cmp.Or(passed, "none listed but its own"))
		return
	}
	// Launcher default model and effort, as an explicit switch to it would be; the permission mode goes with it as
	// the switch carries it, so the restore's fence finds the target as it was prepared.
	target := conversationExecution{Launcher: launcher, PermissionMode: cmp.Or(current.PermissionMode, "default")}
	item := s.planConversationSwitchItem(ctx, state, st, machine, sid, target, nil, resolve)
	if item.Outcome == conversationSwitchRefused {
		s.logf("limit fallback %s/%s: not switched to %s: %s", machine, short(sid), launcher, item.Reason)
		return
	}
	item.OperationID, item.Outcome = randomHex(16), conversationSwitchQueued
	batch := conversationSwitchBatch{ID: randomHex(16), CreatedAt: now.UTC(), Items: []conversationSwitchItem{item}, FirstTurn: true,
		Fallback: &conversationSwitchFallback{ResetsAt: reset.UTC(), RestoreAt: limitContinueAt(reset, now)}}
	batch.Fallback.ContinueID = limitFallbackContinuePrefix + batch.ID
	state.Batches = append(state.Batches, batch)
	// The before-image and operation ID are durable before the switch starts (executeConversationSwitches).
	if err := s.saveConversationSwitches(ctx, state); err != nil {
		s.logf("limit fallback %s/%s: switch state: %v", machine, short(sid), err)
		return
	}
	s.logf("limit fallback %s/%s: usage limit until %s: %s → %s, back about %s (batch %s)", machine, short(sid),
		reset.UTC().Format(time.RFC3339), current.Launcher, launcher, batch.Fallback.RestoreAt.Format(time.RFC3339), batch.ID)
	if s.runConversationSwitchesLocked(ctx, &state) {
		if err := s.saveConversationSwitches(ctx, state); err != nil {
			s.logf("limit fallback %s/%s: switch state: %v", machine, short(sid), err)
		}
	}
}

// conversationSwitchResolver finds machine's conversation for a batch the manager makes itself, as the full-role
// HTTP route does.
func (s *Service) conversationSwitchResolver(machine string) conversationSwitchResolver {
	return func(ctx context.Context, id string) (proto.Session, error) {
		row, ok := s.session(ctx, machine, id)
		if !ok {
			return proto.Session{}, errors.New("conversation not found")
		}
		row.MachineID = machine
		return row, nil
	}
}

// limitFallbacksLocked moves the fallback batches on, after runConversationSwitchesLocked ran their items (with
// book.mu held; true when state changed): a continuation once its switch has started, a restore once its time came,
// a fallback whose limit reset before its switch could start called off, and finished ones forgotten after
// limitFallbackKeep. A restore batch made here starts on a later pass, after state was saved: the durability
// boundary of every batch.
func (s *Service) limitFallbacksLocked(ctx context.Context, state *conversationSwitchState, now time.Time) bool {
	changed := false
	for i := range state.Batches {
		b := &state.Batches[i]
		fb := b.Fallback
		if fb == nil || fb.Done || len(b.Items) != 1 {
			continue
		}
		item := &b.Items[0]
		switch item.Outcome {
		case conversationSwitchRefused:
			// Nothing moved, or the target refused its first turn and the original came back: nothing to continue
			// or restore. Its turn waits for the reset as before.
			s.logf("limit fallback %s/%s: not switched: %s", item.Machine, short(item.ConversationID), item.Reason)
			fb.Done, changed = true, true
			continue
		case conversationSwitchSwitching, conversationSwitchSwitched:
		default:
			// Queued behind its conversation's work. Once the limit is over there is nothing to fall back for.
			if !now.Before(fb.ResetsAt) && s.limitFallbackCallOff(ctx, item) {
				fb.Done, changed = true, true
			}
			continue
		}
		if !fb.Continued {
			s.limitFallbackContinue(ctx, b)
			changed = true
		}
		// Switched: the backup took its first input, so the summary went with it.
		if item.Outcome == conversationSwitchSwitched && fb.Continued && !now.Before(fb.RestoreAt) && s.limitFallbackRestore(ctx, state, i, now) {
			changed = true
		}
	}
	if pruneLimitFallbacks(state, now) {
		changed = true
	}
	return changed
}

// limitFallbackCallOff refuses a queued fallback item whose limit reset before its switch could start, when no
// switch operation was begun for it (a crash after the begin and before the save turns into reconciliation instead).
func (s *Service) limitFallbackCallOff(ctx context.Context, item *conversationSwitchItem) bool {
	st, ok := s.store().(logicalSwitchStore)
	if !ok {
		return false
	}
	if op, err := st.ConversationSwitch(ctx, item.Machine, item.ConversationID, item.OperationID); err != nil || op != nil {
		return false
	}
	item.Outcome, item.Reason = conversationSwitchRefused, "the usage limit reset before the switch could start"
	s.logf("limit fallback %s/%s: %s", item.Machine, short(item.ConversationID), item.Reason)
	return true
}

// limitFallbackContinueText is the continuation in the owner's language.
func (s *Service) limitFallbackContinueText(ctx context.Context) string {
	return prompts.RenderLimitFallbackContinue(answerLanguage(s.hello(ctx).Locale))
}

// limitFallbackContinue hands b's continuation over: queued behind the started switch, it is the backup's first
// input, after the summary, so the work the limit stopped goes on there. An input already waiting for the backup
// (the owner's message, a supervision digest) is newer and goes first with the summary: no continuation then.
func (s *Service) limitFallbackContinue(ctx context.Context, b *conversationSwitchBatch) {
	fb, item := b.Fallback, b.Items[0]
	if st, ok := s.store().(logicalSwitchStore); ok && item.Outcome == conversationSwitchSwitching {
		if pending, err := st.ConversationInputPending(ctx, item.Machine, item.ConversationID); err == nil && pending {
			fb.Continued = true
			s.logf("limit fallback %s/%s: an input is already waiting: no continuation", item.Machine, short(item.ConversationID))
			return
		}
	}
	params := map[string]any{"session_id": item.ConversationID, "text": s.limitFallbackContinueText(ctx),
		"client_msg_id": fb.ContinueID, "message_source": autoContinueSource()}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := s.call(cctx, item.Machine, DeviceSessionSend, params, nil)
	cancel()
	fb.ContinueTries++
	switch {
	case err == nil:
		fb.Continued = true
	case fb.ContinueTries >= limitFallbackContinueTries:
		fb.Continued = true
		s.logf("limit fallback %s/%s: continuation left after %d tries: %v", item.Machine, short(item.ConversationID), fb.ContinueTries, err)
	default:
		s.logf("limit fallback %s/%s: continuation (try %d): %v", item.Machine, short(item.ConversationID), fb.ContinueTries, err)
	}
}

// limitFallbackRestore makes the batch that restores state.Batches[i], whose time came: unless its original launcher
// still shows a failure, which postpones it. A restore that the conversation's own later change refuses ends the
// fallback: that choice stands.
func (s *Service) limitFallbackRestore(ctx context.Context, state *conversationSwitchState, i int, now time.Time) bool {
	b := &state.Batches[i]
	fb, item := b.Fallback, b.Items[0]
	for _, other := range state.Batches {
		if other.RestoreOf == b.ID {
			fb.Done, fb.RestoreBatch = true, other.ID // restored already (conversations_switch restore_batch_id)
			return true
		}
	}
	if h, found := s.launcherHealth(ctx, item.Before.Machine, item.Before.Launcher); found && h.unusable() {
		next := now.Add(limitFallbackRecheck)
		if reset, ok := h.limitReset(); ok && reset.After(now) {
			next = limitContinueAt(reset, now)
		}
		s.logf("limit fallback %s/%s: %s still unusable (%s): back about %s", item.Machine, short(item.ConversationID),
			item.Before.Launcher, cmp.Or(h.Kind, h.State), next.UTC().Format(time.RFC3339))
		fb.RestoreAt = next.UTC()
		return true
	}
	items, err := s.planConversationSwitches(ctx, *state, conversationsSwitchArgs{RestoreBatchID: b.ID}, s.conversationSwitchResolver(item.Machine))
	if err != nil {
		fb.RestoreAt = now.Add(limitFallbackRecheck).UTC()
		s.logf("limit fallback %s/%s: restore: %v", item.Machine, short(item.ConversationID), err)
		return true
	}
	if actionableConversationSwitchItems(items) == 0 {
		fb.Done = true
		reason := ""
		if len(items) > 0 {
			reason = items[0].Reason
		}
		s.logf("limit fallback %s/%s: not restored: %s", item.Machine, short(item.ConversationID), reason)
		return true
	}
	restore := conversationSwitchBatch{ID: randomHex(16), RestoreOf: b.ID, CreatedAt: now.UTC(), Items: items, FirstTurn: true}
	for j := range restore.Items {
		if restore.Items[j].Outcome != conversationSwitchRefused {
			restore.Items[j].OperationID, restore.Items[j].Outcome = randomHex(16), conversationSwitchQueued
		}
	}
	fb.Done, fb.RestoreBatch = true, restore.ID
	state.Batches = append(state.Batches, restore) // b and fb are not used past here: the append may move the slice
	s.logf("limit fallback %s/%s: back to %s (batch %s)", item.Machine, short(item.ConversationID), item.Before.Launcher, restore.ID)
	return true
}

// pruneLimitFallbacks forgets finished fallbacks older than limitFallbackKeep, with their restores once those
// settled. A batch someone asked for is never pruned here.
func pruneLimitFallbacks(state *conversationSwitchState, now time.Time) bool {
	old := map[string]bool{}
	for _, b := range state.Batches {
		if b.Fallback != nil && b.Fallback.Done && now.Sub(b.CreatedAt) > limitFallbackKeep {
			old[b.ID] = true
		}
	}
	if len(old) == 0 {
		return false
	}
	settled := func(b conversationSwitchBatch) bool {
		for _, item := range b.Items {
			if item.Outcome != conversationSwitchSwitched && item.Outcome != conversationSwitchRefused {
				return false
			}
		}
		return true
	}
	before := len(state.Batches)
	state.Batches = slices.DeleteFunc(state.Batches, func(b conversationSwitchBatch) bool {
		if b.RestoreOf != "" && old[b.RestoreOf] {
			return settled(b)
		}
		return old[b.ID] && !restorePending(*state, b.ID, settled)
	})
	return len(state.Batches) != before
}

// restorePending: the batch restoring id is still under way (it is kept, and so is the fallback it restores).
func restorePending(state conversationSwitchState, id string, settled func(conversationSwitchBatch) bool) bool {
	for _, b := range state.Batches {
		if b.RestoreOf == id && !settled(b) {
			return true
		}
	}
	return false
}

// limitFallbackPending: a fallback batch still has its continuation to hand over, which no item state shows.
func limitFallbackPending(state conversationSwitchState) bool {
	for _, b := range state.Batches {
		if fb := b.Fallback; fb != nil && !fb.Done && !fb.Continued && len(b.Items) == 1 &&
			(b.Items[0].Outcome == conversationSwitchSwitching || b.Items[0].Outcome == conversationSwitchSwitched) {
			return true
		}
	}
	return false
}

// limitFallbackDue is when a fallback batch next needs a look while nothing else is under way: the earliest
// restore time of a switched one, or the reset that calls off one still queued; zero when none waits.
func limitFallbackDue(state conversationSwitchState) time.Time {
	var due time.Time
	for _, b := range state.Batches {
		fb := b.Fallback
		if fb == nil || fb.Done || len(b.Items) != 1 {
			continue
		}
		at := fb.RestoreAt
		if b.Items[0].Outcome == conversationSwitchQueued {
			at = fb.ResetsAt
		}
		if due.IsZero() || at.Before(due) {
			due = at
		}
	}
	return due
}

// ---- assistant.limit_fallbacks in persona_settings ----

// getLimitFallbacks says assistant.limit_fallbacks and the launchers it can name.
func (s *Service) getLimitFallbacks(ctx context.Context) (string, error) {
	var b strings.Builder
	if now := limitFallbacksOf(s.settings(ctx)); len(now) == 0 {
		b.WriteString("now: [] (off: a conversation a usage limit stopped waits for the reset)\n")
	} else {
		raw, _ := json.Marshal(now)
		fmt.Fprintf(&b, "now: %s\n", raw)
	}
	ls, err := s.launchers(ctx)
	if err != nil {
		fmt.Fprintf(&b, "(the machine's launchers could not be listed: %s)\n", err)
		return b.String(), nil
	}
	b.WriteString("available:\n")
	for _, l := range ls {
		fmt.Fprintf(&b, "- %s (model %s)\n", l.Name, orNone(l.Model))
	}
	return b.String(), nil
}

// setLimitFallbacks saves assistant.limit_fallbacks: launcher names of the assistant's machine, first choice first;
// [] turns it off.
func (s *Service) setLimitFallbacks(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, i18n.Err("assistant-settings_tool.bad_limit_fallbacks")
	}
	value := cleanLimitFallbacks(names)
	if len(value) > maxLimitFallbacks {
		return nil, i18n.Err("assistant-settings_tool.bad_limit_fallbacks")
	}
	if len(value) > 0 {
		ls, err := s.launchers(ctx)
		if err != nil {
			return nil, err
		}
		for _, name := range value {
			if proto.IsExternalLauncher(name) {
				return nil, i18n.Err("external-adapter.purpose_unsupported")
			}
			if !slices.ContainsFunc(ls, func(l proto.Launcher) bool { return l.Name == name }) {
				return nil, fmt.Errorf("no launcher %q on the assistant's machine (get lists them): %w", name, i18n.Err("assistant-models.err_provider_unsupported"))
			}
		}
	}
	before := limitFallbacksOf(s.settings(ctx))
	if slices.Equal(before, value) {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.limit_fallbacks_unchanged")}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	list := func(l i18n.Lang, names []string) string {
		if len(names) == 0 {
			return i18n.T(l, "assistant-settings_tool.none")
		}
		return strings.Join(names, " → ")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_limit_fallbacks", i18n.Args{"before": list(l, before), "after": list(l, value)})
	}, apply: func(ctx context.Context) (string, error) {
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.LimitFallbacks = value; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		return i18n.T(s.lang(ctx), "assistant-settings_tool.limit_fallbacks_saved", i18n.Args{"value": list(s.lang(ctx), value)}), nil
	}}, nil
}
