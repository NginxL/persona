package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The done receipt lists the brief's criteria as they stood when the agent stopped, read from the supervision
// ledger's per-criterion state (the one the agent reported is met with its evidence, the other still unchecked). Once the
// review accepted the work, the conclusion card leads with the result and leaves the criteria to the task's page, which
// keeps each with its result and what was done beyond the brief (#10380).
func TestReceiptListsTheBriefsCriteria(t *testing.T) {
	s, f, todo, posts, _ := conclusionService(t, false)
	ctx := context.Background()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	if err := s.recordCriterionReports(ctx, todo, "box", "s1", []criterionReport{{Number: 1, State: "met", Evidence: "pull/12 merged"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitsForReview(t, s, f, todo.ID, "PR #12 已合并 https://code.example.com/a/b/pull/12")
	var receipt receiptBody
	waitFor(t, "the done receipt", func() bool {
		items, _, err := s.feed(ctx, "conv1", time.Time{})
		if err != nil {
			return false
		}
		for _, it := range items {
			if it.Kind == feedReceipt && it.Ref == todo.ID {
				_ = json.Unmarshal(it.Body, &receipt)
			}
		}
		return receipt.State == "done"
	})
	if len(receipt.Criteria) != 2 || receipt.Criteria[0].Criterion != "PR 合并到 master" || receipt.Criteria[0].State != criterionMet ||
		receipt.Criteria[0].Evidence != "pull/12 merged" || receipt.Criteria[1].State != criterionUnchecked || receipt.CriteriaMore != 0 {
		t.Fatalf("receipt = %+v", receipt)
	}
	before := len(posts())
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "合并了",
		"checks": []map[string]any{
			{"criterion": "PR 合并到 master", "met": true, "evidence": "已合并"},
			{"criterion": "浅色 / 深色截图贴进 PR", "met": true, "evidence": "两种截图已核对"},
		},
		"also_done": []string{"补了回归测试"}})
	waitFor(t, "the conclusion card", func() bool { return len(cardsWith(posts()[before:], "结论：修登录崩溃")) == 1 })
	card := cardsWith(posts()[before:], "结论：修登录崩溃")[0]
	// The card leads with the result; the criteria stay on the task's page (#10380).
	if !strings.Contains(card, "✅ 做完了") {
		t.Errorf("card lacks the verdict:\n%s", card)
	}
	for _, unwanted := range []string{"对照当初的要求", "PR 合并到 master", "补了回归测试"} {
		if strings.Contains(card, unwanted) {
			t.Errorf("card lists the criteria (%q):\n%s", unwanted, card)
		}
	}
	if strings.Contains(card, "未达成") {
		t.Errorf("every criterion was met, yet the card names a shortfall:\n%s", card)
	}
	if brief, ok := s.briefOfTodo(todo.ID); !ok || brief.Result == nil || brief.Result.Todo != todo.ID || len(brief.Result.Criteria) != 2 ||
		brief.Result.Criteria[0].State != criterionMet || brief.Result.Criteria[1].State != criterionMet ||
		len(brief.Result.AlsoDone) != 1 {
		t.Fatalf("the task's result was not kept for its page: %+v", brief.Result)
	}
	// A decision's card is about what to choose: it names no result.
	if a := askOf(s, todo.ID); a != nil {
		decide := *a
		decide.Outcome = outcomeDecide
		if raw, _ := json.Marshal(s.conclusionCard(ctx, todoNow(s, todo.ID), decide)); strings.Contains(string(raw), "PR 合并到 master") {
			t.Errorf("a decision's card lists the result:\n%s", raw)
		}
	}
}

// The card's one line for a shortfall: only criteria not met, the first three and how many more; nothing when all
// were met or only some were not checked (#10380).
func TestNotMetLine(t *testing.T) {
	r := &BriefResult{Criteria: []BriefCriterion{{Criterion: "PR merged", State: criterionMet},
		{Criterion: "a test covers it", State: criterionNotMet}, {Criterion: "changelog", State: criterionUnchecked}}}
	if got, want := notMetLine(i18n.En, r), "**Not met**: a test covers it"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, c := range []string{"b", "c", "d", "e"} {
		r.Criteria = append(r.Criteria, BriefCriterion{Criterion: c, State: criterionNotMet})
	}
	if got, want := notMetLine(i18n.En, r), "**Not met**: a test covers it; b; c and 2 more"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if notMetLine(i18n.En, nil) != "" || notMetLine(i18n.En, &BriefResult{Criteria: []BriefCriterion{{Criterion: "x", State: criterionUnchecked}}}) != "" {
		t.Fatal("a line with nothing missed")
	}
}
