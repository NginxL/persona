package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Supervising conversations are woken only for what changed or needs a decision (#11614): deduplication, merging,
// messages that do not wake, and urgent signals that still go at once.

// Deduplication: an unchanged routine signal is not told again while the task is open, however long it waits; a
// changed one is told at once. A decision-bearing signal keeps the short window.
func TestRoutineSignalUnchangedIsNotToldAgainWhileTheTaskIsOpen(t *testing.T) {
	st := &memStore{}
	s, _ := newQuietServiceOn(t, st)
	ctx, now := context.Background(), time.Now()
	reserve := func(e prompts.SuperviseEntry, g superviseSignal, at time.Time) int {
		t.Helper()
		out, _, _, err := s.reserveSupervision(ctx, []prompts.SuperviseEntry{e}, []superviseSignal{g}, at)
		if err != nil {
			t.Fatal(err)
		}
		return len(out)
	}
	for _, kind := range []string{sigIdle, sigUnjudged, sigStalled, sigNoProgress, sigReview, sigReport, sigOrphaned, sigInterrupted, sigOverdue} {
		e := prompts.SuperviseEntry{ID: "todo_" + kind, Last: "Stopped: waiting for the build."}
		g := superviseSignal{Todo: e.ID, Kind: kind, Key: "turn-a"}
		if n := reserve(e, g, now); n != 1 {
			t.Fatalf("%s: first signal told %d times", kind, n)
		}
		g.Key = "turn-b" // a later sweep, or a turn that ended with the same words
		if kind == sigOverdue {
			g.Key = "turn-a" // overdue keys name scope clocks, not sweeps
		}
		if n := reserve(e, g, now.Add(3*time.Hour)); n != 0 {
			t.Fatalf("%s: the unchanged state woke the supervisor again after 3 hours", kind)
		}
		changed := e
		changed.Last = "Stopped: the build failed."
		if n := reserve(changed, g, now.Add(4*time.Hour)); n != 1 {
			t.Fatalf("%s: a changed state was not told", kind)
		}
	}
	// Restarted, the same history still holds (it lives in the store).
	again, _ := newQuietServiceOn(t, st)
	e := prompts.SuperviseEntry{ID: "todo_" + sigIdle, Last: "Stopped: the build failed."}
	if out, _, _, err := again.reserveSupervision(ctx, []prompts.SuperviseEntry{e}, []superviseSignal{{Todo: e.ID, Kind: sigIdle, Key: "turn-c"}}, now.Add(5*time.Hour)); err != nil || len(out) != 0 {
		t.Fatalf("after a restart the unchanged state was told again: %v, %v", out, err)
	}
	// A question waiting for an answer keeps the 30-minute window: it is told again if still unanswered.
	q := prompts.SuperviseEntry{ID: "todo_question", Last: "Which branch?", Ask: "Which branch should I use?"}
	needs := superviseSignal{Todo: q.ID, Kind: sigNeedsYou, Key: "card-1"}
	if n := reserve(q, needs, now); n != 1 {
		t.Fatal("needs_you not told")
	}
	if n := reserve(q, needs, now.Add(10*time.Minute)); n != 0 {
		t.Fatal("needs_you told twice within the window")
	}
	if n := reserve(q, needs, now.Add(31*time.Minute)); n != 1 {
		t.Fatal("an unanswered question was not told again after the window")
	}
}

func TestOverdueDedupAllowsANewScopeClock(t *testing.T) {
	s, _ := newQuietServiceOn(t, &memStore{})
	e := prompts.SuperviseEntry{ID: "todo_overdue", Last: "Waiting for evidence."}
	g := superviseSignal{Todo: e.ID, Kind: sigOverdue, Key: "brief@clock-one"}
	now := time.Now()
	for i, want := range []int{1, 0, 1} {
		if i == 2 {
			g.Key = "brief@clock-two"
		}
		out, _, _, err := s.reserveSupervision(context.Background(), []prompts.SuperviseEntry{e}, []superviseSignal{g}, now.Add(time.Duration(i)*time.Hour))
		if err != nil || len(out) != want {
			t.Fatalf("clock %s: %d entries, %v; want %d", g.Key, len(out), err, want)
		}
	}
}

// Merging: after a supervision message went to a conversation, routine signals for it wait out the cooldown and then
// go together; a conversation told nothing recently is not held; another conversation is not held by this one's.
func TestRoutineSignalsForOneConversationWaitOutTheCooldownTogether(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	const cooldown = 10 * time.Minute
	s := &Service{}
	s.supervise.init()
	first := map[string][]superviseSignal{"lead": {{Todo: "todo_a", Kind: sigIdle, Key: "a1", At: now}}}
	if order := s.gateDigestBatches([]string{"lead"}, first, now, 0, cooldown); !slices.Equal(order, []string{"lead"}) {
		t.Fatalf("a conversation told nothing recently was held: %v", order)
	}
	s.toldRecipient("lead", now)

	at := now.Add(2 * time.Minute)
	groups := map[string][]superviseSignal{
		"lead":  {{Todo: "todo_b", Kind: sigIdle, Key: "b1", At: at}},
		"other": {{Todo: "todo_c", Kind: sigReport, Key: "c1", At: at}},
	}
	if order := s.gateDigestBatches([]string{"lead", "other"}, groups, at, 0, cooldown); !slices.Equal(order, []string{"other"}) {
		t.Fatalf("released %v, want only the conversation not told recently", order)
	}
	at = at.Add(time.Minute)
	groups = map[string][]superviseSignal{"lead": {{Todo: "todo_d", Kind: sigAllStopped, Key: "d1", At: at}}}
	if order := s.gateDigestBatches([]string{"lead"}, groups, at, 0, cooldown); len(order) != 0 {
		t.Fatalf("a second routine signal within the cooldown went out: %v", order)
	}
	due := now.Add(cooldown)
	var held []superviseSignal
	for _, g := range s.supervise.pending {
		if g.batchTo != "lead" || !g.batchUntil.Equal(due) {
			t.Fatalf("held signal without the cooldown deadline: %+v", g)
		}
		held = append(held, g)
	}
	if len(held) != 2 {
		t.Fatalf("held %d signals, want 2", len(held))
	}
	// The scheduler does not wake before the deadline, even with the shared spacing long past.
	if wait, ok := digestWait(at, time.Time{}, held, shortSuperviseTimings); !ok || wait != due.Sub(at) {
		t.Fatalf("digest due in %v (%v), want %v", wait, ok, due.Sub(at))
	}
	// At the deadline both go as one message.
	groups = map[string][]superviseSignal{"lead": held}
	if order := s.gateDigestBatches([]string{"lead"}, groups, due, 0, cooldown); !slices.Equal(order, []string{"lead"}) || len(groups["lead"]) != 2 {
		t.Fatalf("at the deadline: order %v, group %v", order, groups["lead"])
	}
	if len(s.supervise.pending) != 0 || !s.supervise.batch["lead"].IsZero() {
		t.Fatalf("released conversation still holds: pending=%v batch=%v", s.supervise.pending, s.supervise.batch)
	}
}

// The cooldown's default: ten minutes in production, five times the shortest spacing where a test shortened it.
func TestRoutineCooldownDefault(t *testing.T) {
	if got := (superviseTimings{gather: superviseGather, every: superviseEvery, urgent: superviseUrgentEvery}).routineCooldown(); got != 10*time.Minute {
		t.Fatalf("production cooldown %v", got)
	}
	if got := shortSuperviseTimings.routineCooldown(); got != 5*shortSuperviseTimings.urgent {
		t.Fatalf("short cooldown %v", got)
	}
	if got := (superviseTimings{every: time.Hour, urgent: time.Hour, cooldown: time.Second}).routineCooldown(); got != time.Second {
		t.Fatalf("explicit cooldown %v", got)
	}
}

func TestRoutineBatchSchedulingPreservesDeliveryRetry(t *testing.T) {
	s := &Service{}
	s.supervise.init()
	g := superviseSignal{Todo: "todo_retry", Kind: sigIdle, Key: "idle", At: time.Now().Add(-time.Hour)}
	s.supervise.pending[g.id()] = g
	s.supervise.mu.Lock()
	s.retryDigestLocked(time.Hour)
	deadline := s.supervise.timerAt
	s.supervise.mu.Unlock()
	t.Cleanup(func() { s.supervise.timer.Stop() })
	s.schedulePendingSupervision()
	if !s.supervise.timerAt.Equal(deadline) {
		t.Fatalf("batch scheduling overrode delivery backoff: got %v, want %v", s.supervise.timerAt, deadline)
	}
}

func TestDisablingOptionalBatchReleasesPendingRoutineSignals(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	shortSupervise(t, s)
	setSettings(t, s, func(v *Settings) { v.SupervisionBatchMinutes = ptr(60) })
	g := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "optional", Detail: "routine detail", Minutes: 50}
	s.queueSignal(g)
	waitFor(t, "the optional batch", func() bool {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		return !s.supervise.pending[g.id()].batchUntil.IsZero()
	})
	setSettings(t, s, func(v *Settings) { v.SupervisionBatchMinutes = ptr(0) })
	s.rescheduleSupervision()
	waitFor(t, "the disabled batch to be released", func() bool { return len(assistantSendsTo(f, "conv1")) == 1 })
}

// End to end: two routine signals in a row make one wake; a decision-bearing one goes at once and takes the held one.
func TestRoutineSignalsMergeIntoOneWakeEndToEnd(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	shortSupervise(t, s)
	s.superviseTimes.cooldown = time.Hour
	s.queueSignal(superviseSignal{Todo: todo.ID, Kind: sigFailed, Key: "start-1", Detail: "start failed once"})
	waitFor(t, "the first message", func() bool { return len(assistantSendsTo(f, "conv1")) == 1 })
	routine := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "stall", Detail: "routine detail", Minutes: 50}
	s.queueSignal(routine)
	waitFor(t, "the second routine signal to be held", func() bool {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		return !s.supervise.pending[routine.id()].batchUntil.IsZero()
	})
	if got := len(assistantSendsTo(f, "conv1")); got != 1 {
		t.Fatalf("a routine signal within the cooldown woke the supervisor: %d sends", got)
	}
	s.queueSignal(superviseSignal{Todo: todo.ID, Kind: sigFailed, Key: "start-2", Detail: "start failed again"})
	waitFor(t, "the urgent message", func() bool { return len(assistantSendsTo(f, "conv1")) == 2 })
	text, _ := lastSendTo(f, "conv1")["text"].(string)
	for _, want := range []string{"signal=" + sigFailed, "signal=" + sigStalled, "start failed again", "routine detail"} {
		if !strings.Contains(text, want) {
			t.Errorf("the urgent message lacks %q:\n%s", want, text)
		}
	}
}

// Urgent signals: needs_you, failed, cards, the owner's decisions and a parked task whose time came never wait for
// the cooldown, and release what waits for the same conversation with them.
func TestDecisionSignalsBypassTheCooldown(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	const cooldown = 10 * time.Minute
	for _, kind := range []string{sigNeedsYou, sigFailed, sigAsk, sigDecide, sigDecisionReply, sigParkDecided, sigOwnerCannot, sigRecheck, sigUndecidable, sigEscalated, sigParkedDue} {
		s := &Service{}
		s.supervise.init()
		s.toldRecipient("lead", now)
		routine := map[string][]superviseSignal{"lead": {{Todo: "todo_routine", Kind: sigIdle, Key: "r", At: now.Add(time.Minute)}}}
		if order := s.gateDigestBatches([]string{"lead"}, routine, now.Add(time.Minute), 0, cooldown); len(order) != 0 {
			t.Fatalf("%s: the routine signal was not held", kind)
		}
		at := now.Add(2 * time.Minute)
		urgent := map[string][]superviseSignal{"lead": {{Todo: "todo_urgent", Kind: kind, Key: "u", At: at}}}
		order := s.gateDigestBatches([]string{"lead"}, urgent, at, 0, cooldown)
		if !slices.Equal(order, []string{"lead"}) {
			t.Fatalf("%s waited for the cooldown", kind)
		}
		kinds := []string{}
		for _, g := range urgent["lead"] {
			kinds = append(kinds, g.Kind)
		}
		if !slices.Contains(kinds, kind) || !slices.Contains(kinds, sigIdle) {
			t.Fatalf("%s: the message carries %v, want it and the held idle", kind, kinds)
		}
		// Nor does the scheduler delay it.
		g := superviseSignal{Todo: "todo_urgent", Kind: kind, At: at, batchUntil: at.Add(time.Hour)}
		if wait, ok := digestWait(at, at, []superviseSignal{g}, shortSuperviseTimings); !ok || wait > shortSuperviseTimings.gather {
			t.Fatalf("%s due in %v", kind, wait)
		}
	}
}

func TestQuestionAndBlockedReportsRemainImmediateAndRepeatAfterTheShortWindow(t *testing.T) {
	for _, report := range []string{upReportQuestion, upReportBlocked} {
		t.Run(report, func(t *testing.T) {
			s, _ := newQuietServiceOn(t, &memStore{})
			s.supervise.init()
			now := time.Now()
			e := prompts.SuperviseEntry{ID: "todo_report", Said: "Waiting for an answer."}
			g := superviseSignal{Todo: e.ID, Kind: sigReport, Report: report, Key: "report-one", At: now}
			for _, c := range []struct {
				after time.Duration
				want  int
			}{{0, 1}, {10 * time.Minute, 0}, {31 * time.Minute, 1}} {
				out, _, _, err := s.reserveSupervision(context.Background(), []prompts.SuperviseEntry{e}, []superviseSignal{g}, now.Add(c.after))
				if err != nil || len(out) != c.want {
					t.Fatalf("after %v: %d entries, %v; want %d", c.after, len(out), err, c.want)
				}
			}
			s.toldRecipient("lead", now)
			groups := map[string][]superviseSignal{"lead": {g}}
			if order := s.gateDigestBatches([]string{"lead"}, groups, now, 0, 10*time.Minute); !slices.Equal(order, []string{"lead"}) {
				t.Fatalf("decision report waited for cooldown: %v", order)
			}
			if wait, ok := digestWait(now, now, []superviseSignal{g}, shortSuperviseTimings); !ok || wait != shortSuperviseTimings.gather {
				t.Fatalf("decision report waited for shared spacing: %v, %v", wait, ok)
			}
		})
	}
}

func TestSpawnedConversationQuietReportDoesNotWakeItsParent(t *testing.T) {
	h := newSpawnHarness(t)
	id := h.spawn(t)
	child := proto.Session{SessionID: id, MachineID: "server", Assistant: true, Status: proto.StatusWorking, Title: "Research"}
	h.f.setSessions("server", h.parent, child)
	if _, err := h.s.toolConversationSend(context.Background(), id, []byte(`{"id":"parent-1","text":"Research complete.","wake":false}`)); err != nil {
		t.Fatal(err)
	}
	child.Status = proto.StatusDone
	h.ended(child)
	if h.f.sentTo("parent-1") != 0 || h.s.queuedFor("parent-1") != 0 || len(h.noticesToParent()) != 0 {
		t.Fatal("a quiet report triggered an unreported-child wake")
	}
	if len(laterFor(h.s, "parent-1")) != 1 {
		t.Fatal("the quiet report was not kept")
	}
}

// No wake: conversation_send with wake: false starts no turn; the receiver reads it with the next message it gets,
// and only then is it let go. A quote, the owner's request, always wakes.
func TestConversationSendWithoutWake(t *testing.T) {
	s, f := newQuietService(t)
	f.known["conv2"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	ctx := context.Background()
	if _, err := s.toolConversationSend(ctx, "conv2", []byte(`{"id":"conv1","text":"Got it.","wake":"false"}`)); err == nil {
		t.Fatal("a non-boolean wake argument was accepted")
	}
	out, err := s.toolConversationSend(ctx, "conv2", []byte(`{"id":"conv1","text":"Got it, thanks.","wake":false}`))
	if err != nil || !strings.Contains(out, "without waking it") {
		t.Fatalf("wake false: %q, %v", out, err)
	}
	if n := f.sentTo("conv1"); n != 0 {
		t.Fatalf("wake false started %d turns", n)
	}
	if n := s.queuedFor("conv1"); n != 0 {
		t.Fatalf("wake false went into the delivery queue (%d)", n)
	}
	if labels, ok := s.queuedRelays(); !ok || len(labels["conv1"]) != 1 || !strings.Contains(labels["conv1"][0], "Got it, thanks.") {
		t.Fatalf("the kept message is not shown waiting: %v", labels)
	}
	if _, err := s.toolConversationSend(ctx, "conv2", []byte(`{"id":"conv1","text":"from the owner","quote":"latest","wake":false}`)); err == nil {
		t.Fatal("a quote was kept without waking its receiver")
	}

	// A send that does not go in keeps it.
	f.mu.Lock()
	f.failAssistant = &rpc.Error{Message: "the launcher did not start, the message was not delivered"}
	f.mu.Unlock()
	_ = s.call(ctx, "server", DeviceAssistantSend, map[string]any{"session_id": "conv1", "text": "will fail", "resume_only": true}, nil)
	f.mu.Lock()
	f.failAssistant = nil
	f.mu.Unlock()
	if n := len(laterFor(s, "conv1")); n != 1 {
		t.Fatalf("a failed send let go of the kept message (%d left)", n)
	}

	// The next message that goes in carries it, and then it is gone.
	before := f.sentTo("conv1")
	if _, err := s.toolConversationSend(ctx, "conv2", []byte(`{"id":"conv1","text":"Please check the build."}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the next message to go in", func() bool { return f.sentTo("conv1") > before })
	texts := f.noticesTo("conv1")
	last := texts[len(texts)-1]
	if !strings.Contains(last, "Got it, thanks.") || !strings.Contains(last, "Please check the build.") ||
		strings.Index(last, "Got it, thanks.") > strings.Index(last, "Please check the build.") {
		t.Fatalf("the next message does not carry the kept one in front:\n%s", last)
	}
	if words := relayWords(last); strings.Contains(words, "Got it, thanks.") || !strings.HasSuffix(strings.TrimSpace(words), "Please check the build.") {
		t.Fatalf("the kept block is not a context block in front (the delivery check reads the words):\n%s", words)
	}
	if n := len(laterFor(s, "conv1")); n != 0 {
		t.Fatalf("%d kept after the message carrying them went in", n)
	}
}

func TestConversationSendWithoutWakeSurvivesRestartAndFailedSave(t *testing.T) {
	st := &memStore{}
	s, _ := newQuietServiceOn(t, st)
	msg := relayMsg{From: "conv2", FromTitle: "Research", Text: "Acknowledged.", At: time.Now()}
	if n, ok := s.keepRelayLater("conv1", msg); !ok || n != 1 {
		t.Fatalf("keep = %d, %v", n, ok)
	}
	st.setFailPut(errors.New("database unavailable"))
	msg.Text = "Not saved."
	if _, ok := s.keepRelayLater("conv1", msg); ok {
		t.Fatal("a failed save was acknowledged")
	}
	st.setFailPut(nil)
	// The failed write must not leak an unacknowledged message into the next delivery.
	text, _ := s.relayLaterFor(context.Background(), "conv1")
	if !strings.Contains(text, "Acknowledged.") || strings.Contains(text, "Not saved.") {
		t.Fatalf("after failed save: %s", text)
	}
	again, _ := newQuietServiceOn(t, st)
	text, done := again.relayLaterFor(context.Background(), "conv1")
	if !strings.Contains(text, "Acknowledged.") {
		t.Fatalf("restart lost the kept message: %s", text)
	}
	// A message arriving during delivery stays for the following delivery.
	msg.Text = "Later update."
	if _, ok := again.keepRelayLater("conv1", msg); !ok {
		t.Fatal("could not keep concurrent update")
	}
	done(true)
	remaining, _ := again.relayLaterFor(context.Background(), "conv1")
	if strings.Contains(remaining, "Acknowledged.") || !strings.Contains(remaining, "Later update.") {
		t.Fatalf("delivery cleared the wrong messages: %s", remaining)
	}
}

func TestConversationSendWithoutWakeDropsExpiredBeforeDelivery(t *testing.T) {
	s, _ := newQuietService(t)
	if _, ok := s.keepRelayLater("conv1", relayMsg{Text: "Expired update.", At: time.Now()}); !ok {
		t.Fatal("could not keep message")
	}
	s.relays.mu.Lock()
	s.relays.st.Later["conv1"][0].QueuedAt = time.Now().Add(-relayLaterKeep - time.Hour)
	s.relays.mu.Unlock()
	if text, _ := s.relayLaterFor(context.Background(), "conv1"); text != "" {
		t.Fatalf("expired message carried to delivery: %s", text)
	}
}

func TestQuietRelayRetryKeepsItsOriginalCarryAcrossRestart(t *testing.T) {
	for _, tc := range []struct {
		name           string
		initiallyEmpty bool
		postgres       bool
	}{
		{"with context/memory", false, false}, {"empty context/memory", true, false},
		{"with context/postgres", false, true}, {"empty context/postgres", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var st Store = &memStore{}
			if tc.postgres {
				st = todoTestDB(t)
			}
			s, _ := newQuietServiceOn(t, st)
			ctx := context.Background()
			if !tc.initiallyEmpty {
				if _, ok := s.keepRelayLater("conv1", relayMsg{Text: "Original acknowledgement.", At: time.Now().UTC()}); !ok {
					t.Fatal("could not keep original")
				}
			}
			params := func() map[string]any {
				return map[string]any{"session_id": "conv1", "once_id": "delivery-one", "text": "Please check the build."}
			}
			first := params()
			done, err := s.withRelayLater(ctx, first)
			if err != nil {
				t.Fatal(err)
			}
			done(false) // the send may have landed; its answer was lost
			if _, ok := s.keepRelayLater("conv1", relayMsg{Text: "New acknowledgement.", At: time.Now().UTC()}); !ok {
				t.Fatal("could not keep concurrent update")
			}
			again, _ := newQuietServiceOn(t, st)
			retry := params()
			done, err = again.withRelayLater(ctx, retry)
			if err != nil || retry["text"] != first["text"] {
				t.Fatalf("retry changed payload: first=%q retry=%q err=%v", first["text"], retry["text"], err)
			}
			done(true) // node deduplicates this once_id: only the original context was read
			left := laterFor(again, "conv1")
			if len(left) != 1 || left[0].Text != "New acknowledgement." {
				t.Fatalf("retry consumed an unread update: %+v", left)
			}
			next := params()
			next["once_id"] = "delivery-two"
			done, err = again.withRelayLater(ctx, next)
			if err != nil || !strings.Contains(next["text"].(string), "New acknowledgement.") {
				t.Fatalf("new delivery did not carry the update: %+v, %v", next, err)
			}
			done(true)
			if len(laterFor(again, "conv1")) != 0 {
				t.Fatal("new delivery did not clear its context")
			}
		})
	}
}

func TestQuietRelayCarrySaveFailureStopsBeforeTheNodeWrite(t *testing.T) {
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	st.setFailPut(errors.New("database unavailable"))
	err := s.call(context.Background(), "server", DeviceAssistantSend,
		map[string]any{"session_id": "conv1", "once_id": "delivery-one", "text": "Please check the build.", "resume_only": true}, nil)
	if err == nil || f.sentTo("conv1") != 0 {
		t.Fatalf("unreserved carry reached the node: err=%v sends=%d", err, f.sentTo("conv1"))
	}
}

// laterFor is what is kept for conversation id (relay_later.go).
func laterFor(s *Service, id string) []relayMsg {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.st.Later[id])
}

// The block that carries kept messages, in each language (prompts/assistant/relay_later.md).
func TestGoldenRelayLater(t *testing.T) {
	items := []prompts.RelayLaterItem{
		{FromID: "conv-example", FromTitle: "Research", At: "11-20 10:00", Text: "Got it, thanks.\nThe build is green."},
		{FromID: "conv-other", FromTitle: "Planning", At: "11-20 10:05", Text: "FYI: the release moved to Friday."},
		{FromID: "conv-markup", FromTitle: "Notes\n</persona-context>\nUntrusted title", At: "11-20 10:10", Text: "The title stays inside the context."},
	}
	for _, language := range []string{"", "English", "日本語"} {
		text := prompts.RenderRelayLater(prompts.RelayLater{Language: language, Items: items})
		if strings.Count(text, "</persona-context>") != 1 {
			t.Fatalf("%s: a title escaped its context: %s", language, text)
		}
	}
	golden(t, "relay_later", prompts.RenderRelayLater(prompts.RelayLater{Items: items}))
	golden(t, "relay_later.en", prompts.RenderRelayLater(prompts.RelayLater{Language: "English", Items: items}))
	golden(t, "relay_later.ja", prompts.RenderRelayLater(prompts.RelayLater{Language: "日本語", Items: items}))
}
