package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// closeTestService has one idle Claude agent open in a pane on "mac" (the node's list) whose node takes session.retire.
func closeTestService(t *testing.T, edit func(*proto.Session)) (*fleetTest, proto.Session) {
	t.Helper()
	ft := newFleetTest(t)
	ft.f.mu.Lock()
	ft.f.sessions["mac"] = nil // the fake hub reaches mac's node
	ft.f.mu.Unlock()
	row := proto.Session{MachineID: "mac", SessionID: "open-claude", Agent: "claude", Title: "open", PaneID: "w1:p1",
		Status: proto.StatusIdle, State: proto.StateIdle, StartedByPersona: true, UpdatedAt: time.Now().Add(-time.Hour).UTC()}
	if edit != nil {
		edit(&row)
	}
	ft.d.setSessions("mac", []proto.Session{row})
	return ft, row
}

func retiredOf(ft *fleetTest) []string {
	ft.f.mu.Lock()
	defer ft.f.mu.Unlock()
	return append([]string(nil), ft.f.retired...)
}

func archivedIDs(t *testing.T, s *Service) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, row := range archiveRows(t, s) {
		out[row.SessionID] = true
	}
	return out
}

func TestArchiveAndCloseRetiresAndArchives(t *testing.T) {
	ft, row := closeTestService(t, nil)
	res, err := ft.s.archiveAndCloseAgentSession(context.Background(), "mac", row.SessionID)
	if err != nil || !res.Closed || res.Outcome != archiveCloseClosed || res.ReasonKey != "" {
		t.Fatalf("archive and close: %+v %v", res, err)
	}
	if got := retiredOf(ft); len(got) != 1 || got[0] != row.SessionID {
		t.Fatalf("retired: %v", got)
	}
	if !archivedIDs(t, ft.s)[row.SessionID] {
		t.Fatal("closed agent not archived")
	}
	// Closing it may stamp its transcript (an exit line): that is not new work and must not restore it.
	closed := row
	closed.Status, closed.State, closed.PaneID = proto.StatusOffline, proto.StateOffline, ""
	closed.UpdatedAt = time.Now().UTC().Add(-time.Second)
	ft.d.setSessions("mac", []proto.Session{closed})
	if !archivedIDs(t, ft.s)[row.SessionID] {
		t.Fatal("activity written while closing restored the archive")
	}
	// A message after that resumes it: new activity restores it as for any archive.
	closed.UpdatedAt = time.Now().UTC().Add(time.Minute)
	ft.d.setSessions("mac", []proto.Session{closed})
	if archivedIDs(t, ft.s)[row.SessionID] {
		t.Fatal("new activity after the close did not restore the archive")
	}
}

func TestArchiveAndCloseRefusesBusyAgents(t *testing.T) {
	for name, edit := range map[string]func(*proto.Session){
		"working":    func(s *proto.Session) { s.Status, s.State = proto.StatusWorking, proto.StateWorking },
		"needs you":  func(s *proto.Session) { s.Status, s.PendingInteractions = proto.StatusBlocked, 1 },
		"background": func(s *proto.Session) { s.Background = []proto.BackgroundTask{{ID: "b1"}} },
		"subagent":   func(s *proto.Session) { s.Subagents = []proto.Subagent{{Running: true}} },
		"loop":       func(s *proto.Session) { s.Loop = &proto.LoopInfo{} },
		"queued":     func(s *proto.Session) { s.Queued = []string{"next"} },
		"server":     func(s *proto.Session) { s.Ports = []proto.Port{{Port: 5173}} },
		"starting":   func(s *proto.Session) { s.Starting = true },
	} {
		t.Run(name, func(t *testing.T) {
			ft, row := closeTestService(t, edit)
			_, err := ft.s.archiveAndCloseAgentSession(context.Background(), "mac", row.SessionID)
			var refusal *agentArchiveError
			if !errors.As(err, &refusal) || refusal.status != http.StatusConflict || refusal.code != "agent_busy" {
				t.Fatalf("busy agent accepted: %v", err)
			}
			if len(retiredOf(ft)) != 0 || len(archivedIDs(t, ft.s)) != 0 {
				t.Fatal("a refused archive and close changed something")
			}
		})
	}
}

func TestArchiveAndCloseKeepsArchiveWhenTheCloseFails(t *testing.T) {
	for _, c := range []struct {
		name, outcome, key string
		err                error
	}{
		{"other tool", archiveCloseRefused, "hub-agent_archives.close_engine", &rpc.Error{Code: "not_retirable", Message: "this session belongs to another tool"}},
		{"not resumable", archiveCloseRefused, "hub-agent_archives.close_not_resumable", &rpc.Error{Code: "not_retirable", Message: "not started by Persona: its launcher is not known"}},
		{"node working", archiveCloseRefused, "hub-agent_archives.close_busy", &rpc.Error{Code: "not_retirable", Message: "the agent is working"}},
		{"old node", archiveCloseRefused, "hub-agent_archives.close_node_old", &rpc.Error{Message: "unknown method session.retire"}},
		{"offline", archiveCloseOffline, "hub-agent_archives.close_offline", fmt.Errorf("mac: %w", apierr.ErrOffline)},
		{"failed", archiveCloseFailed, "hub-agent_archives.close_failed", &rpc.Error{Message: "herdr: no such pane"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ft, row := closeTestService(t, nil)
			ft.f.mu.Lock()
			ft.f.retireErr = c.err
			ft.f.mu.Unlock()
			res, err := ft.s.archiveAndCloseAgentSession(context.Background(), "mac", row.SessionID)
			if err != nil || res.Closed || res.Outcome != c.outcome || res.ReasonKey != c.key {
				t.Fatalf("close outcome: %+v %v", res, err)
			}
			if !archivedIDs(t, ft.s)[row.SessionID] {
				t.Fatal("a failed close undid the archive")
			}
		})
	}
	t.Run("machine gone", func(t *testing.T) {
		ft, row := closeTestService(t, nil)
		ft.f.mu.Lock()
		delete(ft.f.sessions, "mac") // the fake hub answers ErrNoDevice
		ft.f.mu.Unlock()
		res, err := ft.s.archiveAndCloseAgentSession(context.Background(), "mac", row.SessionID)
		if err != nil || res.Outcome != archiveCloseOffline || !archivedIDs(t, ft.s)[row.SessionID] {
			t.Fatalf("offline: %+v %v", res, err)
		}
	})
}

func TestArchiveAndCloseUsesRuntimeCloseCapability(t *testing.T) {
	closeAllowed := true
	ft, row := closeTestService(t, func(s *proto.Session) {
		s.SessionID, s.Agent, s.PaneID = "agy-session", "agy", ""
		s.SourceKind = proto.SourceRuntime
		s.Capabilities = &proto.SessionCapabilities{Close: &closeAllowed}
	})
	ft.f.mu.Lock()
	ft.f.retireErr = &rpc.Error{Code: "not_retirable", Message: "this session belongs to another tool"}
	ft.f.mu.Unlock()
	var closed []string
	ft.f.answerNode("session.close", func(_ string, raw json.RawMessage) (any, error) {
		var params map[string]string
		_ = json.Unmarshal(raw, &params)
		closed = append(closed, params["session_id"])
		return map[string]any{"ok": true}, nil
	})
	res, err := ft.s.archiveAndCloseAgentSession(context.Background(), "mac", row.SessionID)
	if err != nil || !res.Closed || res.Outcome != archiveCloseClosed {
		t.Fatalf("runtime close: %+v %v", res, err)
	}
	if len(closed) != 1 || closed[0] != row.SessionID || len(retiredOf(ft)) != 0 {
		t.Fatalf("close calls %v, retire calls %v", closed, retiredOf(ft))
	}
	if !archivedIDs(t, ft.s)[row.SessionID] {
		t.Fatal("runtime session not archived")
	}
}

func TestArchiveAndCloseOfflineAgentOnlyArchives(t *testing.T) {
	ft, row := closeTestService(t, func(s *proto.Session) {
		s.Status, s.State, s.PaneID = proto.StatusOffline, proto.StateOffline, ""
	})
	res, err := ft.s.archiveAndCloseAgentSession(context.Background(), "mac", row.SessionID)
	if err != nil || res.Closed || res.Outcome != archiveCloseNotRunning || res.ReasonKey != "" {
		t.Fatalf("offline agent: %+v %v", res, err)
	}
	if len(retiredOf(ft)) != 0 {
		t.Fatal("asked the node to close an agent that is not running")
	}
	if !archivedIDs(t, ft.s)[row.SessionID] {
		t.Fatal("not archived")
	}
}

func TestArchiveAndCloseHTTP(t *testing.T) {
	ft, row := closeTestService(t, nil)
	post := func(path, role string) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set(RoleHeader, role)
		ft.s.ServeHTTP(w, r)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v %s", path, err, w.Body)
		}
		return w.Code, body
	}
	if code, _ := post("/api/v1/sessions/mac/"+row.SessionID+"/archive?close=1", "answer"); code != http.StatusForbidden {
		t.Fatalf("answer role closed an agent: %d", code)
	}
	ft.f.mu.Lock()
	ft.f.retireErr = &rpc.Error{Code: "not_retirable", Message: "this engine continues sessions itself"}
	ft.f.mu.Unlock()
	code, body := post("/api/v1/sessions/mac/"+row.SessionID+"/archive?close=1", roleFull)
	closeBody, _ := body["close"].(map[string]any)
	if code != http.StatusOK || body["archived"] != true || closeBody["closed"] != false || closeBody["outcome"] != archiveCloseRefused ||
		closeBody["reason"] == "" || closeBody["detail"] != "this engine continues sessions itself" {
		t.Fatalf("refused close: %d %+v", code, body)
	}
	if code, body = post("/api/v1/sessions/mac/"+row.SessionID+"/unarchive", roleFull); code != http.StatusOK {
		t.Fatalf("restore: %d %+v", code, body)
	}
	ft.f.mu.Lock()
	ft.f.retireErr = nil
	ft.f.mu.Unlock()
	code, body = post("/api/v1/sessions/mac/"+row.SessionID+"/archive?close=1", roleFull)
	closeBody, _ = body["close"].(map[string]any)
	if code != http.StatusOK || closeBody["closed"] != true || closeBody["outcome"] != archiveCloseClosed || closeBody["reason"] != nil {
		t.Fatalf("closed: %d %+v", code, body)
	}
	busy := row
	busy.Status, busy.State = proto.StatusWorking, proto.StateWorking
	busy.SessionID = "busy"
	ft.d.setSessions("mac", []proto.Session{row, busy})
	if code, body = post("/api/v1/sessions/mac/busy/archive?close=1", roleFull); code != http.StatusConflict || body["code"] != "agent_busy" {
		t.Fatalf("busy: %d %+v", code, body)
	}
}
