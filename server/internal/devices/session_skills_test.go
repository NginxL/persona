package devices

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestOrdinarySkillCatalogRouteUsesManagerAndSessionFacts(t *testing.T) {
	d := newTestDevices(t)
	d.opts.ManagerID = "helper"
	m, _ := clipMachine(t, d, "computer", nil)
	var got map[string]any
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	manager, node := rpc.New("manager", h2nW), rpc.New("node", n2hW)
	node.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "session.skills" {
			t.Errorf("unexpected method: %s", method)
		}
		_ = json.Unmarshal(params, &got)
		return assistantskills.Catalog{Version: 1, Complete: false, Skills: []assistantskills.Entry{{ID: "opaque", Name: "review", Source: "user"}}}, nil
	}
	go manager.Serve(ctx, n2hR)
	go node.Serve(ctx, h2nR)
	t.Cleanup(func() { h2nW.Close(); n2hW.Close() })
	m.peer = manager
	handler := d.Handler(http.NotFoundHandler())
	for _, role := range []string{"", "answer", "extension"} {
		req := httptest.NewRequest("GET", "/api/v1/sessions/computer/example/skills", nil)
		req.Header.Set(assistantwire.RoleHeader, role)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("role %q: %d", role, w.Code)
		}
	}
	req := httptest.NewRequest("GET", "/api/v1/sessions/computer/example/skills?cwd=/untrusted&launcher=other&skill_owner=other", nil)
	req.Header.Set(assistantwire.RoleHeader, "full")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "opaque") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("catalog: %d %s", w.Code, w.Body.String())
	}
	if len(got) != 2 || got["session_id"] != "example" || got["skill_owner"] != "agents:helper" {
		t.Fatalf("untrusted execution fields: %+v", got)
	}
}

func TestOrdinarySkillsRequireCapableNodeButPlainChatDoesNot(t *testing.T) {
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "computer"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, info := range []proto.NodeInfo{current("old"), {Version: "old"}} {
		info.Capabilities = slices.DeleteFunc(slices.Clone(info.Capabilities), func(cap string) bool { return cap == nodeproto.SessionSkills })
		m.info = info
		for _, method := range []string{"session.skills", "session.send"} {
			params := map[string]any{"skill_owner": "agents:helper"}
			var re *rpc.Error
			if err := m.call(ctx, method, params, nil); !errors.As(err, &re) || re.Code != "skill_unavailable" || !strings.Contains(string(re.Data), `"none"`) {
				t.Fatalf("old node accepted %s: %v", method, err)
			}
		}
		var re *rpc.Error
		if err := m.call(ctx, "session.send", map[string]any{"text": "Literal @review"}, nil); errors.As(err, &re) && re.Code == "skill_unavailable" {
			t.Fatal("ordinary chat requires skills")
		}
	}
	for _, text := range []string{"Literal @review", `<persona-context kind="skill" ref="opaque">review</persona-context>\nReview`} {
		params, _ := json.Marshal(map[string]any{"text": text})
		prepared, err := d.prepareSessionSkills(params)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		_ = json.Unmarshal(prepared, &fields)
		if strings.Contains(text, "persona-context") != (fields["skill_owner"] == d.sessionSkillOwner()) {
			t.Fatalf("selection ownership: %s", prepared)
		}
	}
}
