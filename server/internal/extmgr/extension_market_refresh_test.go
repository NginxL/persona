package extmgr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

const selfMarketRefreshPath = "/api/v1/extensions/-/self/market/refresh"

// An explicitly scoped extension can ask only for its own install's market. The accepted request returns before the
// read finishes; every extension of that market shares the window, and the read feeds the existing upgrade candidate.
func TestExtensionSelfMarketRefresh(t *testing.T) {
	own, other := newUpgradeMarket(t), newUpgradeMarket(t)
	dir := scopedPackageAt(t, `"hub": ["market.refresh_own"]`, "1.0.0")
	own.publish(dir)
	other.publish(dir)
	spec, err := extension.ParseMarket(own.srv.URL + "/plugins/")
	if err != nil {
		t.Fatal(err)
	}
	otherSpec, err := extension.ParseMarket(other.srv.URL + "/plugins/")
	if err != nil {
		t.Fatal(err)
	}
	mid, otherMid := extension.MarketID(spec), extension.MarketID(otherSpec)

	h := newTestManager(t)
	store := newExtensionStore()
	h.store = store
	row := func(id, origin, permissions string, granted extension.Permissions) extension.Record {
		return extension.Record{ID: id, Source: extension.SourceInstalled, Origin: origin, Version: "1.0.0", Enabled: true,
			Granted: granted, Manifest: json.RawMessage(fmt.Sprintf(`{"manifest_version":1,"id":%q,"name":%q,"version":"1.0.0","permissions":{%s}}`, id, id, permissions))}
	}
	named := extension.Permissions{Hub: []string{extension.ScopeMarketRefreshOwn}}
	namedJSON := `"hub":["market.refresh_own"]`
	store.rows["scoped"] = row("scoped", extension.MarketOrigin(mid, "scoped"), namedJSON, named)
	store.rows["other"] = row("other", extension.MarketOrigin(mid, "other"), namedJSON, named)
	store.rows["elsewhere"] = row("elsewhere", extension.MarketOrigin(otherMid, "elsewhere"), namedJSON, named)
	store.rows["local"] = row("local", "dir:/example", namedJSON, named)
	attached := row("attached", extension.MarketOrigin(mid, "attached"), namedJSON, named)
	attached.Remote = &extension.Remote{Package: true}
	store.rows["attached"] = attached
	store.rows["removed"] = row("removed", "market:missing/removed", namedJSON, named)
	store.rows["unscoped"] = row("unscoped", extension.MarketOrigin(mid, "unscoped"), `"hub":[]`, extension.Permissions{})
	store.rows["legacy"] = row("legacy", extension.MarketOrigin(mid, "legacy"), `"hub_api":true`, extension.Permissions{HubAPI: true})
	startTestExtensions(t, h)

	// One client trusts both synthetic TLS markets, without disabling certificate verification.
	client := own.srv.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs.AddCert(other.srv.Certificate())
	client.Transport = transport
	h.extensions.markets.client = client
	mux := h.routes()
	phone := deviceToken("dev_phone", roleFull)
	for _, tc := range []struct {
		mk   *upgradeMarket
		spec string
		id   string
	}{{own, spec.String(), mid}, {other, otherSpec.String(), otherMid}} {
		code, out := call(t, mux, http.MethodPost, "/api/v1/extensions/-/markets", phone, fmt.Sprintf(`{"spec":%q}`, tc.spec))
		if code != http.StatusOK || out["id"] != tc.id {
			t.Fatalf("register %s: %d %v, want id %s", tc.spec, code, out, tc.id)
		}
	}
	ownReads, otherReads := own.readCount(), other.readCount()
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	now := base
	h.extensions.markets.now = func() time.Time { return now }

	token := func(id string) string {
		return h.extTokens.issue(id, id, []string{extension.ScopeMarketRefreshOwn})
	}
	scopedToken := token("scoped")
	otherToken := token("other")
	elsewhereToken := token("elsewhere")
	localToken := token("local")
	attachedToken := token("attached")
	removedToken := token("removed")
	unscopedToken := h.extTokens.issue("unscoped", "unscoped", nil)
	legacyToken := h.extTokens.issueAll("legacy", "legacy")
	ghostToken := token("ghost")

	do := func(method, path, tok, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept-Language", "en")
		req.Header.Set("Content-Type", "application/json")
		// The manager's hub-facing wrapper must replace these, never trust the extension's claimed identity.
		req.Header.Set("X-Persona-Extension", "elsewhere")
		req.Header.Set("X-Persona-Role", roleFull)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	check := func(tok string, status int, code string) *httptest.ResponseRecorder {
		t.Helper()
		w := do(http.MethodPost, selfMarketRefreshPath, tok, "")
		var out struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != status || out.Code != code {
			t.Fatalf("self refresh: %d %s, want %d %s", w.Code, w.Body.String(), status, code)
		}
		return w
	}

	if w := check(unscopedToken, http.StatusForbidden, "permission_denied"); !strings.Contains(w.Body.String(), `"scope":"market.refresh_own"`) {
		t.Fatalf("missing scope is not named: %s", w.Body.String())
	}
	check(legacyToken, http.StatusForbidden, "permission_denied")
	check(localToken, http.StatusConflict, "not_from_market")
	check(attachedToken, http.StatusConflict, "not_from_market")
	check(removedToken, http.StatusNotFound, "not_found")
	check(ghostToken, http.StatusNotFound, "not_found")
	check(h.cfg.Token, http.StatusForbidden, "permission_denied")

	// Neither another path nor a legacy/admin spelling becomes a scoped route to a caller-selected market.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/extensions/-/markets/" + otherMid + "/refresh"},
		{http.MethodPost, "/api/v1/plugins/-/markets/" + otherMid + "/refresh"},
		{http.MethodPost, "/api/v1/plugins/-/self/market/refresh"},
		{http.MethodPost, "/api/v1/admin/extensions/-/markets/" + otherMid + "/refresh"},
		{http.MethodPut, "/api/v1/extensions/-/markets/" + otherMid + "/token"},
		{http.MethodDelete, "/api/v1/extensions/-/markets/" + otherMid},
		{http.MethodGet, "/api/v1/extensions/-/catalog?refresh=1&market=" + otherMid},
		{http.MethodPost, selfMarketRefreshPath + "/" + otherMid},
	} {
		w := do(tc.method, tc.path, scopedToken, `{"token":"not-a-real-token"}`)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s: %d %s, want 403", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if own.readCount() != ownReads || other.readCount() != otherReads {
		t.Fatalf("refused calls read a market: own %d→%d, other %d→%d", ownReads, own.readCount(), otherReads, other.readCount())
	}

	// Publish only in the caller's own market, then hold its next read open. Hostile selectors still cannot reach other.
	editManifest(t, dir, func(m map[string]any) { m["version"] = "1.1.0" })
	own.publish(dir)
	started := own.holdNextRead()
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- do(http.MethodPost, selfMarketRefreshPath+"?market="+otherMid+"&mid="+otherMid, scopedToken,
			fmt.Sprintf(`{"market":%q,"origin":%q}`, otherMid, extension.MarketOrigin(otherMid, "elsewhere")))
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the accepted self refresh did not start its own market read")
	}
	var first *httptest.ResponseRecorder
	select {
	case first = <-result:
		if first.Code != http.StatusAccepted {
			t.Fatalf("first self refresh: %d %s, want 202", first.Code, first.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("202 waited for the blocked market read")
	}
	if other.readCount() != otherReads {
		t.Fatalf("query/body selected another market: %d reads, want %d", other.readCount(), otherReads)
	}
	ms := h.marketsState()
	ms.mu.Lock()
	done := ms.reading[mid]
	ms.mu.Unlock()
	if done == nil {
		t.Fatal("the accepted read is not tracked by the existing background-read deduplicator")
	}

	if w := check(otherToken, http.StatusTooManyRequests, "rate_limited"); w.Header().Get("Retry-After") != "600" {
		t.Fatalf("second extension's Retry-After = %q, want 600", w.Header().Get("Retry-After"))
	}
	now = base.Add(time.Minute)
	if w := check(scopedToken, http.StatusTooManyRequests, "rate_limited"); w.Header().Get("Retry-After") != "540" {
		t.Fatalf("Retry-After after one minute = %q, want 540", w.Header().Get("Retry-After"))
	}
	now = base.Add(10*time.Minute - 500*time.Millisecond)
	if w := check(scopedToken, http.StatusTooManyRequests, "rate_limited"); w.Header().Get("Retry-After") != "1" {
		t.Fatalf("fractional Retry-After = %q, want 1", w.Header().Get("Retry-After"))
	}
	now = base.Add(10 * time.Minute)
	check(scopedToken, http.StatusAccepted, "")
	// The boundary request joined the read already in flight, and that acceptance has its own window too.
	if w := check(otherToken, http.StatusTooManyRequests, "rate_limited"); w.Header().Get("Retry-After") != "600" {
		t.Fatalf("coalesced acceptance Retry-After = %q, want 600", w.Header().Get("Retry-After"))
	}

	own.releaseRead()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the released market read did not finish")
	}
	if own.readCount() != ownReads+1 || other.readCount() != otherReads {
		t.Fatalf("accepted calls caused own %d and other %d reads, want %d and %d", own.readCount(), other.readCount(), ownReads+1, otherReads)
	}
	up, _, _, ok := h.extensionUpgrades().candidate(h.extensions.reg.GetExact("scoped"))
	if !ok || up.To != "1.1.0" {
		t.Fatalf("the refreshed index did not feed the existing upgrade path: ok=%v upgrade=%+v", ok, up)
	}

	// A different market has an independent window, not a global extension or manager window.
	check(elsewhereToken, http.StatusAccepted, "")
	ms.mu.Lock()
	otherDone := ms.reading[otherMid]
	ms.mu.Unlock()
	if otherDone != nil {
		select {
		case <-otherDone:
		case <-time.After(5 * time.Second):
			t.Fatal("the independent market read did not finish")
		}
	}
	if other.readCount() != otherReads+1 {
		t.Fatalf("independent market reads = %d, want %d", other.readCount(), otherReads+1)
	}

	// Concurrent calls at a later boundary still admit exactly one request and one read of the shared market.
	now = base.Add(20 * time.Minute)
	beforeConcurrent := own.readCount()
	start := make(chan struct{})
	statuses := make(chan int, 2)
	for _, tok := range []string{scopedToken, otherToken} {
		go func() {
			<-start
			statuses <- do(http.MethodPost, selfMarketRefreshPath, tok, "").Code
		}()
	}
	close(start)
	got := map[int]int{}
	for range 2 {
		select {
		case status := <-statuses:
			got[status]++
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent self refresh did not answer")
		}
	}
	if got[http.StatusAccepted] != 1 || got[http.StatusTooManyRequests] != 1 {
		t.Fatalf("concurrent statuses = %v, want one 202 and one 429", got)
	}
	ms.mu.Lock()
	concurrentDone := ms.reading[mid]
	ms.mu.Unlock()
	if concurrentDone != nil {
		select {
		case <-concurrentDone:
		case <-time.After(5 * time.Second):
			t.Fatal("the concurrently admitted market read did not finish")
		}
	}
	if own.readCount() != beforeConcurrent+1 {
		t.Fatalf("concurrent calls caused %d reads, want %d", own.readCount(), beforeConcurrent+1)
	}
}
