package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/imessagewire"
)

type archiveFenceStore struct {
	*memStore
	failRead  atomic.Bool
	failWrite atomic.Bool
}

type committedErrorArchiveStore struct {
	*memStore
	failAfterWrite atomic.Bool
}

func (s *committedErrorArchiveStore) PutSetting(ctx context.Context, key string, v any) error {
	if err := s.memStore.PutSetting(ctx, key, v); err != nil {
		return err
	}
	if key == imessageArchiveReservationsKey && s.failAfterWrite.Swap(false) {
		return errors.New("synthetic lost write acknowledgement")
	}
	return nil
}

type imessageAIReadStore struct {
	*memStore
	fail atomic.Bool
}

func (s *imessageAIReadStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == imessageAIKey && s.fail.Load() {
		return false, errors.New("synthetic settings read failure")
	}
	return s.memStore.Setting(ctx, key, out)
}

type blockingArchiveFenceStore struct {
	*memStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingArchiveFenceStore) PutSetting(ctx context.Context, key string, v any) error {
	if key == imessageArchiveReservationsKey {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.memStore.PutSetting(ctx, key, v)
}

func (s *archiveFenceStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == imessageArchiveReservationsKey && s.failRead.Load() {
		return false, errors.New("synthetic read failure")
	}
	return s.memStore.Setting(ctx, key, out)
}

func (s *archiveFenceStore) PutSetting(ctx context.Context, key string, v any) error {
	if key == imessageArchiveReservationsKey && s.failWrite.Load() {
		return errors.New("synthetic write failure")
	}
	return s.memStore.PutSetting(ctx, key, v)
}

func TestIMessageArchiveFenceRecoversReservations(t *testing.T) {
	needsIMessageModule(t)
	ctx := context.Background()
	store := newMemStore()
	var before imessageArchiveFence
	before.init(true, store)
	gen, err := before.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 32)
	if err := before.begin(ctx, gen, token); err != nil {
		t.Fatal(err)
	}
	// Model a restarted source manager while the former writer has not ended.
	var after imessageArchiveFence
	after.init(true, store)
	newGen, err := after.snapshot(ctx)
	if err != nil || newGen == gen {
		t.Fatalf("restart generation: %q, %v", newGen, err)
	}
	if err := after.begin(ctx, gen, strings.Repeat("b", 32)); !errors.Is(err, errIMessageArchiveRetired) {
		t.Fatalf("old generation admitted: %v", err)
	}
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	err = after.disable(deadline)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("disable should wait for the recovered reservation: %v", err)
	}
	if err := after.end(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := after.disable(ctx); err != nil {
		t.Fatalf("disable after the writer ended: %v", err)
	}
	if err := after.end(ctx, token); err != nil {
		t.Fatalf("ending twice must be harmless: %v", err)
	}
}

func TestIMessageArchiveFenceAbortAfterUncertainPersistence(t *testing.T) {
	needsIMessageModule(t)
	ctx := context.Background()
	store := &committedErrorArchiveStore{memStore: newMemStore()}
	var first imessageArchiveFence
	first.init(true, store)
	gen, err := first.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 32)
	other := strings.Repeat("b", 32)
	if err := first.begin(ctx, gen, other); err != nil {
		t.Fatal(err)
	}
	store.failAfterWrite.Store(true)
	if err := first.begin(ctx, gen, token); err == nil {
		t.Fatal("the synthetic lost acknowledgement was not exercised")
	}
	if err := first.abort(ctx, gen, token); err != nil {
		t.Fatalf("abort uncertain reservation: %v", err)
	}
	wait, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	err = first.disable(wait)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("another active reservation did not remain protected: %v", err)
	}
	if err := first.end(ctx, other); err != nil {
		t.Fatal(err)
	}
	var restarted imessageArchiveFence
	restarted.init(true, store)
	deadline, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := restarted.disable(deadline); err != nil {
		t.Fatalf("an acknowledged abort left a durable reservation behind: %v", err)
	}
}

func TestIMessageArchiveFenceEndAfterUncertainPersistence(t *testing.T) {
	needsIMessageModule(t)
	ctx := context.Background()
	store := &committedErrorArchiveStore{memStore: newMemStore()}
	var f imessageArchiveFence
	f.init(true, store)
	gen, err := f.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("c", 32)
	if err := f.begin(ctx, gen, token); err != nil {
		t.Fatal(err)
	}
	store.failAfterWrite.Store(true)
	if err := f.end(ctx, token); err == nil {
		t.Fatal("the synthetic lost release acknowledgement was not exercised")
	}
	deadline, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := f.disable(deadline); err != nil {
		t.Fatalf("the completed release was not reloaded: %v", err)
	}
}

func TestIMessageArchiveFenceMissingRecordFailsClosed(t *testing.T) {
	needsIMessageModule(t)
	ctx := context.Background()
	store := &archiveFenceStore{memStore: newMemStore()}
	var f imessageArchiveFence
	f.init(true, store)
	gen, err := f.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("d", 32)
	if err := f.begin(ctx, gen, token); err != nil {
		t.Fatal(err)
	}
	store.failWrite.Store(true)
	if err := f.end(ctx, token); err == nil {
		t.Fatal("the synthetic release failure was not exercised")
	}
	store.mu.Lock()
	delete(store.settings, imessageArchiveReservationsKey)
	store.mu.Unlock()
	store.failWrite.Store(false)
	if err := f.disable(ctx); err == nil {
		t.Fatal("a missing reservation record was treated as no active writer")
	}
	if err := store.PutSetting(ctx, imessageArchiveReservationsKey, imessageArchiveReservations{Version: 1, Tokens: []string{token}}); err != nil {
		t.Fatal(err)
	}
	if err := f.end(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := f.disable(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIMessageSettingReadFailureFailsClosedAndRecovers(t *testing.T) {
	needsIMessageModule(t)
	for _, saved := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved off", true: "saved on"}[saved], func(t *testing.T) {
			ctx := context.Background()
			store := &imessageAIReadStore{memStore: newMemStore()}
			if err := store.PutSetting(ctx, imessageAIKey, saved); err != nil {
				t.Fatal(err)
			}
			store.fail.Store(true)
			h := newTestManager(t)
			h.store = store
			h.connectors = connector.NewRegistry(nil)
			if err := h.addIMessage(imessagewire.Config{Machine: "example"}); err != nil {
				t.Fatal(err)
			}
			if h.imessage().AI() || !h.imessageAIPending.Load() {
				t.Fatal("unreadable saved choice enabled iMessage")
			}
			_ = h.kbArchives(ctx)
			if h.imessage().AI() || !h.imessageAIPending.Load() {
				t.Fatal("a failed retry enabled iMessage")
			}
			store.fail.Store(false)
			if saved {
				_ = h.readConnectors(ctx) // recovery also runs when the knowledge archive is off
			} else {
				_ = h.kbArchives(ctx)
			}
			if h.imessage().AI() != saved || h.imessageAIPending.Load() {
				t.Fatalf("did not restore saved choice %t", saved)
			}
			_, err := h.imessageArchive.snapshot(ctx)
			if saved && err != nil || !saved && !errors.Is(err, errIMessageArchiveRetired) {
				t.Fatalf("archive fence after restoring %t: %v", saved, err)
			}
		})
	}
}

func TestIMessageSettingRefreshRecoversSavedChoice(t *testing.T) {
	needsIMessageModule(t)
	ctx := context.Background()
	store := &imessageAIReadStore{memStore: newMemStore()}
	if err := store.PutSetting(ctx, imessageAIKey, true); err != nil {
		t.Fatal(err)
	}
	store.fail.Store(true)
	h := newTestManager(t)
	h.store = store
	h.connectors = connector.NewRegistry(nil)
	if err := h.addIMessage(imessagewire.Config{Machine: "example"}); err != nil {
		t.Fatal(err)
	}
	store.fail.Store(false)
	code, out := call(t, h.routes(), "GET", "/api/v1/imessage", h.cfg.Token, "")
	if code != 200 || out["ai"] != true || h.imessageAIPending.Load() {
		t.Fatalf("refresh did not recover the saved choice: %d %v", code, out)
	}
}

type isolatedArchiveFenceStore struct {
	Store
	key string
}

func (s isolatedArchiveFenceStore) Setting(ctx context.Context, _ string, out any) (bool, error) {
	return s.Store.Setting(ctx, s.key, out)
}

func (s isolatedArchiveFenceStore) PutSetting(ctx context.Context, _ string, v any) error {
	return s.Store.PutSetting(ctx, s.key, v)
}

func TestIMessageArchiveFencePostgresRestart(t *testing.T) {
	needsIMessageModule(t)
	address := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL is required for the disposable Postgres fixture")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, address, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	key := imessageArchiveReservationsKey + ".test." + randomHex(8)
	t.Cleanup(func() {
		var value json.RawMessage
		if _, err := database.TakeSetting(context.Background(), key, &value); err != nil {
			t.Error(err)
		}
		database.Close()
	})
	store := isolatedArchiveFenceStore{Store: database, key: key}
	var first imessageArchiveFence
	first.init(true, store)
	gen, err := first.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("c", 32)
	if err := first.begin(ctx, gen, token); err != nil {
		t.Fatal(err)
	}
	var restarted imessageArchiveFence
	restarted.init(false, store)
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	err = restarted.disable(deadline)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("persisted reservation did not block disable: %v", err)
	}
	if err := restarted.end(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := restarted.disable(ctx); err != nil {
		t.Fatalf("release did not survive restart: %v", err)
	}
}

func TestIMessageArchiveFenceStorageFailureAndCancellation(t *testing.T) {
	needsIMessageModule(t)
	ctx := context.Background()
	store := &archiveFenceStore{memStore: newMemStore()}
	store.failRead.Store(true)
	var f imessageArchiveFence
	f.init(true, store)
	if _, err := f.snapshot(ctx); err == nil {
		t.Fatal("unreadable reservations admitted a read")
	}
	if err := f.disable(ctx); err == nil {
		t.Fatal("unreadable reservations allowed a successful disable")
	}
	store.failRead.Store(false)
	if err := f.enable(ctx); err != nil {
		t.Fatal(err)
	}
	gen, err := f.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 32)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := f.begin(cancelled, gen, token); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled begin: %v", err)
	}
	store.failWrite.Store(true)
	if err := f.begin(ctx, gen, token); err == nil {
		t.Fatal("failed reservation write admitted a commit")
	}
	store.failWrite.Store(false)
	if err := f.begin(ctx, gen, token); err != nil {
		t.Fatal(err)
	}
	store.failWrite.Store(true)
	if err := f.end(ctx, token); err == nil {
		t.Fatal("failed reservation release was acknowledged")
	}
	deadline, done := context.WithTimeout(ctx, 100*time.Millisecond)
	err = f.disable(deadline)
	done()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("disable ignored an uncertain commit: %v", err)
	}
	store.failWrite.Store(false)
	if err := f.end(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := f.disable(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIMessageArchiveFenceDeadlineWhileReservationStoreIsBusy(t *testing.T) {
	needsIMessageModule(t)
	store := &blockingArchiveFenceStore{memStore: newMemStore(), entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(store.release) }) })
	var f imessageArchiveFence
	f.init(true, store)
	gen, err := f.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 32)
	begun := make(chan error, 1)
	go func() { begun <- f.begin(context.Background(), gen, token) }()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reservation did not reach the store")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	disabled := make(chan error, 1)
	go func() { disabled <- f.disable(ctx) }()
	select {
	case err := <-disabled:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("busy store disable: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disable ignored its deadline while waiting for the store")
	}
	release.Do(func() { close(store.release) })
	if err := <-begun; !errors.Is(err, errIMessageArchiveRetired) {
		t.Fatalf("the stalled admission was not retired: %v", err)
	}
	if err := f.end(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if err := f.disable(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIMessageArchiveFenceDoesNotDiscardCorruptReservations(t *testing.T) {
	needsIMessageModule(t)
	store := newMemStore()
	store.settings[imessageArchiveReservationsKey] = json.RawMessage(`{"version":1,"tokens":["bad"]}`)
	var f imessageArchiveFence
	f.init(true, store)
	if err := f.disable(context.Background()); err == nil {
		t.Fatal("a corrupt reservation record was treated as empty")
	}
	if string(store.settings[imessageArchiveReservationsKey]) != `{"version":1,"tokens":["bad"]}` {
		t.Fatal("the unreadable reservation record was replaced")
	}
}

func TestIMessageArchivePendingResponseIsLocalized(t *testing.T) {
	needsIMessageModule(t)
	h := newTestManager(t)
	store := newMemStore()
	store.settings[imessageArchiveReservationsKey] = json.RawMessage(`{"version":1,"tokens":["bad"]}`)
	h.store = store
	h.connectors = connector.NewRegistry(nil)
	if err := h.addIMessage(imessagewire.Config{Machine: "example"}); err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		code, out := computerLocaleRequest(t, mux, "PUT", "/api/v1/imessage/settings", `{"ai":false}`, h.cfg.Token, lang)
		if code != 503 || out["code"] != "imessage_archive_pending" || out["error"] != i18n.T(lang, "hub-imessage.archive_pending") {
			t.Fatalf("%s: %d %v", lang, code, out)
		}
	}
}

func TestIMessageLegacyArchiveCallerFailsClosed(t *testing.T) {
	needsIMessageModule(t)
	h := newTestManager(t)
	h.store = newMemStore()
	h.connectors = connector.NewRegistry(nil)
	if err := h.addIMessage(imessagewire.Config{Machine: "example"}); err != nil {
		t.Fatal(err)
	}
	_, handled, err := h.kbSourcesCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppConnectors,
		Method: assistantwire.ConnectorsChanges, Params: json.RawMessage(`{"id":"imessage","cursor":""}`)})
	if !handled || err == nil || !strings.Contains(err.Error(), "fencing") {
		t.Fatalf("legacy caller: handled=%v error=%v", handled, err)
	}
}
