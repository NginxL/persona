package assistant

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

// "Test connection" / "Fetch models" for a provider being added or edited (#2455, docs/integrations/launchers.md): the manager itself talks to the
// gateway. POST /api/v1/launchers/test sends a short request using the configured protocol;
// POST /api/v1/launchers/discover reads model pages and fills in metadata from the built-in catalog.
// Neither ever puts the key or a header value in what it answers. A probe sends the credential header a launch would
// send (probeTarget's auth); "auto" finds it and says which one the gateway took (#5329).

// probeTimeout bounds one request to the gateway (a var for the tests).
var probeTimeout = 30 * time.Second

// probeInput is the provider to talk to: a stored one by name, what the request gives overriding it. Auth is one of
// the probeAuth* methods; "" leaves the choice to probeTarget.
type probeInput struct {
	Name    string  `json:"name"`
	API     string  `json:"api"`
	BaseURL string  `json:"base_url"`
	Auth    string  `json:"auth"`
	APIKey  string  `json:"api_key"`
	Headers *string `json:"headers"`
	Model   string  `json:"model"`
	// Nil follows the saved default; an explicit empty string tests clearing it.
	Effort *string `json:"effort"`
	// Preset fills in the address and the model left empty (GET /api/v1/launchers/presets), so a key alone is tested.
	Preset string `json:"preset"`
	// ViaMachine: the computer the requests go through (models_via.go); nil keeps the stored one, "" this manager.
	ViaMachine *string `json:"via_machine"`
	// Agent: the agent the provider runs as the form has it (claude, codex, …; "" for a direct API connection), so
	// discovery asks a gateway's capability report about the protocol it goes on (capabilityProtocol); nil (an app
	// that doesn't send it) keeps a stored provider's own.
	Agent *string `json:"agent"`
}

// The credential header(s) a probe sends (probeTarget.auth). A request naming none ("") gets the stored provider's
// method, else the preset's, else both headers (a client from before auth was read).
const (
	probeAuthBoth   = ""        // none named: both x-api-key and Authorization: Bearer
	probeAuthAPIKey = "api_key" // x-api-key only
	probeAuthBearer = "bearer"  // Authorization: Bearer only
	probeAuthAuto   = "auto"    // Bearer, then x-api-key when the gateway refuses the key (send)
)

// launchAuth is the header a provider stored with auth launches with: the predicate personad launch uses
// (proto.ManagedLauncher.BearerAuth), so a probe sends what a launch sets.
func launchAuth(auth string) string {
	if (proto.ManagedLauncher{Auth: auth}).BearerAuth() {
		return probeAuthBearer
	}
	return probeAuthAPIKey
}

// probeTarget is a resolved gateway: everything needed for one request.
type probeTarget struct {
	base, key, headers, model, effort, proxy, noProxy, api, kind, via, sessionID string
	models                                                                       []proto.ProviderModel
	// agent is the agent the request names, else a stored provider's (AgentType); "" for neither.
	agent string
	// chatAlias resolves a stored Chat provider's legacy tier aliases for a connection test, not model discovery.
	chatAlias *storedLauncher
	// through sends the requests through via's node (models_via.go).
	through http.RoundTripper
	// auth is the credential header(s) the requests send (probeAuth*); send settles auto on the first request.
	auth string
}

// canonicalProbeURL reports whether raw is an http(s) URL with no duplicate internal slashes in its path.
func canonicalProbeURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return false
	}
	authority := strings.Index(raw, "//")
	if authority < 0 {
		return false
	}
	authority += len("//")
	slash := strings.IndexByte(raw[authority:], '/')
	if slash < 0 {
		return true
	}
	return !strings.Contains(raw[authority+slash:], "//")
}

// sameProbeGateway reports whether a probe's base_url override still points to stored's canonical gateway so stored
// credentials may be sent. Overrides are compared only against the normalized LaunchBaseURL (never an unnormalized
// legacy stored.BaseURL), must have a canonical path without internal "//" or endpoint suffixes (/v1/messages,
// /chat/completions, /v1/responses), and allow the single-/v1 counterpart on the same canonical base (including across
// a protocol switch between Messages and OpenAI-compatible protocols, #4886).
func sameProbeGateway(stored proto.ManagedLauncher, override, reqAPI string, reqAgent *string) bool {
	b := strings.TrimRight(strings.TrimSpace(override), "/")
	if !canonicalProbeURL(b) {
		return false
	}
	launch := strings.TrimRight(strings.TrimSpace(stored.LaunchBaseURL()), "/")
	if b == launch {
		return true
	}
	eff := stored
	if reqAPI != "" {
		eff.API = reqAPI
	}
	if reqAgent != nil {
		eff.Agent = strings.TrimSpace(*reqAgent)
	}
	u, err := url.Parse(launch)
	if err != nil || u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if !strings.HasSuffix(launch, "/v1") && (stored.MessagesBase() || eff.MessagesBase()) {
		return b == launch+"/v1"
	}
	if eff.MessagesBase() && strings.HasSuffix(launch, "/v1") {
		return b == strings.TrimSuffix(launch, "/v1")
	}
	return false
}

func (s *Service) probeTarget(ctx context.Context, in probeInput) (probeTarget, error) {
	var t probeTarget
	switch in.Auth {
	case probeAuthBoth, probeAuthAPIKey, probeAuthBearer, probeAuthAuto:
	default:
		return t, i18n.Err("assistant-models_launchers.err_auth")
	}
	auth := in.Auth
	var stored *proto.ManagedLauncher
	if in.Name != "" {
		list := s.storedLaunchers(ctx)
		i := slices.IndexFunc(list, func(l storedLauncher) bool { return l.Name == in.Name })
		if i < 0 {
			return t, i18n.Err("assistant-models_probe.err_no_provider", i18n.Args{"name": in.Name})
		}
		l := list[i]
		stored = &l.ManagedLauncher
		key, err := s.openLauncherKey(ctx, l.KeySealed, l.Name, "")
		if err != nil {
			if _, ref := s.keyReference(l.KeySealed); ref {
				return t, err // the provider extension's words
			}
			return t, i18n.Err("assistant-models_probe.err_open_key")
		}
		headers, err := s.openKey(l.HeadersSealed)
		if err != nil {
			return t, i18n.Err("assistant-models_probe.err_open_headers")
		}
		t = probeTarget{base: l.BaseURL, key: key, headers: headers, model: l.Model, effort: l.Effort, proxy: l.Proxy, noProxy: l.NoProxy, api: l.API, kind: l.Kind, via: l.ViaMachine,
			models: l.Models, agent: l.AgentType()}
		if l.Kind == "api" && l.API == "chat" {
			t.chatAlias = &l
		}
		if t.kind == "" {
			t.kind = "launcher" // Stored entries without kind are legacy launchers.
		}
		// None named: the stored one. Codex and Antigravity launch with their own key variables (OPENAI_API_KEY,
		// GEMINI_API_KEY; node managedEnv), which auth does not choose, so they keep both headers whatever a client
		// names (the iOS and Android editors send the stored auth for every provider).
		if a := l.AgentType(); a == "codex" || a == "agy" {
			auth = probeAuthBoth
		} else if auth == "" {
			auth = launchAuth(l.Auth)
		}
	}
	if b := strings.TrimSpace(in.BaseURL); b != "" {
		// The stored key and headers only go to the stored gateway: another address gets just what the request brings.
		if stored != nil && !sameProbeGateway(*stored, b, in.API, in.Agent) {
			t.key, t.headers = "", ""
		}
		t.base = b
	}
	if k := strings.TrimSpace(in.APIKey); k != "" {
		t.key = k
	}
	if in.Headers != nil {
		t.headers = *in.Headers
	}
	if in.ViaMachine != nil {
		t.via = strings.TrimSpace(*in.ViaMachine)
	}
	if m := strings.TrimSpace(in.Model); m != "" {
		t.model = m
	}
	if in.Effort != nil {
		t.effort = strings.TrimSpace(*in.Effort)
	}
	if in.Agent != nil {
		t.agent = strings.TrimSpace(*in.Agent)
	}
	if in.Preset != "" {
		pr, ok := s.presetByID(ctx, in.Preset)
		if !ok {
			return t, errNoPreset
		}
		if in.Name == "" {
			t.kind = pr.Kind
			if t.kind == "" {
				t.kind = "launcher"
			}
		}
		if strings.TrimSpace(t.base) == "" {
			t.base = pr.BaseURL
		}
		if t.model == "" {
			t.model = presetModel(pr)
		}
		if t.api == "" {
			t.api = pr.API
		}
		if auth == "" { // none named, nothing stored: the preset's
			auth = launchAuth(pr.Auth) // saving from the preset without auth stores the preset's (presetFill)
		}
	}
	if in.API != "" {
		t.api = in.API
	}
	if t.api != "" && t.api != "messages" && t.api != "responses" && t.api != "chat" {
		return t, i18n.Err("assistant-models_launchers.err_api")
	}
	// An anonymous Messages probe also tests launchers, so only a stored provider or
	// preset identifies it as direct API. Anonymous Responses calls are direct API.
	if t.kind == "" && t.api == "responses" {
		t.kind = "api"
	}
	if err := checkAPITransport(t.kind, t.api, t.proxy, t.noProxy, strings.TrimSpace(t.headers) != ""); err != nil {
		return t, err
	}
	// Responses and Chat Completions calls send Bearer alone (ai.respond, ai.chatCompletions), whatever auth says. A
	// client naming no method, no stored provider and no preset gets both headers, as before auth was read.
	t.auth = auth
	if !messagesAPI(t.api) {
		t.auth = probeAuthBearer
	}
	t.key = strings.TrimSpace(strings.TrimPrefix(t.key, "Bearer "))
	if t.key == "" && t.auth == probeAuthAuto {
		t.auth = probeAuthBoth // no key, no header: nothing to find
	}
	if err := checkGateway(t.base); err != nil {
		return t, err
	}
	if err := checkHeaders(t.headers); err != nil {
		return t, err
	}
	if t.kind != "api" && strings.TrimSpace(t.headers) != "" {
		l := proto.ManagedLauncher{Agent: t.agent, API: t.api, BaseURL: t.base}
		if !l.CustomHeadersSupported() {
			return t, errRuntimeHeaders
		}
	}
	if t.api == "chat" || t.api == "responses" {
		t.base = proto.APIBaseURL(t.base, t.api)
	} else {
		t.base = strings.TrimRight(strings.TrimSpace(t.base), "/")
	}
	t.sessionID = modelhttp.OpenCodeSessionID("")
	if t.via != "" {
		t.through = viaNode{s: s, machine: t.via}
	}
	return t, nil
}

// endpoint is <base>/v1/<path>, a base already ending in /v1 or a pasted endpoint path not doubled. A Messages base is
// read as the node hands it to Claude Code (proto.MessagesBaseURL), so a test passes for the address Agents will call.
func (t probeTarget) endpoint(path string) string {
	if messagesAPI(t.api) {
		return proto.MessagesBaseURL(t.base) + "/v1/" + path
	}
	base := proto.APIBaseURL(t.base, t.api)
	if strings.HasSuffix(base, "/v1") {
		return base + "/" + path
	}
	return base + "/v1/" + path
}

// modelsEndpoint keeps discovery separate from the inference protocol. DeepSeek documents /models at the
// official origin, not under its /anthropic Messages base (https://api-docs.deepseek.com/api/list-models/).
// Only its known HTTPS bases qualify; custom gateway prefixes and authorities stay untouched.
func (t probeTarget) modelsEndpoint() string {
	if u, ok := officialDeepSeekURL(t.base); ok {
		u.Path, u.RawPath = "/models", ""
		return u.String()
	}
	return t.endpoint("models")
}

// officialDeepSeekURL parses base when it is one of the official DeepSeek API bases: HTTPS, the provider's own host
// (with no user info, query or fragment) and a path Persona accepts for it. Only these bases may be read at the
// provider's own routes (/models, /user/balance); a gateway or a lookalike host is never sent the launcher's key there.
func officialDeepSeekURL(base string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(!strings.EqualFold(u.Host, "api.deepseek.com") && !strings.EqualFold(u.Host, "api.deepseek.com:443")) {
		return nil, false
	}
	switch u.EscapedPath() {
	case "", "/v1", "/anthropic", "/anthropic/v1":
		return u, true
	}
	return nil, false
}

// send sends one request under t.auth. Under auto it goes with Bearer and, when the gateway refuses the key, once
// more with x-api-key (the body is bytes so it can go twice); t then keeps the header of the request answered, so
// later requests (the next model page) send that one alone. When both are refused, the x-api-key answer stands,
// except a 401 after Bearer's 403: the gateway knew the Bearer key and only forbade something, which says more than a
// header it does not read.
func (t *probeTarget) send(ctx context.Context, method, endpoint string, body []byte) (*http.Response, error) {
	if t.auth != probeAuthAuto {
		return t.do(ctx, method, endpoint, body)
	}
	t.auth = probeAuthBearer
	res, err := t.do(ctx, method, endpoint, body)
	if err != nil || !t.keyRefused(res) {
		return res, err
	}
	first := res // its body already read into memory by keyRefused
	t.auth = probeAuthAPIKey
	res, err = t.do(ctx, method, endpoint, body)
	if err == nil && first.StatusCode == http.StatusForbidden && res.StatusCode == http.StatusUnauthorized {
		res.Body.Close()
		t.auth = probeAuthBearer
		return first, nil
	}
	first.Body.Close()
	return res, err
}

// credentialRefused: the gateway turned the key down, a 401 or a 403 other than Anthropic's region block (which
// comes before the key is looked at, so the other header would fare no better). failure calls it code "auth".
func credentialRefused(status int, raw []byte, anthropic bool) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden && !regionBlocked(raw, anthropic)
}

// keyRefused: res is credentialRefused. A 401 / 403's body is read to tell (which also lets the retry reuse the
// connection) and put back for failure.
func (t probeTarget) keyRefused(res *http.Response) bool {
	if res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusForbidden {
		return false
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(raw))
	return credentialRefused(res.StatusCode, raw, t.atAnthropic())
}

// do sends one request with the custom headers first, then the version and the credential header(s) t.auth names
// (send resolves auto before calling it).
func (t probeTarget) do(ctx context.Context, method, endpoint string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	for _, hd := range parseHeaders(t.headers) {
		req.Header.Set(hd[0], hd[1])
	}
	modelhttp.SetDefaultUserAgent(req.Header)
	modelhttp.Source{Role: "background"}.ApplyForURL(req.Header, endpoint) // a provider test or model list, no Agent's
	modelhttp.SetOpenCodeSession(req.Header, endpoint, t.sessionID)
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
		if t.api == "responses" || t.api == "chat" {
			req.Header.Set("accept", "text/event-stream")
		}
	}
	if t.key != "" {
		if t.auth != probeAuthBearer {
			req.Header.Set("x-api-key", t.key)
		}
		if t.auth != probeAuthAPIKey {
			req.Header.Set("authorization", "Bearer "+t.key)
		}
	}
	// A redirect is answered as is, never followed: Go keeps x-api-key and the custom headers on a redirect to another
	// host, and the key must only go to the gateway asked.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if t.proxy != "" {
		// A gateway behind its own proxy: a one-off transport that keeps no connection behind.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DisableKeepAlives = true
		if u, err := url.Parse(t.proxy); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
		client.Transport = transport
	}
	if t.through != nil {
		client.Transport = t.through
	}
	return client.Do(req)
}

// parseHeaders reads "Name: value" lines (ANTHROPIC_CUSTOM_HEADERS); lines without a name are skipped.
func parseHeaders(raw string) [][2]string {
	var out [][2]string
	for _, line := range strings.Split(raw, "\n") {
		if name, value, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && strings.TrimSpace(name) != "" {
			out = append(out, [2]string{strings.TrimSpace(name), strings.TrimSpace(value)})
		}
	}
	return out
}

// redact takes the key and every header value out of a message about the gateway.
func (t probeTarget) redact(s string) string {
	secrets := []string{t.key}
	for _, hd := range parseHeaders(t.headers) {
		secrets = append(secrets, hd[1])
	}
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// pt is an assistant-models_probe text in l.
func pt(l i18n.Lang, key string, args ...i18n.Args) string {
	return i18n.T(l, "assistant-models_probe."+key, args...)
}

// probeCodeRegionBlocked is the code of a failure that is Anthropic refusing the network's region before looking at
// the key (#4482): no key fixes it, another vendor does.
const probeCodeRegionBlocked = "region_blocked"

// failure is what went wrong, said for a person (in ctx's language): the status first, then what the gateway said
// (redacted, clipped); code is set for the failures a client can do more about (probeCodeRegionBlocked).
func (t probeTarget) failure(ctx context.Context, res *http.Response, err error) (msg, code string) {
	l := i18n.From(ctx)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return pt(l, "timeout", i18n.Args{"seconds": int(probeTimeout / time.Second)}), "timeout"
		}
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // the URL is the user's own; the cause is what helps
		}
		return clip(oneLine(t.redact(pt(l, "unreachable", i18n.Args{"error": err.Error()}))), 300), ""
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode == http.StatusForbidden && regionBlocked(raw, t.atAnthropic()) {
		// The gateway's own words ("Request not allowed") only mislead here, so they are left out.
		return pt(l, "region_blocked", i18n.Args{"vendors": regionOpenVendors(l)}), probeCodeRegionBlocked
	}
	said := gatewayMessage(raw)
	var head string
	if (res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusNotFound) &&
		ai.FailureKindFor(res.StatusCode, raw) == ai.FailureModelUnavailable {
		head = pt(l, "status_model_not_found")
		code = "model_not_found"
	} else {
		switch res.StatusCode {
		case http.StatusUnauthorized:
			head = pt(l, "status_401")
		case http.StatusForbidden:
			head = pt(l, "status_403")
		case http.StatusPaymentRequired:
			head = pt(l, "status_402")
			code = "quota_exhausted"
		case http.StatusNotFound:
			head = pt(l, "status_404")
			code = "not_found"
		case http.StatusMethodNotAllowed, http.StatusUnsupportedMediaType:
			head = pt(l, "status_protocol", i18n.Args{"status": res.StatusCode})
			code = "wrong_protocol"
		case http.StatusTooManyRequests:
			head = pt(l, "status_429")
		default:
			head = pt(l, "status_other", i18n.Args{"status": res.StatusCode})
		}
	}
	if credentialRefused(res.StatusCode, raw, t.atAnthropic()) {
		code = "auth"
	}
	if said != "" {
		head = pt(l, "said", i18n.Args{"head": head, "said": said})
	}
	return clip(oneLine(t.redact(head)), 300), code
}

// atAnthropic: the target is Anthropic's own API (not a relay or another vendor).
func (t probeTarget) atAnthropic() bool {
	return proto.AnthropicAPI(t.base)
}

// regionBlocked: a 403 body is Anthropic's region block, {"type":"error","error":{"type":"forbidden","message":
// "Request not allowed"}}, sent before the key is looked at. A key without access is a permission_error with another
// message, and other vendors' 403s say other things, so those keep the plain 403 text. Without the "forbidden" type
// the bare phrase is generic, so it only counts when the target is Anthropic itself (anthropic).
func regionBlocked(raw []byte, anthropic bool) bool {
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	typ := strings.ToLower(strings.TrimSpace(body.Error.Type))
	return (typ == "forbidden" || typ == "" && anthropic) &&
		strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(body.Error.Message), "."), "Request not allowed")
}

// regionOpenVendors names, in l, the built-in vendors that open without a VPN or a foreign card where Anthropic says
// no (the presets whose region is not global), in the file's order.
func regionOpenVendors(l i18n.Lang) string {
	var names []string
	for _, pr := range builtinLauncherPresets(l) {
		if i := slices.IndexFunc(builtinPresets(), func(b builtinPreset) bool { return b.ID == pr.ID }); i >= 0 && builtinPresets()[i].Region != "global" {
			names = append(names, pr.DisplayName)
		}
	}
	return strings.Join(names, pt(l, "list_sep"))
}

// gatewayMessage is the error message in a gateway's answer ({"error":{"message"}}, {"error":{"error":{"message"}}},
// {"error":{"error":"…"}}, {"error":"…"}, {"message"}), or the start of the body. Like the classifier, it reads at most
// error.error.
func gatewayMessage(raw []byte) string {
	var body struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(raw, &body) == nil {
		var e struct {
			Message string          `json:"message"`
			Error   json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body.Error, &e) == nil {
			var deeper struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(e.Error, &deeper) == nil && deeper.Message != "" {
				return deeper.Message
			}
			var deeperText string
			if json.Unmarshal(e.Error, &deeperText) == nil && deeperText != "" {
				return deeperText
			}
			if e.Message != "" {
				return e.Message
			}
		}
		var s string
		switch {
		case json.Unmarshal(body.Error, &s) == nil && s != "":
			return s
		case body.Message != "":
			return body.Message
		}
	}
	return clip(oneLine(string(raw)), 160)
}

// providerTest sends a short request using the configured protocol.
func (s *Service) providerTest(ctx context.Context, in probeInput) map[string]any {
	out := map[string]any{"ok": false, "ms": 0, "model": in.Model}
	t, err := s.probeTarget(ctx, in)
	if err != nil {
		out["error"] = i18n.Localize(err, i18n.From(ctx))
		return out
	}
	if t.chatAlias != nil && t.api == "chat" {
		model, err := resolveAPITierAlias(*t.chatAlias, t.model)
		if err != nil {
			out["error"] = i18n.Localize(err, i18n.From(ctx))
			return out
		}
		t.model = model
	}
	if t.api == "chat" {
		t.effort = chatEffortForModel(t.models, t.model, t.effort)
	}
	if out["model"] = t.model; t.model == "" {
		out["error"] = i18n.T(i18n.From(ctx), "assistant-models_launchers.err_model_required")
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	path := "messages"
	payload := map[string]any{"model": t.model, "max_tokens": 1,
		"messages": []any{map[string]any{"role": "user", "content": "ping"}}}
	if t.api == "responses" {
		path = "responses"
		// The same shape as the real calls (ai.respond: the system prompt as a developer message), so a key that
		// passes is one those calls work with.
		payload = map[string]any{"model": t.model, "input": []any{map[string]any{"role": "developer", "content": "Reply in one word."},
			map[string]any{"role": "user", "content": "ping"}}, "stream": true, "store": false}
	} else if t.api == "chat" {
		path = "chat/completions"
		payload = map[string]any{"model": t.model, "messages": []any{map[string]any{"role": "user", "content": "ping"}}, "stream": true, "max_tokens": 256}
	}
	if t.api == "chat" && ai.OmitChatEffort(t.base, t.key, t.model, t.via, t.effort) {
		t.effort = ""
	}
	if !ai.ApplyReasoningEffort(payload, t.api, t.model, t.effort) {
		out["error"] = i18n.T(i18n.From(ctx), "assistant-models.err_effort_unsupported")
		out["code"] = "unsupported_effort"
		return out
	}
	started := time.Now()
	auto := t.auth == probeAuthAuto
	var res *http.Response
	renamedTokens, droppedEffort := false, false
	// A compatibility attempt changes the rejected request; X-Should-Retry: false only suppresses resending it unchanged.
	for {
		body, _ := json.Marshal(payload)
		res, err = t.send(ctx, http.MethodPost, t.endpoint(path), body)
		if err != nil || t.api != "chat" || res.StatusCode != http.StatusBadRequest {
			break
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, 4096))
		res.Body.Close()
		// Recognized machine codes win over compatibility wording and keep their original response for failure().
		if ai.HasRecognizedErrorCode(raw) {
			res.Body = io.NopCloser(bytes.NewReader(raw))
			break
		}
		fieldRejected := ai.ChatRejectsReasoningEffort(raw)
		offValueRejected := !fieldRejected && ai.ChatNoThinking(t.effort) && ai.ChatRejectsNoEffortValue(raw)
		if readErr == nil && !droppedEffort && payload["reasoning_effort"] != nil && (fieldRejected || offValueRejected) {
			ai.RememberChatEffortRejection(t.base, t.key, t.model, t.via, offValueRejected)
			droppedEffort = true
			delete(payload, "reasoning_effort")
			t.effort = "" // the answer did not verify the requested effort
			s.logf("assistant: chat connection test: gateway rejected reasoning_effort; retrying once without it")
			continue
		}
		if readErr == nil && !renamedTokens && payload["max_tokens"] != nil && ai.ChatRequiresCompletionTokens(raw) {
			renamedTokens = true
			delete(payload, "max_tokens")
			payload["max_completion_tokens"] = 256
			continue
		}
		res.Body = io.NopCloser(bytes.NewReader(raw))
		break
	}
	if err != nil || res.StatusCode/100 != 2 {
		msg, code := t.failure(ctx, res, err)
		out["error"] = msg
		if code != "" {
			out["code"] = code
		}
	} else if t.api == "responses" || t.api == "chat" {
		if t.api == "chat" {
			out["ok"] = chatProbeCompleted(res.Body)
		} else {
			out["ok"] = responsesProbeCompleted(res.Body)
		}
		if out["ok"] == false {
			if ctx.Err() != nil {
				out["error"], _ = t.failure(ctx, nil, ctx.Err())
			} else {
				out["error"] = pt(i18n.From(ctx), "response_unfinished")
			}
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		out["ok"] = true
	}
	if res != nil {
		res.Body.Close()
	}
	if out["ok"] == true && auto {
		out["auth"] = t.auth // the header the gateway took
	}
	if out["ok"] == true {
		out["effort"] = t.effort
	}
	out["ms"] = int(time.Since(started).Milliseconds())
	return out
}

// responsesProbeCompleted requires the same terminal event as the runtime, within the response budget, reading the
// stream's events the way the runtime does (ai.SSEEvents).
func responsesProbeCompleted(body io.Reader) bool {
	completed := false
	err := ai.SSEEvents(io.LimitReader(body, 8<<20), func(data string) bool {
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			return false
		}
		switch event.Type {
		case "response.completed":
			completed = true
			return true
		case "response.failed", "response.incomplete", "error":
			return true
		}
		return false
	})
	return err == nil && completed
}

// chatProbeCompleted follows the first choice to a terminal event, including errors after its finish reason.
func chatProbeCompleted(body io.Reader) bool {
	sc := bufio.NewScanner(io.LimitReader(body, 8<<20))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	finished, hasText := false, false
	var lines []string
	// An SSE event can span several data lines. A terminal result returns stop=true.
	consume := func() (stop, success bool) {
		if len(lines) == 0 {
			return false, false
		}
		data := strings.Join(lines, "\n")
		lines = nil
		if strings.TrimSpace(data) == "[DONE]" {
			return true, hasText
		}
		var event struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index        int    `json:"index"`
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content string `json:"content"`
					Refusal string `json:"refusal"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &event) != nil || len(event.Error) > 0 && string(event.Error) != "null" {
			return true, false
		}
		for _, choice := range event.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.FinishReason == "content_filter" || choice.Delta.Refusal != "" {
				return true, false
			}
			hasText = hasText || strings.TrimSpace(choice.Delta.Content) != ""
			finished = finished || choice.FinishReason != ""
		}
		return false, false
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if stop, success := consume(); stop {
				return success
			}
			continue
		}
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			lines = append(lines, strings.TrimPrefix(data, " "))
		}
	}
	if sc.Err() != nil {
		return false
	}
	if stop, success := consume(); stop {
		return success
	}
	return hasText && finished
}

// providerDiscover reads the provider's model-list endpoint and, when the gateway has one, its capability report
// (applyCapabilities), its models filled in from the catalog.
func (s *Service) providerDiscover(ctx context.Context, in probeInput) map[string]any {
	models := []proto.ProviderModel{}
	guessed := map[string]bool{} // models whose "no thinking level" is only a guess (guessNoEffort)
	enriched := func() []proto.ProviderModel {
		out := make([]proto.ProviderModel, len(models))
		for i, m := range models {
			if guessed[m.ID] {
				m = guessNoEffort(m)
			}
			out[i] = enrichDiscoveredModel(m)
		}
		return out
	}
	fail := func(msg string) map[string]any { return map[string]any{"models": enriched(), "error": msg} }
	t, err := s.probeTarget(ctx, in)
	if err != nil {
		return fail(i18n.Localize(err, i18n.From(ctx)))
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	// failed is fail with what failure says, and its code when it has one
	failed := func(res *http.Response, err error) map[string]any {
		msg, code := t.failure(ctx, res, err)
		if err == nil && res != nil && res.StatusCode == http.StatusNotFound {
			msg = pt(i18n.From(ctx), "models_not_found")
			code = "not_found" // a model-list endpoint is missing, not the model used by a connection test
		}
		out := fail(msg)
		if code != "" {
			out["code"] = code
		}
		return out
	}
	const maxPages = 100
	seenModels := map[string]int{}
	seenCursors := map[string]bool{}
	query := url.Values{}
	modelsEndpoint := t.modelsEndpoint()
	for page := 0; page < maxPages; page++ {
		endpoint := modelsEndpoint
		if len(query) > 0 {
			endpoint += "?" + query.Encode()
		}
		res, err := t.send(ctx, http.MethodGet, endpoint, nil)
		if err != nil || res.StatusCode/100 != 2 {
			out := failed(res, err)
			if res != nil {
				res.Body.Close()
			}
			return out
		}
		raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		res.Body.Close()
		if err != nil {
			return failed(nil, err)
		}
		listed, guesses, ok := parseModelsList(raw)
		maps.Copy(guessed, guesses)
		if !ok {
			return fail(pt(i18n.From(ctx), "models_unreadable"))
		}
		for _, m := range listed {
			if i, ok := seenModels[m.ID]; ok {
				models[i] = mergeDiscoveredMetadata(models[i], m)
			} else {
				seenModels[m.ID] = len(models)
				models = append(models, m)
			}
		}
		var meta struct {
			HasMore            bool            `json:"has_more"`
			LastID             string          `json:"last_id"`
			Next               json.RawMessage `json:"next"`
			NextCursor         string          `json:"next_cursor"`
			NextPage           json.RawMessage `json:"next_page"`
			NextPageToken      string          `json:"next_page_token"`
			NextPageTokenCamel string          `json:"nextPageToken"`
			Links              json.RawMessage `json:"links"`
		}
		if len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '{' {
			if err := json.Unmarshal(raw, &meta); err != nil {
				return fail(pt(i18n.From(ctx), "models_pagination"))
			}
		}
		query = url.Values{}
		var nextPage string
		if len(meta.NextPage) > 0 && string(meta.NextPage) != "null" {
			if json.Unmarshal(meta.NextPage, &nextPage) != nil {
				var n json.Number
				if json.Unmarshal(meta.NextPage, &n) != nil {
					return fail(pt(i18n.From(ctx), "models_pagination"))
				}
				nextPage = n.String()
			}
		}
		var next string
		if len(meta.Next) > 0 && string(meta.Next) != "null" {
			if json.Unmarshal(meta.Next, &next) != nil {
				var n json.Number
				if json.Unmarshal(meta.Next, &n) != nil {
					return fail(pt(i18n.From(ctx), "models_pagination"))
				}
				if nextPage == "" {
					nextPage = n.String()
				}
			}
		}
		if next == "" && len(meta.Links) > 0 {
			var links struct {
				Next string `json:"next"`
			}
			if json.Unmarshal(meta.Links, &links) == nil {
				next = links.Next
			}
		}
		switch {
		case meta.NextCursor != "":
			query.Set("cursor", meta.NextCursor)
		case meta.NextPageToken != "":
			query.Set("page_token", meta.NextPageToken)
		case meta.NextPageTokenCamel != "":
			query.Set("pageToken", meta.NextPageTokenCamel)
		case nextPage != "":
			query.Set("page", nextPage)
		case next != "":
			base, _ := url.Parse(modelsEndpoint)
			ref, err := url.Parse(next)
			if err != nil {
				return fail(pt(i18n.From(ctx), "models_pagination"))
			}
			resolved := base.ResolveReference(ref)
			if resolved.User != nil || resolved.Scheme != base.Scheme || resolved.Host != base.Host || resolved.Path != base.Path || resolved.Fragment != "" || resolved.RawQuery == "" {
				return fail(pt(i18n.From(ctx), "models_pagination"))
			}
			query = resolved.Query()
		case meta.HasMore && meta.LastID != "":
			query.Set("after_id", meta.LastID)
		case meta.HasMore:
			return fail(pt(i18n.From(ctx), "models_pagination"))
		default:
			models = t.applyCapabilities(ctx, models, guessed)
			// no auth: a model list may answer without looking at the key, so only the test says what a launch needs
			return map[string]any{"models": enriched()}
		}
		cursor := query.Encode()
		if seenCursors[cursor] {
			return fail(pt(i18n.From(ctx), "models_pagination"))
		}
		seenCursors[cursor] = true
	}
	return fail(pt(i18n.From(ctx), "models_pagination"))
}

// mergeDiscoveredMetadata keeps the first value and fills gaps from a repeated model.
func mergeDiscoveredMetadata(first, next proto.ProviderModel) proto.ProviderModel {
	if first.Name == "" {
		first.Name = next.Name
	}
	if first.ContextWindow == 0 {
		first.ContextWindow = next.ContextWindow
	}
	return mergeModelMeta(first, next)
}

// enrichDiscoveredModel prefers an exact catalog window and keeps the server's other metadata.
// A window inherited from an earlier generation only fills a gap; a window the server reported wins, and so do the
// thinking levels it reported (settleDiscoveredEfforts), its output limit and what it takes.
func enrichDiscoveredModel(m proto.ProviderModel) proto.ProviderModel {
	m = settleDiscoveredEfforts(m)
	if c, ok := catalogModel(m.ID); ok && c.InheritedFrom == "" && c.ContextWindow > 0 {
		m.ContextWindow = c.ContextWindow
	}
	return enrichModel(m)
}

// parseModelsList reads the three shapes of a model list (cindy's parseModelsListResponse): {"data":[…]},
// {"models":[…]} and a bare array, of objects or of id strings; duplicates dropped, order kept, and entries the list
// marks as no chat model left out. An object's other metadata is read in the gateway's own words (readModelMeta),
// which enrichDiscoveredModel settles.
func parseModelsList(raw []byte) (models []proto.ProviderModel, guessed map[string]bool, ok bool) {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		var wrapped struct {
			Data   []json.RawMessage `json:"data"`
			Models []json.RawMessage `json:"models"`
		}
		if json.Unmarshal(raw, &wrapped) != nil {
			return nil, nil, false
		}
		switch {
		case wrapped.Data != nil:
			list = wrapped.Data
		case wrapped.Models != nil:
			list = wrapped.Models
		default:
			return nil, nil, false
		}
	}
	var out []proto.ProviderModel
	seen := map[string]int{}
	for _, item := range list {
		var m proto.ProviderModel
		var id string
		if json.Unmarshal(item, &id) != nil {
			var fields map[string]json.RawMessage
			if json.Unmarshal(item, &fields) != nil {
				continue
			}
			id = cmp.Or(rawString(fields["id"]), rawString(fields["slug"]), rawString(fields["key"]), rawString(fields["model_name"]),
				rawString(fields["model_id"]))
			if id == "" {
				continue
			}
			if m.Name = cmp.Or(rawString(fields["display_name"]), rawString(fields["name"])); m.Name == id {
				m.Name = ""
			}
			m.ContextWindow = firstTokens(fields["max_model_len"], fields["context_length"], fields["context_window"],
				fields["max_context_length"], fields["max_input_tokens"])
			chat, guess := readModelMeta(fields, &m)
			if !chat {
				continue
			}
			if guess {
				if guessed == nil {
					guessed = map[string]bool{}
				}
				guessed[strings.TrimSpace(id)] = true
			}
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		m.ID = id
		if i, ok := seen[id]; ok {
			out[i] = mergeDiscoveredMetadata(out[i], m)
			continue
		}
		seen[id] = len(out)
		out = append(out, m)
	}
	return out, guessed, true
}

func (s *Service) providerProbeRoutes() {
	s.handle("POST /api/v1/launchers/test", s.probeRoute(s.providerTest))
	s.handle("POST /api/v1/launchers/discover", s.probeRoute(s.providerDiscover))
}

// probeRoute is a test / discover handler: the request is a probeInput, the answer always 200 with ok / error inside.
func (s *Service) probeRoute(run func(context.Context, probeInput) map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in probeInput
		if err := decodeBody(w, r, &in, 1<<20); err != nil {
			writeBodyErr(w, err, i18n.Err("assistant-models_probe.err_bad_request"))
			return
		}
		// the answers' texts read in the request's language, else the user's (failure, providerTest)
		apierr.WriteJSON(w, run(i18n.With(r.Context(), s.requestCtxLang(r.Context())), in))
	}
}
