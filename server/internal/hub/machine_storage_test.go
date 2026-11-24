package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func callStorageRoute(h *Hub, role, method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "/api/v1/machines/m1/"+path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, "/api/v1/machines/m1/"+path, nil)
	}
	r.SetPathValue("mid", "m1")
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: role, ID: "dev"}))
	w := httptest.NewRecorder()
	switch {
	case method == http.MethodGet && path == "storage":
		h.getMachineStorage(w, r)
	case method == http.MethodGet && strings.HasPrefix(path, "storage/hotspots"):
		h.getMachineStorageHotspots(w, r)
	case method == http.MethodPost && path == "storage/cleanup":
		h.postMachineStorageCleanup(w, r)
	}
	return w
}

func TestMachineStorageRoutesNeedFullDevice(t *testing.T) {
	tn := newTermNode(t)
	h := tn.hub
	for _, role := range []string{roleAnswer, roleExtension, roleLink} {
		for _, tc := range [][3]string{
			{http.MethodGet, "storage", ""},
			{http.MethodGet, "storage/hotspots", ""},
			{http.MethodPost, "storage/cleanup", `{"targets":["pip-cache"],"dry_run":true}`},
		} {
			if w := callStorageRoute(h, role, tc[0], tc[1], tc[2]); w.Code != http.StatusForbidden {
				t.Errorf("%s %s with role %s = %d, want 403", tc[0], tc[1], role, w.Code)
			}
		}
	}
	if got := tn.await(t, 0); len(got) != 0 {
		t.Fatalf("node heard refused requests: %q", got)
	}
}

func TestMachineStorageRoutesHappyPathAndValidation(t *testing.T) {
	tn := newTermNode(t)
	tn.answers = map[string]any{
		proto.MethodMachineStorage: proto.MachineStorageReport{
			At:        100,
			OS:        "linux",
			Supported: true,
			Mounts:    []proto.MountStat{{Mount: "/", TotalBytes: 1000, UsedBytes: 500, AvailBytes: 500, UsedPercent: 50}},
		},
		proto.MethodMachineStorageHotspots: proto.MachineStorageHotspotsReport{
			At:         100,
			OS:         "linux",
			Categories: []proto.StorageHotspotCategory{{ID: proto.StorageCategoryPipCache, Bytes: 2048, Cleanable: true, CleanupTargetID: proto.StorageTargetPipCache, Status: proto.StorageStatusOK}},
		},
		proto.MethodMachineStorageCleanup: proto.StorageCleanupResult{
			DryRun:         true,
			EstimatedBytes: 2048,
			Items:          []proto.StorageCleanupItem{{TargetID: proto.StorageTargetPipCache, Bytes: 2048, Status: proto.StorageCleanupPreview}},
		},
	}

	// GET storage
	w := callStorageRoute(tn.hub, roleFull, http.MethodGet, "storage", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET storage = %d: %s", w.Code, w.Body.String())
	}
	var storageRep proto.MachineStorageReport
	if err := json.Unmarshal(w.Body.Bytes(), &storageRep); err != nil || storageRep.MachineID != "m1" || len(storageRep.Mounts) != 1 {
		t.Fatalf("storage response = %s (%v)", w.Body.String(), err)
	}

	// GET hotspots
	w = callStorageRoute(tn.hub, roleFull, http.MethodGet, "storage/hotspots?timeout_ms=4000&max_depth=3", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET hotspots = %d: %s", w.Code, w.Body.String())
	}
	var hotRep proto.MachineStorageHotspotsReport
	if err := json.Unmarshal(w.Body.Bytes(), &hotRep); err != nil || hotRep.MachineID != "m1" || len(hotRep.Categories) != 1 {
		t.Fatalf("hotspots response = %s (%v)", w.Body.String(), err)
	}

	// Validation refusals (never hit node)
	for _, tc := range []struct {
		body string
		code string
	}{
		{`{}`, "bad_request"},
		{`{"targets":[]}`, "bad_request"},
		{`{"targets":["/etc/passwd"],"confirm":true}`, "bad_request"},
		{`{"targets":["pip-cache"]}`, "confirmation_required"},
	} {
		w = callStorageRoute(tn.hub, roleFull, http.MethodPost, "storage/cleanup", tc.body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.code) {
			t.Errorf("POST cleanup %s = %d %s, want 400 %s", tc.body, w.Code, w.Body.String(), tc.code)
		}
	}

	// Preview cleanup (dry_run=true)
	w = callStorageRoute(tn.hub, roleFull, http.MethodPost, "storage/cleanup", `{"targets":["pip-cache"],"dry_run":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST cleanup preview = %d: %s", w.Code, w.Body.String())
	}
	var cleanRep proto.StorageCleanupResult
	if err := json.Unmarshal(w.Body.Bytes(), &cleanRep); err != nil || cleanRep.MachineID != "m1" || !cleanRep.DryRun {
		t.Fatalf("cleanup preview response = %s (%v)", w.Body.String(), err)
	}

	// Confirmed cleanup (confirm=true)
	tn.answers[proto.MethodMachineStorageCleanup] = proto.StorageCleanupResult{
		DryRun:     false,
		FreedBytes: 2048,
		Items:      []proto.StorageCleanupItem{{TargetID: proto.StorageTargetPipCache, Bytes: 2048, Status: proto.StorageCleanupCleaned}},
	}
	w = callStorageRoute(tn.hub, roleFull, http.MethodPost, "storage/cleanup", `{"targets":["pip-cache"],"confirm":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST cleanup confirm = %d: %s", w.Code, w.Body.String())
	}
	got := tn.await(t, 4)
	if len(got) != 4 {
		t.Fatalf("expected 4 node calls, got %v", got)
	}
}

type oldStorageCaller struct{}

func (oldStorageCaller) Call(_ context.Context, method string, _, _ any) error {
	return &rpc.Error{Message: "unknown method " + method}
}
func (oldStorageCaller) Notify(string, any) error { return nil }
func (oldStorageCaller) Done() <-chan struct{}    { return nil }
func (oldStorageCaller) Close(error)              {}

func TestMachineStorageOldNodeReturnsNodeOutdated(t *testing.T) {
	tn := newTermNode(t)
	tn.where.mu.Lock()
	tn.where.peer = oldStorageCaller{}
	tn.where.mu.Unlock()
	w := callStorageRoute(tn.hub, roleFull, http.MethodGet, "storage", "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), proto.CodeNodeOutdated) {
		t.Fatalf("old node response = %d %s, want 409 %s", w.Code, w.Body.String(), proto.CodeNodeOutdated)
	}
}

func TestMachineStorageRoutesAreFullCredentialsOnly(t *testing.T) {
	data, err := os.ReadFile("testdata/route_scopes.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range [][2]string{
		{http.MethodGet, "storage"},
		{http.MethodGet, "storage/hotspots"},
		{http.MethodPost, "storage/cleanup"},
	} {
		url := "/api/v1/machines/m1/" + tc[1]
		for _, p := range []principal{{ID: "dev_watch", Role: roleAnswer}, {ID: "dev_link", Role: roleLink}} {
			if allowed(p, tc[0], url) {
				t.Errorf("%s may %s %s", p.Role, tc[0], url)
			}
		}
		line := "-                    " + tc[0] + " /api/v1/machines/{mid}/" + tc[1] + "\n"
		if !strings.Contains(string(data), line) {
			t.Errorf("route_scopes.txt missing full-credentials-only line for %s %s", tc[0], tc[1])
		}
	}
}
