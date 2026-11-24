package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/botport"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

type configureTransport func(*http.Request) (*http.Response, error)

func (f configureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type configureStore struct {
	*botStore
	writes      atomic.Int64
	rejectWrite atomic.Int64
}

func (s *configureStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == botSettingsKey {
		n := s.writes.Add(1)
		if s.rejectWrite.Load() == n {
			return errors.New("raw-sensitive-store-detail")
		}
	}
	return s.botStore.PutSetting(ctx, key, value)
}

type configurePlatform struct {
	mu           sync.Mutex
	requests     int
	code         int
	publishCode  int
	reject       bool
	beforeConfig func()
	beforeInfo   func()
}

func configureWorld(t *testing.T, code int) (*botWorld, *configureStore, *configurePlatform) {
	t.Helper()
	h := newTestManager(t)
	st := &configureStore{botStore: newBotStore()}
	h.store = st
	f := &configurePlatform{code: code}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A Persona node on the machine running these tests probes this port too (GET /, node/ports.go): answered 404,
		// it is neither a platform request (configurePoll compares their count) nor an unexpected one.
		if ignoreProbe(w, r) {
			return
		}
		f.mu.Lock()
		f.requests++
		code, publishCode, reject, before, beforeInfo := f.code, f.publishCode, f.reject, f.beforeConfig, f.beforeInfo
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			if reject {
				fmt.Fprint(w, `{"code":10003,"msg":"invalid credentials"}`)
			} else {
				fmt.Fprint(w, `{"code":0,"tenant_access_token":"fake-token","expire":7200}`)
			}
		case r.URL.Path == "/open-apis/bot/v3/info":
			if beforeInfo != nil {
				beforeInfo()
			}
			fmt.Fprint(w, `{"code":0,"bot":{"app_name":"Example","open_id":"ou_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/config") && r.Method == "PATCH":
			if before != nil {
				before()
			}
			fmt.Fprintf(w, `{"code":%d,"msg":"raw-sensitive-platform-detail"}`, code)
		case strings.HasSuffix(r.URL.Path, "/publish") && r.Method == "POST":
			fmt.Fprintf(w, `{"code":%d,"msg":"raw-sensitive-platform-detail"}`, publishCode)
		case strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/users/"):
			fmt.Fprint(w, `{"code":0,"data":{"user":{"name":"Example"}}}`)
		default:
			t.Errorf("unexpected platform request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	original := http.DefaultTransport
	http.DefaultTransport = configureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "open.feishu.cn" && r.URL.Host != strings.TrimPrefix(botcard.DomainLark, "https://") && r.URL.Host != "private.example" {
			return nil, fmt.Errorf("unexpected outbound host")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		clone.URL = &u
		clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
		return original.RoundTrip(clone)
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	mux := http.NewServeMux()
	h.botRoutes(mux)
	return &botWorld{h: h, mux: mux}, st, f
}

// The fake platform answers a local node's port probe 404 without counting it or failing the test: the probe landed
// on it during TestFeishuConfigureDiagnosticLifecycle/rejected_replacement in about 1 run in 15 on a machine with a
// node, as "unexpected platform request: GET /".
func TestConfigurePlatformIgnoresPortProbes(t *testing.T) {
	needsLarkModule(t)
	_, _, f := configureWorld(t, 230002)
	req, err := http.NewRequest(http.MethodGet, "https://open.feishu.cn/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "persona-ports") // node.portProbeAgent
	req.Header.Set("Accept", "text/html")
	res, err := http.DefaultClient.Do(req) // configureWorld routes it to the fake platform
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	f.mu.Lock()
	requests := f.requests
	f.mu.Unlock()
	if res.StatusCode != http.StatusNotFound || requests != 0 {
		t.Fatalf("a port probe: HTTP %d, counted %d platform requests", res.StatusCode, requests)
	}
}

func configureCredentials(t *testing.T, w *botWorld, id, secret, domain string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"step": "credentials", "values": map[string]string{"app_id": id, "app_secret": secret, "domain": domain}})
	status, out := w.do(t, "POST", "/api/v1/channels/feishu/bind", string(body))
	if status != 200 {
		t.Fatalf("credentials: %d %v", status, out)
	}
	return out
}

func configurePoll(t *testing.T, w *botWorld, st *configureStore, f *configurePlatform) map[string]any {
	t.Helper()
	f.mu.Lock()
	beforeRequests := f.requests
	f.mu.Unlock()
	beforeWrites := st.writes.Load()
	next := w.bind(t, "pair")
	f.mu.Lock()
	afterRequests := f.requests
	f.mu.Unlock()
	if beforeRequests != afterRequests || beforeWrites != st.writes.Load() {
		t.Fatalf("poll repeated side effects: HTTP %d -> %d, writes %d -> %d", beforeRequests, afterRequests, beforeWrites, st.writes.Load())
	}
	if strings.Contains(fmt.Sprint(next), "raw-sensitive") || strings.Contains(fmt.Sprint(next), "fake-secret") {
		t.Fatal("raw details reached client")
	}
	return next
}

// Exercise the Hub's real channel bind route, keeping both official cloud URLs and every HTTP request on a fake platform.
func TestFeishuConfigureDiagnosticSurvivesPairPoll(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name        string
		code        int
		publishCode int
		issue       botcard.ConfigureIssue
	}{
		{"missing_scope", 230002, 0, botcard.IssuePatchScope},
		{"bot_capability", 210041, 0, botcard.IssueBotCapability},
		{"not_self_built", 210001, 0, botcard.IssueNotSelfBuilt},
		{"incompatible", 210031, 0, botcard.IssueIncompatible},
		{"unreachable", 99991, 0, botcard.IssueUnreachable},
		{"publish_failure", 0, 230002, botcard.IssuePatchScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, st, f := configureWorld(t, tc.code)
			f.publishCode = tc.publishCode
			first := configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
			if strings.Contains(fmt.Sprint(first), "raw-sensitive") || strings.Contains(fmt.Sprint(first), "fake-secret") {
				t.Fatal("raw details reached initial client response")
			}
			want := feishuConfigureNote(i18n.ZhHans, botcard.ConfigureReport{Issue: tc.issue}, "feishu", "cli_example")
			if first["step"] != "pair" || !strings.Contains(fmt.Sprint(first["hint"]), want) {
				t.Fatalf("first response has no expected diagnostic: %v want=%s", first, want)
			}
			for i := 0; i < 3; i++ {
				next := configurePoll(t, w, st, f)
				if next["hint"] != first["hint"] || next["user_code"] != first["user_code"] {
					t.Fatalf("pair poll replaced diagnostic: first=%v next=%v", first, next)
				}
			}
			for _, lang := range []i18n.Lang{i18n.En, i18n.Ja} {
				w.h.opts.Lang = func() i18n.Lang { return lang }
				p := configurePoll(t, w, st, f)
				want := feishuConfigureNote(lang, botcard.ConfigureReport{Issue: tc.issue}, "feishu", "cli_example")
				if !strings.Contains(fmt.Sprint(p["hint"]), want) {
					t.Fatalf("poll did not localize retained report: %v", p)
				}
			}
			var saved map[string]any
			_, _ = st.Setting(context.Background(), botSettingsKey, &saved)
			raw, _ := json.Marshal(saved)
			if strings.Contains(string(raw), "raw-sensitive") {
				t.Fatal("raw platform error persisted")
			}
		})
	}
}

func TestFeishuConfigureDiagnosticLifecycle(t *testing.T) {
	needsLarkModule(t)
	t.Run("renewal_and_reload", func(t *testing.T) {
		w, st, f := configureWorld(t, 230002)
		first := configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
		w.h.bots.mu.Lock()
		expires := w.h.bots.set.Pairing.Expires
		w.h.bots.now = func() time.Time { return expires.Add(time.Second) }
		w.h.bots.mu.Unlock()
		renewed := w.bind(t, "pair")
		if renewed["hint"] != first["hint"] {
			t.Fatalf("renewal lost diagnostic: %v", renewed)
		}
		configurePoll(t, w, st, f)
		// A fresh manager loads the saved record and opens it with the same secret key.
		h := newTestManager(t)
		h.secret = w.h.secret
		h.store = st
		mux := http.NewServeMux()
		h.botRoutes(mux)
		reloaded := &botWorld{h: h, mux: mux}
		// Move this manager's clock to the renewed code's lifetime.
		h.bots.now = w.h.bots.now
		p := configurePoll(t, reloaded, st, f)
		if p["hint"] != first["hint"] {
			t.Fatalf("reload lost diagnostic: %v", p)
		}
	})
	t.Run("switch_features_and_unchanged_credentials", func(t *testing.T) {
		w, st, f := configureWorld(t, 230002)
		first := configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
		for _, body := range []string{`{"features":{"cards":false}}`, `{"enabled":false}`, `{"enabled":true}`, `{"app_id":"cli_example","app_secret":"fake-secret","domain":"feishu"}`} {
			status, out := w.do(t, "PUT", "/api/v1/bots/feishu", body)
			if status != 200 {
				t.Fatalf("settings: %d %v", status, out)
			}
		}
		if p := configurePoll(t, w, st, f); p["hint"] != first["hint"] {
			t.Fatalf("unrelated settings cleared diagnostic: %v", p)
		}
	})
	for _, tc := range []struct{ name, body string }{
		{"app", `{"app_id":"cli_replacement","app_secret":"replacement-secret","domain":"feishu"}`},
		{"secret", `{"app_secret":"replacement-secret"}`},
		{"domain", `{"domain":"lark"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, st, f := configureWorld(t, 230002)
			configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
			status, out := w.do(t, "PUT", "/api/v1/bots/feishu", tc.body)
			if status != 200 {
				t.Fatalf("replacement: %d %v", status, out)
			}
			p := w.bind(t, "pair")
			if p["hint"] != i18n.T(i18n.ZhHans, "hub-channels.pair_hint") {
				t.Fatalf("old app diagnostic survived replacement: %v", p)
			}
			configurePoll(t, w, st, f)
		})
	}
	t.Run("rejected_replacement", func(t *testing.T) {
		w, st, f := configureWorld(t, 230002)
		first := configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
		f.mu.Lock()
		f.reject = true
		f.mu.Unlock()
		rejected := configureCredentials(t, w, "cli_other", "replacement-secret", "feishu")
		if rejected["step"] != "credentials" || rejected["error"] == nil || strings.Contains(fmt.Sprint(rejected), "raw-sensitive") {
			t.Fatalf("invalid credential response: %v", rejected)
		}
		if p := configurePoll(t, w, st, f); p["hint"] != first["hint"] {
			t.Fatalf("failed replacement cleared diagnostic: %v", p)
		}
	})
	t.Run("corrected_configuration", func(t *testing.T) {
		w, st, f := configureWorld(t, 230002)
		configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
		f.mu.Lock()
		f.code = 0
		f.mu.Unlock()
		p := configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
		want := i18n.T(i18n.ZhHans, "hub-channels.auto_configured")
		if !strings.Contains(fmt.Sprint(p["hint"]), want) || strings.Contains(fmt.Sprint(p["hint"]), "/app/cli_example") {
			t.Fatalf("corrected configuration kept old guidance: %v", p)
		}
		if p := configurePoll(t, w, st, f); !strings.Contains(fmt.Sprint(p["hint"]), want) {
			t.Fatalf("corrected result not retained: %v", p)
		}
	})
	t.Run("private_deployment", func(t *testing.T) {
		w, st, f := configureWorld(t, 230002)
		p := configureCredentials(t, w, "cli_example", "fake-secret", "https://private.example")
		if p["hint"] != i18n.T(i18n.ZhHans, "hub-channels.pair_hint") {
			t.Fatalf("private deployment acquired diagnostic: %v", p)
		}
		configurePoll(t, w, st, f)
	})
	t.Run("paired_and_unpaired", func(t *testing.T) {
		w, st, f := configureWorld(t, 230002)
		withAssistant(w.h)
		p := configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
		w.h.botPair(botport.New("cli_example", "fake-secret", "feishu"), "ou_owner", "oc_owner", p["user_code"].(string))
		if p := configurePoll(t, w, st, f); p["done"] != true {
			t.Fatalf("not paired: %v", p)
		}
		var saved FeishuBotSettings
		_, _ = st.Setting(context.Background(), botSettingsKey, &saved)
		if saved.Configure != nil {
			t.Fatal("pair success kept diagnostic")
		}
		status, out := w.do(t, "DELETE", "/api/v1/bots/feishu/owner", "")
		if status != 200 {
			t.Fatalf("unbind: %d %v", status, out)
		}
		p = w.bind(t, "pair")
		if p["hint"] != i18n.T(i18n.ZhHans, "hub-channels.pair_hint") {
			t.Fatalf("unpair resurrected diagnostic: %v", p)
		}
	})
}

// No response from an older credentials save may supply a diagnosis for the current save, even after A -> B -> A.
func TestFeishuConfigureDiagnosticIgnoresLateResult(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name   string
		bodies []string
	}{
		{"new_app", []string{`{"app_id":"cli_replacement","app_secret":"replacement-secret"}`}},
		{"new_secret", []string{`{"app_secret":"replacement-secret"}`}},
		{"new_domain", []string{`{"domain":"lark"}`}},
		{"same_credentials_resaved", []string{`{"app_id":"cli_example","app_secret":"fake-secret","domain":"feishu"}`}},
		{"original_app_restored", []string{`{"app_id":"cli_replacement","app_secret":"replacement-secret"}`, `{"app_id":"cli_example","app_secret":"fake-secret","domain":"feishu"}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, st, f := configureWorld(t, 230002)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			f.mu.Lock()
			f.beforeConfig = func() {
				close(entered)
				select {
				case <-release:
				case <-time.After(10 * time.Second):
					t.Error("timed out waiting for configuration release")
				}
			}
			f.mu.Unlock()
			status, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"enabled":true,"app_id":"cli_example","app_secret":"fake-secret","domain":"feishu"}`)
			if status != 200 {
				t.Fatalf("initial settings: %d %v", status, out)
			}
			w.bind(t, "pair")
			finished := make(chan struct{})
			w.h.bots.mu.Lock()
			receipt := w.h.botConfigureReceiptLocked()
			w.h.bots.mu.Unlock()
			go func() {
				w.h.configureBotForPairing(context.Background(), receipt)
				close(finished)
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("configuration request not reached")
			}
			for _, body := range tc.bodies {
				status, out := w.do(t, "PUT", "/api/v1/bots/feishu", body)
				if status != 200 {
					t.Fatalf("replacement while configuration pending: %d %v", status, out)
				}
			}
			once.Do(func() { close(release) })
			select {
			case <-finished:
			case <-time.After(10 * time.Second):
				t.Fatal("late configuration did not finish")
			}
			p := w.bind(t, "pair")
			if p["hint"] != i18n.T(i18n.ZhHans, "hub-channels.pair_hint") {
				t.Fatalf("late result reached current prompt: %v", p)
			}
			if p := configurePoll(t, w, st, f); p["hint"] != i18n.T(i18n.ZhHans, "hub-channels.pair_hint") {
				t.Fatalf("late result persisted: %v", p)
			}
		})
	}
}

// The network validator starts from an earlier snapshot. A replacement committed during validation must not
// lend its diagnostic to the credentials being saved, even when those looked unchanged in that earlier snapshot.
func TestFeishuConfigureDiagnosticUsesCommitTimeCredentials(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name    string
		replace func(*Manager, *FeishuBotSettings)
	}{
		{"app", func(_ *Manager, s *FeishuBotSettings) { s.AppID = "cli_replacement" }},
		{"domain", func(_ *Manager, s *FeishuBotSettings) { s.Domain = "lark" }},
		{"secret", func(h *Manager, s *FeishuBotSettings) {
			var err error
			s.SecretEnc, err = h.sealKey("replacement-secret")
			if err != nil {
				t.Error(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, st, f := configureWorld(t, 230002)
			configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
			// Commit an intervening generation at the fake platform's validation boundary. This models a concurrent
			// commit without requiring another route to bypass the lifecycle gate added by the separate registration PR.
			f.mu.Lock()
			f.beforeInfo = func() {
				w.h.bots.mu.Lock()
				defer w.h.bots.mu.Unlock()
				tc.replace(w.h, &w.h.bots.set)
				w.h.bots.set.Configure = &botConfigure{Issue: botcard.IssueBotCapability}
				if err := w.h.saveBotSettingsLocked(); err != nil {
					t.Error(err)
				}
			}
			f.mu.Unlock()
			status, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"app_id":"cli_example","app_secret":"fake-secret","domain":"feishu"}`)
			if status != 200 {
				t.Fatalf("save earlier snapshot: %d %v", status, out)
			}
			p := w.bind(t, "pair")
			if p["hint"] != i18n.T(i18n.ZhHans, "hub-channels.pair_hint") {
				t.Fatalf("replacement diagnostic crossed credential generations: %v", p)
			}
			configurePoll(t, w, st, f)
		})
	}
}

// configureSaveHost pauses the actual post-save report, before the credentials handler can start configuring.
type configureSaveHost struct {
	Host
	once             atomic.Bool
	entered, release chan struct{}
}

func (h *configureSaveHost) Notify(event string, value any) error {
	if event == extwire.EventReport && h.once.CompareAndSwap(false, true) {
		close(h.entered)
		select {
		case <-h.release:
		case <-time.After(15 * time.Second):
			return errors.New("report barrier timed out")
		}
	}
	return h.Host.Notify(event, value)
}

func configureAwait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(15 * time.Second):
		t.Fatal(what)
	}
}

// The main-line binding gate now serializes the formerly reproducible route interleaving. Cancellation while
// A is paused must not commit B, and later successful settings saves keep or retire the authoritative outcome.
func TestFeishuConfigureCommittedReceiptThroughRoute(t *testing.T) {
	needsLarkModule(t)
	for _, bodies := range [][]string{
		{`{"app_id":"cli_example","app_secret":"fake-secret","domain":"feishu"}`},
		{`{"app_id":"cli_other","app_secret":"other-secret","domain":"feishu"}`, `{"app_id":"cli_example","app_secret":"fake-secret","domain":"feishu"}`},
	} {
		t.Run(fmt.Sprint(len(bodies)), func(t *testing.T) {
			w, st, f := configureWorld(t, 230002)
			gate := &configureSaveHost{Host: w.h.opts.Hub, entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(gate.release) }) })
			w.h.opts.Hub = gate
			finished := make(chan struct{})
			go func() { defer close(finished); configureCredentials(t, w, "cli_example", "fake-secret", "feishu") }()
			configureAwait(t, gate.entered, "credentials did not reach post-save report")
			before := st.writes.Load()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			rec := httptest.NewRecorder()
			w.mux.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/bots/feishu", strings.NewReader(bodies[0])).WithContext(ctx))
			if rec.Code == 200 || st.writes.Load() != before {
				t.Fatalf("canceled gate waiter committed: %d", rec.Code)
			}
			once.Do(func() { close(gate.release) })
			configureAwait(t, finished, "credentials request did not finish")
			first := w.bind(t, "pair")
			for _, body := range bodies {
				status, out := w.do(t, "PUT", "/api/v1/bots/feishu", body)
				if status != 200 {
					t.Fatalf("later save: %d %v", status, out)
				}
			}
			p := w.bind(t, "pair")
			if len(bodies) == 1 && p["hint"] != first["hint"] {
				t.Fatalf("same credentials lost durable outcome: %v", p)
			}
			if len(bodies) > 1 && p["hint"] != i18n.T(i18n.ZhHans, "hub-channels.pair_hint") {
				t.Fatalf("replacement kept previous generation: %v", p)
			}
			configurePoll(t, w, st, f)
		})
	}
}

// Exercise the receipt-returning commit used by the real bind route, releasing the gate before a later real PUT.
// The old request may not obtain that later save's identity when it finally starts configuration.
func TestFeishuConfigureCommittedReceiptCannotAdoptLaterSave(t *testing.T) {
	needsLarkModule(t)
	for _, bodies := range [][]string{
		{`{"app_id":"cli_example","app_secret":"fake-secret","domain":"feishu"}`},
		{`{"app_id":"cli_other","app_secret":"other-secret","domain":"feishu"}`, `{"app_id":"cli_example","app_secret":"fake-secret","domain":"feishu"}`},
	} {
		t.Run(fmt.Sprint(len(bodies)), func(t *testing.T) {
			w, st, f := configureWorld(t, 230002)
			release, err := w.h.acquireFeishuBind(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			on, id, secret, domain := true, "cli_example", "fake-secret", "feishu"
			receipt, err := w.h.applyBotUpdateWithReceipt(context.Background(), &on, &id, &secret, &domain, nil, false)
			release()
			if err != nil {
				t.Fatal(err)
			}
			for _, body := range bodies {
				status, out := w.do(t, "PUT", "/api/v1/bots/feishu", body)
				if status != 200 {
					t.Fatalf("later save: %d %v", status, out)
				}
			}
			f.mu.Lock()
			before := f.requests
			f.mu.Unlock()
			writes := st.writes.Load()
			if err := w.h.configureBotForPairing(context.Background(), receipt); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			after := f.requests
			f.mu.Unlock()
			if before != after || writes != st.writes.Load() {
				t.Fatal("stale receipt ran configuration or saved an outcome")
			}
			p := w.bind(t, "pair")
			if p["hint"] != i18n.T(i18n.ZhHans, "hub-channels.pair_hint") {
				t.Fatalf("old receipt adopted new generation: %v", p)
			}
		})
	}
}

func TestFeishuConfigurePairedThenUnpairedThroughRoutes(t *testing.T) {
	needsLarkModule(t)
	w, st, f := configureWorld(t, 230002)
	withAssistant(w.h)
	status, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"enabled":true,"app_id":"cli_example","app_secret":"fake-secret","domain":"feishu"}`)
	if status != 200 {
		t.Fatalf("setup: %d %v", status, out)
	}
	original := w.bind(t, "pair")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	f.mu.Lock()
	f.beforeConfig = func() {
		close(entered)
		select {
		case <-release:
		case <-time.After(15 * time.Second):
			t.Error("configuration barrier timed out")
		}
	}
	f.mu.Unlock()
	finished := make(chan struct{})
	go func() { defer close(finished); configureCredentials(t, w, "cli_example", "fake-secret", "feishu") }()
	configureAwait(t, entered, "configuration not reached")
	// Install the fake transport client as the running bot; redemption still enters the real botEvent handler.
	w.h.bots.mu.Lock()
	w.h.bots.api = botport.New("cli_example", "fake-secret", "feishu")
	w.h.bots.mu.Unlock()
	if _, err := w.h.botEvent(context.Background(), botMessageEvent("om_configure_pair_cycle", "ou_owner", "text", textContent(original["user_code"].(string)), nil)); err != nil {
		t.Fatal(err)
	}
	w.h.bots.mu.Lock()
	line := w.h.bots.line
	w.h.bots.mu.Unlock()
	configureAwait(t, line, "pair event not finished")
	if status, out := w.do(t, "GET", "/api/v1/bots/feishu", ""); status != 200 || out["owner"] == nil {
		t.Fatalf("not paired: %d %v", status, out)
	}
	if status, out := w.do(t, "DELETE", "/api/v1/bots/feishu/owner", ""); status != 200 || out["owner"] != nil {
		t.Fatalf("not unpaired: %d %v", status, out)
	}
	once.Do(func() { close(release) })
	configureAwait(t, finished, "configuration did not finish")
	p := w.bind(t, "pair")
	if p["hint"] != i18n.T(i18n.ZhHans, "hub-channels.pair_hint") {
		t.Fatalf("late outcome resurrected after real pair/unpair: %v", p)
	}
	configurePoll(t, w, st, f)
}

func TestFeishuConfigureWriteFailureBoundaries(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name  string
		write int64
	}{{"credentials", 1}, {"diagnostic", 2}, {"code", 3}} {
		t.Run(tc.name, func(t *testing.T) {
			w, st, _ := configureWorld(t, 230002)
			st.rejectWrite.Store(tc.write)
			status, out := w.do(t, "POST", "/api/v1/channels/feishu/bind", `{"step":"credentials","values":{"app_id":"cli_example","app_secret":"fake-secret","domain":"feishu"}}`)
			if status == 200 && out["step"] == "pair" && out["error"] == nil {
				t.Fatalf("failed write acknowledged as durable pairing prompt: %v", out)
			}
			if strings.Contains(fmt.Sprint(out), "raw-sensitive") {
				t.Fatalf("store error exposed: %v", out)
			}
			var saved FeishuBotSettings
			_, _ = st.Setting(context.Background(), botSettingsKey, &saved)
			w.h.bots.mu.Lock()
			live := w.h.bots.set.copy()
			w.h.bots.mu.Unlock()
			a, _ := json.Marshal(live)
			b, _ := json.Marshal(saved)
			if tc.name != "credentials" && string(a) != string(b) {
				t.Fatalf("failed write left live state ahead of durable state: live=%s saved=%s", a, b)
			}
			if tc.name == "credentials" && (live.AppID != "" || saved.AppID != "") {
				t.Fatal("failed credentials commit survived")
			}
		})
	}
}

func TestFeishuConfigureFailedCorrectionRetainsDurableOutcome(t *testing.T) {
	needsLarkModule(t)
	w, st, f := configureWorld(t, 230002)
	first := configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
	f.mu.Lock()
	f.code = 0
	f.mu.Unlock()
	st.rejectWrite.Store(st.writes.Load() + 2) // Credentials commit succeeds; only the new outcome fails.
	out := configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
	if out["step"] != "credentials" || out["error"] == nil || strings.Contains(fmt.Sprint(out), "raw-sensitive") {
		t.Fatalf("missing safe failure receipt: %v", out)
	}
	p := configurePoll(t, w, st, f)
	if p["hint"] != first["hint"] || p["user_code"] != first["user_code"] {
		t.Fatalf("failed correction replaced durable outcome: %v", p)
	}
	var saved FeishuBotSettings
	_, _ = st.Setting(context.Background(), botSettingsKey, &saved)
	if saved.Configure == nil || saved.Configure.Issue != botcard.IssuePatchScope {
		t.Fatal("previous outcome not durable")
	}
	st.rejectWrite.Store(0)
	corrected := configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
	if !strings.Contains(fmt.Sprint(corrected["hint"]), i18n.T(i18n.ZhHans, "hub-channels.auto_configured")) {
		t.Fatalf("explicit correction retry: %v", corrected)
	}
	configurePoll(t, w, st, f)
}

func TestFeishuConfigureUnpairFailurePreservesOwner(t *testing.T) {
	needsLarkModule(t)
	w, st, f := configureWorld(t, 230002)
	withAssistant(w.h)
	p := configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
	w.h.botPair(botport.New("cli_example", "fake-secret", "feishu"), "ou_owner", "oc_owner", p["user_code"].(string))
	var unpaired atomic.Int64
	w.h.opts.AssistantEvent = func(_ string, _ json.RawMessage) { unpaired.Add(1) }
	st.rejectWrite.Store(st.writes.Load() + 1)
	status, out := w.do(t, "DELETE", "/api/v1/bots/feishu/owner", "")
	if status != http.StatusServiceUnavailable || strings.Contains(fmt.Sprint(out), "raw-sensitive") {
		t.Fatalf("failed unpair response: %d %v", status, out)
	}
	if p := configurePoll(t, w, st, f); p["done"] != true {
		t.Fatalf("failed unpair forgot owner: %v", p)
	}
	if unpaired.Load() != 0 {
		t.Fatal("failed unpair notified success")
	}
	st.rejectWrite.Store(0)
	if status, out = w.do(t, "DELETE", "/api/v1/bots/feishu/owner", ""); status != 200 {
		t.Fatalf("unpair retry: %d %v", status, out)
	}
	if unpaired.Load() != 1 {
		t.Fatal("committed unpair did not notify")
	}
	p = w.bind(t, "pair")
	if p["hint"] != i18n.T(i18n.ZhHans, "hub-channels.pair_hint") {
		t.Fatalf("successful unpair kept guidance: %v", p)
	}
}

func TestFeishuConfigureSaveFailureLocalizedAndRetryable(t *testing.T) {
	needsLarkModule(t)
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			w, st, f := configureWorld(t, 230002)
			w.h.opts.Lang = func() i18n.Lang { return lang }
			st.rejectWrite.Store(1)
			out := configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
			if out["error"] != i18n.T(lang, "hub-bots.settings_save_failed") {
				t.Fatalf("failure not localized: %v", out)
			}
			st.rejectWrite.Store(0)
			out = configureCredentials(t, w, "cli_example", "fake-secret", "feishu")
			if out["step"] != "pair" || out["error"] != nil {
				t.Fatalf("credentials retry: %v", out)
			}
			configurePoll(t, w, st, f)
		})
	}
}
