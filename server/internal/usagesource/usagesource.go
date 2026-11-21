// Package usagesource reads subscription usage windows from an account-pool gateway (#2595, docs/server/usage-windows.md
// "Account-pool gateways"): a service that holds several agent accounts and already knows how much of each one's 5-hour and
// weekly allowance is used. The hub asks it, read-only, and turns the answer into the proto.UsageAccount list the
// sidebar draws; the node-local collectors (#2432) and the gateway's numbers meet in devices/usagewindows.go.
//
// Three source types:
//
//   - "relay": an account-pool relay's management API (relay.go). The account list and the stored quota windows, both read
//     from its cache: nothing here ever asks it to probe an upstream.
//   - "relay_key": the same relay's key self-service usage (relaykey.go), read with an ordinary API key instead of a
//     management key: the stored windows of the accounts that key can use.
//   - "generic": Persona's own small JSON shape (generic.go), for any other gateway that wants to feed the sidebar.
//
// Nothing here logs, returns or stores the key. An error carries a code, never the gateway's text.
package usagesource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	TypeRelay    = "relay"
	TypeRelayKey = "relay_key"
	TypeGeneric  = "generic"
)

// Known reports whether t is a type a source may have (what the admin page accepts).
//
// TypeExtension is not one of them: it is read from manager.json (usage.sources[]) and has no address of its own.
func Known(t string) bool { return t == TypeRelay || t == TypeRelayKey || t == TypeGeneric }

// StoredType is the type of a source as a saved config (hub.json) names it. Before the relay type had its own name,
// it was saved under the name of the one relay it was first written for; the types here are the only ones there have
// ever been, so any other non-empty name is that earlier one and reads as relay. What a person types on the admin page
// is not read with this: there only the known types are accepted.
func StoredType(t string) string {
	if t == "" || Known(t) || t == TypeExtension {
		return t
	}
	return TypeRelay
}

const (
	// BodyMost caps one answer (a few hundred accounts with their windows are well under a megabyte).
	BodyMost = 8 << 20
	// AccountsMost caps how many accounts one source may bring; the rest are ignored.
	AccountsMost = 500
	// InventoryMost caps the account list one source may bring (Result.Inventory); a longer one is cut, Complete false.
	InventoryMost = 2000
	// WindowsMost caps the windows of one account.
	WindowsMost = 32
	// Timeout bounds one request; a whole fetch is bounded by the caller's context.
	Timeout = 15 * time.Second
)

// Config is one source.
type Config struct {
	Type string
	URL  string
	Key  string
	// Client is the HTTP client (tests); nil makes one with Timeout and no redirects.
	Client *http.Client
}

// Result is what a source brought.
type Result struct {
	// Accounts are the samples the sidebar's usage strip draws: accounts that are not disabled and have a window with
	// a length and a number, at most AccountsMost.
	Accounts []proto.UsageAccount
	// Inventory is every account the gateway listed (at most InventoryMost), disabled ones and those without such a
	// window included, as usage.gateway_accounts gives them (Source not set; docs/server/usage-windows.md"Gateway account inventory").
	// Only the relay type has one; Complete false: the list stopped short.
	Inventory []proto.GatewayAccount
	Complete  bool
	// Seen counts what the gateway reported, for the "test the connection" button: accounts that have windows we can
	// draw, those windows, and how many accounts each provider has.
	Seen Seen
}

type Seen struct {
	Accounts  int            `json:"accounts"`
	Windows   int            `json:"windows"`
	Providers map[string]int `json:"providers"`
	// Listed is how many accounts the gateway listed at all (those without a window we can draw too).
	Listed int `json:"listed"`
	// QuotaHidden: the gateway answered, but shares no account quota with this key (relay_key), so there is nothing
	// to list whatever accounts the key can use.
	QuotaHidden bool `json:"quota_hidden,omitempty"`
}

// Error codes. The admin UI words them; the text of what the gateway said is never kept.
const (
	CodeUnauthorized = "unauthorized" // 401: the key is wrong or expired
	CodeForbidden    = "forbidden"    // 403: the key may not read this (a scoped key without the route)
	CodeNotFound     = "not_found"    // 404: not this kind of gateway, or an older one without the route
	CodeUnreachable  = "unreachable"  // could not connect
	CodeTimeout      = "timeout"
	CodeBadResponse  = "bad_response" // not the JSON we expect
	CodeTooLarge     = "too_large"
	CodeRedirect     = "redirect" // redirects are not followed: the key would go to another address
	CodeServer       = "server_error"
	CodeBadConfig    = "bad_config"
	CodePending      = "pending" // not read yet (just added, or the hub just started): no data, not "no accounts"
)

// Error is a failed read. Detail is for logs and tests only: it never holds the key or the gateway's body.
type Error struct {
	Code   string
	Status int
	Detail string
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s (HTTP %d)", e.Code, e.Status)
	}
	if e.Detail != "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code
}

// CodeOf is the error code of err ("unreachable" for one that is not an *Error).
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeTimeout
	}
	return CodeUnreachable
}

// Fetch reads the source once. Read-only by construction: the relay adapter sends two calls, a GET and a POST that
// only reads (refresh is always false); the relay_key and generic ones a GET.
func Fetch(ctx context.Context, cfg Config, now time.Time) (Result, error) {
	base, err := NormalizeURL(cfg.URL)
	if err != nil {
		return Result{}, err
	}
	c := &client{base: base, key: cfg.Key, http: cfg.Client}
	if c.http == nil {
		c.http = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	switch cfg.Type {
	case TypeRelay:
		return fetchRelay(ctx, c, now)
	case TypeRelayKey:
		return fetchRelayKey(ctx, c, now)
	case TypeGeneric:
		return fetchGeneric(ctx, c, now)
	}
	return Result{}, &Error{Code: CodeBadConfig, Detail: "unknown source type"}
}

// NormalizeURL checks a source address and makes it canonical: http or https, a host, no credentials in it (the key
// has its own field and is sent as a header), no query or fragment, no trailing slash.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", &Error{Code: CodeBadConfig, Detail: "address must be http(s)://host[:port][/path]"}
	}
	if u.User != nil {
		return "", &Error{Code: CodeBadConfig, Detail: "put the key in the key field, not in the address"}
	}
	if len(raw) > 512 {
		return "", &Error{Code: CodeBadConfig, Detail: "address too long"}
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

type client struct {
	base string
	key  string
	http *http.Client
}

// do sends one request with the key as a bearer token and decodes a JSON answer of at most BodyMost bytes.
func (c *client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return &Error{Code: CodeBadConfig, Detail: "bad request"}
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// net/http's text names the address, not the key; still keep only a code.
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return &Error{Code: CodeTimeout}
		}
		return &Error{Code: CodeUnreachable}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return &Error{Code: CodeUnauthorized, Status: resp.StatusCode}
	case resp.StatusCode == http.StatusForbidden:
		return &Error{Code: CodeForbidden, Status: resp.StatusCode}
	case resp.StatusCode == http.StatusNotFound:
		return &Error{Code: CodeNotFound, Status: resp.StatusCode}
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return &Error{Code: CodeRedirect, Status: resp.StatusCode}
	case resp.StatusCode >= 500:
		return &Error{Code: CodeServer, Status: resp.StatusCode}
	case resp.StatusCode != http.StatusOK:
		return &Error{Code: CodeBadResponse, Status: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, BodyMost+1))
	if err != nil {
		if isTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
			return &Error{Code: CodeTimeout}
		}
		return &Error{Code: CodeUnreachable}
	}
	if len(data) > BodyMost {
		return &Error{Code: CodeTooLarge}
	}
	if err := decodeJSON(data, out); err != nil {
		return &Error{Code: CodeBadResponse, Detail: "not the expected JSON"}
	}
	return nil
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}
