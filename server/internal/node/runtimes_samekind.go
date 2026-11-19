package node

import (
	"context"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// What a start can run where its host is an engine runtime that runs only its own launchers (#8140, Paseo with
// caps.same_kind; docs/integrations/paseo.md "A launcher Paseo does not have"). Such an engine starts every session
// with a provider of its own: a launcher of the login shell's or one kept in Persona never runs there. So where a
// start goes to it, the new-session form lists its providers (launchers.list with the start's host), a name it shares
// with a launcher of the shell's or of Persona's means its provider (the models, Test, start options and the start
// itself), and a start naming a launcher it does not have tells it the kind of agent that launcher starts: it runs one
// of its own of that kind and says so, or refuses. A terminal host keeps its own rules (runtimeOf).

// sameKind: runtime h runs only its own launchers (caps.same_kind).
func sameKind(h *runtimeConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hello.Caps.SameKind
}

// exclusiveRuntime is the engine runtime every start in ctx goes to when it runs only its own launchers: the start's
// host (the one it named, else the machine's default) is an enabled engine whose runtime is connected, starts sessions
// and declares caps.same_kind. nil otherwise: a terminal host, an engine runtime away, one the machine does not
// enable, or one that takes a launcher it does not have as its default (shell_launcher: Cindy).
func (n *Node) exclusiveRuntime(ctx context.Context) *runtimeConn {
	if !n.hostEnabled(n.startKind(ctx)) {
		return nil
	}
	h := n.engineRuntime(ctx)
	if h == nil || !sameKind(h) {
		return nil
	}
	return h
}

// runtimeOfIn resolves an explicitly selected AGY companion or an exclusive engine runtime before the default lookup.
func (n *Node) runtimeOfIn(ctx context.Context, name string) *runtimeConn {
	if assistantLauncherPurpose(ctx) {
		return n.assistantRuntime(name)
	}
	if name == agyAgent && n.startKind(ctx) == agyAgent {
		runtimes := n.runtimes
		if runtimes == nil {
			return nil
		}
		runtimes.mu.Lock()
		defer runtimes.mu.Unlock()
		return runtimes.byRuntime(agyAgent)
	}
	if h := n.exclusiveRuntime(ctx); h != nil {
		if _, own := h.launcherNamed(name); own {
			return h
		}
	}
	return n.runtimeOf(name)
}

// startRuntimeIn is startRuntime for a start in ctx: the start host's exclusive engine runtime for a name it offers,
// else what a terminal host's start would go to.
func (n *Node) startRuntimeIn(ctx context.Context, name string) (*runtimeConn, string) {
	if h := n.exclusiveRuntime(ctx); h != nil {
		if _, own := h.launcherNamed(name); own {
			return h, ""
		}
	}
	return n.startRuntime(name)
}

// usesAgyPane keeps explicitly targeted herdr starts in their pane. With herdr as the default, a
// discovered agy CLI also takes precedence over the companion for Agent starts and model discovery.
func (n *Node) usesAgyPane(ctx context.Context, name, agentType string) bool {
	kind := n.startKind(ctx)
	if assistantLauncherPurpose(ctx) || (kind != proto.EngineHerdr && kind != proto.EngineHerdrCore) ||
		n.agentKindOf(name, agentType) != agyAgent {
		return false
	}
	if host, _ := ctx.Value(startHostKey{}).(string); host != "" {
		return true
	}
	list, err := n.launchers(ctx)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(list, func(l proto.Launcher) bool {
		return l.Name == name || (l.Name == agyAgent && l.Agent == agyAgent)
	})
}

// runtimeLauncherModelsIn is runtimeLauncherModels for a start in ctx (runtimeOfIn): the advertisement of the runtime
// that runs the launcher there; false when none does.
func (n *Node) runtimeLauncherModelsIn(ctx context.Context, name string) (proto.LauncherModels, bool) {
	return launcherModelsOf(n.runtimeOfIn(ctx, name), name)
}

// engineHostLaunchers is launchers.list for a start whose host is engine runtime h, which runs only its own launchers
// (exclusiveRuntime): its launchers in its own order (its default first), then the other runtimes' (a start with one
// goes to its runtime, whatever the host). The login shell's launchers and the providers kept in Persona are left out:
// h would run one of its own in their place. Claude's desktop app and the Codex App come after, as on any host.
func (n *Node) engineHostLaunchers(h *runtimeConn) []proto.Launcher {
	own := h.launcherList()
	out := make([]proto.Launcher, 0, len(own))
	taken := make(map[string]bool, len(own))
	h.mu.Lock()
	for _, l := range own {
		out = append(out, h.formLauncherLocked(l))
		taken[l.Name] = true
	}
	runtime := h.hello.Runtime
	h.mu.Unlock()
	for _, l := range n.runtimes.launchers() {
		if l.Source != runtime && !taken[l.Name] {
			out = append(out, l)
		}
	}
	return out
}

// agentKindOf is the kind of agent launcher name starts, as a runtime that does not have it is told (agent_type):
// what the caller said (the hub's word, from the form or a task), else a launcher kept in Persona's agent, else the
// login shell's word for it, else what its name says (launcherAgentType).
func (n *Node) agentKindOf(name, given string) string {
	if given != "" {
		return given
	}
	if m, ok := n.managedLauncher(name); ok {
		return m.AgentType()
	}
	launcherCache.Lock()
	defer launcherCache.Unlock()
	for _, l := range launcherCache.list {
		if l.Name == name && l.Agent != "" {
			return l.Agent
		}
	}
	return launcherAgentType(name)
}

// cleanInstead keeps what a runtime says it did not start as asked when it is well formed: a launcher name, an effort
// of Persona's, a model of bounded length; nil when nothing is left.
func cleanInstead(in *proto.StartInstead) *proto.StartInstead {
	if in == nil {
		return nil
	}
	out := proto.StartInstead{Model: strings.TrimSpace(in.Model)}
	if shellcmd.NameRe.MatchString(in.Launcher) {
		out.Launcher = in.Launcher
	}
	if in.Effort != "" && validModes("", in.Effort) == nil {
		out.Effort = in.Effort
	}
	if r := []rune(out.Model); len(r) > maxLauncherText {
		out.Model = string(r[:maxLauncherText])
	}
	if out == (proto.StartInstead{}) {
		return nil
	}
	return &out
}

// withInstead adds to session.start's answer what the runtime did not start as asked (instead), for the hub and the
// manager to say where the start was asked for.
func withInstead(res any, err error, instead *proto.StartInstead) (any, error) {
	if m, ok := res.(map[string]any); ok && err == nil && instead != nil {
		m["instead"] = instead
	}
	return res, err
}
