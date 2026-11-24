// Package quota is what a launcher's quota looks like (docs/integrations/launchers.md "Quota") and how a quota plugin reaches the
// launcher's gateway without ever seeing its key. Which gateways are read and how is the quota plugins' business
// (docs/extensions/extensions.md "Quota"): a plugin answers quota.query with a Report, asking the hub (quota.http) to send requests in
// which ${launcher_key} stands for the key. Whoever holds the key — the hub for a launcher added in Persona, the
// machine for a ~/.zshrc one — puts it in (Do), only toward the launcher's own gateway, and takes it out of the answer.
package quota

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/modelhttp"
)

// Report is one key's quota as the app shows it.
type Report struct {
	Gateway string `json:"gateway"` // the gateway's id (the plugin's contribution)
	// GatewayName is what the app calls it ("" from an older hub: the app shows Gateway).
	GatewayName string `json:"gateway_name,omitempty"`
	KeyName     string `json:"key_name,omitempty"` // the key's billing name at the gateway (not the key)
	// Usage is the key's own use (today / this week / this month).
	Usage []Usage `json:"usage,omitempty"`
	// Budget is a spending cap set on the key itself, nil when none.
	Budget []Window `json:"budget,omitempty"`
	// Pools are what the key draws on (accounts, providers), each with its windows.
	Pools     []Pool    `json:"pools,omitempty"`
	Note      string    `json:"note,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

type Usage struct {
	Label string `json:"label"` // today / this week / this month (in Chinese)
	// Period is what Label covers, for clients that add the numbers up: PeriodDay, PeriodWeek or PeriodMonth, in the
	// user's time zone (quota.query's tz). "" when the extension does not say: then no client takes it for any period.
	Period   string   `json:"period,omitempty"`
	Requests int64    `json:"requests"`
	Tokens   int64    `json:"tokens"`
	CostUSD  *float64 `json:"cost_usd,omitempty"`
}

// The periods a Usage may name.
const (
	PeriodDay   = "day"
	PeriodWeek  = "week"
	PeriodMonth = "month"
)

// Pool is an account or provider the key's requests go to.
type Pool struct {
	Name    string   `json:"name"`
	State   string   `json:"state,omitempty"`  // available | cooldown | exhausted | restricted | unavailable | ok | partial | error | unsupported
	Detail  string   `json:"detail,omitempty"` // why, or the state in words
	Windows []Window `json:"windows,omitempty"`
	// Models are per-model windows that have a limit.
	Models []ModelWindows `json:"models,omitempty"`
}

type ModelWindows struct {
	Model   string   `json:"model"`
	Windows []Window `json:"windows"`
}

// Window is one quota window: how much of it is used, of what, until when.
type Window struct {
	Label string `json:"label"` // 5 hours / weekly / monthly / total budget… (in Chinese)
	// UsedRatio is 0…1 when the limit is known.
	UsedRatio *float64   `json:"used_ratio,omitempty"`
	Used      *float64   `json:"used,omitempty"`
	Limit     *float64   `json:"limit,omitempty"`
	Unit      string     `json:"unit,omitempty"` // percent | tokens | requests | usd | credits
	ResetsAt  *time.Time `json:"resets_at,omitempty"`
	Exhausted bool       `json:"exhausted,omitempty"`
}

// ErrUnsupported: no quota plugin reads this launcher's gateway.
var ErrUnsupported = errors.New("这个网关没有能查的额度：没有装能读它的额度扩展")

// KeyPlaceholder stands for the launcher's key in a plugin's request (its URL, headers or body).
const KeyPlaceholder = "${launcher_key}"

// Limits of what a plugin may send and get back.
const (
	RequestBodyMost  = 1 << 20
	ResponseBodyMost = 4 << 20
)

// Request is an HTTP request a quota plugin asks the hub to send to the launcher's gateway.
type Request struct {
	Method string `json:"method"`
	// URL is absolute (the gateway's own origin) or a path, taken from the gateway's root (its base URL without /v1).
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// Response is what the gateway answered, the key taken out wherever it shows.
type Response struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
}

// Root is the gateway's origin and path prefix without a trailing /v1.
func Root(base string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("网关地址 %q 不对", base)
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1")
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u, nil
}

// Target is where a plugin's request goes: an absolute URL of the gateway's own origin, or a path under its root.
// Anything else is refused, so the key goes nowhere it did not already go.
func Target(base, raw string) (*url.URL, error) {
	root, err := Root(base)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		out := *root
		out.Path = strings.TrimRight(root.Path, "/") + u.Path
		out.RawQuery = u.RawQuery
		return &out, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != root.Scheme || !strings.EqualFold(u.Host, root.Host) || u.User != nil {
		return nil, fmt.Errorf("只能向这个启动器自己的网关（%s://%s）发请求", root.Scheme, root.Host)
	}
	u.Fragment = ""
	return u, nil
}

var methods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead}

// Do sends a plugin's request to the gateway behind base with key put in for KeyPlaceholder, and answers with the key
// taken out of the response. Redirects are followed only within the gateway's origin.
func Do(ctx context.Context, client *http.Client, base, key string, r Request) (Response, error) {
	method := strings.ToUpper(r.Method)
	if method == "" {
		method = http.MethodGet
	}
	if !slices.Contains(methods, method) {
		return Response{}, fmt.Errorf("不支持的方法 %s", r.Method)
	}
	if len(r.Body) > RequestBodyMost {
		return Response{}, errors.New("请求体太大")
	}
	put := func(s string) string { return strings.ReplaceAll(s, KeyPlaceholder, key) }
	target, err := Target(base, r.URL)
	if err != nil {
		return Response{}, err
	}
	if strings.Contains(target.Path, KeyPlaceholder) {
		// The path carries the key as one escaped segment: a key with "/" or "?" must not add segments or a query.
		placeholder := (&url.URL{Path: KeyPlaceholder}).EscapedPath()
		target.RawPath = strings.ReplaceAll(target.EscapedPath(), placeholder, url.PathEscape(key))
		target.Path = strings.ReplaceAll(target.Path, KeyPlaceholder, key)
	}
	q := target.Query()
	for k, vs := range q {
		for i := range vs {
			vs[i] = put(vs[i])
		}
		q[k] = vs
	}
	target.RawQuery = q.Encode()
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(put(r.Body))
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return Response{}, err
	}
	for k, v := range r.Headers {
		if strings.EqualFold(k, "host") {
			continue
		}
		req.Header.Set(k, put(v))
	}
	modelhttp.SetDefaultUserAgent(req.Header)
	// A quota or usage read for an extension is Persona's own background request, never an Agent's: its source says so,
	// replacing any source header the extension passed.
	modelhttp.Source{Role: "background"}.ApplyForURL(req.Header, target.String())
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	c := *client
	origin := target.Scheme + "://" + strings.ToLower(target.Host)
	c.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme+"://"+strings.ToLower(next.URL.Host) != origin {
			return errors.New("网关把请求转到了别的地址，没有跟过去")
		}
		if len(via) >= 5 {
			return errors.New("重定向太多")
		}
		return nil
	}
	resp, err := c.Do(req)
	if err != nil {
		return Response{}, errors.New(takeKey(err.Error(), key))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, ResponseBodyMost+1))
	if err != nil {
		return Response{}, err
	}
	if len(data) > ResponseBodyMost {
		return Response{}, errors.New("网关的回答太大")
	}
	out := Response{Status: resp.StatusCode, Headers: map[string][]string{}}
	for k, vs := range resp.Header {
		for _, v := range vs {
			out.Headers[k] = append(out.Headers[k], takeKey(v, key))
		}
	}
	out.Body = takeKey(string(data), key)
	return out, nil
}

// takeKey replaces key in s with KeyPlaceholder, both as written and as escaped in a URL: a key in the query of a
// request that failed shows up escaped in the error (and so does any echo of the URL).
func takeKey(s, key string) string {
	if key == "" {
		return s
	}
	for _, form := range []string{key, url.QueryEscape(key), url.PathEscape(key)} {
		s = strings.ReplaceAll(s, form, KeyPlaceholder)
	}
	return s
}
