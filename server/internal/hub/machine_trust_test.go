package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// trustHub is a hub on the test database with machines (the first is the hub's own when kind local) and their
// hub.json; the trust rows of these ids are cleared first.
func trustHub(t *testing.T, machines ...MachineConfig) (*Hub, *db.DB) {
	t.Helper()
	database := testDB(t)
	h := newTestHub(t)
	h.store = database
	ctx := context.Background()
	entries := []map[string]any{}
	for _, mc := range machines {
		m := newMachine(h, mc)
		h.machines, h.byID[mc.ID] = append(h.machines, m), m
		h.cfg.Machines = append(h.cfg.Machines, mc)
		entries = append(entries, map[string]any{"id": mc.ID, "name": mc.Name, "kind": mc.Kind})
		if err := database.ForgetMachineTrust(ctx, mc.ID); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(map[string]any{"token": "t", "machines": entries})
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	h.loadMachineTrust(ctx)
	return h, database
}

func trustIDs(t *testing.T, h *Hub) []string {
	t.Helper()
	raw, err := assistantLink{h}.DeviceCall(context.Background(), assistantwire.DeviceCall{Device: assistantwire.DeviceAll,
		Method: assistantwire.DeviceDevicesList})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Devices []assistantwire.Device `json:"devices"`
	}
	_ = json.Unmarshal(raw, &res)
	var out []string
	for _, d := range res.Devices {
		out = append(out, d.ID)
	}
	return out
}

func listed(h *Hub, id string) (proto.Machine, bool) {
	for _, m := range h.machineList() {
		if m.ID == id {
			return m, true
		}
	}
	return proto.Machine{}, false
}

// No row is a confirmed computer: an upgraded hub's machines are all where they were (the assistant's machine, the
// assistant's lists, the apps' list without trust).
func TestNoTrustRowChangesNothing(t *testing.T) {
	h, _ := trustHub(t, MachineConfig{ID: "trust-mac", Name: "Mac", Kind: kindLink}, MachineConfig{ID: "trust-box", Name: "Box", Kind: kindLink})
	if got := h.defaultAssistantMachine(); got != "trust-mac" {
		t.Fatalf("assistant machine %q: the first one, as before", got)
	}
	if got := h.machineIDs(); !slices.Equal(got, []string{"trust-mac", "trust-box"}) {
		t.Fatalf("hello machines %v", got)
	}
	if got := trustIDs(t, h); !slices.Equal(got, []string{"trust-mac", "trust-box"}) {
		t.Fatalf("devices.list %v", got)
	}
	for _, id := range []string{"trust-mac", "trust-box"} {
		m, ok := listed(h, id)
		if !ok || m.Trust != "" {
			t.Fatalf("%s listed %v trust %q", id, ok, m.Trust)
		}
	}
}

// A pending computer is listed for the apps (trust pending, an error an old app shows) and left out of everything the
// assistant runs on or hands work to; no agent starts there. Confirming it puts it back; rejecting it removes it, its
// hub.json entry and its Persona Link credential; each is in the audit.
func TestPendingComputer(t *testing.T) {
	h, database := trustHub(t, MachineConfig{ID: "trust-new", Name: "New", Kind: kindLink}, MachineConfig{ID: "trust-own", Name: "Own", Kind: kindLink})
	ctx := context.Background()
	if err := h.markMachinePending(ctx, "trust-new", "dev-1"); err != nil {
		t.Fatal(err)
	}
	if got := h.defaultAssistantMachine(); got != "trust-own" {
		t.Fatalf("assistant machine %q: not the pending first one", got)
	}
	if got := h.machineIDs(); !slices.Equal(got, []string{"trust-own"}) {
		t.Fatalf("hello machines %v", got)
	}
	// The manager knows which wait from the handover (machine_trust is the hub's table).
	if c := h.devicesConfig(); !slices.Equal(c.Pending, []string{"trust-new"}) || c.PendingUnknown {
		t.Fatalf("handover pending %v, unknown %v", c.Pending, c.PendingUnknown)
	}
	if got := trustIDs(t, h); !slices.Equal(got, []string{"trust-own"}) {
		t.Fatalf("devices.list %v", got)
	}
	_, err := assistantLink{h}.DeviceCall(ctx, assistantwire.DeviceCall{Device: "trust-new", Method: assistantwire.DeviceSessionsList})
	if !errors.Is(err, assistantwire.ErrNoDevice) {
		t.Fatalf("a call to it: %v", err)
	}
	m, ok := listed(h, "trust-new")
	if !ok || m.Trust != "pending" || !strings.Contains(m.Error, "确认") || m.ErrorCode != errCodeNeverConnected {
		t.Fatalf("apps' list: %v %+v", ok, m)
	}
	rec := httptest.NewRecorder()
	h.startSession(rec, httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"machine_id":"trust-new","cwd":"/"}`)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"machine_pending"`) {
		t.Fatalf("start there: %d %s", rec.Code, rec.Body.String())
	}

	mux := http.NewServeMux()
	h.machineTrustRoutes(mux)
	post := func(path string, p principal) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, nil)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	device := principal{ID: "dev-1", Role: roleFull}
	if w := post("/api/v1/machines/trust-new/confirm", principal{ID: "pair_x", Role: roleFull, PairHash: "x"}); w.Code != http.StatusForbidden {
		t.Fatalf("a pairing code confirms: %d", w.Code)
	}
	if w := post("/api/v1/machines/trust-none/confirm", device); w.Code != http.StatusNotFound {
		t.Fatalf("no such machine: %d", w.Code)
	}
	if w := post("/api/v1/machines/trust-own/reject", device); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "确认") {
		t.Fatalf("rejecting a confirmed one: %d %s", w.Code, w.Body.String())
	}
	if w := post("/api/v1/machines/trust-new/confirm", device); w.Code != 200 {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	if h.machinePending("trust-new") || !slices.Contains(trustIDs(t, h), "trust-new") || !slices.Contains(h.machineIDs(), "trust-new") {
		t.Fatal("confirmed but still left out")
	}
	if m, _ := listed(h, "trust-new"); m.Trust != "" || strings.Contains(m.Error, "确认") {
		t.Fatalf("confirmed but listed %+v", m)
	}
	if w := post("/api/v1/machines/trust-new/confirm", device); w.Code != 200 {
		t.Fatalf("confirm again: %d", w.Code)
	}

	// Pending again, with a Persona Link credential: rejected.
	if err := h.markMachinePending(ctx, "trust-new", "dev-1"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateDeviceToken(ctx, db.DeviceToken{ID: "trust-new-link", Name: "New", Kind: "link", Role: roleLink,
		MachineID: "trust-new"}, "hash-trust-new-link"); err != nil {
		t.Fatal(err)
	}
	if w := post("/api/v1/machines/trust-new/reject", device); w.Code != 200 {
		t.Fatalf("reject: %d %s", w.Code, w.Body.String())
	}
	if tok, _ := database.DeviceTokenByHash(ctx, "hash-trust-new-link"); tok != nil {
		t.Fatal("its credential still works")
	}
	b, _ := os.ReadFile(filepath.Join(h.cfg.dir, "hub.json"))
	if strings.Contains(string(b), "trust-new") || !strings.Contains(string(b), "trust-own") {
		t.Fatalf("hub.json: %s", b)
	}
	if h.machinePending("trust-new") {
		t.Fatal("still pending")
	}
	// It has no row now (a confirmed one's state) but the manager has not let it go: it stays out of everything, so
	// the next hello does not list it as one just confirmed (the assistant would give it the keys).
	if slices.Contains(h.machineIDs(), "trust-new") || slices.Contains(trustIDs(t, h), "trust-new") ||
		h.defaultAssistantMachine() == "trust-new" {
		t.Fatal("rejected, still reported by the manager, and back in the assistant's machines")
	}
	if _, ok := listed(h, "trust-new"); ok {
		t.Fatal("rejected and still in the apps' list")
	}
	rec = httptest.NewRecorder()
	h.startSession(rec, httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"machine_id":"trust-new","cwd":"/"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("start on a rejected one: %d %s", rec.Code, rec.Body.String())
	}
	h.fleetLeft(func(id string) bool { return id == "trust-own" }) // the manager let it go
	if h.machineLeaving("trust-new") || h.machineUntrusted("trust-new") {
		t.Fatal("still kept out after it left")
	}
	audit, err := database.MachineAudits(ctx, "trust-new")
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, a := range audit {
		actions = append(actions, a.Action+":"+a.Actor)
	}
	if !slices.Equal(actions, []string{"pending:dev-1", "confirm:dev-1", "pending:dev-1", "reject:dev-1"}) {
		t.Fatalf("audit %v", actions)
	}
}

// A computer nobody confirmed within a day is removed; a younger one stays.
func TestPendingComputerExpires(t *testing.T) {
	h, database := trustHub(t, MachineConfig{ID: "trust-old", Name: "Old", Kind: kindLink}, MachineConfig{ID: "trust-young", Name: "Young", Kind: kindLink})
	ctx := context.Background()
	for _, id := range []string{"trust-old", "trust-young"} {
		if err := h.markMachinePending(ctx, id, "dev-1"); err != nil {
			t.Fatal(err)
		}
	}
	h.setPendingLocal("trust-old", time.Now().Add(-pendingFor-time.Minute), true)
	h.expirePending(ctx, time.Now())
	if h.machinePending("trust-old") || !h.machinePending("trust-young") {
		t.Fatal("the old one stays or the young one went")
	}
	list, err := database.PendingMachines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(list, func(p db.PendingMachine) bool { return p.ID == "trust-old" }) {
		t.Fatal("its row stays")
	}
	b, _ := os.ReadFile(filepath.Join(h.cfg.dir, "hub.json"))
	if strings.Contains(string(b), "trust-old") || !strings.Contains(string(b), "trust-young") {
		t.Fatalf("hub.json: %s", b)
	}
	audit, _ := database.MachineAudits(ctx, "trust-old")
	if len(audit) != 2 || audit[1].Action != "expire" || audit[1].Actor != "hub" {
		t.Fatalf("audit %+v", audit)
	}
	_ = database.ForgetMachineTrust(ctx, "trust-young")
}

func TestMachineErrorCode(t *testing.T) {
	no := false
	yes := true
	seen := time.Now()
	for _, c := range []struct {
		name string
		m    proto.Machine
		kind string
		want string
	}{
		{"online", proto.Machine{Online: true}, kindLink, ""},
		{"online, another build it can update", proto.Machine{Online: true, NodeMismatch: true, CanDeploy: &yes}, kindLink, ""},
		{"online, too old to update from here", proto.Machine{Online: true, NodeMismatch: true, CanDeploy: &no}, kindLink, errCodeNodeOutdated},
		{"online, the hub has no build for it", proto.Machine{Online: true, NodeMismatch: true, CanDeploy: &no, DeployNoPackage: true}, kindLink, ""}, // #10566
		{"online, incompatible, no build for it", proto.Machine{Online: true, NodeMismatch: true, CanDeploy: &no, DeployNoPackage: true,
			NodeIncompatible: "node_protocol_old"}, kindLink, errCodeNodeOutdated},
		{"the hub's own machine", proto.Machine{Online: true, NodeMismatch: true, CanDeploy: &no}, "local", ""},
		{"node service down", proto.Machine{Error: "Persona Link: 本机的常驻 node 没在运行（/home/u/.persona/node-attach.sock 连不上）：persona-link node install",
			LastSeen: seen}, kindLink, errCodeNodeServiceDown},
		{"node service down, the link's own words", proto.Machine{Error: "Persona Link: 本机的" + link.NodeDownMarker + "（127.0.0.1:1 连不上）",
			LastSeen: seen}, kindService, errCodeNodeServiceDown},
		{"never connected", proto.Machine{Error: "等这台电脑的 Persona Link 连上来"}, kindLink, errCodeNeverConnected},
		{"link went away", proto.Machine{Error: "link closed: failed to get reader", LastSeen: seen}, kindLink, errCodeLinkOffline},
		{"service machine offline", proto.Machine{Error: "dial tcp: refused", LastSeen: seen}, kindService, errCodeLinkOffline},
		{"ssh machine offline", proto.Machine{Error: "ssh: refused", LastSeen: seen}, "ssh", ""},
	} {
		if got := machineErrorCode(c.m, c.kind); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// failingTrust is the test database whose pending computers cannot be read until fail is false.
type failingTrust struct {
	*db.DB
	fail bool
}

func (f *failingTrust) PendingMachines(ctx context.Context) ([]db.PendingMachine, error) {
	if f.fail {
		return nil, errors.New("database down")
	}
	return f.DB.PendingMachines(ctx)
}

// When the pending computers cannot be read, no computer passes for a confirmed one (the assistant runs on none, it
// lists none, no agent starts, none expires) until they are read again.
func TestPendingComputersUnreadable(t *testing.T) {
	h, database := trustHub(t, MachineConfig{ID: "trust-a", Name: "A", Kind: kindLink}, MachineConfig{ID: "trust-b", Name: "B", Kind: kindLink})
	ctx := context.Background()
	if err := h.markMachinePending(ctx, "trust-a", "dev-1"); err != nil {
		t.Fatal(err)
	}
	h.setPendingLocal("trust-a", time.Now().Add(-pendingFor-time.Minute), true)
	f := &failingTrust{DB: database, fail: true}
	h.store = f
	h.trust.mu.Lock()
	h.trust.pending = nil // a hub that just started
	h.trust.mu.Unlock()
	if h.loadMachineTrust(ctx) {
		t.Fatal("a failed read reported as read")
	}
	if got := h.defaultAssistantMachine(); got != "" {
		t.Fatalf("assistant machine %q while unknown", got)
	}
	if got := h.machineIDs(); len(got) != 0 {
		t.Fatalf("hello machines %v while unknown", got)
	}
	if !h.devicesConfig().PendingUnknown {
		t.Fatal("the handover does not say the pending computers are unknown")
	}
	if got := trustIDs(t, h); len(got) != 0 {
		t.Fatalf("devices.list %v while unknown", got)
	}
	rec := httptest.NewRecorder()
	h.startSession(rec, httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"machine_id":"trust-b","cwd":"/"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("start while unknown: %d", rec.Code)
	}
	h.expirePending(ctx, time.Now().Add(48*time.Hour)) // nothing to go on: nothing removed
	f.fail = false
	if !h.loadMachineTrust(ctx) {
		t.Fatal("read again")
	}
	if got := h.machineIDs(); !slices.Equal(got, []string{"trust-b"}) {
		t.Fatalf("hello machines %v once read", got)
	}
	if c := h.devicesConfig(); c.PendingUnknown || !slices.Equal(c.Pending, []string{"trust-a"}) {
		t.Fatalf("handover once read: pending %v, unknown %v", c.Pending, c.PendingUnknown)
	}
	if !h.machinePending("trust-a") {
		t.Fatal("the pending one was lost")
	}
	_ = database.ForgetMachineTrust(ctx, "trust-a")
}
