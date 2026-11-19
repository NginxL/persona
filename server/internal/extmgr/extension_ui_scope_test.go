package extmgr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

// Two independently installed programs can declare the same pages without sharing routes or switches.
func TestExtensionUIIDsAreLocal(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux, token := h.routes(), h.cfg.Token
	for _, id := range []string{"example-ui", "second-ui"} {
		dir := uiPackage(t)
		if id != "example-ui" {
			path := filepath.Join(dir, extension.ManifestFile)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			m["id"] = id
			delete(m["contributes"].(map[string]any), "events") // Other contribution points stay global.
			raw, _ = json.Marshal(m)
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			path = filepath.Join(dir, "bin", "ui")
			raw, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			raw = []byte(strings.Replace(string(raw), "PERSONA_TEST_PLUGIN=ui", "PERSONA_TEST_PLUGIN=ui EXAMPLE_UI_ID="+id, 1))
			if err := os.WriteFile(path, raw, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		code, preview := call(t, mux, "POST", "/api/v1/extensions/-/preview", token, fmt.Sprintf(`{"source":%q}`, "dir:"+dir))
		if code != http.StatusOK {
			t.Fatalf("%s preview: %d %v", id, code, preview)
		}
		code, installed := call(t, mux, "POST", "/api/v1/extensions/-/install", token, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"]))
		if code < 200 || code >= 300 {
			t.Fatalf("%s install: %d %v", id, code, installed)
		}
		waitLong(t, id+" ready", func() bool {
			p := h.extensions.get(id)
			return p != nil && p.proc != nil && p.proc.Ready()
		})
	}
	_, listed := call(t, mux, "GET", "/api/v1/extensions/-/ui", token, "")
	seen := map[string]bool{}
	for _, row := range listed["ui"].([]any) {
		p := row.(map[string]any)
		if p["id"] == "home" {
			seen[fmt.Sprint(p["extension"])] = true
		}
	}
	if !seen["example-ui"] || !seen["second-ui"] {
		t.Fatalf("both home pages must be discoverable: %v", listed)
	}
	check := func(id, title string) {
		t.Helper()
		code, rendered := call(t, mux, "POST", "/api/v1/extensions/"+id+"/-/ui/home", token, `{}`)
		view, _ := rendered["view"].(map[string]any)
		if code != http.StatusOK || view["title"] != title {
			t.Fatalf("%s route reached the wrong program: %d %v", id, code, rendered)
		}
	}
	check("example-ui", "示例")
	check("second-ui", "second-ui")
	if err := h.extensions.reg.SetContribution(t.Context(), "second-ui", "ui", "home", false, "test"); err != nil {
		t.Fatal(err)
	}
	if code, out := call(t, mux, "POST", "/api/v1/extensions/second-ui/-/ui/home", token, `{}`); code != http.StatusBadGateway || out["code"] != "plugin_failed" {
		t.Fatalf("disabled second-ui/home should be absent: %d %v", code, out)
	}
	check("example-ui", "示例")
}
