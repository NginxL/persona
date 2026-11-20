package node

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestRuntimeAssistantHistorySurvivesRediscovery(t *testing.T) {
	n, _ := runtimeNode(t)
	assistantDir := filepath.Join(n.home, ".persona", "assistant")
	healthDir := filepath.Join(n.home, ".persona", "health")
	workspace := filepath.Join(n.home, "project")
	for id, cwd := range map[string]string{
		"assistant-session": assistantDir,
		"health-session":    healthDir,
		"ordinary-session":  workspace,
	} {
		n.state.put(id, sessionState{Cwd: cwd, Launcher: "saved-launcher", StartedAt: time.Now()})
	}
	hello := assistantRuntimeHello()
	caps := hello["caps"].(map[string]any)
	caps["modes"], caps["model"] = true, true
	rows := []proto.Session{}
	for _, id := range []string{"assistant-session", "health-session", "ordinary-session", "external-session"} {
		rows = append(rows, proto.Session{
			SessionID: id, Cwd: n.home, Launcher: "runtime-launcher", Status: proto.StatusWorking,
			Model: "native-model", PermissionMode: "native-mode", Effort: "high", BypassAvailable: true, Compaction: &proto.Compaction{State: "running"},
		})
	}
	publish := func(host *fakeRuntime) {
		t.Helper()
		if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": rows}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "runtime snapshot", func() bool { return len(n.runtimes.appendSessions(nil)) == len(rows) })
	}
	check := func() {
		t.Helper()
		sessions := listed(t, n)
		for _, id := range []string{"assistant-session", "health-session", "ordinary-session", "external-session"} {
			s, ok := sessions[id]
			if !ok {
				t.Fatalf("missing runtime session %s", id)
			}
			expectedDir := n.home
			switch id {
			case "assistant-session":
				expectedDir = assistantDir
			case "health-session":
				expectedDir = healthDir
			}
			if s.Cwd != expectedDir {
				t.Errorf("%s directory %q, want %q", id, s.Cwd, expectedDir)
			}
			owned := id != "external-session"
			if s.StartedByPersona != owned {
				t.Errorf("%s ownership %v, want %v", id, s.StartedByPersona, owned)
			}
			if owned && s.Launcher != "saved-launcher" {
				t.Errorf("%s launcher %q", id, s.Launcher)
			}
			if s.Status != proto.StatusWorking || s.Model != "native-model" || s.PermissionMode != "native-mode" || s.Effort != "high" || !s.BypassAvailable {
				t.Errorf("runtime state changed for %s: %+v", id, s)
			}
			if s.RequestedModel != nil {
				t.Errorf("%s has a Persona-requested model, but the runtime owns its model selection", id)
			}
			if s.Compaction == nil || s.Compaction.State != "running" {
				t.Errorf("runtime compaction changed for %s: %+v", id, s.Compaction)
			}
			if s.Capabilities == nil || !s.Capabilities.Send || s.Capabilities.Model == nil || !*s.Capabilities.Model {
				t.Errorf("runtime capabilities changed for %s: %+v", id, s.Capabilities)
			}
		}
	}
	host := dialRuntime(t, n, hello)
	publish(host)
	check()
	if err := host.conn.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "runtime disconnected", func() bool { return n.runtimes.ownerOf("assistant-session") == nil })
	host = dialRuntime(t, n, hello)
	publish(host)
	check()
}

func TestRuntimeSessionMetadataChangesArePublished(t *testing.T) {
	row := proto.Session{SessionID: "runtime-session", SourceKind: "runtime", Cwd: "/work/project", Launcher: "runtime"}
	before := listKey([]proto.Session{row})
	for name, change := range map[string]func(*proto.Session){
		"directory": func(s *proto.Session) { s.Cwd = "/work/.persona/assistant" },
		"launcher":  func(s *proto.Session) { s.Launcher = "saved-launcher" },
		"ownership": func(s *proto.Session) { s.StartedByPersona = true },
	} {
		t.Run(name, func(t *testing.T) {
			changed := row
			change(&changed)
			if listKey([]proto.Session{changed}) == before {
				t.Fatal("runtime metadata change was suppressed")
			}
		})
	}
}
