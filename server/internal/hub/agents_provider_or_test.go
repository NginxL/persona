package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentsProviderORCapabilityAdvertisement(t *testing.T) {
	const capability = "agents_provider_or"
	h := &Hub{version: "test-version", cfg: &Config{Name: "home"}}
	if !slices.Contains(h.features(), capability) || slices.Contains(proto.Features, capability) {
		t.Fatalf("personal-Agent provider OR needs a dedicated hub capability, not a node capability: %v", h.features())
	}
	if hello, _ := h.hello()["features"].([]string); !slices.Contains(hello, capability) {
		t.Fatalf("hello does not advertise %q: %v", capability, hello)
	}
	for _, from := range []source{sourceLoopback, sourceLAN} {
		recorder := httptest.NewRecorder()
		h.healthWithSource(recorder, httptest.NewRequest("GET", "/api/v1/health", nil), func(*http.Request) source { return from })
		var health struct {
			Features []string `json:"features"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &health); err != nil || !slices.Contains(health.Features, capability) {
			t.Fatalf("trusted health from %v does not advertise %q: %v, %v", from, capability, health.Features, err)
		}
	}
}

func TestAgentsProviderORProtocolContract(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/protocol/protocol.md")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	start, end := -1, len(lines)
	for n, line := range lines {
		if strings.HasPrefix(line, "#### ") && strings.Contains(line, "agents_provider_or") {
			start = n
			continue
		}
		if start >= 0 && n > start && strings.HasPrefix(line, "#### ") {
			end = n
			break
		}
	}
	if start < 0 {
		t.Fatal("protocol has no dedicated personal-Agent provider OR section naming agents_provider_or")
	}
	section := strings.Join(lines[start:end], "\n")
	for _, required := range []string{
		"case-sensitive", "OR", "AND", "full-set", "counts", "cursor", "delta",
		"64", "empty", "invalid_query", "`provider`", "fallback",
	} {
		if !strings.Contains(section, required) {
			t.Errorf("provider OR protocol section does not define %q", required)
		}
	}
	if lower := strings.ToLower(section); !strings.Contains(lower, "old hub") {
		t.Error("provider OR protocol section does not define old-hub fallback")
	}
}

func TestRuntimeProviderSingletonAndRepetitionCompatibility(t *testing.T) {
	h := newTestHub(t)
	machine := newMachine(h, MachineConfig{ID: "demo"})
	machine.sessions = []proto.Session{
		{MachineID: "demo", SessionID: "lower", Agent: "provider-a", UpdatedAt: time.Unix(2, 0)},
		{MachineID: "demo", SessionID: "upper", Agent: "Provider-A", UpdatedAt: time.Unix(1, 0)},
	}
	h.machines, h.byID["demo"] = []*Machine{machine}, machine
	if page := runtimePage(t, h, "summary=1&provider=provider-a"); page.Total != 1 || len(page.Sessions) != 1 || page.Sessions[0].SessionID != "lower" {
		t.Fatalf("runtime scalar provider compatibility changed: %+v", page)
	}
	recorder := httptest.NewRecorder()
	h.listSessions(recorder, httptest.NewRequest("GET", "/api/v1/sessions?provider=provider-a", nil))
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), `"total"`) {
		t.Fatalf("provider alone opted runtime into paging: %d %s", recorder.Code, recorder.Body)
	}
	for _, query := range []string{
		"provider=provider-a&provider=provider-b&summary=1", // existing repetition remains invalid
		"provider=provider-a&provider=provider-b&counts=1",
	} {
		recorder = httptest.NewRecorder()
		h.listSessions(recorder, httptest.NewRequest("GET", "/api/v1/sessions?"+query, nil))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_query") {
			t.Errorf("%s: HTTP %d %s, want 400 invalid_query", query, recorder.Code, recorder.Body)
		}
	}
}

func TestAgentsProviderORWireGoldenCompatibility(t *testing.T) {
	const capability = "agents_provider_or"
	if slices.Contains(wireFeatures, capability) {
		t.Fatal("the fixed legacy hello/health wire golden feature list must not churn for this capability")
	}
	raw, err := os.ReadFile("../../../spec/fixtures/wire/connection.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples []wireSample
	if err := json.Unmarshal(raw, &samples); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hello_features", "health_features"} {
		found := false
		for _, sample := range samples {
			if sample.Name != name {
				continue
			}
			found = true
			var body struct {
				Features []string `json:"features"`
			}
			if err := json.Unmarshal(sample.Input, &body); err != nil || !slices.Equal(body.Features, wireFeatures) || slices.Contains(body.Features, capability) {
				t.Errorf("%s golden features changed: %v, %v", name, body.Features, err)
			}
		}
		if !found {
			t.Errorf("wire golden %s is missing", name)
		}
	}
}
