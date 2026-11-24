package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// outsideDirectionEnv is the event-capable assistant with a real file-backed knowledge base in a temporary directory.
func outsideDirectionEnv(t *testing.T, st *memStore) *todoEventEnv {
	t.Helper()
	f := &todoEventHub{fakeHub: newFakeHub().withMachine()}
	f.hello.KB = &KBSettings{}
	opts := Options{Hub: f, Logf: testLogf(t), KB: &KBOptions{Dir: t.TempDir()}}
	if st != nil {
		opts.Store = func() Store { return st }
	}
	s := New(opts)
	clock := newFakeClock()
	clock.install(s)
	s.tevents.after = clock.After
	s.directions.after, s.dtree.after = clock.After, clock.After
	s = withSettings(t, f.fakeHub, s)
	shortSupervise(t, s)
	t.Cleanup(s.kbEnd)
	e := &todoEventEnv{todoEnv: &todoEnv{t: t, s: s, f: f.fakeHub}, hub: f, clock: clock}
	if err := s.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	f.known["conv1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	if _, err := kbEng(t, s).Apply(kb.KindProject, "release", kb.Update{Name: "Release", Kind: "long"}); err != nil {
		t.Fatal(err)
	}
	return e
}

func outsideRequest(t *testing.T, e *todoEventEnv, role, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	return w
}

func outsideLeader(t *testing.T, w *httptest.ResponseRecorder) leaderView {
	t.Helper()
	var out struct {
		Leader leaderView `json:"leader"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("leader response: %v: %s", err, w.Body)
	}
	return out.Leader
}

func TestOutsideDirectionCreateProgressReadAndEvent(t *testing.T) {
	st := &memStore{}
	e := outsideDirectionEnv(t, st)
	e.f.setSessions("box", proto.Session{SessionID: "agent-1", MachineID: "box", Agent: "claude", Title: "Outside work"})

	for _, c := range []struct{ name, body string }{
		{"missing name", `{"project":"release"}`},
		{"missing project", `{"name":"Release parity"}`},
		{"unknown project", `{"name":"Release parity","project":"missing"}`},
		{"half an Agent", `{"name":"Release parity","project":"release","machine_id":"box"}`},
		{"unknown machine", `{"name":"Release parity","project":"release","machine_id":"other","session_id":"agent-1"}`},
		{"unknown Agent", `{"name":"Release parity","project":"release","machine_id":"box","session_id":"missing"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if w := outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions", c.body); w.Code != http.StatusBadRequest {
				t.Fatalf("create: %d %s", w.Code, w.Body)
			}
		})
	}
	if w := outsideRequest(t, e, "answer", http.MethodPost, "/api/v1/assistant/directions", `{"name":"Release parity","project":"release"}`); w.Code != http.StatusForbidden {
		t.Fatalf("read-only create: %d %s", w.Code, w.Body)
	}

	bound := outsideLeader(t, outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions",
		`{"name":"Release parity","project":"release","machine_id":"box","session_id":"agent-1"}`))
	if !strings.HasPrefix(bound.ID, "dir_") || !bound.External || bound.MachineID != "box" || bound.SessionID != "agent-1" ||
		bound.Title != "Release parity" || bound.Name != "Release parity" || bound.Project != "release" {
		t.Fatalf("created bound direction: %+v", bound)
	}
	standalone := outsideLeader(t, outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions",
		`{"name":"Standalone direction","project":"release"}`))
	if !standalone.External || standalone.MachineID != "" || standalone.SessionID != "" {
		t.Fatalf("created standalone direction: %+v", standalone)
	}
	if got := e.s.settings(context.Background()).Leaders; !slices.Equal(got, []string{bound.ID, standalone.ID}) {
		t.Fatalf("current directions: %q", got)
	}
	if w := outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions",
		`{"name":"Duplicate binding","project":"release","machine_id":"box","session_id":"agent-1"}`); w.Code != http.StatusConflict {
		t.Fatalf("duplicate binding: %d %s", w.Code, w.Body)
	}

	// Existing whole-list settings edits preserve created outside ids but still reject an arbitrary synthetic id.
	if got, err := e.s.leaderList(context.Background(), e.s.settings(context.Background()), []string{bound.ID, standalone.ID}); err != nil || len(got) != 2 {
		t.Fatalf("outside whole-list validation: %q %v", got, err)
	}
	if _, err := e.s.leaderList(context.Background(), e.s.settings(context.Background()), []string{"dir_invented"}); err == nil {
		t.Fatal("an arbitrary synthetic direction id was accepted")
	}
	kept := e.s.leadSessions(context.Background(), e.s.settings(context.Background()))
	if len(kept) != 1 || kept[0].MachineID != "box" || kept[0].SessionID != "agent-1" {
		t.Fatalf("kept sessions: %+v", kept)
	}
	snap := e.s.snapshot(context.Background())
	for _, id := range []string{bound.ID, standalone.ID} {
		if slices.ContainsFunc(snap.Sessions, func(v SessionView) bool { return v.SessionID == id }) {
			t.Fatalf("snapshot manufactured conversation %s", id)
		}
	}
	changed := e.syncLeaders()
	if len(changed) != 2 || changed[0].Title == "" || changed[1].Title == "" {
		t.Fatalf("outside leader.changed titles: %+v", changed)
	}

	progressBody := `{"reached":"  First milestone\nshipped  ","doing":"Verifying the next step","needs_you":"Review the evidence","links":["https://example.com/evidence","https://example.com/evidence"," https://example.com/notes "]}`
	if w := outsideRequest(t, e, "answer", http.MethodPost, "/api/v1/assistant/directions/"+standalone.ID+"/progress", progressBody); w.Code != http.StatusForbidden {
		t.Fatalf("read-only progress: %d %s", w.Code, w.Body)
	}
	updated := outsideLeader(t, outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions/"+standalone.ID+"/progress", progressBody))
	if updated.Progress == nil || updated.Progress.Reached != "First milestone shipped" || updated.Progress.Doing != "Verifying the next step" ||
		updated.Progress.NeedsYou != "Review the evidence" || updated.Progress.At.IsZero() || len(updated.Progress.Links) != 2 {
		t.Fatalf("updated progress: %+v", updated.Progress)
	}
	var reports []assistantwire.LeaderReportEvent
	for _, ev := range e.told() {
		if ev.name != assistantwire.EventLeaderReport {
			continue
		}
		var report assistantwire.LeaderReportEvent
		if err := json.Unmarshal(ev.raw, &report); err != nil {
			t.Fatal(err)
		}
		reports = append(reports, report)
	}
	if len(reports) != 1 || reports[0].Kind != assistantwire.LeaderProgress || reports[0].Leader != standalone.ID ||
		reports[0].Todo != "" || reports[0].Reached != "First milestone shipped" || reports[0].Link != "https://example.com/evidence" {
		t.Fatalf("progress event: %+v", reports)
	}

	read := outsideRequest(t, e, "", http.MethodGet, "/api/v1/assistant/leaders", "")
	var listed struct {
		Leaders []leaderView `json:"leaders"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Leaders) != 2 || listed.Leaders[1].Progress == nil || listed.Leaders[1].Progress.Reached != "First milestone shipped" {
		t.Fatalf("GET leaders: %s", read.Body)
	}
	tree := outsideRequest(t, e, "", http.MethodGet, "/api/v1/assistant/directions/tree", "")
	var treeOut struct {
		Directions []treeDirection `json:"directions"`
	}
	if err := json.Unmarshal(tree.Body.Bytes(), &treeOut); err != nil {
		t.Fatal(err)
	}
	if len(treeOut.Directions) != 3 || !treeOut.Directions[0].External || treeOut.Directions[0].SessionID != "agent-1" ||
		!treeOut.Directions[1].External || treeOut.Directions[1].SessionID != "" || len(treeOut.Directions[1].Tasks) != 0 {
		t.Fatalf("direction tree: %s", tree.Body)
	}

	// The same settings and binding rows survive a manager reload; no conversation appears meanwhile.
	restarted := outsideDirectionEnv(t, st)
	got := restarted.s.directionOf(standalone.ID)
	if got == nil || got.Outside == nil || got.Progress == nil || got.Progress.Reached != "First milestone shipped" {
		t.Fatalf("reloaded direction: %+v", got)
	}
	if got := restarted.s.leadSessions(context.Background(), restarted.s.settings(context.Background())); len(got) != 1 || got[0].SessionID != "agent-1" {
		t.Fatalf("reloaded keep list: %+v", got)
	}
}

func TestOutsideDirectionWriteFailuresAreNotSuccess(t *testing.T) {
	st := &memStore{}
	e := outsideDirectionEnv(t, st)
	created := outsideLeader(t, outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions", `{"name":"Release parity","project":"release"}`))
	e.syncLeaders()
	st.setFailPut(errors.New("write refused"))
	if w := outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions/"+created.ID+"/progress", `{"reached":"not durable"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed progress save: %d %s", w.Code, w.Body)
	}
	if got := e.s.directionOf(created.ID); got.Progress != nil {
		t.Fatalf("failed save stayed in memory: %+v", got.Progress)
	}
	if events := e.told(); len(events) != 0 {
		t.Fatalf("failed save emitted events: %+v", events)
	}
	before := slices.Clone(e.s.settings(context.Background()).Leaders)
	if w := outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions", `{"name":"Not created","project":"release"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed create save: %d %s", w.Code, w.Body)
	}
	if got := e.s.settings(context.Background()).Leaders; !slices.Equal(got, before) {
		t.Fatalf("failed create changed leaders: %q -> %q", before, got)
	}
	if len(st.outside) != 1 {
		t.Fatalf("failed create left an outside binding: %+v", st.outside)
	}
}

func TestOutsideDirectionSupervisionFallsBackToMain(t *testing.T) {
	e := outsideDirectionEnv(t, &memStore{})
	e.f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Agent: "claude", Status: proto.StatusIdle})
	created := outsideLeader(t, outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions", `{"name":"Release parity","project":"release"}`))
	e.f.judge = `{"verdict":"done","reason":"Evidence checked"}`
	todo := e.create("Check the outside work")
	e.s.todos.mu.Lock()
	e.s.todos.todos[todo.ID].Origin = "conv1"
	e.s.todos.todos[todo.ID].Quiet = true
	e.s.todos.mu.Unlock()
	e.must(http.MethodPut, "/api/v1/todos/"+todo.ID+"/agents/box/s1", "")
	e.s.superviseTodo(context.Background(), todo.ID, created.ID, []string{"Evidence is checked"}, 30)
	todo = onlyTodo(e.s)
	agentTurn(e.s, e.f, proto.StatusWorking, "", 0)
	agentTurn(e.s, e.f, proto.StatusDone, "Evidence is checked", 0)
	waitFor(t, "the main conversation's supervision message", func() bool { return len(e.f.noticesTo("conv1")) == 1 })
	if got := e.f.noticesTo("conv1"); !strings.Contains(got[0], todo.ID) {
		t.Fatalf("main supervision message: %q", got)
	}
	if got := e.f.noticesTo(created.ID); len(got) != 0 {
		t.Fatalf("the conversationless direction received a chat message: %q", got)
	}
}

func reportingDirection(t *testing.T, e *todoEventEnv) string {
	t.Helper()
	d, err := e.s.createOutsideDirection(t.Context(), directionCreateInput{Name: "Release watch", Project: "release"})
	if err != nil {
		t.Fatal(err)
	}
	return d.ID
}

func TestReportingOnlyDirectionSupervisorAssignment(t *testing.T) {
	e := outsideDirectionEnv(t, &memStore{})
	direction := reportingDirection(t, e)
	if candidates := e.s.routeLeaders(t.Context()); len(candidates) != 0 {
		t.Errorf("new Agent supervisor suggestions include a reporting-only direction: %+v", candidates)
	}
	e.f.setSessions("box", boxAgent("worker01"))
	task := onBoxTask(t, e.s, "worker01", "conv1", false, "Evidence checked")
	before := todoNow(e.s, task.ID)

	w := outsideRequest(t, e, "", http.MethodPost, "/api/v1/assistant/agents/box/worker01/supervisor",
		`{"supervisor":"`+direction+`"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("assigning the outside direction returned HTTP %d, want 409", w.Code)
	}
	if _, err := e.s.toolAgentTransfer(t.Context(), "conv1", transferArgs("box", "worker01", direction)); err == nil {
		t.Error("agent_transfer accepted the outside direction")
	}
	if _, err := e.s.planSupervisor(t.Context(), "conv1", direction); err == nil {
		t.Error("task_plan accepted the outside direction as an initial supervisor")
	}
	if got := todoNow(e.s, task.ID); got.Origin != before.Origin || !reflect.DeepEqual(got.Supervision, before.Supervision) {
		t.Errorf("rejected assignment changed task %s: %+v", task.ID, got.Supervision)
	}
}

func TestAssignedOutsideDirectionReviewFallsBackToMain(t *testing.T) {
	for _, mode := range []string{"idle_main", "busy_main", "already_queued"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			e := outsideDirectionEnv(t, &memStore{})
			direction := reportingDirection(t, e)
			task := onBoxTask(t, e.s, "worker01", "conv1", false, "Evidence checked")
			// This ledger predates assignment validation. The worker has finished and is waiting for review.
			v, _, err := e.s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
				v.Supervisor, v.SupervisorSource, v.State = direction, "explicit", reviewPending
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			main := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusIdle}
			switch mode {
			case "busy_main":
				main.Status = proto.StatusWorking
				e.f.setSessions("server", main)
			case "already_queued":
				g := superviseSignal{Todo: task.ID, To: direction, Kind: sigReview, Key: reviewKey(&v), At: v.UpdatedAt}
				// Old dispatch selected the direction. Queue admission also acknowledged this signal's fingerprint.
				e.s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[g.id()] = g })
				e.s.supervise.dedupMu.Lock()
				queued := e.s.deliverDigest(ctx, e.s.settings(ctx), direction, []superviseSignal{g}, "", time.Now(), time.Time{})
				e.s.supervise.dedupMu.Unlock()
				if !queued || e.s.queuedFor(direction) != 1 {
					t.Fatal("the legacy review did not reach the durable queue")
				}
				simulateSupervisionRestart(t, e.s)
			}
			e.s.superviseWatch(ctx, time.Now())
			e.s.sendDigest(ctx)
			if n := e.s.queuedFor(direction); n != 0 {
				t.Fatalf("%d messages stranded on the outside direction", n)
			}
			if mode == "busy_main" {
				if n := e.s.queuedFor("conv1"); n != 1 {
					t.Fatalf("busy main has %d queued messages, want one review", n)
				}
				main.Status = proto.StatusIdle
				e.f.setSessions("server", main)
				e.s.drainRelays("server", []proto.Session{main})
			}
			if mode != "idle_main" {
				waitFor(t, "main's review queue to drain", func() bool { return e.s.queuedFor("conv1") == 0 })
			}
			if got := e.f.noticesTo("conv1"); len(got) != 1 || !strings.Contains(got[0], task.ID) || !strings.Contains(got[0], "signal=review") {
				t.Fatalf("main's review reminders: %q", got)
			}
			task = todoNow(e.s, task.ID)
			review := map[string]any{"todo_id": task.ID, "verdict": "accepted", "checks": completeReviewChecks(task)}
			if _, err := runJSON(t, e.s, "unrelated", "task_review", review); err == nil {
				t.Fatal("an unrelated conversation could review the task")
			}
			if _, err := runJSON(t, e.s, "conv1", "task_review", review); err != nil {
				t.Fatalf("main could not review the task: %v", err)
			}
			if got := todoNow(e.s, task.ID); got.DoneAt == nil || got.Supervision.Supervisor != direction || got.Supervision.SupervisorSource != "explicit" {
				t.Fatalf("main's review changed the recorded assignment: %+v", got.Supervision)
			}
		})
	}
}

func TestOfflineAssignedSupervisorKeepsReview(t *testing.T) {
	e := outsideDirectionEnv(t, &memStore{})
	main := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusIdle}
	lead := proto.Session{MachineID: "server", SessionID: "lead1", Assistant: true, Status: proto.StatusIdle}
	e.f.mu.Lock()
	e.f.known[lead.SessionID] = true
	e.f.mu.Unlock()
	e.f.setSessions("server", main, lead)
	setSettings(t, e.s, func(v *Settings) { v.Leaders = []string{lead.SessionID} })
	e.syncLeaders()
	e.f.setSessions("box", boxAgent("worker01"))
	task := onBoxTask(t, e.s, "worker01", "conv1", false, "Evidence checked")
	e.f.setSessions("server", main) // the pinned conversation is temporarily absent from the node's session list
	w := outsideRequest(t, e, "", http.MethodPost, "/api/v1/assistant/agents/box/worker01/supervisor", `{"supervisor":"lead1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("assigning the offline conversation: %d %s", w.Code, w.Body)
	}
	if _, _, err := e.s.updateSupervision(t.Context(), task.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	e.s.superviseWatch(t.Context(), time.Now())
	e.s.sendDigest(t.Context())
	if got := e.s.queuedDigestSignals(lead.SessionID); len(got) != 1 || got[0].Kind != sigReview || got[0].Todo != task.ID {
		t.Fatalf("offline lead's queued review: %+v", got)
	}
	if got := e.f.noticesTo("conv1"); len(got) != 0 || e.s.queuedFor("conv1") != 0 {
		t.Fatalf("main got the offline lead's review: %q", got)
	}
	e.f.setSessions("server", main, lead)
	e.s.drainRelays("server", []proto.Session{lead})
	waitFor(t, "the returning lead's queue to drain", func() bool { return e.s.queuedFor(lead.SessionID) == 0 })
	if got := e.f.noticesTo(lead.SessionID); len(got) != 1 || !strings.Contains(got[0], "signal=review") {
		t.Fatalf("returning lead's review reminders: %q", got)
	}
}

func TestOutsideDirectionNodeRequests(t *testing.T) {
	e := outsideDirectionEnv(t, &memStore{})
	e.s.devs = newFleetDevs()
	created, handled, err := e.s.NodeRequest(context.Background(), "box", "direction.create", json.RawMessage(`{"name":"Release parity","project":"release"}`))
	if !handled || err != nil {
		t.Fatalf("create dispatch: handled=%v err=%v", handled, err)
	}
	createReply := created.(map[string]any)
	if createReply["status"] != "ok" {
		t.Fatalf("create reply: %+v", createReply)
	}
	id := createReply["leader"].(*leaderView).ID
	updated, handled, err := e.s.NodeRequest(context.Background(), "box", "direction.progress", json.RawMessage(`{"id":"`+id+`","doing":"Reporting from outside"}`))
	if !handled || err != nil || updated.(map[string]any)["status"] != "ok" {
		t.Fatalf("progress dispatch: handled=%v reply=%+v err=%v", handled, updated, err)
	}
	if got := e.s.directionOf(id).Progress; got == nil || got.Doing != "Reporting from outside" {
		t.Fatalf("node-written progress: %+v", got)
	}
	unknown, handled, err := e.s.NodeRequest(context.Background(), "box", "direction.progress", json.RawMessage(`{"id":"dir_missing","doing":"x"}`))
	if !handled || err != nil || unknown.(map[string]any)["status"] != "failed" {
		t.Fatalf("unknown direction reply: handled=%v reply=%+v err=%v", handled, unknown, err)
	}
}

func TestOutsideDirectionProgressValidationAndEnded(t *testing.T) {
	e := outsideDirectionEnv(t, &memStore{})
	created := outsideLeader(t, outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions", `{"name":"Release parity","project":"release"}`))
	if w := outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions/"+created.ID+"/progress", `{"links":["https://example.com"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("links-only progress: %d %s", w.Code, w.Body)
	}
	if w := outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions/dir_missing/progress", `{"doing":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown progress: %d %s", w.Code, w.Body)
	}
	if _, err := e.s.endDirection(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	e.s.directionsSync(context.Background())
	if w := outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions/"+created.ID+"/progress", `{"doing":"too late"}`); w.Code != http.StatusNotFound {
		t.Fatalf("ended progress: %d %s", w.Code, w.Body)
	}
	if got := e.s.directionOf(created.ID); got.Progress != nil {
		t.Fatalf("ended direction took progress: %+v", got.Progress)
	}
}

func TestOutsideDirectionLimit(t *testing.T) {
	e := outsideDirectionEnv(t, &memStore{})
	ids := make([]string, leadersMost)
	for i := range ids {
		ids[i] = "conv_" + strings.Repeat("x", i+1)
	}
	setSettings(t, e.s, func(v *Settings) { v.Leaders = ids })
	if w := outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions", `{"name":"One too many","project":"release"}`); w.Code != http.StatusConflict {
		t.Fatalf("direction limit: %d %s", w.Code, w.Body)
	}
}

func TestOutsideDirectionCreationDoesNotEmitImmediateModelWork(t *testing.T) {
	e := outsideDirectionEnv(t, &memStore{})
	before := time.Now()
	created := outsideLeader(t, outsideRequest(t, e, roleExtension, http.MethodPost, "/api/v1/assistant/directions", `{"name":"Release parity","project":"release"}`))
	if created.At.Before(before.Add(-time.Second)) {
		t.Fatalf("creation time: %s", created.At)
	}
	if len(e.f.modelCalls()) != 0 {
		t.Fatalf("creating a direction called the model: %+v", e.f.modelCalls())
	}
}
