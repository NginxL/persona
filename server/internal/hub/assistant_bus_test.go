package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// Tests never start the assistant's process (the test binary would run itself again).
// A test without an assistant does not wait for its tools either.
func init() {
	assistantCommand = func(context.Context) (*exec.Cmd, error) { return nil, errors.New("no assistant process in tests") }
	assistantToolsWait = 0
}

// testAssistant is the assistant (internal/assistant) on the hub's bus, run in the test's process the way `personad
// assistant` runs it: its own Service, its own store (assistantStoreOf), a real WebSocket connection to the hub.
type testAssistant struct {
	svc   *assistant.Service
	bh    *assistant.BusHub
	stop  context.CancelFunc
	done  chan struct{}
	token string // its bus token (the manager's relay dials with it too)
}

// connectAssistant connects the assistant to h, on a server with h's bus endpoint alone (identities in memory when
// the hub has no database), and waits until the hub has its tools and its snapshot.
func connectAssistant(t *testing.T, h *Hub, stores ...assistant.Store) *testAssistant {
	t.Helper()
	if h.busStore() == nil {
		h.bus.idents = &memIdents{}
	}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	var store assistant.Store
	if len(stores) > 0 {
		store = stores[0]
	}
	return dialAssistantWithStore(t, h, srv.URL, false, store)
}

// dialAssistant runs the assistant against the hub at url with a new token, and waits as connectAssistant does.
// schedule runs its schedule loop too (as `personad assistant` does; the other tests run due tasks themselves).
func dialAssistant(t *testing.T, h *Hub, url string, schedule bool, opts ...func(*assistant.BusOptions)) *testAssistant {
	t.Helper()
	return dialAssistantWithStore(t, h, url, schedule, nil, opts...)
}

func dialAssistantWithStore(t *testing.T, h *Hub, url string, schedule bool, store assistant.Store, opts ...func(*assistant.BusOptions)) *testAssistant {
	t.Helper()
	tok, err := h.createBusToken(context.Background(), assistantwire.AssistantID, "助手")
	if err != nil {
		t.Fatal(err)
	}
	var logMu sync.Mutex
	over := false // t.Logf panics once the test is over
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		if !over {
			t.Logf(format, args...)
		}
	}
	// The hub's test machines are its devices unless the test gives it the real ones (startTestManager): what comes
	// from the nodes is the manager's (fleet_mirror_test.go); its extensions are faked (extensions_fake_test.go).
	o := assistant.BusOptions{Hub: url, Token: tok, Backoff: 50 * time.Millisecond, Version: "test", Deploy: "test", Logf: logf,
		Devices: mirrorOf(h), Extensions: fakeExtOf(h), ConfigFile: managerFileOf(t, h)}
	for _, f := range opts {
		f(&o)
	}
	bh := assistant.NewBusHub(o)
	mgr, _ := managerDBOf(t, h) // taken now: a call of the assistant's that outlives the test never falls back to h.store
	svc := assistant.New(assistant.Options{Hub: bh, Logf: o.Logf, Devices: o.Devices, Store: func() assistant.Store {
		switch {
		case store != nil:
			return store
		case mgr != nil:
			return mgr
		}
		s, _ := h.store.(assistant.Store)
		return s
	}, KB: testKBOf(h), AgentDirs: testAgentDirsOf(h)}) // the knowledge base and hidden folders a test gave it
	if d, ok := testAgentDirs.Load(h); ok {
		d := d.(*hubAgentDirs)
		d.mu.Lock()
		d.changed = svc.HiddenAgentDirsChanged
		d.mu.Unlock()
	}
	svc.SetExtensions(fakeExtOf(h)) // the extensions' tools, the manager's (faked)
	ctx, cancel := context.WithCancel(context.Background())
	a := &testAssistant{svc: svc, bh: bh, stop: cancel, done: make(chan struct{}), token: tok}
	testAssistants.Store(h, a)
	go func() {
		defer close(a.done)
		if schedule {
			_ = bh.Run(ctx, svc)
		} else {
			_ = bh.Serve(ctx, svc)
		}
	}()
	t.Cleanup(func() {
		logMu.Lock()
		over = true
		logMu.Unlock()
		testAssistants.CompareAndDelete(h, a)
		cancel()
		<-a.done
		for _, c := range h.stopBus() {
			_ = c.ws.CloseNow()
		}
	})
	a.ready(t, h)
	return a
}

// ready waits until the assistant is connected, the hub has its tools and the snapshot it reports on connecting.
func (a *testAssistant) ready(t *testing.T, h *Hub) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.bh.Wait(ctx); err != nil {
		t.Fatalf("the assistant did not connect: %v", err)
	}
	eventually(t, 10*time.Second, "the assistant's tools and snapshot", func() bool {
		st := &h.assistant
		st.toolsMu.Lock()
		tools := st.tools != nil
		st.toolsMu.Unlock()
		st.mu.RLock()
		got := st.got
		st.mu.RUnlock()
		return tools && (got || h.store == nil)
	})
}

// restartAssistant stops h's assistant and connects a new one (it reads its store again), waiting for its snapshot.
func restartAssistant(t *testing.T, h *Hub) *testAssistant {
	t.Helper()
	old := assistantOf(t, h)
	old.stop()
	<-old.done
	st := &h.assistant
	st.mu.Lock()
	st.got = false
	st.mu.Unlock()
	return connectAssistant(t, h)
}

// helloChanged tells h's assistant its hello answer now (manager.updated), for a test that changed what it says
// behind the hub's back (its store, its machines), and waits until it took it.
func helloChanged(t *testing.T, h *Hub) {
	t.Helper()
	hello, _ := assistantLink{h}.Hello(context.Background())
	if err := h.notifyBus(assistantwire.AssistantID, assistantwire.EventUpdated, hello); err != nil {
		t.Fatal(err)
	}
	settleAssistant(h)
}

// managerStores are, by hub, the test databases whose assistant keeps its state on a handle of its own
// (useManagerDB): one opened for the manager's set, as the default manager has on a database it shares with the hub.
var managerStores sync.Map // *Hub → *managerHandle

type managerHandle struct {
	url  string
	once sync.Once
	db   *db.DB
	err  error
}

// useManagerDB makes the database at url, which h.store has opened for the hub's set alone, where h's assistant
// keeps its state: it is opened for the manager's set when a test first needs it (a hub test without an assistant
// sees the hub's tables alone).
func useManagerDB(t *testing.T, h *Hub, url string) {
	t.Helper()
	managerStores.Store(h, &managerHandle{url: url})
	t.Cleanup(func() { managerStores.Delete(h) })
}

// managerDBOf is the manager-set handle of h's database, opened on first use; false for a hub without one.
func managerDBOf(t *testing.T, h *Hub) (*db.DB, bool) {
	t.Helper()
	v, ok := managerStores.Load(h)
	if !ok {
		return nil, false
	}
	m := v.(*managerHandle)
	m.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if m.db, m.err = db.Open(ctx, m.url, db.ManagerSet); m.err == nil {
			t.Cleanup(m.db.Close) // after the assistant dialed later stops (cleanups run last first)
		}
	})
	if m.err != nil {
		t.Fatal(m.err)
	}
	return m.db, true
}

// assistantStoreOf is where h's test assistant keeps its state: the manager-set handle of its database
// (useManagerDB), else h.store (a test's in-memory store, or a database opened for both sets).
func assistantStoreOf(t *testing.T, h *Hub) Store {
	t.Helper()
	if m, ok := managerDBOf(t, h); ok {
		return m
	}
	return h.store
}

// managerDB is the database the assistant of h (a hub from newBusHub) keeps its state in: the same one, opened for
// the manager's set (its tables, its settings in settings); h.store is the hub's (hub_settings). A test seeds and
// reads the assistant's state here.
func managerDB(t *testing.T, h *Hub) *db.DB {
	t.Helper()
	m, ok := managerDBOf(t, h)
	if !ok {
		t.Fatal("managerDB: not a hub from newBusHub")
	}
	return m
}

// testAssistants are the connected ones, by hub.
var testAssistants sync.Map // *Hub → *testAssistant

// assistantOf is h's connected assistant (connectAssistant).
func assistantOf(t *testing.T, h *Hub) *testAssistant {
	t.Helper()
	a, ok := testAssistants.Load(h)
	if !ok {
		t.Fatal("no assistant connected to this hub")
	}
	return a.(*testAssistant)
}

// settleAssistant waits until h's assistant took the events the hub sent it so far: a call to it is read after them
// (the same connection, in order), and then its event worker is waited for. Nothing when none is connected.
func settleAssistant(h *Hub) {
	v, ok := testAssistants.Load(h)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = h.callBus(ctx, assistantwire.AssistantID, assistantwire.MethodTools, nil, nil)
	v.(*testAssistant).bh.Settle()
}

// memIdents keeps the bus's identities in memory (hub tests without Postgres).
type memIdents struct {
	mu     sync.Mutex
	byID   map[string]db.BusIdentity
	hashes map[string]string // token hash -> id
}

func (m *memIdents) SaveBusIdentity(_ context.Context, b db.BusIdentity, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byID == nil {
		m.byID, m.hashes = map[string]db.BusIdentity{}, map[string]string{}
	}
	for h, id := range m.hashes {
		if id == b.ID {
			delete(m.hashes, h)
		}
	}
	b.CreatedAt = time.Now()
	m.byID[b.ID], m.hashes[hash] = b, b.ID
	return nil
}

func (m *memIdents) BusIdentityByHash(_ context.Context, hash string) (*db.BusIdentity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.hashes[hash]
	if !ok {
		return nil, nil
	}
	b := m.byID[id]
	return &b, nil
}

func (m *memIdents) BusIdentities(context.Context) ([]db.BusIdentity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []db.BusIdentity
	for _, b := range m.byID {
		out = append(out, b)
	}
	return out, nil
}

func (m *memIdents) DeleteBusIdentity(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.byID[id]
	delete(m.byID, id)
	for h, v := range m.hashes {
		if v == id {
			delete(m.hashes, h)
		}
	}
	return ok, nil
}

// useModel makes c the manager's "ai" (its manager.json), the baseline of the assistant's API purposes, telling a
// connected assistant.
func useModel(t *testing.T, h *Hub, c ai.Config) {
	t.Helper()
	setManagerJSON(t, h, "ai", c)
}

// managerFiles are the test managers' manager.json, by hub: the sections the hub used to hand over (ownconfig.go in
// the assistant).
var managerFiles sync.Map // *Hub → string

// managerFileOf is h's test manager's manager.json (made empty the first time).
func managerFileOf(t *testing.T, h *Hub) string {
	t.Helper()
	if f, ok := managerFiles.Load(h); ok {
		return f.(string)
	}
	f, _ := managerFiles.LoadOrStore(h, filepath.Join(t.TempDir(), "manager.json"))
	t.Cleanup(func() { managerFiles.Delete(h) })
	return f.(string)
}

// setManagerJSON sets key ("section" or "section.key") of h's test manager's manager.json to v, and tells a connected
// manager (it reads the file again at the hello).
func setManagerJSON(t *testing.T, h *Hub, key string, v any) {
	t.Helper()
	file := managerFileOf(t, h)
	cfg := map[string]any{}
	if b, err := os.ReadFile(file); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}
	}
	if section, rest, ok := strings.Cut(key, "."); ok {
		nested, _ := cfg[section].(map[string]any)
		if nested == nil {
			nested = map[string]any{}
		}
		nested[rest] = v
		cfg[section] = nested
	} else {
		cfg[key] = v
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := testAssistants.Load(h); ok && h.busConnOf(assistantwire.AssistantID) != nil {
		helloChanged(t, h)
	}
}

// putModel sets purpose's record in the model configuration (the assistant reads it from its store).
func putModel(t *testing.T, h *Hub, purpose string, b assistantwire.ModelBinding) {
	t.Helper()
	var r struct {
		Revision int64                      `json:"revision"`
		Binding  assistantwire.ModelBinding `json:"binding"`
	}
	ctx := context.Background()
	if _, err := assistantStoreOf(t, h).Setting(ctx, "model_config."+purpose, &r); err != nil {
		t.Fatal(err)
	}
	r.Revision++
	r.Binding = b
	if err := assistantStoreOf(t, h).PutSetting(ctx, "model_config."+purpose, r); err != nil {
		t.Fatal(err)
	}
}
