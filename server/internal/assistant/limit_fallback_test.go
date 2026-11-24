package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The usage-limit fallback (#14435, limit_fallback.go) on the conversation-switch rig: real Postgres, the logical
// segment switch, and a fake node that prepares a target and takes sends.

// limitRow updates machine "server"'s row id as the node would list it.
func limitRow(r *conversationSwitchTestRig, id string, change func(*proto.Session)) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	rows := r.f.sessions["server"]
	for i := range rows {
		if rows[i].SessionID == id {
			change(&rows[i])
			return
		}
	}
	row := proto.Session{SessionID: id, MachineID: "server", Assistant: true, Rev: 100}
	change(&row)
	r.f.sessions["server"] = append(rows, row)
	r.f.known[id] = true
}

// usageLimitTurn is a turn a subscription's usage limit refused, resetting at reset.
func usageLimitTurn(reset time.Time) func(*proto.Session) {
	return func(row *proto.Session) {
		model := "old-model"
		row.MachineID, row.Assistant, row.Agent, row.Launcher, row.Effort = "server", true, "claude", "sub", "high"
		row.Model, row.RequestedModel = "transcript-model", &model
		row.Status, row.State = proto.StatusIdle, proto.StateIdle
		row.TurnError, row.TurnErrorKind, row.TurnErrorResetsAt = "You've hit your usage limit", proto.TurnErrorUsageLimit, &reset
	}
}

// limitSends records the sends that reached a node, by physical session.
type limitSends struct {
	mu   sync.Mutex
	sent []map[string]any
}

func (l *limitSends) to(id string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, p := range l.sent {
		if p["session_id"] == id {
			out = append(out, p)
		}
	}
	return out
}

func newLimitFallbackRig(t *testing.T) (*conversationSwitchTestRig, *limitSends) {
	t.Helper()
	r := newConversationSwitchTestRig(t)
	sends := &limitSends{}
	r.f.answerNode(DeviceSessionSend, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		sends.mu.Lock()
		sends.sent = append(sends.sent, p)
		sends.mu.Unlock()
		return map[string]any{"ok": true, "session_id": p["session_id"]}, nil
	})
	setSettings(t, r.s, func(cfg *Settings) { cfg.LimitFallbacks = []string{"sub", "backup"} })
	return r, sends
}

func limitFallbackState(t *testing.T, s *Service) conversationSwitchState {
	t.Helper()
	state, _, err := s.loadConversationSwitches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// setLimitHealth records that launcher on "server" stopped on a usage limit resetting at reset, as launcher health
// would after a check.
func setLimitHealth(s *Service, launcher string, reset time.Time) {
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	s.healthLoadLocked(context.Background())
	s.health.recs[healthKey("server", launcher)] = &LauncherHealth{Machine: "server", Launcher: launcher, State: healthFailing,
		Kind: errKindUsageLimit, ResetsAt: &reset, CheckedAt: time.Now()}
}

func clearLimitHealth(s *Service, launcher string) {
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	delete(s.health.recs, healthKey("server", launcher))
}

// forceLimitRestoreDue moves fallback batch id's restore time into the past, as the reset passing would.
func forceLimitRestoreDue(t *testing.T, s *Service, id string) {
	t.Helper()
	s.conversationSwitches.mu.Lock()
	defer s.conversationSwitches.mu.Unlock()
	state := limitFallbackState(t, s)
	for i := range state.Batches {
		if state.Batches[i].ID == id {
			state.Batches[i].Fallback.RestoreAt = time.Now().Add(-time.Minute)
		}
	}
	if err := s.saveConversationSwitches(context.Background(), state); err != nil {
		t.Fatal(err)
	}
}

// limitFallbackBatches are the fallback batches, and the restores, in state.
func limitFallbackBatches(state conversationSwitchState) (fallbacks, restores []conversationSwitchBatch) {
	for _, b := range state.Batches {
		switch {
		case b.Fallback != nil:
			fallbacks = append(fallbacks, b)
		case b.RestoreOf != "":
			restores = append(restores, b)
		}
	}
	return fallbacks, restores
}

func TestLimitFallbackMovesMainToABackupAndBack(t *testing.T) {
	r, sends := newLimitFallbackRig(t)
	ctx := context.Background()
	reset := time.Now().Add(2 * time.Hour)
	limitRow(r, "main-chat", usageLimitTurn(reset))

	r.s.limitFallbackTurnSettled(ctx, "server", "main-chat")
	fallbacks, _ := limitFallbackBatches(limitFallbackState(t, r.s))
	if len(fallbacks) != 1 {
		t.Fatalf("fallback batches: %+v", fallbacks)
	}
	forward := fallbacks[0]
	item := forward.Items[0]
	// "sub" is the launcher it is on: the next one listed is the backup.
	if !forward.FirstTurn || item.Target.Launcher != "backup" || item.Target.Model != "" || item.Before.Launcher != "sub" || item.Before.Model != "old-model" ||
		item.Outcome != conversationSwitchSwitching || !forward.Fallback.Continued || forward.Fallback.ContinueID != limitFallbackContinuePrefix+forward.ID {
		t.Fatalf("forward batch: %+v fallback %+v", forward, forward.Fallback)
	}
	if !forward.Fallback.ResetsAt.Equal(reset.UTC()) || forward.Fallback.RestoreAt.Before(reset) {
		t.Fatalf("restore time: %+v", forward.Fallback)
	}
	// The continuation waits in the switch's queue: nothing reached a node before the backup was ready.
	if pending, err := r.d.ConversationInputPending(ctx, "server", "main-chat"); err != nil || !pending {
		t.Fatalf("continuation not queued behind the switch: %v %v", pending, err)
	}
	if got := sends.to("main-chat"); len(got) != 0 {
		t.Fatalf("continuation reached the stopped launcher: %+v", got)
	}

	conversationSwitchTestDrive(t, r.s, forward.ID, map[string]string{"main-chat": conversationSwitchSwitched})
	first := sends.to("prepared-chat-1")
	if len(first) != 1 {
		t.Fatalf("backup's first input: %+v", first)
	}
	text := fmt.Sprint(first[0]["text"])
	if first[0]["client_msg_id"] != forward.Fallback.ContinueID || !strings.Contains(text, "<persona-context") || !strings.Contains(text, "<persona-limit-fallback />") {
		t.Fatalf("the continuation did not carry the switch's summary and its marker: %+v", first[0])
	}
	if in := prompts.ParseInjected(text); in == nil || in.State != "limit_fallback" {
		t.Fatalf("continuation not recognized as Persona's: %+v", in)
	}
	c, err := r.d.LogicalConversation(ctx, "server", "main-chat")
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := c.Active(); !ok || a.Launcher != "backup" || c.Switch != nil {
		t.Fatalf("not on the backup: %+v", c)
	}

	// Another limit while it is on the backup waits as before: one level only, no second batch.
	limitRow(r, "main-chat", usageLimitTurn(time.Now().Add(3*time.Hour)))
	r.s.limitFallbackTurnSettled(ctx, "server", "main-chat")
	if fallbacks, _ := limitFallbackBatches(limitFallbackState(t, r.s)); len(fallbacks) != 1 {
		t.Fatalf("a second fallback opened while on the backup: %+v", fallbacks)
	}

	// The reset came: the restore batch is made on a pass, and starts on a later one.
	forceLimitRestoreDue(t, r.s, forward.ID)
	conversationSwitchTestStatus(t, r.s, forward.ID)
	fallbacks, restores := limitFallbackBatches(limitFallbackState(t, r.s))
	if len(restores) != 1 || restores[0].RestoreOf != forward.ID || !restores[0].FirstTurn || !fallbacks[0].Fallback.Done || fallbacks[0].Fallback.RestoreBatch != restores[0].ID {
		t.Fatalf("restore: fallbacks %+v restores %+v", fallbacks, restores)
	}
	back := restores[0]
	if back.Items[0].Target.Launcher != "sub" || back.Items[0].Target.Model != "old-model" || back.Items[0].Target.Effort != "high" {
		t.Fatalf("restore target: %+v", back.Items[0])
	}
	// Activated with no input, it is not accepted idle: the summary of the backup's work waits for the next input.
	waitFor(t, "the restore to activate", func() bool {
		r.s.logicalSweep(ctx)
		conversationSwitchTestStatus(t, r.s, back.ID)
		op, err := r.d.ConversationSwitch(ctx, "server", "main-chat", back.Items[0].OperationID)
		return err == nil && op != nil && op.Phase == "active"
	})
	for range 3 {
		r.s.logicalSweep(ctx)
		if got := conversationSwitchTestItemFor(t, conversationSwitchTestStatus(t, r.s, back.ID), "main-chat").Outcome; got != conversationSwitchSwitching {
			t.Fatalf("restore accepted without its first input: %s", got)
		}
	}
	if got := sends.to("prepared-chat-2"); len(got) != 0 {
		t.Fatalf("the restore sent something by itself: %+v", got)
	}

	if err := r.s.call(ctx, "server", DeviceSessionSend, map[string]any{"session_id": "main-chat", "text": "the owner's next message", "client_msg_id": "owner-1"}, nil); err != nil {
		t.Fatal(err)
	}
	conversationSwitchTestDrive(t, r.s, back.ID, map[string]string{"main-chat": conversationSwitchSwitched})
	next := sends.to("prepared-chat-2")
	if len(next) != 1 || next[0]["client_msg_id"] != "owner-1" || !strings.Contains(fmt.Sprint(next[0]["text"]), "<persona-context") || !strings.Contains(fmt.Sprint(next[0]["text"]), "the owner's next message") {
		t.Fatalf("the owner's next message did not carry the summary back: %+v", next)
	}
	c, err = r.d.LogicalConversation(ctx, "server", "main-chat")
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := c.Active(); !ok || a.Launcher != "sub" || a.Model != "old-model" || c.Switch != nil {
		t.Fatalf("not back on its launcher: %+v", c)
	}
	if !r.s.conversationSwitches.idle {
		t.Fatal("switch book not idle after the restore")
	}
}

func TestLimitFallbackLeavesWhatItShould(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		id   string
		set  func(*conversationSwitchTestRig)
		row  func(*proto.Session)
	}{
		{name: "off", id: "main-chat", set: func(r *conversationSwitchTestRig) {
			setSettings(t, r.s, func(cfg *Settings) { cfg.LimitFallbacks = nil })
		}},
		{name: "resets soon", id: "main-chat", row: usageLimitTurn(time.Now().Add(5 * time.Minute))},
		{name: "no reset known", id: "main-chat", row: func(row *proto.Session) { row.TurnErrorResetsAt, row.TurnError = nil, "usage limit" }},
		{name: "another error", id: "main-chat", row: func(row *proto.Session) { row.TurnErrorKind = proto.TurnErrorTransient }},
		{name: "coding agent", id: "coding-agent", row: func(row *proto.Session) { row.PaneID = "pane-1" }},
		{name: "side chat a schedule started", id: "side-chat", set: func(r *conversationSwitchTestRig) {
			now := time.Now()
			r.s.availInput("server", "side-chat", proto.SourceScheduledTask, now)
			r.s.availTurnStarted("server", "side-chat", true, now)
			r.s.availTurnEnded(ctx, "server", "side-chat", now)
		}},
		{name: "no backup but its own", id: "main-chat", set: func(r *conversationSwitchTestRig) {
			setSettings(t, r.s, func(cfg *Settings) { cfg.LimitFallbacks = []string{"sub"} })
		}},
		{name: "its backup is limited too", id: "main-chat", set: func(r *conversationSwitchTestRig) {
			setLimitHealth(r.s, "backup", time.Now().Add(3*time.Hour))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, sends := newLimitFallbackRig(t)
			limitRow(r, tc.id, usageLimitTurn(time.Now().Add(2*time.Hour)))
			limitRow(r, tc.id, func(row *proto.Session) { row.Archived = false })
			if tc.row != nil {
				limitRow(r, tc.id, tc.row)
			}
			if tc.set != nil {
				tc.set(r)
			}
			r.s.limitFallbackTurnSettled(ctx, "server", tc.id)
			if fallbacks, _ := limitFallbackBatches(limitFallbackState(t, r.s)); len(fallbacks) != 0 {
				t.Fatalf("fell back: %+v", fallbacks)
			}
			if c, err := r.d.LogicalConversation(ctx, "server", tc.id); err != nil || c != nil {
				t.Fatalf("a switch started: %+v %v", c, err)
			}
			if got := sends.to(tc.id); len(got) != 0 {
				t.Fatalf("sent: %+v", got)
			}
		})
	}
}

// A backup launcher health shows limited is passed over for the next one; the restore waits while the original
// launcher still shows a later reset, and leaves a conversation alone that someone changed meanwhile.
func TestLimitFallbackSkipsALimitedBackupAndKeepsALaterChoice(t *testing.T) {
	r, sends := newLimitFallbackRig(t)
	ctx := context.Background()
	setSettings(t, r.s, func(cfg *Settings) { cfg.LimitFallbacks = []string{"limited", "backup"} })
	setLimitHealth(r.s, "limited", time.Now().Add(3*time.Hour))
	limitRow(r, "main-chat", usageLimitTurn(time.Now().Add(2*time.Hour)))

	r.s.limitFallbackTurnSettled(ctx, "server", "main-chat")
	fallbacks, _ := limitFallbackBatches(limitFallbackState(t, r.s))
	if len(fallbacks) != 1 || fallbacks[0].Items[0].Target.Launcher != "backup" {
		t.Fatalf("the limited backup was not passed over: %+v", fallbacks)
	}
	forward := fallbacks[0]
	conversationSwitchTestDrive(t, r.s, forward.ID, map[string]string{"main-chat": conversationSwitchSwitched})
	if got := sends.to("prepared-chat-1"); len(got) != 1 {
		t.Fatalf("backup's first input: %+v", got)
	}

	// Its time came, but the original launcher still shows a limit resetting later: the restore waits for that one.
	subReset := time.Now().Add(time.Hour)
	setLimitHealth(r.s, "sub", subReset)
	forceLimitRestoreDue(t, r.s, forward.ID)
	conversationSwitchTestStatus(t, r.s, forward.ID)
	fallbacks, restores := limitFallbackBatches(limitFallbackState(t, r.s))
	if len(restores) != 0 || fallbacks[0].Fallback.Done || fallbacks[0].Fallback.RestoreAt.Before(subReset) {
		t.Fatalf("restored while its launcher was still limited: %+v %+v", fallbacks[0].Fallback, restores)
	}

	// That limit is over too, but the conversation got a model of its own meanwhile: that choice stands.
	clearLimitHealth(r.s, "sub")
	waitFor(t, "the conversation's own pick", func() bool {
		c, err := r.d.LogicalConversation(ctx, "server", "main-chat")
		if err != nil {
			t.Fatal(err)
		}
		err = r.d.SetConversationPick(ctx, "server", "main-chat", c.Epoch, "backup", "picked-model", "")
		if errors.Is(err, db.ErrConversationSwitchBusy) {
			return false // NOWAIT can overlap a sweep
		}
		if err != nil {
			t.Fatal(err)
		}
		return true
	})
	forceLimitRestoreDue(t, r.s, forward.ID)
	conversationSwitchTestStatus(t, r.s, forward.ID)
	fallbacks, restores = limitFallbackBatches(limitFallbackState(t, r.s))
	if len(restores) != 0 || !fallbacks[0].Fallback.Done {
		t.Fatalf("restore over a later choice: %+v %+v", fallbacks[0].Fallback, restores)
	}
	c, err := r.d.LogicalConversation(ctx, "server", "main-chat")
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := c.Active(); !ok || a.Launcher != "backup" || a.Model != "picked-model" {
		t.Fatalf("the later choice was overwritten: %+v", c)
	}
}

func TestLimitFallbackTakesTheOwnersSideChat(t *testing.T) {
	r, _ := newLimitFallbackRig(t)
	ctx := context.Background()
	limitRow(r, "side-chat", usageLimitTurn(time.Now().Add(2*time.Hour)))
	limitRow(r, "side-chat", func(row *proto.Session) { row.Archived = false })
	now := time.Now()
	r.s.availOwnerSpoke(SessionRef{MachineID: "server", SessionID: "side-chat"}, now)
	r.s.availTurnStarted("server", "side-chat", true, now)
	r.s.availTurnEnded(ctx, "server", "side-chat", now)
	r.s.limitFallbackTurnSettled(ctx, "server", "side-chat")
	fallbacks, _ := limitFallbackBatches(limitFallbackState(t, r.s))
	if len(fallbacks) != 1 || fallbacks[0].Items[0].ConversationID != "side-chat" || fallbacks[0].Items[0].Target.Launcher != "backup" {
		t.Fatalf("the owner's side chat did not fall back: %+v", fallbacks)
	}
}

func TestLimitFallbackQueuedPastItsResetIsCalledOff(t *testing.T) {
	r, sends := newLimitFallbackRig(t)
	ctx := context.Background()
	limitRow(r, "main-chat", usageLimitTurn(time.Now().Add(2*time.Hour)))
	limitRow(r, "main-chat", func(row *proto.Session) { row.Background = []proto.BackgroundTask{{ID: "bash-1", Kind: "shell"}} })
	r.s.limitFallbackTurnSettled(ctx, "server", "main-chat")
	fallbacks, _ := limitFallbackBatches(limitFallbackState(t, r.s))
	if len(fallbacks) != 1 || fallbacks[0].Items[0].Outcome != conversationSwitchQueued || fallbacks[0].Fallback.Continued {
		t.Fatalf("a busy conversation's fallback should wait: %+v", fallbacks)
	}
	// Its work outlasted the limit.
	r.s.conversationSwitches.mu.Lock()
	state := limitFallbackState(t, r.s)
	state.Batches[len(state.Batches)-1].Fallback.ResetsAt = time.Now().Add(-time.Minute)
	if err := r.s.saveConversationSwitches(ctx, state); err != nil {
		t.Fatal(err)
	}
	r.s.conversationSwitches.mu.Unlock()
	report := conversationSwitchTestStatus(t, r.s, fallbacks[0].ID)
	if item := conversationSwitchTestItemFor(t, report, "main-chat"); item.Outcome != conversationSwitchRefused {
		t.Fatalf("not called off: %+v", item)
	}
	fallbacks, _ = limitFallbackBatches(limitFallbackState(t, r.s))
	if !fallbacks[0].Fallback.Done {
		t.Fatalf("fallback not over: %+v", fallbacks[0].Fallback)
	}
	if c, err := r.d.LogicalConversation(ctx, "server", "main-chat"); err != nil || c != nil {
		t.Fatalf("a switch started: %+v %v", c, err)
	}
	if got := sends.to("main-chat"); len(got) != 0 {
		t.Fatalf("sent: %+v", got)
	}
}

func TestLimitFallbackBookkeeping(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	item := func(outcome string) []conversationSwitchItem {
		return []conversationSwitchItem{{Machine: "server", ConversationID: "main-chat", Outcome: outcome}}
	}
	fb := func(done, continued bool, restoreAt time.Time) *conversationSwitchFallback {
		return &conversationSwitchFallback{ResetsAt: now.Add(time.Hour), RestoreAt: restoreAt, Done: done, Continued: continued}
	}
	t.Run("blocked", func(t *testing.T) {
		open := conversationSwitchState{Batches: []conversationSwitchBatch{{ID: "a", Items: item(conversationSwitchSwitched), Fallback: fb(false, true, now.Add(time.Hour))}}}
		if limitFallbackBlocked(open, "server", "main-chat", now) == "" || limitFallbackBlocked(open, "server", "other", now) != "" {
			t.Fatal("an open fallback must block only its own conversation")
		}
		refused := conversationSwitchState{Batches: []conversationSwitchBatch{{ID: "a", Items: item(conversationSwitchRefused), Fallback: fb(true, false, now)}}}
		if limitFallbackBlocked(refused, "server", "main-chat", now) == "" || limitFallbackBlocked(refused, "server", "main-chat", now.Add(2*time.Hour)) != "" {
			t.Fatal("a refused fallback blocks until its limit resets")
		}
		restored := conversationSwitchState{Batches: []conversationSwitchBatch{{ID: "a", Items: item(conversationSwitchSwitched), Fallback: fb(true, true, now)}}}
		if why := limitFallbackBlocked(restored, "server", "main-chat", now); why != "" {
			t.Fatalf("a finished fallback blocks: %s", why)
		}
	})
	t.Run("due and pending", func(t *testing.T) {
		state := conversationSwitchState{Batches: []conversationSwitchBatch{
			{ID: "a", Items: item(conversationSwitchSwitched), Fallback: fb(false, true, now.Add(3*time.Hour))},
			{ID: "b", Items: item(conversationSwitchSwitched), Fallback: fb(false, true, now.Add(2*time.Hour))},
			{ID: "c", Items: item(conversationSwitchSwitched), Fallback: fb(true, true, now)},
			{ID: "d", Items: item(conversationSwitchSwitched)},
		}}
		if got := limitFallbackDue(state); !got.Equal(now.Add(2 * time.Hour)) {
			t.Fatalf("due: %v", got)
		}
		if limitFallbackPending(state) {
			t.Fatal("pending with every continuation handed over")
		}
		state.Batches[0].Fallback.Continued = false
		if !limitFallbackPending(state) {
			t.Fatal("a continuation still to hand over is not pending")
		}
		if got := limitFallbackDue(conversationSwitchState{}); !got.IsZero() {
			t.Fatalf("due with no fallback: %v", got)
		}
	})
	t.Run("prune", func(t *testing.T) {
		old := now.Add(-limitFallbackKeep - time.Hour)
		state := conversationSwitchState{Batches: []conversationSwitchBatch{
			{ID: "old", CreatedAt: old, Items: item(conversationSwitchSwitched), Fallback: fb(true, true, old)},
			{ID: "old-back", RestoreOf: "old", CreatedAt: old, Items: item(conversationSwitchSwitched), FirstTurn: true},
			{ID: "waiting", CreatedAt: old, Items: item(conversationSwitchSwitched), Fallback: fb(true, true, old)},
			{ID: "waiting-back", RestoreOf: "waiting", CreatedAt: now, Items: item(conversationSwitchSwitching), FirstTurn: true},
			{ID: "new", CreatedAt: now, Items: item(conversationSwitchSwitched), Fallback: fb(true, true, now)},
			{ID: "asked", CreatedAt: old, Items: item(conversationSwitchSwitched)},
		}}
		if !pruneLimitFallbacks(&state, now) {
			t.Fatal("nothing pruned")
		}
		var kept []string
		for _, b := range state.Batches {
			kept = append(kept, b.ID)
		}
		if got := strings.Join(kept, ","); got != "waiting,waiting-back,new,asked" {
			t.Fatalf("kept %s", got)
		}
	})
	t.Run("clean", func(t *testing.T) {
		if got := cleanLimitFallbacks([]string{" opus ", "", "kimi", "opus"}); strings.Join(got, ",") != "opus,kimi" {
			t.Fatalf("clean: %v", got)
		}
	})
}

func TestLimitFallbacksSetting(t *testing.T) {
	ctx := context.Background()
	s := assistantLauncherFixture(t)
	for _, bad := range []string{`"fast"`, `["missing"]`, `["external:acme"]`, `["a","b","c","d","e","f","g","h","i"]`} {
		if change, err := s.setLimitFallbacks(ctx, json.RawMessage(bad)); err == nil || change != nil {
			t.Fatalf("%s: change=%+v err=%v", bad, change, err)
		}
	}
	change, err := s.setLimitFallbacks(ctx, json.RawMessage(`["fast"," codex ","fast"]`))
	if err != nil || change.apply == nil {
		t.Fatalf("set: %+v %v", change, err)
	}
	if card := change.cardText("en"); !strings.Contains(card, "(none) → fast → codex") {
		t.Fatalf("card: %s", card)
	}
	if _, err := change.apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := limitFallbacksOf(s.settings(ctx)); strings.Join(got, ",") != "fast,codex" {
		t.Fatalf("saved: %v", got)
	}
	if change, err := s.setLimitFallbacks(ctx, json.RawMessage(`["fast","codex"]`)); err != nil || change.apply != nil || change.summary == "" {
		t.Fatalf("unchanged: %+v %v", change, err)
	}
	got, err := s.getLimitFallbacks(ctx)
	if err != nil || !strings.Contains(got, `now: ["fast","codex"]`) || !strings.Contains(got, "- fast ") {
		t.Fatalf("get: %s %v", got, err)
	}
	change, err = s.setLimitFallbacks(ctx, json.RawMessage(`[]`))
	if err != nil || change.apply == nil {
		t.Fatalf("off: %+v %v", change, err)
	}
	if _, err := change.apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := limitFallbacksOf(s.settings(ctx)); len(got) != 0 {
		t.Fatalf("not off: %v", got)
	}
}

func TestGoldenLimitFallbackContinueLanguage(t *testing.T) {
	r, _ := newLimitFallbackRig(t)
	for _, locale := range append([]string{"zh-Hans"}, promptLocales...) {
		r.f.mu.Lock()
		r.f.hello.Locale = locale
		r.f.mu.Unlock()
		text := r.s.limitFallbackContinueText(context.Background())
		if in := prompts.ParseInjected(text); in == nil || in.Kind != proto.InjectedFollowup || in.State != "limit_fallback" {
			t.Fatalf("%s: not recognized: %+v", locale, in)
		}
		if locale == "zh-Hans" {
			golden(t, "limit_fallback_continue", text)
			continue
		}
		golden(t, "limit_fallback_continue."+locale, text)
	}
}
