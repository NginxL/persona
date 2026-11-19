package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestIdleCompactSettingsDefaultsAndView(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	ctx := context.Background()
	if !s.idleAutoCompact(ctx) || s.idleAutoCompactMinutes(ctx) != 30 || s.idleAutoCompactTokens(ctx) != 300000 {
		t.Fatalf("defaults: enabled=%v minutes=%d tokens=%d", s.idleAutoCompact(ctx), s.idleAutoCompactMinutes(ctx), s.idleAutoCompactTokens(ctx))
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant", nil))
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || view["idle_auto_compact"] != true || view["idle_auto_compact_minutes"] != float64(30) || view["idle_auto_compact_tokens"] != float64(300000) {
		t.Fatalf("effective defaults in API view: %d %+v", rec.Code, view)
	}
}

func TestIdleCompactSettingsToolApprovalAndValidation(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub().withMachine())
	tool := func(args string) (string, bool) {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: "c1"})
		return r.Content[0].Text, r.IsError
	}
	for _, c := range []struct{ key, want string }{
		{"assistant.idle_auto_compact", "true"}, {"assistant.idle_auto_compact_minutes", "30"}, {"assistant.idle_auto_compact_tokens", "300000"},
	} {
		if got, _ := tool(fmt.Sprintf(`{"action":"get","key":%q}`, c.key)); got != c.want {
			t.Fatalf("%s default: got %q want %q", c.key, got, c.want)
		}
	}
	if out, isErr := tool(`{"action":"set","key":"assistant.idle_auto_compact","value":null}`); !isErr {
		t.Fatalf("null must not disable compaction: %q", out)
	}
	for _, c := range []struct{ key, value string }{
		{"assistant.idle_auto_compact", `"yes"`}, {"assistant.idle_auto_compact_minutes", "0"}, {"assistant.idle_auto_compact_minutes", "1441"},
		{"assistant.idle_auto_compact_tokens", "9999"}, {"assistant.idle_auto_compact_tokens", "10000001"},
	} {
		if out, isErr := tool(fmt.Sprintf(`{"action":"set","key":%q,"value":%s}`, c.key, c.value)); !isErr || strings.TrimSpace(out) == "" {
			t.Fatalf("invalid %s=%s: %q error=%v", c.key, c.value, out, isErr)
		}
	}
	if len(s.pendingQuestions()) != 0 {
		t.Fatal("invalid values unexpectedly created approval cards")
	}
	if out, isErr := tool(`{"action":"set","key":"assistant.idle_auto_compact","value":true}`); isErr || len(s.pendingQuestions()) != 0 || !strings.Contains(out, "unchanged") && !strings.Contains(out, "未变化") {
		t.Fatalf("same value should be a no-op: %q error=%v", out, isErr)
	}
	card := i18n.T(i18n.Default, "assistant-settings_tool.change_idle_auto_compact", i18n.Args{"before": i18n.T(i18n.Default, "assistant-settings_tool.on"), "after": i18n.T(i18n.Default, "assistant-settings_tool.off")})
	go func() {
		q, ok := awaitCard(s)
		if !ok {
			return
		}
		allowed := true
		answer, _ := json.Marshal(proto.Resolution{Allow: &allowed})
		if q.Summary != card {
			t.Errorf("approval card: %q want %q", q.Summary, card)
		}
		if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: answer}); err != nil {
			t.Errorf("approve: %v", err)
		}
	}()
	if out, isErr := tool(`{"action":"set","key":"assistant.idle_auto_compact","value":false}`); isErr || !strings.Contains(out, "saved") && !strings.Contains(out, "保存") {
		t.Fatalf("approved change: %q error=%v", out, isErr)
	}
	if got, _ := tool(`{"action":"get","key":"assistant.idle_auto_compact"}`); got != "false" {
		t.Fatalf("approved value was not persisted: %q", got)
	}
	for _, c := range []struct{ key, value, want string }{
		{"assistant.idle_auto_compact_minutes", "45", "45"}, {"assistant.idle_auto_compact_tokens", "400000", "400000"},
	} {
		go func() {
			q, ok := awaitCard(s)
			if !ok {
				return
			}
			allowed := true
			answer, _ := json.Marshal(proto.Resolution{Allow: &allowed})
			if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: answer}); err != nil {
				t.Errorf("approve %s: %v", c.key, err)
			}
		}()
		out, isErr := tool(fmt.Sprintf(`{"action":"set","key":%q,"value":%s}`, c.key, c.value))
		if isErr {
			t.Fatalf("valid %s: %q", c.key, out)
		}
		if got, _ := tool(fmt.Sprintf(`{"action":"get","key":%q}`, c.key)); got != c.want {
			t.Fatalf("persisted %s: got %q want %q", c.key, got, c.want)
		}
	}
}

func TestIdleCompactSettingsPutValidationAndPersistence(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body)))
		return rec
	}
	for _, body := range []string{
		`{"idle_auto_compact":"false"}`,
		`{"idle_auto_compact_minutes":0}`, `{"idle_auto_compact_minutes":1441}`,
		`{"idle_auto_compact_minutes":2.5}`, `{"idle_auto_compact_tokens":9999}`,
		`{"idle_auto_compact_tokens":10000001}`, `{"idle_auto_compact_tokens":"300000"}`,
	} {
		if rec := put(body); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid PUT %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := put(`{"idle_auto_compact":false,"idle_auto_compact_minutes":45,"idle_auto_compact_tokens":450000}`); rec.Code != http.StatusOK {
		t.Fatalf("valid PUT: %d %s", rec.Code, rec.Body.String())
	}
	ctx := context.Background()
	if s.idleAutoCompact(ctx) || s.idleAutoCompactMinutes(ctx) != 45 || s.idleAutoCompactTokens(ctx) != 450000 {
		t.Fatal("PUT did not persist all three settings")
	}
	if rec := put(`{"notes":"unrelated setting"}`); rec.Code != http.StatusOK || s.idleAutoCompact(ctx) || s.idleAutoCompactMinutes(ctx) != 45 || s.idleAutoCompactTokens(ctx) != 450000 {
		t.Fatal("partial PUT reset idle compaction settings")
	}
}
