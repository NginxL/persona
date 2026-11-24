package hub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/update"
)

func TestDevelopmentPackagesRequireConfirmedAdoptionAndManagerAuth(t *testing.T) {
	h := newTestHub(t)
	h.bus.idents = &memIdents{}
	token, err := h.createBusToken(t.Context(), "remote", "Remote manager")
	if err != nil {
		t.Fatal(err)
	}
	target := devicewire.DevelopmentBuild(strings.Repeat("a", 40), "web-dev-aaaaaaaaaaaa-202611230910", 202611230910)
	h.version, h.build.Number, h.build.Commit = target.Version, target.Build, target.Commit
	h.upd.settings.ConfirmedDevelopment = &target
	h.upd.loaded = true
	dir := filepath.Join(h.upd.packageStore(), target.Version)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"manifest.json", "persona-node-linux-amd64.gz", "database-0.dump"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	get := func(file, bearer string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, target.Packages+"/"+file, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("HTTP %d, want %d", w.Code, status)
		}
		return w
	}
	get("manifest.json", "", http.StatusUnauthorized)
	get("manifest.json?token="+token, "", http.StatusUnauthorized)
	get("persona-node-linux-amd64.gz", "invalid", http.StatusUnauthorized)
	get("database-0.dump", token, http.StatusNotFound)
	for _, file := range []string{"manifest.json", "persona-node-linux-amd64.gz"} {
		w := get(file, token, http.StatusOK)
		if w.Body.String() != file || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("wrong artifact or cache policy")
		}
	}
	if w := get("persona-node-linux-arm64.gz", token, http.StatusNotFound); w.Header().Get(devicewire.DevelopmentCommitHeader) != target.Commit {
		t.Fatal("current platform absence must identify the commit")
	}
	h.bus.current = map[string]*busConn{"remote": {presence: busPresence{Capabilities: []string{devicewire.DevelopmentPackagesCapability}}}}
	cfg := h.devicesConfigFor("remote")
	if cfg.DevelopmentTarget == nil || *cfg.DevelopmentTarget != target || cfg.DevelopmentHold {
		t.Fatalf("confirmed handover: %+v", cfg.DevelopmentTarget)
	}
	if cfg := h.devicesConfigFor("older"); !cfg.DevelopmentHold || cfg.HubVersion != "" || cfg.AutoUpdateNodes {
		t.Fatal("an older manager must not deploy this version from its release source")
	}
	held := func() {
		t.Helper()
		if cfg := h.devicesConfigFor("remote"); !cfg.DevelopmentHold || cfg.DevelopmentTarget != nil || cfg.AutoUpdateNodes || cfg.HubVersion != "" {
			t.Fatal("unconfirmed development identity escaped in configuration")
		}
		get("manifest.json", token, http.StatusGone)
		get("persona-node-linux-amd64.gz", token, http.StatusGone)
	}
	h.upd.confirmed = false
	held()
	h.upd.confirmed = true
	journal := update.DevelopmentAdoptionPath(h.upd.dir)
	if err := update.Save(journal, update.Pending{}); err != nil {
		t.Fatal(err)
	}
	held()
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	h.build.Commit = strings.Repeat("b", 40)
	held()
	h.build.Commit = target.Commit
	if err := h.upd.record(t.Context(), updateRecord{Source: "web-dev", Result: "rolled_back", ToBuild: target.Build, Commit: target.Commit}); err != nil {
		t.Fatal(err)
	}
	held()
	delete(h.bus.current, "remote")
	if _, err := h.revokeBusToken(t.Context(), "remote"); err != nil {
		t.Fatal(err)
	}
	get("manifest.json", token, http.StatusUnauthorized)
}

func TestDevelopmentRollbackCanOfferTheRestoredGoodVersion(t *testing.T) {
	h := newTestHub(t)
	u := h.upd
	good := devicewire.DevelopmentBuild(strings.Repeat("a", 40), "web-dev-aaaaaaaaaaaa-202611230910", 202611230910)
	failed := devicewire.DevelopmentBuild(strings.Repeat("b", 40), "web-dev-bbbbbbbbbbbb-202611230911", 202611230911)
	h.version, h.build.Number, h.build.Commit = good.Version, good.Build, good.Commit
	u.loaded = true
	for _, target := range []devicewire.DevelopmentTarget{good, failed} {
		if err := u.record(t.Context(), updateRecord{Source: "web-dev", Result: "updated", ToVersion: target.Version, ToBuild: target.Build, Commit: target.Commit}); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.record(t.Context(), updateRecord{Source: "web-dev", Result: "rolled_back", ToVersion: failed.Version, ToBuild: failed.Build, Commit: failed.Commit}); err != nil {
		t.Fatal(err)
	}
	if target, hold := u.confirmedDevelopment(); hold || target == nil || *target != good {
		t.Fatal("previous confirmation unavailable after rollback", target)
	}
	h.version, h.build.Number, h.build.Commit = failed.Version, failed.Build, failed.Commit
	if target, hold := u.confirmedDevelopment(); !hold || target != nil {
		t.Fatal("rolled-back receipt accepted", target)
	}
}
