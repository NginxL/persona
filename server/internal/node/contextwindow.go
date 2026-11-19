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
// (the transcript records the id without the suffix). A model that is not a Claude one (a gateway's own) has no window
// we know: the apps then show the tokens only.
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
