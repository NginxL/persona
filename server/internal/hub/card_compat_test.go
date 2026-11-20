package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const ringReply = "今天吃了这些：\n\n```persona-card\n" +
	`{"type":"ring","title":"今天的热量","total":{"value":2000,"unit":"kcal"},"segments":[{"label":"早餐","value":480},{"label":"午餐","value":720}],"note":"还能吃 800 kcal"}` +
	"\n```\n\n继续加油。"

// cardsIn is every persona-card of md, parsed.
func cardsIn(t *testing.T, md string) []wire.PersonaCard {
	t.Helper()
	var out []wire.PersonaCard
	for _, b := range wire.AssistantViewBlocks(md) {
		if b.Language != "persona-card" {
			continue
		}
		var c wire.PersonaCard
		if err := json.Unmarshal(b.Raw, &c); err != nil {
			t.Fatalf("a card that is not JSON: %v\n%s", err, b.Raw)
		}
		out = append(out, c)
	}
	return out
}

// viewsIn is every persona-view of md, parsed.
func viewsIn(t *testing.T, md string) []wire.View {
	t.Helper()
	var out []wire.View
	for _, b := range wire.AssistantViewBlocks(md) {
		if b.Language != "persona-view" {
			continue
		}
		var v wire.View
		if err := json.Unmarshal(b.Raw, &v); err != nil {
			t.Fatalf("a view that is not JSON: %v\n%s", err, b.Raw)
		}
		out = append(out, v)
	}
	return out
}

// mealsRing is the reported card (#3550): a day's goal of 1,950 kcal, 1,770 eaten over four meals.
const mealsRing = "```persona-card\n" +
	`{"type":"ring","title":"今天的热量分配","subtitle":"目标全天摄入","total":{"value":1950,"unit":"kcal"},"segments":[{"label":"早餐","value":420},{"label":"午餐","value":680},{"label":"晚餐","value":520},{"label":"加餐","value":150}]}` +
	"\n```"

// downgradedView is the one persona-view md's ring became, with its section's items by type.
func downgradedView(t *testing.T, md string) (wire.Section, map[string][]wire.Component) {
	t.Helper()
	if len(cardsIn(t, md)) != 0 {
		t.Fatalf("a persona-card is left:\n%s", md)
	}
	views := viewsIn(t, md)
	if len(views) != 1 || len(views[0].Sections) != 1 {
		t.Fatalf("want one persona-view of one section:\n%s", md)
	}
	if views[0].Fallback == "" {
		t.Fatal("the view has no fallback text")
	}
	s := views[0].Sections[0]
	by := map[string][]wire.Component{}
	for _, c := range s.Items {
		by[c.Type] = append(by[c.Type], c)
	}
	return s, by
}

// The reported card on an older app: "Eaten 1,770 / Goal 1,950" on top, the 180 left on its own in grey and not in the
// bar, the title once (the bar is labelled what it shows, not the title again).
func TestRingShowsEatenAgainstTheGoalOnAnOlderApp(t *testing.T) {
	got := ringCardsDowngraded(i18n.ZhHans, "今天吃了这些：\n\n"+mealsRing+"\n\n继续加油。")
	if !strings.HasPrefix(got, "今天吃了这些：\n\n```persona-view\n") || !strings.HasSuffix(got, "\n```\n\n继续加油。") {
		t.Fatalf("the text around the card changed:\n%s", got)
	}
	s, by := downgradedView(t, got)
	if s.Title != "今天的热量分配" || strings.Count(got[:strings.Index(got, `"fallback"`)], "今天的热量分配") != 1 {
		t.Fatalf("the title is not there exactly once:\n%s", got)
	}
	if len(by["text"]) != 1 || by["text"][0].Text != "目标全天摄入" || by["text"][0].Style != wire.TextStyleSecondary {
		t.Fatalf("subtitle = %+v", by["text"])
	}
	m := by["metric"]
	if len(m) != 1 || m[0].Label != "进度" || m[0].Value != "已吃 1,770 / 目标 1,950 kcal" || m[0].Detail != "剩余 180 kcal" {
		t.Fatalf("metric = %+v", m)
	}
	ch := by["chart"]
	if len(ch) != 1 || !ch[0].Stacked || len(ch[0].Series) != 4 {
		t.Fatalf("chart = %+v", ch)
	}
	sum := 0.0
	for _, sr := range ch[0].Series {
		if sr.Name == "剩余" || sr.Points[0].Label != "已吃" {
			t.Fatalf("a bar part = %+v", sr)
		}
		sum += sr.Points[0].Value
	}
	if sum != 1770 {
		t.Fatalf("the bar adds up to %v, want 1770", sum)
	}
	if len(by["row"]) != 4 || by["row"][3].Title != "加餐" || by["row"][3].Value != "150 kcal" {
		t.Fatalf("rows = %+v", by["row"])
	}
	for _, r := range by["row"] {
		if r.Title == "剩余" {
			t.Fatal("remaining is a row among the meals")
		}
	}
	en := ringCardsDowngraded(i18n.En, mealsRing)
	if _, by := downgradedView(t, en); by["metric"][0].Value != "Eaten 1,770 / Goal 1,950 kcal" || by["metric"][0].Detail != "Remaining 180 kcal" {
		t.Fatalf("English metric = %+v", by["metric"])
	}
	ja := ringCardsDowngraded(i18n.Ja, mealsRing)
	if _, by := downgradedView(t, ja); by["metric"][0].Value != "摂取 1,770 / 目標 1,950 kcal" || by["metric"][0].Detail != "残り 180 kcal" {
		t.Fatalf("Japanese metric = %+v", by["metric"])
	}
}

// Past the goal the metric says by how much; without a goal it is the parts' sum alone; a unit that is not food says
// "actual" instead of "eaten".
func TestRingOverTheGoalAndWithoutOne(t *testing.T) {
	over := strings.Replace(mealsRing, `"value":1950`, `"value":1500`, 1)
	_, by := downgradedView(t, ringCardsDowngraded(i18n.ZhHans, over))
	if m := by["metric"][0]; m.Value != "已吃 1,770 / 目标 1,500 kcal" || m.Detail != "超出 270 kcal" {
		t.Fatalf("over the goal: %+v", m)
	}
	none := strings.Replace(mealsRing, `"total":{"value":1950,"unit":"kcal"}`, `"unit":"kcal"`, 1)
	_, by = downgradedView(t, ringCardsDowngraded(i18n.ZhHans, none))
	if m := by["metric"][0]; m.Label != "合计" || m.Value != "1,770 kcal" || m.Detail != "" {
		t.Fatalf("without a goal: %+v", m)
	}
	exact := strings.Replace(mealsRing, `"value":1950`, `"value":1770`, 1)
	if _, by = downgradedView(t, ringCardsDowngraded(i18n.ZhHans, exact)); by["metric"][0].Detail != "" {
		t.Fatalf("on the goal: %+v", by["metric"][0])
	}
	steps := "```persona-card\n" + `{"type":"ring","title":"步数","total":{"value":10000,"unit":"步"},"segments":[{"label":"上午","value":3000},{"label":"下午","value":4500}]}` + "\n```"
	if _, by = downgradedView(t, ringCardsDowngraded(i18n.ZhHans, steps)); by["metric"][0].Value != "实际 7,500 / 目标 10,000 步" || by["chart"][0].Series[0].Points[0].Label != "实际" {
		t.Fatalf("steps: %+v", by)
	}
}

// A goal of 0 is no goal; past three views in a reply (the apps draw no more), rings are words; every view emitted
// passes the hub's own check for assistant views.
func TestRingZeroGoalViewCapAndCheck(t *testing.T) {
	zero := strings.Replace(mealsRing, `"value":1950`, `"value":0`, 1)
	if _, by := downgradedView(t, ringCardsDowngraded(i18n.ZhHans, zero)); by["metric"][0].Value != "1,770 kcal" || by["metric"][0].Detail != "" {
		t.Fatalf("zero goal: %+v", by["metric"])
	}
	view := "```persona-view\n" + `{"sections":[{"items":[{"type":"text","text":"x"}]}],"fallback":"x"}` + "\n```"
	got := ringCardsDowngraded(i18n.ZhHans, view+"\n\n"+mealsRing+"\n\n"+mealsRing+"\n\n"+mealsRing)
	if n := len(viewsIn(t, got)); n != 3 || !strings.HasSuffix(got, "- 剩余 180 kcal") || len(cardsIn(t, got)) != 0 {
		t.Fatalf("views = %d:\n%s", n, got)
	}
	for _, b := range wire.AssistantViewBlocks(got) {
		v, err := b.Parse()
		if err != nil {
			t.Fatalf("view %d: %v", b.Index, err)
		}
		if err := wire.CheckAssistantView(v); err != nil {
			t.Fatalf("view %d does not pass CheckAssistantView: %v", b.Index, err)
		}
	}
}

// More parts than an older app draws series: the first three and "other" in the bar, every part a row.
func TestRingWithManyPartsKeepsFourSeries(t *testing.T) {
	md := "```persona-card\n" + `{"type":"ring","total":{"value":100,"unit":"g"},"segments":[{"label":"a","value":10},{"label":"b","value":10},{"label":"c","value":10},{"label":"d","value":10},{"label":"e","value":10}]}` + "\n```"
	_, by := downgradedView(t, ringCardsDowngraded(i18n.ZhHans, md))
	sr := by["chart"][0].Series
	if len(sr) != 4 || sr[3].Name != "其他" || sr[3].Points[0].Value != 20 || sr[3].Tone != wire.ToneNeutral || len(by["row"]) != 5 {
		t.Fatalf("series = %+v rows = %d", sr, len(by["row"]))
	}
}

// A ring the apps could not draw either (one part, a negative part) is written as words, never left as JSON.
func TestUndrawableRingBecomesWords(t *testing.T) {
	md := "```persona-card\n" + `{"type":"ring","title":"热量","unit":"kcal","segments":[{"label":"早餐","value":480,"note":"燕麦"}],"note":"偏少"}` + "\n```"
	got := ringCardsDowngraded(i18n.ZhHans, md)
	want := "**热量**\n- 合计: 480 kcal\n- 早餐: 480 kcal (燕麦)\n\n偏少"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	neg := "```persona-card\n" + `{"type":"ring","title":"差额","total":{"value":100,"unit":"g"},"segments":[{"label":"a","value":-5},{"label":"b","value":20}]}` + "\n```\n"
	got = ringCardsDowngraded(i18n.ZhHans, neg)
	if strings.Contains(got, "{") || strings.Contains(got, "persona-") || !strings.Contains(got, "- 实际 15 / 目标 100 g") || !strings.Contains(got, "- 剩余 85 g") {
		t.Fatalf("negative ring = %q", got)
	}
}

// Only rings change: other cards, a ring shown as an example inside another code block, and a ring still being
// written stay exactly as they are.
func TestOnlyRingsAreRewritten(t *testing.T) {
	for _, md := range []string{
		"```persona-card\n" + `{"type":"stacked_bar","segments":[{"label":"a","value":1}]}` + "\n```",
		"````markdown\n```persona-card\n" + `{"type":"ring","segments":[{"label":"a","value":1},{"label":"b","value":2}]}` + "\n```\n````",
		"```persona-card\n" + `{"type":"ring","segments":[{"label":"a","value":1}`,
		"ring 一词，没有卡片",
	} {
		if got := ringCardsDowngraded(i18n.ZhHans, md); got != md {
			t.Fatalf("changed:\n%s\n→\n%s", md, got)
		}
	}
}

func TestCardCompatItemsTouchesOnlyAssistantTextAndKeepsTheNodesSlice(t *testing.T) {
	items := []proto.Item{
		{ID: "u", Role: "user", Kind: proto.KindText, Text: ringReply},
		{ID: "a", Role: "assistant", Kind: proto.KindText, Text: ringReply},
		{ID: "t", Role: "assistant", Kind: proto.KindToolResult, Text: ringReply},
	}
	if got := cardCompatItems(i18n.ZhHans, true, true, items); &got[0] != &items[0] {
		t.Fatal("a client that draws rings got a copy")
	}
	got := cardCompatItems(i18n.ZhHans, false, true, items)
	if got[0].Text != ringReply || got[2].Text != ringReply {
		t.Fatal("a user message or tool result was rewritten")
	}
	if strings.Contains(got[1].Text, `"ring"`) {
		t.Fatalf("the assistant's ring went out: %s", got[1].Text)
	}
	if items[1].Text != ringReply {
		t.Fatal("the node's slice was written to")
	}
}

// What each app gets pushed for the same reply: the iOS builds out before the ring (no feature) and an Android without
// it get a persona-view; a new iOS build, Android and the web, which declare persona_card_ring, get the ring.
func TestEachClientGetsTheRingOrAViewPushed(t *testing.T) {
	type app struct {
		name     string
		platform string
		features []string
		wantRing bool
	}
	apps := []app{
		{"old iOS", "ios-app", []string{"elicitation"}, false},
		{"new iOS", "ios-app", []string{"elicitation", featureCardRing}, true},
		{"Android without the feature", "android", []string{"elicitation"}, false},
		{"Android", "android", []string{"elicitation", featureCardRing}, true},
		{"web", "web", []string{"elicitation", featureCardRing}, true},
	}
	h := &Hub{clients: map[*client]struct{}{}}
	clients := map[string]*client{}
	for _, a := range apps {
		c := &client{out: make(chan []byte, 4), subs: map[[2]string]bool{{"m1", "s1"}: true}}
		c.meta.Platform = a.platform
		c.meta.features = map[string]bool{}
		for _, f := range a.features {
			c.meta.features[f] = true
		}
		addTestClient(h, c)
		clients[a.name] = c
	}
	h.itemsToSubscribers("m1", "s1", []proto.Item{{ID: "a1", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: ringReply}}, itemSpan{}, false)
	for _, a := range apps {
		var e struct {
			Items []proto.Item `json:"items"`
		}
		if err := json.Unmarshal(<-clients[a.name].out, &e); err != nil || len(e.Items) != 1 {
			t.Fatalf("%s: %v %+v", a.name, err, e)
		}
		if a.wantRing {
			if c := cardsIn(t, e.Items[0].Text); len(c) != 1 || c[0].Type != wire.PersonaCardKindRing {
				t.Fatalf("%s got %+v, want the ring", a.name, c)
			}
			continue
		}
		downgradedView(t, e.Items[0].Text)
	}
}

func TestWorkReportProjectionFollowsEachClientWithoutChangingHistory(t *testing.T) {
	const report = "### Persona work report\n\n- Status: blocked\n- Merged: no\n- Deployed: n/a\n- Waiting: the sample owner\n\n---"
	const archived = "<closeout status=\"done\">\ndid: reviewed the archive\n</closeout>"
	items := []proto.Item{
		{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: "Three examples checked.\n\n" + report},
		{ID: "u2", Role: "user", Kind: proto.KindText, Text: report},
		{ID: "t3", Role: "assistant", Kind: proto.KindText, Text: report, FullSize: 6000},
		{ID: "a4", Role: "assistant", Kind: proto.KindText, Text: archived},
	}
	h := &Hub{clients: map[*client]struct{}{}}
	for _, known := range []bool{true, false} {
		c := &client{out: make(chan []byte, 1), subs: map[[2]string]bool{{"m1", "s1"}: true}}
		c.meta.features = map[string]bool{featureMarkdownCloseout: known}
		addTestClient(h, c)
	}
	h.itemsToSubscribers("m1", "s1", items, itemSpan{}, false)
	for c := range h.clients {
		var event struct{ Items []proto.Item }
		if err := json.Unmarshal(<-c.out, &event); err != nil || len(event.Items) != len(items) {
			t.Fatalf("event = %+v, err = %v", event, err)
		}
		got := event.Items[0].Text
		if c.has(featureMarkdownCloseout) {
			if got != items[0].Text {
				t.Errorf("new client got %q", got)
			}
		} else if !strings.HasPrefix(got, "Three examples checked.\n\n<closeout status=\"blocked\"") || !strings.HasSuffix(got, "</closeout>") || strings.Contains(got, "### Persona ") {
			t.Errorf("old client cannot strip the report: %q", got)
		}
		for i := 1; i < len(items); i++ {
			if event.Items[i].Text != items[i].Text {
				t.Errorf("item %d changed: %+v", i, event.Items[i])
			}
		}
	}
	if !strings.HasSuffix(items[0].Text, report) {
		t.Fatal("projection mutated the stored reply")
	}
}

// A REST page declares rings in X-Persona-Features, or by matching a connection of its device that declared them.
func TestRequestWantsRingsByHeaderOrMatchingConnection(t *testing.T) {
	c := &client{device: "d1"}
	c.meta.Platform, c.meta.Version = "ios-app", "2.0+300"
	c.meta.features = map[string]bool{featureCardRing: true}
	h := &Hub{clients: map[*client]struct{}{c: {}}}
	req := func(client string, header string) bool {
		r := httptest.NewRequest("GET", "/api/v1/sessions/m1/s1/items", nil)
		ctx := context.WithValue(r.Context(), principalKey{}, principal{ID: "d1", Role: roleFull})
		ctx = context.WithValue(ctx, requestClientKey{}, parseRequestClient(client))
		if header != "" {
			r.Header.Set(featuresHeader, header)
		}
		return h.requestWants(r.WithContext(ctx), featureCardRing)
	}
	if !req("ios-app/2.0+300", "") {
		t.Fatal("the new iOS build's page did not match its connection")
	}
	if req("ios-app/1.9+250", "") {
		t.Fatal("an older build of the same device was taken for one that draws rings")
	}
	if req("", "") {
		t.Fatal("a request naming no client was taken for one that draws rings")
	}
	if !req("android/1", "elicitation, persona_card_ring") {
		t.Fatal("the X-Persona-Features header was not read")
	}
}
