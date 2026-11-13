package prompts

import (
	"strings"
	"testing"
)

// The owner's own messages travel in a batch's context as lines without a message id of their own. The grouping prompt
// has to name that
// marker exactly as the connector writes it, and say they are context only: not grouped, never in assign (an id outside
// group.new invalidates the whole answer, server/internal/assistant/inbox_group.go checkGroupAnswer).
func TestInboxGroupSelfLines(t *testing.T) {
	text := RenderInboxGroup(InboxGroup{NewMax: 3, MaxCalls: 4})
	if want := "我（自己说的，不归类）"; !strings.Contains(text, want) {
		t.Fatalf("the grouping prompt does not name the owner's lines as %q:\n%s", want, text)
	}
	for _, bad := range []string{"我(自己说的,不归类)", "我（自己说的,不归类）", "我(自己说的，不归类)"} {
		if strings.Contains(text, bad) {
			t.Errorf("the marker is spelled %q somewhere instead of with full-width parentheses and comma", bad)
		}
	}
	// The sentence that names the marker has to carry both rules in one place: leave them out of the grouping, and never
	// put them in assign.
	var para string
	for _, p := range strings.Split(text, "\n") {
		if strings.Contains(p, "我（自己说的，不归类）") {
			para = p
			break
		}
	}
	if para == "" {
		t.Fatal("the marker does not start a line of its own")
	}
	lower := strings.ToLower(para)
	if !strings.Contains(lower, "assign") {
		t.Errorf("the marker's line does not say the lines must not go into assign: %q", para)
	}
	if !strings.Contains(para, "不要归类") {
		t.Errorf("the marker's line does not say the lines are not grouped: %q", para)
	}
}
