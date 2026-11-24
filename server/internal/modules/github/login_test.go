package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

type memSecrets struct {
	mu   sync.Mutex
	data []byte
}

func (m *memSecrets) Load(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data, nil
}
func (m *memSecrets) Save(_ context.Context, d []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = d
	return nil
}
func (m *memSecrets) Delete(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = nil
	return nil
}

// The device flow against a made-up GitHub: straight to the code with the GitHub CLI's client ID (no step asks for
// one), waiting, slowing down, the token; it is kept sealed and used before a computer's gh login; signing out
// forgets it.
func TestDeviceLogin(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	polls, codes := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/login/device/code":
			codes++
			if r.Form.Get("client_id") != "178c6fc778ccc68e1d6a" {
				t.Errorf("device code asked with client_id %q", r.Form.Get("client_id"))
			}
			if r.Form.Get("scope") != "repo" {
				t.Errorf("scope %q", r.Form.Get("scope"))
			}
			_, _ = w.Write([]byte(`{"device_code":"dc1","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`))
		case "/login/oauth/access_token":
			if r.Form.Get("client_id") != "178c6fc778ccc68e1d6a" {
				t.Errorf("token asked with client_id %q", r.Form.Get("client_id"))
			}
			polls++
			switch polls {
			case 1:
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			case 2:
				_, _ = w.Write([]byte(`{"error":"slow_down","interval":10}`))
			default:
				_, _ = w.Write([]byte(`{"access_token":"gho_example","token_type":"bearer"}`))
			}
		case "/user":
			if r.Header.Get("Authorization") != "Bearer gho_example" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"login":"example-user"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	sec := &memSecrets{}
	c := New(Config{}, "", nil)
	c.web, c.api = srv.URL, srv.URL
	c.UseSecrets(sec)
	c.check(ctx)
	if st := c.Status(ctx); st.State != connectorwire.StateNotSignedIn || st.Error != notSignedIn || st.Via != "" {
		t.Fatalf("before: %+v", st)
	}

	p, err := c.LoginStep(ctx, "", nil)
	if err != nil || p.Step != "device" || p.Kind != connectorwire.PromptDeviceCode || len(p.Fields) != 0 ||
		p.UserCode != "ABCD-1234" || p.URL != "https://github.com/login/device" || p.Interval != 5 {
		t.Fatalf("first step %+v %v", p, err)
	}
	if !strings.Contains(p.Hint, "GitHub CLI") {
		t.Fatalf("the hint does not say the page shows GitHub CLI: %q", p.Hint)
	}
	if _, err := c.LoginStep(ctx, "client_id", map[string]string{"client_id": "Ov23liExample"}); err == nil {
		t.Fatal("a client_id step is still taken")
	}
	if p, _ = c.LoginStep(ctx, "device", nil); p.Step != "device" || p.Error != "" || p.Interval != 5 {
		t.Fatalf("pending %+v", p)
	}
	if p, _ = c.LoginStep(ctx, "device", nil); p.Interval != 10 {
		t.Fatalf("slow down %+v", p)
	}
	if p, err = c.LoginStep(ctx, "device", nil); err != nil || !p.Done || p.Account != "example-user" {
		t.Fatalf("done %+v %v", p, err)
	}
	if st := c.Status(ctx); st.State != connectorwire.StateOK || st.Via != "设备码登录" {
		t.Fatalf("after: %+v", st)
	}
	var kept deviceLogin
	_ = json.Unmarshal(sec.data, &kept)
	if kept.Token != "gho_example" || strings.Contains(string(sec.data), "client_id") {
		t.Fatalf("kept %s", sec.data)
	}

	// A hub started again signs in from what it kept.
	c2 := New(Config{}, "", nil)
	c2.web, c2.api = srv.URL, srv.URL
	c2.UseSecrets(sec)
	c2.check(ctx)
	if st := c2.Status(ctx); st.State != connectorwire.StateOK {
		t.Fatalf("after a restart: %+v", st)
	}

	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	kept = deviceLogin{}
	_ = json.Unmarshal(sec.data, &kept)
	if kept.Token != "" {
		t.Fatalf("after logout %+v", kept)
	}
	if st := c.Status(ctx); st.State != connectorwire.StateNotSignedIn {
		t.Fatalf("signed out: %+v", st)
	}
	// Signing in again starts at the code.
	if p, _ = c.LoginStep(ctx, "", nil); p.Kind != connectorwire.PromptDeviceCode {
		t.Fatalf("again %+v", p)
	}
	mu.Lock()
	if codes != 2 {
		t.Errorf("device codes asked %d times, want 2", codes)
	}
	mu.Unlock()
	if strings.Contains(string(sec.data), "dc1") {
		t.Fatal("the device code was kept")
	}
}

// A hub from before #1089 kept the user's own OAuth App's client ID next to the token: the token still signs in, the
// client ID is never sent (a new sign-in uses the GitHub CLI's), and the next save drops it.
func TestDeviceLoginOldClientID(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/login/device/code":
			if got := r.Form.Get("client_id"); got != ghCLIClientID {
				t.Errorf("client_id %q", got)
			}
			_, _ = w.Write([]byte(`{"device_code":"dc2","user_code":"WXYZ-9876","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`))
		case "/user":
			if r.Header.Get("Authorization") != "Bearer gho_old" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"login":"example-user"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	sec := &memSecrets{data: []byte(`{"client_id":"Ov23liExample","token":"gho_old"}`)}
	c := New(Config{}, "", nil)
	c.web, c.api = srv.URL, srv.URL
	c.UseSecrets(sec)
	c.check(ctx)
	if st := c.Status(ctx); st.State != connectorwire.StateOK || st.Account != "example-user" {
		t.Fatalf("the old token: %+v", st)
	}
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sec.data), "Ov23liExample") {
		t.Fatalf("the old client ID stayed: %s", sec.data)
	}
	p, err := c.LoginStep(ctx, "", nil)
	if err != nil || p.UserCode != "WXYZ-9876" {
		t.Fatalf("sign in again %+v %v", p, err)
	}
}

// GitHub refusing to give a code is an error, not a step that asks for something.
func TestDeviceLoginRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":"unauthorized_client"}`))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{}, "", nil)
	c.web, c.api = srv.URL, srv.URL
	c.UseSecrets(&memSecrets{})
	p, err := c.LoginStep(context.Background(), "", nil)
	if err == nil || !strings.Contains(err.Error(), "unauthorized_client") || p.Step != "" || len(p.Fields) != 0 {
		t.Fatalf("refused: %+v %v", p, err)
	}
}

// Given back by an extension that held the id (#2830), the connector reads the sign-in again: the extension may have
// signed in under the same id meanwhile.
func TestReloadReadsTheSignInAgain(t *testing.T) {
	ctx := context.Background()
	sec := &memSecrets{}
	c := New(Config{}, "", nil)
	c.UseSecrets(sec)
	if d := c.deviceState(ctx); d.Token != "" {
		t.Fatal("nothing stored yet")
	}
	sec.data = []byte(`{"token":"gho_new"}`) // the extension signed in
	if d := c.deviceState(ctx); d.Token != "" {
		t.Fatal("read once: the old state until reloaded")
	}
	c.Reload()
	if d := c.deviceState(ctx); d.Token != "gho_new" {
		t.Fatalf("after Reload: %+v", d)
	}
}

// Both device-code requests use the web host; transport failures must identify the hub's egress and keep secrets private.
func TestDeviceLoginNetworkFailure(t *testing.T) {
	for _, step := range []string{"", "device"} {
		for _, cause := range []error{context.DeadlineExceeded, errors.New("dial tcp: connection refused"),
			errors.New("proxyconnect tcp: http://proxy-user:proxy-password@proxy.example.com:3128 " + oldToken)} {
			t.Run(step+"/"+cause.Error(), func(t *testing.T) {
				c := New(Config{}, "", nil)
				c.source = SourceDevice
				c.pending = &devicePending{code: "example-code", expires: time.Now().Add(time.Minute)}
				c.client = &http.Client{Transport: responseTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != "github.com" {
						t.Errorf("host = %s", r.URL.Host)
					}
					return nil, cause
				})}
				p, err := c.LoginStep(context.Background(), step, nil)
				if err == nil || p.Done || p.UserCode != "" {
					t.Fatalf("prompt = %+v, error = %v", p, err)
				}
				for _, lang := range i18n.Langs {
					body := apierr.LocalizedBody(http.StatusBadGateway, "", err, lang)
					message := body["error"].(string)
					for _, want := range []string{"github.com", "github.proxy", "manager.json"} {
						if !strings.Contains(message, want) {
							t.Errorf("%s missing %s: %s", lang, want, message)
						}
					}
					for _, secret := range []string{"proxy.example.com", "proxy-user", "proxy-password", "3128", oldToken} {
						if strings.Contains(message, secret) {
							t.Errorf("%s exposed network details", lang)
						}
					}
				}
			})
		}
	}
}
