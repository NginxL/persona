package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// TestHelloAndHealthCarryTheHubsFeatures holds the real feature list, which the shared wire samples no longer carry
// (#11022): the hello and a trusted /health list exactly features(), proto.Features first and in their order, each
// flag once. Every flag the samples carry, but their unknown one, is one the hub lists.
func TestHelloAndHealthCarryTheHubsFeatures(t *testing.T) {
	h := &Hub{version: "test-version", cfg: &Config{Name: "home"}}
	want := h.features()
	if len(want) < len(proto.Features) || !slices.Equal(want[:len(proto.Features)], proto.Features) {
		t.Fatalf("features() does not start with proto.Features in order:\n%v\n%v", want, proto.Features)
	}
	seen := map[string]bool{}
	for _, f := range want {
		if seen[f] {
			t.Errorf("features() lists %q twice", f)
		}
		seen[f] = true
	}
	if got, _ := h.hello()["features"].([]string); !slices.Equal(got, want) {
		t.Fatalf("hello features:\n%v\nwant\n%v", got, want)
	}
	for _, from := range []source{sourceLoopback, sourceLAN} {
		w := httptest.NewRecorder()
		h.healthWithSource(w, httptest.NewRequest("GET", "/api/v1/health", nil), func(*http.Request) source { return from })
		var health struct {
			Features []string `json:"features"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil || !slices.Equal(health.Features, want) {
			t.Fatalf("health features from source %v (%v):\n%v\nwant\n%v", from, err, health.Features, want)
		}
	}
	for _, f := range wireFeatures {
		if listed := slices.Contains(want, f); listed != (f != wireUnknownFeature) {
			t.Errorf("wire samples' flag %q listed by the hub: %v", f, listed)
		}
	}
}
