package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"mime/quotedprintable"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// A list page fetches only the first snippetBytes of a body, so the cut can land inside a character. When the text
// in those bytes is short (HTML with heavy markup), the snippet used to end in the invalid bytes or the U+FFFD the
// split character turned into.

var (
	snippetHan     = strings.Repeat("中文", 400)  // i18n-ignore
	snippetNiHao   = strings.Repeat("你好", 400)  // i18n-ignore
	snippetNihongo = strings.Repeat("日本語", 300) // i18n-ignore
	// GB18030 writes a character outside its two-byte table (an emoji) as four bytes: lead, digit, lead, digit.
	snippetEmoji = strings.Repeat("\U0001F600", 300)
)

// snippetHTML is an HTML body whose <style> block (css rules of 11 bytes) fills most of the first snippetBytes once
// encoded, so the visible text in the fetched part is well under snippetRunes; pad shifts where the cut lands.
func snippetHTML(css int, text string, pad int) string {
	return "<style>" + strings.Repeat("p{margin:0}", css) + strings.Repeat(" ", pad) + "</style><p>x" + text + "</p>"
}

// listSections is what a list fetch returns for header and body: the body cut to snippetBytes.
func listSections(header, body string) []imapclient.FetchBodySectionBuffer {
	if len(body) > snippetBytes {
		body = body[:snippetBytes]
	}
	return []imapclient.FetchBodySectionBuffer{
		{Section: &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader}, Bytes: []byte(header)},
		{Section: &imap.FetchItemBodySection{Specifier: imap.PartSpecifierText}, Bytes: []byte(body)},
	}
}

func quotedPrintable(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// base64Lines is s in base64 with a CRLF every 76 characters, the way mail carries it.
func base64Lines(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.String()
}

func encodeOrFail(t *testing.T, encode func(string) (string, error), s string) string {
	t.Helper()
	out, err := encode(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// checkCutSnippet fails unless snippet is a clean prefix of text that kept what was fetched.
func checkCutSnippet(t *testing.T, snippet, text string) {
	t.Helper()
	if !utf8.ValidString(snippet) || strings.ContainsRune(snippet, utf8.RuneError) {
		t.Fatalf("snippet ends in a broken character: %q", snippet[max(0, len(snippet)-12):])
	}
	if !strings.HasPrefix(text, snippet) || utf8.RuneCountInString(snippet) < 40 {
		t.Fatalf("snippet = %q, want a long prefix of the text", snippet)
	}
}

func TestSnippetOfDropsTheCharacterTheCutSplits(t *testing.T) {
	gbk := simplifiedchinese.GBK.NewEncoder().String
	gb18030 := simplifiedchinese.GB18030.NewEncoder().String
	sjis := japanese.ShiftJIS.NewEncoder().String
	cases := []struct {
		name   string
		header string
		body   func(pad int) string
		text   string // the visible text; the snippet is a prefix of it
		pads   int    // enough shifts to land the cut on every byte of a character
	}{
		{
			name:   "utf-8 8bit",
			header: "Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n",
			body:   func(pad int) string { return snippetHTML(160, snippetHan, pad) },
			text:   "x" + snippetHan, pads: 3,
		},
		{
			// The cut can leave "=E4=BD": the first two bytes of a three-byte character.
			name:   "utf-8 quoted-printable",
			header: "Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n",
			body:   func(pad int) string { return quotedPrintable(t, snippetHTML(130, snippetNiHao, pad)) },
			text:   "x" + snippetNiHao, pads: 12,
		},
		{
			name:   "utf-8 base64",
			header: "Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n",
			body:   func(pad int) string { return base64Lines(snippetHTML(110, snippetHan, pad)) },
			text:   "x" + snippetHan, pads: 3,
		},
		{
			// The GBK decoder turns a lead byte with no trail byte after it into U+FFFD.
			name:   "gbk base64",
			header: "Content-Type: text/html; charset=gbk\r\nContent-Transfer-Encoding: base64\r\n\r\n",
			body:   func(pad int) string { return base64Lines(encodeOrFail(t, gbk, snippetHTML(110, snippetHan, pad))) },
			text:   "x" + snippetHan, pads: 2,
		},
		{
			name:   "gb18030 8bit",
			header: "Content-Type: text/html; charset=gb18030\r\nContent-Transfer-Encoding: 8bit\r\n\r\n",
			body:   func(pad int) string { return encodeOrFail(t, gb18030, snippetHTML(160, snippetHan, pad)) },
			text:   "x" + snippetHan, pads: 2,
		},
		{
			// A cut after the second or third byte of a four-byte character decodes to U+FFFD and the ASCII digit.
			name:   "gb18030 8bit four-byte",
			header: "Content-Type: text/html; charset=gb18030\r\nContent-Transfer-Encoding: 8bit\r\n\r\n",
			body:   func(pad int) string { return encodeOrFail(t, gb18030, snippetHTML(160, snippetEmoji, pad)) },
			text:   "x" + snippetEmoji, pads: 4,
		},
		{
			name:   "shift_jis 8bit",
			header: "Content-Type: text/html; charset=shift_jis\r\nContent-Transfer-Encoding: 8bit\r\n\r\n",
			body:   func(pad int) string { return encodeOrFail(t, sjis, snippetHTML(160, snippetNihongo, pad)) },
			text:   "x" + snippetNihongo, pads: 2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			split := 0
			for pad := range c.pads {
				sections := listSections(c.header, c.body(pad))
				if r, _ := utf8.DecodeLastRuneInString(textFromRaw(append([]byte(c.header), sections[1].Bytes...))); r == utf8.RuneError {
					split++
				}
				checkCutSnippet(t, snippetOf(sections), c.text)
			}
			if split == 0 {
				t.Fatal("no pad split a character: the fixture no longer tests a cut")
			}
		})
	}
}

// Only a cut body is trimmed: a complete one keeps its text, a U+FFFD it really ends in included.
func TestSnippetOfKeepsACompleteBody(t *testing.T) {
	short := snippetHan[:30] // ten whole characters
	for _, c := range []struct{ header, body, want string }{
		{"Content-Type: text/plain; charset=utf-8\r\n\r\n", "Short note �", "Short note �"},
		{"Content-Type: text/html; charset=utf-8\r\n\r\n", "<p>x" + short + "</p>", "x" + short},
		{"Content-Type: text/plain; charset=gbk\r\nContent-Transfer-Encoding: base64\r\n\r\n",
			base64Lines(encodeOrFail(t, simplifiedchinese.GBK.NewEncoder().String, short)), short},
	} {
		if got := snippetOf(listSections(c.header, c.body)); got != c.want {
			t.Errorf("snippet of %q = %q, want %q", c.body, got, c.want)
		}
	}
}

// The same through a real list fetch: the server cuts the BODY.PEEK partial, wherever the text then splits.
func TestListMessagesSnippetCutInsideACharacter(t *testing.T) {
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	for pad := range 3 {
		body := snippetHTML(160, snippetHan, pad)
		raw := strings.Replace(rawMessage("Cut"+itoa(uint32(pad)), "news@example.com", body), "text/plain", "text/html", 1)
		srv.appendMessage(t, "INBOX", raw)
	}
	res, err := s.ListMessages(context.Background(), ListOptions{AccountID: "acc_test", Limit: 3})
	if err != nil || len(res.Messages) != 3 {
		t.Fatalf("list = %+v, %v", res, err)
	}
	for _, m := range res.Messages {
		checkCutSnippet(t, m.Snippet, "x"+snippetHan)
	}
}

// The list fetch's cut can land inside an HTML character reference. html.UnescapeString would leave the start of a
// name as typed ("&nb") and read the start of a number as the number so far ("&#82" of "&#8212;" is "R"), so the
// preview must drop what the cut left of it, as it drops a split character.
func TestSnippetOfDropsTheReferenceTheCutSplits(t *testing.T) {
	const header = "Content-Type: text/html; charset=utf-8\r\n\r\n"
	snippet := func(tail string) string {
		text := "<p>Hello world "
		fill := snippetBytes - len("<style></style>") - len(text) - len(tail)
		return snippetOf(listSections(header, "<style>"+strings.Repeat(" ", fill)+"</style>"+text+tail+"more text"))
	}
	for _, c := range []struct{ name, tail, want string }{
		{"a name cut short", "&nb", "Hello world"},
		{"an unknown name", "&eacu", "Hello world"},
		{"the start of an em dash", "&#82", "Hello world"},
		{"the start of a hexadecimal reference", "&#x2", "Hello world"},
		{"a lone ampersand", "&", "Hello world"},
		{"a complete name", "&nbsp;", "Hello world"},
		{"a complete reference with text", "&amp;", "Hello world &"},
		{"a complete number", "&#8212;", "Hello world —"},
	} {
		if got := snippet(c.tail); got != c.want {
			t.Errorf("%s: snippet = %q, want %q", c.name, got, c.want)
		}
	}
	// A body that was not cut ends as it ends.
	if got := snippetOf(listSections(header, "<p>Tom &amp; Jerry &")); got != "Tom & Jerry &" {
		t.Errorf("uncut: snippet = %q", got)
	}
}
