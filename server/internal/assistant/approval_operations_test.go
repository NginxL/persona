package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestClarifiedApprovalRejectsStaleSetting(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	setSettings(t, ft.s, func(v *Settings) { on := true; v.MainTasksQuiet = &on })
	toolDone := make(chan proto.ToolResult, 1)
	go func() {
		toolDone <- ft.s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "mac", SessionID: "s1",
			Arguments: json.RawMessage(`{"action":"set","key":"assistant.main_tasks_quiet","value":false}`)})
	}()
	cards := ft.waitPending(1)
	if len(cards) != 1 || !cards[0].CanClarify {
		t.Fatalf("setting card = %+v", cards)
	}
	if err := ft.s.clarify(context.Background(), cards[0].ID, "Why change this?", "owner-setting-question", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-toolDone:
		if out.IsError || !strings.Contains(out.Content[0].Text, "remains valid") {
			t.Fatalf("clarified setting tool = %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("setting tool did not return for the answer")
	}
	// The current value changes after the visible before→after proposal was
	// prepared. Rebuilding now yields no change, so the old apply must not run.
	setSettings(t, ft.s, func(v *Settings) { off := false; v.MainTasksQuiet = &off })
	allow := true
	if err := ft.s.resolve(cards[0].ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	result := waitApprovalResult(t, ft, cards[0].ID)
	if !strings.Contains(result, "stale and cancelled") || !strings.Contains(result, "fresh approval") {
		t.Fatalf("stale setting result = %q", result)
	}
	if off := ft.s.mainTasksQuiet(context.Background()); off {
		t.Fatal("stale setting apply changed the value back")
	}
}

func TestClarifiedModelTierApprovalKeepsOriginalRevisionGuard(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	putTierProfiles(t, ft.s, "economy", map[string]ModelTier{"review": {Launcher: "claude"}}, nil)
	toolDone := make(chan proto.ToolResult, 1)
	go func() {
		toolDone <- ft.s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: "mac", SessionID: "s1",
			Arguments: json.RawMessage(`{"action":"set","key":"model_profiles.tiers","value":{"tier":"review","remove":true}}`)})
	}()
	cards := ft.waitPending(1)
	if len(cards) != 1 || !cards[0].CanClarify {
		t.Fatalf("model-tier card = %+v", cards)
	}
	if err := ft.s.clarify(context.Background(), cards[0].ID, "Why remove this preset?", "owner-tier-question", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-toolDone:
		if out.IsError || !strings.Contains(out.Content[0].Text, "remains valid") {
			t.Fatalf("clarified model-tier tool = %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("model-tier tool did not return for the answer")
	}
	// This profile edit does not change the visible tier before/after text, but
	// it advances the revision. Rebuilding from current state must not rebase
	// the old card and remove a tier from a profile the owner never approved.
	if _, err := ft.s.saveModelProfile(context.Background(), nil, "economy", ProfileActor{Kind: "hub"}, func(p *ModelProfile, _ bool) error {
		p.Description = "edited while the owner asked"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	allow := true
	if err := ft.s.resolve(cards[0].ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	result := waitApprovalResult(t, ft, cards[0].ID)
	if !strings.Contains(result, "stale and cancelled") || !strings.Contains(result, "changed after the card") {
		t.Fatalf("stale model-tier result = %q", result)
	}
	profiles, _ := storedTiers(t, ft.s)
	if _, ok := tierOf(profiles, "economy", "review"); !ok {
		t.Fatal("the stale clarified card removed the tier")
	}
}

func TestRepositoryAddRechecksDuplicateAfterClarification(t *testing.T) {
	s, checks := repositoryService(t, ruleStore{&memStore{}})
	f := s.hub.(*fakeHub)
	f.setSessions("server", proto.Session{MachineID: "server", SessionID: "c1", Assistant: true, Agent: "claude", Status: proto.StatusIdle})
	toolDone := make(chan proto.ToolResult, 1)
	go func() { toolDone <- repositoryAddCall(context.Background(), s, "repository_add", repoExample) }()
	card, ok := awaitCard(s)
	if !ok || !card.CanClarify {
		t.Fatalf("repository card = %+v, %v", card, ok)
	}
	if err := s.clarify(context.Background(), card.ID, "Which repository is this?", "owner-repository-question", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-toolDone:
		if out.IsError || !strings.Contains(out.Content[0].Text, approvalops.ClarifyingText) {
			t.Fatalf("clarified repository tool = %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("repository tool did not return for the answer")
	}
	// Another path adds the same canonical repository before the old card's
	// Allow. That is the one saved repository; the clarified card adds nothing.
	if w := placeDo(s, "", "POST", repoBase, repoExample); w.Code != 200 {
		t.Fatalf("external repository add: %d %s", w.Code, w.Body)
	}
	if *checks != 1 {
		t.Fatalf("external checks = %d", *checks)
	}
	allow := true
	if err := s.resolve(card.ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	doc, err := s.repositoryDocument(context.Background())
	if err != nil || len(doc.Repositories) != 1 {
		t.Fatalf("repositories after stale Allow = %+v, %v", doc, err)
	}
	if *checks != 1 {
		t.Fatalf("stale Allow checked or saved another repository: checks=%d", *checks)
	}
	result := waitFakeApprovalResult(t, f, card.ID)
	if !strings.Contains(result, "stale and cancelled") {
		t.Fatalf("stale repository result = %q", result)
	}
}
