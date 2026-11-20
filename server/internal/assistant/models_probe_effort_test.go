package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestProviderTestInheritsSavedEffortWithoutSaving(t *testing.T) {
	for _, api := range []string{"responses", "messages", "chat"} {
		t.Run(api, func(t *testing.T) {
			s, mux := launcherTestService(t)
			var body map[string]any
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body = nil
				_ = json.NewDecoder(r.Body).Decode(&body)
				if api == "responses" {
					_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
				} else if api == "chat" {
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\n"))
				} else {
					_, _ = w.Write([]byte(`{"type":"message","content":[]}`))
				}
			}))
			defer gateway.Close()
			saved, _ := json.Marshal(map[string]any{
				"name": "gateway", "kind": "api", "api": api, "model": "saved-model",
				"base_url": gateway.URL, "api_key": fakeKey, "effort": "high",
			})
			if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers", string(saved)); code != http.StatusOK {
				t.Fatal(raw)
			}
			if r := modelRequest(s, "PATCH", "kb", `{"revision":0,"provider":"launcher.gateway","effort":"low"}`); r.Code != http.StatusOK {
				t.Fatal(r.Body.String())
			}
			_, before := launcherCall(t, mux, "GET", "/api/v1/model-config", "")
			for _, tc := range []struct {
				name, want string
				explicit   bool
				effort     any
			}{
				{name: "default", want: "high"},
				{name: "null", explicit: true, effort: nil, want: "high"},
				{name: "override", explicit: true, effort: "max", want: "max"},
				{name: "off", explicit: true, effort: "off", want: "off"},
				{name: "cleared-provider-draft", explicit: true, effort: "", want: ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					draft := map[string]any{"name": "gateway", "model": "draft-model"}
					if tc.explicit {
						draft["effort"] = tc.effort
					}
					raw, _ := json.Marshal(draft)
					result := probeResult(t, mux, "/api/v1/launchers/test", string(raw))
					if result["ok"] != true || result["effort"] != tc.want || result["model"] != "draft-model" {
						t.Errorf("wrong tested configuration: %v, want effort %q", result, tc.want)
					}
					want := map[string]any{}
					ai.ApplyReasoningEffort(want, api, "draft-model", tc.want)
					for _, field := range []string{"reasoning", "reasoning_effort", "thinking", "output_config"} {
						if !reflect.DeepEqual(body[field], want[field]) {
							t.Errorf("payload %s = %v, want %v", field, body[field], want[field])
						}
					}
				})
			}
			for _, tc := range []struct{ effort, want string }{
				{"", "high"}, {"max", "max"}, {"off", "off"},
			} {
				c, err := s.apiConfig(context.Background(), ModelBinding{Provider: "launcher.gateway", Effort: tc.effort})
				if err != nil || c.ReasoningEffort != tc.want {
					t.Errorf("runtime effort %q: got %q, %v; want %q", tc.effort, c.ReasoningEffort, err, tc.want)
				}
			}
			_, after := launcherCall(t, mux, "GET", "/api/v1/model-config", "")
			if before != after {
				t.Fatal("a connection test changed purpose or provider configuration")
			}
		})
	}
}

func TestProviderTestUsesDraftModelAndEffortWithoutSaving(t *testing.T) {
	for _, tc := range []struct {
		api, model, effort, field string
		want                      any
	}{
		{"responses", "draft-model", "max", "reasoning", map[string]any{"effort": "max"}},
		{"responses", "draft-model", "off", "reasoning", map[string]any{"effort": "none"}},
		{"chat", "draft-model", "max", "reasoning_effort", "max"},
		{"chat", "draft-model", "off", "reasoning_effort", "none"},
		{"messages", "draft-model", "high", "output_config", map[string]any{"effort": "high"}},
		{"messages", "draft-model", "off", "thinking", map[string]any{"type": "disabled"}},
		{"messages", "claude-sonnet-5-5", "off", "thinking", map[string]any{"type": "between_tools"}},
		{"messages", "claude-opus-5-5", "off", "output_config", map[string]any{"effort": "low"}},
	} {
		t.Run(tc.api+"/"+tc.model+"/"+tc.effort, func(t *testing.T) {
			_, mux := launcherTestService(t)
			var body map[string]any
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&body)
				if tc.api == "responses" {
					_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
				} else if tc.api == "chat" {
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\n"))
				} else {
					_, _ = w.Write([]byte(`{"type":"message","content":[]}`))
				}
			}))
			defer gateway.Close()
			saved, _ := json.Marshal(map[string]any{"name": "gateway", "kind": "api", "api": tc.api, "model": "saved-model", "base_url": gateway.URL, "api_key": fakeKey})
			if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers", string(saved)); code != http.StatusOK {
				t.Fatal(raw)
			}
			_, before := launcherCall(t, mux, "GET", "/api/v1/model-config", "")
			draft, _ := json.Marshal(map[string]any{"name": "gateway", "model": tc.model, "effort": tc.effort})
			result := probeResult(t, mux, "/api/v1/launchers/test", string(draft))
			if result["ok"] != true || result["model"] != tc.model || result["effort"] != tc.effort || result["ms"] == nil {
				t.Fatalf("draft combination was not verified: %v", result)
			}
			if body["model"] != tc.model || !reflect.DeepEqual(body[tc.field], tc.want) {
				t.Fatalf("draft not sent to provider: model=%v %s=%v", body["model"], tc.field, body[tc.field])
			}
			_, after := launcherCall(t, mux, "GET", "/api/v1/model-config", "")
			if before != after {
				t.Fatal("a draft connection test changed model configuration")
			}
			stored := probeResult(t, mux, "/api/v1/launchers/test", `{"name":"gateway"}`)
			if stored["model"] != "saved-model" || stored["effort"] != "" {
				t.Fatalf("draft test changed provider defaults: %v", stored)
			}
		})
	}
}

func TestProviderTestCannotVerifyUnsupportedEffort(t *testing.T) {
	_, mux := launcherTestService(t)
	calls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer gateway.Close()
	draft, _ := json.Marshal(map[string]any{"api": "messages", "base_url": gateway.URL, "api_key": fakeKey, "model": "m", "effort": "not-an-effort"})
	result := probeResult(t, mux, "/api/v1/launchers/test", string(draft))
	if result["ok"] != false || result["code"] != "unsupported_effort" || result["effort"] != nil {
		t.Fatalf("unsupported combination marked verified: %v", result)
	}
	if calls != 0 {
		t.Fatalf("unsupported effort reached the provider: %d requests", calls)
	}
}

func TestChatProbeReasoningEffortRetry(t *testing.T) {
	for _, tc := range []struct {
		name, rejection       string
		rejections, wantCalls int
		ok                    bool
		wantLogs              int
	}{
		{"field rejected", `{"error":{"message":"Unsupported parameter: 'reasoning_effort'"}}`, 1, 2, true, 1},
		{"field rejected twice", `{"error":{"message":"Unsupported parameter: 'reasoning_effort'"}}`, 2, 2, false, 1},
		{"value rejected", `{"error":{"message":"unsupported reasoning_effort \"high\""}}`, 1, 1, false, 0},
		{"ceiling rejected", `{"error":{"code":"reasoning_effort_above_ceiling","message":"reasoning_effort high exceeds the ceiling"}}`, 1, 1, false, 0},
		{"ceiling code with generic message", `{"error":{"code":"reasoning_effort_above_ceiling","message":"Unsupported parameter","param":"reasoning_effort"}}`, 1, 1, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []map[string]any
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				requests = append(requests, body)
				if len(requests) <= tc.rejections {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(tc.rejection))
					return
				}
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\n"))
			}))
			defer gateway.Close()
			s, _ := launcherTestService(t)
			var logs []string
			s.logf = func(format string, args ...any) { logs = append(logs, format) }
			effort := "high"
			out := s.providerTest(context.Background(), probeInput{BaseURL: gateway.URL, Model: "m", API: "chat", Effort: &effort})
			if len(requests) != tc.wantCalls || out["ok"] != tc.ok || len(logs) != tc.wantLogs {
				t.Fatalf("requests=%d out=%v logs=%q", len(requests), out, logs)
			}
			if tc.ok && tc.wantLogs == 1 && out["effort"] != "" {
				t.Fatalf("degraded probe still verified effort: %v", out)
			}
			if requests[0]["reasoning_effort"] != "high" {
				t.Fatalf("first request: %v", requests[0])
			}
			if tc.wantCalls == 2 {
				if _, present := requests[1]["reasoning_effort"]; present {
					t.Fatalf("retry kept the rejected field: %v", requests[1])
				}
			}
			if tc.wantLogs == 1 && (!strings.Contains(logs[0], "reasoning_effort") || !strings.Contains(logs[0], "without")) {
				t.Fatalf("degradation log = %q", logs)
			}
		})
	}
}

func TestChatEffortUsesDeclaredModelCapabilities(t *testing.T) {
	s, mux := launcherTestService(t)
	var body map[string]any
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer gateway.Close()
	saved, _ := json.Marshal(map[string]any{
		"name": "chat", "kind": "api", "api": "chat", "base_url": gateway.URL, "model": "limited", "effort": "high",
		"models": []proto.ProviderModel{{ID: "limited", Efforts: []string{"low"}}, {ID: "unknown"}, {ID: "none", NoEffort: true},
			{ID: "direct-off", Efforts: []string{"low"}, AllEfforts: []string{"none", "low"}}},
	})
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers", string(saved)); code != http.StatusOK {
		t.Fatal(raw)
	}
	for _, tc := range []struct {
		name, model, effort, resolved, wire string
	}{
		{name: "declared list excludes level", model: "limited", effort: "high"},
		{name: "declared list includes level", model: "limited", effort: "low", resolved: "low", wire: "low"},
		{name: "off is a separate control", model: "limited", effort: "off", resolved: "off", wire: "none"},
		{name: "empty list is unknown", model: "unknown", effort: "high", resolved: "high", wire: "high"},
		{name: "unlisted model is unknown", model: "unlisted", effort: "max", resolved: "max", wire: "max"},
		{name: "explicit no effort suppresses a level", model: "none", effort: "high"},
		{name: "explicit no effort suppresses off", model: "none", effort: "off"},
		{name: "direct levels include none", model: "direct-off", effort: "off", resolved: "off", wire: "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := s.apiConfig(context.Background(), ModelBinding{Provider: "launcher.chat", Model: tc.model, Effort: tc.effort})
			if err != nil || config.ReasoningEffort != tc.resolved {
				t.Fatalf("resolved effort = %q, %v; want %q", config.ReasoningEffort, err, tc.resolved)
			}
			effort := tc.effort
			out := s.providerTest(context.Background(), probeInput{Name: "chat", Model: tc.model, Effort: &effort})
			if out["ok"] != true || out["effort"] != tc.resolved && (tc.resolved != "" || out["effort"] != nil) {
				t.Fatalf("probe = %v, want resolved effort %q", out, tc.resolved)
			}
			if tc.wire == "" {
				if _, present := body["reasoning_effort"]; present {
					t.Fatalf("unsupported effort was sent: %v", body)
				}
			} else if body["reasoning_effort"] != tc.wire {
				t.Fatalf("wire effort = %v, want %q", body["reasoning_effort"], tc.wire)
			}
		})
	}
}

func TestChatProbeResolvesLegacyTierAliasCapabilities(t *testing.T) {
	s, _ := launcherTestService(t)
	var body map[string]any
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer gateway.Close()
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{
		Name: "old", Kind: "api", API: "chat", BaseURL: gateway.URL, Model: "sonnet", SonnetModel: "limited",
		Models: []proto.ProviderModel{{ID: "limited", Efforts: []string{"low"}, AllEfforts: []string{"none", "low"}}},
	}})
	for _, tc := range []struct{ effort, resolved, wire string }{
		{effort: "high"},
		{effort: "low", resolved: "low", wire: "low"},
	} {
		t.Run(tc.effort, func(t *testing.T) {
			config, err := s.apiConfig(context.Background(), ModelBinding{Provider: "launcher.old", Model: "Sonnet[1m]", Effort: tc.effort})
			if err != nil || config.Model != "limited" || config.ReasoningEffort != tc.resolved {
				t.Fatalf("runtime = %+v, %v; want model limited and effort %q", config, err, tc.resolved)
			}
			effort := tc.effort
			out := s.providerTest(context.Background(), probeInput{Name: "old", Model: "Sonnet[1m]", Effort: &effort})
			if out["ok"] != true || out["model"] != "limited" || out["effort"] != tc.resolved && (tc.resolved != "" || out["effort"] != nil) {
				t.Fatalf("probe = %v, want model limited and effort %q", out, tc.resolved)
			}
			if body["model"] != "limited" || body["reasoning_effort"] != tc.wire && (tc.wire != "" || body["reasoning_effort"] != nil) {
				t.Fatalf("wire request = %v, want model limited and effort %q", body, tc.wire)
			}
		})
	}
}

func TestChatDiscoveryDoesNotResolveUnusedDefaultAlias(t *testing.T) {
	s, _ := launcherTestService(t)
	var modelLists int
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			modelLists++
			_, _ = w.Write([]byte(`{"data":[{"id":"concrete"}]}`))
		case "/v1/model-capabilities":
			http.NotFound(w, r)
		default:
			t.Errorf("request = %s %s, want model discovery", r.Method, r.URL.Path)
		}
	}))
	defer gateway.Close()
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{
		Name: "old", Kind: "api", API: "chat", BaseURL: gateway.URL, Model: "sonnet",
	}})
	out := s.providerDiscover(context.Background(), probeInput{Name: "old"})
	if out["error"] != nil || modelLists != 1 {
		t.Fatalf("discovery = %v, model lists = %d; want the gateway model list", out, modelLists)
	}
	probe := s.providerTest(context.Background(), probeInput{Name: "old"})
	if probe["ok"] != false || probe["error"] == nil || modelLists != 1 {
		t.Fatalf("unresolved named probe = %v, model lists = %d; want a local alias error", probe, modelLists)
	}
}

func TestChatDefaultTriageOffValueFallbackAndCache(t *testing.T) {
	var requests []map[string]any
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, body)
		if body["reasoning_effort"] == "none" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"unsupported_value","message":"Unsupported value: 'none'. Supported values are: 'minimal', 'low', 'medium', 'high'.","param":"reasoning_effort"}}`))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer gateway.Close()
	s, mux := launcherTestService(t)
	create := `{"name":"chat-triage","kind":"api","api":"chat","base_url":` + strconv.Quote(gateway.URL) + `,"model":"m","api_key":` + strconv.Quote(fakeKey) + `}`
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers", create); code != http.StatusOK {
		t.Fatal(raw)
	}
	binding, err := s.resolveModel(context.Background(), "inbox.triage")
	if err != nil || binding.Provider != "launcher.chat-triage" || binding.Effort != "off" {
		t.Fatalf("default triage binding = %+v, %v", binding, err)
	}
	config, err := s.apiConfig(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	client := ai.New(config)
	for i := 0; i < 2; i++ {
		if _, err := client.Chat(context.Background(), ai.Request{Messages: []ai.Message{{Role: ai.RoleUser, Content: "ping"}}}); err != nil {
			t.Fatal(err)
		}
	}
	config.ReasoningEffort = "high"
	if _, err := ai.New(config).Chat(context.Background(), ai.Request{Messages: []ai.Message{{Role: ai.RoleUser, Content: "ping"}}}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 4 {
		t.Fatalf("requests = %d, want 4: %v", len(requests), requests)
	}
	if requests[0]["reasoning_effort"] != "none" || requests[3]["reasoning_effort"] != "high" {
		t.Fatalf("explicit levels were not preserved: %v", requests)
	}
	for _, i := range []int{1, 2} {
		if _, present := requests[i]["reasoning_effort"]; present {
			t.Fatalf("request %d should omit the rejected none field: %v", i+1, requests[i])
		}
	}
}

func TestChatProbeOffValueRetryAndCache(t *testing.T) {
	var requests []map[string]any
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, body)
		if body["reasoning_effort"] == "none" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"unsupported_value","message":"Unsupported value: 'none'. Supported values are: 'minimal', 'low', 'medium', 'high'.","param":"reasoning_effort"}}`))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer gateway.Close()
	s, _ := launcherTestService(t)
	for i := 0; i < 2; i++ {
		effort := "off"
		out := s.providerTest(context.Background(), probeInput{BaseURL: gateway.URL, APIKey: fakeKey, Model: "m", API: "chat", Effort: &effort})
		if out["ok"] != true || out["effort"] != "" {
			t.Fatalf("degraded off probe = %v", out)
		}
	}
	effort := "high"
	out := s.providerTest(context.Background(), probeInput{BaseURL: gateway.URL, APIKey: fakeKey, Model: "m", API: "chat", Effort: &effort})
	if out["ok"] != true || out["effort"] != "high" {
		t.Fatalf("explicit high probe = %v", out)
	}
	if len(requests) != 4 || requests[0]["reasoning_effort"] != "none" || requests[3]["reasoning_effort"] != "high" {
		t.Fatalf("probe requests = %v", requests)
	}
	for _, i := range []int{1, 2} {
		if _, present := requests[i]["reasoning_effort"]; present {
			t.Fatalf("probe request %d should omit none: %v", i+1, requests[i])
		}
	}
}
