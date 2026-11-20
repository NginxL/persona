package feishubot

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// decoded is a rendered card as Feishu would get it.
type decoded map[string]any

// renderCards renders answer and decodes every part's card JSON.
func renderCards(t *testing.T, answer string, budget, maxParts int) (Answer, []decoded) {
	t.Helper()
	a := RenderAnswer(answer, budget, maxParts)
	var out []decoded
	for i, p := range a.Parts {
		c := p.Fill(p.Card("s"))
		var m decoded
		if err := json.Unmarshal([]byte(c.JSON()), &m); err != nil {
			t.Fatalf("part %d: bad JSON %v\n%s", i, err, c.JSON())
		}
		out = append(out, m)
	}
	return a, out
}

func (m decoded) elements() []map[string]any {
	body, _ := m["body"].(map[string]any)
	raw, _ := body["elements"].([]any)
	els := make([]map[string]any, len(raw))
	for i, e := range raw {
		els[i], _ = e.(map[string]any)
	}
	return els
}

func (m decoded) header() map[string]any {
	h, _ := m["header"].(map[string]any)
	return h
}

func (m decoded) title() string {
	h := m.header()
	if h == nil {
		return ""
	}
	tt, _ := h["title"].(map[string]any)
	s, _ := tt["content"].(string)
	return s
}

// tags is the header's text_tag_list as "text/color".
func (m decoded) tags() []string {
	h := m.header()
	if h == nil {
		return nil
	}
	list, _ := h["text_tag_list"].([]any)
	var out []string
	for _, x := range list {
		tag := x.(map[string]any)
		txt := tag["text"].(map[string]any)["content"].(string)
		out = append(out, txt+"/"+tag["color"].(string))
	}
	return out
}

func (m decoded) template() string {
	h := m.header()
	if h == nil {
		return ""
	}
	s, _ := h["template"].(string)
	return s
}

func tagsOf(els []map[string]any) []string {
	out := make([]string, len(els))
	for i, e := range els {
		out[i], _ = e["tag"].(string)
	}
	return out
}

func byTag(els []map[string]any, tag string) []map[string]any {
	var out []map[string]any
	for _, e := range els {
		if e["tag"] == tag {
			out = append(out, e)
		}
	}
	return out
}

// buttonURLs is the open_url addresses of the column_set buttons, then of their "more" menu's entries.
func buttonURLs(els []map[string]any) (urls, labels []string) {
	for _, cs := range byTag(els, "column_set") {
		for _, col := range cs["columns"].([]any) {
			for _, b := range col.(map[string]any)["elements"].([]any) {
				btn := b.(map[string]any)
				if btn["tag"] == "overflow" {
					for _, o := range btn["options"].([]any) {
						opt := o.(map[string]any)
						labels = append(labels, opt["text"].(map[string]any)["content"].(string))
						urls = append(urls, opt["multi_url"].(map[string]any)["url"].(string))
					}
					continue
				}
				labels = append(labels, btn["text"].(map[string]any)["content"].(string))
				beh := btn["behaviors"].([]any)[0].(map[string]any)
				if beh["type"] != "open_url" {
					panic("not open_url")
				}
				urls = append(urls, beh["default_url"].(string))
			}
		}
	}
	return
}

func allContent(els []map[string]any) string {
	var b strings.Builder
	for _, e := range els {
		if s, ok := e["content"].(string); ok {
			b.WriteString(s + "\n")
		}
	}
	return b.String()
}

func TestAnswerEmpty(t *testing.T) {
	for _, in := range []string{"", "\n\n  \n"} {
		a, cards := renderCards(t, in, 1000, 5)
		if len(a.Parts) != 1 || a.Clipped {
			t.Fatalf("%q: %d parts, clipped %v", in, len(a.Parts), a.Clipped)
		}
		if a.Parts[0].Text != "" || len(cards[0].elements()) != 0 || cards[0].header() != nil {
			t.Fatalf("%q: not empty: text %q, card %v", in, a.Parts[0].Text, cards[0])
		}
	}
}

func TestAnswerTitle(t *testing.T) {
	tests := []struct {
		name, in, title, template string
		tags                      []string
	}{
		{"plain", "# 周报\n正文", "周报", Blue, nil},
		{"after blank lines", "\n\n# 周报\n正文", "周报", Blue, nil},
		{"bold title", "# **周报**\n正文", "周报", Blue, nil},
		{"no title", "正文\n更多", "", "", nil},
		{"not first line", "前言\n# 周报\n正文", "", "", nil},
		{"h2 is not a title", "## 周报\n正文", "", "", nil},
		{"status in title", "# 部署完成，请验收\n正文", "部署完成", Orange, []string{"请验收/orange"}},
		{"待验收", "# 待验收：登录页\n正文", "登录页", Orange, []string{"请验收/orange"}},
		{"没有变动 not 有变动", "# 巡检：没有变动\n正文", "巡检", Grey, []string{"无变动/neutral"}},
		{"有改动", "# 巡检有改动\n正文", "巡检", Blue, []string{"有变动/blue"}},
		{"long title clipped", "# " + strings.Repeat("长", 60) + "\nx", strings.Repeat("长", 49) + "…", Blue, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, cards := renderCards(t, tc.in, 1000, 5)
			c := cards[0]
			if c.title() != tc.title || c.template() != tc.template || fmt.Sprint(c.tags()) != fmt.Sprint(tc.tags) {
				t.Fatalf("title %q template %q tags %v", c.title(), c.template(), c.tags())
			}
			if tc.name == "not first line" && !strings.Contains(allContent(c.elements()), "\n# 周报") {
				t.Fatalf("heading not kept as text: %v", c.elements())
			}
			if tc.title != "" && !strings.HasPrefix(a.Parts[0].Text, "**") {
				t.Fatalf("fallback lacks title: %q", a.Parts[0].Text)
			}
		})
	}
}

func TestAnswerBodyStatus(t *testing.T) {
	tests := []struct {
		name, in, template string
		tags               []string
	}{
		{"状态 line", "# 巡检\n状态：无变动\n细节", Grey, []string{"无变动/neutral"}},
		{"bold line", "# 交付\n**请验收**\n细节", Orange, []string{"请验收/orange"}},
		{"bracket line", "# 巡检\n【有变动】两个文件", Blue, []string{"有变动/blue"}},
		{"结论 line", "# 巡检\n结论: 没有改动", Grey, []string{"无变动/neutral"}},
		{"mid-sentence", "# 巡检\n这次没有变动的文件有三个", Blue, nil},
		{"mid-sentence 请验收", "# 巡检\n做完了，请验收", Blue, nil},
		{"title wins", "# 请验收\n状态：无变动", Orange, []string{"请验收/orange"}},
		{"first body status wins", "# 巡检\n状态：有变动\n状态：无变动", Blue, []string{"有变动/blue"}},
		{"inside fence ignored", "# 巡检\n```\n无变动\n```", Blue, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, cards := renderCards(t, tc.in, 1000, 5)
			c := cards[0]
			if c.template() != tc.template || fmt.Sprint(c.tags()) != fmt.Sprint(tc.tags) {
				t.Fatalf("template %q tags %v", c.template(), c.tags())
			}
		})
	}
}

func TestAnswerSections(t *testing.T) {
	in := "# 报告\n一、背景\n背景文字\n## 二级标题\n正文\n**二、结论**\n结论文字"
	_, cards := renderCards(t, in, 1000, 5)
	els := cards[0].elements()
	want := "markdown markdown hr markdown markdown hr markdown markdown"
	if got := strings.Join(tagsOf(els), " "); got != want {
		t.Fatalf("elements %s, want %s", got, want)
	}
	heads := []string{}
	for _, e := range els {
		if e["text_size"] == "heading" {
			heads = append(heads, e["content"].(string))
		}
	}
	if fmt.Sprint(heads) != fmt.Sprint([]string{"**一、背景**", "**二级标题**", "**二、结论**"}) {
		t.Fatalf("headings %q", heads)
	}
}

func TestAnswerSectionLineTooLong(t *testing.T) {
	line := "一、" + strings.Repeat("这是一句很长的话", 6) // > 40 runes
	_, cards := renderCards(t, line+"\n下一行", 1000, 5)
	for _, e := range cards[0].elements() {
		if e["text_size"] == "heading" || e["tag"] == "hr" {
			t.Fatalf("long 一、 line became a section: %v", cards[0].elements())
		}
	}
}

func TestAnswerNote(t *testing.T) {
	for _, line := range []string{"备注：仅供参考", "注: 看这里", "PS: 顺便", "> 备注：引用里的"} {
		_, cards := renderCards(t, "正文\n"+line, 1000, 5)
		notes := []map[string]any{}
		for _, e := range cards[0].elements() {
			if e["text_size"] == "notation" {
				notes = append(notes, e)
			}
		}
		if len(notes) != 1 || !strings.HasPrefix(notes[0]["content"].(string), "<font color='grey'>") {
			t.Fatalf("%q: notes %v", line, cards[0].elements())
		}
	}
}

func TestAnswerTable(t *testing.T) {
	in := "结果如下\n| 名字 | **数量** | 说明 |\n|:--:|--:|---|\n| a | 1 | x<y |\n| b | 2 |\n"
	_, cards := renderCards(t, in, 1000, 5)
	tables := byTag(cards[0].elements(), "table")
	if len(tables) != 1 {
		t.Fatalf("tables %v", cards[0].elements())
	}
	tb := tables[0]
	cols := tb["columns"].([]any)
	if len(cols) != 3 {
		t.Fatalf("columns %v", cols)
	}
	wantAlign := []any{"center", "right", nil}
	wantName := []string{"名字", "数量", "说明"}
	for i, c := range cols {
		col := c.(map[string]any)
		if col["name"] != fmt.Sprintf("c%d", i) || col["data_type"] != "lark_md" || col["horizontal_align"] != wantAlign[i] || col["display_name"] != wantName[i] {
			t.Fatalf("column %d: %v", i, col)
		}
	}
	rows := tb["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows %v", rows)
	}
	r0, r1 := rows[0].(map[string]any), rows[1].(map[string]any)
	if r0["c0"] != "a" || r0["c2"] != "x&#60;y" || r1["c1"] != "2" || r1["c2"] != "" {
		t.Fatalf("rows %v", rows)
	}
}

// A row with more cells than its header (a "|" the row did not escape, inside a code span too) keeps those cells in
// the header's last column: nothing of the row is dropped from the card.
func TestAnswerTableRowExtraCellsKept(t *testing.T) {
	in := "| 命令 | 说明 |\n|---|---|\n" +
		"| `a || b` | 或 |\n" + // two pipes in a code span: one empty cell between them, then the rest of the row
		"| `x` | 管道 `|` 符号 |\n" +
		"| 甲 | 乙 ||\n" // a trailing empty cell adds nothing
	a, cards := renderCards(t, in, 1000, 5)
	tables := byTag(cards[0].elements(), "table")
	if len(tables) != 1 {
		t.Fatalf("tables %v", cards[0].elements())
	}
	rows := tables[0]["rows"].([]any)
	want := []map[string]string{
		{"c0": "`a", "c1": "b` | 或"},
		{"c0": "`x`", "c1": "管道 ` | ` 符号"},
		{"c0": "甲", "c1": "乙"},
	}
	if len(rows) != len(want) {
		t.Fatalf("%d rows, want %d: %v", len(rows), len(want), rows)
	}
	for i, w := range want {
		row := rows[i].(map[string]any)
		for k, v := range w {
			if row[k] != v {
				t.Fatalf("row %d %v, want %v", i, row, w)
			}
		}
	}
	// The rich-text fallback (what is sent when Lark refuses the card) keeps the same words.
	for _, word := range []string{"b`", "或", "符号"} {
		if !strings.Contains(a.Parts[0].Text, word) {
			t.Fatalf("fallback %q lacks %q", a.Parts[0].Text, word)
		}
	}
}

// A long table goes on as more tables with the same header; past 100 rows one row says how many were left out.
func TestAnswerTableLimits(t *testing.T) {
	var b strings.Builder
	b.WriteString("| k | v |\n|---|---|\n")
	for i := 0; i < 105; i++ {
		fmt.Fprintf(&b, "| %d | v%d |\n", i, i)
	}
	_, cards := renderCards(t, b.String(), 1_000_000, 5)
	var rows []any
	for _, c := range cards {
		for _, tb := range byTag(c.elements(), "table") {
			r := tb["rows"].([]any)
			if len(r) > answerTableRows {
				t.Fatalf("table of %d rows", len(r))
			}
			if n := len(tb["columns"].([]any)); n != 2 {
				t.Fatalf("table of %d columns", n)
			}
			rows = append(rows, r...)
		}
	}
	if len(rows) != 101 {
		t.Fatalf("%d rows", len(rows))
	}
	if got := rows[100].(map[string]any)["c0"]; got != "… +5" {
		t.Fatalf("overflow row %v", got)
	}
}

// Wide CJK cells: each cell is clipped, and no card's JSON gets near Feishu's 30 KB.
func TestAnswerTableBytes(t *testing.T) {
	var b strings.Builder
	b.WriteString("| a | b | c |\n|---|---|---|\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", strings.Repeat("字", 400), strings.Repeat("词", 60), strings.Repeat("句", 40))
	}
	a := RenderAnswer(b.String(), 6000, 50)
	for i, p := range a.Parts {
		c := p.Fill(p.Card("s"))
		if n := len(c.JSON()); n > 28_000 {
			t.Fatalf("card %d is %d bytes", i, n)
		}
		for _, tb := range byTag(decode(t, c).elements(), "table") {
			for _, r := range tb["rows"].([]any) {
				if n := utf8.RuneCountInString(r.(map[string]any)["c0"].(string)); n > answerMaxCell {
					t.Fatalf("cell has %d runes", n)
				}
			}
		}
	}
}

func TestAnswerFences(t *testing.T) {
	in := "前言\n```md\n# 不是标题\n## 也不是\n一、不是章节\n| a | b |\n|---|---|\n| 1 | 2 |\n备注：不是备注\nhttps://in.fence.example/x\n```\n后记"
	_, cards := renderCards(t, in, 1000, 5)
	c := cards[0]
	els := c.elements()
	if c.header() != nil || len(byTag(els, "table")) != 0 || len(byTag(els, "hr")) != 0 || len(byTag(els, "column_set")) != 0 {
		t.Fatalf("fence content was parsed: %v", els)
	}
	for _, e := range els {
		if e["text_size"] != nil {
			t.Fatalf("fence content styled: %v", e)
		}
	}
	if !strings.Contains(allContent(els), "```md\n# 不是标题\n## 也不是\n一、不是章节\n| a | b |\n|---|---|") {
		t.Fatalf("fence not verbatim: %q", allContent(els))
	}
}

// A fence before the first heading: the heading after it is not the title.
func TestAnswerFenceBeforeHeading(t *testing.T) {
	_, cards := renderCards(t, "```\ncode\n```\n# 标题", 1000, 5)
	if cards[0].header() != nil {
		t.Fatalf("header %v", cards[0].header())
	}
}

func TestAnswerTableRightAfterFenceOpens(t *testing.T) {
	in := "```\n| a | b |\n|---|---|\n| 1 | 2 |\n```"
	_, cards := renderCards(t, in, 1000, 5)
	if len(byTag(cards[0].elements(), "table")) != 0 {
		t.Fatalf("table inside a fence: %v", cards[0].elements())
	}
}

func TestAnswerEscapesAngleBrackets(t *testing.T) {
	in := "## a<b\n正文 <at id=1></at> 和 `x<y`\n```\nif a<b {}\n```"
	_, cards := renderCards(t, in, 1000, 5)
	got := allContent(cards[0].elements())
	for _, want := range []string{"**a&#60;b**", "正文 &#60;at id=1>&#60;/at> 和 `x<y`", "if a<b {}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

// "~~~" fences are code fences too: what is in them is shown as written.
func TestAnswerTildeFenceVerbatim(t *testing.T) {
	_, cards := renderCards(t, "~~~\nif a<b {}\n~~~", 1000, 5)
	if got := allContent(cards[0].elements()); !strings.Contains(got, "if a<b {}") {
		t.Fatalf("tilde fence changed: %q", got)
	}
}

func TestAnswerLinks(t *testing.T) {
	in := "# 结果\n见 [设计文档](https://a.example/doc) 和 https://b.example/x。\n" +
		"再看 https://a.example/doc，以及 `https://code.example/no` 和 [坏的](javascript:alert(1))\n" +
		"[c](http://c.example/) [d](https://d.example/) [e](https://e.example/)\n" +
		"| k | v |\n|---|---|\n| [f](https://f.example) | 1 |"
	_, cards := renderCards(t, in, 1000, 5)
	els := cards[0].elements()
	if els[len(els)-1]["tag"] != "column_set" {
		t.Fatalf("buttons not last: %v", tagsOf(els))
	}
	urls, labels := buttonURLs(els)
	want := []string{"https://a.example/doc", "https://b.example/x", "http://c.example/", "https://d.example/", "https://e.example/", "https://f.example"}
	if fmt.Sprint(urls) != fmt.Sprint(want) {
		t.Fatalf("urls %v", urls)
	}
	if labels[0] != "设计文档" || labels[1] != "b.example" {
		t.Fatalf("labels %v", labels)
	}
	// Past three, two show and the rest are in the row's "more" menu.
	if row := els[len(els)-1]["columns"].([]any); len(row) != 3 || row[2].(map[string]any)["elements"].([]any)[0].(map[string]any)["tag"] != "overflow" {
		t.Fatalf("row %v", row)
	}
}

// A bare address's button says what it opens, as the caller names it; a link's own words stay its label.
func TestAnswerLinkLabels(t *testing.T) {
	label := func(u *url.URL) string {
		if u.Host == "persona.example" {
			return "Open the chat"
		}
		return "Open " + u.Host
	}
	a := RenderAnswerWith("见 https://persona.example/persona/c1 和 [设计](https://doc.example/x)，还有 https://b.example/y", 6000, 5, label)
	c := a.Parts[0].Fill(a.Parts[0].Card(""))
	var els []map[string]any
	for _, e := range cardMap(t, c)["body"].(map[string]any)["elements"].([]any) {
		els = append(els, e.(map[string]any))
	}
	_, labels := buttonURLs(els)
	if strings.Join(labels, "|") != "设计|Open the chat|Open b.example" {
		t.Fatalf("labels %v", labels)
	}
}

func TestAnswerJavascriptLinkOnly(t *testing.T) {
	_, cards := renderCards(t, "[x](javascript:alert(1)) and javascript://evil.example", 1000, 5)
	if len(byTag(cards[0].elements(), "column_set")) != 0 {
		t.Fatalf("buttons for javascript: %v", cards[0].elements())
	}
}

// longAnswer is n sections, each with a paragraph of per runes.
func longAnswer(n, per int) string {
	var b strings.Builder
	b.WriteString("# 长答案\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "## 第%d节\n%s\n---\n", i, strings.Repeat("字", per))
	}
	b.WriteString("链接 https://x.example/end\n")
	return b.String()
}

func TestAnswerBudgetSplit(t *testing.T) {
	a, cards := renderCards(t, longAnswer(6, 300), 700, 10)
	if len(cards) < 3 || a.Clipped {
		t.Fatalf("%d parts, clipped %v", len(cards), a.Clipped)
	}
	for i, c := range cards {
		els := c.elements()
		if len(els) == 0 {
			t.Fatalf("part %d empty", i)
		}
		if els[0]["tag"] == "hr" {
			t.Fatalf("part %d starts with hr: %v", i, tagsOf(els))
		}
		content := els
		if i == len(cards)-1 {
			content = els[:len(els)-1] // the buttons
		}
		if content[len(content)-1]["tag"] == "hr" {
			t.Fatalf("part %d ends with hr: %v", i, tagsOf(els))
		}
		if (i == 0) != (c.header() != nil) {
			t.Fatalf("part %d header %v", i, c.header())
		}
		if (i == len(cards)-1) != (len(byTag(els, "column_set")) == 1) {
			t.Fatalf("part %d buttons %v", i, tagsOf(els))
		}
		if strings.HasPrefix(a.Parts[i].Text, "---") || strings.HasSuffix(a.Parts[i].Text, "---") {
			t.Fatalf("part %d fallback %q", i, a.Parts[i].Text)
		}
	}
	if !strings.HasPrefix(a.Parts[0].Text, "**长答案**") || strings.Contains(a.Parts[1].Text, "长答案") {
		t.Fatalf("fallbacks %q / %q", a.Parts[0].Text, a.Parts[1].Text)
	}
}

func TestAnswerClipped(t *testing.T) {
	a, cards := renderCards(t, longAnswer(10, 300), 400, 3)
	if !a.Clipped || len(cards) != 3 {
		t.Fatalf("%d parts, clipped %v", len(cards), a.Clipped)
	}
	if len(byTag(cards[2].elements(), "column_set")) != 1 {
		t.Fatalf("no buttons on the last part kept")
	}
	if a2 := RenderAnswer(longAnswer(2, 10), 400, 3); a2.Clipped {
		t.Fatal("short answer clipped")
	}
}

func TestAnswerTableLimitPerCard(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 7; i++ {
		fmt.Fprintf(&b, "表%d\n| a | b |\n|---|---|\n| 1 | 2 |\n\n", i)
	}
	_, cards := renderCards(t, b.String(), 1_000_000, 5)
	if len(cards) != 2 || len(byTag(cards[0].elements(), "table")) != 5 || len(byTag(cards[1].elements(), "table")) != 2 {
		t.Fatalf("%d parts", len(cards))
	}
}

// No card holds more than answerMaxElements elements, counting the dividers drawn before sections.
func TestAnswerElementLimit(t *testing.T) {
	tests := []struct{ name, in string }{
		{"notes only", strings.Repeat("备注：x\n", 200)},
		{"section after 79 notes", strings.Repeat("备注：x\n", 79) + "## 节\n正文"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, cards := renderCards(t, tc.in, 1_000_000, 10)
			for i, c := range cards {
				if n := len(c.elements()); n > answerMaxElements {
					t.Fatalf("part %d has %d elements", i, n)
				}
			}
		})
	}
}

// decode is a card as Feishu would get it.
func decode(t *testing.T, c *Card) decoded {
	t.Helper()
	var m decoded
	if err := json.Unmarshal([]byte(c.JSON()), &m); err != nil {
		t.Fatalf("bad JSON %v", err)
	}
	return m
}

// Addresses keep their own parentheses and drop the prose's punctuation around them.
func TestAnswerLinkEdges(t *testing.T) {
	in := "见 https://wiki.example.com/Go_(lang)。\n" +
		"[doc](https://a.example.com/x_(y))\n" +
		"（参考 https://c.example.com/z）\n" +
		"地址 https://b.example.com/p：说明\n"
	_, cards := renderCards(t, in, 6000, 5)
	urls, _ := buttonURLs(cards[0].elements())
	want := []string{"https://wiki.example.com/Go_(lang)", "https://a.example.com/x_(y)", "https://c.example.com/z", "https://b.example.com/p"}
	if strings.Join(urls, " ") != strings.Join(want, " ") {
		t.Fatalf("urls %q", urls)
	}
}

// A status word on a list item is about that item, not the answer.
func TestAnswerListItemNoStatus(t *testing.T) {
	for _, in := range []string{"# 结果\n- 无变动的文件已跳过\n", "# 结果\n1. 请验收前先看日志\n"} {
		_, cards := renderCards(t, in, 6000, 5)
		if tags := cards[0].tags(); len(tags) != 0 {
			t.Fatalf("%q: tags %v", in, tags)
		}
	}
}

// Without a title the status alone is the header; a title that is only the status stays as it is.
func TestAnswerStatusHeader(t *testing.T) {
	_, cards := renderCards(t, "状态：无变动\n细节", 6000, 5)
	if c := cards[0]; c.title() != "无变动" || c.template() != Grey || len(c.tags()) != 0 {
		t.Fatalf("title %q template %q tags %v", c.title(), c.template(), c.tags())
	}
	_, cards = renderCards(t, "# 请验收\n细节", 6000, 5)
	if c := cards[0]; c.title() != "请验收" || c.template() != Orange {
		t.Fatalf("title %q template %q", c.title(), c.template())
	}
}

// A numbered line that is a sentence stays text.
func TestAnswerSentenceNotSection(t *testing.T) {
	for _, in := range []string{"一、本周处理了两次，都解决了。", "二、做完了！"} {
		_, cards := renderCards(t, "# 周报\n"+in+"\n正文", 6000, 5)
		for _, e := range cards[0].elements() {
			if e["text_size"] == "heading" {
				t.Fatalf("%q became a section", in)
			}
		}
	}
	_, cards := renderCards(t, "# 周报\n二、下周计划：\n正文", 6000, 5)
	if n := len(byTag(cards[0].elements(), "markdown")); n < 2 || cards[0].elements()[0]["text_size"] != "heading" {
		t.Fatalf("short heading with a colon is a section: %v", cards[0].elements())
	}
}

// A section heading never ends a card: it goes on with what follows it.
func TestAnswerHeadingKeptWithBody(t *testing.T) {
	in := strings.Repeat("字", 50) + "\n## 下一节\n" + strings.Repeat("词", 50)
	_, cards := renderCards(t, in, 60, 5)
	if len(cards) != 2 {
		t.Fatalf("%d cards", len(cards))
	}
	first, second := cards[0].elements(), cards[1].elements()
	if last := first[len(first)-1]; last["text_size"] == "heading" || last["tag"] == "hr" {
		t.Fatalf("card ends with %v", last)
	}
	if second[0]["text_size"] != "heading" {
		t.Fatalf("second card starts with %v", second[0])
	}
}

// A table that does not fit takes its section's heading and lead-in along to the next card; a heading at the very end
// of the answer is kept.
func TestAnswerSectionMovesWithTable(t *testing.T) {
	table := "| a | b |\n|---|---|\n" + strings.Repeat("| "+strings.Repeat("字", 20)+" | x |\n", 3)
	in := "开头\n## 第一节\n" + strings.Repeat("词", 30) + "\n## 第二节\n引言\n" + table
	_, cards := renderCards(t, in, 100, 5)
	if len(cards) < 2 {
		t.Fatalf("%d cards", len(cards))
	}
	second := cards[1].elements()
	if second[0]["text_size"] != "heading" || !strings.Contains(second[0]["content"].(string), "第二节") {
		t.Fatalf("second card starts with %v", second[0])
	}
	_, cards = renderCards(t, "正文\n## 结尾", 6000, 5)
	if !strings.Contains(allContent(cards[0].elements()), "结尾") {
		t.Fatalf("last heading lost: %v", cards[0].elements())
	}
}

// cardComponents is a rendered card's Feishu component cost by packAnswer's count: one per element plus a table's wrapper, header row and cells.
func cardComponents(c decoded) int {
	n := 0
	for _, e := range c.elements() {
		n++
		if e["tag"] == "table" {
			n += (len(e["rows"].([]any)) + 1) * len(e["columns"].([]any))
		}
	}
	return n
}

// pipeTable builds a cols x nrows pipe table with header "<column><j>" (the Chinese word for column) and cell "<i>-<j>".
func pipeTable(cols, nrows int) string {
	var head, rows strings.Builder
	for j := 0; j < cols; j++ {
		fmt.Fprintf(&head, "| 列%d ", j)
	}
	for i := 0; i < nrows; i++ {
		for j := 0; j < cols; j++ {
			fmt.Fprintf(&rows, "| %d-%d ", i, j)
		}
		rows.WriteString("|\n")
	}
	return head.String() + "|\n|" + strings.Repeat("---|", cols) + "\n" + rows.String()
}

// Tables split by the component budget: no card passes it, no element holds more rows than its columns allow, every
// real cell survives, and the overflow indicator gets its own element instead of overfilling a full batch.
func TestAnswerTableComponentBudget(t *testing.T) {
	for ncol := 2; ncol < 90; ncol++ {
		if r := answerTableRowCap(ncol); 1+(r+1)*ncol > answerMaxComponents {
			t.Fatalf("%d cols: cap %d over budget", ncol, r)
		}
	}

	for _, tc := range []struct {
		name       string
		cols, rows int
	}{
		{"7 cols 30 rows", 7, 30},
		{"2 cols 100 rows", 2, 100},
		{"5 cols 40 rows", 5, 40},
		{"29 cols 101 rows", 29, 101}, // rowCap 5 divides 100: the indicator used to overfill the batch
		{"30 cols 101 rows", 30, 101}, // rowCap 4
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, cards := renderCards(t, pipeTable(tc.cols, tc.rows), 1_000_000, 50)
			if a.Clipped {
				t.Fatal("clipped")
			}
			var got int
			cells := map[string]bool{}
			for i, c := range cards {
				if cc := cardComponents(c); cc > answerMaxComponents || len(c.elements()) > answerMaxElements {
					t.Fatalf("card %d: %d comps, %d elements", i, cc, len(c.elements()))
				}
				for _, tb := range byTag(c.elements(), "table") {
					r := tb["rows"].([]any)
					if len(r) > answerTableRowCap(tc.cols) || len(tb["columns"].([]any)) != tc.cols {
						t.Fatalf("bad element: %d rows, %d columns", len(r), len(tb["columns"].([]any)))
					}
					got += len(r)
					for _, row := range r {
						for j := 0; j < tc.cols; j++ {
							if v, ok := row.(map[string]any)["c"+strconv.Itoa(j)].(string); ok {
								cells[v] = true
							}
						}
					}
				}
			}
			wantRows := min(tc.rows, answerMaxRows)
			if tc.rows > answerMaxRows {
				wantRows++ // plus the indicator row
			}
			if got != wantRows {
				t.Fatalf("%d rows survived, want %d", got, wantRows)
			}
			for i := 0; i < wantRows && i < answerMaxRows; i++ {
				for j := 0; j < tc.cols; j++ {
					if !cells[fmt.Sprintf("%d-%d", i, j)] {
						t.Fatalf("cell %d-%d lost", i, j)
					}
				}
			}
		})
	}
}

// Prose, dividers and tables share one budget; what overspends moves on with its heading, never stacking two big tables.
func TestAnswerTableMixedBudget(t *testing.T) {
	a, cards := renderCards(t, strings.Repeat("备注：说明\n", 5)+pipeTable(3, 20), 1_000_000, 10)
	if a.Clipped || len(cards) != 1 || cardComponents(cards[0]) != 5+1+(20+1)*3 {
		t.Fatalf("notes+table: %d cards, clipped %v, %d comps", len(cards), a.Clipped, cardComponents(cards[0]))
	}
	a, cards = renderCards(t, pipeTable(7, 24)+"引言\n"+pipeTable(3, 20), 1_000_000, 10) // 176, then 1, then 64
	if a.Clipped || len(cards) != 2 || cardComponents(cards[0]) != 177 || cardComponents(cards[1]) != 64 {
		t.Fatalf("table+prose+table: %d cards, clipped %v", len(cards), a.Clipped)
	}
	if !strings.Contains(allContent(cards[0].elements()), "引言") {
		t.Fatal("lead-in lost")
	}
	_, cards = renderCards(t, strings.Repeat("备注：x\n", 3)+"## 节\n"+pipeTable(7, 24), 1_000_000, 10) // 3+hr+heading+176 > budget
	if len(cards) != 2 || cardComponents(cards[0]) != 3 || cardComponents(cards[1]) != 177 {
		t.Fatalf("section+table: %d cards, %d/%d comps", len(cards), cardComponents(cards[0]), cardComponents(cards[1]))
	}
	if cards[1].elements()[0]["text_size"] != "heading" {
		t.Fatal("section did not move with its table")
	}
	a, cards = renderCards(t, "引言\n## 节\n"+pipeTable(7, 24)+"\n"+pipeTable(7, 24), 1_000_000, 10)
	if a.Clipped || len(cards) != 3 || cardComponents(cards[0]) != 1 || cardComponents(cards[1]) != 177 || cardComponents(cards[2]) != 176 {
		t.Fatalf("carry+table: %d cards, clipped %v", len(cards), a.Clipped)
	}
}

// The fallback a refused card is sent as rich text: rows are "- column: value" bullet lines and never carry a pipe, even one escaped inside a cell.
func TestAnswerTableFallbackNoPipes(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		bullets        int // "- column0:" bullets across all parts; -1 skips
	}{
		{"7x30", pipeTable(7, 30), "列1：0-1", 30},
		{"2x100", pipeTable(2, 100), "列1：99-1", 100},
		{"escaped pipe", "| 名 | 值 |\n|---|---|\n| a \\| b | x |\n", "名：a / b", -1},
		{"bold header", "| **名** | 数量 |\n|---|---|\n| a | 1 |\n", "名：a，数量：1", -1},
		{"ragged row", "| a | b |\n|---|---|\n| x |\n", "- a：x", -1},
		{"overflow", pipeTable(2, 105), "… +5", -1},
		{"header only", "| a | b |\n|---|---|\n", "a，b", -1},
		{"plain prose", "普通正文\n第二行", "普通正文", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var text string
			for _, p := range RenderAnswer(tc.in, 1_000_000, 50).Parts {
				text += p.Text
				if strings.Contains(p.Text, "|") {
					t.Fatalf("fallback carries pipe-table source: %q", p.Text)
				}
			}
			if !strings.Contains(text, tc.want) {
				t.Fatalf("fallback %q lacks %q", text, tc.want)
			}
			if tc.bullets >= 0 && strings.Count(text, "- 列0：") != tc.bullets {
				t.Fatalf("%d bullets, want %d", strings.Count(text, "- 列0："), tc.bullets)
			}
		})
	}
}

func TestAnswerWideTableBullets(t *testing.T) {
	for _, tc := range []struct {
		cols, rows, budget, parts, minCards int
		want, lead                          string
	}{
		{90, 3, 1_000_000, 10, 1, "列89：2-89", "列0："},
		{100, 3, 1_000_000, 10, 1, "列99：2-99", "列0："},
		{90, 101, 1_000_000, 200, 1, "… +1", "列0："}, // past answerMaxRows on the wide bullet path
		{90, 1, 60, 50, 2, "列89：0-89", "列0："},       // one fat row is rune-split, not one oversized block
		{90, 0, 1_000_000, 5, 1, "列89", "列0"},       // header-only wide table degrades the same way
	} {
		a, cards := renderCards(t, pipeTable(tc.cols, tc.rows), tc.budget, tc.parts)
		var text string
		for i, c := range cards {
			text += a.Parts[i].Text
			if len(byTag(c.elements(), "table")) != 0 || cardComponents(c) > answerMaxComponents {
				t.Fatalf("%dx%d card %d has a table or %d comps", tc.cols, tc.rows, i, cardComponents(c))
			}
		}
		if a.Clipped || len(cards) < tc.minCards || strings.Contains(text, "|") ||
			!strings.Contains(text, tc.lead) || !strings.Contains(text, tc.want) {
			t.Fatalf("%dx%d: clipped %v, %d cards, %q", tc.cols, tc.rows, a.Clipped, len(cards), text)
		}
	}
}

// Retitle puts a push's title over an answer's card and keeps what the answer's own header said.
func TestRetitleKeepsTheBodysHeadingAndStatus(t *testing.T) {
	first := func(md string) *Card {
		a := RenderAnswer(md, 6000, 5)
		c := a.Parts[0].Card(md)
		a.Parts[0].Fill(c)
		return c.Retitle("🔔 ", "巡检结果", Wathet)
	}
	// A title with a status: the push's title heads it, the status stays as its tag and color, the body's title is
	// the first line.
	b, _ := json.Marshal(first("# 巡检日报 · 无变动\n\n一切正常"))
	s := string(b)
	if !strings.Contains(s, `"content":"🔔 巡检结果"`) || !strings.Contains(s, `"text_tag"`) || !strings.Contains(s, "无变动") ||
		!strings.Contains(s, `"template":"grey"`) || !strings.Contains(s, "**巡检日报**") {
		t.Fatalf("status title: %s", s)
	}
	// A status alone was the header: it becomes the tag, no extra line.
	b, _ = json.Marshal(first("状态：请验收\n\n改好了"))
	s = string(b)
	if !strings.Contains(s, `"text_tag"`) || !strings.Contains(s, "请验收") || !strings.Contains(s, `"template":"orange"`) || strings.Contains(s, "**请验收**") {
		t.Fatalf("status only: %s", s)
	}
	// A heading that is only the status (with or without the "#"): the tag, not a bold line repeating it.
	for _, md := range []string{"# 请验收\n\n改好了", "# 待验收\n\n改好了", "# 有变动\n\n见下",
		"# 状态：请验收\n\n改好了", "# 结论：无变动\n\n一切正常", "# 【请验收】\n\n改好了"} {
		b, _ = json.Marshal(first(md))
		s = string(b)
		if !strings.Contains(s, `"text_tag"`) || strings.Contains(s, `"content":"**`) {
			t.Fatalf("%q: %s", md, s)
		}
	}
	// A different title that is a suffix of the push's is still kept.
	b, _ = json.Marshal(first("# 结果\n\n正文"))
	if s = string(b); !strings.Contains(s, "**结果**") {
		t.Fatalf("suffix title: %s", s)
	}
	// A long title equal to the push's (the body's is cut at 50 runes) is not said twice.
	long := strings.Repeat("长", 70)
	a := RenderAnswer("# "+long+"\n\n正文", 6000, 5)
	c := a.Parts[0].Card("")
	a.Parts[0].Fill(c)
	b, _ = json.Marshal(c.Retitle("🔔 ", long, Wathet))
	if s = string(b); strings.Contains(s, "**长") {
		t.Fatalf("long equal title: %s", s)
	}
	// The body's title is the push's: not said twice. No title at all: just the push's.
	for _, md := range []string{"# 巡检结果\n\n正文", "只有正文"} {
		b, _ = json.Marshal(first(md))
		s = string(b)
		if strings.Contains(s, "**巡检结果**") || strings.Contains(s, "text_tag") || !strings.Contains(s, `"template":"wathet"`) {
			t.Fatalf("%q: %s", md, s)
		}
	}
}

// A heading that is a status with its label or in brackets leaves no stray label or brackets in the header.
func TestWithoutStatusDropsLabelsAndBrackets(t *testing.T) {
	for _, title := range []string{"状态：请验收", "结论：无变动", "【请验收】", "当前状态: 有变动", "(无变动)"} {
		for _, st := range answerStatuses {
			if got := withoutStatus(title, st); got != title && got != "" && !strings.Contains(title, got) {
				t.Fatalf("%q: %q", title, got)
			}
		}
		for _, md := range []string{"# " + title + "\n\n正文"} {
			b, _ := json.Marshal(RenderAnswer(md, 6000, 5).Parts[0].Card(md))
			s := string(b)
			if strings.Contains(s, `"content":"状态"`) || strings.Contains(s, `"content":"结论"`) || strings.Contains(s, "【 】") || strings.Contains(s, `"content":"()"`) {
				t.Fatalf("%q: %s", title, s)
			}
		}
	}
	if got := withoutStatus("巡检日报 · 无变动", answerStatuses[1]); got != "巡检日报" {
		t.Fatalf("title with status: %q", got)
	}
}

// A piece of text heavy with escapes ("<" is five bytes in a card, a control character six in JSON) or one long note
// line fills a card on its own: it is cut again by bytes, so no card goes past what Feishu takes and none of it is lost.
func TestAnswerEscapeHeavyPieceFitsBytes(t *testing.T) {
	tests := []struct{ name, in, unit string }{
		{"angle brackets", "# 标题\n" + strings.Repeat(strings.Repeat("<", 99)+"\n", 60), "<"},
		{"control characters", strings.Repeat("\x01", 5990), "\x01"},
		{"long note", "正文\n备注：" + strings.Repeat("字", 12000), "字"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, cards := renderCards(t, tc.in, 6000, 0)
			got := ""
			for i, p := range a.Parts {
				if n := len(p.Fill(p.Card("s")).JSON()); n > answerMaxBytes+2000 {
					t.Errorf("part %d is %d bytes", i, n)
				}
				got += allContent(cards[i].elements())
			}
			unit := CardMarkdown(tc.unit)
			if tc.unit == "\x01" {
				unit = tc.unit
			}
			if want := strings.Count(CardMarkdown(tc.in), unit); strings.Count(got, unit) != want {
				t.Fatalf("%d of %d kept", strings.Count(got, unit), want)
			}
		})
	}
}
