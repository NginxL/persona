package usagesource

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// keyDoc is a relay's key self-service usage document as an ordinary key reads it (#12311): the shapes a real relay
// sent, with made-up ids and numbers. The key's own request counts are in the real one too and are not read.
const keyDoc = `{"service":"relay","protocol":"self-usage","protocol_version":1,"api_key":"team-key","generated_at":"2026-11-05T08:59:30Z",
 "today":{"requests":12,"billed_tokens":3400,"cost_usd":1.5},
 "account_quota_shared":true,"account_scope":"models","upstream":[],
 "accounts":[
  {"account_id":"acct-b","provider":"claude","plan":"pro","state":"available","available_at":null,"key_ceiling_percent":0,"cached":true,"current":false,
   "checked_at":"2026-11-05T08:40:00Z","windows":[
    {"account_id":"acct-b","kind":"five_hour","scope":"account","used_ratio":0.46,"source":"usage_header","checked_at":"2026-11-05T08:40:00Z"},
    {"account_id":"acct-b","kind":"weekly","scope":"account","used_ratio":0.47,"resets_at":"2026-11-10T04:00:00Z","source":"usage_header","checked_at":"2026-11-05T08:40:00Z"}]},
  {"account_id":"acct-a","provider":"claude","plan":"max","state":"cooldown","available_at":"2026-11-05T10:00:00Z","cached":true,
   "checked_at":"2026-11-05T08:55:00.123456789Z","windows":[
    {"account_id":"acct-a","kind":"five_hour","scope":"account","used_ratio":0.2,"resets_at":"2026-11-05T12:50:00Z","source":"usage_header","checked_at":"2026-11-05T08:55:00.123456789Z"},
    {"account_id":"acct-a","kind":"weekly","scope":"account","used_ratio":0.99,"resets_at":"2026-11-07T18:00:00Z","source":"usage_header","checked_at":"2026-11-05T08:55:00Z"},
    {"account_id":"acct-a","kind":"weekly_fable","scope":"model","scoped_model":"Fable","applies_to_models":["claude-fable-5-2"],"used_ratio":0.12,"resets_at":"2026-11-07T18:00:00Z","source":"oauth_usage","checked_at":"2026-11-04T07:39:00Z"}]},
  {"account_id":"acct-c","provider":"codex","plan":"pro","state":"available","cached":true,"checked_at":"2026-11-05T08:44:00Z","windows":[
    {"account_id":"acct-c","kind":"daily","used_ratio":0,"resets_at":"2026-11-01T12:17:00Z","source":"usage_header","checked_at":"2026-10-31T12:17:00Z"},
    {"account_id":"acct-c","kind":"weekly","used_ratio":0.04,"resets_at":"2026-11-09T00:02:00Z","source":"wham_usage","checked_at":"2026-11-05T08:44:00Z"}]},
  {"account_id":"acct-g","provider":"antigravity","plan":"ultra","state":"available","cached":true,"checked_at":"2026-11-05T08:40:00Z","windows":[
    {"account_id":"acct-g","kind":"gemini_five_hour","scope":"model","scoped_model":"gemini","used_ratio":0.02,"resets_at":"2026-11-05T12:33:00Z","source":"antigravity","checked_at":"2026-11-05T08:40:00Z"},
    {"account_id":"acct-g","kind":"partner_weekly","scope":"model","scoped_model":"partner","used_ratio":0,"resets_at":"2026-11-12T05:40:00Z","source":"antigravity","checked_at":"2026-11-05T08:40:00Z"}]},
  {"account_id":"acct-d","provider":"deepseek","state":"available","cached":true,"checked_at":"2026-11-05T08:39:00Z","windows":[
    {"account_id":"acct-d","kind":"balance_cny","scope":"account","remaining":52.08,"source":"deepseek_balance","checked_at":"2026-11-05T08:39:00Z"}]},
  {"account_id":"acct-p","provider":"passthrough","state":"available","cached":true,"checked_at":null,"windows":[]},
  {"account_id":"acct-x","state":"unavailable","windows":[
    {"account_id":"acct-x","kind":"weekly","used_ratio":0.5,"resets_at":"2026-11-09T00:00:00Z","checked_at":"2026-11-05T08:00:00Z"}]},
  {"account_id":"acct-a","provider":"claude","plan":"max","windows":[
    {"account_id":"acct-a","kind":"five_hour","used_ratio":0.7,"resets_at":"2026-11-05T12:50:00Z","checked_at":"2026-11-05T08:58:00Z"}]}]}`

// keyRelay serves the document at /me/usage for the right key, and logs every request.
type keyRelay struct {
	mu    sync.Mutex
	calls []*http.Request
	body  string
	srv   *httptest.Server
}

func newKeyRelay(t *testing.T, body string) *keyRelay {
	k := &keyRelay{body: body}
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.calls = append(k.calls, r.Clone(context.Background()))
		k.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"invalid api key"}}`)
			return
		}
		if r.Method != http.MethodGet || (r.URL.Path != "/me/usage" && r.URL.Path != "/gw/me/usage") || r.URL.RawQuery != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		k.mu.Lock()
		body := k.body
		k.mu.Unlock()
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(k.srv.Close)
	return k
}

func (k *keyRelay) answer(body string) {
	k.mu.Lock()
	k.body = body
	k.mu.Unlock()
}

func (k *keyRelay) requests() []*http.Request {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]*http.Request(nil), k.calls...)
}

func TestRelayKeyMapsTheAccountsTheKeyCanUse(t *testing.T) {
	k := newKeyRelay(t, keyDoc)
	res, err := Fetch(context.Background(), Config{Type: TypeRelayKey, URL: k.srv.URL, Key: testKey}, now)
	if err != nil {
		t.Fatal(err)
	}
	calls := k.requests()
	if len(calls) != 1 || calls[0].Method != http.MethodGet || calls[0].URL.Path != "/me/usage" || calls[0].URL.RawQuery != "" {
		t.Fatalf("one plain GET /me/usage, got %d: %+v", len(calls), calls)
	}
	if got := calls[0].Header.Get("Authorization"); got != "Bearer "+testKey {
		t.Errorf("authorization %q", got)
	}
	// claude max, claude pro, codex pro, antigravity ultra (by ref); the balance-only, window-less, provider-less
	// accounts are listed but not drawn, and the repeated acct-a is the first listing
	if len(res.Accounts) != 4 {
		t.Fatalf("accounts: %+v", res.Accounts)
	}
	max, pro, codex, ag := res.Accounts[0], res.Accounts[1], res.Accounts[2], res.Accounts[3]
	if max.Ref != "acct-a" || max.Provider != "claude" || max.Name != "Claude Max" || max.Plan != "max" || max.Stale || max.Problem != "" {
		t.Errorf("claude max: %+v", max)
	}
	if len(max.Windows) != 3 || max.Windows[0].Label != "5h" || max.Windows[0].UsedPercent != 20 || max.Windows[1].Label != "7d" ||
		max.Windows[1].UsedPercent != 99 || max.Windows[1].Model != "" || max.Windows[2].Model != "Fable" || max.Windows[2].UsedPercent != 12 {
		t.Errorf("claude max windows: %+v", max.Windows)
	}
	if want := time.Date(2026, 11, 5, 12, 50, 0, 0, time.UTC); max.Windows[0].ResetsAt == nil || !max.Windows[0].ResetsAt.Equal(want) {
		t.Errorf("reset %v", max.Windows[0].ResetsAt)
	}
	// the sample time is the newest observation (fractional seconds kept), not the time the document was made
	if want := time.Date(2026, 11, 5, 8, 55, 0, 123456789, time.UTC); !max.SampledAt.Equal(want) {
		t.Errorf("sampled %v want %v", max.SampledAt, want)
	}
	if pro.Ref != "acct-b" || pro.Name != "Claude Pro" || len(pro.Windows) != 2 || pro.Windows[0].ResetsAt != nil || pro.Windows[0].UsedPercent != 46 {
		t.Errorf("claude pro: %+v", pro)
	}
	// the daily window is kept here: the merge drops windows past their reset, as for every source
	if codex.Ref != "acct-c" || codex.Name != "Codex Pro" || len(codex.Windows) != 2 || codex.Windows[0].Label != "1d" || codex.Windows[1].UsedPercent != 4 {
		t.Errorf("codex: %+v", codex)
	}
	if ag.Ref != "acct-g" || ag.Name != "Antigravity Ultra" || len(ag.Windows) != 2 || ag.Windows[0].Label != "5h" || ag.Windows[0].Model != "gemini" ||
		ag.Windows[1].Label != "7d" || ag.Windows[1].Model != "partner" {
		t.Errorf("antigravity: %+v", ag)
	}
	if res.Seen.Listed != 7 || res.Seen.Accounts != 4 || res.Seen.Windows != 9 || res.Seen.QuotaHidden ||
		res.Seen.Providers["claude"] != 2 || res.Seen.Providers["codex"] != 1 || res.Seen.Providers["antigravity"] != 1 || len(res.Seen.Providers) != 3 {
		t.Errorf("seen: %+v", res.Seen)
	}
	if res.Inventory != nil || res.Complete {
		t.Errorf("a key sees no pool inventory: %+v %v", res.Inventory, res.Complete)
	}
}

// A launcher's base URL ends in /v1; the document is at the relay's root, under any path prefix it sits behind.
func TestRelayKeyTakesALaunchersBaseURL(t *testing.T) {
	k := newKeyRelay(t, keyDoc)
	for _, url := range []string{k.srv.URL + "/v1", k.srv.URL + "/v1/", k.srv.URL + "/gw/v1"} {
		if _, err := Fetch(context.Background(), Config{Type: TypeRelayKey, URL: url, Key: testKey}, now); err != nil {
			t.Errorf("%s: %v", url, err)
		}
	}
	calls := k.requests()
	if len(calls) != 3 || calls[0].URL.Path != "/me/usage" || calls[1].URL.Path != "/me/usage" || calls[2].URL.Path != "/gw/me/usage" {
		t.Errorf("paths: %v", calls)
	}
}

// A key the relay shares no account quota with reads fine and lists nothing; the test of the connection says why.
func TestRelayKeyWithoutSharedQuota(t *testing.T) {
	k := newKeyRelay(t, `{"service":"relay","account_quota_shared":false,"accounts":[],"today":{"requests":3}}`)
	res, err := Fetch(context.Background(), Config{Type: TypeRelayKey, URL: k.srv.URL, Key: testKey}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Seen.QuotaHidden || len(res.Accounts) != 0 || res.Seen.Listed != 0 {
		t.Errorf("%+v", res)
	}
	// an older relay does not say: its accounts are shared
	k.answer(`{"accounts":[{"account_id":"acct-a","provider":"claude","windows":[{"kind":"weekly","used_ratio":0.3,"checked_at":"2026-11-05T08:00:00Z"}]}]}`)
	res, err = Fetch(context.Background(), Config{Type: TypeRelayKey, URL: k.srv.URL, Key: testKey}, now)
	if err != nil || res.Seen.QuotaHidden || len(res.Accounts) != 1 || res.Accounts[0].Name != "Claude" {
		t.Errorf("%+v %v", res, err)
	}
}

func TestRelayKeyErrors(t *testing.T) {
	k := newKeyRelay(t, keyDoc)
	if _, err := Fetch(context.Background(), Config{Type: TypeRelayKey, URL: k.srv.URL, Key: "a-management-key-instead"}, now); CodeOf(err) != CodeUnauthorized {
		t.Errorf("wrong key: %v", err)
	}
	for body, code := range map[string]string{
		`{}`:                        CodeBadResponse, // not this kind of document
		`{"accounts":null}`:         CodeBadResponse,
		`<html>console</html>`:      CodeBadResponse,
		`{"accounts":"many"}`:       CodeBadResponse,
		`{"accounts":[{"id":"x"}]}`: "", // a list without anything to draw is no error
	} {
		k.answer(body)
		got := ""
		if _, err := Fetch(context.Background(), Config{Type: TypeRelayKey, URL: k.srv.URL, Key: testKey}, now); err != nil {
			got = CodeOf(err)
		}
		if got != code {
			t.Errorf("%s: code %q, want %q", body, got, code)
		}
	}
	// a gateway without the document (an older relay, or another service)
	if _, err := Fetch(context.Background(), Config{Type: TypeRelayKey, URL: k.srv.URL + "/elsewhere", Key: testKey}, now); CodeOf(err) != CodeNotFound {
		t.Errorf("no document: %v", err)
	}
}

func TestStoredTypeKeepsTheKnownTypes(t *testing.T) {
	for in, want := range map[string]string{"": "", "relay": "relay", "relay_key": "relay_key", "generic": "generic", "an-earlier-name": "relay"} {
		if got := StoredType(in); got != want {
			t.Errorf("StoredType(%q) = %q, want %q", in, got, want)
		}
	}
	if Known("") || Known("an-earlier-name") || !Known(TypeRelayKey) {
		t.Error("Known")
	}
}

func TestKeyAccountName(t *testing.T) {
	for _, c := range []struct{ provider, plan, want string }{
		{"claude", "max", "Claude Max"}, {"codex", "", "Codex"}, {"antigravity", " ultra ", "Antigravity Ultra"}, {"kimi", "Allegretto", "Kimi Allegretto"},
	} {
		if got := keyAccountName(c.provider, c.plan); got != c.want {
			t.Errorf("keyAccountName(%q, %q) = %q, want %q", c.provider, c.plan, got, c.want)
		}
	}
}
