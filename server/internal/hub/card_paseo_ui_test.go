package hub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// paseoCards is the persona-view blocks md now holds, each checked as the apps check one.
func paseoCards(t *testing.T, md string) []*wire.View {
	t.Helper()
	var out []*wire.View
	for _, blk := range wire.AssistantViewBlocks(md) {
		if blk.Language != "persona-view" {
			continue
		}
		v, err := blk.Parse()
		if err != nil {
			t.Fatalf("block %d: %v\n%s", blk.Index, err, blk.Raw)
		}
		out = append(out, v)
	}
	return out
}

func onlyItem(t *testing.T, v *wire.View) wire.Component {
	t.Helper()
	if len(v.Sections) != 1 || len(v.Sections[0].Items) == 0 {
		t.Fatalf("sections = %+v", v.Sections)
	}
	return v.Sections[0].Items[0]
}

func TestPaseoUITableBecomesATableCard(t *testing.T) {
	md := "Two features are still off.\n\n```paseo-ui\n" + `{
  "type": "table",
  "title": "尚未开启或未配置完整的功能",
  "columns": ["功能", "当前状态"],
  "rows": [
    ["Inbox AI 分身、助手自动代答", "关闭"],
    ["独立 reviewer Agent", "未配置"]
  ],
  "caption": "跨机器新建 Agent 时，还要看目标机器的启动器配置。"
}` + "\n```\n\nThe rest is on."
	got := paseoUIConverted(i18n.ZhHans, md)
	if strings.Contains(got, "paseo-ui") || !strings.HasPrefix(got, "Two features are still off.\n\n```persona-view\n") || !strings.HasSuffix(got, "```\n\nThe rest is on.") {
		t.Fatalf("text = %q", got)
	}
	cards := paseoCards(t, got)
	if len(cards) != 1 {
		t.Fatalf("cards = %d", len(cards))
	}
	v := cards[0]
	if v.Sections[0].Title != "尚未开启或未配置完整的功能" {
		t.Fatalf("title = %q", v.Sections[0].Title)
	}
	table := onlyItem(t, v)
	if table.Type != wire.ComponentTable || len(table.Columns) != 2 || table.Columns[0].Title != "功能" || len(table.Rows) != 2 ||
		table.Rows[1][1] != "未配置" || table.Caption != "跨机器新建 Agent 时，还要看目标机器的启动器配置。" {
		t.Fatalf("table = %+v", table)
	}
	if !strings.Contains(v.Fallback, "独立 reviewer Agent · 未配置") || !strings.HasPrefix(v.Fallback, "尚未开启或未配置完整的功能\n") {
		t.Fatalf("fallback = %q", v.Fallback)
	}
}

func TestPaseoUITableRowsAsObjectsFooterAlignAndLimits(t *testing.T) {
	var cols []string
	row := map[string]any{}
	for i := range 10 {
		c := string(rune('A' + i))
		cols = append(cols, c)
		row[c] = i
	}
	rows := make([]any, 120)
	for i := range rows {
		rows[i] = row
	}
	raw, _ := json.Marshal(map[string]any{"type": "table", "columns": cols, "rows": rows, "footer": []any{"Sum"}})
	v := paseoUIView(i18n.En, raw)
	table := onlyItem(t, v)
	if len(table.Columns) != 8 || len(table.Rows) != 100 || table.Rows[0][2] != "2" || table.Columns[1].Align != wire.TableAlignEnd {
		t.Fatalf("table = %d columns, %d rows, %+v", len(table.Columns), len(table.Rows), table.Columns[1])
	}
	if table.Caption != "Showing 8 of 10 columns\nShowing the first 100 of 121 rows" {
		t.Fatalf("caption = %q", table.Caption)
	}
	if err := wire.CheckAssistantView(v); err != nil {
		t.Fatal(err)
	}

	v = paseoUIView(i18n.En, json.RawMessage(`{"type":"table","columns":["Region",{"label":"Income","align":"left"},"Δ"],
		"rows":[["East",1280,"+18%"],{"Region":"South","Income":960,"Δ":"n/a"}],"footer":["Total",2240,"+14%"]}`))
	table = onlyItem(t, v)
	if table.Columns[0].Align != "" || table.Columns[1].Align != wire.TableAlignStart || table.Columns[2].Align != "" ||
		len(table.Rows) != 3 || table.Rows[1][1] != "960" || table.Rows[2][0] != "Total" {
		t.Fatalf("table = %+v", table)
	}
}

func TestPaseoUICharts(t *testing.T) {
	v := paseoUIView(i18n.En, json.RawMessage(`{ "type": "bar", "title": "Income", "unit": "k", "data": [
  { "label": "Online", "value": 120 }, { "label": "Store", "value": "86" }, { "label": "Agents", "value": 45, "tone": "green" } ] }`))
	chart := onlyItem(t, v)
	if chart.Type != wire.ComponentChart || chart.Kind != wire.ChartKindBar || chart.Unit != "k" || len(chart.Series) != 1 ||
		len(chart.Series[0].Points) != 3 || chart.Series[0].Points[1].Value != 86 || chart.Series[0].Points[1].Display != "86" {
		t.Fatalf("chart = %+v", chart)
	}
	if v.Fallback != "Income\nOnline 120 k, Store 86, Agents 45 k" {
		t.Fatalf("fallback = %q", v.Fallback)
	}

	// Several series, one left out past four; a null leaves its point out of every series; no names get numbers.
	v = paseoUIView(i18n.En, json.RawMessage(`{ "type": "line", "categories": ["Mon","Tue","Wed"], "area": true,
  "series": [{"values":[1,2,3]},{"values":[4,null,6],"tone":"danger"},{"values":[1,1,1]},{"values":[2,2,2]},{"values":[3,3,3]}] }`))
	chart = onlyItem(t, v)
	if chart.Kind != wire.ChartKindArea || len(chart.Series) != 4 || chart.Series[0].Name != "Series 1" || chart.Series[1].Tone != wire.ToneDanger ||
		len(chart.Series[0].Points) != 2 || chart.Series[0].Points[1].Label != "Wed" || chart.Detail != "Showing 4 of 5 series" {
		t.Fatalf("chart = %+v", chart)
	}
	if err := wire.CheckAssistantView(v); err != nil {
		t.Fatal(err)
	}
}

func TestPaseoUIPieAndDonut(t *testing.T) {
	var data []string
	for i := range 10 {
		data = append(data, `{"label":"s`+string(rune('0'+i))+`","value":1}`)
	}
	v := paseoUIView(i18n.En, json.RawMessage(`{"type":"donut","center":{"value":"312","label":"tickets"},"data":[`+strings.Join(data, ",")+`]}`))
	chart := onlyItem(t, v)
	points := chart.Series[0].Points
	if chart.Kind != wire.ChartKindDonut || chart.Center != "312 tickets" || len(points) != 8 || points[7].Label != "Other" || points[7].Value != 3 {
		t.Fatalf("chart = %+v", chart)
	}
	if err := wire.CheckAssistantView(v); err != nil {
		t.Fatal(err)
	}
	if v := paseoUIView(i18n.En, json.RawMessage(`{"type":"pie","data":[{"label":"a","value":-1},{"label":"b","value":2}]}`)); v != nil {
		t.Fatalf("a negative slice = %+v", v)
	}
	if row := onlyItem(t, paseoUIView(i18n.En, json.RawMessage(`{"type":"pie","data":[{"label":"All","value":5}]}`))); row.Type != wire.ComponentRow || row.Value != "5" {
		t.Fatalf("one slice = %+v", row)
	}
}

func TestPaseoUIStatsProgressChecklistStepsButtons(t *testing.T) {
	v := paseoUIView(i18n.En, json.RawMessage(`{ "type": "stat", "items": [
  { "label": "Income", "value": "¥42.8k", "delta": "+12%", "note": "vs last week" },
  { "label": "Refunds", "value": "1.2%", "delta": "-0.3", "good": "down" },
  { "label": "a", "value": "1" }, { "label": "b", "value": "2" }, { "label": "c", "value": "3" },
  { "label": "d", "value": "4" }, { "label": "e", "value": "5" } ] }`))
	items := v.Sections[0].Items
	if len(items) != 2 || len(items[0].Stats) != 4 || len(items[1].Stats) != 3 {
		t.Fatalf("grids = %+v", items)
	}
	if s := items[0].Stats[0]; s.Detail != "↑ +12% vs last week" || s.Tone != wire.TonePositive {
		t.Fatalf("stat = %+v", s)
	}
	if s := items[0].Stats[1]; s.Detail != "↓ -0.3" || s.Tone != wire.TonePositive {
		t.Fatalf("lower is better = %+v", s)
	}

	v = paseoUIView(i18n.En, json.RawMessage(`{ "type": "progress", "items": [{ "label": "Build", "value": 72 }, { "label": "Cases", "value": 50, "max": 50 }] }`))
	if p := v.Sections[0].Items; *p[0].Ratio != 0.72 || p[0].Detail != "72%" || p[0].Tone != "" || p[1].Detail != "50 / 50" || p[1].Tone != wire.TonePositive {
		t.Fatalf("progress = %+v %+v", p[0], p[1])
	}

	v = paseoUIView(i18n.ZhHans, json.RawMessage(`{ "type": "checklist", "title": "Release", "items": [
  { "text": "Tests pass", "done": true }, { "text": "Canary", "state": "doing", "note": "20 min left" }, "[ ] Roll out", "[!] Sign-off" ], }`))
	rows := v.Sections[0].Items
	if len(rows) != 4 || rows[0].Badge.Text != "已完成" || rows[1].Subtitle != "20 min left" || rows[1].Badge.Tone != wire.ToneCaution ||
		rows[2].Title != "Roll out" || rows[3].Badge.Tone != wire.ToneDanger {
		t.Fatalf("checklist = %+v", rows)
	}
	if v.Fallback != "Release\n[已完成] Tests pass\n[进行中] Canary · 20 min left\n[待办] Roll out\n[受阻] Sign-off" {
		t.Fatalf("fallback = %q", v.Fallback)
	}

	v = paseoUIView(i18n.En, json.RawMessage(`{ "type": "steps", "steps": [
  { "title": "Freeze", "state": "done", "time": "Mon" }, { "title": "Ship", "time": "Wed", "detail": "All at once." } ] }`))
	if rows := v.Sections[0].Items; rows[0].Title != "1. Freeze" || rows[0].Value != "Mon" || rows[1].Badge != nil || rows[1].Subtitle != "All at once." {
		t.Fatalf("steps = %+v", rows)
	}

	v = paseoUIView(i18n.En, json.RawMessage(`{ "type": "buttons", "title": "Next?", "buttons": [
  { "label": "Go on", "message": "Yes, roll it out.", "style": "primary" }, { "label": "Stop" } ] }`))
	if items := v.Sections[0].Items; len(items) != 3 || items[0].Type != wire.ComponentText || items[1].Subtitle != "Yes, roll it out." ||
		items[2].Action != nil || v.Fallback != "Next?\nReply with one of these:\n· Go on\n· Stop" {
		t.Fatalf("buttons = %+v, %q", items, v.Fallback)
	}
}

func TestPaseoUIUnreadableStaysCode(t *testing.T) {
	for _, md := range []string{
		"```paseo-ui\n{\"type\":\"table\"\n```",                // not JSON
		"```paseo-ui\n{\"type\":\"gauge\",\"value\":1}\n```",   // unknown type
		"```paseo-ui\n{\"type\":\"table\",\"columns\":[\"A\"]", // not closed
		"```json\n{\"type\":\"table\",\"columns\":[\"A\"],\"rows\":[]}\n```",
		"````md\n```paseo-ui\n{\"type\":\"bar\",\"data\":[{\"label\":\"a\",\"value\":1}]}\n```\n````", // an example
	} {
		if got := paseoUIConverted(i18n.En, md); got != md {
			t.Errorf("%q became %q", md, got)
		}
	}
}

func TestPaseoUIKeepsTheReplysOwnCardsWhereTheyAre(t *testing.T) {
	bar := "```paseo-ui\n{\"type\":\"bar\",\"title\":\"T\",\"data\":[{\"label\":\"a\",\"value\":1}]}\n```\n"
	own := "```persona-view\n{\"fallback\":\"own\",\"sections\":[{\"items\":[{\"type\":\"text\",\"text\":\"own\"}]}]}\n```\n"
	// Before one of the reply's own cards: its words, so the own card keeps index 0.
	got := paseoUIConverted(i18n.En, bar+own+bar)
	blocks := wire.AssistantViewBlocks(got)
	if len(blocks) != 2 || !strings.HasPrefix(got, "T\na 1\n```persona-view\n{\"fallback\":\"own\"") || !strings.Contains(string(blocks[1].Raw), `"kind":"bar"`) {
		t.Fatalf("text = %q", got)
	}
	// Past three cards a reply: words.
	got = paseoUIConverted(i18n.En, own+own+bar+bar)
	if n := len(wire.AssistantViewBlocks(got)); n != 3 || !strings.HasSuffix(got, "T\na 1\n") {
		t.Fatalf("%d cards: %q", n, got)
	}
}

func TestCardCompatItemsConvertsPaseoUIForEveryClient(t *testing.T) {
	md := "```Paseo-UI\n{\"type\":\"stat\",\"items\":[{\"label\":\"Users\",\"value\":\"12\"}]}\n```"
	items := []proto.Item{
		{ID: "u", Kind: proto.KindText, Role: "user", Text: md},
		{ID: "a", Kind: proto.KindText, Role: "assistant", Text: md},
	}
	for _, ring := range []bool{true, false} {
		got := cardCompatItems(i18n.En, ring, true, items)
		if got[0].Text != md || !strings.HasPrefix(got[1].Text, "```persona-view\n") || items[1].Text != md {
			t.Fatalf("ring %v: %+v", ring, got)
		}
	}
	plain := []proto.Item{{ID: "a", Kind: proto.KindText, Role: "assistant", Text: "no cards"}}
	if got := cardCompatItems(i18n.En, true, true, plain); &got[0] != &plain[0] {
		t.Fatal("a reply without blocks was copied")
	}
}
