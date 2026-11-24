package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installWaterImportPackage(t *testing.T, h *Manager, manifest string) {
	t.Helper()
	pv, err := h.extensions.reg.Preview(context.Background(), "dir:"+declarativePackage(t, manifest), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.extensions.reg.Install(context.Background(), pv.ID, pv.Digest, "synthetic test"); err != nil {
		t.Fatal(err)
	}
}

const waterImportManifest = `{"manifest_version":1,"id":"water","name":"Water","version":"0.4.0","configuration":{"type":"object","properties":{"goal_ml":{"type":"integer","default":2000}}},"contributes":{"providers":[{"id":"water-p","name":"Water P","base_url":"https://water.example.com"}]}}`

func TestHealthWaterImportBridge(t *testing.T) {
	h, store := extensionTestHub(t)
	if v, err := h.WaterExtension(context.Background(), true); err != nil || v != nil {
		t.Fatalf("absent: %+v %v", v, err)
	}
	installWaterImportPackage(t, h, waterImportManifest)
	if err := h.extensions.reg.SetConfig(context.Background(), "water", json.RawMessage(`{"goal_ml":2400}`), "synthetic test"); err != nil {
		t.Fatal(err)
	}
	dir := h.extensions.reg.DataDir("water")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "water.json")
	raw := []byte(`{"records":[{"id":"d1","at":"2024-01-01T09:00:00Z","ml":250}]}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	v, err := h.WaterExtension(context.Background(), true)
	if err != nil || v == nil || v.StorageKey != "water" || v.Version != "0.4.0" || !v.Enabled || !bytes.Equal(v.State, raw) || string(v.Config) != `{"goal_ml":2400}` {
		t.Fatalf("read: %+v %v", v, err)
	}
	metadata, err := h.WaterExtension(context.Background(), false)
	if err != nil || metadata.State != nil {
		t.Fatalf("home read the data file: %+v %v", metadata, err)
	}
	store.failPut = true
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/assistant/health/intake/import", nil)
	if h.DisableWaterExtension(w, r, "water") || w.Code != 409 || !h.extensions.reg.Snapshot("water").Record.Enabled {
		t.Fatalf("failed switch: %d %s", w.Code, w.Body)
	}
	store.failPut = false
	w = httptest.NewRecorder()
	if !h.DisableWaterExtension(w, r, "water") || h.extensions.reg.Snapshot("water").Record.Enabled {
		t.Fatalf("switch: %d %s", w.Code, w.Body)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, raw) {
		t.Fatalf("switch changed data: %s %v", after, err)
	}
	// A corrupt file is handed back unchanged for validation, never renamed/reset.
	if err := os.WriteFile(path, []byte(`bad`), 0600); err != nil {
		t.Fatal(err)
	}
	v, err = h.WaterExtension(context.Background(), true)
	files, e := os.ReadDir(dir)
	if err != nil || e != nil || string(v.State) != "bad" || len(files) != 1 {
		t.Fatalf("corrupt source mutated: %+v %v %v", v, err, e)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	v, err = h.WaterExtension(context.Background(), true)
	if err != nil || string(v.State) != `{}` {
		t.Fatalf("untouched state: %+v %v", v, err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.WaterExtension(context.Background(), true); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("read failure must be reported without a private path: %v", err)
	}
}

func TestHealthWaterImportKeepsDependentGuard(t *testing.T) {
	h, _ := extensionTestHub(t)
	installWaterImportPackage(t, h, waterImportManifest)
	installWaterImportPackage(t, h, `{"manifest_version":1,"id":"water-addon","name":"Water Addon","version":"1.0.0","depends":[{"id":"water"}],"contributes":{"providers":[{"id":"water-addon-p","name":"Water Addon P","base_url":"https://addon.example.com"}]}}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/assistant/health/intake/import", nil)
	if h.DisableWaterExtension(w, r, "water") || w.Code != 409 || !strings.Contains(w.Body.String(), `"has_dependents"`) || !h.extensions.reg.Snapshot("water").Record.Enabled {
		t.Fatalf("dependent guard bypassed: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	if h.DisableWaterExtension(w, r, "replaced-water") || w.Code != 409 {
		t.Fatalf("changed storage key accepted: %d %s", w.Code, w.Body)
	}
}
