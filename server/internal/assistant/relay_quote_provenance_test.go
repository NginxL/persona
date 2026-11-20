package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestRetainedQuoteRequiresSourceVerification(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	ctx := context.Background()
	oldID := "old-garden-note"
	oldKey := quoteRef(d.mainID, oldID)
	book := map[string]any{oldKey: map[string]any{
		"request": BriefRequest{Text: "Map the new garden", Conversation: d.mainID, MessageID: oldID},
		"at":      time.Now().UTC(),
	}}
	if err := d.s.store().PutSetting(ctx, relayQuotesKey, book); err != nil {
		t.Fatal(err)
	}
	owner := ownerItem("new-garden-note", "Map the orchard")
	d.f.setItems(owner)
	if got := d.s.verifiedQuote(ctx, d.mainID, oldID); got != nil {
		t.Fatal("legacy record with unknown provenance was retained")
	}
	var saved map[string]json.RawMessage
	if _, err := d.s.store().Setting(ctx, relayQuotesKey, &saved); err != nil || saved[oldKey] != nil {
		t.Fatalf("historical quote cleanup: saved keys %d, error %v", len(saved), err)
	}
	request, err := d.s.resolveQuote(ctx, d.mainID, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.s.keepQuote(ctx, d.mainID, *request)
	restarted := New(Options{Hub: d.f, Store: func() Store { return d.s.store() }})
	if got := restarted.verifiedQuote(ctx, d.mainID, owner.ID); got == nil || got.Text != owner.Text {
		t.Fatalf("freshly verified record after restart = %+v", got)
	}
}

func TestReceivedWholeAndPartHaveSeparateSelectors(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	ctx := context.Background()
	owner := ownerItem("park-plan", "Mark the east gate and mark the west trail")
	owner.TS = time.Now()
	d.f.setItems(owner)
	if result := quoteSend(t, d, d.mainID, d.leaderID, owner.ID); result.IsError {
		t.Fatal(result.Content[0].Text)
	}
	whole := received(t, d.f, d.mainID)
	if result := quoteSend(t, d, d.mainID, d.leaderID, "mark the west trail"); result.IsError {
		t.Fatal(result.Content[0].Text)
	}
	part := received(t, d.f, d.mainID)
	part.ID = "received-part"
	partRef := prompts.ParseRelayQuoteID(part.Text)
	d.f.setItems(whole, part)
	for _, test := range []struct {
		selector string
		want     string
	}{
		{owner.ID, owner.Text},
		{partRef, "mark the west trail"},
		{"mark the west trail", "mark the west trail"},
	} {
		got, err := d.s.resolveQuote(ctx, d.leaderID, test.selector)
		if err != nil || got == nil || got.Text != test.want {
			t.Errorf("selector %q selected %+v, error %v", test.selector, got, err)
		}
	}
	d.f.setItems(part)
	for _, selector := range []string{owner.ID, part.ID} {
		if got, err := d.s.resolveQuote(ctx, d.leaderID, selector); err == nil || got != nil {
			t.Errorf("selector %q selected a passage without its reference: %+v", selector, got)
		}
	}
}

func TestRetainedQuoteMixedStoreAfterUpgrade(t *testing.T) {
	ctx := context.Background()
	store := todoTestDB(t)
	book := map[string]relayQuote{
		quoteRef("garden", "unmarked"): {Request: BriefRequest{MessageID: "unmarked", Text: "Plant carrots"}},
		quoteRef("garden", "checked"):  {Request: BriefRequest{MessageID: "checked", Text: "Plant beans"}, SourceChecked: true},
	}
	if err := store.PutSetting(ctx, relayQuotesKey, book); err != nil {
		t.Fatal(err)
	}
	service := New(Options{Hub: newFakeHub(), Store: func() Store { return store }})
	got := service.verifiedQuote(ctx, "garden", "checked")
	if got == nil || got.Text != "Plant beans" || service.verifiedQuote(ctx, "garden", "unmarked") != nil {
		t.Fatalf("mixed retained quotes after restart: checked = %+v", got)
	}
	var durable map[string]relayQuote
	if _, err := store.Setting(ctx, relayQuotesKey, &durable); err != nil || len(durable) != 1 || !durable[quoteRef("garden", "checked")].SourceChecked {
		t.Fatalf("persisted upgrade contains %d quotes, error %v", len(durable), err)
	}
}

func TestCodexLatestNeedsAnExplicitOwnerMessage(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	ctx := context.Background()
	d.f.setSessions("server",
		proto.Session{MachineID: "server", SessionID: d.mainID, Agent: "codex", Assistant: true},
		proto.Session{MachineID: "server", SessionID: d.leaderID, Assistant: true},
	)
	first := ownerItem("garden-first", "Plan the planting")
	steered := ownerItem("garden-change", "Include an apple tree")
	for _, page := range [][]proto.Item{{first, steered}, {steered}} {
		d.f.setItems(page...)
		checkQuoteRefusal(t, d, "latest", "id", "exact words")
		for _, selector := range []string{steered.ID, steered.Text} {
			got, err := d.s.resolveQuote(ctx, d.mainID, selector)
			if err != nil || got == nil || got.MessageID != steered.ID || got.Text != steered.Text {
				t.Fatalf("explicit Codex selection %q = %+v, error %v", selector, got, err)
			}
		}
	}
}

func TestAmbiguousQuoteChoiceAsksForTheDisplayedID(t *testing.T) {
	d := newDispatchPolicyHarness(t)
	first := ownerItem("garden-first", strings.Repeat("garden ", 20))
	next := ownerItem("garden-second", "Plant an apple tree")
	next.Queued = true
	d.f.setItems(first, next)
	msg := checkQuoteRefusal(t, d, "latest", first.ID, next.ID)
	if !strings.Contains(msg, "copy the id") || strings.Contains(msg, "or the exact words") {
		t.Errorf("the selection instructions should direct the user to the displayed ids: %q", msg)
	}
}
