package mail

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/mailwire"
)

// Outlook "Sign in with Microsoft" (docs/extensions/mail.md "Outlook: Sign In with Microsoft"): the same flow as Google's. The app opens
// Microsoft's sign-in page (ASWebAuthenticationSession) for a public client the user registered in Microsoft Entra
// themselves (no client secret, PKCE), Microsoft redirects to a fixed custom scheme, and the hub exchanges the code
// for a refresh token. IMAP and SMTP authenticate with SASL XOAUTH2. The client ID is not built in: the user enters
// it in the app once (kept encrypted on the hub), or hub.json's microsoft.client_id gives a default.

const (
	ProviderOutlook = "outlook" // personal Outlook.com and work / school Microsoft 365 mailboxes, signed in with Microsoft

	oauthMicrosoft  = "microsoft" // OAuthCreds.Provider
	microsoftScopes = "https://outlook.office.com/IMAP.AccessAsUser.All https://outlook.office.com/SMTP.Send offline_access openid email"
	// DefaultMicrosoftRedirect is the redirect URI every hub uses unless hub.json says otherwise: a custom scheme the
	// app catches with ASWebAuthenticationSession, so no release per hub and nothing in Info.plist.
	DefaultMicrosoftRedirect = "persona-mail-oauth://outlook"

	microsoftClientSetting = mailwire.MicrosoftClientSetting // the client ID the user entered, encrypted (settings table)
)

var (
	microsoftAuthURL  = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
	microsoftTokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"

	clientIDPattern = lazyre.New(`^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$`)
	aadCode         = lazyre.New(`AADSTS\d+`)
)

// outlookServers are Microsoft's IMAP and SMTP endpoints for every Outlook / Microsoft 365 mailbox.
var outlookServers = AddAccountRequest{
	IMAPHost: "outlook.office365.com", IMAPPort: 993,
	SMTPHost: "smtp.office365.com", SMTPPort: 587, SMTPSecurity: "starttls",
}

// userError is a mistake the user fixes (a client ID, a setting in Entra or Exchange), told in words for people:
// HTTP 400.
type userError struct {
	msg   string
	cause error // the server's own answer, if any: kept so isConnError still sees a protocol reply
}

func (e userError) Error() string { return e.msg }

func (e userError) Unwrap() error { return e.cause }

func userErr(msg string) error { return userError{msg: msg} }

// settingRecords is where the hub keeps small settings (Postgres); the mail package encrypts before it.
type settingRecords interface {
	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, v any) error
}

// SetMicrosoft sets hub.json's defaults: a client ID used until the user enters one, and the redirect URI.
func (s *Service) SetMicrosoft(clientID, redirect string) {
	s.msDefaultClientID = strings.TrimSpace(clientID)
	s.msRedirect = strings.TrimSpace(redirect)
	if s.msRedirect == "" {
		s.msRedirect = DefaultMicrosoftRedirect
	}
}

// MicrosoftConfig is GET /api/v1/mail/oauth/microsoft/config: whether Outlook sign-in can start, with which client
// ID (not a secret: it is in every sign-in URL) from where ("app" | "config"), and the redirect URI to register.
type MicrosoftConfig struct {
	Configured  bool   `json:"configured"`
	ClientID    string `json:"client_id,omitempty"`
	Source      string `json:"source,omitempty"`
	RedirectURI string `json:"redirect_uri"`
}

type sealedSetting struct {
	Enc string `json:"enc"`
}

func (s *Service) redirectURI() string {
	if s.msRedirect == "" {
		return DefaultMicrosoftRedirect
	}
	return s.msRedirect
}

// microsoftClient is the client ID in use: the one entered in the app, else hub.json's.
func (s *Service) microsoftClient(ctx context.Context) (id, source string) {
	s.mu.Lock()
	cached, loaded := s.msClientID, s.msClientLoaded
	s.mu.Unlock()
	if !loaded {
		if recs, ok := s.store.recs.(settingRecords); ok {
			var v sealedSetting
			if found, err := recs.Setting(ctx, microsoftClientSetting, &v); err == nil {
				if found && v.Enc != "" {
					if plain, err := s.store.decrypt(v.Enc); err == nil {
						cached = string(plain)
					}
				}
				s.mu.Lock()
				s.msClientID, s.msClientLoaded = cached, true
				s.mu.Unlock()
			}
		}
	}
	if cached != "" {
		return cached, "app"
	}
	if s.msDefaultClientID != "" {
		return s.msDefaultClientID, "config"
	}
	return "", ""
}

// MicrosoftConfig reports how Outlook sign-in is set up.
func (s *Service) MicrosoftConfig(ctx context.Context) MicrosoftConfig {
	id, src := s.microsoftClient(ctx)
	return MicrosoftConfig{Configured: id != "", ClientID: id, Source: src, RedirectURI: s.redirectURI()}
}

// SetMicrosoftClientID keeps the client ID the user registered (encrypted on the hub); "" forgets it.
func (s *Service) SetMicrosoftClientID(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id != "" && !clientIDPattern.MatchString(id) {
		return userErr("这不像 client ID：Entra 应用「概述」页的「应用程序(客户端) ID」，形如 xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx")
	}
	recs, ok := s.store.recs.(settingRecords)
	if !ok {
		return errors.New("mail: this hub cannot keep settings")
	}
	v := sealedSetting{}
	if id != "" {
		enc, err := s.store.encrypt([]byte(id))
		if err != nil {
			return err
		}
		v.Enc = enc
	}
	if err := recs.PutSetting(ctx, microsoftClientSetting, v); err != nil {
		return err
	}
	s.mu.Lock()
	s.msClientID, s.msClientLoaded = id, true
	s.mu.Unlock()
	return nil
}

// StartMicrosoftOAuth begins a "Sign in with Microsoft" flow. loginHint preselects an account.
func (s *Service) StartMicrosoftOAuth(ctx context.Context, loginHint string) (*OAuthStart, error) {
	clientID, _ := s.microsoftClient(ctx)
	if clientID == "" {
		return nil, userErr("还没有 Outlook 登录用的 client ID：先在 App 里填上你在 Microsoft Entra 注册的应用的 client ID（docs/extensions/mail.md「Outlook 接入方式」）")
	}
	verifier := randomURLSafe(48)
	state, err := s.sealState(oauthState{Verifier: verifier, Created: s.now().Unix(), ClientID: clientID})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	redirect := s.redirectURI()
	q := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"response_mode":         {"query"},
		"redirect_uri":          {redirect},
		"scope":                 {microsoftScopes},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
	}
	if hint := normalizeEmail(loginHint); hint != "" {
		q.Set("login_hint", hint)
	}
	scheme, _, _ := strings.Cut(redirect, ":")
	return &OAuthStart{AuthURL: microsoftAuthURL + "?" + q.Encode(), CallbackScheme: scheme, State: state}, nil
}

// FinishMicrosoftOAuth exchanges the code, checks IMAP and SMTP with the token and saves the account; signing in
// again with an account already added replaces its credentials.
func (s *Service) FinishMicrosoftOAuth(ctx context.Context, req OAuthFinishRequest) (*MailAccount, error) {
	st, err := s.claimState(req.State)
	if err != nil {
		return nil, err
	}
	if req.Error != "" {
		return nil, microsoftError(req.Error, req.ErrorDescription, s.redirectURI(), false)
	}
	if req.Code == "" {
		return nil, errors.New("code is required")
	}
	clientID := st.ClientID
	if clientID == "" {
		clientID, _ = s.microsoftClient(ctx)
	}
	tr, err := s.microsoftToken(ctx, url.Values{
		"client_id":     {clientID},
		"code":          {req.Code},
		"code_verifier": {st.Verifier},
		"redirect_uri":  {s.redirectURI()},
		"grant_type":    {"authorization_code"},
		"scope":         {microsoftScopes},
	})
	if err != nil {
		return nil, err
	}
	if tr.RefreshToken == "" {
		return nil, userErr("Microsoft 没有给 refresh token：重新登录一次（权限里要有 offline_access）")
	}
	if !strings.Contains(tr.Scope, "IMAP.AccessAsUser.All") {
		return nil, userErr("没有拿到读邮件的权限：在 Entra 应用的「API 权限」里加上 IMAP.AccessAsUser.All 和 SMTP.Send，再登录一次")
	}
	email, err := microsoftEmail(tr.IDToken)
	if err != nil {
		return nil, err
	}
	areq := outlookServers
	areq.Email, areq.Provider, areq.Auth = email, ProviderOutlook, AuthOAuth2
	a, err := buildAccount(areq)
	if err != nil {
		return nil, err
	}
	a.OAuth = &OAuthCreds{Provider: oauthMicrosoft, ClientID: clientID, RefreshToken: tr.RefreshToken}
	s.storeAccess(a.OAuth, tr.AccessToken, tr.ExpiresIn)
	if err := s.testAccount(ctx, a); err != nil {
		return nil, outlookHint(err)
	}
	return s.upsertAccount(a)
}

// microsoftToken calls Microsoft's token endpoint; its errors come back in words the user can act on.
func (s *Service) microsoftToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, microsoftTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 Microsoft 的登录服务：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oauth: read token response: %w", err)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("oauth: parse token response (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		return nil, microsoftError(tr.Error, tr.ErrorDesc, s.redirectURI(), form.Get("grant_type") == "refresh_token")
	}
	return &tr, nil
}

// microsoftError turns an error of Microsoft's sign-in (the redirect's error, the token endpoint's) into what to do
// about it; refreshing says the sign-in itself was fine before and is now refused.
func microsoftError(code, desc, redirect string, refreshing bool) error {
	aad := aadCode.FindString(desc)
	switch {
	case aad == "AADSTS65001" || aad == "AADSTS90094" || aad == "AADSTS90095" || aad == "AADSTS900941" || code == "consent_required":
		return userErr("你的工作账号不能自己授权第三方应用读邮箱，要公司的 IT 管理员同意：请管理员在 Microsoft Entra 里为这个应用「授予管理员同意」，" +
			"或者给你开通用户同意；也可以改用个人 Outlook 账号")
	case aad == "AADSTS65004" || code == "access_denied" && aad == "":
		return userErr("你在 Microsoft 的页面上没有同意授权，账号没有添加。要添加的话再点一次「使用 Microsoft 账号登录」并同意")
	case aad == "AADSTS700016" || aad == "AADSTS50194" || aad == "AADSTS700054":
		return userErr("Microsoft 找不到这个 client ID 能用的应用：检查 App 里填的 client ID；注册应用时「支持的帐户类型」要选" +
			"「任何组织目录中的帐户和个人 Microsoft 帐户」")
	case aad == "AADSTS50011":
		return userErr("回调地址对不上：在 Entra 应用的「身份验证 › 移动和桌面应用程序」里加上 " + redirect)
	case aad == "AADSTS7000218":
		return userErr("应用没打开「允许公共客户端流」：在 Entra 应用的「身份验证 › 高级设置」里把它设为「是」")
	case refreshing && (code == "invalid_grant" || aad == "AADSTS70008" || aad == "AADSTS700082" || aad == "AADSTS50173" ||
		aad == "AADSTS50076" || aad == "AADSTS50078" || aad == "AADSTS50079"):
		return userErr("Outlook 的授权过期或被撤销了（改过密码、太久没用、或公司要求重新验证）：在 App 里再点一次" +
			"「使用 Microsoft 账号登录」选同一个邮箱，账号会原地更新")
	}
	msg := code
	if first, _, _ := strings.Cut(strings.TrimSpace(desc), "\n"); first != "" {
		msg += "：" + first
	}
	if msg == "" {
		msg = "未知错误"
	}
	return fmt.Errorf("Microsoft 登录失败：%s", msg)
}

// microsoftEmail reads the mailbox address from the ID token (it came straight from Microsoft's token endpoint over
// TLS, so it needs no signature check): email, else the sign-in name of a work account.
func microsoftEmail(idToken string) (string, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", errors.New("oauth: Microsoft returned no ID token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("oauth: decode ID token: %w", err)
	}
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("oauth: parse ID token: %w", err)
	}
	email := claims.Email
	if !strings.Contains(email, "@") {
		email = claims.PreferredUsername
	}
	if !strings.Contains(email, "@") {
		return "", userErr("Microsoft 没有给出这个账号的邮箱地址：权限里要有 openid 和 email")
	}
	return normalizeEmail(email), nil
}

// outlookHint explains Exchange's refusals of an Outlook account with a good token: IMAP or SMTP AUTH turned off
// for the mailbox or the whole company.
func outlookHint(err error) error {
	if err == nil {
		return nil
	}
	m := err.Error()
	switch {
	case strings.Contains(m, "SmtpClientAuthentication is disabled") || strings.Contains(m, "5.7.139") || strings.Contains(m, "5.7.3"):
		return fmt.Errorf("这个邮箱不允许用 SMTP 认证发信（公司关了 SMTP AUTH）：请 IT 管理员在 Microsoft 365 管理中心为你的邮箱打开"+
			"「经过身份验证的 SMTP」，收信不受影响（%w）", err)
	case strings.HasPrefix(m, "imap") && (strings.Contains(m, "AUTHENTICATE failed") || strings.Contains(m, "XOAUTH2")):
		return fmt.Errorf("Outlook 拒绝了 IMAP 登录，多半是这个邮箱关了 IMAP：个人账号在 outlook.com 的「设置 › 邮件 › 转发和 IMAP」里打开"+
			"「允许设备和应用使用 IMAP」；工作账号请 IT 管理员在 Exchange 管理中心为你的邮箱启用 IMAP（%w）", err)
	}
	return err
}

// claimState opens a sign-in's state; each is good for one attempt within oauthPendingTTL.
func (s *Service) claimState(state string) (*oauthState, error) {
	st, err := s.openState(state)
	if err != nil || s.now().Sub(time.Unix(st.Created, 0)) > oauthPendingTTL {
		return nil, errors.New("invalid or expired sign-in, start again")
	}
	s.mu.Lock()
	now := s.now()
	for k, at := range s.usedStates {
		if now.Sub(at) > oauthPendingTTL {
			delete(s.usedStates, k)
		}
	}
	_, used := s.usedStates[state]
	s.usedStates[state] = now
	s.mu.Unlock()
	if used {
		return nil, errors.New("invalid or expired sign-in, start again")
	}
	return st, nil
}
