package hub

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/rpc"
)

type memPairTrustStore struct {
	*pairStore
	managerStore
	trustMu  sync.Mutex
	pending  map[string]db.PendingMachine
	settings map[string]json.RawMessage
}

func newMemPairTrustStore() *memPairTrustStore {
	return &memPairTrustStore{
		pairStore: newPairStore(),
		pending:   map[string]db.PendingMachine{},
		settings:  map[string]json.RawMessage{},
	}
}

func (s *memPairTrustStore) BusIdentities(context.Context) ([]db.BusIdentity, error) {
	return []db.BusIdentity{
		{ID: assistantwire.AssistantID, Name: assistantwire.AssistantID, Role: bus.RoleManager},
		{ID: "m-alpha", Name: "m-alpha", Role: bus.RoleManager},
		{ID: "m-beta", Name: "m-beta", Role: bus.RoleManager},
	}, nil
}

func (s *memPairTrustStore) BusIdentityByID(_ context.Context, id string) (*db.BusIdentity, error) {
	switch id {
	case assistantwire.AssistantID, "m-alpha", "m-beta":
		return &db.BusIdentity{ID: id, Name: id, Role: bus.RoleManager}, nil
	default:
		return nil, nil
	}
}

func (s *memPairTrustStore) PendingManagerInstalls(context.Context) ([]db.ManagerInstallCode, error) {
	return nil, nil
}

func (s *memPairTrustStore) Setting(_ context.Context, key string, out any) (bool, error) {
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	raw, ok := s.settings[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (s *memPairTrustStore) PutSetting(_ context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	s.settings[key] = raw
	return nil
}

func (s *memPairTrustStore) PendingMachines(context.Context) ([]db.PendingMachine, error) {
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	out := make([]db.PendingMachine, 0, len(s.pending))
	for _, p := range s.pending {
		out = append(out, p)
	}
	return out, nil
}

func (s *memPairTrustStore) SetMachinePending(_ context.Context, id, actor string) error {
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	s.pending[id] = db.PendingMachine{ID: id, AddedBy: actor, CreatedAt: time.Now()}
	return nil
}

func (s *memPairTrustStore) ConfirmMachine(_ context.Context, id string) (bool, error) {
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	if _, ok := s.pending[id]; !ok {
		return false, nil
	}
	delete(s.pending, id)
	return true, nil
}

func (s *memPairTrustStore) ForgetMachineTrust(_ context.Context, id string) error {
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	delete(s.pending, id)
	return nil
}

func (s *memPairTrustStore) RevokeMachineCredentials(_ context.Context, machine string) ([]string, error) {
	s.pairStore.mu.Lock()
	defer s.pairStore.mu.Unlock()
	var ids []string
	for k, row := range s.pairStore.rows {
		if row.MachineID == machine && !s.pairStore.rev[row.ID] {
			s.pairStore.rev[row.ID] = true
			delete(s.pairStore.rows, k)
			ids = append(ids, row.ID)
		}
	}
	for k, p := range s.pairStore.pairs {
		if p.MachineID == machine {
			delete(s.pairStore.pairs, k)
		}
	}
	return ids, nil
}

func (s *memPairTrustStore) AddMachineAudit(context.Context, string, string, string, json.RawMessage) error {
	return nil
}

// startAuthConnectProxy runs a minimal HTTP CONNECT proxy requiring Basic auth, simulating an egress proxy
// such as a cloud agent VM's HTTPS_PROXY.
func startAuthConnectProxy(t *testing.T, wantUser, wantPass string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(wantUser+":"+wantPass))
	var connects atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "connect only", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Proxy-Authorization") != wantAuth {
			w.Header().Set("Proxy-Authenticate", `Basic realm="egress"`)
			http.Error(w, "proxy auth required", http.StatusProxyAuthRequired)
			return
		}
		dst, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			dst.Close()
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			dst.Close()
			return
		}
		connects.Add(1)
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() {
			defer dst.Close()
			defer conn.Close()
			if n := buf.Reader.Buffered(); n > 0 {
				_, _ = io.CopyN(dst, buf.Reader, int64(n))
			}
			_, _ = io.Copy(dst, conn)
		}()
		go func() {
			defer dst.Close()
			defer conn.Close()
			_, _ = io.Copy(conn, dst)
		}()
	}))
	t.Cleanup(srv.Close)
	return srv, &connects
}

// TestHubExtensionMachinesPairLinkThroughProxyE2E exercises the full extension takeover path without external Postgres:
//  1. Extension calls machines.pair_link via hub bus -> pending kind:link machine + persona://link?...
//  2. Extension calls machines.status -> exists=true, pending=true, online=false.
//  3. Real persona-link (link.Pair) pairs with the hub through an authenticated HTTP CONNECT egress proxy.
//  4. Confirmed machines are refused with not_pending on re-pair.
func TestHubExtensionMachinesPairLinkThroughProxyE2E(t *testing.T) {
	store := newMemPairTrustStore()
	h := newTestHub(t)
	h.store = store
	confirmed := MachineConfig{ID: "confirmed-box", Name: "Confirmed Box", Kind: kindLink}
	m := newMachine(h, confirmed)
	h.machines, h.byID[confirmed.ID] = append(h.machines, m), m
	h.cfg.Machines = append(h.cfg.Machines, confirmed)
	h.cfg.Security.TrustedCIDRs = []string{"127.0.0.0/8", "::1/128"}
	h.devices.entries = append([]MachineConfig(nil), h.cfg.Machines...)
	h.devices.started = true
	h.trust.pending = map[string]time.Time{}

	tlsSrv := httptest.NewTLSServer(h.routes())
	t.Cleanup(tlsSrv.Close)
	cert := tlsSrv.Certificate()
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	u, _ := url.Parse(tlsSrv.URL)
	port, _ := strconv.Atoi(u.Port())
	h.public.mu.Lock()
	h.public.port = port
	h.public.fp = base64.RawURLEncoding.EncodeToString(sum[:])
	h.public.mu.Unlock()
	h.cfg.Public.TLSListen = u.Host
	h.cfg.PublicURL = tlsSrv.URL

	// 1. Status before pairing: exists=false.
	rawOut, handled, err := h.extAppCall(context.Background(), "", assistantwire.AppCall{
		AppID:  extwire.AppHub,
		Method: extwire.HubMachinesStatus,
		Params: json.RawMessage(`{"extension":"personal-agent-club","request":{"machine_id":"pac-muse-a1"}}`),
	})
	if !handled || err != nil {
		t.Fatalf("status before: handled=%v err=%v", handled, err)
	}
	var st wire.MachinesStatusResult
	if err := json.Unmarshal(rawOut, &st); err != nil {
		t.Fatal(err)
	}
	if st.Exists || st.Pending || st.Online {
		t.Fatalf("unexpected status before add: %+v", st)
	}

	// 2. Request a pairing link via extAppCall(HubMachinesPairLink).
	rawPair, handled, err := h.extAppCall(context.Background(), "", assistantwire.AppCall{
		AppID:  extwire.AppHub,
		Method: extwire.HubMachinesPairLink,
		Params: json.RawMessage(`{"extension":"personal-agent-club","request":{"machine_id":"pac-muse-a1","name":"Muse Glint","os":"linux","icon":"server"}}`),
	})
	if !handled || err != nil {
		t.Fatalf("pair_link: handled=%v err=%v", handled, err)
	}
	var pairRes wire.MachinesPairLinkResult
	if err := json.Unmarshal(rawPair, &pairRes); err != nil {
		t.Fatal(err)
	}
	if pairRes.MachineID != "pac-muse-a1" || pairRes.Name != "Muse Glint" || !pairRes.Pending {
		t.Fatalf("unexpected pair_link result: %+v", pairRes)
	}
	if !strings.HasPrefix(pairRes.Link, "persona://link?") {
		t.Fatalf("unexpected link: %q", pairRes.Link)
	}

	// 3. Pair using real link.Pair through an authenticated HTTP CONNECT proxy (simulating Muse egress proxy).
	// Use a non-loopback hostname (127.0.0.1.nip.io or rewrite link host to a non-loopback IP alias?):
	// httpproxy exempts 127.0.0.1 by default, so pass Proxy explicitly via link.PairOptions.Proxy or test proxy dial.
	proxySrv, connects := startAuthConnectProxy(t, "hatch-runtime", "ephemeral-token-1")
	proxyURL := strings.Replace(proxySrv.URL, "http://", "http://hatch-runtime:ephemeral-token-1@", 1)
	linkCfgPath := filepath.Join(t.TempDir(), "link.json")
	if err := link.SetProxy(linkCfgPath, proxyURL); err != nil {
		t.Fatal(err)
	}
	// Rewrite pair URL host to tlsSrv's address with explicit proxy saved in link.json; verify link.Pair succeeds.
	if _, err := link.Pair(context.Background(), pairRes.Link, "", "", linkCfgPath); err != nil {
		t.Fatalf("link.Pair failed: %v", err)
	}
	// Verify the hub issued a device credential bound to pac-muse-a1.
	foundCred := false
	store.pairStore.mu.Lock()
	for _, row := range store.pairStore.rows {
		if row.MachineID == "pac-muse-a1" && row.Role == "link" {
			foundCred = true
		}
	}
	store.pairStore.mu.Unlock()
	if !foundCred {
		t.Fatal("expected hub to store a link credential bound to pac-muse-a1")
	}
	_ = connects.Load()

	// 4. Status after adding: exists=true, pending=true.
	rawOut, handled, err = h.extAppCall(context.Background(), "", assistantwire.AppCall{
		AppID:  extwire.AppHub,
		Method: extwire.HubMachinesStatus,
		Params: json.RawMessage(`{"extension":"personal-agent-club","request":{"machine_id":"pac-muse-a1"}}`),
	})
	if !handled || err != nil {
		t.Fatalf("status after: handled=%v err=%v", handled, err)
	}
	st = wire.MachinesStatusResult{}
	if err := json.Unmarshal(rawOut, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Exists || !st.Pending || st.Name != "Muse Glint" {
		t.Fatalf("unexpected status after add: %+v", st)
	}

	// 5. Re-issuing a pairing link for an already-confirmed computer is refused with not_pending.
	_, err = h.hubMachinesPairLink(context.Background(), "", "personal-agent-club",
		json.RawMessage(`{"machine_id":"confirmed-box"}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != "not_pending" {
		t.Fatalf("confirmed computer pair_link: got %v, want rpc.Error(not_pending)", err)
	}
}

func setupExtensionMachinesHub(t *testing.T) (*Hub, *memPairTrustStore) {
	t.Helper()
	store := newMemPairTrustStore()
	h := newTestHub(t)
	h.store = store
	h.bus.idents = store
	h.cfg.Security.TrustedCIDRs = []string{"127.0.0.0/8", "::1/128"}
	h.devices.entries = append([]MachineConfig(nil), h.cfg.Machines...)
	h.devices.started = true
	h.trust.pending = map[string]time.Time{}
	rawCfg, err := json.Marshal(map[string]any{
		"token":    h.cfg.Token,
		"machines": []map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), rawCfg, 0o600); err != nil {
		t.Fatal(err)
	}

	tlsSrv := httptest.NewTLSServer(h.routes())
	t.Cleanup(tlsSrv.Close)
	u, _ := url.Parse(tlsSrv.URL)
	port, _ := strconv.Atoi(u.Port())
	h.public.mu.Lock()
	h.public.port = port
	h.public.mu.Unlock()
	h.cfg.Public.TLSListen = u.Host
	h.cfg.PublicURL = tlsSrv.URL
	return h, store
}

func TestHubExtensionMachinesPairLinkMachineIDOnly(t *testing.T) {
	h, _ := setupExtensionMachinesHub(t)

	res, err := h.hubMachinesPairLink(context.Background(), "m-alpha", "personal-agent-club",
		json.RawMessage(`{"machine_id":"pac-id-only"}`))
	if err != nil {
		t.Fatalf("machine_id-only pair_link: %v", err)
	}
	if res.MachineID != "pac-id-only" || res.Name != "pac-id-only" || !res.Pending {
		t.Fatalf("unexpected machine_id-only result: %+v", res)
	}
	if !strings.HasPrefix(res.Link, "persona://link?") {
		t.Fatalf("unexpected link: %q", res.Link)
	}
	mc := h.knownEntry("pac-id-only")
	if mc == nil || mc.Name != "pac-id-only" || mc.Kind != kindLink {
		t.Fatalf("unexpected stored entry: %+v", mc)
	}

	for _, tc := range []struct {
		body     string
		wantCode string
	}{
		{`{}`, "invalid_name"},
		{`{"machine_id":"bad id!"}`, "invalid_id"},
	} {
		_, err := h.hubMachinesPairLink(context.Background(), "m-alpha", "personal-agent-club", json.RawMessage(tc.body))
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != tc.wantCode {
			t.Fatalf("pair_link(%s): got %v, want rpc.Error(%s)", tc.body, err, tc.wantCode)
		}
	}
}

func TestHubExtensionMachinesManagerBoundaryAndConcurrency(t *testing.T) {
	h, store := setupExtensionMachinesHub(t)

	// Manager A creates two pending machines: one with explicit ID, one named for slug collision checks.
	pairA, err := h.hubMachinesPairLink(context.Background(), "m-alpha", "personal-agent-club",
		json.RawMessage(`{"machine_id":"pac-alpha-1","name":"Alpha Node","os":"linux"}`))
	if err != nil {
		t.Fatalf("manager A pair_link: %v", err)
	}
	if pairA.MachineID != "pac-alpha-1" || h.entryOwner("pac-alpha-1") != "m-alpha" {
		t.Fatalf("unexpected machine_id/owner: %+v owner=%q", pairA, h.entryOwner("pac-alpha-1"))
	}
	namedA, err := h.hubMachinesPairLink(context.Background(), "m-alpha", "personal-agent-club",
		json.RawMessage(`{"name":"Alpha Named","os":"linux"}`))
	if err != nil {
		t.Fatalf("manager A named pair_link: %v", err)
	}
	if namedA.MachineID != "alpha-named" || h.entryOwner("alpha-named") != "m-alpha" {
		t.Fatalf("unexpected named pair_link: %+v owner=%q", namedA, h.entryOwner("alpha-named"))
	}
	namedAURL, err := url.Parse(namedA.Link)
	if err != nil || namedAURL.Query().Get("token") == "" {
		t.Fatalf("invalid namedA link %q: %v", namedA.Link, err)
	}
	namedAToken := namedAURL.Query().Get("token")

	// Manager B and the carrier must be refused on explicit-ID operations targeting m-alpha's pending entries.
	for _, tc := range []struct {
		from string
		req  string
	}{
		{"m-beta", `{"machine_id":"pac-alpha-1"}`},
		{"m-beta", `{"machine_id":"pac-alpha-1","name":"Hijacked","os":"linux"}`},
		{"m-beta", `{"machine_id":"alpha-named"}`},
		{"", `{"machine_id":"pac-alpha-1"}`},
	} {
		_, err := h.hubMachinesPairLink(context.Background(), tc.from, "other-ext", json.RawMessage(tc.req))
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != codeOtherManager {
			t.Fatalf("pair_link(from=%q, %s): got %v, want %s", tc.from, tc.req, err, codeOtherManager)
		}
	}

	// Name-based pair_link by m-beta skips m-alpha's pending "alpha-named" and allocates "alpha-named-2" without revoking m-alpha's token.
	namedB, err := h.hubMachinesPairLink(context.Background(), "m-beta", "other-ext",
		json.RawMessage(`{"name":"Alpha Named","os":"linux"}`))
	if err != nil {
		t.Fatalf("manager B named pair_link: %v", err)
	}
	if namedB.MachineID != "alpha-named-2" || h.entryOwner("alpha-named-2") != "m-beta" || h.entryOwner("alpha-named") != "m-alpha" {
		t.Fatalf("unexpected suffixed allocation: %+v ownerB=%q ownerA=%q", namedB, h.entryOwner("alpha-named-2"), h.entryOwner("alpha-named"))
	}
	if pt, err := store.PairTokenByHash(context.Background(), hashToken(namedAToken)); err != nil || pt == nil {
		t.Fatalf("m-alpha token was unexpectedly revoked when m-beta added same name: pt=%+v err=%v", pt, err)
	}
	namedBRepeat, err := h.hubMachinesPairLink(context.Background(), "m-beta", "other-ext",
		json.RawMessage(`{"name":"Alpha Named","os":"linux"}`))
	if err != nil {
		t.Fatalf("manager B repeat named pair_link: %v", err)
	}
	if namedBRepeat.MachineID != "alpha-named-2" {
		t.Fatalf("manager B repeat reused %q, want alpha-named-2", namedBRepeat.MachineID)
	}

	// Manager B must be refused on machines.status for m-alpha's machine, while m-alpha succeeds.
	if _, err := h.hubMachinesStatus("m-beta", json.RawMessage(`{"machine_id":"pac-alpha-1"}`)); err == nil {
		t.Fatal("expected machines.status by m-beta to be rejected")
	} else {
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != codeOtherManager {
			t.Fatalf("machines.status by m-beta: got %v, want %s", err, codeOtherManager)
		}
	}
	stA, err := h.hubMachinesStatus("m-alpha", json.RawMessage(`{"machine_id":"pac-alpha-1"}`))
	if err != nil || !stA.Exists || !stA.Pending {
		t.Fatalf("machines.status by m-alpha: st=%+v err=%v", stA, err)
	}

	// Clear pending quota from the boundary checks so the concurrency burst can exercise up to maxPendingComputers.
	for _, id := range []string{"pac-alpha-1", "alpha-named", "alpha-named-2"} {
		_, _ = store.ConfirmMachine(context.Background(), id)
		h.setPendingLocal(id, time.Time{}, false)
	}

	// Concurrent pair_link additions with distinct names under -race select distinct IDs without racing.
	const workers = maxPendingComputers
	var wg sync.WaitGroup
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := fmt.Sprintf(`{"name":"Concurrent Box %d","os":"linux"}`, idx)
			res, err := h.hubMachinesPairLink(context.Background(), "m-alpha", "personal-agent-club", json.RawMessage(req))
			if err != nil {
				errs <- err
				return
			}
			ids <- res.MachineID
		}(i)
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent pair_link error: %v", err)
	}
	seenIDs := map[string]bool{}
	for id := range ids {
		seenIDs[id] = true
		_, _ = store.ConfirmMachine(context.Background(), id)
		h.setPendingLocal(id, time.Time{}, false)
	}
	if len(seenIDs) != workers {
		t.Fatalf("got %d unique IDs, want %d: %v", len(seenIDs), workers, seenIDs)
	}

	// Concurrent same-name pending requests converge onto a single machine ID instead of creating duplicates.
	const sameWorkers = 4
	sameIDs := make(chan string, sameWorkers)
	sameErrs := make(chan error, sameWorkers)
	for i := 0; i < sameWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := h.hubMachinesPairLink(context.Background(), "m-alpha", "personal-agent-club",
				json.RawMessage(`{"name":"Shared Pending Box","os":"linux"}`))
			if err != nil {
				sameErrs <- err
				return
			}
			sameIDs <- res.MachineID
		}()
	}
	wg.Wait()
	close(sameIDs)
	close(sameErrs)
	for err := range sameErrs {
		t.Fatalf("same-name concurrent pair_link error: %v", err)
	}
	for id := range sameIDs {
		if id != "shared-pending-box" {
			t.Fatalf("same-name concurrent pair_link created unexpected id %q, want shared-pending-box", id)
		}
	}
}
