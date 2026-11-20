package proto

import "strings"

// MaxModelIDLen bounds a model id Persona passes to an agent (a Bedrock inference-profile ARN is under 200).
const MaxModelIDLen = 256

// ModelIDRule says ValidModelID's rule in a tool's error for the model that called it (the apps' users read
// assistant-models.err_model_invalid).
const ModelIDRule = "start with a letter or digit (or ~ and then one), then only letters, digits and . _ - / : @ [ ] (at most 256 bytes)"

// ValidModelID says whether id may be passed to an agent as its model (#1644): `--model <id>` when it starts, `/model
// <id>` typed into a running Claude Code, a Codex app-server's model field. Claude Code and Codex take any string, so
// a typed id is only held to what real ids are made of: a letter or digit first (or a `~` and then one: a gateway's
// "latest" alias such as ~vendor/model-latest), then letters, digits and `. _ - / : @ [ ]`, at most MaxModelIDLen
// bytes. That covers aliases (opus), bracketed variants (opus[1m]), a gateway's vendor/model, tags (qwen3:32b), dated
// ids (claude-x@20250101) and ARNs, and keeps out what could become more than one argument or a flag on the way:
// whitespace, control characters, quotes, `$`, backticks, `;`, `|`, `&`, `*`, a leading `-`. Every place a model goes
// still quotes it (a leading `~` is never expanded); this is the rule all of them share, the same in the hub, the
// manager and the node.
func ValidModelID(id string) bool {
	if id == "" || len(id) > MaxModelIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case alnum(c):
		case i == 0 && c == '~' && len(id) > 1 && alnum(id[1]):
		case i > 0 && (c == '.' || c == '_' || c == '-' || c == '/' || c == ':' || c == '@' || c == '[' || c == ']'):
		default:
			return false
		}
	}
	return true
}

// alnum: an ASCII letter or digit.
func alnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// OneMillionSuffix is the marker a model id carries when it names the 1M context variant ("claude-opus-5-5[1m]").
const OneMillionSuffix = "[1m]"

// TrimModelWindow trims id and drops a trailing 1M marker ("[1m]", any case), so two spellings of one model compare
// equal: the transcript's echo and the launcher's declared id. It is the base model id, not necessarily one the agent
// offers (the marker says how it runs, not which model it is).
func TrimModelWindow(id string) string {
	s := strings.TrimSpace(id)
	if strings.HasSuffix(strings.ToLower(s), OneMillionSuffix) {
		return s[:len(s)-len(OneMillionSuffix)]
	}
	return s
}

// NormalizeSessionModel maps a model id a transcript reported to the launcher's own declared spelling, or hands it back
// unchanged when nothing declared matches. A gateway may echo a routed name ("passthrough/vendor/model", "group/model")
// for a model the launcher was asked for by its clean id; the apps compare the two by the declared id, so the reported
// one is folded back to it where the declaration allows the reading.
//
// declared are the ids the session's launcher offers (its main, small, subagent and tier models, and its own list).
// Matching is tried on the whole id first, with its 1M marker kept: a transcript echo of a 1M model keeps the marker
// only when its own id had one, so a plain echo does not falsely gain "[1m]" while a "[1m]" echo adopts the declared
// spelling. With no whole match, every "/" suffix is tried longest-first: "passthrough/vendor/model" tries
// "vendor/model" before "model", so a declared id that itself contains "/" still wins. Unmatched, the id is returned
// as it came (nothing is stripped blindly, so "group/auto-large" and "gateway/echo" stay whole).
func NormalizeSessionModel(raw string, declared []string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "<") && strings.HasSuffix(raw, ">") {
		return ""
	}
	declared = nonEmptyIDs(declared)
	if len(declared) == 0 {
		return raw
	}
	if got, ok := matchDeclared(raw, declared); ok {
		return got
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '/' {
			continue
		}
		suf := raw[i+1:]
		if suf == "" {
			continue
		}
		if got, ok := matchDeclared(suf, declared); ok {
			return got
		}
	}
	return raw
}

// matchDeclared tries candidate against declared: an exact case-insensitive hit stands for itself; else the base ids
// (TrimModelWindow) are compared. A base hit returns the declared id spelled without its 1M marker, keeping the
// candidate's marker only when the candidate had one, so a "[1m]" candidate keeps "[1m]" and a plain one does not gain
// it, both adopting the declared base spelling.
func matchDeclared(candidate string, declared []string) (string, bool) {
	for _, d := range declared {
		if strings.EqualFold(candidate, d) {
			return d, true
		}
	}
	candBase := TrimModelWindow(candidate)
	candHas1M := !strings.EqualFold(candidate, candBase)
	if candBase == "" {
		return "", false
	}
	for _, d := range declared {
		if strings.EqualFold(candBase, TrimModelWindow(d)) {
			if candHas1M {
				return TrimModelWindow(d) + OneMillionSuffix, true
			}
			return TrimModelWindow(d), true
		}
	}
	return "", false
}

// nonEmptyIDs trims each id and drops the empty ones.
func nonEmptyIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// SameLauncherModel says whether reported (a transcript echo, a gateway's routed name) names the declared model: the
// same id after trimming and dropping 1M markers, or one is the other's "/" suffix at a boundary (a gateway prefixing
// the model it was asked for, or a launcher declaring a "vendor/model" while the gateway echoes the bare id). Both
// empty or a bracketed placeholder ("<synthetic>") name no model.
func SameLauncherModel(reported, declared string) bool {
	r, d := strings.TrimSpace(reported), strings.TrimSpace(declared)
	if r == "" || d == "" || isPlaceholder(r) || isPlaceholder(d) {
		return false
	}
	rBase, dBase := TrimModelWindow(r), TrimModelWindow(d)
	if strings.EqualFold(rBase, dBase) {
		return true
	}
	if hasSuffixAtSlash(rBase, dBase) || hasSuffixAtSlash(dBase, rBase) {
		return true
	}
	return false
}

// hasSuffixAtSlash: s ends with suffix on a "/" boundary and both sides are more than the suffix alone.
func hasSuffixAtSlash(s, suffix string) bool {
	if !strings.Contains(s, "/") || suffix == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '/' && strings.EqualFold(s[i+1:], suffix) {
			return true
		}
	}
	return false
}

// isPlaceholder: a bracketed stand-in such as "<synthetic>" rather than a model id.
func isPlaceholder(s string) bool {
	return strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">")
}
