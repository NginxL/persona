package extmgr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/botport"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// fakeAccounts is the accounts site's app registration: begin hands out a device code, each poll answers the next of
// polls (the last one repeats).
type fakeAccounts struct {
	srv     *httptest.Server
	mu      sync.Mutex
	polls   []string
	asked   []string // "begin" / "poll <device code>"
	devices int
}

func newFakeAccounts(t *testing.T, polls ...string) *fakeAccounts {
	f := &fakeAccounts{polls: polls}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.PostForm.Get("action") {
		case "begin":
			f.devices++
			f.asked = append(f.asked, "begin")
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dc_secret_" + string(rune('0'+f.devices)),
				"verification_uri_complete": "https://accounts.example.com/page?user_code=UC", "expire_in": 600, "interval": 1})
		case "poll":
			f.asked = append(f.asked, "poll "+r.PostForm.Get("device_code"))
			body := f.polls[0]
			if len(f.polls) > 1 {
				f.polls = f.polls[1:]
			}
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// registerWorld is a manager with no app yet, the fake accounts site and the fake open platform the new app lives on.
func registerWorld(t *testing.T, polls ...string) (*botWorld, *fakeAccounts, *[]string) {
	t.Helper()
	h := newTestManager(t)
	h.store = newBotStore()
	a := withAssistant(h)
	var mu sync.Mutex
	var told []string
	inner := h.opts.AssistantEvent
	h.opts.AssistantEvent = func(event string, params json.RawMessage) {
		inner(event, params)
		mu.Lock()
		told = append(told, event+" "+string(params))
		mu.Unlock()
	}
	api := newFakeOpenAPI(t)
	acc := newFakeAccounts(t, polls...)
	h.bots.registrar = botport.NewRegistrar(acc.srv.URL)
	h.bots.regDomain = func(string) string { return api.srv.URL }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.startBots(ctx)
	mux := http.NewServeMux()
	h.botRoutes(mux)
	return &botWorld{h: h, api: api, a: a, mux: mux, ctx: ctx}, acc, &told
}

func (w *botWorld) bind(t *testing.T, step string) map[string]any {
	t.Helper()
	body := ""
	if step != "" {
		body = `{"step":"` + step + `"}`
	}
	code, out := w.do(t, "POST", "/api/v1/channels/feishu/bind", body)
	if code != 200 {
		t.Fatalf("bind %q: %d %v", step, code, out)
	}
	return out
}

// Binding with no app starts a registration; once confirmed, the new app is saved, its confirming person is the owner
// in the private chat the greeting landed in, and the assistant starts the chat without greeting again.
func TestBotRegistrationCreatesTheAppAndPairsTheOwner(t *testing.T) {
	needsLarkModule(t)
	w, acc, told := registerWorld(t,
		`{"error":"authorization_pending"}`,
		`{"client_id":"cli_new","client_secret":"new-secret","user_info":{"open_id":"ou_me","tenant_brand":"feishu"}}`)

	p := w.bind(t, "")
	if p["step"] != stepRegister || p["kind"] != "qr" || p["qr"] == nil || p["link"] != p["url"] || !strings.Contains(p["url"].(string), "accounts.example.com") {
		t.Fatalf("first step: %v", p)
	}
	if raw, _ := json.Marshal(p); strings.Contains(string(raw), "dc_secret") {
		t.Fatalf("the device code reached the app: %s", raw)
	}
	// Sealed, and only in the top-level field the key rotation re-seals (db.sealedSettings).
	w.h.bots.mu.Lock()
	stored, _ := json.Marshal(w.h.bots.set)
	sealed := w.h.bots.set.RegistrationDeviceEnc
	w.h.bots.mu.Unlock()
	var top map[string]json.RawMessage
	_ = json.Unmarshal(stored, &top)
	if sealed == "" || strings.Contains(string(stored), "dc_secret") || strings.Contains(string(top["registration"]), sealed) {
		t.Fatalf("the device code is not sealed at the top level: %s", stored)
	}
	if p := w.bind(t, stepRegister); p["step"] != stepRegister || p["done"] != nil {
		t.Fatalf("pending: %v", p)
	}
	if p := w.bind(t, stepRegister); p["done"] != true {
		t.Fatalf("done: %v", p)
	}
	_, view := w.do(t, "GET", "/api/v1/bots/feishu", "")
	if view["app_id"] != "cli_new" || view["enabled"] != true || view["owner"] == nil {
		t.Fatalf("bound: %v", view)
	}
	w.h.bots.mu.Lock()
	owner, reg := *w.h.bots.set.Owner, w.h.bots.set.Registration
	w.h.bots.mu.Unlock()
	if owner.OpenID != "ou_me" || owner.ChatID != "oc_p2p" || reg != nil {
		t.Fatalf("owner %+v, registration %+v", owner, reg)
	}
	w.api.find(t, "/open-apis/im/v1/messages", `"receive_id":"ou_me"`)
	greeted := false
	for _, e := range *told {
		if strings.HasPrefix(e, assistantwire.EventFeishuOwner) && strings.Contains(e, `"greeted":true`) {
			greeted = true
		}
	}
	if !greeted {
		t.Fatalf("the assistant was not told the owner was greeted: %v", *told)
	}
	if n := strings.Count(strings.Join(acc.asked, "\n"), "begin"); n != 1 {
		t.Fatalf("one registration, asked %v", acc.asked)
	}
}

// A tenant on the other cloud is polled there. Declining waits for an explicit bind before starting again.
func TestBotRegistrationDeclinedWaitsForBind(t *testing.T) {
	needsLarkModule(t)
	w, acc, _ := registerWorld(t, `{"error":"tenant_mismatch","user_info":{"tenant_brand":"lark"}}`, `{"error":"access_denied"}`)
	w.bind(t, "")
	if p := w.bind(t, stepRegister); p["step"] != stepRegister || p["error"] != nil {
		t.Fatalf("switched cloud: %v", p)
	}
	w.h.bots.mu.Lock()
	region := w.h.bots.set.Registration.Region
	w.h.bots.mu.Unlock()
	if region != "lark" {
		t.Fatalf("region %q", region)
	}
	p := w.bind(t, stepRegister)
	if p["step"] != stepRegister || p["error"] == nil {
		t.Fatalf("declined: %v", p)
	}
	if p := w.bind(t, stepRegister); p["error"] == nil {
		t.Fatalf("declined on the next poll: %v", p)
	}
	if acc.devices != 1 {
		t.Fatalf("polling a declined registration started another: %v", acc.asked)
	}
	if p := w.bind(t, ""); p["step"] != stepRegister || p["error"] != nil {
		t.Fatalf("explicit retry: %v", p)
	}
	if n := strings.Count(strings.Join(acc.asked, "\n"), "begin"); n != 2 {
		t.Fatalf("a second registration, asked %v", acc.asked)
	}
}

// A bot with credentials already goes on to the pairing code, as before.
func TestBotWithCredentialsStillPairsWithACode(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	acc := newFakeAccounts(t, `{}`)
	w.h.bots.registrar = botport.NewRegistrar(acc.srv.URL)
	if p := w.bind(t, ""); p["kind"] != PromptPairCode {
		t.Fatalf("pair step: %v", p)
	}
	if len(acc.asked) != 0 {
		t.Fatalf("registered with an app already there: %v", acc.asked)
	}
}
