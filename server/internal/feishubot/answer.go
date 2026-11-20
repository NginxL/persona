package feishubot

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// A model's final answer laid out as cards by plain rules (no model call): the first "# " heading becomes the
// header, a status word in it (or on a short line of its own) a tag on the header, "## " headings and lines like
// Chinese-numeral lines ("1." with an ideographic comma) start sections with a divider between them, pipe tables become table elements, "Note:" lines small grey
// text, and the answer's web links a row of open_url buttons on the last card. What does not fit one card goes on to
// the next; past maxParts cards the rest is left out (Answer.Clipped) and the app has the whole answer.

// Answer is an answer laid out: the cards to send, in order.
type Answer struct {
	Parts   []AnswerPart
	Clipped bool // more than maxParts cards: the rest was left out
}

// AnswerPart is one card of an answer.
type AnswerPart struct {
	header *cardHeader
	els    []any
	Text   string // the part as card Markdown: what is sent when Feishu refuses the card
}

// Card starts the part's card (its header on the first); summary is what the chat list shows.
func (p AnswerPart) Card(summary string) *Card {
	c := NewCard("", "", summary)
	if p.header != nil {
		h := *p.header
		c.Header = &h
	}
	return c
}

// Fill adds the part's elements to c.
func (p AnswerPart) Fill(c *Card) *Card {
	c.Body.Elements = append(c.Body.Elements, p.els...)
	return c
}

// Limits of what one answer shows.
const (
	answerMaxElements = 80    // per card (a card takes 200 components, a table's cells count)
	answerMaxTables   = 5     // per card (what Feishu takes)
	answerMaxRows     = 100   // per table; the table pages by 10
	answerTableRows   = 30    // rows per table element: a longer table goes on as more tables
	answerTableBytes  = 9000  // JSON bytes per table element, likewise
	answerMaxBytes    = 24000 // JSON bytes of a card's answer (Feishu takes about 30 KB)
	answerMaxCell     = 300   // runes per table cell
	answerMaxTitle    = 50    // runes of the header's title
	answerMaxLinks    = 6     // links (linkRow shows the first as buttons, the rest in a "more" menu)
	answerMaxLabel    = 20    // runes of a link button's label

	answerMaxComponents = 180 // per card; margin for the panel, notes and buttons added after packing
)

type answerBlock struct {
	el    map[string]any
	src   string // fallback Markdown for the block (tables are bullet lines, never pipe-table source)
	size  int    // runes it takes
	bytes int    // JSON bytes it takes
	comps int    // Feishu card components it takes (a table costs its cells plus its header)
	table bool
	hr    bool
	sect  bool
}

// Status words: tagged on the header, and they pick its color.
type answerStatus struct {
	words    []string
	tag      string
	color    string // the tag's color
	template string // the header's color
}

var answerStatuses = []answerStatus{ // in order: the longer "no changes" wording contains the "changed" one
	{[]string{"请验收", "待验收"}, "请验收", "orange", Orange},
	{[]string{"无变动", "没有变动", "没变动", "无改动", "没有改动"}, "无变动", "neutral", Grey},
	{[]string{"有变动", "有改动"}, "有变动", "blue", Blue},
}

var (
	answerSectionRe = lazyre.New(`^[一二三四五六七八九十]{1,3}[、.．]\s*\S`)
	answerNoteRe    = lazyre.New(`^(?:>\s*)?(?:备注|注|附注|PS|P\.S\.)\s*[:：]`)
	answerStatusRe  = lazyre.New(`^(?:当前状态|状态|结论)\s*[:：]\s*`)
	// answerStatusLabelRe: what is left of a heading that was a status after its label ("Status: please review").
	answerStatusLabelRe = lazyre.New(`^(?:当前状态|状态|结论)\s*[:：]?$`)
	// answerEmptyBracketsRe: brackets a status was taken out of.
	answerEmptyBracketsRe = lazyre.New(`[【（(\[]\s*[】）)\]]`)
	answerLinkRe          = lazyre.New(`\[([^\]\n]+)\]\((https?://(?:[^\s()]|\([^\s()]*\))+)\)`)
	answerURLRe           = lazyre.New(`https?://[^\s<>\[\]（）【】，。；：、！？「」《》"'` + "`" + `]+`)
	answerListRe          = lazyre.New(`^(?:[-*+]|\d+[.)])\s`)
	answerCellRe          = lazyre.New(`^:?-{2,}:?$`)
)

// RenderAnswer lays out answer (GitHub Markdown) as cards of about budget runes each, at most maxParts of them.
// There is always one part, empty for an empty answer.
func RenderAnswer(answer string, budget, maxParts int) Answer {
	return RenderAnswerWith(answer, budget, maxParts, nil)
}

// RenderAnswerWith is RenderAnswer with label naming the button of a bare address in the answer (what it opens: "Open
// the conversation"); nil, or "" from it, names it by its host. A link written with words keeps them.
func RenderAnswerWith(answer string, budget, maxParts int, label func(*url.URL) string) Answer {
	lines := strings.Split(strings.ReplaceAll(answer, "\r\n", "\n"), "\n")
	var (
		title, status string
		blocks        []answerBlock
		prose         []string
		links         []linkItem
		seenURL       = map[string]bool{}
		started       bool
		fence         string
	)
	flush := func() {
		text := strings.Trim(strings.Join(prose, "\n"), "\n")
		prose = nil
		if strings.TrimSpace(text) == "" {
			return
		}
		blocks = append(blocks, fitBlocks(text, budget, answerMarkdown)...)
	}
	addLinks := func(line string) {
		line = inlineRe.ReplaceAllString(line, "")
		for _, m := range answerLinkRe.FindAllStringSubmatch(line, -1) {
			links = addLink(links, seenURL, m[2], m[1], label)
		}
		for _, u := range answerURLRe.FindAllString(answerLinkRe.ReplaceAllString(line, ""), -1) {
			links = addLink(links, seenURL, trimURL(u), "", label)
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		t := strings.TrimSpace(line)
		if fence != "" {
			prose = append(prose, line)
			if strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if f := fenceMarker(t); f != "" {
			fence = f
			prose = append(prose, line)
			started = true
			continue
		}
		if t == "" {
			prose = append(prose, line)
			continue
		}
		addLinks(t)
		if !started && strings.HasPrefix(t, "# ") {
			started = true
			title = plainHeading(t)
			continue
		}
		started = true
		if status == "" {
			status = lineStatus(t)
		}
		switch {
		case strings.HasPrefix(t, "## ") || isSectionLine(t):
			flush()
			blocks = append(blocks, answerSection(plainHeading(t)))
		case t == "---" || t == "***" || t == "___":
			flush()
			blocks = append(blocks, answerBlock{el: map[string]any{"tag": "hr"}, src: "---", comps: 1, hr: true})
		case answerNoteRe.MatchString(t):
			flush()
			blocks = append(blocks, fitBlocks(t, budget, answerNote)...)
		case isTableStart(lines, i):
			flush()
			j := i + 2
			for j < len(lines) && isTableRow(lines[j]) {
				addLinks(lines[j])
				j++
			}
			blocks = append(blocks, answerTables(lines[i:j], budget)...)
			i = j - 1
		default:
			prose = append(prose, line)
		}
	}
	flush()
	if s := titleStatus(title); s != "" {
		status = s
	}

	var header *cardHeader
	for _, st := range answerStatuses {
		if st.tag != status {
			continue
		}
		if title == "" { // no title: the status is the header
			header = &cardHeader{Title: text(st.tag), Template: st.template}
		} else {
			header = &cardHeader{Title: text(clipRunes(withoutStatus(title, st), answerMaxTitle)), Template: st.template,
				TextTagList: []any{map[string]any{"tag": "text_tag", "text": text(st.tag), "color": st.color}}}
		}
	}
	if header == nil && title != "" {
		header = &cardHeader{Title: text(clipRunes(title, answerMaxTitle)), Template: Blue}
	}

	parts := packAnswer(blocks, budget)
	out := Answer{}
	if maxParts > 0 && len(parts) > maxParts {
		parts, out.Clipped = parts[:maxParts], true
	}
	if len(parts) == 0 {
		parts = []AnswerPart{{}}
	}
	parts[0].header = header
	if title != "" {
		parts[0].Text = strings.TrimSpace("**" + CardMarkdown(title) + "**\n\n" + parts[0].Text)
	}
	if len(links) > 0 {
		last := &parts[len(parts)-1]
		last.els = append(last.els, linkRow(links))
	}
	out.Parts = parts
	return out
}

// packAnswer puts the blocks on cards: a new card when the next block would take it past budget runes or the
// element and table limits; a divider is drawn before each section but the first, and never at a card's edge.
func packAnswer(blocks []answerBlock, budget int) []AnswerPart {
	var parts []AnswerPart
	var cur []answerBlock
	size, bytes, tables, comps := 0, 0, 0, 0
	// push ends the card. Unless it is the last, the last section on it (its heading and what follows it so far) goes
	// on to the next card instead, so a heading stays with what it heads; one that starts the card stays.
	push := func(last bool) {
		var carry []answerBlock
		if !last {
			for k := len(cur) - 1; k > 0; k-- {
				if cur[k].sect {
					carry, cur = append([]answerBlock{}, cur[k:]...), cur[:k]
					break
				}
			}
		}
		for len(cur) > 0 && cur[len(cur)-1].hr {
			cur = cur[:len(cur)-1]
		}
		defer func() {
			for _, b := range carry {
				cur = append(cur, b)
				size, bytes, comps = size+b.size, bytes+b.bytes, comps+b.comps
				if b.table {
					tables++
				}
			}
		}()
		if len(cur) == 0 {
			return
		}
		p := AnswerPart{}
		var src []string
		for _, b := range cur {
			p.els = append(p.els, b.el)
			if b.src != "" {
				src = append(src, b.src)
			}
		}
		p.Text = CardMarkdown(strings.Join(src, "\n\n"))
		parts = append(parts, p)
		cur, size, bytes, tables, comps = nil, 0, 0, 0, 0
	}
	for _, b := range blocks {
		need := 1 // elements b adds: a section after something takes a divider too
		if b.sect && len(cur) > 0 && !cur[len(cur)-1].hr {
			need = 2
		}
		over := func() bool {
			return len(cur) > 0 && (size+b.size > budget || bytes+b.bytes > answerMaxBytes || len(cur)+need > answerMaxElements ||
				comps+b.comps+need-1 > answerMaxComponents || b.table && tables >= answerMaxTables)
		}
		if over() {
			push(false)
		}
		if over() { // the carried section fills a card on its own: ship it heading and all
			push(true)
		}
		if b.hr && (len(cur) == 0 || cur[len(cur)-1].hr) {
			continue
		}
		if b.sect && len(cur) > 0 && !cur[len(cur)-1].hr {
			cur = append(cur, answerBlock{el: map[string]any{"tag": "hr"}, src: "---", comps: 1, hr: true})
			comps++
		}
		cur = append(cur, b)
		size += b.size
		bytes += b.bytes
		comps += b.comps
		if b.table {
			tables++
		}
	}
	push(true)
	return parts
}

// fitBlocks is text cut into blocks made by mk, each at most budget runes and answerMaxBytes JSON bytes: packAnswer
// puts a block on an empty card whatever its size, and text heavy with what JSON or a card escapes ("<" is five bytes,
// a control character six) takes far more bytes than runes, so a piece over the bytes is cut again, smaller.
func fitBlocks(text string, budget int, mk func(string) answerBlock) []answerBlock {
	var out []answerBlock
	for _, piece := range Split(text, budget) {
		b := mk(piece)
		n := utf8.RuneCountInString(piece)
		if b.bytes <= answerMaxBytes {
			out = append(out, b)
			continue
		}
		smaller := max(n*answerMaxBytes/b.bytes*9/10, 64) // a margin: escapes are not spread evenly
		if smaller >= n {
			out = append(out, b)
			continue
		}
		out = append(out, fitBlocks(piece, smaller, mk)...)
	}
	return out
}

func answerMarkdown(md string) answerBlock {
	return sized(answerBlock{el: map[string]any{"tag": "markdown", "content": CardMarkdown(md)}, src: md, size: utf8.RuneCountInString(md)})
}

func answerSection(title string) answerBlock {
	md := "**" + CardMarkdown(title) + "**"
	return sized(answerBlock{el: map[string]any{"tag": "markdown", "content": md, "text_size": "heading"}, src: "**" + title + "**",
		size: utf8.RuneCountInString(title), sect: true})
}

func answerNote(line string) answerBlock {
	md := "<font color='grey'>" + CardMarkdown(strings.TrimSpace(strings.TrimPrefix(line, ">"))) + "</font>"
	return sized(answerBlock{el: map[string]any{"tag": "markdown", "content": md, "text_size": "notation"}, src: line, size: utf8.RuneCountInString(line)})
}

// answerTables is a pipe table's lines as table elements: up to answerTableRows rows each, fewer for wide columns so
// the cells fit a card's component budget; a table too wide for one row is rune-split bullet Markdown instead.
func answerTables(lines []string, budget int) []answerBlock {
	head := tableCells(lines[0])
	aligns := tableCells(lines[1])
	cols := make([]any, len(head))
	ncol := len(head)
	for i, h := range head {
		col := map[string]any{"name": "c" + strconv.Itoa(i), "display_name": orSpace(stripBold(h)), "data_type": "lark_md", "width": "auto"}
		if i < len(aligns) {
			a := strings.TrimSpace(aligns[i])
			switch {
			case strings.HasPrefix(a, ":") && strings.HasSuffix(a, ":"):
				col["horizontal_align"] = "center"
			case strings.HasSuffix(a, ":"):
				col["horizontal_align"] = "right"
			}
		}
		cols[i] = col
	}
	names := make([]string, ncol)
	for i, h := range head {
		names[i] = clipRunes(strings.ReplaceAll(strings.TrimSpace(stripBold(h)), "|", "/"), answerMaxCell)
	}
	wide := 1+2*ncol > answerMaxComponents // no table element can hold even one row
	rowCap := answerTableRowCap(ncol)
	var out []answerBlock
	var rows []any
	var src []string
	size, bytes := 0, 0
	flush := func() {
		if len(rows) == 0 {
			return
		}
		if wide {
			for _, t := range Split(tableFallback(names, src), budget) {
				out = append(out, answerMarkdown(t))
			}
			rows, src, size, bytes = nil, nil, 0, 0
			return
		}
		el := map[string]any{"tag": "table", "page_size": min(10, len(rows)), "row_height": "low",
			"header_style": map[string]any{"text_align": "left", "text_size": "normal", "background_style": "grey", "bold": true, "lines": 1},
			"columns":      cols, "rows": rows}
		out = append(out, sized(answerBlock{el: el, src: tableFallback(names, src), size: size,
			comps: 1 + (len(rows)+1)*ncol, table: true}))
		rows, src, size, bytes = nil, nil, 0, 0
	}
	body := lines[2:]
	for n, l := range body {
		if n == answerMaxRows {
			more := len(body) - n
			if len(rows) >= rowCap { // the indicator row counts too: ship a full batch first
				flush()
			}
			rows = append(rows, map[string]any{"c0": "… +" + strconv.Itoa(more)})
			src = append(src, "| … +"+strconv.Itoa(more)+" |") // re-parsed by tableFallback, never sent with pipes
			break
		}
		cells := tableCells(l)
		if len(cells) > len(head) { // a "|" the row did not escape (inside a code span too) splits its cell, so the row is
			// wider than the header; those words are still the row's, so keep them in the last column rather than drop them
			last := cells[len(head)-1]
			for _, extra := range cells[len(head):] {
				switch {
				case extra == "":
				case last == "":
					last = extra
				default:
					last += " | " + extra
				}
			}
			cells = append(cells[:len(head)-1], last)
		}
		row := map[string]any{}
		rs := 0
		for i := range head {
			v := ""
			if i < len(cells) {
				v = clipRunes(CardMarkdown(cells[i]), answerMaxCell)
			}
			row["c"+strconv.Itoa(i)] = v
			rs += utf8.RuneCountInString(v)
		}
		j, _ := json.Marshal(row)
		if len(rows) > 0 && (len(rows) >= rowCap || bytes+len(j) > answerTableBytes) {
			flush()
		}
		rows = append(rows, row)
		src = append(src, l)
		size += rs
		bytes += len(j)
	}
	if len(rows) == 0 { // a table with no rows still shows its header
		if wide {
			if text := tableFallback(names, nil); text != "" {
				for _, t := range Split(text, budget) {
					out = append(out, answerMarkdown(t))
				}
			}
			return out
		}
		rows = []any{}
		el := map[string]any{"tag": "table", "page_size": 1, "row_height": "low",
			"header_style": map[string]any{"text_align": "left", "text_size": "normal", "background_style": "grey", "bold": true, "lines": 1},
			"columns":      cols, "rows": rows}
		if len(out) == 0 {
			return []answerBlock{sized(answerBlock{el: el, src: tableFallback(names, nil), comps: 1 + ncol, table: true})}
		}
		return out
	}
	flush()
	return out
}

// answerTableRowCap is the most rows one element may hold: wrapper, header row and cells must fit a card alone.
func answerTableRowCap(ncol int) int {
	return min(answerTableRows, max(1, (answerMaxComponents-1)/ncol-1))
}

// tableFallback renders a table as "- column: value, …" bullet lines, never pipe source (the refused-card fallback is rich text, no GFM tables).
func tableFallback(names, bodyLines []string) string {
	var lines []string
	if len(bodyLines) == 0 {
		var shown []string
		for _, n := range names {
			if n != "" {
				shown = append(shown, n)
			}
		}
		if len(shown) > 0 {
			lines = append(lines, "- "+strings.Join(shown, "，"))
		}
	}
	for _, l := range bodyLines {
		var parts []string
		for i, c := range tableCells(l) {
			v := strings.ReplaceAll(clipRunes(c, answerMaxCell), "|", "/")
			if v == "" {
				continue
			}
			if i < len(names) && names[i] != "" {
				v = names[i] + "：" + v
			}
			parts = append(parts, v)
		}
		if len(parts) > 0 {
			lines = append(lines, "- "+strings.Join(parts, "，"))
		}
	}
	return strings.Join(lines, "\n")
}

func isTableStart(lines []string, i int) bool {
	if i+1 >= len(lines) || !strings.Contains(lines[i], "|") {
		return false
	}
	head, sep := tableCells(lines[i]), tableCells(lines[i+1])
	if len(head) < 2 || len(sep) != len(head) {
		return false
	}
	for _, c := range sep {
		if !answerCellRe.MatchString(strings.TrimSpace(c)) {
			return false
		}
	}
	return true
}

func isTableRow(line string) bool {
	t := strings.TrimSpace(line)
	return t != "" && strings.Contains(t, "|") && fenceMarker(t) == ""
}

// tableCells splits a pipe table's line into its cells ("\|" is a pipe inside one).
func tableCells(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	if strings.HasSuffix(t, "|") && !strings.HasSuffix(t, `\|`) {
		t = t[:len(t)-1]
	}
	var cells []string
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		switch {
		case t[i] == '\\' && i+1 < len(t) && t[i+1] == '|':
			b.WriteByte('|')
			i++
		case t[i] == '|':
			cells = append(cells, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(t[i])
		}
	}
	return append(cells, strings.TrimSpace(b.String()))
}

// fenceMarker is the backticks or tildes opening a code fence on line t, if it opens one.
func fenceMarker(t string) string {
	for _, f := range []string{"```", "~~~"} {
		if strings.HasPrefix(t, f) {
			n := len(t) - len(strings.TrimLeft(t, f[:1]))
			return t[:n]
		}
	}
	return ""
}

// isSectionLine: a line like "1. Background" or "**2. Conclusion**" in Chinese numerals (short and without sentence punctuation: a heading).
func isSectionLine(t string) bool {
	s := stripBold(t)
	if !answerSectionRe.MatchString(s) || utf8.RuneCountInString(s) > 24 {
		return false
	}
	return !strings.ContainsAny(s, "。！？；，!?;") // "1. Handled twice this week, both resolved." (full-width punctuation) is a sentence
}

// plainHeading is a heading line's text: no "#", no bold.
func plainHeading(t string) string {
	return stripBold(strings.TrimSpace(strings.TrimLeft(t, "#")))
}

func stripBold(s string) string {
	s = strings.TrimSpace(s)
	for _, m := range []string{"**", "__"} {
		if strings.HasPrefix(s, m) && strings.HasSuffix(s, m) && len(s) > 2*len(m) {
			s = strings.TrimSpace(s[len(m) : len(s)-len(m)])
		}
	}
	return s
}

// titleStatus is the status a title names anywhere in it.
func titleStatus(title string) string {
	for _, st := range answerStatuses {
		for _, w := range st.words {
			if strings.Contains(title, w) {
				return st.tag
			}
		}
	}
	return ""
}

// lineStatus is the status a line of the body starts with ("Status: no changes", "**please review**", "[changed]…"): only at the
// start, so "the files with no changes this time" in a sentence does not count.
func lineStatus(t string) string {
	if answerListRe.MatchString(t) { // "- files with no changes were skipped" is an item, not the answer's status
		return ""
	}
	s := strings.Trim(t, " >*_#`【】[]()（）-•·")
	s = answerStatusRe.ReplaceAllString(s, "")
	s = strings.TrimLeft(s, " *_【[（(")
	for _, st := range answerStatuses {
		for _, w := range st.words {
			if strings.HasPrefix(s, w) {
				return st.tag
			}
		}
	}
	return ""
}

// addLink adds a link opening u (http(s) only, each address once, at most answerMaxLinks); one without words of its
// own is named by name, else by its host.
func addLink(links []linkItem, seen map[string]bool, u, label string, name func(*url.URL) string) []linkItem {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" || seen[u] || len(links) >= answerMaxLinks {
		return links
	}
	seen[u] = true
	label = strings.TrimSpace(strings.Trim(stripBold(label), "`"))
	if label == "" || strings.HasPrefix(label, "http") {
		label = ""
		if name != nil {
			label = strings.TrimSpace(name(p))
		}
		if label == "" {
			label = p.Host
		}
	}
	return append(links, linkItem{clipRunes(label, answerMaxLabel), u, u})
}

func orSpace(s string) string {
	if s == "" {
		return " "
	}
	return s
}

// sized notes the JSON bytes b's element takes.
func sized(b answerBlock) answerBlock {
	// Measured as Card.JSON sends it (no HTML escaping: "&#60;" is 5 bytes, not 10).
	var j bytes.Buffer
	enc := json.NewEncoder(&j)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(b.el)
	b.bytes = j.Len() - 1 // Encode's newline
	if b.comps == 0 {
		b.comps = 1
	}
	return b
}

// trimURL drops what follows a bare address in prose: closing punctuation, and a ")" it does not open.
func trimURL(u string) string {
	for {
		t := strings.TrimRight(u, ".,;:!?'\"")
		if strings.HasSuffix(t, ")") && strings.Count(t, "(") < strings.Count(t, ")") {
			t = t[:len(t)-1]
		}
		if t == u {
			return u
		}
		u = t
	}
}

// withoutStatus is title without the status word the tag beside it already says ("Login redesign (please review)" → "Login redesign"),
// or title as it is when that would leave nothing.
func withoutStatus(title string, st answerStatus) string {
	for _, w := range st.words {
		if i := strings.Index(title, w); i >= 0 {
			t := strings.TrimSpace(title[:i]) + " " + strings.TrimSpace(title[i+len(w):])
			t = answerEmptyBracketsRe.ReplaceAllString(t, "") // "[please review]" in full-width brackets leaves the empty brackets
			t = strings.Trim(t, " -—·|:：，,")
			if answerStatusLabelRe.MatchString(t) {
				t = "" // "Status: please review" leaves only its label
			}
			if t = strings.TrimSpace(t); t != "" {
				return t
			}
			return title
		}
	}
	return title
}

// Retitle puts icon+title (in template's color, cut to 60 runes) over a card laid out by RenderAnswer, keeping what its
// own header said: a status (please review / no changes / changed) stays as its tag and color, and its own title, when it is
// neither title nor only the status, becomes the card's first line instead of being lost.
func (c *Card) Retitle(icon, title, template string) *Card {
	title = strings.TrimSpace(title)
	old := c.Header
	h := &cardHeader{Title: text(icon + clipRunes(oneLine(title), 60)), Template: template}
	c.Header = h
	if old == nil {
		return c
	}
	own := strings.TrimSpace(old.Title.Content)
	if st, only := statusOnly(own); only {
		// the heading was the status alone (with or without its tag): it becomes the tag, nothing else to keep
		h.Template, h.TextTagList = st.template, []any{map[string]any{"tag": "text_tag", "text": text(st.tag), "color": st.color}}
		return c
	}
	if len(old.TextTagList) > 0 {
		h.Template, h.TextTagList = old.Template, old.TextTagList
	}
	if own != "" && !sameTitle(own, title) {
		line := map[string]any{"tag": "markdown", "content": "**" + EscapeMarkdown(own) + "**"}
		c.Body.Elements = append([]any{line}, c.Body.Elements...)
	}
	return c
}

// statusOnly: a heading that says nothing but one of the statuses ("please review", "Status: no changes"…).
func statusOnly(heading string) (answerStatus, bool) {
	for _, st := range answerStatuses {
		for _, w := range st.words {
			if i := strings.Index(heading, w); i >= 0 {
				rest := answerStatusRe.ReplaceAllString(heading[:i]+heading[i+len(w):], "")
				if strings.Trim(rest, " -—·|:：，,()（）【】*") == "" {
					return st, true
				}
			}
		}
	}
	return answerStatus{}, false
}

// sameTitle: own (a header's title, maybe cut to answerMaxTitle with "…") is title.
func sameTitle(own, title string) bool {
	if own == title {
		return true
	}
	cut, ok := strings.CutSuffix(own, "…")
	return ok && len([]rune(own)) >= answerMaxTitle && strings.HasPrefix(title, cut)
}
