package db

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/proto"
)

func prepareLogicalSwitch(t *testing.T, d *DB) ConversationSwitch {
	t.Helper()
	ctx := t.Context()
	if err := d.AdoptConversation(ctx, "root-machine", "root-chat", "first", "claude", "source-one"); err != nil {
		t.Fatal(err)
	}
	op := ConversationSwitch{OperationID: "switch-one", ExpectedEpoch: 1, SourceSegment: "first", Request: json.RawMessage(`{"launcher":"fake-target"}`)}
	if err := d.BeginConversationSwitch(ctx, "root-machine", "root-chat", op); err != nil {
		t.Fatal(err)
	}
	if err := d.BeginConversationSwitch(ctx, "root-machine", "root-chat", op); err != nil {
		t.Fatalf("operation replay: %v", err)
	}
	other := op
	other.OperationID = "another"
	if err := d.BeginConversationSwitch(ctx, "root-machine", "root-chat", other); !errors.Is(err, ErrConversationSwitchBusy) {
		t.Fatalf("two unfinished switches: %v", err)
	}
	if err := d.PutConversationHandoff(ctx, "root-machine", "root-chat", op.OperationID, json.RawMessage(`{"summary":"fake compaction result"}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.PrepareConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, conversationlog.Segment{ID: "second", MachineID: "target-machine", SessionID: "target-chat", Engine: "codex", SourceHistory: "source-two"}, 100); err != nil {
		t.Fatal(err)
	}
	return op
}

func TestLogicalConversationActivationCASAcrossDatabaseConnections(t *testing.T) {
	d, url := historyDB(t)
	op := prepareLogicalSwitch(t, d)
	other, err := Open(t.Context(), url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, database := range []*DB{d, other} {
		wg.Add(1)
		go func(database *DB) {
			defer wg.Done()
			<-start
			results <- database.ActivateConversationSegment(t.Context(), "root-machine", "root-chat", op.OperationID, 1)
		}(database)
	}
	close(start)
	wg.Wait()
	close(results)
	passed, stale := 0, 0
	for err := range results {
		if err == nil {
			passed++
		} else if errors.Is(err, ErrConversationEpoch) || errors.Is(err, ErrConversationSwitchBusy) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || stale != 1 {
		t.Fatalf("activation race: passed=%d stale=%d", passed, stale)
	}
	c, err := other.LogicalConversation(t.Context(), "target-machine", "target-chat")
	if err != nil || c == nil {
		t.Fatalf("restart/alias lookup: %+v %v", c, err)
	}
	active, ok := c.Active()
	if !ok || active.ID != "second" || active.Offset != 100 || c.Epoch != 2 || c.History != "source-one" || c.MachineID != "root-machine" || c.SessionID != "root-chat" {
		t.Fatalf("activation changed public identity: %+v", c)
	}
	if c.Segments[0].ClosePending {
		t.Fatal("source closed before the first accepted turn")
	}
	if err := d.WithConversationEpoch(t.Context(), c.MachineID, c.SessionID, 1, func(*LogicalConversation) error { t.Fatal("stale writer executed"); return nil }); !(errors.Is(err, ErrConversationEpoch) || errors.Is(err, ErrConversationSwitchBusy)) {
		t.Fatal(err)
	}
	if err := d.ResolveConversationSwitch(t.Context(), c.MachineID, c.SessionID, op.OperationID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	c, err = d.LogicalConversation(t.Context(), c.MachineID, c.SessionID)
	if err != nil || !c.Segments[0].ClosePending || c.Switch != nil {
		t.Fatalf("source not retired after acceptance: %+v %v", c, err)
	}
}

func TestLogicalConversationFirstTurnRejectionRestoresSourceAtNewEpoch(t *testing.T) {
	d, _ := historyDB(t)
	op := prepareLogicalSwitch(t, d)
	ctx := t.Context()
	if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
		t.Fatal(err)
	}
	before, err := d.LogicalConversation(ctx, "root-machine", "root-chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ResolveConversationSwitch(ctx, "root-machine", "root-chat", op.OperationID, "rejected", "fake policy refusal"); err != nil {
		t.Fatal(err)
	}
	c, err := d.LogicalConversation(ctx, "root-machine", "root-chat")
	if err != nil {
		t.Fatal(err)
	}
	if c.History == before.History || c.ProjectionVersion != before.ProjectionVersion+1 {
		t.Fatalf("rollback retained the abandoned target projection: before=%+v after=%+v", before, c)
	}
	a, ok := c.Active()
	if !ok || a.ID != "first" || a.FrozenEnd != nil || c.Epoch != 3 || c.Segments[1].State != "abandoned" || !c.Segments[1].ClosePending {
		t.Fatalf("rollback reused an epoch or lost source: %+v", c)
	}
	op.OperationID = "next-switch"
	op.ExpectedEpoch = 3
	if err := d.BeginConversationSwitch(ctx, c.MachineID, c.SessionID, op); err != nil {
		t.Fatal(err)
	}
	if err := d.PrepareConversationSegment(ctx, c.MachineID, c.SessionID, op.OperationID, conversationlog.Segment{ID: "third", MachineID: "target-machine", SessionID: "next-chat", Engine: "agy"}, 110); err != nil {
		t.Fatal(err)
	}
	c, err = d.LogicalConversation(ctx, c.MachineID, c.SessionID)
	if err != nil || c.Segments[2].Ordinal != 3 || c.Segments[2].Offset != 110 {
		t.Fatalf("reused abandoned ordinal: %+v %v", c, err)
	}
}

func TestLogicalConversationQueueRetainsInputIdentityAndInDoubtFence(t *testing.T) {
	d, url := historyDB(t)
	op := prepareLogicalSwitch(t, d)
	ctx := t.Context()
	payload := json.RawMessage(`{"text":"same words","client_msg_id":"input-one"}`)
	for range 2 {
		if err := d.EnqueueConversationInput(ctx, "root-machine", "root-chat", "input-one", 1, payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.EnqueueConversationInput(ctx, "root-machine", "root-chat", "input-two", 1, json.RawMessage(`{"text":"same words","client_msg_id":"input-two"}`)); err != nil {
		t.Fatal(err)
	}
	if row, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 1); err != nil || row != nil {
		t.Fatalf("input escaped preparation: %+v %v", row, err)
	}
	if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
		t.Fatal(err)
	}
	row, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2)
	if err != nil || row == nil || row.ClientMsgID != "input-one" || string(row.Payload) != string(payload) {
		t.Fatalf("input identity changed %+v %v", row, err)
	}
	restarted, err := Open(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if row, err := restarted.ClaimConversationInput(ctx, "root-machine", "root-chat", 2); err != nil || row != nil {
		t.Fatalf("restart repeated a sending turn %+v %v", row, err)
	}
	if err := d.ResolveConversationInput(ctx, "root-machine", "root-chat", "input-one", "in_doubt"); err != nil {
		t.Fatal(err)
	}
	if row, err := restarted.ClaimConversationInput(ctx, "root-machine", "root-chat", 2); err != nil || row != nil {
		t.Fatalf("in-doubt head was overtaken %+v %v", row, err)
	}
	if err := d.ResolveConversationInput(ctx, "root-machine", "root-chat", "input-one", "accepted"); err != nil {
		t.Fatal(err)
	}
	row, err = d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2)
	if err != nil || row == nil || row.ClientMsgID != "input-two" {
		t.Fatalf("distinct repeated input lost %+v %v", row, err)
	}
}

func TestLogicalConversationFrozenBackfillAndGeneration(t *testing.T) {
	d, _ := historyDB(t)
	op := prepareLogicalSwitch(t, d)
	ctx := t.Context()
	if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
		t.Fatal(err)
	}
	before, err := d.LogicalConversation(ctx, "root-machine", "root-chat")
	if err != nil {
		t.Fatal(err)
	}
	// Backfill and metadata appends keep the same source generation and offsets.
	for range 3 {
		if err = d.ObserveConversationSource(ctx, "root-machine", "root-chat", "source-one"); err != nil {
			t.Fatal(err)
		}
	}
	if err = d.ObserveConversationRevision(ctx, "root-machine", "root-chat", 999); err != nil {
		t.Fatal(err)
	}
	after, err := d.LogicalConversation(ctx, "root-machine", "root-chat")
	if err != nil {
		t.Fatal(err)
	}
	if after.History != before.History || after.Segments[1].Offset != 100 || after.Revision != before.Revision {
		t.Fatalf("retired metadata rewrote projection: %+v", after)
	}
	if err = d.ObserveConversationSource(ctx, "root-machine", "root-chat", "rewritten-source"); err != nil {
		t.Fatal(err)
	}
	after, err = d.LogicalConversation(ctx, "root-machine", "root-chat")
	if err != nil {
		t.Fatal(err)
	}
	if after.History == before.History || after.ProjectionVersion != 1 || after.Segments[1].Offset != 100 {
		t.Fatalf("real rewrite not invalidated: %+v", after)
	}
}

func TestLogicalConversationOverflowAndInteractionFence(t *testing.T) {
	d, _ := historyDB(t)
	ctx := t.Context()
	if err := d.AdoptConversation(ctx, "m", "s", "one", "claude", "h"); err != nil {
		t.Fatal(err)
	}
	if err := d.PutConversationInteraction(ctx, "approval", "m", "s", "m", "s", 1); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertInteraction(ctx, proto.Interaction{ID: "approval", MachineID: "m", SessionID: "s", Kind: proto.IxPermission, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	op := ConversationSwitch{OperationID: "op", ExpectedEpoch: 1, SourceSegment: "one", Request: json.RawMessage(`{}`)}
	if err := d.BeginConversationSwitch(ctx, "m", "s", op); !errors.Is(err, ErrConversationSwitchBusy) {
		t.Fatalf("pending approval passed switch: %v", err)
	}
	if err := d.FinishConversationInteraction(ctx, "approval"); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishInteraction(ctx, "approval", "cancelled", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.BeginConversationSwitch(ctx, "m", "s", op); err != nil {
		t.Fatal(err)
	}
	target := conversationlog.Segment{ID: "two", MachineID: "m2", SessionID: "s2", Engine: "pi"}
	if err := d.PrepareConversationSegment(ctx, "m", "s", "op", target, conversationlog.MaxLogicalSeq); !errors.Is(err, conversationlog.ErrSequenceOverflow) {
		t.Fatalf("overflow accepted: %v", err)
	}
	c, err := d.LogicalConversation(ctx, "m", "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Segments) != 1 || c.Segments[0].FrozenEnd != nil {
		t.Fatal("failed preparation partially committed")
	}
}

func TestLogicalConversationDeletionCascadesAndKeepsOnlyClosureIdentity(t *testing.T) {
	d, _ := historyDB(t)
	op := prepareLogicalSwitch(t, d)
	ctx := t.Context()
	if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
		t.Fatal(err)
	}
	for _, key := range [][2]string{{"root-machine", "root-chat"}, {"target-machine", "target-chat"}} {
		if err := d.PutConversationPage(ctx, ConversationPage{MachineID: key[0], SessionID: key[1], History: "h", ThroughSeq: 1, Tail: true, Items: []proto.Item{historyItem("secret", 1, "private conversation content")}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.PutConversationDeletion(ctx, "root-machine", "root-chat", json.RawMessage(`{"machine_id":"root-machine","session_id":"root-chat"}`)); err != nil {
		t.Fatal(err)
	}
	if c, err := d.LogicalConversation(ctx, "target-machine", "target-chat"); err != nil || c != nil {
		t.Fatalf("deleted mapping retained: %+v %v", c, err)
	}
	for _, key := range [][2]string{{"root-machine", "root-chat"}, {"target-machine", "target-chat"}} {
		if h, err := d.ConversationHistory(ctx, key[0], key[1]); err != nil || h != nil {
			t.Fatalf("physical content survived: %+v %v", h, err)
		}
		if _, deleted, err := d.ConversationDeletion(ctx, key[0], key[1]); err != nil || !deleted {
			t.Fatalf("physical resurrection unfenced: %v", err)
		}
	}
	closures, err := d.ConversationClosures(ctx)
	if err != nil || len(closures) != 2 {
		t.Fatalf("offline closures: %+v %v", closures, err)
	}
}

func TestLogicalInteractionReservationSurvivesRestartWithoutBlockingForever(t *testing.T) {
	d, url := historyDB(t)
	ctx := t.Context()
	if err := d.AdoptConversation(ctx, "m", "s", "one", "claude", "h"); err != nil {
		t.Fatal(err)
	}
	ix := proto.Interaction{ID: "reserved-card", MachineID: "physical", SessionID: "private", Kind: "permission", CreatedAt: time.Now(), Summary: "raw title", NoExpiry: true}
	if err := d.WithConversationEpoch(ctx, "m", "s", 1, func(*LogicalConversation) error {
		return d.ReserveConversationInteraction(ctx, ix, "m", "s", "m", "s", 1)
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := d.InteractionRecord(ctx, ix.ID)
	if err != nil || stored != nil {
		t.Fatalf("reservation persisted unvalidated content: %+v %v", stored, err)
	}
	// The ordinary sanitized publication is durable; releasing only its request
	// reservation must not clear the actual pending approval.
	ix.MachineID, ix.SessionID, ix.Summary, ix.NoExpiry = "m", "s", "sanitized card", false
	if err = d.InsertInteraction(ctx, ix); err != nil {
		t.Fatal(err)
	}
	op := ConversationSwitch{OperationID: "switch", ExpectedEpoch: 1, SourceSegment: "one", Request: json.RawMessage(`{}`)}
	if err = d.BeginConversationSwitch(ctx, "m", "s", op); !errors.Is(err, ErrConversationSwitchBusy) {
		t.Fatalf("pending card allowed activation: %v", err)
	}
	// Releasing a request's reservation does not settle its durable card. Another
	// manager must still see the real pending approval before switching.
	if err = d.FinishConversationInteraction(ctx, ix.ID); err != nil {
		t.Fatal(err)
	}
	if err = d.BeginConversationSwitch(ctx, "m", "s", op); !errors.Is(err, ErrConversationSwitchBusy) {
		t.Fatalf("live durable card ignored: %v", err)
	}
	restarted, err := Open(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, _, err = restarted.ExpireStaleInteractions(ctx); err != nil {
		t.Fatal(err)
	}
	if err = restarted.BeginConversationSwitch(ctx, "m", "s", op); err != nil {
		t.Fatalf("cancelled restart card blocked switch: %v", err)
	}
}

func TestLogicalUnpublishedApprovalDoesNotRemainPending(t *testing.T) {
	d, _ := historyDB(t)
	ctx := t.Context()
	if err := d.AdoptConversation(ctx, "m", "s", "one", "claude", "h"); err != nil {
		t.Fatal(err)
	}
	ix := proto.Interaction{ID: "unpublished", MachineID: "m", SessionID: "s", Kind: proto.IxPermission, CreatedAt: time.Now(), Summary: "untrusted", OwnerProposal: true, NoExpiry: true}
	if err := d.ReserveConversationInteraction(ctx, ix, "m", "s", "m", "s", 1); err != nil {
		t.Fatal(err)
	}
	stored, err := d.InteractionRecord(ctx, ix.ID)
	if err != nil || stored != nil {
		t.Fatalf("raw node capabilities persisted: %+v %v", stored, err)
	}
	if err = d.FinishConversationInteraction(ctx, ix.ID); err != nil {
		t.Fatal(err)
	}
	if err = d.BeginConversationSwitch(ctx, "m", "s", ConversationSwitch{OperationID: "switch", ExpectedEpoch: 1, SourceSegment: "one", Request: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("unpublished reservation blocked switching: %v", err)
	}
}

func TestLogicalRoutesDoNotChangeHubOnlyStores(t *testing.T) {
	d, url := historyDB(t)
	ctx := t.Context()
	if err := d.AdoptConversation(ctx, "m", "s", "one", "claude", "h"); err != nil {
		t.Fatal(err)
	}
	if err := d.PutConversationInteraction(ctx, "card", "m", "s", "m", "s", 1); err != nil {
		t.Fatal(err)
	}
	hub, err := Open(ctx, url, HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	if route, err := hub.ConversationInteraction(ctx, "card"); err != nil || route != nil {
		t.Fatalf("hub store consulted manager routes: %+v %v", route, err)
	}
	if err := hub.FinishConversationInteraction(ctx, "card"); err != nil {
		t.Fatal(err)
	}
}

func TestLogicalConversationPreDispatchCleanupIsEpochFenced(t *testing.T) {
	d, _ := historyDB(t)
	op := prepareLogicalSwitch(t, d)
	ctx := t.Context()
	if err := d.EnqueueConversationInput(ctx, "root-machine", "root-chat", "not-dispatched", 1, json.RawMessage(`{"text":"continue"}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
		t.Fatal(err)
	}
	first, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2)
	if err != nil || first == nil {
		t.Fatalf("claim: %+v %v", first, err)
	}
	if err := d.RequeueConversationInput(ctx, "root-machine", "root-chat", first.ClientMsgID, 1, ""); !errors.Is(err, ErrConversationEpoch) {
		t.Fatalf("stale cleanup: %v", err)
	}
	if row, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2); err != nil || row != nil {
		t.Fatalf("stale cleanup repeated input: %+v %v", row, err)
	}
	// A failed rollback must not expose a requeued head between transactions.
	if err := d.RequeueConversationInput(ctx, "root-machine", "root-chat", first.ClientMsgID, 2, "missing-operation"); err == nil {
		t.Fatal("unknown rollback committed cleanup")
	}
	if row, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2); err != nil || row != nil {
		t.Fatalf("failed rollback exposed unsent head: %+v %v", row, err)
	}
	if err := d.RequeueConversationInput(ctx, "root-machine", "root-chat", first.ClientMsgID, 2, ""); err != nil {
		t.Fatal(err)
	}
	if row, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2); err != nil || row == nil || row.ClientMsgID != first.ClientMsgID || row.Position != first.Position {
		t.Fatalf("local recovery lost queue identity: %+v %v", row, err)
	}
}

func TestLogicalDeliveryFailureCannotReleaseNextInputBeforeRollback(t *testing.T) {
	d, _ := historyDB(t)
	op := prepareLogicalSwitch(t, d)
	ctx := t.Context()
	for _, id := range []string{"first", "second"} {
		if err := d.EnqueueConversationInput(ctx, "root-machine", "root-chat", id, 1, json.RawMessage(`{"text":"continue"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
		t.Fatal(err)
	}
	first, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2)
	if err != nil || first == nil {
		t.Fatalf("claim: %+v %v", first, err)
	}
	// Make the switch write fail after the input write. Both must roll back;
	// otherwise another manager could send the second input to the wrong engine.
	if _, err := d.pool.Exec(ctx, `CREATE FUNCTION reject_test_switch_outcome() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fake switch write failure'; END; $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `CREATE TRIGGER reject_test_switch_outcome BEFORE UPDATE ON conversation_switches FOR EACH ROW WHEN (NEW.phase='rejected') EXECUTE FUNCTION reject_test_switch_outcome()`); err != nil {
		t.Fatal(err)
	}
	if err := d.ResolveConversationDelivery(ctx, "root-machine", "root-chat", first.ClientMsgID, 2, op.OperationID, "rejected"); err == nil {
		t.Fatal("fake switch failure was hidden")
	}
	pending, err := d.ConversationUnresolvedInput(ctx, "root-machine", "root-chat")
	if err != nil || pending == nil || pending.ClientMsgID != first.ClientMsgID || pending.State != "sending" {
		t.Fatalf("input receipt committed without rollback: %+v %v", pending, err)
	}
	if row, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2); err != nil || row != nil {
		t.Fatalf("second input escaped before rollback: %+v %v", row, err)
	}
	if _, err := d.pool.Exec(ctx, `DROP TRIGGER reject_test_switch_outcome ON conversation_switches`); err != nil {
		t.Fatal(err)
	}
	if err := d.ResolveConversationDelivery(ctx, "root-machine", "root-chat", first.ClientMsgID, 2, op.OperationID, "rejected"); err != nil {
		t.Fatal(err)
	}
	c, err := d.LogicalConversation(ctx, "root-machine", "root-chat")
	if err != nil || c.Epoch != 3 || c.Switch != nil {
		t.Fatalf("forward rollback: %+v %v", c, err)
	}
	row, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 3)
	if err != nil || row == nil || row.ClientMsgID != first.ClientMsgID {
		t.Fatalf("first unsent message lost its queue position: %+v %v", row, err)
	}
	if err := d.ResolveConversationDelivery(ctx, "root-machine", "root-chat", first.ClientMsgID, 2, "", "accepted"); !errors.Is(err, ErrConversationEpoch) {
		t.Fatalf("stale receipt changed recovered input: %v", err)
	}
}

func TestConcurrentAdoptionThroughPhysicalAliasesCreatesOnlyOneRoot(t *testing.T) {
	d, url := historyDB(t)
	other, err := Open(t.Context(), url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, database := range []*DB{d, other} {
		go func(i int, database *DB) {
			<-start
			machine, segment := "former-device", "former-segment"
			if i == 1 {
				machine, segment = "current-device", "current-segment"
			}
			results <- database.AdoptConversationAliases(t.Context(), machine, "same-chat", segment, "claude", "history", []string{"former-device", "current-device"})
		}(i, database)
	}
	close(start)
	passed, stale := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			passed++
		} else if errors.Is(err, ErrConversationEpoch) || errors.Is(err, ErrConversationSwitchBusy) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if passed != 1 || stale != 1 {
		t.Fatalf("adoption race: passed=%d stale=%d", passed, stale)
	}
	var roots int
	if err := d.pool.QueryRow(t.Context(), `SELECT count(*) FROM logical_conversations WHERE session_id='same-chat'`).Scan(&roots); err != nil || roots != 1 {
		t.Fatalf("roots=%d err=%v", roots, err)
	}
}

func TestAliasDeletionPreventsAdoptionUnderAnotherDeviceName(t *testing.T) {
	d, _ := historyDB(t)
	if err := d.PutConversationDeletion(t.Context(), "former-device", "deleted-chat", json.RawMessage(`{"session_id":"deleted-chat"}`)); err != nil {
		t.Fatal(err)
	}
	err := d.AdoptConversationAliases(t.Context(), "current-device", "deleted-chat", "first", "claude", "history", []string{"former-device"})
	if !errors.Is(err, ErrConversationDeleted) {
		t.Fatalf("adopted deleted alias: %v", err)
	}
}

func TestAcceptedInputKeepsOnlyDigestAndCannotReplayAfterPayloadCleanup(t *testing.T) {
	d, _ := historyDB(t)
	ctx := t.Context()
	op := prepareLogicalSwitch(t, d)
	payload := json.RawMessage(`{"text":"private input","params":{"supervision_instructions":"private role"}}`)
	if err := d.EnqueueConversationInput(ctx, "root-machine", "root-chat", "receipt", 1, payload); err != nil {
		t.Fatal(err)
	}
	if err := d.PutConversationHandoff(ctx, "root-machine", "root-chat", op.OperationID, json.RawMessage(`{"text":"private handoff"}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
		t.Fatal(err)
	}
	if row, err := d.ClaimConversationInput(ctx, "root-machine", "root-chat", 2); err != nil || row == nil {
		t.Fatalf("claim: %+v %v", row, err)
	}
	var before []byte
	if err := d.pool.QueryRow(ctx, `SELECT handoff FROM conversation_switches WHERE operation_id=$1`, op.OperationID).Scan(&before); err != nil || len(before) == 0 {
		t.Fatalf("fixture has no handoff to clean: %q %v", before, err)
	}
	if err := d.ResolveConversationDelivery(ctx, "root-machine", "root-chat", "receipt", 2, op.OperationID, "accepted"); err != nil {
		t.Fatal(err)
	}
	var raw, hash []byte
	if err := d.pool.QueryRow(ctx, `SELECT payload,payload_hash FROM conversation_inputs WHERE client_msg_id='receipt'`).Scan(&raw, &hash); err != nil || string(raw) != "{}" || len(hash) != 32 {
		t.Fatalf("content retained: %q hash=%d %v", raw, len(hash), err)
	}
	var handoff []byte
	if err := d.pool.QueryRow(ctx, `SELECT handoff FROM conversation_switches WHERE operation_id=$1`, op.OperationID).Scan(&handoff); err != nil || len(handoff) != 0 {
		t.Fatalf("handoff retained: %q %v", handoff, err)
	}
	if state, err := d.ConversationInputReceipt(ctx, "root-machine", "root-chat", "receipt", payload); err != nil || state != "accepted" {
		t.Fatalf("receipt: %s %v", state, err)
	}
	if err := d.EnqueueConversationInput(ctx, "root-machine", "root-chat", "receipt", 2, payload); err != nil {
		t.Fatalf("same input after cleanup: %v", err)
	}
	if _, err := d.ConversationInputReceipt(ctx, "root-machine", "root-chat", "receipt", json.RawMessage(`{"text":"different"}`)); err == nil {
		t.Fatal("reused identity changed content")
	}
}
