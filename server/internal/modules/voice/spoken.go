package voice

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// An answer made speakable (docs/assistant/voice.md "Result"): code blocks, tables, inline code, paths, file:line, hashes, links
// and the Markdown marks become short stand-ins (prompts/voice/spoken.md), so the voice model has the whole answer to
// sum up and to answer follow-up questions from, with nothing in it that it would read out sign by sign. The answer
// itself stays in the conversation, on the screen.

// spokenMost is how many characters of an answer the voice model gets at most: its commentary holds 4000 (wire.go),
// with the report's own words and the tasks around it (reportWords cuts it further when they need the room). A longer
// one keeps its start and its end.
const spokenMost = 3000

var (
	fenceRe    = lazyre.New("^\\s{0,3}(`{3,}|~{3,})")
	tableSepRe = lazyre.New(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	headingRe  = lazyre.New(`^\s{0,3}#{1,6}\s+`)
	quoteRe    = lazyre.New(`^\s{0,3}(?:>\s?)+`)
	listRe     = lazyre.New(`^\s*(?:[-*+]|\d{1,3}[.)])\s+(?:\[[ xX]\]\s+)?`)
	ruleRe     = lazyre.New(`^\s{0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)

	inlineCodeRe = lazyre.New("(`+)([^`\n]+?)(`+)")
	imageRe      = lazyre.New(`!\[([^\]\n]*)\]\([^)\n]*\)`)
	linkRe       = lazyre.New(`\[([^\]\n]+)\]\([^)\s]+(?:\s+"[^"\n]*")?\)`)
	autolinkRe   = lazyre.New(`<((?:https?|ftp)://[^>\s]+)>`)
	urlRe        = lazyre.New(`\b(?:(?:https?|ftp|file)://|www\.)[^\s<>"\x{80}-\x{10FFFF}]+`)
	fileLineRe   = lazyre.New(`((?:~/|\.{1,2}/|/)?(?:[\w@+-][\w.@+-]*/)*[\w@+-][\w.@+-]*\.[A-Za-z][A-Za-z0-9]{0,7}):(\d+)(?::\d+)?\b`)
	pathRe       = lazyre.New(`(?:~/|\.{1,2}/|/)?(?:[\w@+-][\w.@+-]*/)+[\w@+-][\w.@+-]*`)
	extRe        = lazyre.New(`\.[A-Za-z][A-Za-z0-9]{0,7}$`)
	uuidRe       = lazyre.New(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	hashRe       = lazyre.New(`\b[0-9a-f]{7,64}\b`)
	boldRe       = lazyre.New(`(\*\*|__)([^\n]+?)(\*\*|__)`)
	strikeRe     = lazyre.New(`~~([^\n]+?)~~`)
	emRe         = lazyre.New(`(^|[^\w*])\*([^*\s][^*\n]*?)\*($|[^\w*])`)
	brRe         = lazyre.New(`(?i)<br\s*/?>`)
	blankRunsRe  = lazyre.New(`\n{3,}`)
	heldRe       = lazyre.New("\uE000(\\d+)\uE001")
)

// inlineCodeMost: inline code this short is said as it is (`npm test`); longer is a stand-in.
const inlineCodeMost = 32

// speaker writes one answer's stand-ins; what is already final is held behind a mark (U+E000 n U+E001) so the passes
// after it leave it alone.
type speaker struct {
	l    i18n.Lang
	held []string
}

func (s *speaker) stand(kind string, n int, name string) string {
	return prompts.RenderVoiceSpoken(prompts.VoiceSpoken{Lang: string(s.l), Kind: kind, N: n, Name: name})
}

func (s *speaker) hold(text string) string {
	s.held = append(s.held, s.expand(text)) // a held text never holds a mark: one expand puts every one back
	return fmt.Sprintf("\uE000%d\uE001", len(s.held)-1)
}

// expand puts back what text holds behind marks.
func (s *speaker) expand(text string) string {
	return heldRe.ReplaceAllStringFunc(text, func(m string) string {
		i, _ := strconv.Atoi(heldRe.FindStringSubmatch(m)[1])
		return s.held[i]
	})
}

// speakable is md with what is not to be read out replaced by stand-ins and the Markdown marks gone.
func speakable(l i18n.Lang, md string) string {
	md = strings.NewReplacer("\r\n", "\n", "\uE000", "", "\uE001", "").Replace(md) // the marks are ours
	sp := &speaker{l: l}
	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			fence, n, j := m[1], 0, i+1
			for ; j < len(lines); j++ {
				if t := strings.TrimSpace(lines[j]); strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
					break
				}
				n++
			}
			out = append(out, sp.stand("code", n, ""))
			i = j
			continue
		}
		if tableRow(line) && i+1 < len(lines) && tableSepRe.MatchString(lines[i+1]) {
			j := i + 2
			for j < len(lines) && tableRow(lines[j]) {
				j++
			}
			out = append(out, sp.stand("table", j-i-2, ""))
			i = j - 1
			continue
		}
		if ruleRe.MatchString(line) {
			continue
		}
		line = headingRe.ReplaceAllString(line, "")
		line = quoteRe.ReplaceAllString(line, "")
		line = listRe.ReplaceAllString(line, "")
		out = append(out, sp.inline(line))
	}
	text := sp.expand(strings.Join(out, "\n"))
	return strings.TrimSpace(blankRunsRe.ReplaceAllString(text, "\n\n"))
}

func tableRow(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "|") && strings.Count(t, "|") >= 2
}

// inline does one line's inline marks.
func (s *speaker) inline(line string) string {
	line = inlineCodeRe.ReplaceAllStringFunc(line, func(m string) string {
		g := inlineCodeRe.FindStringSubmatch(m)
		if len(g[1]) != len(g[3]) {
			return m
		}
		return s.hold(s.code(strings.TrimSpace(g[2])))
	})
	line = imageRe.ReplaceAllStringFunc(line, func(string) string { return s.hold(s.stand("image", 0, "")) })
	line = linkRe.ReplaceAllStringFunc(line, func(m string) string {
		text := strings.TrimSpace(linkRe.FindStringSubmatch(m)[1])
		if urlRe.MatchString(text) {
			return s.hold(s.stand("link", 0, ""))
		}
		return s.hold(marks(s.words(text))) // its text is read: what it holds is said as everywhere else
	})
	line = autolinkRe.ReplaceAllStringFunc(line, func(string) string { return s.hold(s.stand("link", 0, "")) })
	line = urlRe.ReplaceAllStringFunc(line, func(m string) string {
		rest := m[len(strings.TrimRight(m, ".,;:!?)]'")):] // the sentence's punctuation, not the link's
		return s.hold(s.stand("link", 0, "")) + rest
	})
	return marks(s.words(line))
}

// marks drops the emphasis marks and turns <br> into a space.
func marks(line string) string {
	line = boldRe.ReplaceAllString(line, "$2")
	line = strikeRe.ReplaceAllString(line, "$1")
	line = emRe.ReplaceAllString(line, "$1$2$3")
	return brRe.ReplaceAllString(line, " ")
}

// words does a line's paths, file:line, UUIDs and hashes.
func (s *speaker) words(line string) string {
	line = s.paths(line)
	line = uuidRe.ReplaceAllStringFunc(line, func(string) string { return s.hold(s.stand("hash", 0, "")) })
	return hashRe.ReplaceAllStringFunc(line, func(m string) string {
		if !strings.ContainsAny(m, "0123456789") || !strings.ContainsAny(m, "abcdef") {
			return m // a number, a word
		}
		return s.hold(s.stand("hash", 0, ""))
	})
}

// code is what inline code says: a file and line, a path, a link, a hash, itself when it is short, else a stand-in.
func (s *speaker) code(c string) string {
	switch {
	case fileLineRe.FindString(c) == c:
		return s.fileLine(c)
	case pathRe.FindString(c) == c && isPath(c):
		return s.stand("path", 0, baseName(c))
	case urlRe.FindString(c) == c:
		return s.stand("link", 0, "")
	case uuidRe.FindString(c) == c || hashRe.FindString(c) == c && strings.ContainsAny(c, "0123456789") && strings.ContainsAny(c, "abcdef"):
		return s.stand("hash", 0, "")
	case len([]rune(c)) <= inlineCodeMost:
		return c
	}
	return s.stand("snippet", 0, "")
}

// paths replaces file:line and paths in prose (a fraction, a date or "and/or" is not one).
func (s *speaker) paths(line string) string {
	line = fileLineRe.ReplaceAllStringFunc(line, func(m string) string { return s.hold(s.fileLine(m)) })
	return pathRe.ReplaceAllStringFunc(line, func(m string) string {
		if !isPath(m) {
			return m
		}
		return s.hold(s.stand("path", 0, baseName(m)))
	})
}

func (s *speaker) fileLine(m string) string {
	g := fileLineRe.FindStringSubmatch(m)
	n, _ := strconv.Atoi(g[2])
	return s.stand("line", n, baseName(g[1]))
}

// isPath: p (with a slash in it) is a file's path: rooted, or its last part has an extension, or it has three parts or
// more, and not every part is a number.
func isPath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") {
		return true
	}
	parts := strings.Split(p, "/")
	numbers := true
	for _, part := range parts {
		numbers = numbers && strings.Trim(part, "0123456789.") == ""
	}
	if numbers {
		return false
	}
	return extRe.MatchString(parts[len(parts)-1]) || len(parts) >= 3
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	return p[strings.LastIndex(p, "/")+1:]
}

// clipMiddle keeps text within most characters: its start and its end, the middle replaced by the omitted stand-in
// (only that, when most leaves no room for more).
func clipMiddle(l i18n.Lang, text string, most int) string {
	r := []rune(text)
	if len(r) <= most {
		return text
	}
	omitted := prompts.RenderVoiceSpoken(prompts.VoiceSpoken{Lang: string(l), Kind: "omitted"})
	keep := most - len([]rune(omitted)) - 2
	if keep < 2 {
		return omitted
	}
	head := keep * 2 / 3
	tail := keep - head
	return strings.TrimSpace(string(r[:head])) + "\n" + omitted + "\n" + strings.TrimSpace(string(r[len(r)-tail:]))
}
