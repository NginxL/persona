package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func queueRenewAcceptanceFixture(t *testing.T) (State, Scope, time.Time, string, Job) {
	t.Helper()
	state, scope := NewState(), testScope()
	now := time.Date(2026, 11, 21, 1, 0, 0, 0, time.UTC)
	key := "queue/jobs"
	enqueue := Enqueue{RequestID: NewRequestID(now), Key: key, Dedup: "fixture-event", Data: encoded("payload"), NotBefore: now.UnixMilli()}
	value, _, err := call(t, &state, scope, "storage.queue.enqueue", enqueue, now)
	if err != nil {
		t.Fatal("enqueue renew fixture:", err)
	}
	id := value.(map[string]string)["id"]
	claim := Claim{RequestID: NewRequestID(now), Key: key, LeaseSeconds: 30}
	if _, _, err := call(t, &state, scope, "storage.queue.claim", claim, now); err != nil {
		t.Fatal("claim renew fixture:", err)
	}
	job := state.Queues[key][id]
	if job == nil || job.State != "leased" || job.Run != scope.Run || job.Lease == "" || job.Attempts != 1 {
		t.Fatal("renew fixture has no current-run lease", job)
	}
	return state, scope, now, id, *job
}

func TestQueueRenewMaximumSuccessAndExactReceiptReplay(t *testing.T) {
	state, scope, now, id, claimed := queueRenewAcceptanceFixture(t)
	renewAt := now.Add(2 * time.Second)
	in := Renew{
		RequestID: NewRequestID(renewAt), Key: "queue/jobs", ID: id, Lease: claimed.Lease,
		LeaseSeconds: int(LeaseMost / time.Second),
	}
	receipts := len(state.Receipts)
	first, changed, err := call(t, &state, scope, "storage.queue.renew", in, renewAt)
	if err != nil || !changed {
		t.Fatal("maximum-duration renew was not committed:", first, changed, err)
	}
	job := state.Queues[in.Key][id]
	if job == nil {
		t.Fatal("renew deleted its job")
	}
	if want := renewAt.Add(LeaseMost).UnixMilli(); job.LeaseUntil != want {
		t.Fatal("renew did not set the maximum lease from the renewal time:", job.LeaseUntil, "want", want)
	}
	otherwise := *job
	otherwise.LeaseUntil = claimed.LeaseUntil
	if !reflect.DeepEqual(otherwise, claimed) {
		t.Fatal("renew changed job state, attempts, payload, dedup, not_before, run or lease token:", otherwise, "want", claimed)
	}
	if len(state.Receipts) != receipts+1 {
		t.Fatal("renew did not commit exactly one replay receipt")
	}
	receipt, exists := state.Receipts[in.RequestID]
	if !exists || jsonValue(t, receipt.Result) != jsonValue(t, first) {
		t.Fatal("renew receipt does not contain its original successful result")
	}

	committed := jsonValue(t, state)
	replay, changed, err := call(t, &state, scope, "storage.queue.renew", in, renewAt.Add(time.Second))
	if err != nil || changed || jsonValue(t, replay) != jsonValue(t, first) {
		t.Fatal("exact lost-response replay did not return the original receipt:", replay, changed, err)
	}
	if jsonValue(t, state) != committed {
		t.Fatal("exact renew replay extended the lease or rewrote state a second time")
	}

	conflicting := in
	conflicting.LeaseSeconds--
	beforeConflict := jsonValue(t, state)
	if _, changed, err := call(t, &state, scope, "storage.queue.renew", conflicting, renewAt.Add(2*time.Second)); !errors.Is(err, ErrConflict) || changed {
		t.Fatal("same request ID accepted a changed renew payload:", changed, err)
	}
	if jsonValue(t, state) != beforeConflict {
		t.Fatal("changed-payload renew conflict mutated the job or its receipt")
	}
}

func TestQueueRenewRejectsInvalidDurationsWithoutMutation(t *testing.T) {
	for _, row := range []struct {
		name     string
		duration any
	}{
		{name: "zero", duration: 0},
		{name: "negative", duration: -1},
		{name: "fractional", duration: 1.5},
		{name: "excessive", duration: int(LeaseMost/time.Second) + 1},
	} {
		t.Run(row.name, func(t *testing.T) {
			state, scope, now, id, claimed := queueRenewAcceptanceFixture(t)
			probeAt := now.Add(time.Second)
			probe := Renew{RequestID: NewRequestID(probeAt), Key: "queue/jobs", ID: id, Lease: claimed.Lease, LeaseSeconds: int(LeaseMost / time.Second)}
			if _, changed, err := call(t, &state, scope, "storage.queue.renew", probe, probeAt); err != nil || !changed {
				t.Fatal("valid renew prerequisite:", changed, err)
			}
			at := now.Add(2 * time.Second)
			in := map[string]any{
				"request_id": NewRequestID(at), "key": "queue/jobs", "id": id,
				"lease": claimed.Lease, "lease_seconds": row.duration,
			}
			before := jsonValue(t, state)
			if _, changed, err := call(t, &state, scope, "storage.queue.renew", in, at); !errors.Is(err, ErrInvalid) || changed {
				t.Fatal("invalid lease_seconds was not rejected as invalid without a commit:", changed, err)
			}
			if jsonValue(t, state) != before {
				t.Fatal("invalid lease_seconds mutated the job or created a receipt")
			}
		})
	}
}

func TestQueueRenewRejectsStaleMissingAndRevokedRequestsWithoutMutation(t *testing.T) {
	for _, row := range []struct {
		name string
		want error
	}{
		{name: "expired", want: ErrStale},
		{name: "replaced run", want: ErrStale},
		{name: "wrong token", want: ErrStale},
		{name: "wrong key", want: ErrNotFound},
		{name: "deleted job", want: ErrNotFound},
		{name: "revoked queue grant", want: ErrDenied},
		{name: "revoked key grant", want: ErrDenied},
	} {
		t.Run(row.name, func(t *testing.T) {
			state, scope, now, id, claimed := queueRenewAcceptanceFixture(t)
			probeAt := now.Add(time.Second)
			probe := Renew{RequestID: NewRequestID(probeAt), Key: "queue/jobs", ID: id, Lease: claimed.Lease, LeaseSeconds: int(LeaseMost / time.Second)}
			if _, changed, err := call(t, &state, scope, "storage.queue.renew", probe, probeAt); err != nil || !changed {
				t.Fatal("valid renew prerequisite:", changed, err)
			}
			at := now.Add(2 * time.Second)
			in := Renew{RequestID: NewRequestID(at), Key: "queue/jobs", ID: id, Lease: claimed.Lease, LeaseSeconds: 5}
			switch row.name {
			case "expired":
				at = time.UnixMilli(state.Queues[in.Key][id].LeaseUntil)
				in.RequestID = NewRequestID(at)
			case "replaced run":
				scope.Run = "replacement-runtime"
				state.ReplaceRun(scope.Run)
			case "wrong token":
				in.Lease = "forged-lease-token"
			case "wrong key":
				in.Key = "queue/other"
			case "deleted job":
				settle := Settle{RequestID: NewRequestID(at), Key: in.Key, ID: id, Lease: claimed.Lease}
				if _, _, err := call(t, &state, scope, "storage.queue.ack", settle, at); err != nil {
					t.Fatal("ack deleted-job fixture:", err)
				}
				settle.RequestID, settle.Lease = NewRequestID(at), ""
				if _, _, err := call(t, &state, scope, "storage.queue.forget", settle, at); err != nil {
					t.Fatal("forget deleted-job fixture:", err)
				}
			case "revoked queue grant":
				scope.Granted.Queues = false
			case "revoked key grant":
				scope.Granted.Patterns = []string{"state/*"}
			}
			before := jsonValue(t, state)
			if _, changed, err := call(t, &state, scope, "storage.queue.renew", in, at); !errors.Is(err, row.want) || changed {
				t.Fatalf("renew error = %v, changed = %v; want %v without a commit", err, changed, row.want)
			}
			if jsonValue(t, state) != before {
				t.Fatal("rejected renew mutated durable state or granted new authority")
			}
		})
	}
}

func TestQueueRenewReceiptDoesNotReviveExpiredDeletedOrReplacedLease(t *testing.T) {
	for _, scenario := range []string{"expired", "deleted", "replaced run"} {
		t.Run(scenario, func(t *testing.T) {
			state, scope, now, id, claimed := queueRenewAcceptanceFixture(t)
			at := now.Add(time.Second)
			in := Renew{RequestID: NewRequestID(at), Key: "queue/jobs", ID: id, Lease: claimed.Lease, LeaseSeconds: int(LeaseMost / time.Second)}
			if _, changed, err := call(t, &state, scope, "storage.queue.renew", in, at); err != nil || !changed {
				t.Fatal("commit renew before stale replay:", changed, err)
			}
			replayAt := at.Add(time.Second)
			switch scenario {
			case "expired":
				replayAt = time.UnixMilli(state.Queues[in.Key][id].LeaseUntil)
			case "deleted":
				settle := Settle{RequestID: NewRequestID(replayAt), Key: in.Key, ID: id, Lease: claimed.Lease}
				if _, _, err := call(t, &state, scope, "storage.queue.ack", settle, replayAt); err != nil {
					t.Fatal("ack stale-replay fixture:", err)
				}
				settle.RequestID, settle.Lease = NewRequestID(replayAt), ""
				if _, _, err := call(t, &state, scope, "storage.queue.forget", settle, replayAt); err != nil {
					t.Fatal("forget stale-replay fixture:", err)
				}
			case "replaced run":
				scope.Run = "replacement-runtime"
				state.ReplaceRun(scope.Run)
			}
			before := jsonValue(t, state)
			if _, changed, err := call(t, &state, scope, "storage.queue.renew", in, replayAt); !errors.Is(err, ErrStale) || changed {
				t.Fatal("stale renew receipt returned fresh success:", changed, err)
			}
			if jsonValue(t, state) != before {
				t.Fatal("stale renew receipt replay mutated state")
			}
		})
	}
}

func TestQueueRenewReceiptCannotBypassCurrentGrantWithdrawal(t *testing.T) {
	for _, withdrawal := range []string{"queue flag", "key pattern"} {
		t.Run(withdrawal, func(t *testing.T) {
			state, scope, now, id, claimed := queueRenewAcceptanceFixture(t)
			at := now.Add(time.Second)
			in := Renew{RequestID: NewRequestID(at), Key: "queue/jobs", ID: id, Lease: claimed.Lease, LeaseSeconds: 5}
			if _, changed, err := call(t, &state, scope, "storage.queue.renew", in, at); err != nil || !changed {
				t.Fatal("commit renew before grant withdrawal:", changed, err)
			}
			if withdrawal == "queue flag" {
				scope.Granted.Queues = false
			} else {
				scope.Granted.Patterns = []string{"state/*"}
			}
			before := jsonValue(t, state)
			if _, changed, err := call(t, &state, scope, "storage.queue.renew", in, at.Add(time.Second)); !errors.Is(err, ErrDenied) || changed {
				t.Fatal("renew receipt bypassed current grant withdrawal:", changed, err)
			}
			if jsonValue(t, state) != before {
				t.Fatal("denied renew replay mutated state")
			}
		})
	}
}
