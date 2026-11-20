package feishubot

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Card is a message card (card JSON 2.0: Markdown with headings, tables and code blocks; Feishu 7.20 and later).
// Buttons carry a value that comes back in the card.action.trigger callback (CardAction.Action.Value); buttons inside
// a form submit it too (CardAction.Action.FormValue). 2.0 rejects unknown fields, so only documented ones are written.
type Card struct {
	Schema string         `json:"schema"`
	Config map[string]any `json:"config"`
	Header *cardHeader    `json:"header,omitempty"`
	Body   cardBody       `json:"body"`
}

type cardHeader struct {
	Title       plain  `json:"title"`
	Template    string `json:"template,omitempty"`
	TextTagList []any  `json:"text_tag_list,omitempty"` // tags on the right of the title
}

type cardBody struct {
	Elements []any `json:"elements"`
}

type plain struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

func text(s string) plain { return plain{Tag: "plain_text", Content: s} }

// Header colors. A header's color is the card's state (docs/extensions/bots.md "How the cards look"): orange, the
// owner is to act; red, urgent or failed; blue, under way or only to know; green, done; grey, over. Tags take the same
// names, and Neutral (grey) for what only says the card's kind.
const (
	Blue    = "blue"
	Green   = "green"
	Orange  = "orange"
	Red     = "red"
	Grey    = "grey"
	Purple  = "purple"
	Wathet  = "wathet"
	Neutral = "neutral"
)

// NewCard starts a card; an empty title leaves the header out. summary is what the chat list shows for it (an
// assistant card in it as its title or fallback, never its JSON).
func NewCard(title, template, summary string) *Card {
	cfg := map[string]any{"update_multi": true}
	if s := strings.TrimSpace(wire.AssistantCardText(summary)); s != "" {
		cfg["summary"] = map[string]any{"content": clipRunes(oneLine(s), 60)}
	}
	c := &Card{Schema: "2.0", Config: cfg, Body: cardBody{Elements: []any{}}}
	if title != "" {
		c.Header = &cardHeader{Title: text(title), Template: template}
	}
	return c
}

// Markdown adds a block of card Markdown (CardMarkdown for a model's answer, EscapeMarkdown for anything typed).
func (c *Card) Markdown(s string) *Card {
	if strings.TrimSpace(s) == "" {
		return c
	}
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "markdown", "content": s})
	return c
}

// Note adds a line of small grey text.
func (c *Card) Note(s string) *Card {
	if s == "" {
		return c
	}
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "div",
		"text": map[string]any{"tag": "plain_text", "content": s, "text_size": "notation", "text_color": "grey"}})
	return c
}

// Collapsible adds a folded panel: title on its header, md inside.
func (c *Card) Collapsible(title, md string) *Card {
	if strings.TrimSpace(md) == "" {
		return c
	}
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "collapsible_panel", "expanded": false,
		"header":   map[string]any{"title": map[string]any{"tag": "markdown", "content": title}},
		"elements": []any{map[string]any{"tag": "markdown", "content": md}}})
	return c
}

func (c *Card) HR() *Card {
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "hr"})
	return c
}

// Button is one button of a card.
type Button map[string]any

// Button styles.
const (
	Primary = "primary"
	Default = "default"
	Danger  = "danger"
)

// Btn is a button that calls back with value.
func Btn(label, style string, value map[string]any) Button {
	return Button{"tag": "button", "text": text(label), "type": style, "behaviors": []any{map[string]any{"type": "callback", "value": value}}}
}

// Submit is a form's button: it calls back with value and the form's fields. name is unique in the card.
func Submit(label, style, name string, value map[string]any) Button {
	b := Btn(label, style, value)
	b["form_action_type"], b["name"] = "submit", name
	return b
}

// LinkBtn opens a page (http(s): what card links are documented to take).
func LinkBtn(label, url string) Button {
	open := map[string]any{"type": "open_url", "default_url": url, "pc_url": url, "android_url": url, "ios_url": url}
	return Button{"tag": "button", "text": text(label), "type": Default, "behaviors": []any{open}}
}

// buttonRow lays buttons side by side, wrapping onto more lines on a narrow screen.
func buttonRow(bs []Button) map[string]any {
	cols := make([]any, len(bs))
	for i, b := range bs {
		cols[i] = map[string]any{"tag": "column", "width": "auto", "elements": []any{b}}
	}
	return map[string]any{"tag": "column_set", "flex_mode": "flow", "horizontal_spacing": "8px", "columns": cols}
}

// Buttons adds a row of buttons; empty ones (nil) are left out, and no row when none is left.
func (c *Card) Buttons(bs ...Button) *Card {
	bs = slices.DeleteFunc(slices.Clone(bs), func(b Button) bool { return len(b) == 0 })
	if len(bs) == 0 {
		return c
	}
	c.Body.Elements = append(c.Body.Elements, buttonRow(bs))
	return c
}

// AppendToLinks adds a navigation button to the last row when that row already consists only of open_url actions. A
// callback action row (approval, form or another command) stays separate from navigation. Otherwise this is Buttons.
func (c *Card) AppendToLinks(b Button) *Card {
	if len(b) == 0 {
		return c
	}
	if len(c.Body.Elements) > 0 {
		row, ok := c.Body.Elements[len(c.Body.Elements)-1].(map[string]any)
		if ok && row["tag"] == "column_set" && row["flex_mode"] == "flow" && linkColumns(row["columns"]) {
			cols, _ := row["columns"].([]any)
			row["columns"] = append(cols, buttonRow([]Button{b})["columns"].([]any)...)
			return c
		}
	}
	return c.Buttons(b)
}

// linkColumns reports whether every button in a row opens a URL, rather than invoking a command callback.
func linkColumns(raw any) bool {
	cols, ok := raw.([]any)
	if !ok || len(cols) == 0 {
		return false
	}
	for _, rawCol := range cols {
		col, ok := rawCol.(map[string]any)
		if !ok {
			return false
		}
		elements, ok := col["elements"].([]any)
		if !ok {
			return false
		}
		for _, rawElement := range elements {
			var button Button
			switch b := rawElement.(type) {
			case Button:
				button = b
			case map[string]any:
				button = Button(b)
			default:
				return false
			}
			if button["tag"] != "button" {
				return false
			}
			behaviors, ok := button["behaviors"].([]any)
			if !ok || len(behaviors) == 0 {
				return false
			}
			for _, rawBehavior := range behaviors {
				behavior, ok := rawBehavior.(map[string]any)
				if !ok || behavior["type"] != "open_url" {
					return false
				}
			}
		}
	}
	return true
}

// Form adds a form: its fields (Input, Select, MarkdownBlock) and the submit buttons under them.
func (c *Card) Form(name string, fields []any, submit ...Button) *Card {
	els := append([]any{}, fields...)
	if len(submit) > 0 {
		els = append(els, buttonRow(submit))
	}
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "form", "name": name, "elements": els})
	return c
}

// Input is a text field of a form; multiline for longer answers.
func Input(name, label, placeholder string, multiline bool) map[string]any {
	m := map[string]any{"tag": "input", "name": name, "placeholder": text(placeholder), "width": "fill", "max_length": 1000}
	if label != "" {
		m["label"], m["label_position"] = text(label), "top"
	}
	if multiline {
		m["input_type"], m["rows"] = "multiline_text", 2
	}
	return m
}

// Option is one choice of a Select: what it shows and the value it submits.
type Option struct{ Label, Value string }

// Select is a picker of a form; multi lets several be picked (their values come back as a list).
func Select(name, placeholder string, options []Option, multi bool) map[string]any {
	tag := "select_static"
	if multi {
		tag = "multi_select_static"
	}
	opts := make([]any, len(options))
	for i, o := range options {
		opts[i] = map[string]any{"text": text(o.Label), "value": o.Value}
	}
	return map[string]any{"tag": tag, "name": name, "placeholder": text(placeholder), "options": opts, "width": "fill"}
}

// MarkdownBlock is a Markdown block inside a form.
func MarkdownBlock(s string) map[string]any { return map[string]any{"tag": "markdown", "content": s} }

// JSON is the card as message content. HTML characters stay as they are: a card's limit is in bytes.
func (c *Card) JSON() string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(c)
	return strings.TrimSuffix(b.String(), "\n")
}

// Raw is the card as a callback's new card (CardResponse.Card).
func (c *Card) Raw() *CardPayload { return &CardPayload{Type: "raw", Data: c} }

// --- Markdown ---

var (
	fenceRe  = lazyre.New("(?s)```.*?(?:```|$)|~~~.*?(?:~~~|$)")
	inlineRe = lazyre.New("`[^`\n]+`")
	// A list item's line: its indentation, its marker ("- ", "* ", "+ ", "1. ", "1) ") and what follows it.
	listItemRe = lazyre.New(`^([ \t]*)(?:[-*+]|\d{1,9}[.)])[ \t]+\S`)
)

// CardMarkdown turns GitHub Markdown (a model's answer) into what a card shows as written: outside code, "<" is
// escaped, since cards read <at>, <font>, <link>… as their own tags (and drop what they do not know); an autolink
// (<https://…>, <mailto:…>) becomes a card link first; and a list is nested the way cards nest it (listIndent). The
// assistant's persona-card charts are written as numbers first (PersonaCards).
func CardMarkdown(s string) string {
	s = listIndent(PersonaCards(s))
	var b strings.Builder
	last := 0
	for _, loc := range fenceRe.FindAllStringIndex(s, -1) {
		b.WriteString(cardProse(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(cardProse(s[last:]))
	return b.String()
}

// listIndent rewrites a list's indentation for cards: one nesting level is four leading spaces there (Lark's Markdown
// component says a list is nested "4 spaces per level"), while CommonMark — and so a model writing an answer — indents
// a sub-item by its parent marker's width: two spaces under "- ", three under "1. ". Passed through unchanged those
// sub-items are siblings of their parent to Lark, so the answer's hierarchy is lost. Each item's observed indentation
// is mapped to a level (an item deeper than the one above it, by any amount, is one level deeper) and written out as
// four spaces per level; a paragraph at the left margin ends the list. Fenced code (the levels carry across it, so a
// list continues around a code block), text indented four or more spaces at the left margin (an indented code block)
// and a list item's indented continuation lines are left as they are; a tab counts as four columns. A list inside a
// blockquote is left as written.
func listIndent(s string) string {
	lines := strings.Split(s, "\n")
	var levels []int // the indent width of each open nesting level, outermost first
	fence := ""
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if fence != "" { // inside code: as written
			if strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if f := fenceMarker(t); f != "" {
			fence = f
			continue
		}
		m := listItemRe.FindStringSubmatchIndex(line)
		if m == nil {
			if t != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				levels = levels[:0] // a paragraph at the left margin ends the list
			}
			continue
		}
		indent, rest := line[:m[3]], line[m[3]:]
		w := 0
		for _, c := range indent {
			if c == '\t' {
				w += 4
			} else {
				w++
			}
		}
		if len(levels) == 0 && w >= 4 {
			continue // four or more spaces at the left margin is an indented code block, not a list item
		}
		for len(levels) > 0 && w < levels[len(levels)-1] {
			levels = levels[:len(levels)-1]
		}
		if len(levels) == 0 || w > levels[len(levels)-1] {
			levels = append(levels, w)
		}
		lines[i] = strings.Repeat("    ", len(levels)-1) + rest
	}
	return strings.Join(lines, "\n")
}

func cardProse(s string) string {
	var b strings.Builder
	last := 0
	for _, loc := range inlineRe.FindAllStringIndex(s, -1) {
		b.WriteString(proseText(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(proseText(s[last:]))
	return b.String()
}

var (
	// A CommonMark autolink a card can open: http(s) or mailto, the scheme in any case.
	autolinkRe = lazyre.New(`^<((?i:https?://|mailto:)[^\s<>]+)>`)
	// Percent-encodes what would end a card link's address early. "[" and "]" stay in an http(s) host (IPv6).
	hostEncoder = strings.NewReplacer("(", "%28", ")", "%29")
	pathEncoder = strings.NewReplacer("(", "%28", ")", "%29", "[", "%5B", "]", "%5D")
	// What in a link's text would end it or be read as italics / bold, as entities (the ones EscapeMarkdown uses).
	labelEscaper = strings.NewReplacer("[", "&#91;", "]", "&#93;", "_", "&#95;", "*", "&#42;")
)

// proseText is prose outside code. Cards have no autolink form and read "<" as the start of their own tags, so an
// autolink becomes [address](address) and every other "<" is escaped. A backslash-escaped "<" is not an autolink.
func proseText(s string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i:]
		m := autolinkRe.FindStringSubmatch(s)
		if m == nil || escapedAt(b.String()) {
			b.WriteString("&#60;")
			s = s[1:]
			continue
		}
		if strings.HasSuffix(b.String(), "](") { // [text](<address>): only the address goes in
			b.WriteString(linkAddress(m[1]))
		} else {
			b.WriteString("[" + labelEscaper.Replace(m[1]) + "](" + linkAddress(m[1]) + ")")
		}
		s = s[len(m[0]):]
	}
}

// escapedAt reports whether before ends in an odd number of backslashes, which escape the character after it.
func escapedAt(before string) bool {
	n := len(before) - len(strings.TrimRight(before, `\`))
	return n%2 == 1
}

// linkAddress is u as a card link's address, the same address with what would end it early percent-encoded.
func linkAddress(u string) string {
	i := strings.Index(u, "://")
	if i < 0 { // mailto: has no host
		return pathEncoder.Replace(u)
	}
	host := len(u)
	if j := strings.IndexAny(u[i+3:], "/?#"); j >= 0 {
		host = i + 3 + j
	}
	return hostEncoder.Replace(u[:host]) + pathEncoder.Replace(u[host:])
}

var mdEscaper = strings.NewReplacer("\\", "&#92;", "*", "&#42;", "_", "&#95;", "`", "&#96;", "[", "&#91;", "]", "&#93;",
	"~", "&#126;", "<", "&#60;", ">", "&#62;", "#", "&#35;", "|", "&#124;")

// EscapeMarkdown makes text show as it is in a card's Markdown (names, titles, what a user typed).
func EscapeMarkdown(s string) string { return mdEscaper.Replace(s) }

// Code puts text in an inline code span (backticks in it become quotes: a span cannot hold them).
func Code(s string) string {
	s = strings.ReplaceAll(oneLine(s), "`", "'")
	if s == "" {
		return ""
	}
	return "`" + s + "`"
}

// Split cuts long Markdown into pieces of at most n runes (channel.Split).
func Split(s string, n int) []string { return channel.Split(s, n) }

// PostMarkdown is a rich-text message whose one paragraph is Markdown: what is sent when a card is refused.
func PostMarkdown(md string) string {
	b, _ := json.Marshal(map[string]any{"zh_cn": map[string]any{"content": [][]map[string]string{{{"tag": "md", "text": md}}}}})
	return string(b)
}

// TextContent is a plain text message.
func TextContent(s string) string {
	b, _ := json.Marshal(map[string]string{"text": s})
	return string(b)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
