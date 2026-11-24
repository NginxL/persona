package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func directSupervisionTask(t *testing.T, s *Service, f *fakeHub, supervisor, title string) (Todo, superviseSignal) {
	t.Helper()
	ctx := context.Background()
	f.mu.Lock()
	f.known[supervisor] = true
	f.mu.Unlock()
	task, err := s.createTodo(ctx, title, "Verify direct supervision transcript confirmation", supervisor, "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, task.ID, supervisor, []string{"Verified"}, 0)
	if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
		v.SupervisorSource = "plan"
		v.State = reviewPending
		return true
	}); err != nil {
		t.Fatal(err)
	}
	g := superviseSignal{
		Todo:   task.ID,
		Kind:   sigReview,
		Key:    "review-" + task.ID,
		Detail: "Needs supervisor review",
		To:     supervisor,
		At:     time.Now().UTC(),
	}
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		pending[g.id()] = g
	})
	return task, g
}

func TestDirectSupervisionSendConfirmsMatchingTranscriptItemWithoutDuplicate(t *testing.T) {
	store := &memStore{}
	s, f := newQuietServiceOn(t, store)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
	)
	_, g := directSupervisionTask(t, s, f, "conv2", "Confirmed direct supervision")
	tries := relayTries(f, "conv2")

	now := time.Now()
	if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", now, time.Time{}) {
		t.Fatal("confirmed direct supervision digest was not accepted")
	}
	f.mu.Lock()
	reads := f.itemReads
	f.mu.Unlock()
	if reads == 0 {
		t.Fatal("direct supervision send succeeded without checking the destination transcript")
	}
	if got := triesOf(f, tries); len(got) != 1 || got[0] == "" {
		t.Fatalf("direct sends = %q, want one send with once_id", got)
	}
	if q := s.queuedFor("conv2"); q != 0 {
		t.Fatalf("confirmed direct supervision remained queued: %d", q)
	}
	if !s.supervising("conv2") {
		t.Fatal("confirmed direct supervision did not keep pushes held")
	}
	if pending := storedSupervisePending(t, s); len(pending) != 0 {
		t.Fatalf("confirmed direct supervision left pending outbox entries: %+v", pending)
	}

	// Recovery sweep and a repeat within the dedup window must not duplicate the confirmed delivery.
	s.drainHeldRelays(ctx, now.Add(time.Minute))
	s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[g.id()] = g })
	if s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", now.Add(time.Minute), now) {
		t.Fatal("confirmed direct supervision repeated within dedup window")
	}
	if got := triesOf(f, tries); len(got) != 1 {
		t.Fatalf("direct sends after recovery/repeat = %q, want 1", got)
	}
}

func TestDirectSupervisionSendSuccessWithoutTranscriptItemQueuesAndRetriesUnderSameOnceID(t *testing.T) {
	store := &memStore{}
	s, f := newQuietServiceOn(t, store)
	t.Cleanup(func() { stopRelayTimer(s, "conv2") })
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	conv2 := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		conv2,
	)
	_, g := directSupervisionTask(t, s, f, "conv2", "Unconfirmed direct supervision")
	tries := relayTries(f, "conv2")
	f.mu.Lock()
	f.omitSentItems = true // RPC returns success, but no user item lands in conv2's transcript
	f.mu.Unlock()

	now := time.Now()
	if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", now, time.Time{}) {
		t.Fatal("unconfirmed direct supervision was neither delivered nor queued")
	}
	stopRelayTimer(s, "conv2")

	got := triesOf(f, tries)
	if len(got) != 1 || got[0] == "" {
		t.Fatalf("initial sends = %q, want one send with once_id", got)
	}
	if s.supervising("conv2") {
		t.Fatal("unconfirmed direct supervision kept pushes held")
	}
	msg := savedRelay(t, store, "conv2")
	if msg.OnceID != got[0] || msg.TriedAt.IsZero() || msg.DoubtedAt.IsZero() || msg.Looks != 1 || msg.LookAfter.IsZero() {
		t.Fatalf("persisted unconfirmed relay = %+v, want once_id=%q and attempt state", msg, got[0])
	}

	// Second check still sees no item: stays queued without resending yet.
	advanceRelayLook(t, s, "conv2", 1)
	drainRelayTest(t, s, conv2)
	stopRelayTimer(s, "conv2")
	if len(triesOf(f, tries)) != 1 || s.queuedFor("conv2") != 1 {
		t.Fatalf("retried before bounded look budget expired: tries=%q queued=%d", triesOf(f, tries), s.queuedFor("conv2"))
	}

	// Third check sees no item and retries under the same once_id; if the retry RPC also lacks a transcript item,
	// it stays queued instead of dropping delivery.
	advanceRelayLook(t, s, "conv2", 2)
	drainRelayTest(t, s, conv2)
	stopRelayTimer(s, "conv2")
	got = triesOf(f, tries)
	if len(got) != 2 || got[0] != got[1] {
		t.Fatalf("retry tries = %q, want same once_id %q twice", got, got[0])
	}
	if s.queuedFor("conv2") != 1 {
		t.Fatalf("unconfirmed queued retry left queue: queued=%d", s.queuedFor("conv2"))
	}

	// Once the runtime writes the user item on the next retry, confirmation completes delivery without another copy.
	advanceRelayLook(t, s, "conv2", 1)
	drainRelayTest(t, s, conv2)
	stopRelayTimer(s, "conv2")
	f.mu.Lock()
	f.omitSentItems = false
	f.mu.Unlock()
	advanceRelayLook(t, s, "conv2", 2)
	drainRelayTest(t, s, conv2)
	stopRelayTimer(s, "conv2")
	got = triesOf(f, tries)
	if len(got) != 3 || got[0] != got[2] || s.queuedFor("conv2") != 0 {
		t.Fatalf("final confirmed retry: tries=%q queued=%d", got, s.queuedFor("conv2"))
	}
	if !s.supervising("conv2") {
		t.Fatal("confirmed retry did not hold pushes")
	}
}

func TestDirectSpecialDigestUnconfirmedStaysQueued(t *testing.T) {
	t.Run("pr_conflict_late_transcript_settles_without_duplicate", func(t *testing.T) {
		store := &memStore{}
		s, f, task := newPRConflictTask(t, store)
		t.Cleanup(func() { stopRelayTimer(s, "conv1") })
		ctx := context.Background()
		idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
		if err := s.applyPRStatus(ctx, task, TodoPRStatus{
			URL: task.PRURL, State: "open", HeadOID: "head-direct-unconfirmed-late", Conflicts: true, CheckedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		g, ok := pendingPRConflict(s, task.ID)
		if !ok {
			t.Fatal("no pending PR conflict signal")
		}
		tries := relayTries(f, "conv1")
		var sentText string
		f.mu.Lock()
		prevSend := f.onAssistantSend
		f.onAssistantSend = func(f *fakeHub, p map[string]any) {
			if prevSend != nil {
				prevSend(f, p)
			}
			sentText, _ = p["text"].(string)
		}
		f.omitSentItems = true
		f.mu.Unlock()

		if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{g}, "", time.Now(), time.Time{}) {
			t.Fatal("unconfirmed special digest was not retained in durable queue")
		}
		stopRelayTimer(s, "conv1")

		queued := queuedRelays(s, "conv1")
		if len(queued) != 1 {
			t.Fatalf("unconfirmed special digest did not retain its attempt: %+v", queued)
		}
		got := triesOf(f, tries)
		msg := savedRelay(t, store, "conv1")
		if len(got) != 1 || msg.OnceID != got[0] || msg.TriedAt.IsZero() || msg.DoubtedAt.IsZero() || msg.Looks != 1 || msg.LookAfter.IsZero() || msg.Text == "" || !strings.Contains(sentText, msg.Text) || relayWords(msg.Text) != relayWords(sentText) {
			t.Fatalf("persisted special digest = %+v, tries=%q, sentText=%q", msg, got, sentText)
		}

		// Late transcript write settles the retained attempt without sending a second copy under a new identity.
		f.mu.Lock()
		f.items = append(f.items, proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, Text: sentText, TS: time.Now()})
		f.omitSentItems = false
		f.mu.Unlock()
		advanceRelayLook(t, s, "conv1", 1)
		drainRelayTest(t, s, idle)
		stopRelayTimer(s, "conv1")
		if len(triesOf(f, tries)) != 1 || s.queuedFor("conv1") != 0 {
			t.Fatalf("late transcript item did not settle without duplicate: tries=%q queued=%d", triesOf(f, tries), s.queuedFor("conv1"))
		}
	})

	t.Run("decision_reply_unconfirmed_retries_under_same_once_id", func(t *testing.T) {
		s, f, todo := decisionAgentReplyService(t)
		t.Cleanup(func() { stopRelayTimer(s, "conv1") })
		g := pendingDecisionReply(t, s, todo)
		idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
		f.setSessions("server", idle)
		tries := relayTries(f, "conv1")
		f.mu.Lock()
		f.omitSentItems = true
		f.mu.Unlock()

		dispatchSupervisionNow(t, s, g)
		stopRelayTimer(s, "conv1")

		queued := queuedRelays(s, "conv1")
		if len(queued) != 1 {
			t.Fatalf("unconfirmed special digest did not retain its attempt: %+v", queued)
		}
		got := triesOf(f, tries)
		if len(got) != 1 || queued[0].OnceID != got[0] || queued[0].TriedAt.IsZero() || queued[0].DoubtedAt.IsZero() || queued[0].Interject {
			t.Fatalf("retained decision reply = %+v, tries=%q", queued[0], got)
		}

		// Second look still misses without resending early.
		advanceRelayLook(t, s, "conv1", 1)
		drainRelayTest(t, s, idle)
		stopRelayTimer(s, "conv1")
		if len(triesOf(f, tries)) != 1 || s.queuedFor("conv1") != 1 {
			t.Fatalf("decision reply retried early: tries=%q queued=%d", triesOf(f, tries), s.queuedFor("conv1"))
		}

		// Final look retries under the exact same once_id and confirms once the transcript write succeeds.
		f.mu.Lock()
		f.omitSentItems = false
		f.mu.Unlock()
		advanceRelayLook(t, s, "conv1", 2)
		drainRelayTest(t, s, idle)
		stopRelayTimer(s, "conv1")
		got = triesOf(f, tries)
		if len(got) != 2 || got[0] != got[1] || s.queuedFor("conv1") != 0 {
			t.Fatalf("decision reply retry after look budget: tries=%q queued=%d", got, s.queuedFor("conv1"))
		}
	})

	t.Run("untried_special_digest_still_restores_for_revalidation", func(t *testing.T) {
		store := &memStore{}
		s, f, task := newPRConflictTask(t, store)
		ctx := context.Background()
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
		if err := s.applyPRStatus(ctx, task, TodoPRStatus{
			URL: task.PRURL, State: "open", HeadOID: "head-untried-reval", Conflicts: true, CheckedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		g, ok := pendingPRConflict(s, task.ID)
		if !ok {
			t.Fatal("no pending PR conflict signal")
		}
		if s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{g}, "", time.Now(), time.Time{}) {
			t.Fatal("untried special digest should stay pending for revalidation, not queue")
		}
		if q := s.queuedFor("conv1"); q != 0 {
			t.Fatalf("untried special digest was queued: %d", q)
		}
		if _, pending := pendingPRConflict(s, task.ID); !pending {
			t.Fatal("untried special digest was not restored to pending")
		}
		var history map[string]superviseFingerprint
		if _, err := store.Setting(ctx, superviseDedupKey, &history); err != nil {
			t.Fatal(err)
		}
		if len(history) != 0 {
			t.Fatalf("untried special digest retained dedup reservation: %+v", history)
		}
	})
}

func TestDirectDecisionReplyUnresolvedLegacyRelayBlocksInterjection(t *testing.T) {
	for _, tc := range []struct {
		name         string
		durableDoubt bool
		withdrawn    bool
	}{
		{name: "durable_doubt", durableDoubt: true},
		{name: "withdrawn", withdrawn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := decisionAgentReplyService(t)
			t.Cleanup(func() { stopRelayTimer(s, "conv1") })
			g := pendingDecisionReply(t, s, todo)
			setSupervisorWorking(f)

			head := relayMsg{
				From:      superviseTool,
				FromTitle: "督办",
				Tool:      superviseTool,
				Text:      "older unresolved supervision",
				At:        time.Now().Add(-time.Minute).UTC(),
				Withdrawn: tc.withdrawn,
			}
			if tc.durableDoubt {
				head.DoubtedAt = time.Now().Add(-30 * time.Second).UTC()
			}
			if s.queueRelay("conv1", head) != 1 {
				t.Fatal("failed to queue unresolved legacy supervision relay")
			}

			before := f.sent()
			dispatchSupervisionNow(t, s, g)
			if got := f.sent() - before; got != 0 {
				t.Fatalf("decision interjection bypassed the unresolved relay with %d direct send(s)", got)
			}
			if !strings.Contains(strings.Join(pendingKinds(s, todo.ID), ","), sigDecisionReply) {
				t.Fatalf("blocked decision reply did not remain pending: %v", pendingKinds(s, todo.ID))
			}
			if a := askOf(s, todo.ID); a == nil || a.AgentReply == nil || !a.AgentReply.ToldAt.IsZero() {
				t.Fatalf("blocked decision reply was marked told: %+v", a)
			}
			if queued := queuedRelays(s, "conv1"); len(queued) != 1 || queued[0].Text != head.Text {
				t.Fatalf("unresolved legacy relay was disturbed: %+v", queued)
			}

			// Once the unresolved legacy relay is confirmed in the transcript and reconciled, the pending decision reply can send.
			idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			f.mu.Lock()
			f.items = append(f.items, proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, Text: head.Text, TS: time.Now()})
			f.mu.Unlock()
			f.setSessions("server", idle)
			drainRelayTest(t, s, idle)
			stopRelayTimer(s, "conv1")
			if q := s.queuedFor("conv1"); q != 0 {
				t.Fatalf("reconciled legacy relay stayed queued: %d", q)
			}

			dispatchSupervisionNow(t, s, g)
			if got := f.sent() - before; got != 1 {
				t.Fatalf("decision reply sends after legacy relay reconciliation = %d, want 1", got)
			}
			if a := askOf(s, todo.ID); a == nil || a.AgentReply == nil || a.AgentReply.ToldAt.IsZero() {
				t.Fatalf("reconciled decision reply was not marked told: %+v", a)
			}
		})
	}
}

func TestDirectSupervisionSendRestartRecoversThroughDurableQueue(t *testing.T) {
	for _, landedLate := range []bool{false, true} {
		name := "absent_item_resends_under_same_once_id"
		if landedLate {
			name = "late_item_settles_without_duplicate"
		}
		t.Run(name, func(t *testing.T) {
			store := &memStore{}
			s, f := newQuietServiceOn(t, store)
			t.Cleanup(func() { stopRelayTimer(s, "conv2") })
			s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			ctx := context.Background()
			conv2 := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			f.setSessions("server",
				proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
				conv2,
			)
			_, g1 := directSupervisionTask(t, s, f, "conv2", "First restarted task")
			_, g2 := directSupervisionTask(t, s, f, "conv2", "Second restarted task")
			firstTries := relayTries(f, "conv2")
			f.mu.Lock()
			f.omitSentItems = true
			f.mu.Unlock()

			now := time.Now()
			if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g1, g2}, "", now, time.Time{}) {
				t.Fatal("initial direct supervision was not persisted for recovery")
			}
			stopRelayTimer(s, "conv2")
			initialIDs := triesOf(f, firstTries)
			if len(initialIDs) != 1 || initialIDs[0] == "" {
				t.Fatalf("initial sends = %q, want one send", initialIDs)
			}
			persisted := savedRelay(t, store, "conv2")
			if persisted.OnceID != initialIDs[0] || persisted.TriedAt.IsZero() {
				t.Fatalf("persisted relay = %+v", persisted)
			}

			// Simulate a hub restart 53 seconds later with no in-memory timers or doubt cache.
			const restartDelay = 53 * time.Second
			persisted.TriedAt = persisted.TriedAt.Add(-restartDelay)
			persisted.DoubtedAt = persisted.DoubtedAt.Add(-restartDelay)
			persisted.LookAfter = persisted.LookAfter.Add(-restartDelay)
			var savedState relayState
			if ok, err := store.Setting(ctx, relayKey, &savedState); !ok || err != nil || len(savedState.Queue["conv2"]) != 1 {
				t.Fatalf("load relay state before restart: ok=%v err=%v state=%+v", ok, err, savedState)
			}
			savedState.Queue["conv2"][0] = persisted
			if err := store.PutSetting(ctx, relayKey, savedState); err != nil {
				t.Fatal(err)
			}

			restarted, node := newQuietServiceOn(t, store)
			t.Cleanup(func() { stopRelayTimer(restarted, "conv2") })
			restarted.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			// memStore persists settings (assistant.relay, assistant.supervision_dedup) only; restore the open tasks as Postgres would.
			s.todos.mu.RLock()
			restarted.todos.mu.Lock()
			if restarted.todos.todos == nil {
				restarted.todos.todos = map[string]*Todo{}
			}
			for id, task := range s.todos.todos {
				copyTask := *task
				if task.Supervision != nil {
					copySup := *task.Supervision
					copyTask.Supervision = &copySup
				}
				restarted.todos.todos[id] = &copyTask
			}
			restarted.todos.loaded = true
			restarted.todos.mu.Unlock()
			s.todos.mu.RUnlock()
			node.mu.Lock()
			node.known["conv2"] = true
			if landedLate {
				node.items = []proto.Item{{Role: "user", Kind: proto.KindText, Text: persisted.Text, TS: persisted.TriedAt.Add(time.Second)}}
			}
			node.mu.Unlock()
			node.setSessions("server",
				proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
				conv2,
			)
			restartTries := relayTries(node, "conv2")

			restarted.drainHeldRelays(ctx, time.Now())
			waitFor(t, "post-restart reconciliation pass", func() bool {
				if landedLate {
					return restarted.queuedFor("conv2") == 0 && !drainingNow(restarted, "conv2")
				}
				restarted.relays.mu.Lock()
				defer restarted.relays.mu.Unlock()
				q := restarted.relays.st.Queue["conv2"]
				return len(q) == 1 && q[0].Looks == 2 && !restarted.relays.draining["conv2"]
			})
			stopRelayTimer(restarted, "conv2")

			if landedLate {
				if got := triesOf(node, restartTries); len(got) != 0 || restarted.queuedFor("conv2") != 0 {
					t.Fatalf("late transcript item was resent or left queued: tries=%q queued=%d", got, restarted.queuedFor("conv2"))
				}
				return
			}

			// Look 2 found no item and kept the relay queued without resending early.
			if got := triesOf(node, restartTries); len(got) != 0 || restarted.queuedFor("conv2") != 1 {
				t.Fatalf("after second check: tries=%q queued=%d", got, restarted.queuedFor("conv2"))
			}

			// Look 3 reaches relayLookLimit and resends both signals under the exact same once_id.
			advanceRelayLook(t, restarted, "conv2", 2)
			restarted.drainHeldRelays(ctx, time.Now())
			waitFor(t, "post-restart resend confirmed", func() bool { return restarted.queuedFor("conv2") == 0 && !drainingNow(restarted, "conv2") })
			stopRelayTimer(restarted, "conv2")

			got := triesOf(node, restartTries)
			if len(got) != 1 || got[0] != initialIDs[0] {
				t.Fatalf("post-restart resend ids = %q, want [%q]", got, initialIDs[0])
			}
			sent := strings.Join(node.noticesTo("conv2"), "\n")
			if !strings.Contains(sent, "First restarted task") || !strings.Contains(sent, "Second restarted task") {
				t.Fatalf("resent digest missing signals: %q", sent)
			}
		})
	}
}

func TestDirectSupervisionSendUnreadableTranscriptPreservesStateForRetry(t *testing.T) {
	for _, itemLanded := range []bool{true, false} {
		name := "unreadable_then_missing_retries"
		if itemLanded {
			name = "unreadable_then_present_settles_without_duplicate"
		}
		t.Run(name, func(t *testing.T) {
			store := &memStore{}
			s, f := newQuietServiceOn(t, store)
			t.Cleanup(func() { stopRelayTimer(s, "conv2") })
			s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			ctx := context.Background()
			conv2 := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			f.setSessions("server",
				proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
				conv2,
			)
			_, g := directSupervisionTask(t, s, f, "conv2", "Unreadable transcript task")
			tries := relayTries(f, "conv2")
			f.mu.Lock()
			if itemLanded {
				f.itemsFail = 1
			} else {
				f.itemsFail = relayLookLimit
				f.omitSentItems = true
			}
			f.mu.Unlock()

			if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", time.Now(), time.Time{}) {
				t.Fatal("unreadable transcript did not preserve delivery state")
			}
			stopRelayTimer(s, "conv2")
			msg := savedRelay(t, store, "conv2")
			initial := triesOf(f, tries)
			if len(initial) != 1 || msg.OnceID != initial[0] || msg.TriedAt.IsZero() || msg.Looks != 1 {
				t.Fatalf("state after unreadable transcript: tries=%q relay=%+v", initial, msg)
			}

			if itemLanded {
				// Transcript becomes readable on Look 2 and confirms the original send with zero resends.
				advanceRelayLook(t, s, "conv2", 1)
				drainRelayTest(t, s, conv2)
				stopRelayTimer(s, "conv2")
				if got := triesOf(f, tries); len(got) != 1 || s.queuedFor("conv2") != 0 {
					t.Fatalf("readable transcript did not settle without duplicate: tries=%q queued=%d", got, s.queuedFor("conv2"))
				}
				return
			}

			// Look 2 still cannot read transcript.
			advanceRelayLook(t, s, "conv2", 1)
			drainRelayTest(t, s, conv2)
			stopRelayTimer(s, "conv2")
			if got := triesOf(f, tries); len(got) != 1 || s.queuedFor("conv2") != 1 {
				t.Fatalf("retried before bounded unreadable checks completed: tries=%q queued=%d", got, s.queuedFor("conv2"))
			}

			// Look 3 hits relayLookLimit and retries under the same once_id; transcript now succeeds.
			f.mu.Lock()
			f.omitSentItems = false
			f.mu.Unlock()
			advanceRelayLook(t, s, "conv2", 2)
			drainRelayTest(t, s, conv2)
			stopRelayTimer(s, "conv2")
			if got := triesOf(f, tries); len(got) != 2 || got[0] != got[1] || s.queuedFor("conv2") != 0 {
				t.Fatalf("bounded unreadable fallback: tries=%q queued=%d", got, s.queuedFor("conv2"))
			}
		})
	}
}

func TestDirectSupervisionConfirmationPreservesNonSupervisionAndRefusalBehavior(t *testing.T) {
	t.Run("ordinary_conversation_relay_unchanged", func(t *testing.T) {
		s, f := newQuietService(t)
		f.mu.Lock()
		f.known["conv2"] = true
		f.mu.Unlock()
		f.setSessions("server",
			proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		)
		msg := relayMsg{From: "conv2", FromTitle: "Planning", Tool: "conversation_send", Text: "ordinary relay", At: time.Now().UTC()}
		if err := s.tryDirectRelay(t.Context(), "server", "conv1", &msg); err != nil {
			t.Fatalf("ordinary direct relay failed: %v", err)
		}
		f.mu.Lock()
		reads := f.itemReads
		f.mu.Unlock()
		if reads != 0 || !msg.TriedAt.IsZero() {
			t.Fatalf("ordinary conversation relay checked transcript or set TriedAt: reads=%d msg=%+v", reads, msg)
		}
	})

	t.Run("direct_supervision_refusal_unchanged", func(t *testing.T) {
		store := &memStore{}
		s, f := newQuietServiceOn(t, store)
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		ctx := context.Background()
		f.setSessions("server",
			proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		)
		_, g := directSupervisionTask(t, s, f, "conv2", "Refused supervision task")
		f.mu.Lock()
		f.failAssistant = &rpc.Error{Code: assistantwire.CodeSupervisionRuntime, Message: "unsupported runtime"}
		f.mu.Unlock()

		if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", time.Now(), time.Time{}) {
			t.Fatal("refused direct supervision was not retired")
		}
		f.mu.Lock()
		reads := f.itemReads
		pushes := len(f.pushes)
		f.mu.Unlock()
		if reads != 0 || s.queuedFor("conv2") != 0 || pushes != 1 {
			t.Fatalf("refused direct supervision: reads=%d queued=%d pushes=%d", reads, s.queuedFor("conv2"), pushes)
		}
	})
}
