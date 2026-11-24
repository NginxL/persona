package mail

import (
	"bytes"
	"html"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// snippetOf turns the fetched header + body sections into a short preview.
func snippetOf(sections []imapclient.FetchBodySectionBuffer) string {
	var header, body []byte
	for _, sec := range sections {
		if sec.Section == nil {
			continue
		}
		switch sec.Section.Specifier {
		case imap.PartSpecifierHeader:
			header = sec.Bytes
		case imap.PartSpecifierText:
			body = sec.Bytes
		}
	}
	if len(body) == 0 {
		return ""
	}
	// A full section means the list fetch cut the body at snippetBytes, possibly inside a character.
	cut := len(body) >= snippetBytes
	if cut {
		body = dropSplitQPEscape(body)
	}
	raw := append(append([]byte{}, header...), body...)
	text := textFromRawCut(raw, cut)
	if text == "" {
		text = textFromRawCut(body, cut) // no usable header: try the body alone
	}
	if cut {
		text = dropSplitRune(text)
	}
	text = strings.Join(strings.Fields(text), " ")
	return truncateRunes(text, snippetRunes)
}

// dropSplitQPEscape drops a quoted-printable escape cut after its first hex digit ("=E"), which the decoder would pass
// through as the literal text "=E". In a body in another encoding those two bytes are only text at the cut.
func dropSplitQPEscape(b []byte) []byte {
	if n := len(b); n >= 2 && b[n-2] == '=' && strings.IndexByte("0123456789ABCDEFabcdef", b[n-1]) >= 0 {
		return b[:n-2]
	}
	return b
}

// dropSplitRune drops what a byte cut leaves of the last character: the U+FFFD a charset decoder (GBK, Shift_JIS)
// writes for a lead byte without its trail byte, the U+FFFD and ASCII digit (and U+FFFD) GB18030 writes for the first
// two (three) bytes of a four-byte character, or the at most utf8.UTFMax-1 bytes of an incomplete UTF-8 sequence.
func dropSplitRune(s string) string {
	const bad = string(utf8.RuneError)
	t, lone := strings.CutSuffix(s, bad)
	if n := len(t) - 1; n >= len(bad) && '0' <= t[n] && t[n] <= '9' && strings.HasSuffix(t[:n], bad) {
		return t[:n-len(bad)]
	}
	if lone {
		return t
	}
	for range utf8.UTFMax - 1 {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// splitReference is the start of an HTML character reference at the end of a text: its "&" and what of the name or
// number follows, without the ";" that would end it.
var splitReference = lazyre.New(`&(?:#[0-9]{0,7}|#[xX][0-9a-fA-F]{0,6}|[A-Za-z][A-Za-z0-9]{0,31})?$`)

// dropSplitReference drops a character reference a byte cut left unfinished at the end of s: html.UnescapeString would
// leave "&nb" as it is, and read "&#82", the start of "&#8212;", as an "R".
func dropSplitReference(s string) string {
	if i := strings.LastIndexByte(s, '&'); i >= 0 && len(s)-i <= 34 {
		if loc := splitReference.FindStringIndex(s[i:]); loc != nil {
			return s[:i]
		}
	}
	return s
}

// textFromRaw extracts readable text from a possibly truncated raw message.
func textFromRaw(raw []byte) string { return textFromRawCut(raw, false) }

// textFromRawCut is textFromRaw for a raw message that was cut where cut says so: a character reference the cut left
// unfinished at the end of an HTML part is dropped (dropSplitReference).
func textFromRawCut(raw []byte, cut bool) string {
	if len(raw) == 0 {
		return ""
	}
	// Tolerate truncation and unknown charsets: message.Read still returns a
	// usable entity for the leading bytes.
	e, _ := gomessage.Read(bytes.NewReader(raw))
	if e == nil {
		return ""
	}
	return textFromEntity(e, 0, cut)
}

const textFromEntityMaxDepth = 20 // real mail is shallow; a cap stops a crafted nest from blowing the stack

// textFromEntity returns the first non-empty text/* part, depth-first; multipart parts (e.g. alternative inside mixed)
// are descended into.
func textFromEntity(e *gomessage.Entity, depth int, cut bool) string {
	if depth > textFromEntityMaxDepth {
		return ""
	}
	if mr := e.MultipartReader(); mr != nil {
		for {
			p, err := mr.NextPart()
			if gomessage.IsUnknownEncoding(err) {
				continue // a transfer encoding go-message cannot decode: skip the part, the next one is still readable
			}
			// A part in an unknown charset comes back with its raw body, as go-message intends.
			if p == nil || (err != nil && !gomessage.IsUnknownCharset(err)) {
				return "" // end of this multipart (or a truncated tail)
			}
			if text := textFromEntity(p, depth+1, cut); text != "" {
				return text
			}
		}
	}
	mt := contentMediaType(e.Header)
	if !strings.HasPrefix(mt, "text/") {
		return ""
	}
	return renderText(mt, readAllTolerant(e.Body, 8<<10), cut)
}

// readAllTolerant reads at most n bytes, ignoring decode errors at a truncated
// tail.
func readAllTolerant(r io.Reader, n int) string {
	if r == nil {
		return ""
	}
	buf := &bytes.Buffer{}
	_, _ = io.CopyN(buf, r, int64(n))
	return buf.String()
}

// renderText converts text/plain or text/html to preview text.
func renderText(mediaType string, s string, cut bool) string {
	if strings.HasPrefix(mediaType, "text/html") {
		if cut {
			s = dropSplitReference(s)
		}
		return stripHTML(s)
	}
	return s
}

// stripHTML removes tags, comments and script, style and title content and unescapes entities.
func stripHTML(s string) string {
	var b strings.Builder
	lower := asciiLower(s) // same length as s: tags are matched at s's offsets
	i := 0
	var last byte // the last byte written
	for i < len(s) {
		if s[i] != '<' {
			b.WriteByte(s[i])
			last = s[i]
			i++
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			end := strings.Index(s[i+len("<!--"):], "-->")
			if end < 0 {
				break // the rest is a comment
			}
			i += len("<!--") + end + len("-->")
			continue
		}
		end := tagEnd(s, i)
		if end < 0 {
			break
		}
		tag := lower[i : end+1]
		i = end + 1
		if name := bodyHidden(tag); name != "" {
			j := strings.Index(lower[i:], "</"+name)
			if j < 0 {
				break // a preview cut off inside a <style> block: the rest is CSS, not text
			}
			i += j
		} else if strings.HasPrefix(tag, "<br") || strings.HasPrefix(tag, "</p") || strings.HasPrefix(tag, "</div") {
			b.WriteByte(' ')
			last = ' '
		} else if isBlockTag(tag) && b.Len() > 0 && !strings.ContainsRune(" \t\r\n", rune(last)) {
			b.WriteByte(' ')
			last = ' '
		}
	}
	return dropPadding(html.UnescapeString(b.String()))
}

// isPadding: the characters a mail pads its hidden preheader with, so that mail apps pull no body text into the
// preview, and that strings.Fields does not take for white space: zero-width space, non-joiner and joiner, word
// joiner, byte order mark, soft hyphen and combining grapheme joiner.
func isPadding(r rune) bool {
	switch r {
	case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff', '\u00ad', '\u034f':
		return true
	}
	return false
}

// dropPadding is s without runs of padding characters that stand next to white space or at an end of s. A run between
// two other characters is part of a word and stays: U+200C in Persian and Indic words, U+200D in emoji sequences and
// Indic conjuncts, U+00AD where a word may be broken.
func dropPadding(s string) string {
	if !strings.ContainsFunc(s, isPadding) {
		return s
	}
	rs := []rune(s)
	out := make([]rune, 0, len(rs))
	for i := 0; i < len(rs); {
		if !isPadding(rs[i]) {
			out = append(out, rs[i])
			i++
			continue
		}
		j := i
		for j < len(rs) && isPadding(rs[j]) {
			j++
		}
		if i > 0 && j < len(rs) && !unicode.IsSpace(rs[i-1]) && !unicode.IsSpace(rs[j]) {
			out = append(out, rs[i:j]...)
		}
		i = j
	}
	return string(out)
}

// tagEnd is the index of the ">" that ends the tag s[i] (a "<") starts, -1 when there is none: a ">" inside a quoted
// attribute value (one opened right after "=") does not end it.
func tagEnd(s string, i int) int {
	var quote byte
	for j := i + 1; j < len(s); j++ {
		switch c := s[j]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			k := j - 1
			for k > i && (s[k] == ' ' || s[k] == '\t' || s[k] == '\r' || s[k] == '\n') {
				k--
			}
			if s[k] == '=' {
				quote = c
			}
		case c == '>':
			return j
		}
	}
	return -1
}

// bodyHidden is the name of the element tag (lower-cased) opens when what it holds is no part of the text: script,
// style and title; "" for any other tag.
func bodyHidden(tag string) string {
	for _, name := range []string{"script", "style", "title"} {
		if rest, ok := strings.CutPrefix(tag, "<"+name); ok && rest != "" && strings.IndexByte(">/ \t\r\n", rest[0]) >= 0 {
			return name
		}
	}
	return ""
}

// blockTags are the elements that lay out what is inside them apart from what is around them, as a paragraph does:
// table cells and rows, lists and their items, headings and block quotes.
var blockTags = map[string]bool{"td": true, "th": true, "tr": true, "table": true, "li": true, "ul": true, "ol": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "blockquote": true}

// isBlockTag reports whether tag, lower-cased, starts or ends one of blockTags.
func isBlockTag(tag string) bool {
	name := strings.TrimPrefix(strings.TrimPrefix(tag, "<"), "/")
	if end := strings.IndexFunc(name, func(r rune) bool { return !('a' <= r && r <= 'z' || '0' <= r && r <= '9') }); end >= 0 {
		name = name[:end]
	}
	return blockTags[name]
}

// asciiLower lowers A–Z only, byte for byte. strings.ToLower can change the length (U+0130 and the Kelvin sign lower
// to one byte, an invalid byte becomes U+FFFD's three), and stripHTML indexes the lowered copy with s's offsets.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	rs := []rune(s)
	return string(rs[:n])
}

// PlainText is an HTML body as readable text (styles and scripts dropped, whitespace collapsed).
func PlainText(html string) string { return strings.Join(strings.Fields(stripHTML(html)), " ") }

var (
	blockTag   = lazyre.New(`(?i)<\s*(br|/p|/div|/ul|/ol|/tr|/h[1-6]|/blockquote|/table)\b[^>]*>`)
	listItem   = lazyre.New(`(?i)<\s*li\b[^>]*>`)
	spaces     = lazyre.New(`[ \t\x{00a0}]+`)
	blankLines = lazyre.New(`\n{3,}`)
)

// ReadableText is an HTML mail as text that keeps its paragraphs and list items (PlainText flattens everything onto
// one line, fine for a preview but not for reading or translating).
func ReadableText(h string) string {
	h = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(h) // line breaks in the source are only spaces
	h = blockTag.ReplaceAllString(h, "\n$0")
	h = listItem.ReplaceAllString(h, "\n• $0")
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(stripHTML(h), "\r", ""), "\n") {
		lines = append(lines, strings.TrimSpace(spaces.ReplaceAllString(l, " ")))
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
