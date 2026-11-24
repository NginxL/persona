package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// parsePairLink reads what `personad hub pair` printed: persona://link?… (made for one machine) or persona://pair?…
// (the apps' link; then the machine is named on the command line).
func parsePairLink(raw string) (token string, c Config, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !pairScheme(u.Scheme) || (u.Host != "link" && u.Host != "pair") {
		return "", c, newTextError("link-errors.pair_link_invalid", nil)
	}
	q := u.Query()
	token, c.Hub, c.FP, c.Machine = q.Get("token"), q.Get("hub"), q.Get("fp"), q.Get("machine")
	if token == "" {
		return "", c, newTextError("link-errors.pair_token_missing", nil)
	}
	if c.FP != "" {
		// The hub's own TLS port, pinned: url names the host, tls the port (the hub serves the API at its root).
		port := q.Get("tls")
		if port == "" {
			return "", c, newTextError("link-errors.pair_pin_without_tls", nil)
		}
		if base, err := url.Parse(q.Get("url")); err == nil && base.Hostname() != "" {
			u := "https://" + net.JoinHostPort(base.Hostname(), port)
			c.Endpoints = append(c.Endpoints, Endpoint{URL: u, Kind: kindOf(u)})
		}
	} else {
		// No pin: the hub has no TLS of its own and url is the HTTPS reverse proxy in front of it, path and all.
		e, err := proxyEndpoint(q.Get("url"))
		if err != nil {
			return "", c, newTextError("link-errors.pair_tls_required", nil)
		}
		c.Trust = TrustSystem
		c.Endpoints = append(c.Endpoints, e)
	}
	for _, alt := range q["alt"] {
		if e, err := normalize(alt, nil); err == nil {
			e.Manual = false
			c.Endpoints = append(c.Endpoints, e)
		}
	}
	if len(c.Endpoints) == 0 {
		return "", c, newTextError("link-errors.pair_no_addresses", nil)
	}
	return token, c, nil
}

// proxyEndpoint is the candidate for a hub behind an HTTPS reverse proxy: its https URL with the port spelled out (as
// the hub's own list has it) and the path prefix kept.
func proxyEndpoint(raw string) (Endpoint, error) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "https://") {
		return Endpoint{}, errors.New("not an https URL")
	}
	e, err := normalize(raw, nil)
	if err != nil {
		return Endpoint{}, err
	}
	u, _ := url.Parse(e.URL)
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "443")
	}
	u.Host = strings.ToLower(u.Host)
	return Endpoint{URL: u.String(), Kind: kindOf(u.String())}, nil
}

// Pair redeems a pairing link for this computer's credential and writes link.json. machine names the computer in
// the hub's hub.json when the link does not carry it; name is how the hub lists the credential.
func Pair(ctx context.Context, raw, machine, name, path string) (*Config, error) {
	return PairWithSSH(ctx, raw, machine, name, path, nil)
}

// PairWithSSH is Pair with an optional SSH tunnel: when ssh is non-nil, the pairing request uses the tunnel (alone
// when ssh.Always, or after direct candidates fail) and saves ssh in link.json alongside the credential.
func PairWithSSH(ctx context.Context, raw, machine, name, path string, ssh *SSH, sshBin ...string) (*Config, error) {
	token, c, err := parsePairLink(raw)
	if err != nil {
		return nil, err
	}
	if existing, err := Read(path); err == nil {
		c.Proxy = existing.Proxy // set before pairing (persona-link proxy, pair --proxy): pairing goes through it too
		c.ProxyAlways = existing.ProxyAlways
	}
	if c.Machine != "" && machine != "" && machine != c.Machine {
		return nil, newTextError("link-errors.pair_machine_mismatch", nil, i18n.Args{"expected": strconv.Quote(c.Machine), "actual": strconv.Quote(machine)})
	}
	if c.Machine == "" {
		c.Machine = machine
	}
	if c.Machine == "" {
		return nil, newTextError("link-errors.pair_machine_required", nil)
	}
	var bin string
	if len(sshBin) > 0 {
		bin = sshBin[0]
	}
	if ssh != nil {
		norm, err := NormalizeSSH(ssh.Server, ssh.Target, ssh.Always, c.Endpoints)
		if err != nil {
			return nil, err
		}
		if norm.Always && c.ProxyAlways { // as SetSSH: only one route can be the only one
			return nil, ErrSSHAlwaysProxyAlways
		}
		c.SSH = norm
	}
	// system_name: a hub that has no name of its own for this computer yet names it so (docs/operations/link.md "The
	// computer's name").
	body, _ := json.Marshal(map[string]string{"name": name, "kind": "link", "machine": c.Machine, "system_name": SystemName()})
	type redeemed struct {
		Token string `json:"token"`
		Role  string `json:"role"`
	}
	redeemOne := func(ctx context.Context, client *http.Client, baseURL string) (redeemed, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/auth/tokens", bytes.NewReader(body))
		if err != nil {
			return redeemed{}, err
		}
		req.Header = bearer(token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return redeemed{}, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode != http.StatusOK {
			// With the cause a localized summary moved to detail (as apierr.Describe, #5728).
			var e struct {
				Error  string `json:"error"`
				Detail any    `json:"detail"`
			}
			_ = json.Unmarshal(b, &e)
			why := e.Error
			if d, ok := e.Detail.(string); ok && d != "" && d != why {
				if why != "" {
					why = defaultText("link-errors.hub_rejection_detail", i18n.Args{"error": why, "detail": d})
				} else {
					why = d
				}
			}
			if why == "" {
				why = resp.Status
			}
			return redeemed{}, newTextError("link-errors.hub_rejected", nil, i18n.Args{"error": why})
		}
		var out redeemed
		if err := json.Unmarshal(b, &out); err != nil || out.Token == "" || out.Role != "link" {
			return redeemed{}, newTextError("link-errors.hub_no_credential", nil)
		}
		return out, nil
	}
	// redeemVia races eps, straight to the hub or through outbound proxy p when it is set.
	redeemVia := func(eps []Endpoint, p *url.URL) (redeemed, error) {
		client := hubClient(&c, p, nil)
		defer client.CloseIdleConnections()
		got, _, err := race(ctx, eps, func(ctx context.Context, ep Endpoint) (redeemed, error) {
			return redeemOne(ctx, client, ep.URL)
		}, func(redeemed) {})
		return got, err
	}
	redeemSSH := func() (redeemed, error) {
		for attempt := 0; attempt < 2; attempt++ {
			t, err := startSSHTunnel(bin, *c.SSH)
			if err != nil {
				return redeemed{}, err
			}
			client := tunnelClient(&c, t)
			actx, acancel := context.WithTimeout(ctx, dialTimeout)
			got, err := redeemOne(actx, client, tunnelBaseURL(&c, *c.SSH))
			acancel()
			retryBind := err != nil && attempt == 0 && t.isDone() && t.localBindBusy()
			client.CloseIdleConnections()
			t.close()
			if retryBind {
				continue
			}
			if err != nil {
				var se *SSHError
				if errors.As(err, &se) {
					return redeemed{}, se
				}
				return redeemed{}, &attemptError{ep: Endpoint{URL: c.SSH.Server, Kind: KindSSH}, err: err}
			}
			return got, nil
		}
		return redeemed{}, &SSHError{Code: SSHUnreachable, Server: c.SSH.Server, Target: c.SSH.Target}
	}

	// The link's routes, in its order (routeOrder). A hub that answered (refused the pairing, or has another key) ends
	// it there.
	p, eps, perr := proxyRoute(&c)
	got, err := tryRoutes(routeOrder(&c, p != nil || perr != nil, ""), func(kind string) (redeemed, error) {
		switch kind {
		case KindSSH:
			return redeemSSH()
		case KindProxy:
			if perr != nil {
				return redeemed{}, perr
			}
			return redeemVia(eps, p)
		}
		return redeemVia(c.Endpoints, nil)
	}, func(err error) bool { return ctx.Err() != nil || pairAnswered(err) })
	if err != nil {
		return nil, newTextError("link-errors.pair_failed", err)
	}
	c.Token = got.Token
	err = Update(path, func(old *Config) error {
		var manual []Endpoint
		if old.Hub == c.Hub {
			for _, e := range old.Endpoints {
				if e.Manual {
					manual = append(manual, e)
				}
			}
		}
		keep := c
		keep.Endpoints = append(manual, c.Endpoints...)
		keep.SSH = cloneSSH(c.SSH)
		keep.Proxy, keep.ProxyAlways = old.Proxy, old.ProxyAlways // the computer's network setting, not the pairing's
		// Pairing with a hub's link means dialing it; what passive mode had stays for switching back.
		keep.Mode, keep.Passive = ModeActive, old.Passive
		*old = keep
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// pairAnswered reports whether a direct pairing attempt already reached the hub (which refused the token or had the
// wrong pinned key), so falling back to SSH should not hide a rejected link or wrong hub key.
func pairAnswered(err error) bool {
	return errors.Is(err, errPin) || strings.Contains(err.Error(), "hub 拒绝了：") // i18n-ignore: detects the canonical hub-refusal prefix before presentation
}

// AddAddress adds a candidate typed on this computer (a domain name, say); RemoveAddress takes one out again.
func AddAddress(path, raw string) (Endpoint, error) {
	var added Endpoint
	err := Update(path, func(c *Config) error {
		if c.Token == "" {
			return newTextError("link-errors.not_paired", nil)
		}
		e, err := normalize(raw, c.Endpoints)
		if err != nil {
			return err
		}
		added = e
		c.Endpoints = append([]Endpoint{e}, slicesDelete(c.Endpoints, e.URL)...)
		return nil
	})
	return added, err
}

func RemoveAddress(path, raw string) error {
	return Update(path, func(c *Config) error {
		e, err := normalize(raw, c.Endpoints)
		if err != nil {
			return err
		}
		c.Endpoints = slicesDelete(c.Endpoints, e.URL)
		return nil
	})
}

func slicesDelete(eps []Endpoint, u string) []Endpoint {
	out := eps[:0:0]
	for _, e := range eps {
		if e.URL != u {
			out = append(out, e)
		}
	}
	return out
}
