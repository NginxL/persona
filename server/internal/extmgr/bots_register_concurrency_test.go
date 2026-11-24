package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botport"
)

// observeBindWait signals when a request evaluates the admission select's cancellation case.
// The first bind is paused in the fake accounts server, so the second cannot acquire the gate.
type observeBindWait struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (c *observeBindWait) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

type registrationBindResult struct {
	code   int
	prompt map[string]any
}

func startRegistrationBind(w *botWorld, ctx context.Context, step string) <-chan registrationBindResult {
	body, _ := json.Marshal(map[string]any{"step": step, "values": map[string]string{}})
	return startBotRequest(w, ctx, http.MethodPost, "/api/v1/channels/feishu/bind", string(body))
}

func startBotRequest(w *botWorld, ctx context.Context, method, path, body string) <-chan registrationBindResult {
	done := make(chan registrationBindResult, 1)
	go func() {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		rec := httptest.NewRecorder()
		w.mux.ServeHTTP(rec, req)
		var p map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		done <- registrationBindResult{rec.Code, p}
	}()
	return done
}

func awaitRegistration[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("registration test did not reach its synchronization point")
		var zero T
		return zero
	}
}

func registrationPrompt(t *testing.T, ch <-chan registrationBindResult) map[string]any {
	t.Helper()
	r := awaitRegistration(t, ch)
	if r.code != http.StatusOK {
		t.Fatalf("bind: HTTP %d, %v", r.code, r.prompt)
	}
	return r.prompt
}

// controlledRegistration uses only local fake requests. Cleanup releases paused handlers after a failed assertion.
func controlledRegistration(t *testing.T, w *botWorld, reply func(action, code string, n int, stop <-chan struct{}) string) (*atomic.Int32, *atomic.Int32) {
	t.Helper()
	var begins, polls atomic.Int32
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		action := r.PostForm.Get("action")
		var n int
		if action == "begin" {
			n = int(begins.Add(1))
		} else {
			n = int(polls.Add(1))
		}
		body := reply(action, r.PostForm.Get("device_code"), n, stop)
		if body == "" && action == "begin" {
			body = fmt.Sprintf(`{"device_code":"fake-device-%d","verification_uri_complete":"https://accounts.example.com/page?user_code=%d","expire_in":600}`, n, n)
		}
		_, _ = rw.Write([]byte(body))
	}))
	t.Cleanup(func() { close(stop); srv.Close() })
	w.h.bots.registrar = botport.NewRegistrar(srv.URL)
	return &begins, &polls
}

func TestBotRegistrationConcurrentBindsShareSession(t *testing.T) {
	needsLarkModule(t)
	w, _, _ := registerWorld(t)
	entered, release := make(chan struct{}), make(chan struct{})
	begins, _ := controlledRegistration(t, w, func(action, code string, n int, stop <-chan struct{}) string {
		if action == "begin" && n == 1 {
			close(entered)
			select {
			case <-release:
			case <-stop:
			}
		}
		return ""
	})
	first := startRegistrationBind(w, context.Background(), "")
	awaitRegistration(t, entered)
	waiting := &observeBindWait{Context: context.Background(), waiting: make(chan struct{})}
	second := startRegistrationBind(w, waiting, "")
	awaitRegistration(t, waiting.waiting)
	// Unrelated bot reads remain available while Begin waits on the network.
	viewed := make(chan struct{})
	go func() { _ = w.h.botView(w.ctx); close(viewed) }()
	awaitRegistration(t, viewed)
	close(release)
	a, b := registrationPrompt(t, first), registrationPrompt(t, second)
	if begins.Load() != 1 || a["url"] != b["url"] || a["step"] != stepRegister {
		t.Fatalf("concurrent binds: begins=%d, same URL=%v, steps=%v/%v", begins.Load(), a["url"] == b["url"], a["step"], b["step"])
	}
}

func TestBotRegistrationCanceledWaitDoesNotAffectActiveBind(t *testing.T) {
	needsLarkModule(t)
	w, _, _ := registerWorld(t)
	entered, release := make(chan struct{}), make(chan struct{})
	begins, _ := controlledRegistration(t, w, func(action, code string, n int, stop <-chan struct{}) string {
		if action == "begin" && n == 1 {
			close(entered)
			select {
			case <-release:
			case <-stop:
			}
		}
		return ""
	})
	first := startRegistrationBind(w, context.Background(), "")
	awaitRegistration(t, entered)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := &observeBindWait{Context: ctx, waiting: make(chan struct{})}
	second := startRegistrationBind(w, waiting, "")
	awaitRegistration(t, waiting.waiting)
	cancel()
	if r := awaitRegistration(t, second); r.code != http.StatusBadGateway {
		t.Fatalf("canceled wait: %+v", r)
	}
	close(release)
	p := registrationPrompt(t, first)
	if next := w.bind(t, ""); next["url"] != p["url"] || begins.Load() != 1 {
		t.Fatal("canceled waiter changed the active registration")
	}
}

func TestBotRegistrationCanceledBeginCannotReplaceNextSession(t *testing.T) {
	needsLarkModule(t)
	w, _, _ := registerWorld(t)
	entered, release, replied := make(chan struct{}), make(chan struct{}), make(chan struct{})
	begins, _ := controlledRegistration(t, w, func(action, code string, n int, stop <-chan struct{}) string {
		if action == "begin" && n == 1 {
			close(entered)
			select {
			case <-release:
			case <-stop:
			}
			close(replied)
		}
		return ""
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := startRegistrationBind(w, ctx, "")
	awaitRegistration(t, entered)
	cancel()
	if r := awaitRegistration(t, first); r.code != http.StatusBadGateway {
		t.Fatalf("canceled begin: %+v", r)
	}
	next := w.bind(t, "")
	close(release)
	awaitRegistration(t, replied)
	if p := w.bind(t, ""); p["url"] != next["url"] || begins.Load() != 2 {
		t.Fatal("old Begin response changed the next session")
	}
}

func TestBotRegistrationCanceledPollCannotChangeNextSession(t *testing.T) {
	needsLarkModule(t)
	for name, body := range map[string]string{
		"expired":   `{"error":"expired_token"}`,
		"denied":    `{"error":"access_denied"}`,
		"failed":    `{"error":"registration_failed"}`,
		"slow_down": `{"error":"slow_down"}`,
		"cloud":     `{"error":"tenant_mismatch","user_info":{"tenant_brand":"lark"}}`,
		"confirmed": `{"client_id":"cli_old","client_secret":"fake-secret","user_info":{"open_id":"ou_old","tenant_brand":"feishu"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			w, _, _ := registerWorld(t)
			entered, release, replied := make(chan struct{}), make(chan struct{}), make(chan struct{})
			begins, polls := controlledRegistration(t, w, func(action, code string, n int, stop <-chan struct{}) string {
				if action == "begin" {
					return ""
				}
				if n == 1 {
					close(entered)
					select {
					case <-release:
					case <-stop:
					}
					close(replied)
					return body
				}
				if code != "fake-device-2" {
					t.Errorf("polled the wrong session: %q", code)
				}
				return `{"error":"authorization_pending"}`
			})
			w.bind(t, "")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first := startRegistrationBind(w, ctx, stepRegister)
			awaitRegistration(t, entered)
			cancel()
			if r := awaitRegistration(t, first); r.code != http.StatusBadGateway {
				t.Fatalf("canceled poll: %+v", r)
			}
			// The user reopens pairing after expiry, while the old fake HTTP response is still paused.
			w.h.bots.mu.Lock()
			later := w.h.bots.set.Registration.Expires.Add(time.Second)
			w.h.bots.now = func() time.Time { return later }
			w.h.bots.mu.Unlock()
			next := w.bind(t, "")
			w.h.bots.mu.Lock()
			saved := w.h.bots.set.registration()
			w.h.bots.mu.Unlock()
			close(release)
			awaitRegistration(t, replied)
			p := w.bind(t, stepRegister)
			w.h.bots.mu.Lock()
			current := w.h.bots.set.registration()
			configured := w.h.bots.set.AppID != "" || w.h.bots.set.Owner != nil
			w.h.bots.mu.Unlock()
			if !reflect.DeepEqual(saved, current) || configured || p["url"] != next["url"] || begins.Load() != 2 || polls.Load() != 2 {
				t.Fatalf("old Poll changed a new session: equal=%v, configured=%v, begins=%d, polls=%d", reflect.DeepEqual(saved, current), configured, begins.Load(), polls.Load())
			}
		})
	}
}

func TestBotRegistrationTerminalPollWaitsForExplicitBind(t *testing.T) {
	needsLarkModule(t)
	for name, body := range map[string]string{
		"expired": `{"error":"expired_token"}`,
		"denied":  `{"error":"access_denied"}`,
		"failed":  `{"error":"registration_failed"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w, acc, _ := registerWorld(t, body)
			first := w.bind(t, "")
			p := w.bind(t, stepRegister)
			again := w.bind(t, stepRegister)
			if p["error"] == nil || again["error"] != p["error"] {
				t.Fatalf("terminal error lost: %v / %v", p, again)
			}
			w.h.bots.mu.Lock()
			reg := w.h.bots.set.registration()
			w.h.bots.mu.Unlock()
			if reg == nil || reg.URL != first["url"] || reg.Failure == "" || acc.devices != 1 {
				t.Fatal("terminal session was replaced or cleared")
			}
			retry := w.bind(t, "")
			if retry["step"] != stepRegister || retry["error"] != nil {
				t.Fatalf("explicit retry: %v", retry)
			}
			if acc.devices != 2 {
				t.Fatal("explicit bind did not create a fresh session")
			}
		})
	}
}

func TestBotRegistrationExpiredLocallyWaitsForExplicitBind(t *testing.T) {
	needsLarkModule(t)
	w, acc, _ := registerWorld(t, `{"error":"authorization_pending"}`)
	first := w.bind(t, "")
	w.h.bots.mu.Lock()
	later := w.h.bots.set.Registration.Expires.Add(time.Second)
	w.h.bots.now = func() time.Time { return later }
	w.h.bots.mu.Unlock()
	p := w.bind(t, stepRegister)
	if p["url"] != first["url"] || p["error"] == nil || len(acc.asked) != 1 {
		t.Fatalf("expiry renewed in background: %v, calls=%v", p, acc.asked)
	}
	w.bind(t, "")
	if acc.devices != 2 {
		t.Fatalf("explicit bind did not renew: %v", acc.asked)
	}
}

func TestBotRegistrationConfirmedSessionSurvivesExpiry(t *testing.T) {
	needsLarkModule(t)
	w, acc, _ := registerWorld(t)
	first := w.bind(t, "")
	w.h.bots.mu.Lock()
	later := w.h.bots.set.Registration.Expires.Add(time.Second)
	w.h.bots.now = func() time.Time { return later }
	w.h.bots.set.Registration.AppID = "cli_confirmed"
	w.h.bots.mu.Unlock()
	on, id, secret, domain := true, "cli_confirmed", "fake-secret", w.api.srv.URL
	release, err := w.h.acquireFeishuBind(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = w.h.applyBotUpdate(w.ctx, &on, &id, &secret, &domain, nil, true)
	release()
	if err != nil {
		t.Fatal(err)
	}
	if p := w.bind(t, ""); p["url"] != first["url"] || acc.devices != 1 {
		t.Fatal("confirmed session was replaced before its finish retry")
	}
}

func TestBotRegistrationConcurrentPollsFinishOnce(t *testing.T) {
	needsLarkModule(t)
	w, _, _ := registerWorld(t)
	entered, release := make(chan struct{}), make(chan struct{})
	_, polls := controlledRegistration(t, w, func(action, code string, n int, stop <-chan struct{}) string {
		if action == "begin" {
			return ""
		}
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-stop:
			}
		}
		return `{"client_id":"cli_new","client_secret":"fake-secret","user_info":{"open_id":"ou_me","tenant_brand":"feishu"}}`
	})
	w.bind(t, "")
	first := startRegistrationBind(w, context.Background(), stepRegister)
	awaitRegistration(t, entered)
	waiting := &observeBindWait{Context: context.Background(), waiting: make(chan struct{})}
	second := startRegistrationBind(w, waiting, stepRegister)
	awaitRegistration(t, waiting.waiting)
	viewed := make(chan struct{})
	go func() { _ = w.h.botView(w.ctx); close(viewed) }()
	awaitRegistration(t, viewed)
	close(release)
	a, b := registrationPrompt(t, first), registrationPrompt(t, second)
	if a["done"] != true || b["done"] != true || polls.Load() != 1 {
		t.Fatalf("concurrent finish: %v / %v; polls=%d", a, b, polls.Load())
	}
}

func TestBotRegistrationBeginFailurePreservesCredentialFallback(t *testing.T) {
	needsLarkModule(t)
	w, _, _ := registerWorld(t)
	controlledRegistration(t, w, func(action, code string, n int, stop <-chan struct{}) string {
		if action == "poll" {
			return `{"error":"expired_token"}`
		}
		if n == 1 || n == 3 {
			return `{"error":"temporarily_unavailable"}`
		}
		return ""
	})
	if p := w.bind(t, ""); p["step"] != "credentials" || p["error"] != nil {
		t.Fatalf("initial fallback: %v", p)
	}
	w.bind(t, "")
	expired := w.bind(t, stepRegister)
	if p := w.bind(t, ""); p["step"] != "credentials" || p["error"] != expired["error"] {
		t.Fatalf("retry fallback lost the terminal error: %v", p)
	}
}

func TestBotRegistrationTypedCredentialsEndTheRegistration(t *testing.T) {
	needsLarkModule(t)
	w, acc, _ := registerWorld(t, `{"error":"authorization_pending"}`)
	w.bind(t, "")
	body := fmt.Sprintf(`{"step":"credentials","values":{"app_id":"cli_typed","app_secret":"fake-secret","domain":%q}}`, w.api.srv.URL)
	code, p := w.do(t, http.MethodPost, "/api/v1/channels/feishu/bind", body)
	if code != http.StatusOK || p["step"] != "pair" {
		t.Fatalf("typed credentials: HTTP %d, %v", code, p)
	}
	if p := w.bind(t, stepRegister); p["step"] != "pair" {
		t.Fatalf("old register poll did not resume configured pairing: %v", p)
	}
	w.h.bots.mu.Lock()
	reg, id := w.h.bots.set.registration(), w.h.bots.set.AppID
	w.h.bots.mu.Unlock()
	if reg != nil || id != "cli_typed" || len(acc.asked) != 1 {
		t.Fatal("old registration survived the user's credential choice")
	}
}
