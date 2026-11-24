package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// #10527: an external-event park may name the worker's existing pending owner prompt by its interaction id.
// The tests read the JSON contract rather than production struct fields so they compile against the pre-fix hub.
const parkOwnerInteractionID = "ix_read_only_permission"

// parkOwnerStore keeps the two existing stores this acceptance path uses: the supervision ledger and interaction history.
// It is test infrastructure only; task_review, projection and resolution still run through the Service.
type parkOwnerStore struct {
	*fleetMem
	mu   sync.Mutex
	rows map[string]db.TodoSupervisionRow
}

func newParkOwnerStore() *parkOwnerStore {
	return &parkOwnerStore{fleetMem: newFleetMem(), rows: map[string]db.TodoSupervisionRow{}}
}

func newParkOwnerService(t *testing.T) (*Service, *fakeHub, Todo, *parkOwnerStore) {
	t.Helper()
	s, f, todo := newSupervisedService(t)
	store := newParkOwnerStore()
	s.store = func() Store { return store }
	// The replacement store must retain the existing main identity: under #5732 Origin alone is not review authority,
	// and these cases must reach interaction validation rather than failing earlier on an accidentally anonymous fixture.
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	return s, f, todo, store
}

func (s *parkOwnerStore) ListTodoSupervision(context.Context) ([]db.TodoSupervisionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.TodoSupervisionRow, 0, len(s.rows))
	for _, row := range s.rows {
		out = append(out, row)
	}
	return out, nil
}

func (s *parkOwnerStore) PutTodoSupervision(_ context.Context, row db.TodoSupervisionRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[row.TodoID] = row
	return nil
}

func (s *parkOwnerStore) ReviewDoneTodo(context.Context, string, string, bool, time.Time) (*db.TodoRow, bool, error) {
	return nil, false, nil // not used by these parked-task cases
}

func (s *parkOwnerStore) LinkTodoReviewer(context.Context, string, string, string, string, string, string, time.Time) error {
	return nil // not used by these parked-task cases
}

func parkOwnerLedger(t *testing.T, s *Service, store *parkOwnerStore, id string) string {
	t.Helper()
	rows, err := store.ListTodoSupervision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]any{todoNow(s, id).Supervision, rows, pendingKinds(s, id)})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func parkExternalInteractionArgs(kind, interactionID string) map[string]any {
	a := parkEvidenceArgs(kind)
	a["evidence"].(map[string]any)["interaction_id"] = interactionID
	return a
}

func parkOwnerPrompt() proto.Interaction {
	now := time.Now().UTC()
	deadline := now.Add(6 * time.Hour)
	return proto.Interaction{
		ID: parkOwnerInteractionID, MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
		ToolName: "AskUserQuestion", CreatedAt: now.Add(-5 * time.Minute), ExpiresAt: now.Add(2 * time.Hour),
		Questions: []proto.Question{{
			Question: "Allow read-only access to the connector?",
			Options:  []proto.QuestionOption{{Label: "Allow read-only"}, {Label: "Later", Action: proto.QuestionOptionDefer}},
		}},
		Delegate: &proto.Delegate{What: "Read-only connector preflight", Deadline: &deadline},
	}
}

func releaseParkWorker(t *testing.T, s *Service, id string) {
	t.Helper()
	if err := s.releaseStoppedAgents(context.Background(), id, []TodoAgent{{MachineID: "box", SessionID: "s1"}}); err != nil {
		t.Fatal(err)
	}
}

func listedParkEvidence(t *testing.T, s *Service, id string) (map[string]any, map[string]any) {
	t.Helper()
	listed := listedTodos(t, s)[id]
	sup, _ := listed["supervision"].(map[string]any)
	parked, _ := sup["parked"].(map[string]any)
	evidence, _ := parked["evidence"].(map[string]any)
	view, _ := listed["view"].(map[string]any)
	return evidence, view
}

func requireOneOwnerInteraction(t *testing.T, view map[string]any, interactionID string) {
	t.Helper()
	if view["section"] != "needs_you" || view["reason"] != "agent_asks" {
		t.Fatalf("the linked park is not in the owner-decision flow: %v", view)
	}
	asks, _ := view["asks"].([]any)
	if len(asks) != 1 {
		t.Fatalf("owner asks = %v, want exactly the existing prompt", asks)
	}
	ask, _ := asks[0].(map[string]any)
	if ask["interaction"] != interactionID || ask["home"] != "run" {
		t.Fatalf("owner ask = %v, want the existing interaction %q", ask, interactionID)
	}
}

func TestTaskReviewSchemaOffersExternalEventInteractionID(t *testing.T) {
	var schema struct {
		Properties struct {
			Evidence struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"evidence"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(prompts.Tool("task_review").InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	raw, ok := schema.Properties.Evidence.Properties["interaction_id"]
	if !ok {
		t.Fatal("task_review's external-event evidence cannot name the existing pending interaction")
	}
	var field struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &field); err != nil || field.Type != "string" {
		t.Fatalf("evidence.interaction_id schema = %s, %v", raw, err)
	}
}

func TestParkEvidenceRefusesUnknownResolvedOrIneligibleInteractionIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		id   string
	}{
		{"unknown", "external_event", "ix_not_pending"},
		{"resolved", "external_event", parkOwnerInteractionID},
		{"another workers prompt", "external_event", "ix_other_worker"},
		{"a person reply cannot borrow an owner prompt", "person_reply", parkOwnerInteractionID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo, store := newParkOwnerService(t)
			switch tc.name {
			case "resolved":
				ix := parkOwnerPrompt()
				putPending(s, &pending{ix: ix, done: make(chan proto.Resolution, 1), told: true})
				if err := s.resolve(ix.ID, proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: "Allow read-only"}}); err != nil {
					t.Fatal(err)
				}
			case "another workers prompt":
				ix := parkOwnerPrompt()
				ix.ID, ix.SessionID = tc.id, "another-worker"
				putPending(s, &pending{ix: ix, done: make(chan proto.Resolution, 1), told: true})
			case "a person reply cannot borrow an owner prompt":
				ix := parkOwnerPrompt()
				putPending(s, &pending{ix: ix, done: make(chan proto.Resolution, 1), told: true})
			}
			before := parkOwnerLedger(t, s, store, todo.ID)
			pushes, notices, sends := len(f.pushed()), len(f.notices()), len(sentToAgent(f))
			_, err := reviewErr(s, todo.ID, parkExternalInteractionArgs(tc.kind, tc.id))
			if err == nil {
				t.Errorf("task_review accepted %s as a verified owner interaction", tc.id)
			} else if !strings.Contains(err.Error(), "evidence.interaction_id") {
				t.Errorf("refusal does not name the bad reference: %v", err)
			}
			if after := parkOwnerLedger(t, s, store, todo.ID); before != after {
				t.Errorf("an ineligible interaction changed the park ledger or owner signals\nbefore: %s\nafter: %s", before, after)
			}
			if len(f.pushed()) != pushes || len(f.notices()) != notices || len(sentToAgent(f)) != sends {
				t.Error("an ineligible interaction sent a request, notification or Agent instruction")
			}
		})
	}
}

func TestParkEvidenceRecordsTheVerifiedInteractionWithoutSendingAnotherPrompt(t *testing.T) {
	s, f, todo, _ := newParkOwnerService(t)
	ix := parkOwnerPrompt()
	putPending(s, &pending{ix: ix, done: make(chan proto.Resolution, 1), told: true})
	// No active task work remains, but the exact pending request is still this parked task's existing request.
	releaseParkWorker(t, s, todo.ID)
	pushes, notices, sends, cards := len(f.pushed()), len(f.notices()), len(sentToAgent(f)), len(s.pendingInteractions())
	if _, err := reviewErr(s, todo.ID, parkExternalInteractionArgs("external_event", ix.ID)); err != nil {
		t.Fatal(err)
	}
	evidence, _ := listedParkEvidence(t, s, todo.ID)
	if evidence["interaction_id"] != ix.ID || evidence["kind"] != "external_event" || evidence["contact"] != nil {
		t.Fatalf("park evidence did not keep the verified interaction without inventing a contact: %v", evidence)
	}
	if got := todoNow(s, todo.ID).Supervision; got.State != reviewParked || got.Parked == nil || got.Parked.Until.IsZero() {
		t.Fatalf("linking the prompt discarded the park or its fallback: %+v", got)
	}
	if len(f.pushed()) != pushes || len(f.notices()) != notices || len(sentToAgent(f)) != sends || len(s.pendingInteractions()) != cards {
		t.Fatal("linking the existing prompt generated a card, notification or Agent instruction")
	}
}

func TestParkedLinkedInteractionIsOneOwnerAskAndResolvesThroughTheExistingPath(t *testing.T) {
	s, f, todo, store := newParkOwnerService(t)
	ix := parkOwnerPrompt()
	putPending(s, &pending{ix: ix, done: make(chan proto.Resolution, 1), told: true})
	s.record(ix)
	releaseParkWorker(t, s, todo.ID)
	if _, err := reviewErr(s, todo.ID, parkExternalInteractionArgs("external_event", ix.ID)); err != nil {
		t.Fatal(err)
	}
	_, view := listedParkEvidence(t, s, todo.ID)
	requireOneOwnerInteraction(t, view, ix.ID)
	if got := s.pendingInteractions(); len(got) != 1 || got[0].ID != ix.ID {
		t.Fatalf("the existing prompt was duplicated or replaced: %+v", got)
	}
	pending, ok := s.pendingIx(ix.ID)
	if !ok || len(pending.Questions) != 1 || len(pending.Questions[0].Options) != 2 ||
		pending.Questions[0].Options[1].Action != proto.QuestionOptionDefer || pending.Delegate == nil ||
		pending.Delegate.Deadline == nil || !pending.Delegate.Deadline.Equal(*ix.Delegate.Deadline) {
		t.Fatalf("the explicit defer choice or its schedule changed: %+v", pending)
	}

	sends := len(sentToAgent(f))
	body, _ := json.Marshal(map[string]any{"answers": map[string]string{ix.Questions[0].Question: "Later"}})
	rec := httptest.NewRecorder()
	s.fleetHandler(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+ix.ID+"/resolve", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("answering the linked prompt through its existing route: %d %s", rec.Code, rec.Body.String())
	}
	status, resolution, err := store.InteractionOutcome(context.Background(), ix.ID)
	if err != nil || status != "resolved" || resolution == nil || resolution.Answers[ix.Questions[0].Question] != "Later" {
		t.Fatalf("the exact interaction was not resolved once: status=%q resolution=%+v err=%v", status, resolution, err)
	}
	if len(sentToAgent(f)) != sends {
		t.Fatal("resolving the existing prompt generated a duplicate Agent send")
	}
	_, view = listedParkEvidence(t, s, todo.ID)
	asks, _ := view["asks"].([]any)
	if view["section"] != "waiting" || view["reason"] != "parked" || len(asks) != 0 {
		t.Fatalf("the resolved prompt stayed actionable or discarded the park fallback: %v", view)
	}
}
