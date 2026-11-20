package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The owner's session filing book (#11391). These tests use the HTTP contract and the settings store, not the
// filing book's implementation, so an older manager fails at the missing routes rather than at future symbols.
const directionSessionsSettingKey = "assistant.direction_sessions"

// directionFilingAnswer: Direction and Previous are null when the book has no entry (#11458).
type directionFilingAnswer struct {
	Status    int     `json:"-"`
	Direction *string `json:"direction"`
	Previous  *string `json:"previous"`
	Changed   bool    `json:"changed"`
	Code      string  `json:"code"`
	Error     string  `json:"error"`
	Raw       string  `json:"-"`
}

// directionNull is how the answers' null is shown in the tests' messages.
const directionNull = "<null>"

// directionValue is a nullable answer field, directionNull for null.
func directionValue(v *string) string {
	if v == nil {
		return directionNull
	}
	return *v
}

type directionSessionView struct {
	MachineID string     `json:"machine_id"`
	SessionID string     `json:"session_id"`
	Title     string     `json:"title"`
	Source    string     `json:"source"`
	At        *time.Time `json:"at"`
	State     string     `json:"state"`
}

type directionEndedTaskView struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	State       string     `json:"state"`
	DoneAt      *time.Time `json:"done_at"`
	CancelledAt *time.Time `json:"cancelled_at"`
}

type directionReadAnswer struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	EndedAt    *time.Time               `json:"ended_at"`
	Notes      string                   `json:"notes"`
	NotesAt    *time.Time               `json:"notes_at"`
	Progress   *leaderProgress          `json:"progress"`
	Sessions   []directionSessionView   `json:"sessions"`
	EndedTasks []directionEndedTaskView `json:"ended_tasks"`
	More       bool                     `json:"more"`
	// SessionsUnavailable (#11458): what places sessions could not be read; the ended tasks are still there.
	SessionsUnavailable bool `json:"sessions_unavailable"`
}

// directionFilingEnv has two current directions, a main chat, a background supervisor, ordinary chats in this
// manager's own and health spaces, and a chat belonging to another manager. All ids and titles are placeholders.
func directionFilingEnv(t *testing.T, st Store) *todoEventEnv {
	t.Helper()
	if st == nil {
		st = &memStore{}
	}
	e := newTodoEventEnv(t, st)
	if err := e.s.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	setSettings(t, e.s, func(v *Settings) {
		v.MachineID, v.Main = "server", treeMain
		v.Leaders = []string{"conv_lead_a", "conv_lead_b"}
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "conv_background", "server"
	})
	e.title("conv_lead_a", "1110 | 功能 | Android parity")
	e.title("conv_lead_b", "1110 | 功能 | Push relay")
	e.syncLeaders()
	directionFilingSetSessions(e)
	return e
}

func directionFilingSetSessions(e *todoEventEnv, extra ...proto.Session) {
	list := []proto.Session{
		{MachineID: "server", SessionID: treeMain, Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, Title: "Main"},
		{MachineID: "server", SessionID: "conv_lead_a", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, Title: "Android parity"},
		{MachineID: "server", SessionID: "conv_lead_b", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, Title: "Push relay"},
		{MachineID: "server", SessionID: "conv_background", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, Title: "Background"},
		{MachineID: "server", SessionID: "chat-a", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, State: proto.StateIdle, Title: "Original chat title"},
		{MachineID: "server", SessionID: "chat-b", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, State: proto.StateIdle, Title: "Second chat"},
		{MachineID: "server", SessionID: "chat-opened", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, State: proto.StateIdle, Title: "Opened by a leader"},
		{MachineID: "server", SessionID: "chat-health", Cwd: "/home/u/.persona/health", Assistant: true, Space: SpaceHealth, Status: proto.StatusIdle, Title: "Health chat"},
		{MachineID: "server", SessionID: "chat-other-manager", Cwd: "/home/u/.persona/assistant-m-0d0d0d", Assistant: true, Status: proto.StatusIdle, Title: "Another manager's chat"},
	}
	list = append(list, extra...)
	e.f.setSessions("server", list...)
}

func directionFilingPut(s *Service, role, lang, machine, session, direction string) directionFilingAnswer {
	body, _ := json.Marshal(map[string]string{"direction": direction})
	return directionFilingPutBody(s, role, lang, machine, session, string(body))
}

// directionFilingPutBody is the write with body as it is ({"direction":null} takes the entry out).
func directionFilingPutBody(s *Service, role, lang, machine, session, body string) directionFilingAnswer {
	r := httptest.NewRequest(http.MethodPut, "/api/v1/assistant/sessions/"+machine+"/"+session+"/direction", strings.NewReader(body))
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	if lang != "" {
		r.Header.Set("Accept-Language", lang)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var out directionFilingAnswer
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	out.Status, out.Raw = w.Code, w.Body.String()
	return out
}

func directionFilingMustPut(t *testing.T, s *Service, machine, session, direction string) directionFilingAnswer {
	t.Helper()
	out := directionFilingPut(s, roleFull, "", machine, session, direction)
	if out.Status != http.StatusOK {
		t.Fatalf("file %s/%s: got %d, want 200: %s", machine, session, out.Status, out.Raw)
	}
	if directionValue(out.Direction) != direction {
		t.Fatalf("file %s/%s: direction %q, want %q: %s", machine, session, directionValue(out.Direction), direction, out.Raw)
	}
	return out
}

// directionFilingMustRemove takes the session's entry out ({"direction":null}).
func directionFilingMustRemove(t *testing.T, s *Service, machine, session string) directionFilingAnswer {
	t.Helper()
	out := directionFilingPutBody(s, roleFull, "", machine, session, `{"direction":null}`)
	if out.Status != http.StatusOK || out.Direction != nil {
		t.Fatalf("remove %s/%s: got %d: %s", machine, session, out.Status, out.Raw)
	}
	return out
}

// directionUndo repeats the write with the answer's previous: null takes the entry out again.
func directionUndo(t *testing.T, s *Service, machine, session string, filed directionFilingAnswer) directionFilingAnswer {
	t.Helper()
	if filed.Previous == nil {
		return directionFilingMustRemove(t, s, machine, session)
	}
	return directionFilingMustPut(t, s, machine, session, *filed.Previous)
}

func directionReadGet(t *testing.T, s *Service, role, path string) (int, directionReadAnswer, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var out directionReadAnswer
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func directionMustRead(t *testing.T, s *Service, role, id string) directionReadAnswer {
	t.Helper()
	code, out, raw := directionReadGet(t, s, role, "/api/v1/assistant/directions/"+id)
	if code != http.StatusOK {
		t.Fatalf("read direction %s: %d %s", id, code, raw)
	}
	return out
}

func directionBookSnapshot(t *testing.T, s *Service) string {
	t.Helper()
	var raw json.RawMessage
	found, err := s.store().Setting(context.Background(), directionSessionsSettingKey, &raw)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return ""
	}
	return string(raw)
}

func directionSessionIDs(list []directionSessionView) []string {
	out := make([]string, 0, len(list))
	for _, session := range list {
		out = append(out, session.SessionID)
	}
	slices.Sort(out)
	return out
}

func directionFindSession(t *testing.T, list []directionSessionView, id string) directionSessionView {
	t.Helper()
	for _, session := range list {
		if session.SessionID == id {
			return session
		}
	}
	t.Fatalf("session %s is not listed: %+v", id, list)
	return directionSessionView{}
}

// directionFilingFleetEnv uses an absorbed computer id on writes. The canonical id is what must reach the book and
// every later read, including after a restart.
func directionFilingFleetEnv(t *testing.T, st *fleetMem) *fleetTest {
	t.Helper()
	return directionFilingFleetEnvOn(t, st, st)
}

// directionFilingFleetEnvOn is directionFilingFleetEnv on store, which keeps its data in st (a wrapper of st that
// watches its writes).
func directionFilingFleetEnvOn(t *testing.T, store Store, st *fleetMem) *fleetTest {
	t.Helper()
	ft := newFleetTestOn(t, store)
	ft.st = st
	ft.s.devs = &aliasDevs{fleetDevs: ft.d, alias: map[string]string{"old-mac": "mac", "a-mac": "mac"}}
	ft.f.hello.Assistant.MachineID = "mac"
	ft.f.hello.Assistant.Machines = []string{"mac"}
	setSettings(t, ft.s, func(v *Settings) {
		v.MachineID, v.Main = "mac", treeMain
		v.Leaders = []string{"conv_lead_a", "conv_lead_b"}
	})
	convs := []proto.Session{
		{MachineID: "mac", SessionID: treeMain, Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, Title: "Main"},
		{MachineID: "mac", SessionID: "conv_lead_a", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, Title: "Android parity"},
		{MachineID: "mac", SessionID: "conv_lead_b", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, Title: "Push relay"},
		{MachineID: "mac", SessionID: "chat-a", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, Title: "Original chat title"},
	}
	ft.sessions("mac", convs...)
	ft.f.setSessions("mac", convs...)
	ft.s.directionsSync(context.Background())
	return ft
}

func TestDirectionFilingPersistsMovesOnOwnAndUndoAcrossRestart(t *testing.T) {
	ft := directionFilingFleetEnv(t, newFleetMem())
	st := ft.st

	first := directionFilingMustPut(t, ft.s, "old-mac", "chat-a", "conv_lead_a")
	if first.Previous != nil || !first.Changed {
		t.Fatalf("first filing: %+v", first)
	}
	var book map[string]struct {
		Direction string    `json:"direction"`
		By        string    `json:"by"`
		At        time.Time `json:"at"`
		Title     string    `json:"title"`
	}
	found, err := st.Setting(context.Background(), directionSessionsSettingKey, &book)
	if err != nil || !found {
		t.Fatalf("filing book: found=%v err=%v", found, err)
	}
	record, ok := book[runtimeListID("mac", "chat-a")]
	if !ok || record.Direction != "conv_lead_a" || record.By != titleByUser || record.At.IsZero() || record.Title != "Original chat title" {
		t.Fatalf("canonical owner record: %+v, found=%v; book=%+v", record, ok, book)
	}
	if _, exists := book[runtimeListID("old-mac", "chat-a")]; exists {
		t.Fatalf("absorbed computer id was persisted: %+v", book)
	}

	// A restart reads the filing, not a cached decorated session row.
	ft = directionFilingFleetEnv(t, st)
	read := directionMustRead(t, ft.s, "readonly", "conv_lead_a")
	filed := directionFindSession(t, read.Sessions, "chat-a")
	if filed.MachineID != "mac" || filed.Title != "Original chat title" || filed.At == nil || filed.Source == "" {
		t.Fatalf("filed session after restart: %+v", filed)
	}

	moved := directionFilingMustPut(t, ft.s, "old-mac", "chat-a", "conv_lead_b")
	if directionValue(moved.Previous) != "conv_lead_a" || !moved.Changed {
		t.Fatalf("move: %+v", moved)
	}
	ft = directionFilingFleetEnv(t, st)
	if got := directionMustRead(t, ft.s, "", "conv_lead_b"); len(got.Sessions) != 1 || got.Sessions[0].SessionID != "chat-a" {
		t.Fatalf("move did not survive restart: %+v", got.Sessions)
	}

	// Undo is the same write with the previous effective placement.
	undo := directionUndo(t, ft.s, "old-mac", "chat-a", moved)
	if directionValue(undo.Direction) != "conv_lead_a" || directionValue(undo.Previous) != "conv_lead_b" || !undo.Changed {
		t.Fatalf("undo move: %+v", undo)
	}
	onOwn := directionFilingMustPut(t, ft.s, "old-mac", "chat-a", "")
	if directionValue(onOwn.Previous) != "conv_lead_a" || !onOwn.Changed {
		t.Fatalf("on its own: %+v", onOwn)
	}
	// Make the on-own choice observable: without that record this recorded origin would put the chat under A.
	ft.s.originSet(context.Background(), "mac", "chat-a", OriginSpawned, "conv_lead_a", "")
	ft = directionFilingFleetEnv(t, st)
	if got := directionMustRead(t, ft.s, "", "conv_lead_a"); slices.Contains(directionSessionIDs(got.Sessions), "chat-a") {
		t.Fatalf("on-own did not survive restart and override origin: %+v", got.Sessions)
	}
	restored := directionUndo(t, ft.s, "old-mac", "chat-a", onOwn)
	if directionValue(restored.Previous) != "" || !restored.Changed {
		t.Fatalf("undo on-own: %+v", restored)
	}
	if got := directionMustRead(t, ft.s, "", "conv_lead_a"); !slices.Contains(directionSessionIDs(got.Sessions), "chat-a") {
		t.Fatalf("undo did not restore the earlier placement: %+v", got.Sessions)
	}
}

type directionFailingStore struct {
	*memStore
	failWrites bool
}

func (st *directionFailingStore) PutSetting(ctx context.Context, key string, value any) error {
	if st.failWrites && key == directionSessionsSettingKey {
		return errors.New("synthetic direction book write failure")
	}
	return st.memStore.PutSetting(ctx, key, value)
}

func TestDirectionFilingWriteFailureIsAtomic(t *testing.T) {
	st := &directionFailingStore{memStore: &memStore{}}
	e := directionFilingEnv(t, st)
	directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")
	before := directionBookSnapshot(t, e.s)
	st.failWrites = true
	out := directionFilingPut(e.s, roleFull, "en", "server", "chat-a", "conv_lead_b")
	if out.Status != http.StatusInternalServerError && out.Status != http.StatusServiceUnavailable {
		t.Fatalf("failed save: got %d, want 500 or 503: %s", out.Status, out.Raw)
	}
	if out.Error == "" {
		t.Fatalf("failed save lacked an error: %s", out.Raw)
	}
	if got := directionBookSnapshot(t, e.s); got != before {
		t.Fatalf("failed save changed durable book:\nbefore %s\nafter  %s", before, got)
	}
	st.failWrites = false
	// The failed in-memory change must not survive the next lazy load.
	if got := directionMustRead(t, e.s, "", "conv_lead_a"); !slices.Contains(directionSessionIDs(got.Sessions), "chat-a") {
		t.Fatalf("failed save changed current placement: %+v", got.Sessions)
	}
	if got := directionMustRead(t, e.s, "", "conv_lead_b"); slices.Contains(directionSessionIDs(got.Sessions), "chat-a") {
		t.Fatalf("failed save leaked into the new direction: %+v", got.Sessions)
	}
	retry := directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_b")
	if directionValue(retry.Previous) != "conv_lead_a" || !retry.Changed {
		t.Fatalf("retry after failed save: %+v", retry)
	}
}

func TestDirectionFilingLimitExcludesOnOwn(t *testing.T) {
	e := directionFilingEnv(t, nil)
	var extra []proto.Session
	ids := []string{"chat-on-own"}
	for i := range 51 {
		ids = append(ids, fmt.Sprintf("chat-limit-%02d", i))
	}
	for _, id := range ids {
		extra = append(extra, proto.Session{MachineID: "server", SessionID: id, Cwd: "/home/u/.persona/assistant", Assistant: true,
			Status: proto.StatusIdle, Title: id})
	}
	directionFilingSetSessions(e, extra...)
	directionFilingMustPut(t, e.s, "server", ids[0], "")
	for _, id := range ids[1:51] {
		directionFilingMustPut(t, e.s, "server", id, "conv_lead_a")
	}
	if got := directionMustRead(t, e.s, "", "conv_lead_a"); len(got.Sessions) != 50 {
		t.Fatalf("direction holds %d sessions, want 50", len(got.Sessions))
	}
	before := directionBookSnapshot(t, e.s)
	var code string
	reasons := map[string]string{}
	for _, lang := range []string{"en", "zh-Hans", "ja"} {
		out := directionFilingPut(e.s, roleFull, lang, "server", ids[51], "conv_lead_a")
		if out.Status != http.StatusConflict {
			t.Fatalf("%s: 51st session: got %d, want 409: %s", lang, out.Status, out.Raw)
		}
		if out.Code == "" || out.Error == "" {
			t.Fatalf("%s: 51st session was not a coded refusal: %s", lang, out.Raw)
		}
		if code == "" {
			code = out.Code
		} else if out.Code != code {
			t.Fatalf("limit code changed by language: %q then %q", code, out.Code)
		}
		reasons[lang] = out.Error
		if got := directionBookSnapshot(t, e.s); got != before {
			t.Fatalf("%s: refused 51st session changed the book", lang)
		}
	}
	if len(reasons) != 3 || reasons["en"] == reasons["zh-Hans"] || reasons["en"] == reasons["ja"] || reasons["zh-Hans"] == reasons["ja"] {
		t.Fatalf("limit reason is not localized in en, zh-Hans and ja: %+v", reasons)
	}
}

func TestDirectionFilingRefusals(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		machine string
		session string
		target  string
		prepare func(*todoEventEnv)
	}{
		{name: "unknown computer", status: http.StatusNotFound, machine: "computer-missing", session: "chat-a", target: "conv_lead_a"},
		{name: "unknown session", status: http.StatusNotFound, machine: "server", session: "chat-missing", target: "conv_lead_a"},
		{name: "unknown direction", status: http.StatusNotFound, machine: "server", session: "chat-b", target: "direction-missing"},
		{name: "ended direction", status: http.StatusConflict, machine: "server", session: "chat-b", target: "conv_lead_b", prepare: func(e *todoEventEnv) {
			if code, _ := doRoute(t, e.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_b/end", "", roleFull); code != http.StatusOK {
				t.Fatalf("end direction: %d", code)
			}
			e.s.directionsSync(context.Background())
		}},
		{name: "agent on supervised open task", status: http.StatusConflict, machine: "box", session: "agent-supervised", target: "conv_lead_a", prepare: func(e *todoEventEnv) {
			e.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "agent-supervised", Agent: "claude", Status: proto.StatusWorking, State: proto.StateWorking})
			id := e.task("Supervised task", func(todo *Todo) {
				todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
				todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "agent-supervised"}}
			})
			e.s.todos.mu.Lock()
			e.s.todos.activeBySession[[2]string{"box", "agent-supervised"}] = id
			e.s.todos.mu.Unlock()
		}},
		{name: "main conversation", status: http.StatusConflict, machine: "server", session: treeMain, target: "conv_lead_a"},
		{name: "background supervisor", status: http.StatusConflict, machine: "server", session: "conv_background", target: "conv_lead_a"},
		{name: "direction leader conversation", status: http.StatusConflict, machine: "server", session: "conv_lead_b", target: "conv_lead_a"},
		{name: "cross-space chat", status: http.StatusConflict, machine: "server", session: "chat-health", target: "conv_lead_a"},
		{name: "session owned by another manager", status: http.StatusNotFound, machine: "server", session: "chat-other-manager", target: "conv_lead_a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := directionFilingEnv(t, nil)
			// Every refusal must preserve both an existing filing and the rest of the book.
			directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")
			if tc.prepare != nil {
				tc.prepare(e)
			}
			before := directionBookSnapshot(t, e.s)
			var code string
			reasons := map[string]string{}
			for _, lang := range []string{"en", "zh-Hans", "ja"} {
				out := directionFilingPut(e.s, roleFull, lang, tc.machine, tc.session, tc.target)
				if out.Status != tc.status {
					t.Fatalf("%s: got %d, want %d: %s", lang, out.Status, tc.status, out.Raw)
				}
				if out.Code == "" || out.Error == "" {
					t.Fatalf("%s: refusal has no code and localized reason: %s", lang, out.Raw)
				}
				if code == "" {
					code = out.Code
				} else if out.Code != code {
					t.Fatalf("code changed by language: %q then %q", code, out.Code)
				}
				reasons[lang] = out.Error
				if got := directionBookSnapshot(t, e.s); got != before {
					t.Fatalf("%s: refusal changed the book:\nbefore %s\nafter  %s", lang, before, got)
				}
			}
			if reasons["en"] == reasons["zh-Hans"] || reasons["en"] == reasons["ja"] || reasons["zh-Hans"] == reasons["ja"] {
				t.Fatalf("reason is not localized in en, zh-Hans and ja: %+v", reasons)
			}
		})
	}
}

func TestDirectionFilingRequiresFullDeviceAndDirectionReadAllowsReadOnly(t *testing.T) {
	e := directionFilingEnv(t, nil)
	for _, role := range []string{"answer", "readonly", roleExtension} {
		before := directionBookSnapshot(t, e.s)
		out := directionFilingPut(e.s, role, "", "server", "chat-a", "conv_lead_a")
		if out.Status != http.StatusForbidden {
			t.Fatalf("%s: got %d, want 403: %s", role, out.Status, out.Raw)
		}
		if got := directionBookSnapshot(t, e.s); got != before {
			t.Fatalf("%s changed the book", role)
		}
	}
	if got := directionMustRead(t, e.s, "readonly", "conv_lead_a"); got.ID != "conv_lead_a" {
		t.Fatalf("read-only current direction: %+v", got)
	}
	if code, _ := doRoute(t, e.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_a/end", "", roleFull); code != http.StatusOK {
		t.Fatalf("end: %d", code)
	}
	e.s.directionsSync(context.Background())
	ended := directionMustRead(t, e.s, "readonly", "conv_lead_a")
	if ended.EndedAt == nil {
		t.Fatalf("read-only ended direction has no ended_at: %+v", ended)
	}
}

func TestDirectionFilingHasNoModelSendOrWakeSideEffects(t *testing.T) {
	e := directionFilingEnv(t, nil)
	modelCalls := 0
	e.s.offers.ask = func(context.Context, string, string, any) error {
		modelCalls++
		return errors.New("filing must not call a model")
	}
	e.f.mu.Lock()
	sends, agentSends, onceSends := len(e.f.sends), len(e.f.agentSent), len(e.f.sentOnce)
	starts, pushes, asked := len(e.f.starts), len(e.f.pushes), len(e.f.asked)
	e.f.mu.Unlock()

	directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")

	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	if modelCalls != 0 || len(e.f.asked) != asked {
		t.Fatalf("filing called a model: route=%d, gateway=%d", modelCalls, len(e.f.asked)-asked)
	}
	if len(e.f.sends) != sends || len(e.f.agentSent) != agentSends || len(e.f.sentOnce) != onceSends {
		t.Fatalf("filing sent to a conversation or Agent: assistant=%d agent=%d once=%d",
			len(e.f.sends)-sends, len(e.f.agentSent)-agentSends, len(e.f.sentOnce)-onceSends)
	}
	if len(e.f.starts) != starts || len(e.f.pushes) != pushes {
		t.Fatalf("filing started or woke someone: starts=%d pushes=%d", len(e.f.starts)-starts, len(e.f.pushes)-pushes)
	}
}

func TestDirectionFilingPreviousIgnoresNonDirectionOrigin(t *testing.T) {
	e := directionFilingEnv(t, nil)
	// conversation_new records any opener, not only a direction. An ordinary opener was not a direction placement.
	e.s.originSet(context.Background(), "server", "chat-opened", OriginSpawned, "chat-b", "")
	filed := directionFilingMustPut(t, e.s, "server", "chat-opened", "conv_lead_a")
	if filed.Previous != nil {
		t.Fatalf("ordinary opener became a previous direction: %s", filed.Raw)
	}
	undo := directionUndo(t, e.s, "server", "chat-opened", filed)
	if undo.Direction != nil || directionValue(undo.Previous) != "conv_lead_a" || !undo.Changed {
		t.Fatalf("undo did not restore the ordinary opener's placement: %s", undo.Raw)
	}
	if directionBookHas(t, e.s, "server", "chat-opened") {
		t.Fatalf("undo of a first filing left an entry: %s", directionBookSnapshot(t, e.s))
	}
}

// directionBookHas: the stored book has an entry for machine/session under its own key that places it. An entry taken
// out is kept, marked removed_at, only for its Undo (#11458): that is no entry.
func directionBookHas(t *testing.T, s *Service, machine, session string) bool {
	t.Helper()
	var book map[string]map[string]json.RawMessage
	if _, err := s.store().Setting(context.Background(), directionSessionsSettingKey, &book); err != nil {
		t.Fatal(err)
	}
	entry, ok := book[runtimeListID(machine, session)]
	_, removed := entry["removed_at"]
	return ok && !removed
}

func TestDirectionFilingAddsNoMCPToolAndSessionsChangeDoesNotFile(t *testing.T) {
	e := directionFilingEnv(t, nil)
	for _, tool := range append(e.s.Tools(context.Background()), e.s.AgentTools()...) {
		if strings.Contains(tool.Name, "direction") && tool.Name != "direction_name" && tool.Name != "leader_progress" {
			t.Fatalf("filing gained an MCP tool or prompt: %s", tool.Name)
		}
	}
	before := directionBookSnapshot(t, e.s)
	e.s.SessionsChanged(context.Background(), DeviceSessions{MachineID: "server", Sessions: []proto.Session{{
		MachineID: "server", SessionID: "chat-never-automatic", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle,
	}}})
	if got := directionBookSnapshot(t, e.s); got != before {
		t.Fatalf("a computer report filed or refiled a session without the owner:\nbefore %s\nafter  %s", before, got)
	}
}
