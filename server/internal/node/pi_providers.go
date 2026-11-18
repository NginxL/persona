package node

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// pi and Persona's model providers (#8194, docs/integrations/pi.md "Persona's model providers"): pi's Persona extension
// asks this node over node.sock (launcher.pi, with the machine's launch key, as `personad launch` does) for the
// Persona-managed providers in this computer's scope that speak a protocol pi has, and registers them with
// pi.registerProvider(). Their secrets are pulled from the manager the way any start pulls them (ensureManaged) and
// travel to pi only in this answer: nothing is written to disk, and pi keeps them in its memory.

// piProviderPrefix starts the id of every provider pi gets from Persona, so it never takes the id of one the user
// configured in pi themselves.
const piProviderPrefix = "persona-"

// piProviderPull bounds the pull of one provider's secrets; piAgentsDefaultWait the manager's answer for the default.
var (
	piProviderPull      = 20 * time.Second
	piAgentsDefaultWait = 5 * time.Second
)

type piModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ContextWindow int      `json:"context_window"`
	MaxTokens     int      `json:"max_tokens"`
	Reasoning     bool     `json:"reasoning"`
	Input         []string `json:"input"`
}

// piProvider is one provider as pi's registerProvider takes it (the extension renames the fields).
type piProvider struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Launcher   string            `json:"launcher"`
	API        string            `json:"api"`
	BaseURL    string            `json:"base_url"`
	APIKey     string            `json:"api_key"`
	AuthHeader bool              `json:"auth_header,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Models     []piModel         `json:"models"`
	// Model is the provider's own default model.
	Model string `json:"model"`
}

type piSkipped struct {
	Launcher string `json:"launcher"`
	Error    string `json:"error"`
}

// piDefault is Model Settings › Agents when pi can use it here: the provider's id and the model.
type piDefault struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort,omitempty"`
}

type piProvidersAnswer struct {
	Providers []piProvider `json:"providers"`
	Default   *piDefault   `json:"default,omitempty"`
	Skipped   []piSkipped  `json:"skipped,omitempty"`
}

// piProvidersFor answers launcher.pi: the providers for a request that carries the launch key, the same refusal as
// launcher.env's for any other.
func (n *Node) piProvidersFor(ctx context.Context, req sockRequest) any {
	if refused := n.launchKeyRefusal(req); refused != nil {
		return refused
	}
	return n.piProviders(ctx)
}

// piProviders are the catalog's providers pi can use, with their secrets, pulled where they were not yet. A provider
// the manager refuses (its own-service rule, a key that did not resolve) or cannot be asked for is skipped with why;
// one out of this computer's scope was never named to this node, so it is not even skipped.
func (n *Node) piProviders(ctx context.Context) piProvidersAnswer {
	n.managed.mu.Lock()
	names := make([]string, 0, len(n.managed.byID))
	for name, l := range n.managed.byID {
		if l.PiAPI() != "" {
			names = append(names, name)
		}
	}
	n.managed.mu.Unlock()
	slices.Sort(names)
	out := piProvidersAnswer{Providers: []piProvider{}}
	for _, name := range names {
		pctx, cancel := context.WithTimeout(ctx, piProviderPull)
		err := n.ensureManaged(pctx, name)
		cancel()
		l, ok := n.managedLauncher(name)
		switch {
		case err != nil:
			var re *rpc.Error
			if errors.As(err, &re) && re.Message != "" {
				err = errors.New(re.Message)
			}
			out.Skipped = append(out.Skipped, piSkipped{Launcher: name, Error: err.Error()})
			continue
		case !ok:
			continue // left the catalog meanwhile
		case l.Withheld:
			out.Skipped = append(out.Skipped, piSkipped{Launcher: name, Error: errManagerAway(name).Error()})
			continue
		}
		p, ok := piProviderOf(l)
		if !ok {
			out.Skipped = append(out.Skipped, piSkipped{Launcher: name, Error: "no API key for pi to send (it takes no sign-in of its own)"})
			continue
		}
		out.Providers = append(out.Providers, p)
	}
	if len(out.Providers) > 0 {
		out.Default = n.piAgentsDefault(ctx, out.Providers)
	}
	return out
}

// piAgentsDefault is Model Settings › Agents for pi: the manager names the provider and model (names only), and it
// counts when that provider is one of providers. nil otherwise (an older manager, Agents on another launcher).
func (n *Node) piAgentsDefault(ctx context.Context, providers []piProvider) *piDefault {
	peer := n.peer
	if peer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, piAgentsDefaultWait)
	defer cancel()
	var res struct {
		Launcher string `json:"launcher"`
		Model    string `json:"model"`
		Effort   string `json:"effort"`
	}
	if err := peer.Call(ctx, "launchers.agents_default", map[string]any{}, &res); err != nil || res.Launcher == "" {
		return nil
	}
	return piDefaultOf(providers, res.Launcher, res.Model, res.Effort)
}

// piDefaultOf is launcher's provider among providers with model (its own default when ""), nil when pi does not have
// that provider.
func piDefaultOf(providers []piProvider, launcher, model, effort string) *piDefault {
	i := slices.IndexFunc(providers, func(p piProvider) bool { return p.Launcher == launcher })
	if i < 0 {
		return nil
	}
	p := providers[i]
	model = cmp.Or(piModelID(model), p.Model)
	if model == "" || !slices.ContainsFunc(p.Models, func(m piModel) bool { return m.ID == model }) {
		return nil // registerProvider gets only the models listed: pi could not select another
	}
	return &piDefault{Provider: p.ID, Model: model, Effort: effort}
}

// piProviderOf is managed provider l (with its secrets) as pi takes it; false when l has no key: pi has nothing to
// send, and a sign-in of its own is the user's business.
func piProviderOf(l proto.ManagedLauncher) (piProvider, bool) {
	api := l.PiAPI()
	if api == "" || l.APIKey == "" {
		return piProvider{}, false
	}
	p := piProvider{ID: piProviderPrefix + l.Name, Name: "Persona · " + l.Name, Launcher: l.Name, API: api, APIKey: l.APIKey,
		Headers: piHeaders(l.Headers)}
	if api == "anthropic-messages" {
		p.BaseURL = cmp.Or(l.LaunchBaseURL(), "https://api.anthropic.com")
		p.AuthHeader = l.BearerAuth() // the same key as Authorization: Bearer as well
	} else {
		p.BaseURL = cmp.Or(strings.TrimRight(l.LaunchBaseURL(), "/"), "https://api.openai.com/v1")
	}
	if modelhttp.OpenCodeGo(p.BaseURL) && !modelhttp.HasOpenCodeSessionHeader(l.Headers) {
		if p.Headers == nil {
			p.Headers = map[string]string{}
		}
		p.Headers[modelhttp.OpenCodeSessionHeader] = modelhttp.OpenCodeSessionID("launcher:" + cmp.Or(l.Name, p.BaseURL))
	}
	var ids []string
	add := func(id string) {
		if id = piModelID(id); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	add(l.Model)
	for _, m := range l.Models {
		add(m.ID)
	}
	for _, id := range []string{l.OpusModel, l.SonnetModel, l.FableModel, l.HaikuModel, l.SmallModel, l.SubagentModel, l.CustomModel} {
		add(id)
	}
	if len(ids) == 0 {
		return piProvider{}, false // no model to name: pi could not select one
	}
	p.Model = ids[0]
	for _, id := range ids {
		p.Models = append(p.Models, piModelOf(api, id, l.Models))
	}
	return p, true
}

// piModelID is a model id as the gateway takes it: Claude Code's "[1m]" and the like are its own, not the model's.
func piModelID(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.IndexByte(id, '['); i > 0 {
		id = id[:i]
	}
	return id
}

// piModelOf is model id for pi, from what the provider knows of it (its context window, its thinking levels).
func piModelOf(api, id string, known []proto.ProviderModel) piModel {
	m := piModel{ID: id, Name: id, Input: []string{"text"}}
	lower := strings.ToLower(id)
	if api == "anthropic-messages" {
		m.ContextWindow = 200000
		m.Reasoning = strings.Contains(lower, "claude")
		if strings.Contains(lower, "claude") {
			m.Input = []string{"text", "image"}
		}
	} else {
		m.ContextWindow = 128000
		m.Reasoning = strings.HasPrefix(lower, "gpt-5") || strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "o4")
	}
	for _, k := range known {
		if piModelID(k.ID) != id {
			continue
		}
		if k.Name != "" {
			m.Name = k.Name
		}
		if k.ContextWindow > 0 {
			m.ContextWindow = k.ContextWindow
		}
		if k.NoEffort {
			m.Reasoning = false
		} else if len(k.Efforts) > 0 || len(k.AllEfforts) > 0 {
			m.Reasoning = true
		}
		if k.ImageInput != nil {
			m.Input = []string{"text"}
			if *k.ImageInput {
				m.Input = append(m.Input, "image")
			}
		}
		if k.MaxOutputTokens > 0 {
			m.MaxTokens = min(k.MaxOutputTokens, m.ContextWindow)
		}
		break
	}
	if m.MaxTokens == 0 {
		m.MaxTokens = min(32000, m.ContextWindow/4)
	}
	return m
}

// piHeaders are a provider's extra headers ("Name: value" a line, ANTHROPIC_CUSTOM_HEADERS) as a map.
func piHeaders(raw string) map[string]string {
	var out map[string]string
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if k, v = strings.TrimSpace(k), strings.TrimSpace(v); !ok || k == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}
