package node

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

// A managed Claude Code provider's gateway models, as Claude Code's /model menu adds them when its gateway model
// discovery is on (gatewayDiscoveryVar), read by the node itself (#8417). Claude Code keeps the discovered list in one
// file per config directory (<config dir>/cache/gateway-models.json) for the base URL it fetched last, and its menu
// shows that list only while it is the running gateway's. It fetches in the background at startup, so the initialize
// answer listAgentModels reads holds the list as the previous run left it: on a computer with two providers, each
// probe overwrote the other's, and a provider's gateway models showed on one computer and not on another. The node
// asks the gateway the way Claude Code does, so the picker shows the same list whichever provider was asked last.

// gatewayDiscoveryVar turns Claude Code's gateway model discovery on.
const gatewayDiscoveryVar = "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"

// gatewayModelsTimeout bounds the node's own /v1/models request; it runs beside the CLI's start, which takes longer.
var gatewayModelsTimeout = 10 * time.Second

// gatewayModelKept: the ids Claude Code keeps from a gateway's list (the others are not Claude models to it).
var gatewayModelKept = lazyre.New(`(?i)(claude|anthropic)`)

// gatewayEnv is the environment a managed provider's claude reads its discovery switch, credential, headers and proxy
// from, lowest first: the node's environment, the user's settings env (Claude Code applies it over the environment) and
// the provider's own (Persona's --settings pins it over the user's; a "" value there sets the variable empty).
func (n *Node) gatewayEnv(l proto.ManagedLauncher) map[string]string {
	ours := providerEnv(l, "", "", false)
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for k, v := range n.userSettingsEnv(map[string]any{"CLAUDE_CONFIG_DIR": ours["CLAUDE_CONFIG_DIR"]}) {
		if s, ok := settingsEnvString(v); ok {
			env[k] = s
		}
	}
	for k, v := range ours {
		env[k] = v
	}
	return env
}

// settingsEnvString is a settings env value as Claude Code puts it in the environment.
func settingsEnvString(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		return v, true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	case float64:
		b, _ := json.Marshal(v)
		return string(b), true
	}
	return "", false
}

// envOn: an environment switch Claude Code reads as on.
func envOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// gatewayModels is what l's gateway lists for Claude Code's /model menu, nil when its discovery is off, it has no
// gateway or credential, or the gateway does not answer with a list (the CLI's own answer then stands alone).
func (n *Node) gatewayModels(ctx context.Context, l proto.ManagedLauncher) []proto.AgentModel {
	if l.AgentType() != "claude" || !l.MessagesBase() || l.BaseURL == "" {
		return nil
	}
	env := n.gatewayEnv(l)
	base := strings.TrimRight(env["ANTHROPIC_BASE_URL"], "/")
	if !envOn(env[gatewayDiscoveryVar]) || base == "" || firstPartyHost(base) {
		return nil
	}
	// Only the provider's own credential (its key, or one its env sets): before a withheld provider's pull it has
	// none, and the one the user's settings or the shell carry belongs to another gateway, which a launch never sends
	// here (it pins the provider's key over it).
	ours := providerEnv(l, "", "", false)
	token, key := ours["ANTHROPIC_AUTH_TOKEN"], ours["ANTHROPIC_API_KEY"]
	if token == "" && key == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, gatewayModelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models?limit=1000", nil)
	if err != nil {
		return nil
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("x-api-key", key)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	for _, line := range strings.Split(env["ANTHROPIC_CUSTOM_HEADERS"], "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && strings.TrimSpace(k) != "" {
			req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	modelhttp.SetDefaultUserAgent(req.Header)
	modelhttp.SetOpenCodeSession(req.Header, req.URL.String(), "")
	resp, err := gatewayClient(env).Do(req)
	if err != nil {
		n.logf("launcher %s: its gateway's model list: %v", l.Name, redactSecrets(err.Error()))
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		n.logf("launcher %s: its gateway's model list answered %d", l.Name, resp.StatusCode)
		return nil
	}
	var body struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Description string `json:"description"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&body); err != nil {
		return nil
	}
	var out []proto.AgentModel
	for _, m := range body.Data {
		if m.ID == "" || !gatewayModelKept.MatchString(m.ID) {
			continue
		}
		out = append(out, proto.AgentModel{ID: m.ID, Name: cmp.Or(strings.TrimSpace(m.DisplayName), m.ID),
			Description: cmp.Or(strings.TrimSpace(m.Description), "From gateway")})
	}
	return out
}

// firstPartyHost: Anthropic's own API, whose list Claude Code does not ask for (it is not a gateway).
func firstPartyHost(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "anthropic.com" || strings.HasSuffix(h, ".anthropic.com")
}

// gatewayClient sends the request through the proxy env names for it (lower case first, as the runtime under Claude
// Code reads them) and follows no redirect, as Claude Code's own request does not.
func gatewayClient(env map[string]string) *http.Client {
	dt, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Transport: http.DefaultTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	cfg := httpproxy.Config{
		HTTPProxy:  cmp.Or(env["http_proxy"], env["HTTP_PROXY"]),
		HTTPSProxy: cmp.Or(env["https_proxy"], env["HTTPS_PROXY"]),
		NoProxy:    cmp.Or(env["no_proxy"], env["NO_PROXY"]),
	}
	proxy := cfg.ProxyFunc()
	t := dt.Clone()
	t.Proxy = func(r *http.Request) (*url.URL, error) { return proxy(r.URL) }
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// withGatewayModels adds the gateway's models to a /model list, after it, leaving out those it already has (by id,
// or as an alias resolves, with or without [1m]): the CLI lists them itself when its cache was this gateway's.
func withGatewayModels(list, gateway []proto.AgentModel) []proto.AgentModel {
	seen := map[string]bool{}
	for _, m := range list {
		seen[strings.TrimSuffix(m.ID, oneMillionSuffix)] = true
		if m.Resolved != "" {
			seen[strings.TrimSuffix(m.Resolved, oneMillionSuffix)] = true
		}
	}
	for _, m := range gateway {
		id := strings.TrimSuffix(m.ID, oneMillionSuffix)
		if seen[id] {
			continue
		}
		seen[id] = true
		list = append(list, m)
	}
	return list
}
