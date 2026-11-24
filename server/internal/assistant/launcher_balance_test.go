package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Official DeepSeek launchers report a prepaid balance at the provider's own origin (#11825). The manager reads it
// with the launcher's sealed key and hands on an amount and currency, never the key.
func TestLauncherBalancesReadsTheOfficialDeepSeekBalance(t *testing.T) {
	s, mux := launcherTestService(t)
	base := "https://api.deepseek.com/anthropic"
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
		`{"name":"deepseek","base_url":"`+base+`","model":"deepseek-v4-pro","api_key":"`+fakeKey+`"}`); code != 200 {
		t.Fatal(code, raw)
	}
	var requests []*http.Request
	var bodies []string
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Clone(context.Background()))
		return probeResponse(r, 200, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"12.34","granted_balance":"0.00","topped_up_balance":"12.34"}]}`), nil
	})
	accounts, known := s.LauncherBalances(context.Background())
	if !known || len(accounts) != 1 {
		t.Fatalf("known=%v accounts=%+v", known, accounts)
	}
	a := accounts[0]
	if a.Provider != "deepseek" || a.Name != "deepseek" || a.Problem != "" || a.Balance == nil {
		t.Fatalf("account: %+v", a)
	}
	if a.Balance.Amount != "12.34" || a.Balance.Currency != "CNY" {
		t.Fatalf("balance: %+v", a.Balance)
	}
	if len(requests) != 1 {
		t.Fatalf("requests: %d", len(requests))
	}
	r := requests[0]
	if r.Method != http.MethodGet || r.URL.String() != "https://api.deepseek.com/user/balance" {
		t.Fatalf("request: %s %s", r.Method, r.URL)
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeKey || r.Header.Get("x-api-key") != fakeKey {
		t.Fatal("the launcher's key was not sent to its own provider")
	}
	raw, _ := json.Marshal(accounts)
	bodies = append(bodies, string(raw))
	for _, body := range bodies {
		if strings.Contains(body, fakeKey) {
			t.Fatalf("the key leaked into a sample: %s", body)
		}
	}
}

// A genuine zero is a balance the sidebar can show, not the absence of one.
func TestLauncherBalancesKeepsAGenuineZero(t *testing.T) {
	s, mux := launcherTestService(t)
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
		`{"name":"deepseek","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro","api_key":"`+fakeKey+`"}`); code != 200 {
		t.Fatal(code, raw)
	}
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		return probeResponse(r, 200, `{"is_available":false,"balance_infos":[{"currency":"cny","total_balance":"0.00"}]}`), nil
	})
	accounts, _ := s.LauncherBalances(context.Background())
	if len(accounts) != 1 || accounts[0].Balance == nil || accounts[0].Balance.Amount != "0.00" || accounts[0].Balance.Currency != "CNY" || accounts[0].Problem != "" {
		t.Fatalf("zero balance: %+v", accounts)
	}
}

// A failed read is a problem code, never a made-up zero.
func TestLauncherBalancesReportsFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		netErr bool
		want   string
	}{
		{name: "refused key", status: http.StatusUnauthorized, body: `{"error":{"message":"bad key"}}`, want: "rejected"},
		{name: "forbidden key", status: http.StatusForbidden, body: `{}`, want: "rejected"},
		{name: "server error", status: http.StatusInternalServerError, body: `{}`, want: "failed"},
		{name: "redirect", status: http.StatusFound, body: ``, want: "failed"},
		{name: "not json", status: http.StatusOK, body: `not json`, want: "failed"},
		{name: "no amount", status: http.StatusOK, body: `{"balance_infos":[]}`, want: "failed"},
		{name: "bad amount", status: http.StatusOK, body: `{"balance_infos":[{"currency":"CNY","total_balance":"twelve"}]}`, want: "failed"},
		{name: "unreachable", netErr: true, want: "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mux := launcherTestService(t)
			if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
				`{"name":"deepseek","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro","api_key":"`+fakeKey+`"}`); code != 200 {
				t.Fatal(code, raw)
			}
			interceptProbe(t, func(r *http.Request) (*http.Response, error) {
				if tc.netErr {
					return nil, io.ErrUnexpectedEOF
				}
				return probeResponse(r, tc.status, tc.body), nil
			})
			accounts, known := s.LauncherBalances(context.Background())
			if !known || len(accounts) != 1 || accounts[0].Problem != tc.want || accounts[0].Balance != nil {
				t.Fatalf("%s: known=%v %+v", tc.name, known, accounts)
			}
		})
	}
}

// Only the provider's own origin is asked: a gateway or a lookalike host never receives the launcher's key here.
func TestLauncherBalancesIgnoresOtherOrigins(t *testing.T) {
	for _, base := range []string{
		"https://gateway.example.com/anthropic",
		"https://api.deepseek.com.example.com/anthropic",
		"https://api.deepseek.com:8443/anthropic",
		"http://api.deepseek.com/anthropic",
		"https://api.deepseek.com/team/anthropic",
	} {
		s, mux := launcherTestService(t)
		if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
			`{"name":"gw","base_url":"`+base+`","model":"deepseek-v4-pro","api_key":"`+fakeKey+`"}`); code != 200 {
			t.Fatal(code, raw)
		}
		asked := false
		interceptProbe(t, func(r *http.Request) (*http.Response, error) {
			asked = true
			return probeResponse(r, 200, `{}`), nil
		})
		if accounts, known := s.LauncherBalances(context.Background()); !known || len(accounts) != 0 || asked {
			t.Fatalf("%s: known=%v accounts=%+v asked=%v", base, known, accounts, asked)
		}
	}
}

// A launcher with no key of its own may use the key one of its computers keeps (#11825: the launcher's credential
// mechanism, not a second one).
func TestLauncherBalancesUsesAComputersKey(t *testing.T) {
	s, mux := launcherTestService(t)
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
		`{"name":"deepseek","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro"}`); code != 200 {
		t.Fatal(code, raw)
	}
	sealed, err := s.sealKey(fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.putSetting(context.Background(), launchersMachineKeysKey, []machineKey{{Launcher: "deepseek", Machine: "m1", KeySealed: sealed}}); err != nil {
		t.Fatal(err)
	}
	var key string
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		key = r.Header.Get("Authorization")
		return probeResponse(r, 200, `{"balance_infos":[{"currency":"CNY","total_balance":"3.00"}]}`), nil
	})
	accounts, _ := s.LauncherBalances(context.Background())
	if len(accounts) != 1 || accounts[0].Balance == nil || accounts[0].Balance.Amount != "3.00" || key != "Bearer "+fakeKey {
		t.Fatalf("accounts=%+v key=%q", accounts, key)
	}
}

// Two launchers that hold one key are one account: one row, under the first launcher's name.
func TestLauncherBalancesDeduplicatesOneKey(t *testing.T) {
	s, mux := launcherTestService(t)
	for _, name := range []string{"deepseek", "deepseek-flash"} {
		if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
			`{"name":"`+name+`","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro","api_key":"`+fakeKey+`"}`); code != 200 {
			t.Fatal(code, raw)
		}
	}
	requests := 0
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		requests++
		return probeResponse(r, 200, `{"balance_infos":[{"currency":"CNY","total_balance":"1.00"}]}`), nil
	})
	accounts, _ := s.LauncherBalances(context.Background())
	if len(accounts) != 1 || requests != 1 {
		t.Fatalf("accounts=%+v requests=%d", accounts, requests)
	}
}

// A corrupt or unresolvable key on the first computer must not hide a usable key on the next (#11825).
func TestLauncherBalancesTriesTheNextComputersKey(t *testing.T) {
	s, mux := launcherTestService(t)
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
		`{"name":"deepseek","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro"}`); code != 200 {
		t.Fatal(code, raw)
	}
	sealed, err := s.sealKey(fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.putSetting(context.Background(), launchersMachineKeysKey, []machineKey{
		{Launcher: "deepseek", Machine: "a-first", KeySealed: "not-a-sealed-value"},
		{Launcher: "deepseek", Machine: "b-second", KeySealed: sealed},
	}); err != nil {
		t.Fatal(err)
	}
	var key string
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		key = r.Header.Get("Authorization")
		return probeResponse(r, 200, `{"balance_infos":[{"currency":"CNY","total_balance":"3.00"}]}`), nil
	})
	accounts, known := s.LauncherBalances(context.Background())
	if !known || len(accounts) != 1 || accounts[0].Balance == nil || accounts[0].Balance.Amount != "3.00" || key != "Bearer "+fakeKey {
		t.Fatalf("known=%v accounts=%+v key=%q", known, accounts, key)
	}
}

// A key that cannot be opened fails before any request, so it is a local credential problem ("failed"), never the
// provider turning the key away ("rejected") (#11825).
func TestLauncherBalancesReportsAnUnopenableKeyAsFailed(t *testing.T) {
	s, _ := launcherTestService(t)
	putLaunchers(t, s, storedLauncher{KeySealed: "not-a-sealed-value",
		ManagedLauncher: proto.ManagedLauncher{Name: "deepseek", BaseURL: "https://api.deepseek.com/anthropic"}})
	asked := false
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		asked = true
		return probeResponse(r, 200, `{"balance_infos":[{"currency":"CNY","total_balance":"1.00"}]}`), nil
	})
	accounts, known := s.LauncherBalances(context.Background())
	if !known || len(accounts) != 1 || accounts[0].Problem != "failed" || accounts[0].Balance != nil || asked {
		t.Fatalf("known=%v accounts=%+v asked=%v", known, accounts, asked)
	}
}

// Two launchers sharing one broken key are one account still: one "balance unavailable" row, not two (#11825).
func TestLauncherBalancesReportsASharedBrokenKeyOnce(t *testing.T) {
	s, _ := launcherTestService(t)
	putLaunchers(t, s,
		storedLauncher{KeySealed: "not-a-sealed-value",
			ManagedLauncher: proto.ManagedLauncher{Name: "deepseek", BaseURL: "https://api.deepseek.com/anthropic"}},
		storedLauncher{KeySealed: "not-a-sealed-value",
			ManagedLauncher: proto.ManagedLauncher{Name: "deepseek-flash", BaseURL: "https://api.deepseek.com/anthropic"}},
	)
	accounts, known := s.LauncherBalances(context.Background())
	if !known || len(accounts) != 1 || accounts[0].Problem != "failed" || accounts[0].Balance != nil {
		t.Fatalf("known=%v accounts=%+v", known, accounts)
	}
}

// keyFailStore fails one setting's read, as a database error on that row only.
type keyFailStore struct {
	Store
	fail string
}

func (s keyFailStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == s.fail {
		return false, errors.New("database unavailable")
	}
	return s.Store.Setting(ctx, key, out)
}

// A machine-key store that cannot be read is not "every computer's key is gone": the caller keeps what it had
// instead of dropping the launchers that rely on such a key (#11825).
func TestLauncherBalancesKeepsSamplesWhenTheMachineKeyStoreCannotBeRead(t *testing.T) {
	ctx := context.Background()
	s, _, st := modelService(t)
	putLaunchers(t, s, storedLauncher{
		ManagedLauncher: proto.ManagedLauncher{Name: "deepseek", BaseURL: "https://api.deepseek.com/anthropic"}})
	sealed, err := s.sealKey(fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.putSetting(ctx, launchersMachineKeysKey, []machineKey{{Launcher: "deepseek", Machine: "m1", KeySealed: sealed}}); err != nil {
		t.Fatal(err)
	}
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		return probeResponse(r, 200, `{"balance_infos":[{"currency":"CNY","total_balance":"3.00"}]}`), nil
	})
	if accounts, known := s.LauncherBalances(ctx); !known || len(accounts) != 1 || accounts[0].Balance == nil {
		t.Fatalf("first read: known=%v accounts=%+v", known, accounts)
	}

	s.store = func() Store { return keyFailStore{Store: st, fail: launchersMachineKeysKey} }
	if accounts, known := s.LauncherBalances(ctx); known || accounts != nil {
		t.Fatalf("an unreadable key store must not answer: known=%v accounts=%+v", known, accounts)
	}
}

// The balance read goes where the launcher's own calls go: through its proxy (honoring no_proxy) or through the
// computer it names, with the same no-redirect policy as a probe (#11825).
func TestBalanceClientFollowsTheLaunchersRoute(t *testing.T) {
	s, _ := launcherTestService(t)
	direct, err := s.balanceClient(storedLauncher{})
	if err != nil || direct.Timeout != balanceTimeout || direct.CheckRedirect == nil || direct.Transport != nil {
		t.Fatalf("direct: err=%v client=%+v", err, direct)
	}
	proxied, err := s.balanceClient(storedLauncher{ManagedLauncher: proto.ManagedLauncher{
		Proxy: "http://proxy.example.com:8080", NoProxy: "localhost"}})
	if err != nil {
		t.Fatalf("proxied err: %v", err)
	}
	tr, ok := proxied.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("proxied transport: %T", proxied.Transport)
	}
	req, err := http.NewRequest(http.MethodGet, "https://api.deepseek.com/user/balance", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := tr.Proxy(req); err != nil || u == nil || u.String() != "http://proxy.example.com:8080" {
		t.Fatalf("the provider's address must use the launcher's proxy: %v %v", u, err)
	}
	bypassed, err := http.NewRequest(http.MethodGet, "http://localhost:8080/user/balance", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := tr.Proxy(bypassed); err != nil || u != nil {
		t.Fatalf("no_proxy must exempt its addresses: %v %v", u, err)
	}
	via, err := s.balanceClient(storedLauncher{ViaMachine: "m1"})
	if err != nil {
		t.Fatalf("via err: %v", err)
	}
	if v, ok := via.Transport.(viaNode); !ok || v.machine != "m1" || v.s != s {
		t.Fatalf("via machine: %#v", via.Transport)
	}
}

// An invalid or hostless configured proxy must fail cleanly and never fall back to a direct HTTP request (#11825).
func TestLauncherBalancesRejectsInvalidOrHostlessProxyWithoutDirectRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy string
	}{
		{name: "unparseable url", proxy: "://bad-proxy"},
		{name: "hostless http scheme", proxy: "http://"},
		{name: "path only", proxy: "/just/a/path"},
		{name: "missing scheme hostport", proxy: "proxy.example.com:8080"},
		{name: "schemeless host", proxy: "//proxy.example.com:8080"},
		{name: "unsupported scheme", proxy: "ftp://proxy.example.com:21"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := launcherTestService(t)
			sealed, err := s.sealKey(fakeKey)
			if err != nil {
				t.Fatal(err)
			}
			l := storedLauncher{
				KeySealed: sealed,
				ManagedLauncher: proto.ManagedLauncher{
					Name:    "deepseek",
					BaseURL: "https://api.deepseek.com/anthropic",
					Proxy:   tc.proxy,
				},
			}
			if client, err := s.balanceClient(l); err == nil || client != nil {
				t.Fatalf("balanceClient(%q) must return error, got client=%+v err=%v", tc.proxy, client, err)
			}
			putLaunchers(t, s, l)
			asked := false
			interceptProbe(t, func(r *http.Request) (*http.Response, error) {
				asked = true
				return probeResponse(r, http.StatusOK, `{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}`), nil
			})
			accounts, known := s.LauncherBalances(context.Background())
			if !known || len(accounts) != 1 || accounts[0].Balance != nil || accounts[0].Problem != "failed" || asked {
				t.Fatalf("proxy %q must fail without sending a direct request: known=%v asked=%v accounts=%+v", tc.proxy, known, asked, accounts)
			}
		})
	}
}

// Rotating what a secret reference (such as op://…) resolves to changes BalanceCredential even though the stored
// sealed reference bytes stay the same, so a subsequent failed read cannot keep showing the previous key's balance
// (#11825). A temporary resolution failure of the unchanged sealed reference leaves BalanceCredential empty while
// keeping BalanceSealedCredential unchanged (so usageBalances retains the last amount as stale), whereas replacing the
// sealed reference itself changes BalanceSealedCredential even when the replacement cannot be resolved.
func TestLauncherBalancesCredentialChangesWhenReferenceRotates(t *testing.T) {
	ctx := context.Background()
	s, mux := launcherTestService(t)
	const (
		ref  = "exv://Example/deepseek/key"
		ref2 = "exv://Example/deepseek/replacement"
		key1 = "sk-deepseek-rotated-old-0001"
		key2 = "sk-deepseek-rotated-new-0002"
	)
	vault := &fakeLauncherSecrets{values: map[string]string{ref: key1}}
	s.refs = vault
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
		`{"name":"deepseek","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro","api_key_reference":"`+ref+`"}`); code != 200 {
		t.Fatal(code, raw)
	}
	storedBefore := s.storedLaunchers(ctx)
	if len(storedBefore) != 1 || storedBefore[0].KeySealed == "" {
		t.Fatalf("stored: %+v", storedBefore)
	}
	sealedBefore := storedBefore[0].KeySealed

	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer " + key1:
			return probeResponse(r, http.StatusOK, `{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}`), nil
		case "Bearer " + key2:
			return probeResponse(r, http.StatusUnauthorized, `{"error":{"message":"bad key"}}`), nil
		default:
			return probeResponse(r, http.StatusInternalServerError, `{}`), nil
		}
	})

	first, known := s.LauncherBalances(ctx)
	if !known || len(first) != 1 || first[0].Balance == nil || first[0].Balance.Amount != "12.34" || first[0].Problem != "" {
		t.Fatalf("first read: known=%v %+v", known, first)
	}
	want1 := sha256.Sum256([]byte(key1))
	if first[0].BalanceCredential != hex.EncodeToString(want1[:]) {
		t.Fatalf("first credential=%q want=%q", first[0].BalanceCredential, hex.EncodeToString(want1[:]))
	}
	if first[0].BalanceSealedCredential != sealedCredential(sealedBefore) {
		t.Fatalf("first sealed credential=%q want=%q", first[0].BalanceSealedCredential, sealedCredential(sealedBefore))
	}

	// Temporarily fail reference resolution without changing the stored sealed reference (e.g. vault locked).
	// BalanceCredential is left empty and BalanceSealedCredential stays equal to the prior sealed digest.
	vault.mu.Lock()
	delete(vault.values, ref)
	vault.mu.Unlock()
	tempBroken, known := s.LauncherBalances(ctx)
	if !known || len(tempBroken) != 1 || tempBroken[0].Balance != nil || tempBroken[0].Problem != "failed" {
		t.Fatalf("temporary broken reference read: known=%v %+v", known, tempBroken)
	}
	if tempBroken[0].BalanceCredential != "" {
		t.Fatalf("temporary broken read must leave BalanceCredential empty, got %q", tempBroken[0].BalanceCredential)
	}
	if tempBroken[0].BalanceSealedCredential != sealedCredential(sealedBefore) {
		t.Fatalf("temporary broken sealed credential=%q want=%q", tempBroken[0].BalanceSealedCredential, sealedCredential(sealedBefore))
	}

	// Rotate the secret behind the same sealed reference without touching the stored launcher.
	vault.mu.Lock()
	vault.values[ref] = key2
	vault.mu.Unlock()
	if storedAfter := s.storedLaunchers(ctx); len(storedAfter) != 1 || storedAfter[0].KeySealed != sealedBefore {
		t.Fatalf("sealed reference should be unchanged: %+v", storedAfter)
	}

	second, known := s.LauncherBalances(ctx)
	if !known || len(second) != 1 || second[0].Balance != nil || second[0].Problem != "rejected" {
		t.Fatalf("rotated read: known=%v %+v", known, second)
	}
	want2 := sha256.Sum256([]byte(key2))
	if second[0].BalanceCredential != hex.EncodeToString(want2[:]) {
		t.Fatalf("rotated credential=%q want=%q", second[0].BalanceCredential, hex.EncodeToString(want2[:]))
	}
	if second[0].BalanceSealedCredential != sealedCredential(sealedBefore) {
		t.Fatalf("rotated sealed credential=%q want=%q", second[0].BalanceSealedCredential, sealedCredential(sealedBefore))
	}
	if second[0].BalanceCredential == first[0].BalanceCredential {
		t.Fatalf("rotating a reference must change BalanceCredential: %q", second[0].BalanceCredential)
	}
	if strings.Contains(second[0].BalanceCredential, key1) || strings.Contains(second[0].BalanceCredential, key2) {
		t.Fatalf("BalanceCredential must never carry a key: %q", second[0].BalanceCredential)
	}

	// Replacing the sealed reference itself with a different reference that then fails to resolve changes
	// BalanceSealedCredential while leaving BalanceCredential empty.
	vault.mu.Lock()
	vault.values[ref2] = "sk-deepseek-replacement-0003"
	vault.mu.Unlock()
	if code, raw := launcherCall(t, mux, "PUT", "/api/v1/launchers/deepseek",
		`{"base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro","api_key_reference":"`+ref2+`"}`); code != 200 {
		t.Fatal(code, raw)
	}
	vault.mu.Lock()
	delete(vault.values, ref2)
	vault.mu.Unlock()
	storedReplaced := s.storedLaunchers(ctx)
	if len(storedReplaced) != 1 || storedReplaced[0].KeySealed == "" || storedReplaced[0].KeySealed == sealedBefore {
		t.Fatalf("replaced stored launcher: %+v", storedReplaced)
	}
	replaced, known := s.LauncherBalances(ctx)
	if !known || len(replaced) != 1 || replaced[0].Balance != nil || replaced[0].Problem != "failed" {
		t.Fatalf("replaced reference read: known=%v %+v", known, replaced)
	}
	if replaced[0].BalanceCredential != "" {
		t.Fatalf("replaced unopenable read must leave BalanceCredential empty, got %q", replaced[0].BalanceCredential)
	}
	if replaced[0].BalanceSealedCredential != sealedCredential(storedReplaced[0].KeySealed) || replaced[0].BalanceSealedCredential == first[0].BalanceSealedCredential {
		t.Fatalf("replaced sealed credential=%q want=%q (distinct from %q)", replaced[0].BalanceSealedCredential, sealedCredential(storedReplaced[0].KeySealed), first[0].BalanceSealedCredential)
	}
}

// Multiple slow launchers must not consume the whole poll budget sequentially and starve a later healthy launcher
// (#11825): deduplicated balance reads run concurrently with a bounded worker count.
func TestLauncherBalancesReadsConcurrentlySoSlowLaunchersDoNotStarveHealthyOnes(t *testing.T) {
	oldTimeout := balanceTimeout
	balanceTimeout = 200 * time.Millisecond
	t.Cleanup(func() { balanceTimeout = oldTimeout })

	s, mux := launcherTestService(t)
	for _, tc := range []struct{ name, key string }{
		{"slow-1", "sk-deepseek-slow-0001"},
		{"slow-2", "sk-deepseek-slow-0002"},
		{"healthy", "sk-deepseek-healthy-0003"},
	} {
		if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
			`{"name":"`+tc.name+`","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro","api_key":"`+tc.key+`"}`); code != 200 {
			t.Fatal(code, raw)
		}
	}

	var healthyAttempts atomic.Int32
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer sk-deepseek-healthy-0003" {
			healthyAttempts.Add(1)
			return probeResponse(r, http.StatusOK, `{"balance_infos":[{"currency":"CNY","total_balance":"42.00"}]}`), nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	// Overall budget is shorter than 2 * balanceTimeout, so two slow reads in sequence would exhaust it before
	// healthy was ever attempted.
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	accounts, known := s.LauncherBalances(ctx)
	if !known || len(accounts) != 3 {
		t.Fatalf("known=%v accounts=%+v", known, accounts)
	}
	if healthyAttempts.Load() != 1 {
		t.Fatalf("healthy launcher was not attempted: %d", healthyAttempts.Load())
	}
	if accounts[0].Name != "slow-1" || accounts[0].Problem != "unreachable" || accounts[0].Balance != nil || accounts[0].BalanceSkipped {
		t.Fatalf("slow-1: %+v", accounts[0])
	}
	if accounts[1].Name != "slow-2" || accounts[1].Problem != "unreachable" || accounts[1].Balance != nil || accounts[1].BalanceSkipped {
		t.Fatalf("slow-2: %+v", accounts[1])
	}
	if accounts[2].Name != "healthy" || accounts[2].Problem != "" || accounts[2].BalanceSkipped ||
		accounts[2].Balance == nil || accounts[2].Balance.Amount != "42.00" || accounts[2].Balance.Currency != "CNY" {
		t.Fatalf("healthy: %+v", accounts[2])
	}
}

// When the overall poll context expires before every launcher gets a full attempt, neither in-flight nor queued
// launchers are misreported as unreachable (#11825).
func TestLauncherBalancesDoesNotTurnContextExhaustionIntoUnreachable(t *testing.T) {
	oldTimeout, oldWorkers := balanceTimeout, balanceReadWorkers
	balanceTimeout = 2 * time.Second
	balanceReadWorkers = 2
	t.Cleanup(func() {
		balanceTimeout = oldTimeout
		balanceReadWorkers = oldWorkers
	})

	s, mux := launcherTestService(t)
	for _, tc := range []struct{ name, key string }{
		{"slow-1", "sk-deepseek-exhaust-0001"},
		{"slow-2", "sk-deepseek-exhaust-0002"},
		{"queued-3", "sk-deepseek-exhaust-0003"},
	} {
		if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
			`{"name":"`+tc.name+`","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro","api_key":"`+tc.key+`"}`); code != 200 {
			t.Fatal(code, raw)
		}
	}

	var queuedAttempts atomic.Int32
	interceptProbe(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer sk-deepseek-exhaust-0003" {
			queuedAttempts.Add(1)
			return probeResponse(r, http.StatusOK, `{"balance_infos":[{"currency":"CNY","total_balance":"9.99"}]}`), nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	accounts, known := s.LauncherBalances(ctx)
	if !known || len(accounts) != 3 {
		t.Fatalf("known=%v accounts=%+v", known, accounts)
	}
	if queuedAttempts.Load() != 0 {
		t.Fatalf("queued-3 should not have been attempted after context expiry: %d", queuedAttempts.Load())
	}
	for _, a := range accounts {
		if !a.BalanceSkipped || a.Problem != "" || a.Balance != nil {
			t.Fatalf("context exhaustion must mark skipped, never unreachable: %+v", a)
		}
	}
}

// Per-computer keys override the launcher's global key (matching keySealedFor): when both are configured, the
// per-computer balance is read instead of the global account's balance, while device scope (shareSelected) limits
// consideration to in-scope machines (#11825).
func TestLauncherBalancesPrefersPerComputerKeyOverGlobalKey(t *testing.T) {
	const (
		globalKey = "sk-deepseek-global-0001"
		m1Key     = "sk-deepseek-m1-0002"
		bKey      = "sk-deepseek-b-0003"
	)
	respondByKey := func(r *http.Request) (*http.Response, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer " + globalKey:
			return probeResponse(r, http.StatusOK, `{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}`), nil
		case "Bearer " + m1Key:
			return probeResponse(r, http.StatusOK, `{"balance_infos":[{"currency":"CNY","total_balance":"88.88"}]}`), nil
		case "Bearer " + bKey:
			return probeResponse(r, http.StatusOK, `{"balance_infos":[{"currency":"CNY","total_balance":"66.66"}]}`), nil
		default:
			return probeResponse(r, http.StatusUnauthorized, `{}`), nil
		}
	}

	t.Run("per-computer override beats global key", func(t *testing.T) {
		s, mux := launcherTestService(t)
		if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
			`{"name":"deepseek","base_url":"https://api.deepseek.com/anthropic","model":"deepseek-v4-pro","api_key":"`+globalKey+`"}`); code != 200 {
			t.Fatal(code, raw)
		}
		sealedM1, err := s.sealKey(m1Key)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.putSetting(context.Background(), launchersMachineKeysKey, []machineKey{
			{Launcher: "deepseek", Machine: "m1", KeySealed: sealedM1},
		}); err != nil {
			t.Fatal(err)
		}
		var gotAuth string
		interceptProbe(t, func(r *http.Request) (*http.Response, error) {
			gotAuth = r.Header.Get("Authorization")
			return respondByKey(r)
		})
		accounts, known := s.LauncherBalances(context.Background())
		if !known || len(accounts) != 1 || accounts[0].Balance == nil || accounts[0].Balance.Amount != "88.88" || gotAuth != "Bearer "+m1Key {
			t.Fatalf("per-computer key must override global key: known=%v auth=%q accounts=%+v", known, gotAuth, accounts)
		}
	})

	t.Run("shareSelected ignores out-of-scope machine keys and prefers in-scope overrides", func(t *testing.T) {
		s, _ := launcherTestService(t)
		sealedGlobal, err := s.sealKey(globalKey)
		if err != nil {
			t.Fatal(err)
		}
		sealedM1, err := s.sealKey(m1Key)
		if err != nil {
			t.Fatal(err)
		}
		sealedB, err := s.sealKey(bKey)
		if err != nil {
			t.Fatal(err)
		}
		putLaunchers(t, s, storedLauncher{
			KeySealed:       sealedGlobal,
			Share:           shareSelected,
			ScopeMachines:   []string{"m2"},
			ManagedLauncher: proto.ManagedLauncher{Name: "deepseek", BaseURL: "https://api.deepseek.com/anthropic"},
		})
		if err := s.putSetting(context.Background(), launchersMachineKeysKey, []machineKey{
			{Launcher: "deepseek", Machine: "m1", KeySealed: sealedM1},
			{Launcher: "deepseek", Machine: "b-override", KeySealed: sealedB},
		}); err != nil {
			t.Fatal(err)
		}
		interceptProbe(t, respondByKey)
		// m2 has no machine key and m1/b-override are out of scope -> falls back to globalKey (12.34).
		accounts, known := s.LauncherBalances(context.Background())
		if !known || len(accounts) != 1 || accounts[0].Balance == nil || accounts[0].Balance.Amount != "12.34" {
			t.Fatalf("in-scope machine without override must use global fallback: known=%v accounts=%+v", known, accounts)
		}

		// Adding b-override alongside a-default in ScopeMachines must still prefer b-override's per-computer key (66.66).
		putLaunchers(t, s, storedLauncher{
			KeySealed:       sealedGlobal,
			Share:           shareSelected,
			ScopeMachines:   []string{"a-default", "b-override"},
			ManagedLauncher: proto.ManagedLauncher{Name: "deepseek", BaseURL: "https://api.deepseek.com/anthropic"},
		})
		accounts, known = s.LauncherBalances(context.Background())
		if !known || len(accounts) != 1 || accounts[0].Balance == nil || accounts[0].Balance.Amount != "66.66" {
			t.Fatalf("in-scope per-computer override must beat global fallback: known=%v accounts=%+v", known, accounts)
		}
	})
}

// When ViaMachine is set, LauncherBalances uses keySealedFor(l, machineKeys, l.ViaMachine) so the routed computer's
// own key overrides both the global key and other computers' keys, falls back to the global key only when the routed
// computer has no override, and never leaks the global account's balance when the routed computer's override fails to
// open (#11825).
func TestLauncherBalancesViaMachineUsesKeySealedForPrecedence(t *testing.T) {
	const (
		globalKey = "sk-deepseek-via-global-0001"
		otherKey  = "sk-deepseek-via-other-0002"
		serverKey = "sk-deepseek-via-server-0003"
	)
	respondByKey := func(r *http.Request) (*http.Response, error) {
		switch r.Header.Get("Authorization") {
		case "Bearer " + globalKey:
			return probeResponse(r, http.StatusOK, `{"balance_infos":[{"currency":"CNY","total_balance":"12.34"}]}`), nil
		case "Bearer " + otherKey:
			return probeResponse(r, http.StatusOK, `{"balance_infos":[{"currency":"CNY","total_balance":"55.55"}]}`), nil
		case "Bearer " + serverKey:
			return probeResponse(r, http.StatusOK, `{"balance_infos":[{"currency":"CNY","total_balance":"77.77"}]}`), nil
		default:
			return probeResponse(r, http.StatusUnauthorized, `{}`), nil
		}
	}

	t.Run("routed machine per-computer key overrides global and other machines", func(t *testing.T) {
		s, _ := launcherTestService(t)
		devs := &viaDevs{}
		s.devs = devs
		sealedGlobal, _ := s.sealKey(globalKey)
		sealedOther, _ := s.sealKey(otherKey)
		sealedServer, _ := s.sealKey(serverKey)
		putLaunchers(t, s, storedLauncher{
			KeySealed:       sealedGlobal,
			ViaMachine:      "server",
			ManagedLauncher: proto.ManagedLauncher{Name: "deepseek", BaseURL: "https://api.deepseek.com/anthropic"},
		})
		if err := s.putSetting(context.Background(), launchersMachineKeysKey, []machineKey{
			{Launcher: "deepseek", Machine: "a-other", KeySealed: sealedOther},
			{Launcher: "deepseek", Machine: "server", KeySealed: sealedServer},
		}); err != nil {
			t.Fatal(err)
		}
		interceptProbe(t, respondByKey)
		accounts, known := s.LauncherBalances(context.Background())
		if !known || len(accounts) != 1 || accounts[0].Balance == nil || accounts[0].Balance.Amount != "77.77" {
			t.Fatalf("ViaMachine override balance: known=%v accounts=%+v", known, accounts)
		}
		if devs.calls.Load() != 1 || http.Header(devs.saw.Header).Get("Authorization") != "Bearer "+serverKey {
			t.Fatalf("expected routed read with server key: calls=%d auth=%q", devs.calls.Load(), http.Header(devs.saw.Header).Get("Authorization"))
		}
	})

	t.Run("routed machine without override falls back to global key rather than another machine", func(t *testing.T) {
		s, _ := launcherTestService(t)
		devs := &viaDevs{}
		s.devs = devs
		sealedGlobal, _ := s.sealKey(globalKey)
		sealedOther, _ := s.sealKey(otherKey)
		putLaunchers(t, s, storedLauncher{
			KeySealed:       sealedGlobal,
			ViaMachine:      "server",
			ManagedLauncher: proto.ManagedLauncher{Name: "deepseek", BaseURL: "https://api.deepseek.com/anthropic"},
		})
		if err := s.putSetting(context.Background(), launchersMachineKeysKey, []machineKey{
			{Launcher: "deepseek", Machine: "a-other", KeySealed: sealedOther},
		}); err != nil {
			t.Fatal(err)
		}
		interceptProbe(t, respondByKey)
		accounts, known := s.LauncherBalances(context.Background())
		if !known || len(accounts) != 1 || accounts[0].Balance == nil || accounts[0].Balance.Amount != "12.34" {
			t.Fatalf("ViaMachine global fallback balance: known=%v accounts=%+v", known, accounts)
		}
		if devs.calls.Load() != 1 || http.Header(devs.saw.Header).Get("Authorization") != "Bearer "+globalKey {
			t.Fatalf("expected routed read with global fallback key: calls=%d auth=%q", devs.calls.Load(), http.Header(devs.saw.Header).Get("Authorization"))
		}
	})

	t.Run("broken routed machine override reports failed instead of leaking global balance", func(t *testing.T) {
		s, _ := launcherTestService(t)
		devs := &viaDevs{}
		s.devs = devs
		sealedGlobal, _ := s.sealKey(globalKey)
		putLaunchers(t, s, storedLauncher{
			KeySealed:       sealedGlobal,
			ViaMachine:      "server",
			ManagedLauncher: proto.ManagedLauncher{Name: "deepseek", BaseURL: "https://api.deepseek.com/anthropic"},
		})
		if err := s.putSetting(context.Background(), launchersMachineKeysKey, []machineKey{
			{Launcher: "deepseek", Machine: "server", KeySealed: "not-a-sealed-value"},
		}); err != nil {
			t.Fatal(err)
		}
		interceptProbe(t, respondByKey)
		accounts, known := s.LauncherBalances(context.Background())
		if !known || len(accounts) != 1 || accounts[0].Balance != nil || accounts[0].Problem != "failed" || devs.calls.Load() != 0 {
			t.Fatalf("broken ViaMachine override must not fall back to global key: known=%v calls=%d accounts=%+v", known, devs.calls.Load(), accounts)
		}
	})

	t.Run("routed machine with no own or global key is skipped instead of using another machine's key", func(t *testing.T) {
		s, _ := launcherTestService(t)
		devs := &viaDevs{}
		s.devs = devs
		sealedOther, _ := s.sealKey(otherKey)
		putLaunchers(t, s, storedLauncher{
			ViaMachine:      "server",
			ManagedLauncher: proto.ManagedLauncher{Name: "deepseek", BaseURL: "https://api.deepseek.com/anthropic"},
		})
		if err := s.putSetting(context.Background(), launchersMachineKeysKey, []machineKey{
			{Launcher: "deepseek", Machine: "a-other", KeySealed: sealedOther},
		}); err != nil {
			t.Fatal(err)
		}
		asked := false
		interceptProbe(t, func(r *http.Request) (*http.Response, error) {
			asked = true
			return respondByKey(r)
		})
		accounts, known := s.LauncherBalances(context.Background())
		if !known || len(accounts) != 0 || asked || devs.calls.Load() != 0 {
			t.Fatalf("ViaMachine with no effective key must be skipped: known=%v asked=%v calls=%d accounts=%+v", known, asked, devs.calls.Load(), accounts)
		}
	})
}
