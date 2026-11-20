package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestSupervisionBatchManagerConfigOverridesSavedSettings(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	f.settings = &Settings{SupervisionBatchMinutes: ptr(60)}
	s := newTestService(t, f)
	pin := func(minutes *int) {
		f.mu.Lock()
		f.hello.Assistant.SupervisionBatchMinutes = minutes
		f.mu.Unlock()
	}
	if got := supervisionBatchMinutesOf(s.settings(ctx)); got != 60 {
		t.Fatalf("unset manager pin = %d, want saved setting 60", got)
	}
	pin(ptr(45))
	if got := supervisionBatchMinutesOf(s.settings(ctx)); got != 45 {
		t.Fatalf("manager-pinned effective setting = %d, want 45", got)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant", nil))
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || view["supervision_batch_minutes"] != float64(45) {
		t.Fatalf("manager-pinned API view: %d %+v", rec.Code, view)
	}
	readback := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(`{"action":"get","key":"assistant.supervision_batch_minutes"}`), MachineID: "server", SessionID: "conv1"})
	if readback.IsError || len(readback.Content) != 1 || readback.Content[0].Text != "45" {
		t.Fatalf("manager-pinned persona_settings readback: %+v", readback)
	}
	if len(s.pendingQuestions()) != 0 {
		t.Fatal("reading a manager pin created an approval card")
	}
	pin(ptr(0))
	if got := supervisionBatchMinutesOf(s.settings(ctx)); got != 0 {
		t.Fatalf("explicit manager zero = %d, want immediate delivery", got)
	}
	pin(nil)
	if got := supervisionBatchMinutesOf(s.settings(ctx)); got != 60 {
		t.Fatalf("removing the manager pin = %d, want saved setting 60", got)
	}
}

func TestSupervisionBatchManagerConfigZeroReleasesHeldDigest(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	shortSupervise(t, s)
	f.mu.Lock()
	f.hello.Assistant.SupervisionBatchMinutes = ptr(60)
	f.mu.Unlock()
	routine := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "manager-release-on-zero", Minutes: 50}
	s.queueSignal(routine)
	waitFor(t, "a manager-pinned held batch", func() bool {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		return !s.supervise.pending[routine.id()].batchUntil.IsZero()
	})
	f.mu.Lock()
	f.hello.Assistant.SupervisionBatchMinutes = ptr(0)
	f.mu.Unlock()
	s.ProfileChanged(context.Background())
	waitFor(t, "the held digest after the manager pin is reloaded as zero", func() bool { return len(assistantSendsTo(f, "conv1")) == 1 })
}

func TestSupervisionBatchSettingsDefaultsAndView(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant", nil))
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || view["supervision_batch_minutes"] != float64(0) {
		t.Fatalf("effective default in API view: %d %+v", rec.Code, view)
	}
}

func TestSupervisionBatchSettingsToolApprovalAndReadback(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub().withMachine())
	tool := func(args string) (string, bool) {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: "conv1"})
		return r.Content[0].Text, r.IsError
	}
	if got, isErr := tool(`{"action":"get","key":"assistant.supervision_batch_minutes"}`); isErr || got != "0" {
		t.Fatalf("default readback: %q error=%v", got, isErr)
	}
	for _, value := range []string{"null", "-1", "1441", "2.5", `"45"`} {
		if out, isErr := tool(fmt.Sprintf(`{"action":"set","key":"assistant.supervision_batch_minutes","value":%s}`, value)); !isErr || strings.TrimSpace(out) == "" {
			t.Fatalf("invalid value %s: %q error=%v", value, out, isErr)
		}
	}
	if len(s.pendingQuestions()) != 0 {
		t.Fatal("invalid values unexpectedly created approval cards")
	}
	go func() {
		q, ok := awaitCard(s)
		if !ok {
			return
		}
		allowed := true
		answer, _ := json.Marshal(proto.Resolution{Allow: &allowed})
		if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: answer}); err != nil {
			t.Errorf("approve: %v", err)
		}
	}()
	if out, isErr := tool(`{"action":"set","key":"assistant.supervision_batch_minutes","value":45}`); isErr || !strings.Contains(out, "45") {
		t.Fatalf("approved setting: %q error=%v", out, isErr)
	}
	if got, _ := tool(`{"action":"get","key":"assistant.supervision_batch_minutes"}`); got != "45" {
		t.Fatalf("persisted readback = %q, want 45", got)
	}
	if out, isErr := tool(`{"action":"set","key":"assistant.supervision_batch_minutes","value":45}`); isErr || !strings.Contains(out, "unchanged") && !strings.Contains(out, "未变化") {
		t.Fatalf("same value should need no approval: %q error=%v", out, isErr)
	}
}

func TestSupervisionBatchSettingsPutValidationAndPersistence(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body)))
		return rec
	}
	for _, body := range []string{
		`{"supervision_batch_minutes":-1}`, `{"supervision_batch_minutes":1441}`,
		`{"supervision_batch_minutes":2.5}`, `{"supervision_batch_minutes":"45"}`,
	} {
		if rec := put(body); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid PUT %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := put(`{"supervision_batch_minutes":45}`); rec.Code != http.StatusOK {
		t.Fatalf("valid PUT: %d %s", rec.Code, rec.Body.String())
	}
	if got := supervisionBatchMinutesOf(s.settings(context.Background())); got != 45 {
		t.Fatalf("PUT readback = %d, want 45", got)
	}
	if rec := put(`{"notes":"unrelated setting"}`); rec.Code != http.StatusOK || supervisionBatchMinutesOf(s.settings(context.Background())) != 45 {
		t.Fatal("partial PUT reset the supervision batch interval")
	}
	if rec := put(`{"supervision_batch_minutes":0}`); rec.Code != http.StatusOK || supervisionBatchMinutesOf(s.settings(context.Background())) != 0 {
		t.Fatal("PUT did not restore the legacy default")
	}
}

func TestSupervisionBatchSettingsZeroReleasesHeldDigest(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	shortSupervise(t, s)
	setSettings(t, s, func(v *Settings) { v.SupervisionBatchMinutes = ptr(60) })
	routine := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "release-on-zero", Minutes: 50}
	s.queueSignal(routine)
	waitFor(t, "a held batch", func() bool {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		return !s.supervise.pending[routine.id()].batchUntil.IsZero()
	})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(`{"supervision_batch_minutes":0}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable batching: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "the held digest after restoring the legacy cadence", func() bool { return len(assistantSendsTo(f, "conv1")) == 1 })
}
