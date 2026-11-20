package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Opt-in UI fixture: real manager authorization, archive validation, publication and list,
// but synthetic hub/device transport. It neither starts a model nor touches a real user's store.
// Run the repository mock on 18578, this test with SKILL_IMPORT_QA=1 on 18579, and Vite on
// 18580 with PERSONA_HUB_URL=http://127.0.0.1:18579. POST /_qa/stop ends the fixture.
func TestSkillImportBrowserHarness(t *testing.T) {
	if os.Getenv("SKILL_IMPORT_QA") != "1" {
		t.Skip("opt-in isolated browser fixture")
	}
	root := t.TempDir()
	t.Setenv("PERSONA_SKILLS_DIR", filepath.Join(root, "skills"))
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", filepath.Join(root, "plugins"))
	store := &assistantskills.Importer{}
	f := newFakeHub().withMachine()
	f.hello.Assistant.Machines = []string{"server"}
	f.devices = []Device{{ID: "server", Home: "/home/example", Name: "Example computer"}}
	f.nodes = map[string]nodeAnswer{}
	f.nodes["skills.import"] = func(_ string, raw json.RawMessage) (any, error) {
		var in struct {
			assistantskills.ImportRequest
			Scope string `json:"scope"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		out, err := store.Import(context.Background(), assistantskills.ImportTarget{Scope: in.Scope, Cwd: filepath.Join(root, "assistant")}, in.ImportRequest)
		var coded *assistantskills.ImportError
		if errors.As(err, &coded) {
			return nil, &rpc.Error{Code: coded.Code, Message: coded.Code}
		}
		return out, err
	}
	s := newTestService(t, f)
	mockURL := os.Getenv("SKILL_IMPORT_QA_MOCK")
	if mockURL == "" {
		mockURL = "http://127.0.0.1:18578"
	}
	target, _ := url.Parse(mockURL)
	fallback := httputil.NewSingleHostReverseProxy(target)
	done := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_qa/stop":
			select {
			case done <- struct{}{}:
			default:
			}
			apierr.WriteJSON(w, map[string]bool{"stopped": true})
		case "/api/v1/skills":
			apierr.WriteJSON(w, map[string]any{"machine": "Example computer", "skills": assistantskills.List(assistantskills.UserDir(), assistantskills.Selection{})})
		case "/api/v1/skills/import":
			r.Header.Set(RoleHeader, roleFull)
			s.ServeHTTP(w, r)
		default:
			fallback.ServeHTTP(w, r)
		}
	})
	addr := os.Getenv("SKILL_IMPORT_QA_ADDR")
	if addr == "" {
		addr = "127.0.0.1:18579"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	defer srv.Close()
	go srv.Serve(listener)
	t.Log("skill import fixture ready on loopback port 18579")
	select {
	case <-done:
	case <-time.After(30 * time.Minute):
		t.Fatal("browser fixture deadline exceeded")
	}
}
