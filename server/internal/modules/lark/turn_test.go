package lark

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Each progress card's answer has a key of its own: a held run answers every turn under a new card.
func TestAnswerKey(t *testing.T) {
	a, b := answerKey("run1", "om_1"), answerKey("run1", "om_2")
	if a == b || a != answerKey("run1", "om_1") || len(a) > 50 || !strings.HasPrefix(a, "ans-") {
		t.Fatalf("keys %q %q", a, b)
	}
}

func TestFeishuEndCardLocales(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, end := range []channel.End{channel.EndDone, channel.EndStopped, channel.EndError, channel.EndGaveUp,
			channel.EndGone, channel.EndOffline, channel.EndRecalled, channel.EndUnrelated} {
			name := string(end)
			if end == channel.EndDone {
				name = "done"
			}
			t.Run(string(lang)+"/"+name, func(t *testing.T) {
				want := i18n.T(lang, "assistant-feishu.end_"+name)
				foot := endFoot(lang, "Persona", time.Second, 1, true, end)
				c := botcard.NewCard("", "", foot).Note(foot)
				endHeader(lang, c, end)
				if strings.Contains(c.JSON(), "assistant-feishu.") || !strings.HasPrefix(foot, want+" · ") {
					t.Fatalf("unlocalized ending: %s", c.JSON())
				}
				if end == channel.EndDone {
					if c.Header != nil {
						t.Fatalf("normal completion gained a header: %s", c.JSON())
					}
				} else if c.Header == nil || c.Header.Title.Content != want {
					t.Fatalf("ending header: %s", c.JSON())
				}
			})
		}
	}
}

// Each piece of a /read answer has a key of its own, and a held run's later turn other keys.
func TestReadAnswerKey(t *testing.T) {
	turn := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	a := readAnswerKey("run1", turn, 0)
	if a != readAnswerKey("run1", turn, 0) || len(a) > 46 || !strings.HasPrefix(a, "rd-") {
		t.Fatalf("key %q", a)
	}
	seen := map[string]bool{a: true}
	for _, k := range []string{readAnswerKey("run1", turn, 1), readAnswerKey("run1", turn.Add(time.Minute), 0), readAnswerKey("run2", turn, 0)} {
		if seen[k] {
			t.Fatalf("key %q repeats", k)
		}
		seen[k] = true
	}
}
