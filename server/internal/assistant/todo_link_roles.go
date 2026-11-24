package assistant

import (
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Link roles are decided twice, without any network call on the report path: prose that explicitly frames a PR as
// related work or background marks it reference at import; the acceptance gate additionally uses forge ownership
// evidence (supervise_review_delivery.go). Anything not provably a reference stays delivery, preserving the old gate.

var (
	// referenceLineRe matches a line opened by a related-work/background marker; every link on such a line is a
	// reference. The (?m) flags anchor ^/$ to line boundaries rather than the whole report.
	referenceLineRe = lazyre.New(`(?im)^\s*(?:[-*]\s*)?(?:related\s+(?:work|prs?|pull\s+requests?)|background|see\s+also|references?)\b`) // i18n-ignore: matches Agent report prose
	// referenceClauseRe matches an in-sentence disclaimer that a named PR is related work, explicitly framed as
	// background or not this task's delivery. A bare "background" is deliberately not enough: ordinary delivery
	// prose ("fixed the background sync worker in <PR>") must keep the delivery role. Only links inside the same
	// sentence carry the marker, so a delivery PR in the next sentence keeps its role.
	referenceClauseRe = lazyre.New(`(?i)(?:related\s+work|background(?:\s+work)?\s*:|(?:as|for|is)\s+background(?:\s+work)?\b|not\s+this\s+(?:task|research|work)'?s?\s+delivery|not\s+(?:our|my)\s+delivery|rather\s+than\s+(?:this|our|my)\s+delivery)`) // i18n-ignore: matches Agent report prose
	citationBlockRe   = lazyre.New(`(?s)<oai-mem-citation>.*?</oai-mem-citation>`)
)

// referenceReportLinks returns the raw URLs in an Agent report that prose or citation metadata frames as references
// rather than delivery artifacts. It only reads the report text; callers still run the forge-ownership check.
func referenceReportLinks(text string, raws []string) map[string]bool {
	out := map[string]bool{}
	if strings.TrimSpace(text) == "" || len(raws) == 0 {
		return out
	}
	// URLs inside runtime citation footers are citations, never deliveries.
	for _, block := range citationBlockRe.FindAllString(text, -1) {
		for _, raw := range raws {
			if containsURL(block, raw) {
				out[raw] = true
			}
		}
	}
	for _, line := range strings.Split(text, "\n") {
		lineIsReference := referenceLineRe.MatchString(line)
		var sentences []string
		if lineIsReference {
			sentences = []string{line}
		} else {
			sentences = splitSentencesKeepingURLs(line, raws)
		}
		for _, sentence := range sentences {
			if !lineIsReference && !referenceClauseRe.MatchString(sentence) {
				continue
			}
			for _, raw := range raws {
				if containsURL(sentence, raw) {
					out[raw] = true
				}
			}
		}
	}
	return out
}

// containsURL reports whether text contains raw as a complete URL, not as a prefix of a longer address. A match whose
// next rune is alphanumeric or a path separator is a longer URL (e.g. ".../pull/1" inside ".../pull/11"), not a hit.
func containsURL(text, raw string) bool {
	start := 0
	for {
		at := strings.Index(text[start:], raw)
		if at < 0 {
			return false
		}
		after := start + at + len(raw)
		if after >= len(text) {
			return true
		}
		switch r := text[after]; {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '/':
			start = after // longer address; keep looking for a complete occurrence
		default:
			return true
		}
	}
}

// splitSentencesKeepingURLs breaks a line at sentence/semicolon boundaries, but never at punctuation that sits inside
// a reported URL (URLs contain '.' and ';'), so a disclaimer stays bound to the link it actually sits next to.
func splitSentencesKeepingURLs(line string, raws []string) []string {
	protected := make([]bool, len(line))
	for _, raw := range raws {
		start := 0
		for {
			at := strings.Index(line[start:], raw)
			if at < 0 {
				break
			}
			lo, hi := start+at, start+at+len(raw)
			for i := lo; i < hi; i++ {
				protected[i] = true
			}
			start = hi
		}
	}
	var out []string
	start := 0
	for i, r := range line {
		if (r == '.' || r == ';' || r == '。' || r == '；') && !protected[i] {
			out = append(out, line[start:i])
			start = i + 1
		}
	}
	out = append(out, line[start:])
	return out
}
