package hub

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// POST /sessions goes through the real manager before the node starts the process. Inheriting an effort there
// would make the node refuse otherwise valid native omp starts.
func TestOmpNewAgentModelFromSessionsHTTP(t *testing.T) {
	h := newTestHub(t)
	if !h.loadMachineTrust(t.Context()) {
		t.Fatal("could not initialize model-start machine trust")
	}
	const bound = "provider/settings-model"
	h.store = &newAgentStore{memStore: newMemStore(), b: assistantwire.ModelBinding{Provider: "launcher.omp", Model: bound, Effort: "high"}}
	starts := make(chan map[string]any, 1)
	modelsNode(t, h, MachineConfig{ID: "box", Name: "Box"}, func(method string, p map[string]any) (any, error) {
		switch method {
		case "launchers.list":
			return map[string]any{"launchers": []proto.Launcher{{Name: "omp", Agent: "omp", Source: "path"}}}, nil
		case "session.start":
			starts <- p
			title, _ := p["tab_name"].(string)
			return map[string]any{"session": proto.Session{SessionID: title, Cwd: "/code", Launcher: "omp", Agent: "omp"}}, nil
		}
		return map[string]any{}, nil
	})
	connectAssistant(t, h)
	for _, tc := range []struct{ name, launcher, model, want string }{
		{"follow-settings", "", "", bound},
		{"launcher-default", "omp", "", ""},
		{"manual-model", "omp", "provider/typed", "provider/typed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"machine_id": "box", "cwd": "/code", "tab_name": tc.name, "prompt": "Check the project"}
			if tc.launcher != "" {
				body["launcher"] = tc.launcher
			}
			if tc.model != "" {
				body["model"] = tc.model
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r := modelsRequest(h, http.MethodPost, "/api/v1/sessions", string(raw))
			if r.Code != http.StatusOK {
				t.Fatalf("start: %d %s", r.Code, r.Body)
			}
			select {
			case p := <-starts:
				model, sent := p["model"].(string)
				if p["launcher"] != "omp" || model != tc.want || sent != (tc.want != "") || p["effort"] != nil {
					t.Fatalf("node start: %+v, want model %q and no effort", p, tc.want)
				}
			default:
				t.Fatal("HTTP start returned without reaching the node")
			}
		})
	}
}
