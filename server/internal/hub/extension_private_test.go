package hub

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// An extension's token never sees the health space's conversations or a private card (extension_private.go), whatever
// it was granted: not in the lists, their totals and counts, and not read by id.
func TestExtensionTokensNeverSeeTheHealthSpace(t *testing.T) {
	h, url, _, b := reachHub(t)
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "mb", Sessions: []proto.Session{
		{MachineID: "mb", SessionID: "b1", Title: "B-SESSION", Cwd: "/work/project"},
		{MachineID: "mb", SessionID: "bh", Title: "HEALTH-SESSION", Cwd: "/home/me/.persona/health", Assistant: true, Space: "health"},
		{MachineID: "mb", SessionID: "bd", Title: "HEALTH-DIR", Cwd: "/home/me/.persona/health/notes", Assistant: true},
	}})
	eventually(t, 5*time.Second, "mb's sessions", func() bool { _, ok := h.localMachine("mb").session("bd"); return ok })
	cards := []proto.Interaction{
		{ID: "ix_ok", MachineID: "mb", SessionID: "b1", Cwd: "/work/project", Kind: proto.IxQuestion, Summary: "OK-CARD"},
		{ID: "ix_private", MachineID: "mb", SessionID: "b1", Cwd: "/work/project", Kind: proto.IxQuestion, Private: true, Summary: "PRIVATE-CARD"},
		{ID: "ix_health", MachineID: "mb", SessionID: "bh", Kind: proto.IxQuestion, Summary: "HEALTH-CARD"},
		{ID: "ix_health_cwd", MachineID: "mb", SessionID: "b1", Cwd: "/home/me/.persona/health", Kind: proto.IxQuestion, Summary: "HEALTH-CWD-CARD"},
	}
	for _, ix := range cards {
		if err := b.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: ix}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 5*time.Second, "the cards", func() bool { return h.interactionOwner("ix_health_cwd") != "" })

	for _, path := range []string{"/api/v1/sessions", "/api/v1/sessions?machine_id=mb", "/api/v1/sessions?limit=50",
		"/api/v1/sessions?limit=50&summary=1", "/api/v1/sessions?counts=1&machine_id=mb", "/api/v1/sessions?limit=50&with_counts=1&machine_id=mb"} {
		code, body := extDo(t, url, http.MethodGet, path, "", nil)
		if code != http.StatusOK || strings.Contains(body, "HEALTH") || strings.Contains(body, `"bh"`) || strings.Contains(body, `"bd"`) {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		if !strings.Contains(path, "counts=1&") && !strings.Contains(body, "B-SESSION") {
			t.Fatalf("%s: the session it may see is missing: %s", path, body)
		}
	}
	// Totals and counts leave them out too.
	_, body := extDo(t, url, http.MethodGet, "/api/v1/sessions?counts=1&machine_id=mb", "", nil)
	var counts struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &counts); err != nil || counts.Total != 1 {
		t.Fatalf("counts %s", body)
	}

	for _, sid := range []string{"bh", "bd", "never-listed"} {
		for _, path := range []string{"/items", "/items?fold_work=1", "/items/i1/full", "/items/i1/image", "/items/i1/visualization", "/info", "/screen"} {
			if code, body := extDo(t, url, http.MethodGet, "/api/v1/sessions/mb/"+sid+path, "", nil); code != http.StatusNotFound {
				t.Errorf("%s%s: %d %s", sid, path, code, body)
			}
		}
	}
	if code, body := extDo(t, url, http.MethodGet, "/api/v1/sessions/mb/b1/info", "", nil); code != http.StatusOK {
		t.Fatalf("b1 info: %d %s", code, body)
	}

	code, body := extDo(t, url, http.MethodGet, "/api/v1/interactions", "", nil)
	if code != http.StatusOK || !strings.Contains(body, "OK-CARD") || strings.Contains(body, "PRIVATE") || strings.Contains(body, "HEALTH") {
		t.Fatalf("pending cards: %d %s", code, body)
	}
}
