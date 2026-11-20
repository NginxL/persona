package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskExecutionFoldedAliasKeepsModelEffortChecks(t *testing.T) {
	setup := func(t *testing.T) (*Service, *fakeHub) {
		t.Helper()
		f := newFakeHub().withMachine()
		f.agentModels = map[string][]proto.AgentModel{"claude": {
			{ID: "test-model", Efforts: []string{"low"}},
			{ID: "test-alias", Resolved: "test-model", Efforts: []string{"low"}},
		}}
		s := newTestService(t, f)
		catalog, err := s.askLauncherModels(context.Background(), "server", "claude", false)
		if err != nil || len(catalog.Models) != 1 || !slices.Contains(catalog.Models[0].Aliases, "test-alias") {
			t.Fatalf("alias was not folded into its model: %+v %v", catalog, err)
		}
		return s, f
	}

	t.Run("save", func(t *testing.T) {
		s, f := setup(t)
		body := `{"title":"Check","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"model":"test-alias","effort":"high"}`
		if r := request(s, "POST", "/api/v1/assistant/tasks", body); r.Code != http.StatusBadRequest {
			t.Fatalf("unsupported alias effort saved: %d %s", r.Code, r.Body.String())
		}
		if f.sent() != 0 {
			t.Fatal("validation dispatched a model turn")
		}
	})

	t.Run("stored run", func(t *testing.T) {
		s, f := setup(t)
		setTasks(s, Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
		setModel(t, s, "task.job", ModelBinding{Provider: "launcher.claude", Model: "test-alias", Effort: "high"})
		before := f.sent()
		if _, err := runNow(t, s, "job"); err == nil {
			t.Fatal("stored unsupported alias effort ran")
		}
		if f.sent() != before {
			t.Fatal("unsupported stored choice dispatched a model turn")
		}
	})

	t.Run("supported choice", func(t *testing.T) {
		s, f := setup(t)
		body := `{"title":"Check","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"model":"test-alias","effort":"low"}`
		r := request(s, "POST", "/api/v1/assistant/tasks", body)
		var saved struct{ Task Task }
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &saved) != nil {
			t.Fatalf("supported alias save: %d %s", r.Code, r.Body.String())
		}
		if _, err := runNow(t, s, saved.Task.ID); err != nil {
			t.Fatalf("supported alias run: %v", err)
		}
		if got := f.lastSend(t); got["model"] != "test-alias" || got["effort"] != "low" {
			t.Fatalf("saved choice changed during dispatch: %v", got)
		}
	})
}
