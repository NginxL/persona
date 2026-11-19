package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// #10268: a blocked Status dialog plus an undrained Stop must not be sent
// around the plain/force/close refusal cycle. No queue entry is ours to delete.
func TestArchiveBlockedRecoveryAdvice(t *testing.T) {
	for _, mode := range []string{"plain", "close", "force"} {
		t.Run(mode, func(t *testing.T) {
			ft, row := closeTestService(t, func(s *proto.Session) {
				s.Status, s.State = proto.StatusBlocked, proto.StateNeedsYou
				s.Queued = []string{"Stop", "unrelated work"}
			})
			args := map[string]any{"machine": "mac", "session_id": row.SessionID}
			if mode != "plain" {
				args[mode] = true
			}
			_, err := archiveTool(t, ft, "agent_archive", args)
			if err == nil {
				t.Fatal("blocked session was archived")
			}
			msg := err.Error()
			if strings.Contains(msg, "without clos") {
				t.Fatalf("unusable recovery: %s", msg)
			}
			for _, want := range []string{"dialog", "computer", "queued", "retry"} {
				if !strings.Contains(msg, want) {
					t.Errorf("missing %q in %s", want, msg)
				}
			}
			if len(ft.d.callsTo("mac", "session.close")) != 0 || len(retiredOf(ft)) != 0 || len(archivedIDs(t, ft.s)) != 0 || len(ft.s.pendingQuestions()) != 0 {
				t.Fatal("refusal changed session or requested an approval")
			}
			raw, ok := ft.s.nodeSession("mac", row.SessionID)
			if !ok || len(raw.Queued) != 2 || raw.Queued[1] != "unrelated work" {
				t.Fatalf("queue changed: %+v", raw)
			}
			// The user dismisses Status and exits Claude on the computer. Only the
			// resulting authoritative inventory makes the ordinary retry available.
			row.Status, row.State = proto.StatusOffline, proto.StateOffline
			row.Queued = nil
			ft.d.setSessions("mac", []proto.Session{row})
			if _, err := ft.s.setAgentArchive(context.Background(), "mac", row.SessionID, true); err != nil {
				t.Fatalf("retry after exit: %v", err)
			}
			if !archivedIDs(t, ft.s)[row.SessionID] {
				t.Fatal("retry did not archive")
			}
		})
	}
}

func TestArchiveCloseAdviceMatchesPlainGuard(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*proto.Session)
		plain bool
	}{
		{"working", func(s *proto.Session) { s.Status = proto.StatusWorking; s.State = proto.StateWorking }, false},
		{"blocked", func(s *proto.Session) { s.Status = proto.StatusBlocked; s.State = proto.StateNeedsYou }, false},
		{"background", func(s *proto.Session) { s.Background = []proto.BackgroundTask{{ID: "active"}} }, false},
		{"loop", func(s *proto.Session) { s.Loop = &proto.LoopInfo{} }, true},
		{"queued", func(s *proto.Session) { s.Queued = []string{"keep"} }, true},
		{"server", func(s *proto.Session) { s.Ports = []proto.Port{{Port: 5173}} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft, row := closeTestService(t, tc.edit)
			_, err := archiveTool(t, ft, "agent_archive", map[string]any{"machine": "mac", "session_id": row.SessionID, "close": true})
			if err == nil {
				t.Fatal("close accepted")
			}
			if strings.Contains(err.Error(), "without clos") != tc.plain {
				t.Fatalf("plain permitted=%v, advice=%s", tc.plain, err)
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/?close=1", nil)
			r.SetPathValue("sid", row.SessionID)
			r.Header.Set(RoleHeader, roleFull)
			ft.s.setAgentSessionArchive(w, r, "mac", true)
			if w.Code != http.StatusConflict {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body)
			}
			// Inspect the typed refusal separately: the endpoint localizes for its caller.
			_, err = ft.s.archiveAndCloseAgentSession(context.Background(), "mac", row.SessionID)
			if strings.Contains(i18n.Localize(err, i18n.En), "without clos") != tc.plain {
				t.Fatalf("service advice: %v", err)
			}
			_, plainErr := ft.s.setAgentArchive(context.Background(), "mac", row.SessionID, true)
			if (plainErr == nil) != tc.plain {
				t.Fatalf("plain archive: %v", plainErr)
			}
		})
	}
}

func TestArchiveRecoveryHTTPUsesCallerLanguage(t *testing.T) {
	for _, mode := range []string{"", "?close=1", "?force=1"} {
		for _, tc := range []struct{ lang, want string }{
			{"en", "read-only screen"}, {"zh-Hans", "只读页面"}, {"ja", "読み取り専用画面"},
		} {
			t.Run(mode+tc.lang, func(t *testing.T) {
				ft, row := closeTestService(t, func(s *proto.Session) {
					s.Status, s.State = proto.StatusBlocked, proto.StateNeedsYou
					s.Queued = []string{"Stop", "keep this work"}
				})
				res, err := serveHTTP(context.Background(), ft.s, assistantwire.HTTPRequest{
					Method: http.MethodPost, Path: "/api/v1/sessions/mac/" + row.SessionID + "/archive" + mode,
					Header: map[string][]string{RoleHeader: {roleFull}, "Accept-Language": {tc.lang}},
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if res.Status != http.StatusConflict || !strings.Contains(string(res.Body), tc.want) || !strings.Contains(string(res.Body), "agent_busy") {
					t.Fatalf("localized recovery: %d %s", res.Status, res.Body)
				}
				if len(ft.d.callsTo("mac", "session.close")) != 0 || len(retiredOf(ft)) != 0 || len(archivedIDs(t, ft.s)) != 0 {
					t.Fatal("localized refusal changed session")
				}
			})
		}
	}
}

func TestForceArchiveRecoveryDistinguishesQuestionAndIdle(t *testing.T) {
	t.Run("question", func(t *testing.T) {
		ft, row := closeTestService(t, func(s *proto.Session) { s.Status, s.State = proto.StatusBlocked, proto.StateNeedsYou })
		ix := proto.Interaction{ID: "keep-question", MachineID: "mac", SessionID: row.SessionID, Kind: proto.IxQuestion}
		ft.s.ixMapLocked()[ix.ID] = &pending{ix: ix}
		_, err := archiveTool(t, ft, "agent_archive", map[string]any{"machine": "mac", "session_id": row.SessionID, "force": true})
		if err == nil || !strings.Contains(err.Error(), "pending question") || strings.Contains(err.Error(), "without clos") {
			t.Fatalf("question advice: %v", err)
		}
		if len(ft.s.pendingInteractions()) != 1 || len(ft.d.callsTo("mac", "session.close")) != 0 || len(archivedIDs(t, ft.s)) != 0 {
			t.Fatal("question was changed")
		}
	})
	t.Run("idle", func(t *testing.T) {
		ft, row := closeTestService(t, nil)
		_, err := archiveTool(t, ft, "agent_archive", map[string]any{"machine": "mac", "session_id": row.SessionID, "force": true})
		if err == nil || !strings.Contains(err.Error(), "without force") {
			t.Fatalf("idle advice: %v", err)
		}
		if len(ft.d.callsTo("mac", "session.close")) != 0 || len(archivedIDs(t, ft.s)) != 0 {
			t.Fatal("idle force changed session")
		}
		if _, err := ft.s.setAgentArchive(context.Background(), "mac", row.SessionID, true); err != nil {
			t.Fatalf("ordinary archive: %v", err)
		}
	})
}
