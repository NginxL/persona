package assistantwire

import (
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// AuthErrorWords are the words of a model service that refused a request because of sign-in, the key, billing, the
// certificate or the model picked: an error the user must fix, never worth a "Continue". The hub's agent triage
// counts them among its user errors (assistant/agents_work_turns.go userErrorWords); a turn whose whole answer is one
// of them failed (ErrorOnlyText, #4669). Lower case: matched against lower-cased text.
var AuthErrorWords = []string{"failed to authenticate", "oauth token has expired", "invalid api key", "not logged in", "please run /login",
	"issue with the selected model", "permission_error", "authentication_error", "credit balance", "billing",
	"ssl certificate", "self-signed certificate", "certificate verif"}

// upstreamErrorWords are a gateway's words for an upstream it could not reach. Only an answer that is nothing but
// them counts (ErrorOnlyText): a reply may talk about upstreams.
var upstreamErrorWords = []string{"no eligible upstream", "upstream connect error", "upstream request timeout"}

// errorOnlyMax is the longest answer ErrorOnlyText takes for an error line: Claude Code's own error lines are one
// short line ("Invalid API key · Fix external API key"); a longer answer is a reply that may mention one.
const errorOnlyMax = 300

// ErrorOnlyText: answer, the whole of what a turn said, is only a model-service error line (an auth error, a
// gateway's upstream error, an API error as Claude Code writes it), not a reply: the turn failed even when the
// runtime did not flag it (#4669). One short line only.
func ErrorOnlyText(answer string) bool {
	t := strings.TrimSpace(answer)
	if t == "" || strings.Contains(t, "\n") || utf8.RuneCountInString(t) > errorOnlyMax {
		return false
	}
	if CutOff(t) {
		return true
	}
	return AuthErrorText(t) || containsAny(strings.ToLower(t), upstreamErrorWords)
}

// AuthErrorText: text has one of AuthErrorWords.
func AuthErrorText(text string) bool {
	return containsAny(strings.ToLower(text), AuthErrorWords)
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// toolSearchWords name Claude Code's tool search in an endpoint's refusal (#10365): its ToolSearch tool, the
// defer_loading flag of a deferred tool, the tool_reference blocks that load one, the tool_search_* tool types. Lower
// case: matched against lower-cased text.
var toolSearchWords = []string{"toolsearch is not supported", "defer_loading", "tool_reference", "tool_search", "tool search"}

// badRequest is an HTTP 400 in an error's words. Compiled on first use (see usageLimitLine).
var badRequest = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\b400\b`) })

// ToolSearchRefused: text, the words of a turn that failed, is an endpoint refusing Claude Code's tool search (#10365):
// a 400, or a gateway's "not representable", that names it, as "API Error: 400 request is not representable on this
// edge: ToolSearch is not supported on this endpoint (defer_loading:true, tool_search_*, tool_reference)" or "400
// tools.3.defer_loading: Extra inputs are not permitted". Only a failed turn's words: a reply may talk about tool search.
// The node resends such a turn once with every tool loaded; the hub gives what is left its own code.
func ToolSearchRefused(text string) bool {
	t := strings.ToLower(text)
	if !strings.Contains(t, "not representable") && !badRequest().MatchString(t) {
		return false
	}
	return containsAny(t, toolSearchWords)
}

// ImageLimitErrorWords name model API failures caused by accumulated pictures in a conversation exceeding the API's
// request size, count or multi-image dimension limits (#13045): a gateway's 32 MB limit ("Request too large"), an image
// Claude Code stripped ("could not be processed and was removed"), or Anthropic's "many-image requests" dimension limit.
// Lower case: matched against lower-cased text of a failed turn.
var ImageLimitErrorWords = []string{
	"request too large",
	"request_too_large",
	"could not be processed and was removed",
	"many-image requests",
}

// ImageLimitErrorText reports whether a failed turn's error text describes an image-limit failure (#13045).
func ImageLimitErrorText(text string) bool {
	return containsAny(strings.ToLower(text), ImageLimitErrorWords)
}
