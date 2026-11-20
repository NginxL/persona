package node

import (
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// Context usage of a session for the apps' agent rows and session header (#2430). The numbers come from the
// transcript tail readQuickMetaSince already parses: the latest reply's input tokens (transcript.Meta.ContextTokens)
// and, for Codex, the window its rollout records. Nothing here reads a file.

const (
	claudeWindow   = 200_000   // a Claude model's window without the 1M variant
	claudeWindow1M = 1_000_000 // the [1m] variants, and the models whose window is 1M without it
)

// contextWindowOf is the model's context window in tokens, 0 when it cannot be told. recorded is the window the
// transcript itself says (Codex); otherwise Claude models are known by id: a "[1m]" suffix means the 1M variant, so
// does a model whose window is 1M without it (claudeNative1M), and so does a reply that already saw more than 200k
// (the transcript records the id without the suffix). Without a managed launcher's declaration (contextWindowWithLauncher),
// a model that is not a Claude one (a gateway's own) has no window we know: the apps then show the tokens only.
func contextWindowOf(model string, recorded, used int) int {
	if recorded > 0 {
		return recorded
	}
	id := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(id, "[1m]") {
		return claudeWindow1M
	}
	id = claudeModelAfterRoute(id)
	if id == "" {
		return 0
	}
	if used > claudeWindow || claudeNative1M(id) {
		return claudeWindow1M
	}
	return claudeWindow
}

// lookupProviderModel finds model in l.Models, matching exact base IDs first, then "/" route suffixes from longest to
// shortest, and finally proto.SameLauncherModel (where the declared ID carries a vendor prefix and the transcript
// echoes the bare ID). Bracketed placeholders ("<synthetic>") match nothing.
func lookupProviderModel(l proto.ManagedLauncher, model string) (proto.ProviderModel, bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" || isBracketedModel(trimmed) {
		return proto.ProviderModel{}, false
	}
	base := proto.TrimModelWindow(trimmed)
	if base == "" || isBracketedModel(base) {
		return proto.ProviderModel{}, false
	}
	for _, pm := range l.Models {
		if strings.EqualFold(base, proto.TrimModelWindow(pm.ID)) {
			return pm, true
		}
	}
	for i := 0; i < len(base); i++ {
		if base[i] != '/' {
			continue
		}
		suf := base[i+1:]
		if suf == "" {
			continue
		}
		for _, pm := range l.Models {
			if strings.EqualFold(suf, proto.TrimModelWindow(pm.ID)) {
				return pm, true
			}
		}
	}
	for _, pm := range l.Models {
		if proto.SameLauncherModel(trimmed, pm.ID) {
			return pm, true
		}
	}
	return proto.ProviderModel{}, false
}

// launcherContextWindow resolves the context window a managed launcher declares for a session's model, with the same
// precedence providerEnv uses for CLAUDE_CODE_MAX_CONTEXT_TOKENS: l.MaxContext when > 0, else the first matching
// Models[].ContextWindow > 0 across primary and fallbackModels, else 0.
func launcherContextWindow(l proto.ManagedLauncher, primary string, fallbackModels ...string) int {
	if l.MaxContext > 0 {
		return l.MaxContext
	}
	if pm, ok := lookupProviderModel(l, primary); ok && pm.ContextWindow > 0 {
		return pm.ContextWindow
	}
	for _, m := range fallbackModels {
		if pm, ok := lookupProviderModel(l, m); ok && pm.ContextWindow > 0 {
			return pm.ContextWindow
		}
	}
	return 0
}

// contextWindowWithLauncher resolves a session's context window: a window recorded in the transcript itself (Codex)
// comes first; otherwise a managed launcher's MaxContext or matching model ContextWindow supplies the window, keeping
// 1M promotion when l.MaxContext is unset and an explicit "[1m]" suffix or >200k observed usage exceeds a 200k catalog
// entry; when neither is declared, contextWindowOf decides.
func contextWindowWithLauncher(l *proto.ManagedLauncher, model string, recorded, used int, fallbackModels ...string) int {
	if recorded > 0 {
		return recorded
	}
	if l != nil {
		if w := launcherContextWindow(*l, model, fallbackModels...); w > 0 {
			if l.MaxContext <= 0 && w == claudeWindow && (used > claudeWindow || strings.Contains(strings.ToLower(strings.TrimSpace(model)), oneMillionSuffix)) {
				return claudeWindow1M
			}
			return w
		}
	}
	return contextWindowOf(model, recorded, used)
}

// differentClaudeModels reports whether run and said name two distinct Claude models (neither is a dated spelling of
// the other), such as an overload fallback answering in place of the session's configured Claude model.
func differentClaudeModels(run, said string) bool {
	r := claudeModelAfterRoute(strings.ToLower(proto.TrimModelWindow(run)))
	s := claudeModelAfterRoute(strings.ToLower(proto.TrimModelWindow(said)))
	return r != "" && s != "" && !strings.HasPrefix(s, r) && !strings.HasPrefix(r, s)
}

// claudeModelAfterRoute is id or, for a gateway's routed name ("passthrough/claude-opus-5-5"), the Claude model after
// its last "/"; "" when neither names a Claude model. id is lower case.
func claudeModelAfterRoute(id string) string {
	if strings.HasPrefix(id, "claude-") {
		return id
	}
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		if tail := id[i+1:]; strings.HasPrefix(tail, "claude-") {
			return tail
		}
	}
	return ""
}

// claudeNative1M reports a Claude model whose window is 1M without the "[1m]" opt-in, as Claude Code's own model list
// has them (2.1.311, #5758): Opus from 4.7, Sonnet and Haiku from 5, every Fable and Mythos. Opus and Sonnet 4.6 run
// with 200k there unless "[1m]" is chosen, though the API (and the model catalog, models_catalog.json) allows them 1M.
// id is lower case, claude-<family>-<major>[-<minor>], perhaps with a date ("-20250929", or Vertex's "@20250929").
// Older ids that put the version first (claude-3-5-sonnet-…) are 200k.
func claudeNative1M(id string) bool {
	id, _, _ = strings.Cut(id, "@")
	parts := strings.Split(strings.TrimPrefix(id, "claude-"), "-")
	if len(parts) < 2 {
		return false
	}
	major, ok := modelVersionPart(parts[1])
	if !ok {
		return false
	}
	minor := 0
	if len(parts) > 2 {
		minor, _ = modelVersionPart(parts[2]) // a date, or no number, is no minor version
	}
	switch parts[0] {
	case "fable", "mythos":
		return true
	case "opus":
		return major > 4 || major == 4 && minor >= 7
	case "sonnet", "haiku":
		return major >= 5
	}
	return false
}

// modelVersionPart is one component of a model's version: one or two digits (a date's eight are not).
func modelVersionPart(s string) (int, bool) {
	if s == "" || len(s) > 2 {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// setContext copies cached title provenance and cumulative output alongside the context fields.
// The context fields stay zero (and so off the wire) while the transcript has no reply yet.
func setContext(s *proto.Session, m quickMeta) {
	s.OutputTokens = m.outputTokens
	s.TitleManual = s.TitleManual || m.titleManual
	if m.title != "" && s.Title == m.title || m.title == "" && m.titleSource == "prompt" {
		// A terminal's tool-only fallback still has known transcript provenance
		// once the complete scan proves that no custom/AI title exists.
		s.TitleSource = proto.ParseTitleSource(m.titleSource)
	}
	if m.contextTokens <= 0 {
		return
	}
	s.ContextTokens = proto.Tokens32(m.contextTokens)
	s.ContextWindow = proto.Tokens32(contextWindowOf(m.model, m.contextWindow, m.contextTokens))
}

// setRuntimeContext is setContext for a runtime's session whose conversation is a transcript here (Paseo). The model
// the runtime runs it as decides the window when it names the 1M variant: the transcript records the id without the
// suffix, and behind a gateway another id altogether, so such a session read as 200k, or as no window at all, until
// a reply passed 200k (#4496). A window the transcript records itself stays, and so does the transcript's word while
// the session answers as another model: a refusal's fallback, or a reply from a Claude model that is not the one the
// runtime names (an overload fallback). The title is the runtime's too: it is the first prompt only when it is what
// Paseo makes of that prompt (runtimetitle.go, #8133).
func setRuntimeContext(s *proto.Session, m quickMeta) {
	setContext(s, m)
	runtimeTitleSource(s, m)
	if s.ContextTokens == 0 || m.contextWindow > 0 || m.modelFallback != nil {
		return
	}
	run := strings.ToLower(strings.TrimSpace(s.Model))
	if !strings.HasSuffix(run, oneMillionSuffix) {
		return
	}
	run = strings.TrimSuffix(run, oneMillionSuffix)
	said := strings.ToLower(strings.TrimSpace(m.model))
	// Two Claude ids that are not one model (neither begins the other: a transcript may add the model's date).
	if strings.HasPrefix(run, "claude-") && strings.HasPrefix(said, "claude-") && !strings.HasPrefix(said, run) && !strings.HasPrefix(run, said) {
		return
	}
	s.ContextWindow = claudeWindow1M
}

// sessionManagedLauncher returns the managed launcher associated with session id (explicit row Launcher first, else
// ownLauncher: state record, runtime row, or inferred shell/managed launcher).
func (n *Node) sessionManagedLauncher(id, explicit string) (proto.ManagedLauncher, bool) {
	if n == nil {
		return proto.ManagedLauncher{}, false
	}
	name := explicit
	if name == "" && id != "" {
		if n.state != nil {
			if st, ok := n.state.get(id); ok && st.Launcher != "" {
				name = st.Launcher
			}
		}
		if name == "" {
			if s, _, listed := n.runtimes.rowOf(id); listed && s.Launcher != "" {
				name = s.Launcher
			}
		}
		if name == "" {
			name = n.inferLauncher(id)
		}
	}
	if name == "" {
		return proto.ManagedLauncher{}, false
	}
	return n.managedLauncher(name)
}

// sessionFallbackModels returns the session's configured/displayed model IDs to try after m.model when no refusal or
// cross-Claude overload fallback occurred on the latest reply.
func (n *Node) sessionFallbackModels(s *proto.Session, m quickMeta) []string {
	if s == nil || m.modelFallback != nil || differentClaudeModels(s.Model, m.model) {
		return nil
	}
	var requested string
	if s.RequestedModel != nil && *s.RequestedModel != "" {
		requested = *s.RequestedModel
	} else if n != nil && n.state != nil && s.SessionID != "" {
		if st, ok := n.state.get(s.SessionID); ok && st.Model != "" {
			requested = st.Model
		}
	}
	hasShown := s.Model != "" && s.Model != m.model
	hasReq := requested != "" && requested != m.model && requested != s.Model
	switch {
	case hasShown && hasReq:
		return []string{s.Model, requested}
	case hasShown:
		return []string{s.Model}
	case hasReq:
		return []string{requested}
	default:
		return nil
	}
}

// sessionContextWindow computes the context window for session id from quickMeta m, falling back to its managed
// launcher's MaxContext / Models when the transcript itself records no window.
func (n *Node) sessionContextWindow(id, explicitLauncher string, s *proto.Session, m quickMeta) int {
	if m.contextWindow > 0 {
		return m.contextWindow
	}
	l, ok := n.sessionManagedLauncher(id, explicitLauncher)
	if !ok {
		return contextWindowOf(m.model, m.contextWindow, m.contextTokens)
	}
	// When the reply came from a refusal or overload fallback onto a known Claude model, keep that fallback model's own
	// window rather than attributing the configured launcher's capacity to it.
	if s != nil && (m.modelFallback != nil || differentClaudeModels(s.Model, m.model)) {
		if w := contextWindowOf(m.model, m.contextWindow, m.contextTokens); w > 0 {
			return w
		}
		return contextWindowWithLauncher(&l, m.model, m.contextWindow, m.contextTokens)
	}
	return contextWindowWithLauncher(&l, m.model, m.contextWindow, m.contextTokens, n.sessionFallbackModels(s, m)...)
}

// setSessionContext is setContext with a managed-launcher fallback when the transcript records no context window.
func (n *Node) setSessionContext(s *proto.Session, m quickMeta) {
	setContext(s, m)
	if s == nil || s.ContextTokens == 0 || m.contextWindow > 0 {
		return
	}
	s.ContextWindow = proto.Tokens32(n.sessionContextWindow(s.SessionID, s.Launcher, s, m))
}

// setRuntimeSessionContext is setRuntimeContext with a managed-launcher fallback when the transcript records no window.
func (n *Node) setRuntimeSessionContext(s *proto.Session, m quickMeta) {
	setRuntimeContext(s, m)
	if s == nil || s.ContextTokens == 0 || m.contextWindow > 0 {
		return
	}
	l, ok := n.sessionManagedLauncher(s.SessionID, s.Launcher)
	if !ok {
		return
	}
	if m.modelFallback != nil || differentClaudeModels(s.Model, m.model) {
		if s.ContextWindow == 0 {
			s.ContextWindow = proto.Tokens32(contextWindowWithLauncher(&l, m.model, m.contextWindow, m.contextTokens))
		}
		return
	}
	if w := contextWindowWithLauncher(&l, m.model, m.contextWindow, m.contextTokens, n.sessionFallbackModels(s, m)...); w > 0 {
		// Keep a runtime's explicit [1m] variant at 1M when MaxContext is unset and the catalog entry says 200k.
		if l.MaxContext <= 0 && w == claudeWindow && s.ContextWindow == claudeWindow1M {
			return
		}
		s.ContextWindow = proto.Tokens32(w)
	}
}

// applyLauncherContextWindow fills ContextWindow on a decorated non-Codex row (such as a cached offline Claude row)
// whose transcript reported tokens without a recorded window before Launcher or the launcher catalog was available.
func (n *Node) applyLauncherContextWindow(s *proto.Session) {
	if n == nil || s == nil || s.ContextTokens == 0 || s.Launcher == "" || s.Agent == "codex" || s.ModelFallback != nil {
		return
	}
	if s.ContextWindow != 0 && s.ContextWindow != claudeWindow {
		return
	}
	l, ok := n.managedLauncher(s.Launcher)
	if !ok {
		return
	}
	var req string
	if s.RequestedModel != nil {
		req = *s.RequestedModel
	}
	var w int
	if req != "" && req != s.Model {
		w = launcherContextWindow(l, s.Model, req)
	} else {
		w = launcherContextWindow(l, s.Model)
	}
	if w > 0 {
		if l.MaxContext <= 0 && w == claudeWindow && (int(s.ContextTokens) > claudeWindow || strings.Contains(strings.ToLower(s.Model), oneMillionSuffix)) {
			w = claudeWindow1M
		}
		s.ContextWindow = proto.Tokens32(w)
	}
}
