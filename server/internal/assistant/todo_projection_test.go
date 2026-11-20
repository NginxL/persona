package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// The To-dos view's adapter (todo_projection.go, #10226): the facts it hands the projection are the list's row, so the
// shared cases (which decode rows of GET /api/v1/todos) say what the manager answers.

// projectionNotListedYet are the Facts fields GET /api/v1/todos does not carry yet; a field the row starts carrying is
// reported here, to be copied in projectionFacts and dropped from this set.
var projectionNotListedYet = map[string]bool{}

// Every field of a listed row, filled, reads the same through the adapter as through the row's JSON, and every JSON
// field of projection.Facts is one the row writes: a field added to either side without the other fails here, field
// by field.
func TestProjectionFactsMatchTheList(t *testing.T) {
	var row listedTodo
	n := 0
	fillForTest(reflect.ValueOf(&row).Elem(), 0, &n)
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var decoded projection.Facts
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got := projectionFacts(row, nil, nil)
	ft, gv, dv := reflect.TypeOf(decoded), reflect.ValueOf(got), reflect.ValueOf(decoded)
	for i := range ft.NumField() {
		name, tag := ft.Field(i).Name, ft.Field(i).Tag.Get("json")
		if tag == "-" {
			continue
		}
		listed := !dv.Field(i).IsZero()
		switch {
		case projectionNotListedYet[name] && listed:
			t.Errorf("GET /api/v1/todos carries %s now: copy it in projectionFacts and drop it from "+
				"projectionNotListedYet", name)
		case !projectionNotListedYet[name] && !listed:
			t.Errorf("projection.Facts.%s (%q) is not a field of the listed row", name, strings.Split(tag, ",")[0])
		case !reflect.DeepEqual(gv.Field(i).Interface(), dv.Field(i).Interface()):
			a, _ := json.Marshal(gv.Field(i).Interface())
			j, _ := json.Marshal(dv.Field(i).Interface())
			t.Errorf("projection.Facts.%s: the adapter gives %s, the row's JSON %s", name, a, j)
		}
	}
}

// fillForTest gives every exported field v reaches a value that is not zero (one element for a slice or map), at most
// eight levels deep; n makes each time and number its own.
func fillForTest(v reflect.Value, depth int, n *int) {
	if depth > 8 {
		return
	}
	*n++
	switch {
	case v.Type() == reflect.TypeFor[time.Time]():
		v.Set(reflect.ValueOf(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC).Add(time.Duration(*n) * time.Minute)))
		return
	case v.Type() == reflect.TypeFor[json.RawMessage]():
		v.SetBytes([]byte(`{}`))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("v" + strings.Repeat("x", *n%3+1))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(*n%100 + 1))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(*n%100 + 1))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(*n) + 0.5)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillForTest(p.Elem(), depth+1, n)
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillForTest(s.Index(0), depth+1, n)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fillForTest(k, depth+1, n)
		fillForTest(e, depth+1, n)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fillForTest(v.Field(i), depth+1, n)
			}
		}
	}
}

// The projection compares the manager's own words (review states, outcomes, kinds, delivery, who closed it): each row
// below is written with the manager's constants and reads as the reason they mean, so a renamed constant fails here
// rather than filing the to-do under rest.
func TestProjectionReadsTheManagersWords(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ago := func(h int) *time.Time { at := now.Add(-time.Duration(h) * time.Hour); return &at }
	base := func(id string) Todo {
		return Todo{ID: id, Title: id, CreatedAt: *ago(30), UpdatedAt: *ago(30), Origin: "conv_main", Agents: []TodoAgent{}}
	}
	ledger := func(state string, r ...TaskReview) *Supervision {
		return &Supervision{State: state, Reviews: r, UpdatedAt: *ago(2)}
	}
	parked, accepted, needsUser, pending := base("parked"), base("accepted"), base("needs_user"), base("pending")
	parked.Supervision = ledger(reviewParked, TaskReview{At: *ago(2), Verdict: reviewParked})
	parked.Supervision.Parked = &Parked{Reason: "The venue answers by mail", Until: now.Add(time.Hour), WaitingOn: parkSomeoneElse}
	accepted.Supervision = ledger(reviewAccepted, TaskReview{At: *ago(2), Verdict: reviewAccepted})
	needsUser.Supervision = ledger(reviewNeedsUser, TaskReview{At: *ago(2), Verdict: reviewNeedsUser, Reason: "Which one?"})
	pending.Supervision = ledger(reviewPending)
	cancelled, reported, concern, deployed := base("cancelled"), base("reported"), base("concern"), base("deployed")
	cancelled.DoneAt, cancelled.DoneBy = ago(1), db.DoneByCancelled
	cancelled.Supervision = ledger(reviewCancelled, TaskReview{At: *ago(1), Verdict: reviewCancelled, Note: "Not needed"})
	reported.DoneAt, reported.DoneBy, reported.Kind = ago(1), db.DoneByReported, db.KindConcern
	concern.Kind, concern.Delivery = db.KindConcern, &TodoDelivery{State: deliveryLive, At: *ago(2)}
	deployed.Delivery = &TodoDelivery{State: deliveryLive, At: *ago(2)}
	auto := base("auto")
	auto.DoneAt, auto.DoneBy = ago(1), db.DoneByAuto
	asked := func(id, outcome string, briefing bool) listedTodo {
		a := &todoAsk{Outcome: outcome, Verdict: "Needs your decision", AskedAt: *ago(2), Question: "Which one?"}
		if briefing {
			a.ForBriefing, a.Deadline, a.Default = true, ptrTo(now.Add(time.Hour)), defaultTaken
		}
		return listedTodo{Todo: base(id), Ask: a}
	}
	rows := []struct {
		row          listedTodo
		reason, line string
	}{
		{listedTodo{Todo: formatTodo(parked)}, assistantwire.TodoReasonSomeoneElse, "parked"},
		{listedTodo{Todo: formatTodo(accepted)}, assistantwire.TodoReasonFinishedUnseen, "accepted"},
		{listedTodo{Todo: formatTodo(needsUser)}, assistantwire.TodoReasonOwnerDecides, "decision"},
		{listedTodo{Todo: formatTodo(pending)}, assistantwire.TodoReasonFinishedUnseen, "finished"},
		{listedTodo{Todo: formatTodo(cancelled)}, assistantwire.TodoReasonClosedUnseen, "canceled"},
		{listedTodo{Todo: formatTodo(reported)}, assistantwire.TodoReasonClosedUnseen, "reported"},
		{listedTodo{Todo: formatTodo(auto)}, assistantwire.TodoReasonClosedUnseen, "completed"},
		{listedTodo{Todo: formatTodo(concern)}, assistantwire.TodoReasonRest, ""},
		{listedTodo{Todo: formatTodo(deployed)}, assistantwire.TodoReasonFinishedUnseen, "deployed"},
		{asked("decide", outcomeDecide, false), assistantwire.TodoReasonOwnerDecides, "decision"},
		{asked("cannot", outcomeCannot, false), assistantwire.TodoReasonOwnerDecides, "cannot"},
		{asked("done", outcomeDone, false), assistantwire.TodoReasonFinishedUnseen, "finished"},
		{asked("briefing", outcomeDecide, true), assistantwire.TodoReasonCanWait, "decision_default"},
	}
	for _, r := range rows {
		v := projection.View(projectionFacts(r.row, nil, nil), now)
		key := ""
		if v.Line != nil {
			key = v.Line.Key
		}
		if v.Reason != r.reason || key != r.line {
			t.Errorf("%s: %s with line %q, want %s with %q", r.row.ID, v.Reason, key, r.reason, r.line)
		}
	}
	if v := projection.View(projectionFacts(listedTodo{Todo: formatTodo(cancelled)}, nil, nil), now); v.Line == nil ||
		v.Line.Args["reason"] != "Not needed" {
		t.Errorf("a cancellation's line: %+v", v.Line)
	}

	// Priorities: high, normal, low, in that order within To do.
	high, normal, low := base("high"), base("normal"), base("low")
	high.Priority, low.Priority = db.PriorityHigh, db.PriorityLow
	var facts []projection.Facts
	for _, td := range []Todo{low, normal, high} {
		facts = append(facts, projectionFacts(listedTodo{Todo: formatTodo(td)}, nil, nil))
	}
	if views := projection.Views(facts, now); views[2].Rank != 0 || views[1].Rank != 1 || views[0].Rank != 2 {
		t.Errorf("ranks by priority: low %d, normal %d, high %d", views[0].Rank, views[1].Rank, views[2].Rank)
	}
}

func ptrTo[T any](v T) *T { return &v }

// The adapter on the manager's own list: a plain to-do, one a run works on, one just closed and a failed start whose
// run is on a computer no open to-do uses, with the knowledge base's title on their project's place; and the same rows
// decoded from GET /api/v1/todos read the same views.
func TestProjectionOfTheManagersList(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newKBService(t, f)
	if _, err := kbEng(t, s).Apply(kb.KindProject, "Garden", kb.Update{Name: "Garden", Kind: "long"}); err != nil {
		t.Fatal(err)
	}
	e := &todoEnv{t: t, s: s, f: f}
	ctx := context.Background()
	began := time.Now().UTC().Add(-time.Minute)
	e.f.setSessions("server", proto.Session{MachineID: "server", SessionID: "s1", Title: "Hedge trimming",
		Status: proto.StatusWorking, State: proto.StateWorking, StateSince: began, UpdatedAt: began,
		Activity: "measuring the hedge"})
	// The failed start's earlier run moved ten hours after the failure (its session's times are what dates it).
	moved := began.Add(10 * time.Hour)
	e.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s2", Title: "Ferry booking",
		Status: proto.StatusIdle, State: proto.StateIdle, StateSince: moved, UpdatedAt: moved})
	var created struct{ Todo Todo }
	_ = json.Unmarshal(e.must("POST", "/api/v1/todos", `{"title":"Water the roses","project":"Garden"}`).Body.Bytes(), &created)
	plain := created.Todo
	if plain.Project == "" {
		t.Fatal("the to-do got no project")
	}
	worked, closed, failed := e.create("Prune the hedge"), e.create("Order compost"), e.create("Book the ferry")
	e.must("PUT", "/api/v1/todos/"+worked.ID+"/agents/server/s1", "")
	e.must("PATCH", "/api/v1/todos/"+closed.ID, `{"done":true}`)
	e.must("PUT", "/api/v1/todos/"+failed.ID+"/agents/box/s2", "")
	if err := e.s.failTodo(ctx, failed.ID, "The computer is offline"); err != nil {
		t.Fatal(err)
	}

	body := e.must("GET", "/api/v1/todos", "").Body.Bytes()
	var listed struct{ Todos []listedTodo }
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	got := e.s.todoProjectionOf(ctx, listed.Todos, now)
	views := got.byID()

	want := map[string][3]string{
		plain.ID:  {assistantwire.TodoPhaseTodo, assistantwire.TodoListSectionTodo, assistantwire.TodoReasonRest},
		worked.ID: {assistantwire.TodoPhaseWorking, assistantwire.TodoListSectionWorking, assistantwire.TodoReasonAgentRunning},
		closed.ID: {assistantwire.TodoPhaseDone, assistantwire.TodoListSectionJustFinished, assistantwire.TodoReasonClosedUnseen},
		failed.ID: {assistantwire.TodoPhaseNeedsYou, assistantwire.TodoListSectionNeedsYou, assistantwire.TodoReasonFailedStart},
	}
	for id, w := range want {
		v := views[id]
		if [3]string{v.Phase, v.Section, v.Reason} != w {
			t.Errorf("%s: %s · %s · %s, want %v", id, v.Phase, v.Section, v.Reason, w)
		}
	}
	if l := views[worked.ID].Line; l == nil || l.Key != "working" || l.Args["run"] != "Hedge trimming" ||
		l.Args["text"] != "measuring the hedge" {
		t.Errorf("the working run's line: %+v", l)
	}
	if c := got.counts(); c.Open != 3 || c.InProgress != 1 || c.NeedsYou != 1 || c.Sections["just_finished"] != 1 {
		t.Errorf("counts %+v", c)
	}
	places := e.s.todoPlaces(ctx, got).Places
	if len(places) != 4 || places[3].ID != "project:"+plain.Project || places[3].Title != "Garden" {
		t.Errorf("places %+v", places)
	}

	// Two days on, the failed start is a To do; its run's session, fetched though no open to-do has a run on that
	// computer, holds the Inactive mark back.
	later := now.Add(50 * time.Hour)
	if v := e.s.todoProjectionOf(ctx, listed.Todos, later).byID()[failed.ID]; v.Reason != assistantwire.TodoReasonRest ||
		!v.Inactive.IsZero() || !v.NextChangeAt.Equal(moved.Add(48*time.Hour)) {
		t.Errorf("the failed start two days on: %+v (its run moved at %v)", v, moved)
	}

	// The rows decode straight into projection.Facts, as the shared cases do, and read the same views.
	var rows struct{ Todos []projection.Facts }
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	machines := map[string][]proto.Session{}
	for _, m := range []string{"server", "box"} {
		machines[m], _ = e.s.sessions(ctx, m)
	}
	for i := range rows.Todos {
		for _, a := range rows.Todos[i].Agents {
			for _, c := range machines[a.MachineID] {
				if c.SessionID == a.SessionID {
					rows.Todos[i].Sessions = append(rows.Todos[i].Sessions, c)
				}
			}
		}
		rows.Todos[i].Loc = e.s.location(ctx)
	}
	for _, at := range []time.Time{now, later} {
		fromJSON, fromAdapter := projection.Views(rows.Todos, at), e.s.todoProjectionOf(ctx, listed.Todos, at).byID()
		for i, r := range rows.Todos {
			if !reflect.DeepEqual(fromJSON[i], fromAdapter[r.ID]) {
				t.Errorf("%s at %v from the JSON: %+v, from the adapter %+v", r.ID, at, fromJSON[i], fromAdapter[r.ID])
			}
		}
	}

	// A computer that does not answer lists nothing: the failed start's run reads as offline, and the to-do gets no
	// Inactive mark (nor a moment it would get one), since that run may be working on it.
	e.f.mu.Lock()
	e.f.sessionsFail["box"] = 1
	e.f.mu.Unlock()
	if v := e.s.todoProjectionOf(ctx, listed.Todos, later).byID()[failed.ID]; v.Reason != assistantwire.TodoReasonRest ||
		!v.Inactive.IsZero() || !v.NextChangeAt.IsZero() {
		t.Errorf("the failed start with its computer unreached: %+v", v)
	}
}

// On a manager that carries the fleet, as in production, the adapter reads only the sessions of the open to-dos'
// active runs, never a computer's whole list or its archive's records, and the archive's row of a run the computer no
// longer lists, which reads as offline as in the computer's whole list: the views are those the whole list gives.
func TestProjectionReadsOnlyTheLinkedRunsOnTheFleet(t *testing.T) {
	d := newFleetDevs()
	d.add("box", "Computer")
	st := newFleetMem()
	s := New(Options{Hub: newFakeHub(), Devices: d, Store: func() Store { return st }, Logf: func(string, ...any) {}})
	ctx := context.Background()
	gone := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	archive := map[string]agentArchive{}
	for _, sid := range []string{"gone", "old1", "old2"} {
		archive[runtimeListID("box", sid)] = agentArchive{Session: archiveSnapshot(proto.Session{MachineID: "box", SessionID: sid,
			Title: "Ferry booking", UpdatedAt: gone}), Archived: true, ChangedAt: gone}
	}
	if err := st.PutSetting(ctx, agentArchivesKey, archive); err != nil {
		t.Fatal(err)
	}
	began := time.Now().UTC().Add(-time.Minute)
	d.setSessions("box", []proto.Session{
		{SessionID: "s1", Title: "Hedge trimming", Status: proto.StatusWorking, StateSince: began, UpdatedAt: began,
			Activity: "measuring the hedge"},
		{SessionID: "s2", Title: "Something else", Status: proto.StatusIdle, UpdatedAt: began},
	})
	created := began.Add(-time.Hour)
	todo := func(id, machine, sid string) listedTodo {
		return listedTodo{Todo: formatTodo(Todo{ID: id, Title: id, CreatedAt: created, UpdatedAt: created,
			Agents: []TodoAgent{{MachineID: machine, SessionID: sid, LinkedAt: created}}})}
	}
	list := []listedTodo{todo("todo_worked", "box", "s1"), todo("todo_archived", "box", "gone"),
		todo("todo_elsewhere", "nowhere", "s9")}
	now := time.Now()

	d.mu.Lock()
	lists := d.machines["box"].lists
	d.mu.Unlock()
	got := s.todoProjectionOf(ctx, list, now)
	d.mu.Lock()
	if d.machines["box"].lists != lists {
		t.Error("the computer's whole list was read")
	}
	d.mu.Unlock()
	if n := len(got.facts[0].Sessions); n != 1 || got.facts[0].Sessions[0].SessionID != "s1" {
		t.Errorf("the working run's sessions: %+v", got.facts[0].Sessions)
	}
	if ss := got.facts[1].Sessions; len(ss) != 1 || ss[0].SessionID != "gone" || ss[0].State != proto.StateOffline {
		t.Errorf("the archived run's sessions: %+v", ss)
	}
	if !got.facts[2].Unreached || got.facts[0].Unreached || got.facts[1].Unreached {
		t.Errorf("unreached: %v %v %v", got.facts[0].Unreached, got.facts[1].Unreached, got.facts[2].Unreached)
	}

	whole, err := s.sessions(ctx, "box")
	if err != nil {
		t.Fatal(err)
	}
	all := map[[2]string]proto.Session{}
	for _, c := range whole {
		c.MachineID = "box"
		all[[2]string{"box", c.SessionID}] = c
	}
	facts := make([]projection.Facts, len(list))
	for i := range list {
		facts[i] = projectionFacts(list[i], all, s.location(ctx))
	}
	facts[2].Unreached = true
	if want := projection.Views(facts, now); !reflect.DeepEqual(got.views, want) {
		t.Fatalf("from the linked runs:\n%+v\nfrom the whole list:\n%+v", got.views, want)
	}
	if v := got.views[0]; v.Reason != assistantwire.TodoReasonAgentRunning {
		t.Errorf("the working run's to-do: %+v", v)
	}
}

// The adapter hands the projection the pending cards of the open to-dos' active runs: a run that asks is asked by its
// card, whose id rides on the ask and whose kind says what it asks, and a card of a session no run of the to-do is on
// is no ask of its. Of two cards of one instant the same one is named on every read, so the same state read again is
// the same bytes and the list's ETag holds. A health conversation's question is open, as its push offers nothing to
// answer it with; once the cards are answered the run's ask is a question again until the session moves on.
func TestProjectionAsksByTheRunsCard(t *testing.T) {
	e := newTodoEnv(t, nil)
	began := time.Now().UTC().Add(-time.Minute)
	e.sessions("server", proto.Session{SessionID: "s1", Title: "Visa form", Status: proto.StatusBlocked,
		State: proto.StateNeedsYou, StateSince: began, UpdatedAt: began, PendingInteractions: 1,
		AttentionReason: "Wants to run a command"})
	todo := e.create("Fill in the visa form")
	e.must("PUT", "/api/v1/todos/"+todo.ID+"/agents/server/s1", "")
	cards := []proto.Interaction{
		{ID: "ix_other", MachineID: "server", SessionID: "s2", Kind: proto.IxQuestion, CreatedAt: began.Add(-time.Hour),
			ExpiresAt: began.Add(time.Hour), Questions: []proto.Question{{Question: "Which folder?",
				Options: []proto.QuestionOption{{Label: "Documents"}}}}},
		{ID: "ix_visa", MachineID: "server", SessionID: "s1", Kind: proto.IxPermission, ToolName: "Bash", CreatedAt: began,
			ExpiresAt: began.Add(time.Hour)},
		{ID: "ix_visa_q", MachineID: "server", SessionID: "s1", Kind: proto.IxQuestion, CreatedAt: began,
			ExpiresAt: began.Add(time.Hour), Cwd: "/home/user/.persona/health", Questions: []proto.Question{{
				Question: "Which clinic?", Options: []proto.QuestionOption{{Label: "The one by the station"}}}}},
	}
	e.s.fleet.mu.Lock()
	if e.s.fleet.interactions == nil {
		e.s.fleet.interactions = map[string]*pending{}
	}
	for _, c := range cards {
		e.s.fleet.interactions[c.ID] = &pending{ix: c}
	}
	e.s.fleet.mu.Unlock()

	runAsk := func(body []byte) assistantwire.OwnerAsk {
		t.Helper()
		var got struct{ Todos []listedTodo }
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		for _, r := range got.Todos {
			if r.ID == todo.ID && r.View != nil && len(r.View.Asks) == 1 {
				return r.View.Asks[0]
			}
		}
		t.Fatalf("no one ask on %s: %s", todo.ID, body)
		return assistantwire.OwnerAsk{}
	}
	body := e.must("GET", "/api/v1/todos", "").Body.Bytes()
	if a := runAsk(body); a.Kind != assistantwire.OwnerAskKindTool || a.Interaction != "ix_visa" ||
		a.Home != assistantwire.OwnerAskHomeRun || a.Risk != assistantwire.OwnerAskRiskOpen ||
		a.Lockscreen != assistantwire.OwnerAskLockscreenOpenOnly || a.Text != "Wants to run a command" {
		t.Fatalf("the run's ask %+v", a)
	}
	for range 8 { // the fleet's cards are a map: each read ranges over them in another order
		if again := e.must("GET", "/api/v1/todos", "").Body.Bytes(); !bytes.Equal(again, body) {
			t.Fatalf("unchanged state read again:\n%s\n%s", body, again)
		}
	}

	e.s.fleet.mu.Lock()
	delete(e.s.fleet.interactions, "ix_visa")
	e.s.fleet.mu.Unlock()
	if a := runAsk(e.must("GET", "/api/v1/todos", "").Body.Bytes()); a.Kind != assistantwire.OwnerAskKindQuestion ||
		a.Interaction != "ix_visa_q" || a.Risk != assistantwire.OwnerAskRiskOpen ||
		a.Lockscreen != assistantwire.OwnerAskLockscreenOpenOnly {
		t.Fatalf("the run's ask by a health conversation's question %+v", a)
	}
	e.s.fleet.mu.Lock()
	delete(e.s.fleet.interactions, "ix_visa_q")
	e.s.fleet.mu.Unlock()
	if a := runAsk(e.must("GET", "/api/v1/todos", "").Body.Bytes()); a.Kind != assistantwire.OwnerAskKindQuestion ||
		a.Interaction != "" || a.Risk != assistantwire.OwnerAskRiskOpen {
		t.Fatalf("the run's ask with its cards answered %+v", a)
	}
}

func TestDeliveryApprovalProjectsToNeedsYou(t *testing.T) {
	now := time.Now().UTC()
	task := listedTodo{Todo: Todo{
		ID: "todo_release", Title: "Release sample page", Origin: "main", PRURL: "https://github.com/example/app/pull/12",
		Delivery: &TodoDelivery{State: deliveryPR, Unblock: unblockUser, Gate: "Approve the release", At: now.Add(-time.Hour)},
		PRStatus: &TodoPRStatus{URL: "https://github.com/example/app/pull/12", State: "open", CheckedAt: now},
	}}
	project := func() assistantwire.TodoSummary { return projection.View(projectionFacts(task, nil, time.UTC), now) }
	if view := project(); view.Section != assistantwire.TodoListSectionNeedsYou || view.Line == nil ||
		view.Line.Args["text"] != "Approve the release" || len(view.Asks) != 0 {
		t.Fatalf("delivery approval view: %+v", view)
	}
	if old := triageTodo(task, nil, now, time.Time{}, time.UTC, false); old.Section != triageWaiting {
		t.Fatalf("legacy placement: %+v", old)
	}
	task.Delivery.Unblock = unblockOther
	if view := project(); view.Section == assistantwire.TodoListSectionNeedsYou {
		t.Fatalf("reviewer or CI wait needs the owner: %+v", view)
	}
	task.Delivery.Unblock, task.Todo.DoneAt = unblockUser, &now
	if view := project(); view.Phase != assistantwire.TodoPhaseDone {
		t.Fatalf("closed task returned to the owner: %+v", view)
	}
}
