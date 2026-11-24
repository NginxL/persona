package assistant

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
)

// The background breaker's fallback note is a fact of the supervision message, so it is in the digest's language
// like the rest of the entry (#13058): each kind's note per language is pinned by a golden, and digestEntry puts the
// note for the owner's locale above the entry.
func TestBackgroundBreakerNoteFollowsTheDigestLanguage(t *testing.T) {
	s, f, _, _, nextSignal := setupAllStoppedBackground(t)
	ctx := t.Context()
	stopped := nextSignal(1)
	for _, kind := range []struct {
		name    string
		breaker breakerFallback
		facts   []string
	}{
		{"auth", breakerFallback{Kind: prompts.BreakerAuth, Tool: "task_review"}, []string{"assistant_denied: task_review"}},
		{"all_stopped", breakerFallback{Kind: prompts.BreakerAllStopped, Rounds: 2, Reason: "repeated_noop"}, []string{"all_stopped", " 2 ", "repeated_noop"}},
	} {
		for _, tc := range []struct{ locale, want string }{
			{"en", "The background supervisor"},
			{"zh-Hans", "后台督办"},
			{"ja", "バックグラウンドの監督"},
		} {
			t.Run(kind.name+"/"+tc.locale, func(t *testing.T) {
				f.hello.Locale = tc.locale
				b := kind.breaker
				g := stopped
				g.Breaker = &b
				note := breakerNote(g, answerLanguage(string(s.botLang(ctx))))
				if !strings.HasPrefix(note, tc.want) || strings.HasSuffix(note, "\n") {
					t.Fatalf("%s note in %s = %q", kind.name, tc.locale, note)
				}
				for _, fact := range kind.facts {
					if !strings.Contains(note, fact) {
						t.Fatalf("%s note in %s lost %q: %q", kind.name, tc.locale, fact, note)
					}
				}
				golden(t, "breaker_note."+kind.name+"."+tc.locale, note)
				e, ok := s.digestEntry(ctx, g, "")
				if !ok {
					t.Fatal("the all_stopped entry did not render")
				}
				if !strings.HasPrefix(e.Detail, note+"\n") {
					t.Fatalf("entry detail does not start with the %s note: %q", tc.locale, e.Detail)
				}
			})
		}
	}
}

// A signal queued by an older hub carries the note as finished text: it is shown as it is, never dropped.
func TestBackgroundBreakerNoteFromAnOlderHubIsShownAsItIs(t *testing.T) {
	s, f, _, _, nextSignal := setupAllStoppedBackground(t)
	f.hello.Locale = "en"
	g := nextSignal(1)
	g.BreakerNote = "note queued before the language change"
	e, ok := s.digestEntry(t.Context(), g, "")
	if !ok || !strings.HasPrefix(e.Detail, g.BreakerNote+"\n") {
		t.Fatalf("an older hub's note was not kept: ok=%v detail=%q", ok, e.Detail)
	}
}
