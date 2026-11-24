package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

type postedCard struct{ card, fallback string }

// splitService collects cards on a fast report cycle; failPart, when set, fails the first post whose card contains it.
func splitService(t *testing.T, failPart string) (*Service, *fakeHub, func() []postedCard) {
	t.Helper()
	s, f, _ := reportService(t, true)
	s.reports.mu.Lock()
	s.reports.delay, s.reports.retry = testReportDelay, testReportDelay
	s.reports.mu.Unlock()
	var got []postedCard // app answers run under the fake hub's lock, which the accessor takes too
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m struct{ Card, Fallback string }
		_ = json.Unmarshal(p, &m)
		got = append(got, postedCard{m.Card, m.Fallback})
		if failPart != "" && strings.Contains(m.Card, failPart) {
			failPart = "" // one-shot: the retry of the batch must go through
			return nil, errors.New("network jitter")
		}
		return map[string]any{"message_id": "om_r"}, nil
	})
	return s, f, func() []postedCard { f.mu.Lock(); defer f.mu.Unlock(); return append([]postedCard(nil), got...) }
}

// handTodos makes n todos and reports them done; rich ones also carry a PR stage, agent session and 300-rune text.
func handTodos(s *Service, n int, rich bool) []Todo {
	s.todos.mu.Lock()
	ts := make([]Todo, n)
	for i := range ts {
		td := Todo{ID: fmt.Sprintf("todo_split_%02d", i+1), Origin: "conv1", Title: fmt.Sprintf("第%02d件任务，一个足够长的标题用来超过旧的六十字兜底", i+1)}
		s.todos.todos[td.ID], ts[i] = &td, td
	}
	s.todos.mu.Unlock()
	if rich {
		s.reports.mu.Lock()
		s.reports.loaded, s.reports.stages = true, map[string]todoStage{}
		for i, td := range ts {
			s.reports.stages[td.ID] = todoStage{Stage: "review", PR: fmt.Sprintf("https://git.example.com/team/repo/pull/%02d", i+1)}
		}
		s.reports.mu.Unlock()
	}
	for i, td := range ts {
		sess, text := [2]string{}, "做完了"
		if rich {
			sess = [2]string{"box", fmt.Sprintf("sess-%02d", i+1)}
			text = fmt.Sprintf("特别说明%02d", i+1) + strings.Repeat("很", 290)
		}
		s.taskReport(td, reportDone, "box", sess, text, nil)
	}
	return ts
}

// A few items stay one card without a part suffix; the fallback keeps every title instead of a 60-rune clip.
func TestReportCardsSmallBatchOneCard(t *testing.T) {
	needsLarkLayer(t)
	s, _, posts := splitService(t, "")
	handTodos(s, 3, false)
	waitFor(t, "the one report card", func() bool { return len(posts()) == 1 })
	if c := posts()[0]; strings.Contains(c.card, "（1/") || strings.Contains(c.card, "(1/") || strings.HasSuffix(c.fallback, "…") {
		t.Fatalf("one-card shape or full-titles fallback wrong:\n%q", c.fallback)
	}
}

// 60 tasks go out as 4 ordered cards with every link and (k/N) titles; a failure after card 1 requeues the batch.
func TestReportCardsSplitAndRetryWholeBatch(t *testing.T) {
	needsLarkLayer(t)
	s, _, posts := splitService(t, "（2/4）")
	ts := handTodos(s, 60, false)
	waitFor(t, "card 1, failed card 2, then four retried cards", func() bool { return len(posts()) == 6 })
	got := posts()
	if !strings.Contains(got[0].card, "（1/4）") || !strings.Contains(got[1].card, "（2/4）") {
		t.Fatal("first attempt did not post cards 1-2 before failing")
	}
	var body, fb strings.Builder
	for i := 2; i < 6; i++ {
		c := got[i]
		if !strings.Contains(c.card, fmt.Sprintf("（%d/4）", i-1)) || len(c.card) >= 30000 {
			t.Fatalf("retried card %d: part title missing or %d bytes", i-1, len(c.card))
		}
		body.WriteString(c.card)
		fb.WriteString(c.fallback)
	}
	all, pos := body.String(), -1
	for _, td := range ts {
		link := `"default_url":"https://hub.example.com/todos/` + td.ID + `"`
		if p := strings.Index(all, td.Title); p < 0 || p < pos || strings.Count(all, link) != 1 || !strings.Contains(fb.String(), td.Title) {
			t.Fatalf("title, link or fallback lost for %s", td.ID)
		}
		pos = strings.Index(all, td.Title)
	}
}

// The needs item leads the first card; its answerable question card follows every report card.
func TestReportCardsNeedsItemOnFirstCard(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := splitService(t, "")
	td := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusBlocked, "", 1)
	ix := proto.Interaction{ID: "ix-split", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "用哪个分支？", Options: []proto.QuestionOption{{Label: "main"}}}}}
	f.answerApp(AppInteractions, InteractionsGet, func(json.RawMessage) (any, error) {
		return map[string]any{"ok": true, "interaction": ix}, nil
	})
	s.taskReport(td, reportNeeds, "box", [2]string{"box", "s1"}, noticeAsk(ix), &ix)
	handTodos(s, 30, false)
	waitFor(t, "two report cards and the question card", func() bool { return len(posts()) == 3 })
	if !strings.Contains(posts()[0].card, "（1/2）") || !strings.Contains(posts()[0].card, "用哪个分支") || !strings.Contains(posts()[2].card, `"a":"ix"`) {
		t.Fatalf("the decision is not first or its card did not follow:\n%s", posts()[0].card)
	}
}

// With three link buttons and 300-rune text per task, the real cards stay within the byte and element budgets; every
// task/PR/agent link lands once in order and the last item stays whole on its card.
func TestReportCardsRealJSONBudgetWithButtonRows(t *testing.T) {
	needsLarkLayer(t)
	s, _, posts := splitService(t, "")
	ts := handTodos(s, 15, true)
	waitFor(t, "the byte budget to split the heavy cards", func() bool { return len(posts()) >= 2 })
	var all strings.Builder
	for i, c := range posts() {
		var v struct {
			Body struct {
				Elements []json.RawMessage `json:"elements"`
			} `json:"body"`
		}
		_ = json.Unmarshal([]byte(c.card), &v)
		if len(c.card) > reportCardMaxBytes || len(v.Body.Elements) > reportCardMaxElements {
			t.Fatalf("card %d over budget: %d bytes, %d elements", i+1, len(c.card), len(v.Body.Elements))
		}
		all.WriteString(c.card)
	}
	body, pos := all.String(), -1
	for i, td := range ts {
		for _, link := range []string{`"default_url":"https://hub.example.com/todos/` + td.ID + `"`, fmt.Sprintf(`"default_url":"https://git.example.com/team/repo/pull/%02d"`, i+1), fmt.Sprintf(`"default_url":"https://hub.example.com/s/box/sess-%02d"`, i+1)} {
			if strings.Count(body, link) != 1 {
				t.Fatalf("link lost or repeated: %s", link)
			}
		}
		if p := strings.Index(body, td.Title); p < 0 || p < pos {
			t.Fatalf("title %q lost or out of order", td.Title)
		}
		pos = strings.Index(body, td.Title)
	}
	last := posts()[len(posts())-1]
	if !strings.Contains(last.card, ts[len(ts)-1].Title) || !strings.Contains(last.card, fmt.Sprintf("特别说明%02d", len(ts))) {
		t.Fatalf("the last item was split off its title or body")
	}
}
