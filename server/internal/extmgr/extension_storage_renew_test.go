package extmgr

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extmgr/storage"
	"github.com/nwverse/persona/server/internal/rpc"
)

func extmgrQueueRenewAcceptanceJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func extmgrQueueRenewAcceptanceExpectCode(t *testing.T, err error, want string) {
	t.Helper()
	var public *rpc.Error
	if !errors.As(err, &public) {
		t.Fatalf("want RPC code %s, got %v", want, err)
	}
	if public.Code != want {
		t.Fatalf("want RPC code %s, got %s (%v)", want, public.Code, err)
	}
}

func extmgrQueueRenewAcceptanceOnlyJob(t *testing.T, processMethod func(string, any, any) error, key string) wire.StorageJob {
	t.Helper()
	var page wire.StorageQueueListResult
	if err := processMethod("storage.queue.list", wire.StorageQueueListParams{Key: key}, &page); err != nil {
		t.Fatal("list queue renew fixture:", err)
	}
	if len(page.Items) != 1 {
		t.Fatal("queue renew fixture does not contain exactly one job", page.Items)
	}
	return page.Items[0]
}

func TestStorageSourceAdapterQueueRenewLostAckReplayAndRunFence(t *testing.T) {
	database := storagePostgresFixture(t)
	backend := &storageFaultStore{DB: database}
	program, adapter, facade := storageAdapterProgram(t)
	_, old := storageProcessManager(t, backend, program, adapter, facade)
	callOld := func(method string, params, out any) error {
		return storageProcessCall(t, old, method, params, out)
	}
	key := "queue/jobs"
	enqueue := wire.StorageEnqueueParams{
		RequestID: storageProcessID(t, old), Key: key, Dedup: "fixture-event",
		Data: base64.StdEncoding.EncodeToString([]byte("payload")),
	}
	var enqueued wire.StorageJobResult
	if err := callOld("storage.queue.enqueue", enqueue, &enqueued); err != nil || enqueued.ID == "" {
		t.Fatal("enqueue adapter renew fixture:", enqueued, err)
	}
	claim := wire.StorageClaimParams{RequestID: storageProcessID(t, old), Key: key, LeaseSeconds: 30}
	var claimed wire.StorageClaimResult
	if err := callOld("storage.queue.claim", claim, &claimed); err != nil || claimed.Job == nil {
		t.Fatal("claim adapter renew fixture:", claimed, err)
	}
	original := *claimed.Job
	in := wire.StorageRenewParams{
		RequestID: storageProcessID(t, old), Key: key, ID: original.ID, Lease: original.Lease,
		LeaseSeconds: int(storage.LeaseMost / time.Second),
	}

	// The wrapper reports a lost acknowledgment only after the real Postgres
	// commit. The identical request must recover that original receipt.
	backend.loseAck.Store(true)
	var lost map[string]any
	extmgrQueueRenewAcceptanceExpectCode(t, callOld("storage.queue.renew", in, &lost), "storage_unknown")
	var recovered map[string]any
	if err := callOld("storage.queue.renew", in, &recovered); err != nil {
		t.Fatal("recover exact renew after a real committed lost acknowledgment:", err)
	}
	firstResult := extmgrQueueRenewAcceptanceJSON(t, recovered)
	renewed := extmgrQueueRenewAcceptanceOnlyJob(t, callOld, key)
	if renewed.LeaseUntil <= original.LeaseUntil {
		t.Fatal("committed renew did not extend lease_until", renewed.LeaseUntil, original.LeaseUntil)
	}
	if renewed.ID != original.ID || renewed.Dedup != original.Dedup || renewed.Data != original.Data || renewed.State != original.State || renewed.NotBefore != original.NotBefore || renewed.Attempts != original.Attempts || renewed.Lease != original.Lease {
		t.Fatal("renew changed job identity, state, attempts, payload, dedup, not_before or lease token", renewed, original)
	}

	var replayed map[string]any
	if err := callOld("storage.queue.renew", in, &replayed); err != nil || extmgrQueueRenewAcceptanceJSON(t, replayed) != firstResult {
		t.Fatal("exact renew replay did not return the original successful receipt", replayed, err)
	}
	if after := extmgrQueueRenewAcceptanceOnlyJob(t, callOld, key); after != renewed {
		t.Fatal("exact renew replay extended the lease or changed the job a second time", after, renewed)
	}
	conflicting := in
	conflicting.LeaseSeconds--
	extmgrQueueRenewAcceptanceExpectCode(t, callOld("storage.queue.renew", conflicting, &replayed), "storage_conflict")
	if after := extmgrQueueRenewAcceptanceOnlyJob(t, callOld, key); after != renewed {
		t.Fatal("changed-payload renew conflict mutated the job", after, renewed)
	}

	// An independent pool and process prove persistence. Its trusted start
	// intentionally retires the old lease as unknown without changing its expiry.
	// Capture the old run's fresh request ID while its read-only request call is
	// still admitted; after replacement, even that request is retired.
	oldAttempt := in
	oldAttempt.RequestID = storageProcessID(t, old)
	other := storagePostgres(t)
	_, next := storageProcessManager(t, &storageFaultStore{DB: other}, program, adapter, facade)
	callNext := func(method string, params, out any) error {
		return storageProcessCall(t, next, method, params, out)
	}
	persisted := extmgrQueueRenewAcceptanceOnlyJob(t, callNext, key)
	if persisted.LeaseUntil != renewed.LeaseUntil || persisted.State != "unknown" || persisted.Attempts != original.Attempts || persisted.Data != original.Data || persisted.Dedup != original.Dedup || persisted.NotBefore != original.NotBefore || persisted.Lease != original.Lease {
		t.Fatal("fresh pool/process did not preserve the renewed lease and job metadata", persisted, renewed)
	}
	extmgrQueueRenewAcceptanceExpectCode(t, callOld("storage.queue.renew", oldAttempt, &replayed), "storage_retired")
	nextAttempt := in
	nextAttempt.RequestID = storageProcessID(t, next)
	extmgrQueueRenewAcceptanceExpectCode(t, callNext("storage.queue.renew", nextAttempt, &replayed), "storage_retired")
	if after := extmgrQueueRenewAcceptanceOnlyJob(t, callNext, key); after != persisted {
		t.Fatal("replaced-run renew mutated the retained unknown job", after, persisted)
	}
}
