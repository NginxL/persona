package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

var modelGroupSettingKeys = []string{
	"model_config.kb",
	"model_config.inbox.triage",
	"model_config.agent.delegate",
	"model_config.translate",
}

func modelGroupArgs(t *testing.T, action, key string, value any) json.RawMessage {
	t.Helper()
	args := map[string]any{"action": action, "key": key}
	if value != nil {
		args["value"] = value
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func modelGroupResultText(r proto.ToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}

func putModelGroupProviders(t *testing.T, s *Service, baseURL string) string {
	t.Helper()
	key := sealedKey(t, s, "fake-model-group-credential")
	putLaunchers(t, s,
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "old-provider", Kind: "api", API: "messages", BaseURL: baseURL, Model: "old-default"}, KeySealed: key},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "new-provider", Kind: "api", API: "messages", BaseURL: baseURL, Model: "new-default"}, KeySealed: key},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "incompatible", Kind: "launcher", Agent: "unsupported", API: "responses"}, KeySealed: key},
	)
	return key
}

func TestModelGroupSettingsListAndGet(t *testing.T) {
	s, _, _ := modelService(t)
	key := putModelGroupProviders(t, s, "https://provider.invalid")
	setModel(t, s, "kb", ModelBinding{Provider: "launcher.old-provider", Model: "old-model", Effort: "low"})

	listed := s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "server", Arguments: modelGroupArgs(t, "list", "", nil)})
	if listed.IsError {
		t.Fatal(modelGroupResultText(listed))
	}
	listText := modelGroupResultText(listed)
	for _, settingKey := range modelGroupSettingKeys {
		if !strings.Contains(listText, settingKey) {
			t.Errorf("list does not expose %s", settingKey)
		}
	}

	got := s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "server", Arguments: modelGroupArgs(t, "get", "model_config.kb", nil)})
	if got.IsError {
		t.Fatal(modelGroupResultText(got))
	}
	text := modelGroupResultText(got)
	var out struct {
		Key       string          `json:"key"`
		Purpose   ModelPurpose    `json:"purpose"`
		Providers []ModelProvider `json:"providers"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("get is not a model-group view: %v; %s", err, text)
	}
	if out.Key != "model_config.kb" || out.Purpose.Effective.Provider != "launcher.old-provider" || out.Purpose.Effective.Model != "old-model" || out.Purpose.Effective.Effort != "low" || out.Purpose.Revision != 1 {
		t.Fatalf("effective binding was not read back: %+v", out)
	}
	for _, provider := range []string{"launcher.old-provider", "launcher.new-provider"} {
		if !slices.Contains(out.Purpose.ProviderIDs, provider) {
			t.Errorf("valid configured provider %s missing from %+v", provider, out.Purpose.ProviderIDs)
		}
	}
	if slices.Contains(out.Purpose.ProviderIDs, "launcher.incompatible") {
		t.Fatalf("incompatible provider offered for a background API purpose: %+v", out.Purpose.ProviderIDs)
	}
	if strings.Contains(text, "fake-model-group-credential") || strings.Contains(text, key) {
		t.Fatal("get returned a credential or its sealed representation")
	}
}

func TestModelGroupProviderSetRequiresApproval(t *testing.T) {
	s, _, _ := modelService(t)
	putModelGroupProviders(t, s, "https://provider.invalid")
	setModel(t, s, "kb", ModelBinding{Provider: "launcher.old-provider", Model: "old-model", Effort: "low"})
	setModel(t, s, "translate", ModelBinding{Provider: "launcher.old-provider", Model: "keep-model"})
	before := modelRecordOf(t, s, "kb")
	unrelated := modelRecordOf(t, s, "translate")
	args := modelGroupArgs(t, "set", "model_config.kb", "launcher.new-provider")

	done := make(chan proto.ToolResult, 1)
	go func() {
		done <- s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "main-1", Arguments: args})
	}()
	card, ok := awaitCard(s)
	if !ok {
		t.Fatal("provider change did not wait for a phone approval card")
	}
	for _, want := range []string{"launcher.old-provider", "launcher.new-provider", "old-model", "new-default", "→"} {
		if !strings.Contains(card.Summary, want) {
			t.Errorf("approval card does not show %q: %s", want, card.Summary)
		}
	}
	if after := modelRecordOf(t, s, "kb"); after.Revision != before.Revision || after.Binding != before.Binding {
		t.Fatalf("model group changed before approval: before=%+v after=%+v", before, after)
	}
	allow := true
	if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: card.ID, Answer: standingJSON(t, proto.Resolution{Allow: &allow})}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.IsError {
			t.Fatalf("approved provider change failed: %s", modelGroupResultText(result))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approved provider change did not finish")
	}
	after := modelRecordOf(t, s, "kb")
	if after.Revision != before.Revision+1 || after.Binding.Provider != "launcher.new-provider" || after.Binding.Model != "" || after.Binding.Effort != "" {
		t.Fatalf("approved binding did not replace the provider and clear its old fields: %+v", after)
	}
	if got := modelRecordOf(t, s, "translate"); got.Revision != unrelated.Revision || got.Binding != unrelated.Binding {
		t.Fatalf("an unrelated model group changed: before=%+v after=%+v", unrelated, got)
	}
	readback := s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "server", Arguments: modelGroupArgs(t, "get", "model_config.kb", nil)})
	if readback.IsError || !strings.Contains(modelGroupResultText(readback), `"provider":"launcher.new-provider"`) || !strings.Contains(modelGroupResultText(readback), `"model":"new-default"`) {
		t.Fatalf("get did not read back the effective provider default: %s", modelGroupResultText(readback))
	}
}

func TestModelGroupProviderRejectsUnknownAndIncompatible(t *testing.T) {
	for _, provider := range []string{"launcher.missing", "launcher.incompatible"} {
		t.Run(provider, func(t *testing.T) {
			s, _, _ := modelService(t)
			putModelGroupProviders(t, s, "https://provider.invalid")
			setModel(t, s, "kb", ModelBinding{Provider: "launcher.old-provider", Model: "old-model"})
			before := modelRecordOf(t, s, "kb")
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			result := s.Tool(ctx, ToolCall{Name: "persona_settings", MachineID: "server", Arguments: modelGroupArgs(t, "set", "model_config.kb", provider)})
			text := modelGroupResultText(result)
			if !result.IsError {
				t.Fatalf("invalid provider was accepted: %s", text)
			}
			for _, valid := range []string{"launcher.old-provider", "launcher.new-provider"} {
				if !strings.Contains(text, valid) {
					t.Errorf("rejection does not list valid provider %s: %s", valid, text)
				}
			}
			if pending := s.pendingQuestions(); len(pending) != 0 {
				t.Fatalf("invalid provider created an approval card: %+v", pending)
			}
			if after := modelRecordOf(t, s, "kb"); after.Revision != before.Revision || after.Binding != before.Binding {
				t.Fatalf("invalid provider changed the model group: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestModelGroupConnectionTestDoesNotSave(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		ok     bool
	}{{"success", http.StatusOK, true}, {"failure", http.StatusUnauthorized, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("x-api-key") != "fake-model-group-credential" {
					t.Error("provider check did not use its configured test credential")
				}
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": "connection ok"}}})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "synthetic connection failure"}})
			}))
			defer srv.Close()
			s, _, _ := modelService(t)
			key := putModelGroupProviders(t, s, srv.URL)
			setModel(t, s, "kb", ModelBinding{Provider: "launcher.old-provider", Model: "saved-model", Effort: "low"})
			before := modelRecordOf(t, s, "kb")
			result := s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "server", Arguments: modelGroupArgs(t, "test", "model_config.kb", "launcher.new-provider")})
			text := modelGroupResultText(result)
			if result.IsError {
				t.Fatalf("connection test could not return its result: %s", text)
			}
			var out struct {
				Key      string              `json:"key"`
				Provider string              `json:"provider"`
				Check    proto.LauncherCheck `json:"check"`
			}
			if err := json.Unmarshal([]byte(text), &out); err != nil {
				t.Fatalf("connection test is not structured: %v; %s", err, text)
			}
			if out.Key != "model_config.kb" || out.Provider != "launcher.new-provider" || out.Check.OK != tc.ok || out.Check.Model != "new-default" {
				t.Fatalf("unexpected connection result: %+v", out)
			}
			if tc.ok && !strings.Contains(out.Check.Answer, "connection ok") {
				t.Fatalf("successful answer missing: %+v", out.Check)
			}
			if !tc.ok && !strings.Contains(out.Check.Error, "synthetic connection failure") {
				t.Fatalf("failure detail missing: %+v", out.Check)
			}
			if calls.Load() != 1 {
				t.Fatalf("connection test made %d requests, want 1", calls.Load())
			}
			if pending := s.pendingQuestions(); len(pending) != 0 {
				t.Fatalf("read-only connection test created an approval card: %+v", pending)
			}
			if after := modelRecordOf(t, s, "kb"); after.Revision != before.Revision || after.Binding != before.Binding {
				t.Fatalf("connection test saved a model group: before=%+v after=%+v", before, after)
			}
			if strings.Contains(text, "fake-model-group-credential") || strings.Contains(text, key) {
				t.Fatal("connection test returned a credential or its sealed representation")
			}
		})
	}
}

func TestModelGroupLauncherConnectionTest(t *testing.T) {
	s, f, _ := modelService(t)
	putModelGroupProviders(t, s, "https://provider.invalid")
	setModel(t, s, "kb", ModelBinding{Provider: "launcher.old-provider", Model: "saved-model"})
	before := modelRecordOf(t, s, "kb")
	result := s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "server", Arguments: modelGroupArgs(t, "test", "model_config.kb", "launcher.claude")})
	text := modelGroupResultText(result)
	if result.IsError {
		t.Fatalf("launcher connection test failed: %s", text)
	}
	var out struct {
		Machine string              `json:"machine"`
		Check   proto.LauncherCheck `json:"check"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil || out.Machine != "server" || !out.Check.OK {
		t.Fatalf("unexpected launcher connection result: %+v, %v", out, err)
	}
	f.mu.Lock()
	checked := append([]string(nil), f.checked...)
	f.mu.Unlock()
	if !slices.Equal(checked, []string{"server/claude"}) {
		t.Fatalf("existing node launcher check was not reused: %v", checked)
	}
	if pending := s.pendingQuestions(); len(pending) != 0 {
		t.Fatalf("read-only launcher test created a card: %+v", pending)
	}
	if after := modelRecordOf(t, s, "kb"); after.Revision != before.Revision || after.Binding != before.Binding {
		t.Fatalf("launcher test saved a model group: before=%+v after=%+v", before, after)
	}
}

func TestModelGroupProviderApprovalRechecksRevision(t *testing.T) {
	s, _, _ := modelService(t)
	putModelGroupProviders(t, s, "https://provider.invalid")
	setModel(t, s, "kb", ModelBinding{Provider: "launcher.old-provider", Model: "old-model"})
	args := modelGroupArgs(t, "set", "model_config.kb", "launcher.new-provider")
	done := make(chan proto.ToolResult, 1)
	go func() {
		done <- s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "main-1", Arguments: args})
	}()
	card, ok := awaitCard(s)
	if !ok {
		t.Fatal("provider change did not wait for approval")
	}
	setModel(t, s, "kb", ModelBinding{Provider: "launcher.old-provider", Model: "concurrent-model"})
	concurrent := modelRecordOf(t, s, "kb")
	allow := true
	if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: card.ID, Answer: standingJSON(t, proto.Resolution{Allow: &allow})}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if !strings.Contains(modelGroupResultText(result), "Nothing was applied") {
			t.Fatalf("stale approval did not report its refusal: %s", modelGroupResultText(result))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale approval did not finish")
	}
	if after := modelRecordOf(t, s, "kb"); after.Revision != concurrent.Revision || after.Binding != concurrent.Binding {
		t.Fatalf("stale approval overwrote the concurrent edit: concurrent=%+v after=%+v", concurrent, after)
	}
}
