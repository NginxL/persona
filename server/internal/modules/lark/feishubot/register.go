package feishubot

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Creating the app in one authorization (docs/extensions/bots.md "Pairing and permissions"): the accounts site's
// device-code registration makes a personal-agent app with the bot, its scopes and the card callback, and hands back
// its App ID and Secret together with the open id of the person who confirmed it. That person becomes the owner, so
// no console visit, no copy-paste and no pairing code. No token is needed: the device code is the only credential.

const (
	registrationPath = "/oauth/v1/app/registration"
)

// registerScopes are the tenant scopes the new app asks for beyond the platform's template: what the bot reads and
// sends in the owner's private chat. Group messaging stays the user's later decision, as with AutoConfigure.
var registerScopes = append([]string{"im:message", "im:message.p2p_msg:readonly", "im:resource"}, configureScopes...)

// Registrar talks to the accounts site of one cloud ("feishu" or "lark", AccountsHost).
type Registrar struct {
	http *http.Client
	host func(region string) string // replaced in tests
}

func NewRegistrar() *Registrar {
	return &Registrar{http: &http.Client{Timeout: 30 * time.Second}, host: AccountsHost}
}

// WithHost points every region at one server (tests).
func (r *Registrar) WithHost(host string) *Registrar {
	r.host = func(string) string { return host }
	return r
}

// Begin starts a registration of an app named name in region.
func (r *Registrar) Begin(ctx context.Context, region, name string) (RegisterChallenge, error) {
	var out struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpireIn                int    `json:"expire_in"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
		Error                   string `json:"error"`
		ErrorDescription        string `json:"error_description"`
	}
	err := r.post(ctx, region, url.Values{
		"action":            {"begin"},
		"archetype":         {"PersonalAgent"},
		"auth_method":       {"client_secret"},
		"request_user_info": {"open_id tenant_brand"},
	}, &out)
	if err != nil {
		return RegisterChallenge{}, err
	}
	if out.Error != "" {
		return RegisterChallenge{}, fmt.Errorf("registration: %s", cmp.Or(out.ErrorDescription, out.Error))
	}
	raw := strings.TrimSpace(out.VerificationURIComplete)
	if raw == "" && out.VerificationURI != "" && out.UserCode != "" {
		raw = strings.TrimRight(out.VerificationURI, "/") + "?user_code=" + url.QueryEscape(out.UserCode)
	}
	if strings.TrimSpace(out.DeviceCode) == "" || raw == "" {
		return RegisterChallenge{}, fmt.Errorf("registration: no device code or page in the answer")
	}
	page, err := registrationPage(raw, name)
	if err != nil {
		return RegisterChallenge{}, err
	}
	expires := out.ExpiresIn
	if out.ExpireIn > 0 {
		expires = out.ExpireIn
	}
	return RegisterChallenge{DeviceCode: strings.TrimSpace(out.DeviceCode), URL: page, ExpiresIn: expires, Interval: out.Interval}, nil
}

// Poll asks once whether the registration in region was confirmed.
func (r *Registrar) Poll(ctx context.Context, region, deviceCode string) (RegisterTicket, error) {
	var out struct {
		ClientID         string `json:"client_id"`
		ClientSecret     string `json:"client_secret"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		UserInfo         struct {
			OpenID      string `json:"open_id"`
			TenantBrand string `json:"tenant_brand"`
		} `json:"user_info"`
	}
	if err := r.post(ctx, region, url.Values{"action": {"poll"}, "device_code": {deviceCode}}, &out); err != nil {
		return RegisterTicket{}, err
	}
	t := RegisterTicket{AppID: strings.TrimSpace(out.ClientID), AppSecret: strings.TrimSpace(out.ClientSecret),
		OpenID: strings.TrimSpace(out.UserInfo.OpenID), Brand: strings.TrimSpace(out.UserInfo.TenantBrand), ErrorText: out.ErrorDescription}
	switch out.Error {
	case "":
		t.Status = RegisterPending // confirmed, but the credentials may still be on their way
		if t.AppID != "" && t.AppSecret != "" && t.OpenID != "" {
			t.Status = RegisterDone
		}
	case "authorization_pending":
		t.Status = RegisterPending
	case "slow_down":
		t.Status = RegisterSlowDown
	case "expired_token":
		t.Status = RegisterExpired
	case "access_denied":
		t.Status = RegisterDenied
	default:
		t.Status = RegisterFailed
		t.ErrorText = cmp.Or(out.ErrorDescription, out.Error)
	}
	return t, nil
}

func (r *Registrar) post(ctx context.Context, region string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.host(region)+registrationPath, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// The answer is JSON on errors too (an OAuth-style {error, error_description} with a 4xx status).
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("registration: HTTP %d: %w", resp.StatusCode, err)
	}
	return nil
}

// registrationPage adds the app's name and what it asks for beyond the template (addons: the tenant scopes and the
// card callback, gzip'd JSON in base64url) to the authorization page's address.
func registrationPage(raw, name string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for k, v := range map[string]string{"from": "sdk", "tp": "sdk", "name": name} {
		if q.Get(k) == "" && v != "" {
			q.Set(k, v)
		}
	}
	if q.Get("addons") == "" { // one the platform put there itself is left as it is
		addons, err := encodeAddons(map[string]any{
			"scopes":    map[string]any{"tenant": registerScopes},
			"callbacks": map[string]any{"items": []string{"card.action.trigger"}},
		})
		if err != nil {
			return "", err
		}
		q.Set("addons", addons)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func encodeAddons(v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}
