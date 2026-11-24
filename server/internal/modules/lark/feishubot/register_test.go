package feishubot

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Begin asks for a personal-agent app with the owner's open id, and the page it gives carries the app's name and
// the scopes and card callback the bot needs.
func TestRegistrationBeginAsksForTheOwnerAndTheBotsScopes(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != registrationPath {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		form = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dc_1", "user_code": "UC", "verification_uri": "https://accounts.example.com/verify",
			"expire_in": 600, "interval": 5})
	}))
	defer srv.Close()
	ch, err := NewRegistrar().WithHost(srv.URL).Begin(context.Background(), "feishu", "Persona")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"action": "begin", "archetype": "PersonalAgent", "auth_method": "client_secret", "request_user_info": "open_id tenant_brand"} {
		if got := form.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if ch.DeviceCode != "dc_1" || ch.ExpiresIn != 600 || ch.Interval != 5 {
		t.Fatalf("challenge: %+v", ch)
	}
	u, err := url.Parse(ch.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("user_code") != "UC" || q.Get("name") != "Persona" || q.Get("from") != "sdk" {
		t.Fatalf("page: %s", ch.URL)
	}
	raw, err := base64.RawURLEncoding.DecodeString(q.Get("addons"))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(zr)
	var addons struct {
		Scopes struct {
			Tenant []string `json:"tenant"`
		} `json:"scopes"`
		Callbacks struct {
			Items []string `json:"items"`
		} `json:"callbacks"`
	}
	if err := json.Unmarshal(payload, &addons); err != nil {
		t.Fatal(err)
	}
	if len(addons.Scopes.Tenant) != len(registerScopes) || len(addons.Callbacks.Items) != 1 || addons.Callbacks.Items[0] != "card.action.trigger" {
		t.Fatalf("addons: %s", payload)
	}
}

// Poll maps the platform's OAuth-style answers; a confirmation without the credentials yet is still pending.
func TestRegistrationPollStatuses(t *testing.T) {
	cases := []struct {
		body, want string
	}{
		{`{"error":"authorization_pending"}`, RegisterPending},
		{`{"error":"slow_down"}`, RegisterSlowDown},
		{`{"error":"expired_token"}`, RegisterExpired},
		{`{"error":"access_denied"}`, RegisterDenied},
		{`{"error":"tenant_mismatch","user_info":{"tenant_brand":"lark"}}`, RegisterFailed},
		{`{"user_info":{"tenant_brand":"feishu"}}`, RegisterPending},
		{`{"client_id":"cli_new","client_secret":"sec","user_info":{"open_id":"ou_me","tenant_brand":"feishu"}}`, RegisterDone},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			if r.PostForm.Get("action") != "poll" || r.PostForm.Get("device_code") != "dc_1" {
				t.Errorf("poll form: %v", r.PostForm)
			}
			w.WriteHeader(http.StatusBadRequest) // errors come with a 4xx; the JSON is read all the same
			_, _ = io.WriteString(w, c.body)
		}))
		got, err := NewRegistrar().WithHost(srv.URL).Poll(context.Background(), "feishu", "dc_1")
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		if got.Status != c.want {
			t.Errorf("%s: status %q, want %q", c.body, got.Status, c.want)
		}
		if c.want == RegisterDone && (got.AppID != "cli_new" || got.AppSecret != "sec" || got.OpenID != "ou_me") {
			t.Errorf("done: %+v", got)
		}
	}
}

func TestAccountsHost(t *testing.T) {
	if AccountsHost("lark") != AccountsLark || AccountsHost("feishu") != AccountsFeishu || AccountsHost("") != AccountsFeishu {
		t.Fatal("accounts hosts")
	}
}
