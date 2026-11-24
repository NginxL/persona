package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Signing in from the app with GitHub's device flow (docs/extensions/connectors.md "Login supports"): the hub asks GitHub for a code
// with the GitHub CLI's OAuth App (ghCLIClientID), the user enters it at github.com/login/device, and the token GitHub
// then grants is kept sealed in the hub (connectorwire.Secrets) and used before a computer's gh login. Nobody registers an
// OAuth App; GitHub's authorization page says "GitHub CLI".

// ghCLIClientID is the GitHub CLI's OAuth App: oauthClientID in cli/cli's internal/authflow/flow.go, which its source
// says is "safe to be embedded in version control". The device flow needs no client secret.
const ghCLIClientID = "178c6fc778ccc68e1d6a"

// deviceScopes are what the connector uses (source.go MissingScopes): repo covers reading private repositories'
// PRs, issues, comments, checks and CI logs, searching code and issues, commenting and reviewing, and notifications.
const deviceScopes = "repo"

// deviceLogin is what is kept in the hub's secrets for GitHub. Hubs before #1089 also kept the user's own OAuth
// App's client_id: it is no longer read and is dropped at the next save; a token granted to it still works.
type deviceLogin struct {
	Token string `json:"token,omitempty"`
	// Account is the account the inbox was last read as (account.go), kept with the sign-in so that a restart does
	// not forget it. It is not a secret; this is the one store the connector has.
	Account *accountRecord `json:"account,omitempty"`
	// InboxMessages preserves message identities across a restart, in the same store as the account.
	InboxMessages map[string]inboxMessageRecord `json:"inbox_messages,omitempty"`
}

// devicePending is a device code waiting for the user.
type devicePending struct {
	code, userCode, uri string
	interval            int
	expires             time.Time
}

// UseSecrets gives the connector the hub's store for its sign-in (the device flow's token).
func (c *Connector) UseSecrets(s connectorwire.Secrets) {
	c.mu.Lock()
	c.secrets = s
	c.mu.Unlock()
}

// Reload forgets the sign-in read from the secrets, to read it again: the hub gives the connector back after an
// extension's held its id (#2830), which may have signed in, out or again meanwhile under the same id.
func (c *Connector) Reload() {
	c.mu.Lock()
	c.dev, c.devLoaded = deviceLogin{}, false
	// The account kept there too (restore): whoever held the id may have saved over it, and the check below
	// writes it back when it is gone.
	c.acct.restored, c.acct.kept = false, accountRecord{}
	c.mu.Unlock()
	c.kickCheck()
}

// deviceState is what the secrets keep, read once (again after Reload).
func (c *Connector) deviceState(ctx context.Context) deviceLogin {
	c.mu.Lock()
	s, loaded, d := c.secrets, c.devLoaded, c.dev
	c.mu.Unlock()
	if loaded || s == nil {
		return d
	}
	raw, err := s.Load(ctx)
	if err != nil {
		c.mu.Lock()
		c.devUnreadable = true
		c.mu.Unlock()
		return d
	}
	_ = json.Unmarshal(raw, &d)
	c.mu.Lock()
	c.dev, c.devLoaded, c.devUnreadable = d, true, false
	c.mu.Unlock()
	return d
}

// saveDevice changes what the secrets keep (change runs under c.mu) and saves it, one save at a time so that the
// last change is the one kept.
func (c *Connector) saveDevice(ctx context.Context, change func(*deviceLogin)) error {
	c.saving.Lock()
	defer c.saving.Unlock()
	var s connectorwire.Secrets
	for try := 0; ; try++ {
		c.deviceState(ctx) // what is kept already, so that the save does not drop it
		c.mu.Lock()
		// A store that cannot be read has nothing this save could drop: a new sign-in writes over it, the way out
		// of a secret that no longer opens.
		if s = c.secrets; s == nil || c.devLoaded || c.devUnreadable {
			break
		}
		c.mu.Unlock() // Reload forgot it meanwhile: read again, or the save would drop what is kept there
		if try == 8 {
			return errors.New("the GitHub sign-in store keeps changing")
		}
	}
	change(&c.dev)
	c.devLoaded = true
	raw, _ := json.Marshal(c.dev)
	c.mu.Unlock()
	if s == nil {
		return errors.New("no secrets store")
	}
	return s.Save(ctx, raw)
}

func (p devicePending) prompt(err string) connectorwire.Prompt {
	return connectorwire.Prompt{Step: "device", Kind: connectorwire.PromptDeviceCode, URL: p.uri, UserCode: p.userCode, Interval: p.interval, Error: err,
		Hint: "在 GitHub 的验证页输入这个验证码并授权（授权页上显示的是「GitHub CLI」）；授权后这里会自动继续。"}
}

// LoginStep signs in with the device flow: the code, then waiting for the user.
func (c *Connector) LoginStep(ctx context.Context, step string, _ map[string]string) (connectorwire.Prompt, error) {
	switch step {
	case "":
		return c.startDevice(ctx)
	case "device":
		if c.Source() != SourceDevice {
			return connectorwire.Prompt{Step: "device", Error: "先选「设备码登录」"}, nil
		}
		return c.pollDevice(ctx)
	}
	return connectorwire.Prompt{}, fmt.Errorf("unknown step %q", step)
}

// startDevice asks GitHub for a code for the user to enter.
func (c *Connector) startDevice(ctx context.Context) (connectorwire.Prompt, error) {
	var r struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
	}
	if err := c.postForm(ctx, "/login/device/code", url.Values{"client_id": {ghCLIClientID}, "scope": {deviceScopes}}, &r); err != nil {
		return connectorwire.Prompt{}, err
	}
	if r.DeviceCode == "" {
		why := r.Error
		if why == "" {
			why = "no device code"
		}
		return connectorwire.Prompt{}, fmt.Errorf("GitHub 没给验证码（%s）", why)
	}
	p := devicePending{code: r.DeviceCode, userCode: r.UserCode, uri: r.VerificationURI, interval: max(r.Interval, 5),
		expires: time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)}
	c.mu.Lock()
	c.pending = &p
	c.mu.Unlock()
	return p.prompt(""), nil
}

// pollDevice asks GitHub once whether the user has entered the code; the same step while not yet.
func (c *Connector) pollDevice(ctx context.Context) (connectorwire.Prompt, error) {
	c.mu.Lock()
	p := c.pending
	c.mu.Unlock()
	if p == nil {
		return c.startDevice(ctx)
	}
	if time.Now().After(p.expires) {
		return c.restartDevice(ctx, "验证码过期了，换了一个新的")
	}
	var r struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Interval    int    `json:"interval"`
	}
	err := c.postForm(ctx, "/login/oauth/access_token", url.Values{"client_id": {ghCLIClientID}, "device_code": {p.code},
		"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}, &r)
	if err != nil {
		return connectorwire.Prompt{}, err
	}
	switch {
	case r.AccessToken != "":
	case r.Error == "authorization_pending":
		return p.prompt(""), nil
	case r.Error == "slow_down":
		c.mu.Lock()
		p.interval = max(r.Interval, p.interval+5)
		c.mu.Unlock()
		return p.prompt(""), nil
	case r.Error == "expired_token":
		return c.restartDevice(ctx, "验证码过期了，换了一个新的")
	case r.Error == "access_denied":
		return c.restartDevice(ctx, "在 GitHub 上取消了授权")
	default:
		return c.restartDevice(ctx, "GitHub 没有授权（"+r.Error+"）")
	}
	if err := c.saveDevice(ctx, func(d *deviceLogin) { d.Token = r.AccessToken }); err != nil {
		return connectorwire.Prompt{}, err
	}
	c.mu.Lock()
	c.newCredential(r.AccessToken)
	c.pending = nil
	c.mu.Unlock()
	c.check(ctx)
	st := c.Status(ctx)
	if st.State != connectorwire.StateOK {
		return connectorwire.Prompt{Step: "device", Error: st.Error}, nil
	}
	return connectorwire.Prompt{Done: true, Account: st.Account}, nil
}

func (c *Connector) restartDevice(ctx context.Context, why string) (connectorwire.Prompt, error) {
	p, err := c.startDevice(ctx)
	if err == nil && p.Error == "" {
		p.Error = why
	}
	return p, err
}

// Logout forgets the device flow's token. A computer's gh login is not
// the hub's to end: choosing another source leaves it (source.go).
func (c *Connector) Logout(ctx context.Context) error {
	if err := c.forgetDevice(ctx); err != nil {
		return err
	}
	c.check(ctx)
	return nil
}

func (c *Connector) forgetDevice(ctx context.Context) error {
	had := c.deviceState(ctx).Token != ""
	c.mu.Lock()
	if c.source == SourceDevice {
		c.newCredential("")
	}
	c.pending = nil
	// With the token in memory, in one step: a read between the two would take the kept one for a new sign-in and
	// put it back (tokenNow).
	c.dev.Token = ""
	c.mu.Unlock()
	if !had {
		return nil
	}
	return c.saveDevice(ctx, func(d *deviceLogin) { d.Token = "" })
}

// postForm posts to GitHub's web host (the device flow is not the API's), answers as JSON.
func (c *Connector) postForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.web+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		// Transport errors may contain a proxy address or credentials, which Scrub only masks for GitHub tokens.
		// Keep the remediation, not the underlying network diagnostic. Response read errors remain scrubbed below.
		return i18n.Err("connector-github.device_unreachable")
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("GitHub %s: %d", path, resp.StatusCode)
	}
	if readErr != nil {
		return fmt.Errorf("GitHub %s: read response: %s", path, Scrub(readErr.Error()))
	}
	return json.Unmarshal(raw, out)
}
