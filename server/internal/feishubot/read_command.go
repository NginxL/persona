package feishubot

import (
	"strings"
	"unicode"
)

// readCommandNames are the names "/read" goes by (#11803): the English one and its Chinese alias, matched whatever
// their case.
var readCommandNames = []string{"read", "读群"} // i18n-ignore: the command's own names

// ReadCommand is the line after "/read" (or "/读群", or either with the full-width "／") when text starts with it as a
// word of its own ("/read what broke", "/read", but not "/reader"); ok is false for any other text. The hub reads it
// to take the owner's "@bot /read …" from a group (docs/extensions/bots.md "Reading a group with /read"), and the
// assistant again before it acts on one.
func ReadCommand(text string) (rest string, ok bool) {
	r := []rune(strings.TrimSpace(text))
	if len(r) < 2 || r[0] != '/' && r[0] != '／' { // i18n-ignore: the full-width slash
		return "", false
	}
	r = r[1:]
	for _, name := range readCommandNames {
		n := []rune(name)
		if len(r) < len(n) || !strings.EqualFold(string(r[:len(n)]), name) {
			continue
		}
		if len(r) > len(n) && !unicode.IsSpace(r[len(n)]) {
			continue
		}
		return strings.TrimSpace(string(r[len(n):])), true
	}
	return "", false
}
