package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSkillCatalogUsesConversationExecutionAndRejectsForeignSessions(t *testing.T) {
	s, f := pickFixture(t)
	s.id = "helper-a"
	var request map[string]any
	f.answerNode("skills.list", func(machine string, raw json.RawMessage) (any, error) {
		if machine != "server" {
			t.Fatalf("wrong machine: %s", machine)
		}
		_ = json.Unmarshal(raw, &request)
		return assistantskills.Catalog{Version: 1, Skills: []assistantskills.Entry{{ID: "selected", Name: "review", Source: "project"}}}, nil
	})
	w := appRequest(t, s, "GET", "/api/v1/assistant/skills?machine_id=server&session_id=side&cwd=/secret", "", "en")
	if w.Code != 200 || request["launcher"] != "cx" || request["skill_owner"] != "helper-a" || request["cwd"] == "/secret" {
		t.Fatalf("catalog execution: %d %s %+v", w.Code, w.Body.String(), request)
	}
	for _, sid := range []string{"agent", "foreign"} {
		w := appRequest(t, s, "GET", "/api/v1/assistant/skills?machine_id=server&session_id="+sid, "", "en")
		if w.Code != 404 {
			t.Fatalf("%s should not expose a catalog: %d %s", sid, w.Code, w.Body.String())
		}
	}
}

func TestSkillSelectionFailsClosedButOrdinaryChatNeedsNoCatalog(t *testing.T) {
	s, f := pickFixture(t)
	calls := 0
	f.answerNode("skills.list", func(_ string, _ json.RawMessage) (any, error) {
		calls++
		return assistantskills.Catalog{Version: 1, Skills: []assistantskills.Entry{{ID: "selected", Name: "review", Source: "project"}}}, nil
	})
	ordinary := "Email me@example.com and literal @review"
	if _, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": ordinary}}); err != nil || calls != 0 {
		t.Fatalf("ordinary chat queried the catalog: %v %d", err, calls)
	}
	block := `<persona-context kind="skill" ref="selected">Review</persona-context>`
	out, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": block + "\nCheck this"}})
	if err != nil || out["skill_owner"] == nil || !strings.Contains(out["text"].(string), `ref="selected"`) || calls != 1 {
		t.Fatalf("explicit invocation lost: %+v %v calls=%d", out, err, calls)
	}
	if _, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": strings.ReplaceAll(block, "selected", "/secret")}}); err == nil {
		t.Fatal("accepted stale/client-path reference")
	}
}

func TestSkillCatalogDoesNotTreatLegacyNodeInventoryAsExecutableCatalog(t *testing.T) {
	s, f := pickFixture(t)
	f.answerNode("skills.list", func(_ string, _ json.RawMessage) (any, error) {
		return map[string]any{"skills": []any{map[string]any{"name": "old", "path": "/legacy/SKILL.md"}}}, nil
	})
	w := appRequest(t, s, "GET", "/api/v1/assistant/skills?machine_id=server&session_id=side", "", "en")
	if w.Code == 200 {
		t.Fatalf("legacy inventory accepted as an effective runtime catalog: %s", w.Body.String())
	}
}

func TestSkillCatalogAndSelectionSupportRuntimeBackedAgySession(t *testing.T) {
	s, f := pickFixture(t)
	s.id = "helper-a"
	f.setSessions("server",
		proto.Session{SessionID: "main", Assistant: true, Launcher: "gw"},
		proto.Session{SessionID: "agy-chat-1", Assistant: true, Launcher: "agy", SourceKind: proto.SourceRuntime, Cwd: "/assistant-agy"},
	)
	var requests []map[string]any
	f.answerNode("skills.list", func(_ string, raw json.RawMessage) (any, error) {
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		requests = append(requests, req)
		return assistantskills.Catalog{Version: 1, Skills: []assistantskills.Entry{{ID: "agy-ref-1", Name: "review", Source: "project · ."}}}, nil
	})
	w := appRequest(t, s, "GET", "/api/v1/assistant/skills?machine_id=server&session_id=agy-chat-1", "", "en")
	if w.Code != 200 || len(requests) != 1 || requests[0]["session_id"] != "agy-chat-1" || requests[0]["cwd"] != "/assistant-agy" || requests[0]["skill_owner"] != "helper-a" {
		t.Fatalf("agy /assistant/skills: %d %s %+v", w.Code, w.Body.String(), requests)
	}
	block := `<persona-context kind="skill" ref="agy-ref-1">review</persona-context>`
	out, err := s.SendParams(context.Background(), SendParams{
		MachineID: "server",
		SessionID: "agy-chat-1",
		Params:    map[string]any{"text": block + "\nCheck this"},
	})
	if err != nil || out["skill_owner"] != "helper-a" || len(requests) != 2 {
		t.Fatalf("agy SendParams with selected skill: %+v %v reqs=%d", out, err, len(requests))
	}
}
