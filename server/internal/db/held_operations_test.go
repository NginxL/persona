package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// openHeld is open with held_operations empty (open does not clear it).
func openHeld(t *testing.T) *DB {
	t.Helper()
	d := open(t)
	if _, err := d.pool.Exec(context.Background(), `DELETE FROM held_operations`); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestHeldOperationKeepsSenderAndCannotRetryAfterSignOut(t *testing.T) {
	d, ctx := openHeld(t), context.Background()
	call := json.RawMessage(`{"caller":{"kind":"device","credential_id":"dev_sender"},"params":{"text":"hello"}}`)
	op, _, err := d.HoldOperation(ctx, HeldOperation{Kind: HeldKindMessage, OpID: "hello",
		MachineID: "mac", SessionID: "s1", Call: call, Lang: "en"})
	if err != nil || op.CredentialID != "dev_sender" {
		t.Fatalf("held operation: %+v, %v", op, err)
	}
	rows, err := d.HeldOperations(ctx, HeldKindMessage)
	if err != nil || len(rows) != 1 || rows[0].CredentialID != "dev_sender" || rows[0].Call != nil {
		t.Fatalf("held operation summaries: %+v, %v", rows, err)
	}
	if ok, err := d.SettleHeldOperation(ctx, op.Seq, HeldWaiting, HeldFailed, HeldSignedOut, ""); err != nil || !ok {
		t.Fatalf("fail signed-out message: %t, %v", ok, err)
	}
	if ok, _, err := d.RetryHeldOperation(ctx, op.Seq, time.Now()); err != nil || ok {
		t.Fatalf("retry signed-out message: %t, %v", ok, err)
	}
	if ok, err := d.DeleteHeldOperation(ctx, op.Seq, HeldFailed); err != nil || !ok {
		t.Fatalf("delete signed-out message: %t, %v", ok, err)
	}
}

// A held operation is kept once per kind, machine, session and op id, waiting, its call as it came; the list comes
// back in the order they were held.
func TestHeldOperationsKeptOnce(t *testing.T) {
	d, ctx := openHeld(t), context.Background()
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	call := json.RawMessage(`{"machine_id":"mac","method":"session.send","params":{"session_id":"s1","client_msg_id":"m1","text":"hi"},"caller":{"kind":"device"}}`)
	op := HeldOperation{Kind: HeldKindMessage, OpID: "m1", MachineID: "mac", SessionID: "s1", Call: call, Lang: "en", SentAt: at}
	first, fresh, err := d.HoldOperation(ctx, op)
	if err != nil || !fresh {
		t.Fatalf("hold: %+v %v %v", first, fresh, err)
	}
	if first.Seq == 0 || first.State != HeldWaiting || first.Failure != "" || !first.SentAt.Equal(at) || !first.HeldAt.Equal(at) ||
		!first.TriedAt.IsZero() || first.Attempts != 0 || first.Lang != "en" {
		t.Fatalf("held %+v", first)
	}
	if !jsonEqual(t, string(first.Call), string(call)) {
		t.Fatalf("call %s, want %s", first.Call, call)
	}
	again, fresh, err := d.HoldOperation(ctx, op)
	if err != nil || fresh || again.Seq != first.Seq {
		t.Fatalf("again: %+v %v %v", again, fresh, err)
	}
	second, _, err := d.HoldOperation(ctx, HeldOperation{Kind: HeldKindMessage, OpID: "m1", MachineID: "mac", SessionID: "s2", Call: call})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := d.HeldOperations(ctx, HeldKindMessage)
	if err != nil || len(ops) != 2 || ops[0].Seq != first.Seq || ops[1].Seq != second.Seq || ops[1].Seq <= ops[0].Seq {
		t.Fatalf("list %+v %v", ops, err)
	}
	if other, err := d.HeldOperations(ctx, "start"); err != nil || len(other) != 0 {
		t.Fatalf("another kind: %+v %v", other, err)
	}
}

// A try is claimed only from waiting; its outcome moves it only from being sent; Retry waits again with a new day;
// Delete drops it (only from the states given, when any); a restart fails as unclear what was left being sent.
func TestHeldOperationStates(t *testing.T) {
	d, ctx := openHeld(t), context.Background()
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	hold := func(id string) int64 {
		t.Helper()
		op, _, err := d.HoldOperation(ctx, HeldOperation{Kind: HeldKindMessage, OpID: id, MachineID: "mac", SessionID: "s1",
			Call: json.RawMessage(`{}`), SentAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return op.Seq
	}
	get := func(seq int64) HeldOperation {
		t.Helper()
		ops, err := d.HeldOperations(ctx, HeldKindMessage)
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range ops {
			if op.Seq == seq {
				return op
			}
		}
		return HeldOperation{}
	}
	a, b, c := hold("a"), hold("b"), hold("c")

	tried := at.Add(time.Minute)
	if op, ok, err := d.ClaimHeldOperation(ctx, a, tried); !ok || err != nil || op.Attempts != 1 || !op.TriedAt.Equal(tried) ||
		op.State != HeldSending || string(op.Call) != "{}" {
		t.Fatalf("claim: %+v %v %v", op, ok, err)
	}
	if _, ok, _ := d.ClaimHeldOperation(ctx, a, tried); ok {
		t.Fatal("claimed twice")
	}
	if op := get(a); op.State != HeldSending || !op.TriedAt.Equal(tried) || op.Attempts != 1 {
		t.Fatalf("claimed %+v", op)
	}
	if ok, _ := d.SettleHeldOperation(ctx, b, HeldSending, HeldFailed, HeldRefused, "no"); ok {
		t.Fatal("settled one not being sent")
	}
	if ok, err := d.SettleHeldOperation(ctx, a, HeldSending, HeldFailed, HeldRefused, "a draft is in the way"); !ok || err != nil {
		t.Fatalf("settle: %v %v", ok, err)
	}
	if op := get(a); op.State != HeldFailed || op.Failure != HeldRefused || op.Error != "a draft is in the way" {
		t.Fatalf("settled %+v", op)
	}
	again := at.Add(time.Hour)
	if ok, _, err := d.RetryHeldOperation(ctx, a, again); !ok || err != nil {
		t.Fatalf("retry: %v %v", ok, err)
	}
	if op := get(a); op.State != HeldWaiting || op.Failure != "" || op.Error != "" || !op.HeldAt.Equal(again) || !op.SentAt.Equal(at) ||
		op.Attempts != 1 {
		t.Fatalf("retried %+v", op)
	}

	// b being sent (by another manager on the same database, in a switch-over): the owner's Retry and Delete leave it,
	// and so does a Retry of one that waits.
	if _, ok, _ := d.ClaimHeldOperation(ctx, b, tried); !ok {
		t.Fatal("claim b")
	}
	if ok, _, err := d.RetryHeldOperation(ctx, b, again); ok || err != nil {
		t.Fatalf("retried one being sent: %v %v", ok, err)
	}
	if ok, err := d.DeleteHeldOperation(ctx, b); ok || err != nil {
		t.Fatalf("deleted one being sent: %v %v", ok, err)
	}
	if op := get(b); op.State != HeldSending {
		t.Fatalf("b %+v", op)
	}
	if ok, _, _ := d.RetryHeldOperation(ctx, c, again); ok {
		t.Fatal("retried a waiting one")
	}
	if op, found, err := d.HeldOperationOf(ctx, HeldKindMessage, "mac", "s1", "b"); !found || err != nil || op.Seq != b || op.State != HeldSending {
		t.Fatalf("b by its key: %+v %v %v", op, found, err)
	}
	if _, found, err := d.HeldOperationOf(ctx, HeldKindMessage, "mac", "s2", "b"); found || err != nil {
		t.Fatalf("another session's b: %v %v", found, err)
	}
	// A repair or an owner's change over b's try touches only that try (its attempts), never a newer claim.
	if op := get(b); op.Attempts != 1 {
		t.Fatalf("b's attempts %d", op.Attempts)
	}
	if ok, err := d.SettleHeldTry(ctx, b, 2, HeldFailed, HeldUnclear, ""); ok || err != nil {
		t.Fatalf("settled another try: %v %v", ok, err)
	}
	if ok, err := d.DeleteHeldTry(ctx, b, 2); ok || err != nil {
		t.Fatalf("deleted another try: %v %v", ok, err)
	}
	if ok, err := d.SettleHeldTry(ctx, c, 0, HeldFailed, HeldUnclear, ""); ok || err != nil {
		t.Fatalf("settled a waiting one: %v %v", ok, err)
	}
	if op := get(b); op.State != HeldSending {
		t.Fatalf("b %+v", op)
	}
	// b being sent when the manager stopped: unclear at the next start; c waiting stays.
	if n, err := d.RecoverHeldOperations(ctx, HeldKindMessage); n != 1 || err != nil {
		t.Fatalf("recover: %d %v", n, err)
	}
	if op := get(b); op.State != HeldFailed || op.Failure != HeldUnclear {
		t.Fatalf("recovered %+v", op)
	}
	if op := get(c); op.State != HeldWaiting {
		t.Fatalf("c %+v", op)
	}

	renewed := at.Add(2 * time.Hour)
	if ok, err := d.RenewHeldOperation(ctx, c, renewed); !ok || err != nil {
		t.Fatalf("renew: %v %v", ok, err)
	}
	if op := get(c); op.State != HeldWaiting || !op.HeldAt.Equal(renewed) || !op.SentAt.Equal(at) {
		t.Fatalf("renewed %+v", op)
	}
	if ok, _ := d.RenewHeldOperation(ctx, b, renewed); ok {
		t.Fatal("renewed one that is not waiting")
	}
	if ok, _ := d.DeleteHeldOperation(ctx, c, HeldSending); ok {
		t.Fatal("deleted a waiting one as sent")
	}
	if ok, err := d.DeleteHeldOperation(ctx, c); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, _ := d.DeleteHeldOperation(ctx, c); ok {
		t.Fatal("deleted twice")
	}
	if ok, _, _ := d.RetryHeldOperation(ctx, c, again); ok {
		t.Fatal("retried a deleted one")
	}
	if _, ok, _ := d.ClaimHeldOperation(ctx, a, tried); !ok {
		t.Fatal("claim a again")
	}
	if ok, err := d.SettleHeldTry(ctx, a, get(a).Attempts, HeldFailed, HeldUnclear, ""); !ok || err != nil {
		t.Fatalf("settle that try: %v %v", ok, err)
	}
	if op := get(a); op.State != HeldFailed || op.Failure != HeldUnclear {
		t.Fatalf("a settled %+v", op)
	}
	if ok, _, _ := d.RetryHeldOperation(ctx, a, again); !ok {
		t.Fatal("retry a")
	}
	if _, ok, _ := d.ClaimHeldOperation(ctx, a, tried); !ok {
		t.Fatal("claim a once more")
	}
	if ok, err := d.DeleteHeldTry(ctx, a, get(a).Attempts-1); ok || err != nil {
		t.Fatalf("deleted with an older try: %v %v", ok, err)
	}
	if ok, err := d.DeleteHeldTry(ctx, a, get(a).Attempts); !ok || err != nil {
		t.Fatalf("delete that try: %v %v", ok, err)
	}
	if ops, _ := d.HeldOperations(ctx, HeldKindMessage); len(ops) != 1 || ops[0].Seq != b {
		t.Fatalf("left %+v", ops)
	}
	if ok, err := d.DeleteHeldOperation(ctx, b, HeldFailed); !ok || err != nil { // b was found to have arrived
		t.Fatalf("delete b from failed: %v %v", ok, err)
	}
	if ops, _ := d.HeldOperations(ctx, HeldKindMessage); len(ops) != 0 {
		t.Fatalf("left %+v", ops)
	}
}

// A held message's row shows its words and how many pictures it has without its call being read (HeldOperations
// leaves the call out); a Retry of one whose try may have arrived keeps that mark while it waits, a refused one's
// does not; a hold the manager could not confirm is dropped only while it waits untried.
func TestHeldOperationsShownRetriedAndUnsure(t *testing.T) {
	d, ctx := openHeld(t), context.Background()
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	hold := func(id string, call string) int64 {
		t.Helper()
		op, _, err := d.HoldOperation(ctx, HeldOperation{Kind: HeldKindMessage, OpID: id, MachineID: "mac", SessionID: "s1",
			Call: json.RawMessage(call), SentAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return op.Seq
	}
	a := hold("a", `{"params":{"text":"hello","images":[{"data":"x"},{"data":"y"}]}}`)
	b := hold("b", `{"params":{"images":"not a list"}}`)
	ops, err := d.HeldOperations(ctx, HeldKindMessage)
	if err != nil || len(ops) != 2 {
		t.Fatalf("list %+v %v", ops, err)
	}
	if ops[0].Text != "hello" || ops[0].Images != 2 || ops[0].Call != nil || ops[1].Text != "" || ops[1].Images != 0 {
		t.Fatalf("shown %+v", ops)
	}
	if op, found, err := d.HeldOperationOf(ctx, HeldKindMessage, "mac", "s1", "a"); !found || err != nil || op.Call == nil || op.Images != 2 {
		t.Fatalf("by key %+v %v %v", op, found, err)
	}
	u := hold("u", `{}`)
	for seq, failure := range map[int64]string{a: HeldUnclear, b: HeldRefused, u: HeldUnconfirmed} {
		if _, ok, _ := d.ClaimHeldOperation(ctx, seq, at); !ok {
			t.Fatal("claim")
		}
		if ok, _ := d.SettleHeldOperation(ctx, seq, HeldSending, HeldFailed, failure, ""); !ok {
			t.Fatal("settle")
		}
	}
	if ok, check, err := d.RetryHeldOperation(ctx, a, at); !ok || !check || err != nil {
		t.Fatalf("retry unclear: %v %v %v", ok, check, err)
	}
	if ok, check, err := d.RetryHeldOperation(ctx, b, at); !ok || check || err != nil {
		t.Fatalf("retry refused: %v %v %v", ok, check, err)
	}
	// Left to the owner after it was looked for: a Retry sends it anyway, no mark.
	if ok, check, err := d.RetryHeldOperation(ctx, u, at); !ok || check || err != nil {
		t.Fatalf("retry unconfirmed: %v %v %v", ok, check, err)
	}
	if op, _, _ := d.HeldOperationOf(ctx, HeldKindMessage, "mac", "s1", "u"); op.State != HeldWaiting || op.Failure != "" {
		t.Fatalf("u retried %+v", op)
	}
	ops, _ = d.HeldOperations(ctx, HeldKindMessage)
	if ops[0].State != HeldWaiting || ops[0].Failure != HeldUnclear || ops[1].State != HeldWaiting || ops[1].Failure != "" {
		t.Fatalf("retried %+v", ops)
	}
	// Unsure holds: c waits untried (dropped); a waits but was tried (kept); none for another session.
	hold("c", `{}`)
	if ok, err := d.DeleteUnsureHeld(ctx, HeldKindMessage, "mac", "s1", "a"); ok || err != nil {
		t.Fatalf("dropped a tried one: %v %v", ok, err)
	}
	if ok, err := d.DeleteUnsureHeld(ctx, HeldKindMessage, "mac", "s2", "c"); ok || err != nil {
		t.Fatalf("dropped another session's: %v %v", ok, err)
	}
	if ok, err := d.DeleteUnsureHeld(ctx, HeldKindMessage, "mac", "s1", "c"); !ok || err != nil {
		t.Fatalf("unsure c: %v %v", ok, err)
	}
	if ops, _ := d.HeldOperations(ctx, HeldKindMessage); len(ops) != 3 {
		t.Fatalf("left %+v", ops)
	}
}
