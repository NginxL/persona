package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// prepareSubscriptionCall plays the coordinator's first accepted question: the
// tool returns non-finally and the retained operation is applied later, after
// the test has changed current state.
func prepareSubscriptionCall(e *toolEnv, args string) approval.Operation {
	e.t.Helper()
	var retained approval.Operation
	e.h.opts.AskPrepared = func(_ context.Context, ix proto.Interaction, op approval.Operation) (approval.Outcome, error) {
		e.mu.Lock()
		e.cards = append(e.cards, ix)
		e.mu.Unlock()
		retained = op
		return approval.Outcome{Clarifying: true, Text: approval.ClarifyingText}, nil
	}
	res := e.h.callSubscriptionTool(context.Background(), asker{"mac", "conv1"}, "subscription_save", json.RawMessage(args))
	if res.IsError || !strings.Contains(res.Content[0].Text, "remains valid") || retained.Apply == nil {
		e.t.Fatalf("prepared subscription call = %+v", res)
	}
	return retained
}

func TestSubscriptionSaveRechecksRevisionAfterClarification(t *testing.T) {
	t.Run("update", func(t *testing.T) {
		e := newToolEnv(t)
		before := e.made()
		retained := prepareSubscriptionCall(e, fmt.Sprintf(`{"action":"update","id":%q,"revision":%d,"label":"Approved name"}`, before.ID, before.Revision))
		external, err := e.h.subs.Update(context.Background(), before.ID, subscriptions.Patch{Revision: before.Revision, Label: ptrTo("External name")}, subscriptionActor)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := retained.Apply(context.Background(), allowCard(proto.Interaction{})); !errors.Is(err, approval.ErrStale) {
			t.Fatalf("stale update apply error = %v", err)
		}
		current, _, err := e.h.subs.Get(context.Background(), before.ID)
		if err != nil || current.Label != external.Label || current.Revision != external.Revision {
			t.Fatalf("stale update changed current state: %+v, %v", current, err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		e := newToolEnv(t)
		before := e.made()
		retained := prepareSubscriptionCall(e, fmt.Sprintf(`{"action":"delete","id":%q,"revision":%d}`, before.ID, before.Revision))
		external, err := e.h.subs.Update(context.Background(), before.ID, subscriptions.Patch{Revision: before.Revision, Label: ptrTo("Do not delete")}, subscriptionActor)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := retained.Apply(context.Background(), allowCard(proto.Interaction{})); !errors.Is(err, approval.ErrStale) {
			t.Fatalf("stale delete apply error = %v", err)
		}
		current, _, err := e.h.subs.Get(context.Background(), before.ID)
		if err != nil || current.Label != external.Label {
			t.Fatalf("stale delete removed or changed the subscription: %+v, %v", current, err)
		}
	})

	t.Run("create with an externally created equivalent", func(t *testing.T) {
		e := newToolEnv(t)
		retained := prepareSubscriptionCall(e, `{"action":"create","connector":"example","source":"notices","recipe":`+toolRecipe+`,"label":"Office notices"}`)
		var recipe subscriptions.Recipe
		if err := json.Unmarshal([]byte(toolRecipe), &recipe); err != nil {
			t.Fatal(err)
		}
		external, err := e.h.subs.Create(context.Background(), subscriptions.CreateRequest{Connector: "example", Source: "notices", Recipe: &recipe, Label: "Office notices"}, subscriptionActor)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := retained.Apply(context.Background(), allowCard(proto.Interaction{})); !errors.Is(err, approval.ErrStale) {
			t.Fatalf("equivalent create apply error = %v", err)
		}
		list, _, err := e.h.subs.List(context.Background(), subscriptions.Filter{Connector: "example"})
		if err != nil || len(list) != 1 || list[0].ID != external.ID {
			t.Fatalf("stale create made a duplicate: %+v, %v", list, err)
		}
	})
}

func TestSubscriptionSaveAppliesCurrentProposalAfterClarification(t *testing.T) {
	e := newToolEnv(t)
	before := e.made()
	retained := prepareSubscriptionCall(e, fmt.Sprintf(`{"action":"update","id":%q,"revision":%d,"label":"Approved after answer"}`, before.ID, before.Revision))
	text, err := retained.Apply(context.Background(), allowCard(proto.Interaction{}))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil || out["saved"] != true {
		t.Fatalf("fresh apply = %s, %v", text, err)
	}
	current, _, err := e.h.subs.Get(context.Background(), before.ID)
	if err != nil || current.Label != "Approved after answer" {
		t.Fatalf("fresh apply did not make the approved change: %+v, %v", current, err)
	}
}
