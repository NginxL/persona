package db

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extmgr/storage"
)

func dbQueueRenewAcceptanceInitial(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(storage.NewState())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Drive the existing detached business-data update boundary with the real
// storage engine. The database still owns row locking, atomic persistence and
// the independent-connection visibility asserted below.
func dbQueueRenewAcceptanceCall(t *testing.T, database *DB, scope extension.StorageScope, initial json.RawMessage, method string, params any, now time.Time) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return database.ExtensionStorage(t.Context(), scope, initial, func(data json.RawMessage, _ string) (json.RawMessage, any, bool, error) {
		var state storage.State
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, nil, false, err
		}
		value, changed, err := storage.Execute(&state, scope, method, raw, now)
		if err != nil || !changed {
			return data, value, false, err
		}
		next, err := json.Marshal(state)
		return next, value, true, err
	})
}

func dbQueueRenewAcceptanceJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func dbQueueRenewAcceptanceDecode(t *testing.T, value any, out any) {
	t.Helper()
	raw := dbQueueRenewAcceptanceJSON(t, value)
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

func dbQueueRenewAcceptanceState(t *testing.T, database *DB, plugin string) (storage.State, json.RawMessage, int64) {
	t.Helper()
	var raw json.RawMessage
	var revision int64
	if err := database.pool.QueryRow(t.Context(), `SELECT state,revision FROM extension_storage_spaces WHERE plugin=$1 AND NOT retained`, plugin).Scan(&raw, &revision); err != nil {
		t.Fatal(err)
	}
	var state storage.State
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	return state, raw, revision
}

func TestExtensionStorageQueueRenewDurabilityAndExactReplay(t *testing.T) {
	database := open(t)
	row, scope := storageFixture(t, database, "fixture-queue-renew-db")
	cleanupStorageFixture(t, database, row.ID)
	initial := dbQueueRenewAcceptanceInitial(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	key := "queue/jobs"
	enqueue := storage.Enqueue{RequestID: storage.NewRequestID(now), Key: key, Dedup: "fixture-event", Data: base64.StdEncoding.EncodeToString([]byte("payload")), NotBefore: now.UnixMilli()}
	value, err := dbQueueRenewAcceptanceCall(t, database, scope, initial, "storage.queue.enqueue", enqueue, now)
	if err != nil {
		t.Fatal("enqueue durable renew fixture:", err)
	}
	var enqueued struct {
		ID string `json:"id"`
	}
	dbQueueRenewAcceptanceDecode(t, value, &enqueued)
	if enqueued.ID == "" {
		t.Fatal("enqueue returned no job ID")
	}
	scope.Start = false
	claim := storage.Claim{RequestID: storage.NewRequestID(now), Key: key, LeaseSeconds: 30}
	value, err = dbQueueRenewAcceptanceCall(t, database, scope, initial, "storage.queue.claim", claim, now)
	if err != nil {
		t.Fatal("claim durable renew fixture:", err)
	}
	var claimed struct {
		Job *storage.JobView `json:"job"`
	}
	dbQueueRenewAcceptanceDecode(t, value, &claimed)
	if claimed.Job == nil || claimed.Job.Lease == "" {
		t.Fatal("claim returned no lease")
	}
	beforeState, _, _ := dbQueueRenewAcceptanceState(t, database, row.ID)
	beforeJob := *beforeState.Queues[key][enqueued.ID]
	receiptCount := len(beforeState.Receipts)

	renewAt := now.Add(2 * time.Second)
	in := storage.Renew{
		RequestID: storage.NewRequestID(renewAt), Key: key, ID: enqueued.ID, Lease: claimed.Job.Lease,
		LeaseSeconds: int(storage.LeaseMost / time.Second),
	}
	first, err := dbQueueRenewAcceptanceCall(t, database, scope, initial, "storage.queue.renew", in, renewAt)
	if err != nil {
		t.Fatal("commit maximum-duration queue renew:", err)
	}
	firstResult := dbQueueRenewAcceptanceJSON(t, first)

	// A separate pool must observe both the extended lease and its receipt.
	other, err := Open(context.Background(), os.Getenv("PERSONA_TEST_DATABASE_URL"), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	value, err = dbQueueRenewAcceptanceCall(t, other, scope, initial, "storage.queue.list", storage.QueueList{Key: key}, renewAt)
	if err != nil {
		t.Fatal("read renewed lease through a fresh pool:", err)
	}
	var page struct {
		Items []storage.JobView `json:"items"`
	}
	dbQueueRenewAcceptanceDecode(t, value, &page)
	if len(page.Items) != 1 || page.Items[0].ID != enqueued.ID {
		t.Fatal("fresh pool could not read the renewed job", page.Items)
	}
	if want := renewAt.Add(storage.LeaseMost).UnixMilli(); page.Items[0].LeaseUntil != want {
		t.Fatal("fresh pool observed the wrong renewed lease:", page.Items[0].LeaseUntil, "want", want)
	}
	renewedState, _, _ := dbQueueRenewAcceptanceState(t, other, row.ID)
	renewedJob := *renewedState.Queues[key][enqueued.ID]
	otherwise := renewedJob
	otherwise.LeaseUntil = beforeJob.LeaseUntil
	if !reflect.DeepEqual(otherwise, beforeJob) {
		t.Fatal("durable renew changed state, attempts, payload, dedup, not_before, run or lease token:", otherwise, "want", beforeJob)
	}
	if len(renewedState.Receipts) != receiptCount+1 {
		t.Fatal("durable renew did not commit exactly one receipt")
	}
	receipt, exists := renewedState.Receipts[in.RequestID]
	if !exists || !jsonEqual(t, string(receipt.Result), string(firstResult)) {
		t.Fatal("persisted renew receipt does not hold the original successful result; exists", exists, "got", receipt.Result, "want", firstResult, "request", in.RequestID)
	}

	_, beforeReplay, beforeRevision := dbQueueRenewAcceptanceState(t, other, row.ID)
	replay, err := dbQueueRenewAcceptanceCall(t, other, scope, initial, "storage.queue.renew", in, renewAt.Add(time.Second))
	if err != nil {
		t.Fatal("replay exact renew after its lost response:", err)
	}
	if !bytes.Equal(dbQueueRenewAcceptanceJSON(t, replay), firstResult) {
		t.Fatal("exact renew replay returned a different result")
	}
	afterReplay, afterReplayRaw, afterRevision := dbQueueRenewAcceptanceState(t, other, row.ID)
	if afterRevision != beforeRevision || !bytes.Equal(afterReplayRaw, beforeReplay) {
		t.Fatal("exact renew replay wrote the document or extended the lease twice")
	}
	if afterReplay.Queues[key][enqueued.ID].LeaseUntil != renewedJob.LeaseUntil {
		t.Fatal("exact renew replay changed lease_until")
	}

	conflicting := in
	conflicting.LeaseSeconds--
	if _, err := dbQueueRenewAcceptanceCall(t, other, scope, initial, "storage.queue.renew", conflicting, renewAt.Add(2*time.Second)); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("changed renew payload did not conflict with its original receipt:", err)
	}
	_, afterConflict, conflictRevision := dbQueueRenewAcceptanceState(t, other, row.ID)
	if conflictRevision != beforeRevision || !bytes.Equal(afterConflict, beforeReplay) {
		t.Fatal("changed-payload renew conflict mutated the durable job or receipt")
	}
}
