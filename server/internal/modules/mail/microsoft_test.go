package mail

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-smtp"
)

const testMSClient = "11111111-2222-3333-4444-555555555555"

func fakeMSIDToken(claims map[string]any) string {
	enc := base64.RawURLEncoding.EncodeToString
	raw, _ := json.Marshal(claims)
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc(raw) + "." + enc([]byte("sig"))
}

// fakeMicrosoft serves the token endpoint: PKCE checked against start's challenge, no client secret, a new refresh
// token on every refresh (Microsoft rotates them). refreshes counts the refreshes.
type fakeMicrosoft struct {
	challenge string
	idClaims  map[string]any
	scope     string
	failCode  string // the authorization code exchange fails with this AADSTS description
	refreshes int
}

func (f *fakeMicrosoft) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("client_id") != testMSClient || r.Form.Has("client_secret") || !strings.Contains(r.Form.Get("scope"), "IMAP.AccessAsUser.All") {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_request","error_description":"AADSTS900144: bad request"}`))
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if f.failCode != "" {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": f.failCode + ": something.\r\nTrace ID: x"})
				return
			}
			if base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge || r.Form.Get("code") != "the-code" ||
				r.Form.Get("redirect_uri") != DefaultMicrosoftRedirect {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant","error_description":"AADSTS50011: redirect mismatch"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "refresh_token": "rt-1", "expires_in": 3599,
				"scope": f.scope, "id_token": fakeMSIDToken(f.idClaims)})
		case "refresh_token":
			f.refreshes++
			want := "rt-" + strconv.Itoa(f.refreshes)
			if r.Form.Get("refresh_token") != want {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant","error_description":"AADSTS70008: The refresh token has expired."}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at-" + strconv.Itoa(f.refreshes+1),
				"refresh_token": "rt-" + strconv.Itoa(f.refreshes+1), "expires_in": 3599})
		}
	}))
	t.Cleanup(srv.Close)
	old := microsoftTokenURL
	microsoftTokenURL = srv.URL
	t.Cleanup(func() { microsoftTokenURL = old })
}

const outlookScope = "https://outlook.office.com/IMAP.AccessAsUser.All https://outlook.office.com/SMTP.Send openid email"

func startMicrosoft(t *testing.T, s *Service) (state, challenge string) {
	t.Helper()
	st, err := s.StartMicrosoftOAuth(context.Background(), "Me@Example.com")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	u, _ := url.Parse(st.AuthURL)
	q := u.Query()
	if u.Host != "login.microsoftonline.com" || !strings.HasPrefix(u.Path, "/common/") || st.CallbackScheme != "persona-mail-oauth" ||
		q.Get("redirect_uri") != DefaultMicrosoftRedirect || q.Get("client_id") != testMSClient || q.Get("code_challenge_method") != "S256" ||
		!strings.Contains(q.Get("scope"), "offline_access") || q.Get("state") != st.State || q.Get("login_hint") != "me@example.com" {
		t.Fatalf("auth url = %s, scheme = %s", st.AuthURL, st.CallbackScheme)
	}
	return st.State, q.Get("code_challenge")
}

// The client ID is the user's own: none at first (start says what to do), one entered in the app is kept
// encrypted and wins over hub.json's default.
func TestMicrosoftClientID(t *testing.T) {
	s, recs := newOAuthService(t)
	ctx := context.Background()
	if c := s.MicrosoftConfig(ctx); c.Configured || c.RedirectURI != DefaultMicrosoftRedirect {
		t.Fatalf("config %+v", c)
	}
	if _, err := s.StartMicrosoftOAuth(ctx, ""); err == nil || statusFor(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "client ID") {
		t.Fatalf("start without a client: %v", err)
	}
	if err := s.SetMicrosoftClientID(ctx, "not-a-guid"); err == nil || statusFor(err) != http.StatusBadRequest {
		t.Fatalf("bad client id: %v", err)
	}
	s.SetMicrosoft("99999999-8888-7777-6666-555555555555", "")
	if c := s.MicrosoftConfig(ctx); c.Source != "config" {
		t.Fatalf("hub.json default: %+v", c)
	}
	if err := s.SetMicrosoftClientID(ctx, " "+testMSClient+" "); err != nil {
		t.Fatal(err)
	}
	if c := s.MicrosoftConfig(ctx); !c.Configured || c.ClientID != testMSClient || c.Source != "app" {
		t.Fatalf("entered: %+v", c)
	}
	if strings.Contains(recs.dump(), testMSClient) {
		t.Fatal("the client ID is stored in plain text")
	}
	// Another start of the hub reads it back.
	s2, err := New(s.dir, recs)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if c := s2.MicrosoftConfig(ctx); c.ClientID != testMSClient {
		t.Fatalf("reloaded: %+v", c)
	}
}

// Sign in with a personal account (email claim) and a work account (preferred_username): Outlook's servers,
// XOAUTH2, credentials encrypted; each refresh's new refresh token is kept, also across a restart.
func TestMicrosoftSignInAndRotation(t *testing.T) {
	for _, claims := range []map[string]any{{"email": "Me@Example.com"}, {"preferred_username": "me@example.com"}} {
		s, recs := newOAuthService(t)
		ctx := context.Background()
		if err := s.SetMicrosoftClientID(ctx, testMSClient); err != nil {
			t.Fatal(err)
		}
		f := &fakeMicrosoft{idClaims: claims, scope: outlookScope}
		f.serve(t)
		state, ch := startMicrosoft(t, s)
		f.challenge = ch
		a, err := s.FinishMicrosoftOAuth(ctx, OAuthFinishRequest{State: state, Code: "the-code"})
		if err != nil {
			t.Fatalf("finish %v: %v", claims, err)
		}
		if a.Email != "me@example.com" || a.Provider != ProviderOutlook || a.Auth != AuthOAuth2 || a.IMAPHost != "outlook.office365.com" ||
			a.IMAPPort != 993 || a.SMTPHost != "smtp.office365.com" || a.SMTPPort != 587 || a.SMTPSecurity != "starttls" || a.OAuth != nil {
			t.Fatalf("account %+v", a)
		}
		if strings.Contains(recs.dump(), "rt-1") {
			t.Fatal("refresh token stored in plain text")
		}
		got, _ := s.account(a.ID)
		if got.OAuth.Provider != oauthMicrosoft || got.OAuth.ClientID != testMSClient {
			t.Fatalf("creds %+v", got.OAuth)
		}
		// Expire the cached access token: the refresh returns rt-2, which is saved.
		s.accessEntry(got.OAuth).expires = s.now()
		if tok, err := s.oauthToken(ctx, got); err != nil || tok != "at-2" {
			t.Fatalf("refresh: %q %v", tok, err)
		}
		s2, err := New(s.dir, recs)
		if err != nil {
			t.Fatal(err)
		}
		reloaded, _ := s2.account(a.ID)
		if reloaded.OAuth.RefreshToken != "rt-2" {
			t.Fatalf("the rotated refresh token was not kept: %q", reloaded.OAuth.RefreshToken)
		}
		if tok, err := s2.oauthToken(ctx, reloaded); err != nil || tok != "at-3" {
			t.Fatalf("refresh after a restart: %q %v", tok, err)
		}
		s2.Close()
	}
}

// What the user is told when Microsoft or Exchange says no.
func TestMicrosoftSignInOnDomainsWithIMAPPresets(t *testing.T) {
	for _, domain := range []string{"outlook.com", "hotmail.com", "live.com", "company.example"} {
		req := outlookServers
		req.Email, req.Provider, req.Auth = "me@"+domain, ProviderOutlook, AuthOAuth2
		a, err := buildAccount(req)
		if err != nil {
			t.Fatalf("%s: %v", domain, err)
		}
		if a.Provider != ProviderOutlook || a.Auth != AuthOAuth2 || a.SMTPHost != outlookServers.SMTPHost {
			t.Errorf("%s: provider %q, auth %q, smtp %q", domain, a.Provider, a.Auth, a.SMTPHost)
		}
	}
}

func TestMicrosoftErrorsSayWhatToDo(t *testing.T) {
	for _, tc := range []struct {
		code, desc string
		refreshing bool
		want       string
	}{
		{"invalid_grant", "AADSTS65001: The user or administrator has not consented", false, "IT 管理员"},
		{"access_denied", "AADSTS90094: Admin consent is required", false, "管理员同意"},
		{"access_denied", "", false, "没有同意授权"},
		{"unauthorized_client", "AADSTS700016: Application with identifier was not found", false, "支持的帐户类型"},
		{"invalid_request", "AADSTS50011: The redirect URI does not match", false, DefaultMicrosoftRedirect},
		{"invalid_client", "AADSTS7000218: The request body must contain client_assertion", false, "允许公共客户端流"},
		{"invalid_grant", "AADSTS70008: The refresh token has expired", true, "重新"},
		{"invalid_grant", "AADSTS50173: The provided grant has expired due to it being revoked", true, "使用 Microsoft 账号登录"},
		{"temporarily_unavailable", "AADSTS90002: Tenant not found", false, "Microsoft 登录失败：temporarily_unavailable：AADSTS90002"},
	} {
		if err := microsoftError(tc.code, tc.desc, DefaultMicrosoftRedirect, tc.refreshing); !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %q → %v, want %q", tc.code, tc.desc, err, tc.want)
		}
	}
	for _, tc := range []struct{ err, want string }{
		{"imap: XOAUTH2: AUTHENTICATE failed.", "打开「允许设备和应用使用 IMAP」"},
		{"smtp: 535 5.7.139 Authentication unsuccessful, SmtpClientAuthentication is disabled for the Tenant.", "经过身份验证的 SMTP"},
		{"imap: dial: connection refused", "imap: dial: connection refused"},
	} {
		if got := outlookHint(errors.New(tc.err)); !strings.Contains(got.Error(), tc.want) {
			t.Errorf("%q → %v", tc.err, got)
		}
	}

	// A sign-in the admin has to approve fails before anything is saved; so does one without mail permissions.
	s, _ := newOAuthService(t)
	ctx := context.Background()
	_ = s.SetMicrosoftClientID(ctx, testMSClient)
	f := &fakeMicrosoft{idClaims: map[string]any{"email": "me@example.com"}, scope: outlookScope}
	f.serve(t)
	state, _ := startMicrosoft(t, s)
	if _, err := s.FinishMicrosoftOAuth(ctx, OAuthFinishRequest{State: state, Error: "access_denied",
		ErrorDescription: "AADSTS90094: Admin consent is required for the permissions requested."}); err == nil || !strings.Contains(err.Error(), "管理员同意") {
		t.Fatalf("admin consent: %v", err)
	}
	f.scope = "openid email"
	state, f.challenge = startMicrosoft(t, s)
	if _, err := s.FinishMicrosoftOAuth(ctx, OAuthFinishRequest{State: state, Code: "the-code"}); err == nil || !strings.Contains(err.Error(), "API 权限") {
		t.Fatalf("no mail scope: %v", err)
	}
	if accts, _ := s.ListAccounts(); len(accts) != 0 {
		t.Fatalf("saved %v", accts)
	}
}

func TestMicrosoftHTTP(t *testing.T) {
	s, _ := newOAuthService(t)
	h := s.Handler()
	do := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, out := do("GET", "/api/v1/mail/oauth/microsoft/config", ""); code != 200 || out["configured"] != false || out["redirect_uri"] != DefaultMicrosoftRedirect {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := do("POST", "/api/v1/mail/oauth/microsoft/start", "{}"); code != 400 {
		t.Fatalf("start without a client: %d", code)
	}
	if code, out := do("PUT", "/api/v1/mail/oauth/microsoft/config", `{"client_id":"`+testMSClient+`"}`); code != 200 || out["source"] != "app" {
		t.Fatalf("%d %v", code, out)
	}
	if code, out := do("POST", "/api/v1/mail/oauth/microsoft/start", `{"login_hint":"me@example.com"}`); code != 200 || out["callback_scheme"] != "persona-mail-oauth" {
		t.Fatalf("%d %v", code, out)
	}
}

// An Outlook account sends over SMTP with XOAUTH2 and nothing is appended to Sent (Exchange files it itself); a
// company that turned SMTP AUTH off gets told what to ask for.
func TestOutlookSendsOverSMTPWithXOAUTH2(t *testing.T) {
	srv := newTestIMAP(t)
	fs := newFakeSMTPServer(t)
	fs.token = "at-ok"
	s := newServiceWithAccount(t, srv)
	s.refreshAccess = func(*OAuthCreds) (string, error) { return "at-ok", nil }
	host, port, _ := net.SplitHostPort(fs.addr)
	a := srv.testAccount()
	a.Provider, a.Auth, a.Password = ProviderOutlook, AuthOAuth2, ""
	a.OAuth = &OAuthCreds{Provider: oauthMicrosoft, ClientID: testMSClient, RefreshToken: "rt"}
	a.SMTPHost, a.SMTPSecurity = host, "none"
	a.SMTPPort, _ = strconv.Atoi(port)
	s.mu.Lock()
	s.accts[a.ID] = a
	s.mu.Unlock()
	ctx := context.Background()
	if err := s.Send(ctx, SendRequest{AccountID: a.ID, To: []Address{{Email: "li@example.com"}}, Subject: "你好", Text: "正文"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if fs.authUser != a.Username {
		t.Fatalf("XOAUTH2 user %q", fs.authUser)
	}
	// Read Sent with the test server's password (it does not speak XOAUTH2): still empty.
	check := srv.testAccount()
	check.ID = "acc_check"
	s.mu.Lock()
	s.accts[check.ID] = check
	s.mu.Unlock()
	if res, err := s.ListMessages(ctx, ListOptions{AccountID: check.ID, Mailbox: "Sent"}); err != nil || len(res.Messages) != 0 {
		t.Fatalf("appended to Sent: %v %v", res, err)
	}
	fs.authErr = &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 139},
		Message: "Authentication unsuccessful, SmtpClientAuthentication is disabled for the Tenant."}
	if err := s.Send(ctx, SendRequest{AccountID: a.ID, To: []Address{{Email: "li@example.com"}}, Subject: "x", Text: "y"}); err == nil ||
		!strings.Contains(err.Error(), "经过身份验证的 SMTP") {
		t.Fatalf("SMTP AUTH off: %v", err)
	}
}
