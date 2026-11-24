package extmgr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

// The real bind flow confirms the app and saves credentials, then a fake send failure leaves its finish retryable.
func confirmedRegistrationWorld(t *testing.T) (*botWorld, *fakeAccounts, *botRegistration) {
	t.Helper()
	w, acc, _ := registerWorld(t, `{"client_id":"cli_old","client_secret":"old-secret","user_info":{"open_id":"ou_me","tenant_brand":"feishu"}}`)
	w.api.mu.Lock()
	w.api.rejectInteractive = 99991
	w.api.mu.Unlock()
	w.bind(t, "")
	if p := w.bind(t, stepRegister); p["error"] == nil || p["done"] == true {
		t.Fatalf("expected retryable fake finish failure: %v", p)
	}
	w.api.mu.Lock()
	w.api.rejectInteractive = 0
	w.api.mu.Unlock()
	w.h.bots.mu.Lock()
	reg := w.h.bots.set.registration()
	id := w.h.bots.set.AppID
	w.h.bots.mu.Unlock()
	if reg == nil || reg.AppID != "cli_old" || id != "cli_old" {
		t.Fatal("the confirmed session did not retain its saved credentials")
	}
	return w, acc, reg
}

type registrationSettingsStore struct {
	Store
	mu     sync.Mutex
	writes []FeishuBotSettings
	fail   bool
}

func (s *registrationSettingsStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == botSettingsKey {
		s.mu.Lock()
		s.writes = append(s.writes, value.(FeishuBotSettings).copy())
		fail := s.fail
		s.mu.Unlock()
		if fail {
			return errors.New("synthetic credential settings failure")
		}
	}
	return s.Store.PutSetting(ctx, key, value)
}

func TestBotRegistrationSettingsPUTRetiresOnlyChangedCredentials(t *testing.T) {
	needsLarkModule(t)
	for _, kind := range []string{"app", "secret", "domain", "unchanged", "features", "enabled"} {
		t.Run(kind, func(t *testing.T) {
			w, acc, old := confirmedRegistrationWorld(t)
			store := &registrationSettingsStore{Store: w.h.store}
			w.h.store = store
			expectedID := "cli_old"
			retired := true
			var body string
			switch kind {
			case "app":
				expectedID = "cli_new"
				body = fmt.Sprintf(`{"app_id":"cli_new","app_secret":"new-secret","domain":%q}`, w.api.srv.URL)
			case "secret":
				body = `{"app_secret":"new-secret"}`
			case "domain":
				api := newFakeOpenAPI(t)
				body = fmt.Sprintf(`{"domain":%q}`, api.srv.URL)
			case "unchanged":
				retired = false
				body = fmt.Sprintf(`{"app_id":"cli_old","app_secret":"old-secret","domain":%q}`, w.api.srv.URL)
			case "features":
				retired = false
				body = `{"features":{"main":false}}`
			case "enabled":
				retired = false
				body = `{"enabled":false}`
			}
			code, p := w.do(t, http.MethodPut, "/api/v1/bots/feishu", body)
			if code != http.StatusOK {
				t.Fatalf("settings PUT: %d %v", code, p)
			}
			w.h.bots.mu.Lock()
			reg := w.h.bots.set.registration()
			w.h.bots.mu.Unlock()
			if retired {
				if reg != nil {
					t.Fatal("changed credentials retained the confirmed old registration")
				}
				store.mu.Lock()
				writes := append([]FeishuBotSettings(nil), store.writes...)
				store.mu.Unlock()
				if len(writes) == 0 {
					t.Fatal("no credential commit was observed")
				}
				for _, saved := range writes {
					if saved.AppID != expectedID || saved.Registration != nil || saved.RegistrationDeviceEnc != "" || saved.RegistrationSecretEnc != "" {
						t.Fatal("credentials and registration retirement were not saved together")
					}
				}
				for _, step := range []string{"", stepRegister} {
					if p := w.bind(t, step); p["step"] != "pair" {
						t.Fatalf("old registration resumed after settings PUT: %v", p)
					}
				}
			} else if !reflect.DeepEqual(reg, old) {
				t.Fatal("an unchanged credential or unrelated setting retired the session")
			}
			if len(acc.asked) != 2 {
				t.Fatalf("settings update unexpectedly called the registrar: %v", acc.asked)
			}
			_, view := w.do(t, http.MethodGet, "/api/v1/bots/feishu", "")
			if view["app_id"] != expectedID {
				t.Fatal("binding restored the old app over the user's choice")
			}
		})
	}
}

func TestBotRegistrationSettingsPUTFailureKeepsConfirmedSession(t *testing.T) {
	needsLarkModule(t)
	for _, failure := range []string{"validation", "save"} {
		t.Run(failure, func(t *testing.T) {
			w, _, old := confirmedRegistrationWorld(t)
			var before FeishuBotSettings
			if ok, err := w.h.store.Setting(w.ctx, botSettingsKey, &before); !ok || err != nil {
				t.Fatal("missing initial persisted settings")
			}
			if failure == "validation" {
				w.api.mu.Lock()
				w.api.botInfoFail = true
				w.api.mu.Unlock()
			} else {
				w.h.store = &registrationSettingsStore{Store: w.h.store, fail: true}
			}
			body := fmt.Sprintf(`{"app_id":"cli_new","app_secret":"new-secret","domain":%q}`, w.api.srv.URL)
			if code, _ := w.do(t, http.MethodPut, "/api/v1/bots/feishu", body); code != http.StatusBadRequest {
				t.Fatalf("failed PUT returned %d", code)
			}
			w.h.bots.mu.Lock()
			reg, id, secret := w.h.bots.set.registration(), w.h.bots.set.AppID, w.h.bots.set.SecretEnc
			w.h.bots.mu.Unlock()
			plain, err := w.h.openKey(secret)
			if !reflect.DeepEqual(reg, old) || id != "cli_old" || err != nil || plain != "old-secret" {
				t.Fatal("failed replacement changed the current credentials or registration")
			}
			var saved FeishuBotSettings
			if ok, err := w.h.store.Setting(w.ctx, botSettingsKey, &saved); err != nil || !ok || !reflect.DeepEqual(saved, before) {
				t.Fatal("failed replacement changed persisted registration")
			}
		})
	}
}

func TestBotRegistrationSettingsPUTWaitsForPollAndFinish(t *testing.T) {
	needsLarkModule(t)
	w, _, _ := registerWorld(t)
	entered, release := make(chan struct{}), make(chan struct{})
	controlledRegistration(t, w, func(action, code string, n int, stop <-chan struct{}) string {
		if action == "begin" {
			return ""
		}
		close(entered)
		select {
		case <-release:
		case <-stop:
		}
		return `{"client_id":"cli_old","client_secret":"old-secret","user_info":{"open_id":"ou_me","tenant_brand":"feishu"}}`
	})
	w.bind(t, "")
	poll := startRegistrationBind(w, context.Background(), stepRegister)
	awaitRegistration(t, entered)
	waiting := &observeBindWait{Context: context.Background(), waiting: make(chan struct{})}
	body := fmt.Sprintf(`{"app_id":"cli_new","app_secret":"new-secret","domain":%q}`, w.api.srv.URL)
	put := startBotRequest(w, waiting, http.MethodPut, "/api/v1/bots/feishu", body)
	awaitRegistration(t, waiting.waiting)
	// A feature-only PUT must complete while the registration holds the lifecycle gate over a paused Poll.
	feature := startBotRequest(w, context.Background(), http.MethodPut, "/api/v1/bots/feishu", `{"features":{"main":false}}`)
	registrationPrompt(t, feature)
	close(release)
	if p := registrationPrompt(t, poll); p["done"] != true {
		t.Fatalf("finish: %v", p)
	}
	registrationPrompt(t, put)
	if p := w.bind(t, stepRegister); p["step"] != "pair" {
		t.Fatalf("old finish survived the credential replacement: %v", p)
	}
	_, view := w.do(t, http.MethodGet, "/api/v1/bots/feishu", "")
	if view["app_id"] != "cli_new" || view["owner"] != nil || view["features"].(map[string]any)["main"] != false {
		t.Fatalf("the old finish overwrote settings: %v", view)
	}
}

func TestBotRegistrationSettingsPUTValidationOrdersOldBind(t *testing.T) {
	needsLarkModule(t)
	w, _, _ := confirmedRegistrationWorld(t)
	entered, release, stop := make(chan struct{}), make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(stop) })
	var calls atomic.Int32
	w.api.mu.Lock()
	// Only the PUT's validation asks as cli_new (the new app runs only once validated). The bot's own start-up asks
	// as cli_old in the background and, on a loaded machine, can reach the fake after this is set (#13688).
	w.api.beforeBotInfo = func(app string) {
		if app == "cli_new" && calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-stop:
			}
		}
	}
	w.api.mu.Unlock()
	body := fmt.Sprintf(`{"app_id":"cli_new","app_secret":"new-secret","domain":%q,"features":{"agents":true}}`, w.api.srv.URL)
	put := startBotRequest(w, context.Background(), http.MethodPut, "/api/v1/bots/feishu", body)
	awaitRegistration(t, entered)
	waiting := &observeBindWait{Context: context.Background(), waiting: make(chan struct{})}
	bind := startRegistrationBind(w, waiting, stepRegister)
	awaitRegistration(t, waiting.waiting)
	// Neither the lifecycle gate nor the remote validation holds bots.mu or blocks independent switches.
	enabled := startBotRequest(w, context.Background(), http.MethodPut, "/api/v1/bots/feishu", `{"enabled":false,"features":{"read_group":true}}`)
	if p := registrationPrompt(t, enabled); p["enabled"] != false {
		t.Fatalf("disable PUT did not report disabled: %v", p)
	}
	close(release)
	if p := registrationPrompt(t, put); p["enabled"] != false {
		t.Fatalf("credential PUT undid the disable: %v", p)
	}
	if p := registrationPrompt(t, bind); p["step"] != "credentials" {
		t.Fatalf("queued old bind finished over new credentials: %v", p)
	}
	_, view := w.do(t, http.MethodGet, "/api/v1/bots/feishu", "")
	if view["app_id"] != "cli_new" || view["owner"] != nil || view["enabled"] != false {
		t.Fatalf("queued old bind changed the new app: %v", view)
	}
	var saved FeishuBotSettings
	if ok, err := w.h.store.Setting(w.ctx, botSettingsKey, &saved); !ok || err != nil || saved.Enabled || !saved.Features.Agents || !saved.Features.ReadGroup {
		t.Fatal("queued old poll or credential PUT changed the disabled state or independent feature saves")
	}
}

func TestBotRegistrationRetiredPollKeepsDisabledBotOff(t *testing.T) {
	needsLarkModule(t)
	for _, resume := range []string{"bind", "settings"} {
		t.Run(resume, func(t *testing.T) {
			w, acc, _ := confirmedRegistrationWorld(t)
			body := fmt.Sprintf(`{"app_id":"cli_new","app_secret":"new-secret","domain":%q,"enabled":false}`, w.api.srv.URL)
			if code, p := w.do(t, http.MethodPut, "/api/v1/bots/feishu", body); code != http.StatusOK || p["enabled"] != false {
				t.Fatalf("replace and disable: %d %v", code, p)
			}
			store := &registrationSettingsStore{Store: w.h.store}
			w.h.store = store
			for range 2 {
				if p := w.bind(t, stepRegister); p["step"] != "credentials" || p["done"] == true {
					t.Fatalf("retired poll did not stop passively: %v", p)
				}
			}
			var saved FeishuBotSettings
			if ok, err := store.Setting(w.ctx, botSettingsKey, &saved); !ok || err != nil || saved.Enabled || saved.Pairing != nil || saved.Registration != nil || saved.AppID != "cli_new" {
				t.Fatal("retired poll changed the disabled settings")
			}
			store.mu.Lock()
			writes := len(store.writes)
			store.mu.Unlock()
			if writes != 0 || len(acc.asked) != 2 {
				t.Fatal("retired poll wrote settings or called the registrar")
			}
			step := ""
			if resume == "settings" {
				if code, p := w.do(t, http.MethodPut, "/api/v1/bots/feishu", `{"enabled":true}`); code != http.StatusOK || p["enabled"] != true {
					t.Fatalf("explicit enable: %d %v", code, p)
				}
				step = stepRegister
			}
			if p := w.bind(t, step); p["step"] != "pair" {
				t.Fatalf("explicit resume did not use configured pairing: %v", p)
			}
			if ok, err := store.Setting(w.ctx, botSettingsKey, &saved); !ok || err != nil || !saved.Enabled || saved.AppID != "cli_new" {
				t.Fatal("explicit resume did not retain the chosen app and enable it")
			}
		})
	}
}
