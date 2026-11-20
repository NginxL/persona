package prompts

import (
	"strings"
	"testing"
)

func TestSuperviseDecisionInstructionsAppearOnce(t *testing.T) {
	policy := RenderSupervisionInstructions()
	for _, instruction := range []string{"- `recheck`:", "- `undecidable`:", "- `owner_cannot_answer`:", "`todo_decision_add`"} {
		if got := strings.Count(policy, instruction); got != 1 {
			t.Errorf("static policy: %q occurs %d times, want 1", instruction, got)
		}
	}
	if !strings.Contains(policy, "Never take an irreversible/externally visible step on the owner's behalf") {
		t.Fatal("static decision policy lost the owner's approval boundary")
	}
	for _, role := range []struct {
		name string
		data Supervise
	}{
		{"main", Supervise{}},
		{"side", Supervise{Side: true}},
		{"leader", Supervise{Leader: true}},
		{"background", Supervise{Leader: true, Background: true}},
	} {
		t.Run(role.name, func(t *testing.T) {
			for _, decisions := range []bool{false, true} {
				data := role.data
				data.Decisions = decisions
				data.Entries = []SuperviseEntry{{ID: "todo_example", Title: "Example task", Kind: "recheck"}}
				text := RenderSupervise(data)
				// i18n-ignore: old handling instructions must not return in event payloads.
				for _, instruction := range []string{
					"- 决定交回你核对的：", "- 用户那边没有能回答的东西的：",
					"- 用户回「你来定」的：", "- 用户回「这不是我要的」的：", "- 用户回「先讲清背景」的：",
				} {
					if got := strings.Count(text, instruction); got != 0 {
						t.Errorf("Decisions=%v: event repeats %q %d times", decisions, instruction, got)
					}
				}
				if got := strings.Count(text, "todo_decision_add"); got != 0 {
					t.Errorf("Decisions=%v: event repeats decision recording %d times", decisions, got)
				}
				// i18n-ignore: approval handling also belongs only in the static fragment.
				approval := "标着「不会默认执行」的（不可撤回，或别人看得到）你不能替用户走这一步"
				if got := strings.Count(text, approval); got != 0 {
					t.Errorf("Decisions=%v: event repeats approval handling %d times", decisions, got)
				}
			}
		})
	}
}

func TestSuperviseReviewDoesNotAssertCompletion(t *testing.T) {
	const source = "https://github.com/example/app/pull/12"
	criteria := []string{
		"Revalidate and merge the original delivery PR after the prerequisite repair",
		"Deploy and verify the delivery in production",
	}
	for _, role := range []struct {
		name string
		data Supervise
	}{
		{"main", Supervise{}},
		{"side", Supervise{Side: true}},
		{"leader", Supervise{Leader: true}},
		{"background", Supervise{Leader: true, Background: true}},
	} {
		t.Run(role.name, func(t *testing.T) {
			for _, event := range []struct {
				name, detail string
				evidence     []string
			}{
				{"prerequisite merged", "PR merged: " + source, nil},
				{"PR closed", "PR closed: " + source, nil},
				{"changes requested", "PR changes requested: " + source, []string{"criterion 1 [blocked]: changes requested"}},
				{"judged done candidate", "The judge found the reported work finished", []string{
					"criterion 1 [satisfied]: original PR revalidated and merged",
					"criterion 2 [satisfied]: deployed version and live reading recorded",
				}},
				{"recovered review without a reason", "", nil},
			} {
				t.Run(event.name, func(t *testing.T) {
					data := role.data
					data.Entries = []SuperviseEntry{{
						ID: "todo_example", Title: "Deliver the original change", Kind: "review",
						Detail: event.detail, DoneWhen: criteria, Evidence: event.evidence,
					}}
					text := RenderSupervise(data)
					// i18n-ignore: review is a notification, not an acceptance verdict.
					wants := []string{"signal=review", "待逐项复核，不代表完成", event.detail}
					wants = append(wants, criteria...)
					wants = append(wants, event.evidence...)
					for _, want := range wants {
						if !strings.Contains(text, want) {
							t.Errorf("review message lost %q:\n%s", want, text)
						}
					}
					// i18n-ignore: the old blanket completion claim must not return.
					if strings.Contains(text, "hub 判断做完了") {
						t.Errorf("a review wake still claims completion:\n%s", text)
					}
				})
			}
		})
	}
}
