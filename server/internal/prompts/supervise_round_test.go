package prompts

import (
	"strings"
	"testing"
)

// What only a Fresh message carries. i18n-ignore: exact generated prompt text.
const (
	freshHeader = "这一轮通常从干净的上下文开始"
	lastReview  = "上次结论（"
)

func background(fresh bool, entries ...SuperviseEntry) Supervise {
	return Supervise{Leader: true, Background: true, Fresh: fresh, Entries: entries}
}

// A Fresh round is the same facts as the message that is not Fresh plus its header line, marker and last reviews: the
// handling rules are in neither (they are the static supervision instructions), whatever kinds its entries are.
func TestSuperviseFreshAddsOnlyTheRoundsFacts(t *testing.T) {
	for _, entry := range []SuperviseEntry{
		{ID: "t1", Title: "Example task", Kind: "idle"},
		{ID: "t1", Title: "Example task", Kind: "review"},
		{ID: "t1", Title: "Example task", Kind: "report", Report: "done"},
		{ID: "t1", Title: "Example task", Kind: "idle", Closeout: &SuperviseCloseout{Status: "done"}},
		{ID: "t1", Title: "Example task", Kind: "stalled", Minutes: 30},
		{ID: "t1", Title: "Example task", Kind: "failed"},
	} {
		fresh := RenderSupervise(background(true, entry))
		whole := RenderSupervise(background(false, entry))
		for _, banned := range []string{"怎么处理", "task_review", "agent_send", "interaction_answer", "supervision_finish", "task_scope"} {
			if strings.Contains(fresh, banned) {
				t.Errorf("%s: a Fresh round carries handling policy %q:\n%s", entry.Kind, banned, fresh)
			}
		}
		if !strings.Contains(fresh, "signal="+entry.Kind) {
			t.Errorf("%s: the round lost its signal:\n%s", entry.Kind, fresh)
		}
		lines := strings.Split(fresh, "\n")
		var kept []string
		for _, l := range lines {
			if !strings.HasPrefix(l, freshHeader) && l != SuperviseRoundMarker {
				kept = append(kept, l)
			}
		}
		if strings.Join(kept, "\n") != whole {
			t.Errorf("%s: a Fresh round differs from the whole message by more than its header and marker:\n%s\n---\n%s", entry.Kind, fresh, whole)
		}
		// Every Fresh message is still one ParseInjected reads as supervision, at its level.
		if in := ParseInjected(fresh); in == nil || in.State != "supervise" || in.Level != "triage" {
			t.Fatalf("not read back as a supervision message: %+v", in)
		}
	}
}

// Not Fresh, a message is as before #7315 (the goldens in internal/assistant/testdata hold the exact bytes), with no
// header line, marker or last review.
func TestSuperviseNotFreshIsUnchanged(t *testing.T) {
	entry := SuperviseEntry{ID: "t1", Title: "Example task", Kind: "idle", LastReview: &SuperviseLastReview{Verdict: "rework", At: "11-14 10:20", Note: "add the tests"}}
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
			data := role.data
			data.Entries = []SuperviseEntry{entry}
			text := RenderSupervise(data)
			for _, n := range []string{freshHeader, SuperviseRoundMarker, lastReview} {
				if strings.Contains(text, n) {
					t.Errorf("%q is in the message:\n%s", n, text)
				}
			}
			data.Entries[0].LastReview = nil
			if again := RenderSupervise(data); again != text {
				t.Fatal("the last review changed a message that is not Fresh")
			}
			if IsSuperviseRound(text) {
				t.Fatal("a message that is not Fresh reads as a round")
			}
		})
	}
}

// A Fresh message says where earlier rounds' decisions are, carries the marker line after its level tag and each
// entry's last review; IsSuperviseRound is true only for such a supervision message.
func TestSuperviseRoundMarker(t *testing.T) {
	text := RenderSupervise(Supervise{Level: "info", Leader: true, Background: true, Fresh: true, Entries: []SuperviseEntry{
		{ID: "t1", Title: "Example task", Kind: "review", LastReview: &SuperviseLastReview{Verdict: "rework", At: "11-14 10:20", Note: "add the tests"}},
		{ID: "t2", Title: "Other task", Kind: "idle"},
	}})
	lines := strings.Split(text, "\n")
	if !strings.HasPrefix(lines[2], freshHeader) || lines[3] != `<persona-supervision level="info" />` || lines[4] != SuperviseRoundMarker {
		t.Fatalf("header, level tag and marker:\n%s", strings.Join(lines[:6], "\n"))
	}
	if !strings.Contains(text, "\n   "+lastReview+"11-14 10:20）：rework，add the tests\n") || strings.Count(text, lastReview) != 1 {
		t.Fatalf("the last review:\n%s", text)
	}
	if in := ParseInjected(text); in == nil || in.State != "supervise" || in.Level != "info" {
		t.Fatalf("ParseInjected: %+v", in)
	}
	if !IsSuperviseRound(text) || !IsSuperviseRound("<persona-context>\n- 现在：x\n</persona-context>\n"+text) {
		t.Fatal("a round is not read as one")
	}
	for _, other := range []string{
		"hello " + SuperviseRoundMarker,
		"Look:\n" + SuperviseRoundMarker + "\nis a tag",
		RenderSupervise(Supervise{Entries: []SuperviseEntry{{ID: "t1", Title: "Example task", Kind: "idle"}}}),
	} {
		if IsSuperviseRound(other) {
			t.Fatalf("read as a round: %q", other)
		}
	}
}
