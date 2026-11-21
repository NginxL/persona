package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Nothing saved: the policy carries no release fields, so the node keeps 20 minutes and 75% of the window (#12008).
// Each release field goes once its own setting is saved, and a value saved out of range (by hand) is left out.
func TestIdleReleasePolicyOnlyOnceSaved(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub().withMachine())
	got := s.idleCompactionPolicy(ctx)
	if got != (assistantwire.IdleCompaction{Enabled: true, IdleMinutes: 30, ContextTokens: 300000}) {
		t.Fatalf("default policy: %+v", got)
	}
	if raw, _ := json.Marshal(got); strings.Contains(string(raw), "release") {
		t.Fatalf("default policy sent release fields: %s", raw)
	}
	if s.idleReleaseMinutes(ctx) != 20 {
		t.Fatalf("default release minutes: %d", s.idleReleaseMinutes(ctx))
	}

	tokens := 200000
	setSettings(t, s, func(v *Settings) { v.IdleAutoCompactTokens = &tokens })
	if got := s.idleCompactionPolicy(ctx); got.ReleaseTokens != tokens || got.ReleaseMinutes != 0 {
		t.Fatalf("saved tokens: %+v", got)
	}
	off := false
	setSettings(t, s, func(v *Settings) { v.IdleAutoCompact = &off })
	if got := s.idleCompactionPolicy(ctx); got.Enabled || got.ReleaseTokens != tokens {
		t.Fatalf("the pre-delivery switch must not turn off the release-time threshold: %+v", got)
	}
	minutes := 45
	setSettings(t, s, func(v *Settings) { v.IdleReleaseMinutes = &minutes })
	if got := s.idleCompactionPolicy(ctx); got.ReleaseMinutes != minutes || got.ReleaseTokens != tokens {
		t.Fatalf("saved minutes: %+v", got)
	}

	badMinutes, badTokens := 60, 5
	setSettings(t, s, func(v *Settings) { v.IdleReleaseMinutes, v.IdleAutoCompactTokens = &badMinutes, &badTokens })
	if got := s.idleCompactionPolicy(ctx); got.ReleaseMinutes != 0 || got.ReleaseTokens != 0 || s.idleReleaseMinutes(ctx) != 20 {
		t.Fatalf("out-of-range saved values: %+v, minutes in effect %d", got, s.idleReleaseMinutes(ctx))
	}
}

func TestIdleReleaseSettingsToolAndAPI(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub().withMachine())
	tool := func(args string) (string, bool) {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: "c1"})
		return r.Content[0].Text, r.IsError
	}
	approve := func(wantCard string) {
		go func() {
			q, ok := awaitCard(s)
			if !ok {
				return
			}
			if wantCard != "" && q.Summary != wantCard {
				t.Errorf("approval card: %q want %q", q.Summary, wantCard)
			}
			allowed := true
			answer, _ := json.Marshal(proto.Resolution{Allow: &allowed})
			if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: answer}); err != nil {
				t.Errorf("approve: %v", err)
			}
		}()
	}
	if got, _ := tool(`{"action":"get","key":"assistant.idle_release_minutes"}`); got != "20" {
		t.Fatalf("default: %q", got)
	}
	for _, v := range []string{"4", "56", "0", `"30"`, "2.5", "null"} {
		if out, isErr := tool(fmt.Sprintf(`{"action":"set","key":"assistant.idle_release_minutes","value":%s}`, v)); !isErr {
			t.Fatalf("invalid value %s accepted: %q", v, out)
		}
	}
	if len(s.pendingQuestions()) != 0 {
		t.Fatal("invalid values created approval cards")
	}
	if out, isErr := tool(`{"action":"set","key":"assistant.idle_release_minutes","value":20}`); isErr || len(s.pendingQuestions()) != 0 {
		t.Fatalf("the value in effect should be a no-op: %q error=%v", out, isErr)
	}
	approve(i18n.T(i18n.Default, "assistant-settings_tool.change_idle_release_minutes", i18n.Args{"before": 20, "after": 45}))
	if out, isErr := tool(`{"action":"set","key":"assistant.idle_release_minutes","value":45}`); isErr {
		t.Fatalf("valid change: %q", out)
	}
	if got, _ := tool(`{"action":"get","key":"assistant.idle_release_minutes"}`); got != "45" || s.idleCompactionPolicy(ctx).ReleaseMinutes != 45 {
		t.Fatalf("approved value: %q, policy %+v", got, s.idleCompactionPolicy(ctx))
	}
	if settingTitles["assistant.idle_release_minutes"] != "title_idle_release_minutes" {
		t.Fatal("no approval-card title for assistant.idle_release_minutes")
	}

	// Saving the default token count while unset is a change: it makes 300000 the release-time threshold too.
	approve("")
	if out, isErr := tool(`{"action":"set","key":"assistant.idle_auto_compact_tokens","value":300000}`); isErr || s.idleCompactionPolicy(ctx).ReleaseTokens != 300000 {
		t.Fatalf("saving the default tokens: %q error=%v, policy %+v", out, isErr, s.idleCompactionPolicy(ctx))
	}
	if out, isErr := tool(`{"action":"set","key":"assistant.idle_auto_compact_tokens","value":300000}`); isErr || len(s.pendingQuestions()) != 0 {
		t.Fatalf("saving the saved value again should be a no-op: %q error=%v", out, isErr)
	}

	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body)))
		return rec
	}
	for _, body := range []string{`{"idle_release_minutes":4}`, `{"idle_release_minutes":56}`, `{"idle_release_minutes":"30"}`, `{"idle_release_minutes":2.5}`} {
		if rec := put(body); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid PUT %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := put(`{"idle_release_minutes":30}`); rec.Code != http.StatusOK || s.idleReleaseMinutes(ctx) != 30 {
		t.Fatalf("valid PUT: %d %s", rec.Code, rec.Body.String())
	}
	if rec := put(`{"notes":"unrelated setting"}`); rec.Code != http.StatusOK || s.idleReleaseMinutes(ctx) != 30 {
		t.Fatal("a partial PUT reset idle_release_minutes")
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant", nil))
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || view["idle_release_minutes"] != float64(30) {
		t.Fatalf("view: %v %+v", err, view["idle_release_minutes"])
	}
}
