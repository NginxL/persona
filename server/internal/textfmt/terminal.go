package textfmt

import (
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Control bytes and runes of the terminal encoding (ECMA-48); the C1 ones are two bytes in UTF-8.
const (
	escByte = 0x1b // ESC
	csiRune = 0x9b // CSI
	oscRune = 0x9d // OSC
	stRune  = 0x9c // ST, as the second byte of U+009C
	dcsRune = 0x90 // DCS
	sosRune = 0x98 // SOS
	pmRune  = 0x9e // PM
	apcRune = 0x9f // APC
)

// StripTerminalEscapes is s without terminal escape sequences and control characters: for text a shell or a CLI wrote
// with colour, prompts and shell integration in it. It drops CSI, OSC (an iTerm2 shell-integration record, an OSC 0/7/8
// title or hyperlink, OSC 52 clipboard), DCS / SOS / PM / APC and the two-byte ESC sequences, turns CR and CRLF into LF,
// and keeps LF and TAB; every other control (C0, DEL, C1) goes. Invalid UTF-8 becomes U+FFFD.
//
// A string with nothing to drop comes back as it is, without a copy.
func StripTerminalEscapes(s string) string {
	if plainText(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == escByte:
			i = skipEscape(s, i)
		case c < utf8.RuneSelf:
			i++
			switch {
			case c == '\n' || c == '\t':
				b.WriteByte(c)
			case c == '\r':
				if i < len(s) && s[i] == '\n' {
					continue // the LF is written on its own next round
				}
				b.WriteByte('\n')
			case c < 0x20 || c == 0x7f:
			default:
				b.WriteByte(c)
			}
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				b.WriteRune(utf8.RuneError)
				i++
				continue
			}
			i += size
			switch {
			case r == csiRune:
				i = skipCSI(s, i)
			case r == oscRune || r == dcsRune || r == sosRune || r == pmRune || r == apcRune:
				i = skipString(s, i)
			case r >= 0x80 && r <= 0x9f:
			default:
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// plainText: s has no escape or control character at all, so stripping it is s itself.
func plainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			return false
		}
		if r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	return true
}

// skipEscape is the index past the escape sequence whose ESC is at i. The introducer at i+1 picks the form: CSI, OSC and
// DCS / SOS / PM / APC run on, anything else is a two-byte or intermediate sequence, which ends at its final byte.
func skipEscape(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return j
	}
	switch s[j] {
	case '[':
		return skipCSI(s, j+1)
	case ']':
		return skipString(s, j+1)
	case 'P', 'X', '^', '_':
		return skipString(s, j+1)
	}
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
		j++
	}
	if j < len(s) && s[j] >= 0x30 && s[j] <= 0x7e {
		j++
	}
	return j
}

// skipCSI is the index past the CSI sequence whose body starts at i: parameter bytes, intermediate bytes, final byte. A
// body cut short ends where the next unrelated byte is.
func skipCSI(s string, i int) int {
	for i < len(s) {
		switch c := s[i]; {
		case c >= 0x30 && c <= 0x3f: // parameter
			i++
		case c >= 0x20 && c <= 0x2f: // intermediate
			i++
		case c >= 0x40 && c <= 0x7e: // final
			return i + 1
		default:
			return i
		}
	}
	return i
}

// skipString is the index past a string sequence (OSC, DCS, SOS, PM, APC) whose body starts at i: on past its BEL, ST
// (ESC \ or U+009C) or, unterminated, up to the next LF or the end. Stopping at the LF keeps a truncated record from
// swallowing the lines after it.
func skipString(s string, i int) int {
	for i < len(s) {
		switch {
		case s[i] == '\a':
			return i + 1
		case s[i] == '\n':
			return i
		case s[i] == escByte && i+1 < len(s) && s[i+1] == '\\':
			return i + 2
		case s[i] == 0xc2 && i+1 < len(s) && s[i+1] == stRune:
			return i + 2
		case s[i] < utf8.RuneSelf:
			i++
		default:
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
		}
	}
	return i
}

// TailClip is s's last n runes behind an ellipsis; a string of at most n runes comes back as it is, without a copy. The
// front goes, so the end (an error message's own words) stays.
func TailClip(s string, n int) string {
	if n < 0 {
		n = 0
	}
	i := len(s)
	for k := 0; k < n && i > 0; k++ {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	if i == 0 {
		return s
	}
	return "…" + s[i:]
}

var (
	// Startup prefixes a line may open with: the zsh console warning, gitstatus' restart line and its failure, and
	// oh-my-zsh's tag with its update question. Stripped off a line that carries a real error after them.
	startupPrefixes = []*lazyre.Regexp{
		lazyre.New(`(?i)^\[warning\]:\s*console output during zsh initialization[^.]*\.?`),
		lazyre.New(`(?i)^restart zsh to retry gitstatus initialization(?::\s*exec\s+zsh)?`),
		lazyre.New(`(?i)^gitstatus failed to initialize[^:]*:?`),
		lazyre.New(`(?i)^\[oh-my-zsh\]\s*(would you like to update\?)?`),
	}
	// startupNoiseOnly: the whole line is shell startup or prompt-integration noise.
	startupNoiseOnly = lazyre.New(`(?i)^(?:(\[warning\]:\s*)?console output during zsh initialization.*|` +
		`restart zsh to retry gitstatus initialization(?::\s*exec\s+zsh)?[ .…]*|` +
		`gitstatus failed to initialize.*|\[oh-my-zsh\].*|last login:.*|iterm2_prompt.*|powerlevel10k.*)$`)
	// meaningfulError: a line that names a failure (Python and Node exceptions end in Error / Exception).
	meaningfulError = lazyre.New(`(?i)(\berror\b|\bfatal\b|\bfailed\b|\bexception\b|\btraceback\b|[a-z]\w*(error|exception)\b|` +
		`command not found|no such file|not found|permission denied|unauthorized|forbidden|invalid|refused|timed? out|cannot|` +
		`could not|can't|unable to|unknown option|unrecognized|\b40[0134]\b|\b429\b|\b50[0234]\b)`)
	tracebackLine = lazyre.New(`(?i)^traceback\b`)
)

// LauncherErrorSummary is what a launcher's check or failed start said, as an error line: terminal escapes, bare prompt
// marks and the shell's own startup noise gone, the last line that names an error (with a little of what follows it),
// one line, cut from the front to maxRunes so the error's own words stay.
//
// A non-empty input never summarizes to ""; with no error word in it, its own lines are the summary.
func LauncherErrorSummary(s string, maxRunes int) string {
	clean := StripTerminalEscapes(s)
	var all, meaningful []string
	for _, raw := range strings.Split(clean, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		all = append(all, t)
		if t == "%" { // a zsh PROMPT_SP end-of-prompt mark
			continue
		}
		if l := stripStartupNoise(t); l != "" {
			meaningful = append(meaningful, l)
		}
	}
	lines := meaningful
	if len(lines) == 0 { // every line was startup noise: the noise itself beats nothing
		lines = all
	}
	if len(lines) == 0 {
		return ""
	}
	idx := -1
	for i, l := range lines {
		if meaningfulError.MatchString(l) {
			idx = i // the last one: a CLI prints the cause last
		}
	}
	var picked []string
	if idx < 0 {
		picked = lines
	} else {
		if idx > 0 && !tracebackLine.MatchString(lines[idx]) {
			if h := lastTracebackLine(lines[:idx]); h >= 0 {
				picked = append(picked, lines[h]) // the traceback header a Python or Node exception belongs to
			}
		}
		picked = append(picked, lines[idx])
		if idx < len(lines)-1 && utf8.RuneCountInString(lines[idx+1]) <= 200 {
			picked = append(picked, lines[idx+1]) // one short line of detail
		}
	}
	out := OneLine(strings.Join(picked, " "))
	if maxRunes <= 0 {
		return "…"
	}
	if utf8.RuneCountInString(out) > maxRunes {
		return TailClip(out, maxRunes-1)
	}
	return out
}

// lastTracebackLine is the index of the latest line that opens a traceback, in the lines before an exception, or -1.
func lastTracebackLine(lines []string) int {
	for i := len(lines) - 1; i >= 0 && len(lines)-i <= 10; i-- {
		if tracebackLine.MatchString(lines[i]) {
			return i
		}
	}
	return -1
}

// stripStartupNoise is line with its shell startup prefixes gone; "" when nothing of the line but such noise is left.
func stripStartupNoise(line string) string {
	if startupNoiseOnly.MatchString(line) {
		return ""
	}
	for {
		stripped := false
		for _, re := range startupPrefixes {
			if m := re.FindStringIndex(line); m != nil && m[0] == 0 && m[1] > 0 {
				line = strings.TrimLeft(line[m[1]:], " .:…\t")
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
		if startupNoiseOnly.MatchString(line) {
			return ""
		}
	}
	return strings.TrimSpace(line)
}
