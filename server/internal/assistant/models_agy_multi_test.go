package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestCustomAntigravityRuntimeLauncherViewsAndDetails(t *testing.T) {
	s, f, _ := modelService(t)
	f.mu.Lock()
	f.launcherList = []proto.Launcher{
		{Name: "agy", Source: "agy", Agent: "agy", Model: "gemini-4-neon-high", Assistant: true},
		{Name: "agy-wrap", Source: "agy-wrap", Agent: "agy", DisplayName: "Antigravity Wrapped", Model: "gemini-4-neon-high", Assistant: true},
	}
	f.agentModels = map[string][]proto.AgentModel{
		"agy-wrap": {
			{ID: "gemini-4-neon-high", Name: "Gemini 4 Neon (High)", Default: true},
			{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash"},
		},
	}
	f.mu.Unlock()

	// Listed in Settings > Model Providers with its display name preserved.
	listRes, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{
		Method: "GET", Path: "/api/v1/launchers",
	}, s.lang)
	if err != nil || listRes.Status != http.StatusOK {
		t.Fatalf("GET /api/v1/launchers: %d %s %v", listRes.Status, listRes.Body, err)
	}
	var listed struct {
		Launchers []LauncherView `json:"launchers"`
	}
	if err := json.Unmarshal(listRes.Body, &listed); err != nil {
		t.Fatal(err)
	}
	var found *LauncherView
	for i := range listed.Launchers {
		if listed.Launchers[i].Name == "agy-wrap" {
			found = &listed.Launchers[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected agy-wrap in /api/v1/launchers, got %+v", listed.Launchers)
	}
	if found.DisplayName != "Antigravity Wrapped" || found.Source != "agy-wrap" || found.Agent != "agy" {
		t.Fatalf("unexpected LauncherView: %+v", *found)
	}

	// Detail route populates Antigravity ACP details and models for custom runtime sources.
	detailRes, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{
		Method: "GET", Path: "/api/v1/launchers/agy-wrap",
	}, s.lang)
	if err != nil || detailRes.Status != http.StatusOK {
		t.Fatalf("GET /api/v1/launchers/agy-wrap: %d %s %v", detailRes.Status, detailRes.Body, err)
	}
	var detail struct {
		Launcher LauncherView `json:"launcher"`
	}
	if err := json.Unmarshal(detailRes.Body, &detail); err != nil {
		t.Fatal(err)
	}
	md, ok := detail.Launcher.Details["server"]
	if !ok {
		t.Fatalf("expected server detail for custom Antigravity runtime, got %+v", detail.Launcher.Details)
	}
	if md.Agent != "agy" || md.Auth != "Antigravity ACP (agy-wrap)" || len(md.Models) != 2 {
		t.Fatalf("unexpected custom Antigravity detail: %+v", md)
	}

	// Main chat picker offers both the default agy and the custom agy-wrap companion.
	pickerRes, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{
		Method: "GET", Path: "/api/v1/machines/server/launchers?purpose=assistant",
	}, s.lang)
	if err != nil || pickerRes.Status != http.StatusOK {
		t.Fatalf("GET assistant picker: %d %s %v", pickerRes.Status, pickerRes.Body, err)
	}
	var picker struct {
		Launchers []ConfiguredLauncher `json:"launchers"`
	}
	if err := json.Unmarshal(pickerRes.Body, &picker); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range picker.Launchers {
		seen[l.Name] = true
	}
	if !seen["agy"] || !seen["agy-wrap"] {
		t.Fatalf("expected both agy and agy-wrap in assistant picker, got %+v", picker.Launchers)
	}
}
