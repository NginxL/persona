package assistant

import (
	"encoding/json"
	"testing"
)

// The externally visible contract works without a browser's legacy storage.
func TestAgentsDisplayRouteContract(t *testing.T) {
	s, _ := navigationService(t, newSettingsBoardStore())
	w := boardCall(s, "GET", "/api/v1/agents/display-settings", "", roleAnswer)
	if w.Code != 200 {
		t.Fatalf("authenticated display read = %d, want 200: %s", w.Code, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["show_botmux"] != true || out["configured"] != false {
		t.Fatalf("unset = %v", out)
	}
	w = boardCall(s, "PUT", "/api/v1/agents/display-settings", `{"show_botmux":false}`, roleFull)
	if w.Code != 200 {
		t.Fatalf("full display write = %d: %s", w.Code, w.Body)
	}
	w = boardCall(s, "GET", "/api/v1/agents/display-settings", "", roleAnswer)
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out["show_botmux"] != false || out["configured"] != true {
		t.Fatalf("saved = %d %v", w.Code, out)
	}
}
