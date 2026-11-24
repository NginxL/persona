package assistant

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Records written before text_delivery existed still load. Unknown delivery must not become an Agent success claim.
func TestLegacyCheckDecisionDeliveryCard(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, tc := range []struct{ name, chosen, delivery, key string }{
			{"old words", "Run only the unit tests first", "", "assistant-reports.decision_chosen_recorded"},
			{"old label or identical words", "Pause", "", "assistant-reports.decision_chosen_recorded"},
			{"old recommendation", "", "", "assistant-reports.verdict_taken_supervisor"},
			{"old ordinary label", "Pause", "", "assistant-reports.decision_chosen_recorded"},
			{"future delivery", "Pause", "future", "assistant-reports.decision_chosen_recorded"},
		} {
			t.Run(string(lang)+"/"+tc.name, func(t *testing.T) {
				s, _, st := modelService(t)
				ctx := context.Background()
				// Use the old JSON shape, rather than serializing today's acceptAsk with an accidentally supplied default.
				raw := map[string]any{"conclusion": true, "outcome": "decide", "check": "scheduled-check",
					"asked_at": "2026-11-01T09:00:00Z", "accepted_at": "2026-11-01T09:01:00Z", "chosen": tc.chosen,
					"options": []map[string]string{{"label": "Continue"}, {"label": "Pause"}}}
				if tc.name == "old ordinary label" {
					delete(raw, "check")
				}
				if tc.delivery != "" {
					raw["text_delivery"] = tc.delivery
				}
				if err := st.PutSetting(ctx, acceptanceKey, map[string]any{"todo_legacy": raw}); err != nil {
					t.Fatal(err)
				}
				a, ok := s.askCopy("todo_legacy")
				if !ok {
					t.Fatal("legacy acceptance record was lost")
				}
				want := i18n.T(lang, tc.key, i18n.Args{"option": tc.chosen})
				if got := acceptedNote(lang, &a); got != want {
					t.Fatalf("legacy note: %q, want %q", got, want)
				}
				if a.Chosen != tc.chosen || a.AcceptedAt.IsZero() {
					t.Fatalf("legacy choice changed: %+v", a)
				}
			})
		}
	}
}
