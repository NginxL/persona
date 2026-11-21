package assistant

import (
	"context"
	"encoding/json"
	"github.com/nwverse/persona/server/internal/db"
	"net/http"
	"testing"
)

func TestMainFeedHydratesPendingPlansAcrossConversations(t *testing.T) {
	e := newTodoEnv(t, nil)
	setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
	ctx := context.Background()
	for _, p := range []*TaskPlan{
		{ID: "pending-routine", Conversation: "routine-chat", State: planOpen},
		{ID: "pending-side", Conversation: "side-chat", State: planOpen},
		{ID: "closed-side", Conversation: "side-chat", State: planDismissed},
	} {
		if err := e.s.savePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, conversation := range []string{"conv1", "other"} {
		w := e.must("GET", "/api/v1/assistant/conversations/"+conversation+"/feed?paginate=1", "")
		var out struct {
			Plans map[string]*TaskPlan `json:"plans"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		want := 0
		if conversation == "conv1" {
			want = 2
		}
		if len(out.Plans) != want {
			t.Fatalf("%s: plans = %+v, want %d", conversation, out.Plans, want)
		}
		if out.Plans["closed-side"] != nil {
			t.Fatal("closed plan was hydrated")
		}
	}
}

func TestMainPendingPlansRespectExtensionFeedScope(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
		e.sessions("server", scopeListed()...)
		p := &TaskPlan{ID: "private-pending-" + randomHex(6), Conversation: "routine-chat", State: planOpen}
		if err := e.s.savePlan(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		for _, role := range []string{"", roleExtension} {
			var body []byte
			if role == "" {
				body = e.must("GET", "/api/v1/assistant/conversations/conv1/feed", "").Body.Bytes()
			} else {
				w := e.doAs(role, "GET", "/api/v1/assistant/conversations/conv1/feed", "")
				if w.Code != http.StatusOK {
					t.Fatalf("extension: %d %s", w.Code, w.Body)
				}
				body = w.Body.Bytes()
			}
			var out struct {
				Plans      map[string]*TaskPlan `json:"plans"`
				PendingIDs *[]string            `json:"pending_plan_ids"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if role == "" && (out.Plans[p.ID] == nil || out.PendingIDs == nil) {
				t.Fatalf("owner missing pending plan: %s", body)
			}
			if role == roleExtension && (out.Plans[p.ID] != nil || out.PendingIDs != nil) {
				t.Fatalf("extension received cross-conversation plan: %s", body)
			}
		}
	})
}

func TestMainPendingPlansKeepHealthSeparate(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		ctx := context.Background()
		e.s.noteConversationSpaces(ctx, []db.ConversationSpace{{MachineID: "server", SessionID: "health-pending", Space: SpaceHealth}})
		p := &TaskPlan{ID: "health-pending-" + randomHex(6), Conversation: "health-pending", State: planOpen}
		if err := e.s.savePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
		plans, err := e.s.mainPendingPlans(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range plans {
			if got.ID == p.ID {
				t.Fatal("Health plan reached Main")
			}
		}
		tagged := e.s.planWithSpace(ctx, p)
		if tagged.Space != SpaceHealth || p.Space != "" {
			t.Fatalf("space snapshot mutated source: %+v / %+v", tagged, p)
		}
	})
}
