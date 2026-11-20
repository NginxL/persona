package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

type directionFilingTreeDirection struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Secretary bool                   `json:"secretary"`
	Counts    treeCounts             `json:"counts"`
	Running   bool                   `json:"running"`
	Tasks     []treeTask             `json:"tasks"`
	Sessions  []directionSessionView `json:"sessions"`
}

func directionFilingTreeGet(t *testing.T, s *Service) ([]directionFilingTreeDirection, string) {
	t.Helper()
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/directions/tree", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("tree: %d %s", w.Code, w.Body)
	}
	var out struct {
		Directions []directionFilingTreeDirection `json:"directions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Directions, w.Body.String()
}

func directionFilingTreeMaps(t *testing.T, raw string) []map[string]json.RawMessage {
	t.Helper()
	var out struct {
		Directions []map[string]json.RawMessage `json:"directions"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out.Directions
}

func TestDirectionTreeSessionsAreAdditiveActiveOnlyAndCountsUnchanged(t *testing.T) {
	e := directionFilingEnv(t, nil)
	beforeDirections, beforeRaw := directionFilingTreeGet(t, e.s)
	if len(beforeDirections) != 3 {
		t.Fatalf("fixture directions: %+v", beforeDirections)
	}

	directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")
	directionFilingMustPut(t, e.s, "server", "chat-b", "conv_lead_a") // quiet: direction read only
	e.s.originSet(context.Background(), "server", "chat-opened", OriginSpawned, "conv_lead_a", "")
	e.f.mu.Lock()
	for i := range e.f.sessions["server"] {
		switch e.f.sessions["server"][i].SessionID {
		case "chat-a":
			e.f.sessions["server"][i].Status, e.f.sessions["server"][i].State = proto.StatusWorking, proto.StateWorking
		case "chat-opened":
			e.f.sessions["server"][i].State = proto.StateNeedsYou
		}
	}
	e.f.mu.Unlock()

	afterDirections, afterRaw := directionFilingTreeGet(t, e.s)
	a := afterDirections[0]
	if got, want := directionSessionIDs(a.Sessions), []string{"chat-a", "chat-opened"}; !slices.Equal(got, want) {
		t.Fatalf("active direction sessions: got %v, want %v; all=%+v", got, want, a.Sessions)
	}
	if a.Counts != beforeDirections[0].Counts || a.Running != beforeDirections[0].Running || !reflect.DeepEqual(a.Tasks, beforeDirections[0].Tasks) {
		t.Fatalf("filings changed counts/running/tasks: before=%+v after=%+v", beforeDirections[0], a)
	}
	if got := directionMustRead(t, e.s, "", "conv_lead_a"); !slices.Contains(directionSessionIDs(got.Sessions), "chat-b") {
		t.Fatalf("quiet session disappeared from the direction read: %+v", got.Sessions)
	}

	// Remove only the additive field: every old direction field remains byte-for-byte equivalent. A known-key
	// decoder also accepts the new payload, as older clients do.
	beforeMaps, afterMaps := directionFilingTreeMaps(t, beforeRaw), directionFilingTreeMaps(t, afterRaw)
	if len(beforeMaps) != len(afterMaps) {
		t.Fatalf("direction count changed: %d then %d", len(beforeMaps), len(afterMaps))
	}
	type legacyDirection struct {
		ID        string     `json:"id"`
		Name      string     `json:"name"`
		Secretary bool       `json:"secretary"`
		Counts    treeCounts `json:"counts"`
		Running   bool       `json:"running"`
		Tasks     []treeTask `json:"tasks"`
	}
	for i := range beforeMaps {
		additiveRaw, _ := json.Marshal(afterMaps[i])
		delete(beforeMaps[i], "sessions")
		delete(afterMaps[i], "sessions")
		if !reflect.DeepEqual(beforeMaps[i], afterMaps[i]) {
			t.Fatalf("old fields changed at %d:\nbefore=%v\nafter=%v", i, beforeMaps[i], afterMaps[i])
		}
		var legacy legacyDirection
		if err := json.Unmarshal(additiveRaw, &legacy); err != nil || legacy.ID != afterDirections[i].ID {
			t.Fatalf("known-key decoder rejected additive sessions at %d: %+v, %v", i, legacy, err)
		}
	}
}

func TestDirectionTreeOwnTodoAgentNotDuplicatedButReadKeepsFiling(t *testing.T) {
	e := directionFilingEnv(t, nil)
	e.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "agent-own-todo", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, Title: "Own to-do agent"})
	directionFilingMustPut(t, e.s, "box", "agent-own-todo", "conv_lead_a")
	todoID := e.task("Owner's own to-do", func(todo *Todo) {
		todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "agent-own-todo"}}
	})

	directions, _ := directionFilingTreeGet(t, e.s)
	if slices.Contains(directionSessionIDs(directions[0].Sessions), "agent-own-todo") {
		t.Fatalf("own-to-do Agent duplicated in direction sessions: %+v", directions[0].Sessions)
	}
	secretary := directions[len(directions)-1]
	if !secretary.Secretary || !slices.ContainsFunc(secretary.Tasks, func(task treeTask) bool { return task.ID == todoID }) {
		t.Fatalf("own to-do left the secretary group: %+v", secretary)
	}
	if got := directionMustRead(t, e.s, "", "conv_lead_a"); !slices.Contains(directionSessionIDs(got.Sessions), "agent-own-todo") {
		t.Fatalf("owner filing lost from direction read: %+v", got.Sessions)
	}
}

func TestDirectionTreeFiledSessionStateChangePublishes(t *testing.T) {
	e := directionFilingEnv(t, nil)
	e.clock.advance(time.Minute)
	directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")
	ctx := context.Background()
	e.s.directionTreeSync(ctx)
	first := e.directionsEvents()

	e.f.mu.Lock()
	for i := range e.f.sessions["server"] {
		if e.f.sessions["server"][i].SessionID == "chat-a" {
			e.f.sessions["server"][i].State = proto.StateNeedsYou
		}
	}
	changed := slices.Clone(e.f.sessions["server"])
	e.f.mu.Unlock()
	// There are no task Agents on this computer. Only its filed session can require the rebuild.
	e.s.SessionsChanged(ctx, DeviceSessions{MachineID: "server", Sessions: changed})
	e.s.dtree.mu.Lock()
	armed := e.s.dtree.armed
	e.s.dtree.mu.Unlock()
	if !armed {
		t.Fatal("a filed session's state change did not schedule a tree rebuild")
	}
	e.clock.advance(directionTreeSettle)
	if got := e.directionsEvents(); got != first+1 {
		t.Fatalf("directions events: got %d, want %d", got, first+1)
	}
}

func TestDirectionFilingDeletedWithAssistantConversation(t *testing.T) {
	e := directionFilingEnv(t, nil)
	directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")
	code, _ := doRoute(t, e.s, http.MethodDelete, "/api/v1/assistant/conversations/chat-a", `{"machine_id":"server"}`, roleFull)
	if code != http.StatusOK {
		t.Fatalf("delete conversation: %d", code)
	}
	if got := directionMustRead(t, e.s, "", "conv_lead_a"); slices.Contains(directionSessionIDs(got.Sessions), "chat-a") {
		t.Fatalf("deleted conversation kept its direction row: %+v", got.Sessions)
	}
	var book map[string]json.RawMessage
	found, err := e.s.store().Setting(context.Background(), directionSessionsSettingKey, &book)
	if err != nil || !found {
		t.Fatalf("read filing book: found=%v err=%v", found, err)
	}
	if _, ok := book[runtimeListID("server", "chat-a")]; ok {
		t.Fatalf("deleted conversation kept its filing: %+v", book)
	}
}

func TestDirectionFilingDeletedLeaderOpenedConversationNotListed(t *testing.T) {
	e := directionFilingEnv(t, nil)
	// Origins are retained for history, but a deleted conversation must not reappear through that origin.
	e.s.originSet(context.Background(), "server", "chat-opened", OriginSpawned, "conv_lead_a", "")
	code, _ := doRoute(t, e.s, http.MethodDelete, "/api/v1/assistant/conversations/chat-opened", `{"machine_id":"server"}`, roleFull)
	if code != http.StatusOK {
		t.Fatalf("delete leader-opened conversation: %d", code)
	}
	if got := directionMustRead(t, e.s, "", "conv_lead_a"); slices.Contains(directionSessionIDs(got.Sessions), "chat-opened") {
		t.Fatalf("deleted leader-opened conversation was resurrected by its origin: %+v", got.Sessions)
	}
}

func TestDirectionFilingSurvivesEndingAndRemakingDirection(t *testing.T) {
	e := directionFilingEnv(t, nil)
	directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")
	if code, _ := doRoute(t, e.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_a/end", "", roleFull); code != http.StatusOK {
		t.Fatalf("end: %d", code)
	}
	e.s.directionsSync(context.Background())
	ended := directionMustRead(t, e.s, "readonly", "conv_lead_a")
	if ended.EndedAt == nil || !slices.Contains(directionSessionIDs(ended.Sessions), "chat-a") {
		t.Fatalf("ending dropped filings: %+v", ended)
	}
	if code, _ := doRoute(t, e.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_a/make", "", roleFull); code != http.StatusOK {
		t.Fatalf("make again: %d", code)
	}
	e.s.directionsSync(context.Background())
	current := directionMustRead(t, e.s, "", "conv_lead_a")
	if current.EndedAt != nil || !slices.Contains(directionSessionIDs(current.Sessions), "chat-a") {
		t.Fatalf("remaking did not restore filings: %+v", current)
	}
}

func TestDirectionFilingSurvivesHubAgentDeletionAsNotListed(t *testing.T) {
	e := directionFilingEnv(t, nil)
	e.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "agent-hub-deleted", Agent: "claude", Status: proto.StatusIdle, Title: "Deleted Agent"})
	directionFilingMustPut(t, e.s, "box", "agent-hub-deleted", "conv_lead_a")
	// sessions.deleted lives only in the hub. The manager observes exactly this: no row in the next computer list.
	e.f.setSessions("box")
	got := directionMustRead(t, e.s, "", "conv_lead_a")
	missing := directionFindSession(t, got.Sessions, "agent-hub-deleted")
	if missing.State != "not_listed" || missing.Title != "Deleted Agent" {
		t.Fatalf("reversibly hub-deleted Agent lost its filing: %+v", missing)
	}
}

func TestDirectionReadLiteralRoutesRemainTreeAndOffers(t *testing.T) {
	e := directionFilingEnv(t, nil)
	for path, key := range map[string]string{
		"/api/v1/assistant/directions/tree":   "directions",
		"/api/v1/assistant/directions/offers": "offers",
	} {
		w := httptest.NewRecorder()
		e.s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s captured by direction read: %d %s", path, w.Code, w.Body)
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out[key] == nil {
			t.Fatalf("%s lost its literal response: %v %s", path, err, w.Body)
		}
	}
}
