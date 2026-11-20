package assistant

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// cardTags is the element tags of a card and its JSON.
func cardTags(t *testing.T, c botAnswerCard) ([]string, string) {
	t.Helper()
	js := c.card.JSON()
	var m struct {
		Body struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, e := range m.Body.Elements {
		tags = append(tags, e["tag"].(string))
	}
	return tags, js
}

func TestAnswerCards(t *testing.T) {
	if got := answerCards(i18n.ZhHans, nil, "# 标题\n正文", true, []string{"step"}, "foot"); got != nil {
		t.Fatalf("quiet: %v", got)
	}

	const foot = "FOOT-MARK"
	long := "# 长\n" + strings.Repeat("## 节\n"+strings.Repeat("字", 3000)+"\n", 4)
	cards := answerCards(i18n.ZhHans, nil, long, false, []string{"运行 · ls"}, foot)
	if len(cards) < 2 {
		t.Fatalf("%d cards", len(cards))
	}
	for i, c := range cards {
		tags, js := cardTags(t, c)
		last := i == len(cards)-1
		if strings.Contains(js, foot) != last || strings.Count(js, foot) > 1 {
			t.Fatalf("card %d: foot %d times", i, strings.Count(js, foot))
		}
		if (i == 0) != (len(tags) > 0 && tags[0] == "collapsible_panel") || strings.Count(strings.Join(tags, " "), "collapsible_panel") > 1 {
			t.Fatalf("card %d: tags %v", i, tags)
		}
		if c.fallback == "" {
			t.Fatalf("card %d: no fallback", i)
		}
	}

	cards = answerCards(i18n.ZhHans, nil, "正文", false, nil, foot)
	tags, js := cardTags(t, cards[0])
	if len(cards) != 1 || strings.Contains(js, "collapsible_panel") || strings.Count(js, foot) != 1 {
		t.Fatalf("steps nil: tags %v", tags)
	}
}

// The panel is outside RenderAnswer's body byte budget: on a packed first card it shrinks until measured card.JSON() fits, oldest hidden first.
func TestAnswerCardsPanelBudget(t *testing.T) {
	// '<' expands to "&#60;" in card Markdown, packing the first card's answer near its byte budget.
	packed := "# H\n" + strings.Repeat("<", 2400) + strings.Repeat("字", 3600)
	t.Run("pathological on packed card", func(t *testing.T) {
		cards := answerCards(i18n.ZhHans, nil, packed, false, pathologicalSteps(40), "FOOT-MARK")
		for i, ac := range cards {
			if n := len(ac.card.JSON()); n > answerCardMaxBytes {
				t.Fatalf("card %d is %d JSON bytes, want <= %d", i, n, answerCardMaxBytes)
			}
		}
		tags, js := cardTags(t, cards[0])
		if len(tags) == 0 || tags[0] != "collapsible_panel" {
			t.Fatalf("panel missing or not first: %v", tags)
		}
		for _, want := range []string{"…前面还有 20 步", "运行20", "运行39", "FOOT-MARK"} {
			if !strings.Contains(js, want) {
				t.Fatalf("missing %q in %s", want, js)
			}
		}
		if strings.Contains(js, "运行19") {
			t.Fatalf("an oldest step was kept: %s", js)
		}
	})
	t.Run("empty steps no panel", func(t *testing.T) {
		js := answerCards(i18n.ZhHans, nil, packed, false, []string{}, "FOOT-MARK")[0].card.JSON()
		if len(js) > answerCardMaxBytes || strings.Contains(js, "collapsible_panel") {
			t.Fatalf("empty steps rendered a panel or card over budget: %d bytes", len(js))
		}
	})
	t.Run("short line unchanged", func(t *testing.T) {
		js := answerCards(i18n.ZhHans, nil, "正文内容", false, []string{"STEP-PLAIN-MARK `ls`"}, "FOOT-MARK")[0].card.JSON()
		if !strings.Contains(js, "- STEP-PLAIN-MARK `ls`") || len(js) > answerCardMaxBytes {
			t.Fatalf("short panel changed or card over budget: %s", js)
		}
	})
}

// A chart in an answer (persona-card, #3140) is written as numbers before the answer is cut into cards: one longer
// than a card's budget does not vanish into an empty card, and one across the cut does not show its JSON.
func TestAnswerCardsWriteChartsAsNumbersBeforeCutting(t *testing.T) {
	var pts []string
	for i := 1; i <= 300; i++ {
		pts = append(pts, fmt.Sprintf(`{"label":"d%d","value":%d}`, i, 8000+i))
	}
	long := "```persona-card\n" + `{"type":"line","title":"近 300 天步数","unit":"步","points":[` + strings.Join(pts, ",") + `]}` + "\n```"
	var week []string
	for i := 1; i <= 7; i++ {
		week = append(week, fmt.Sprintf(`{"label":"11/%d","value":%d}`, i, 8000+i))
	}
	// pretty-printed, as models often write it: the cut lands inside the JSON
	across := "~~~persona-card\n{\n  \"type\": \"line\",\n  \"title\": \"近 7 天步数\",\n  \"unit\": \"步\",\n  \"points\": [\n    " +
		strings.Join(week, ",\n    ") + "\n  ]\n}\n~~~"
	for _, tc := range []struct{ name, answer, want, point string }{
		{"longer than a card", "最近 300 天平均每天 8,150 步。\n\n" + long + "\n\n后面的话", "近 300 天步数", "8,300"},
		{"across the cut", strings.Repeat("说", botAnswerPiece-100) + "\n\n" + across + "\n\n后面的话", "近 7 天步数", "8,007"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cards := answerCards(i18n.ZhHans, nil, tc.answer, false, nil, "FOOT")
			var all string
			for i, c := range cards {
				_, js := cardTags(t, c)
				if strings.TrimSpace(c.fallback) == "" {
					t.Errorf("card %d of %d is empty: %s", i, len(cards), js)
				}
				all += js
			}
			if !strings.Contains(all, tc.want) || !strings.Contains(all, tc.point) || strings.Contains(all, "persona-card") || strings.Contains(all, `\"label\"`) || strings.Contains(all, `\"type\"`) {
				t.Errorf("the chart is not written as numbers:\n%s", all)
			}
		})
	}
}

// A link's button says what it opens: a page of the hub's web app by the page, a Feishu document as one, any other
// site by its name.
func TestLinkNamer(t *testing.T) {
	name := linkNamer(i18n.ZhHans, "https://hub.example.com")
	for _, c := range []struct{ u, want string }{
		{"https://hub.example.com/persona/c1", "打开会话"},
		{"https://hub.example.com/s/server/a1", "打开会话"},
		{"https://hub.example.com/todos/t1", "打开待办"},
		{"https://hub.example.com/inbox", "打开收件箱"},
		{"https://hub.example.com/kb/people/p1", "打开知识库"},
		{"https://hub.example.com/machines", "打开网页版"},
		{"https://example.feishu.cn/docx/AbC", "打开飞书文档"},
		{"https://example.feishu.cn/calendar", "打开 example.feishu.cn"},
		{"https://www.github.com/nwverse/persona", "打开 github.com"},
	} {
		u, _ := url.Parse(c.u)
		if got := name(u); got != c.want {
			t.Errorf("%s: %q, want %q", c.u, got, c.want)
		}
	}
}
