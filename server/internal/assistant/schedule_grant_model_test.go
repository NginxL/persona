package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestScheduleRunPersistenceDoesNotReadAnotherTasksGrantModel(t *testing.T) {
	st := newSettingsFaultStore()
	s := newServiceOn(t, newFakeHub().withMachine(), st)
	guarded := granted(depsTask())
	setTasks(s, guarded, Task{ID: "other", Title: "Other", Prompt: "Check progress", Repeat: repeatDaily, Time: "09:00"})
	run := ScheduleRun{RefID: "other", Status: runFailed}
	run.ID = s.recordRun(run)
	if run.ID == "" {
		t.Fatal("run was not recorded")
	}
	key := modelRecordKey("task." + guarded.ID)
	reads := st.readCount(key)
	st.failRead = key
	if _, err := s.updateRunResult(run, runOK, "", true); err != nil {
		t.Fatalf("an unrelated grant model prevented saving the run result: %v", err)
	}
	if got := st.readCount(key); got != reads {
		t.Fatalf("runtime save read another task's model %d more times", got-reads)
	}
	var stored []Task
	if found, err := st.Setting(t.Context(), tasksKey, &stored); err != nil || !found {
		t.Fatalf("read saved tasks: found=%t error=%v", found, err)
	}
	if len(stored) != 2 || stored[0].liveGrant() == nil || stored[1].LastStatus != runOK {
		t.Fatalf("runtime persistence lost the unrelated grant or run outcome: %+v", stored)
	}
}

func TestScheduleGrantModelEditsRevokeAuthorization(t *testing.T) {
	for _, field := range []string{"model", "effort", "launcher"} {
		t.Run(field, func(t *testing.T) {
			g := newGrantFixture(t)
			g.f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}, {Name: "custom", Agent: "claude"}}
			patch := func(extra map[string]any) {
				t.Helper()
				body := map[string]any{"title": "Dependency update", "prompt": depsTask().Prompt, "repeat": "daily", "time": "09:00", "enabled": true}
				for k, v := range extra {
					body[k] = v
				}
				raw, _ := json.Marshal(body)
				if r := scheduleRequest(g.s, "PUT", "tasks/deps", string(raw)); r.Code != http.StatusOK {
					t.Fatalf("save: %d %s", r.Code, r.Body)
				}
			}
			// Pin the already-used computer before granting, so a later model edit changes no target field.
			patch(map[string]any{"execution_machine_id": "server"})
			machine, session := g.round("deps", nil)
			id, out := g.plan(machine, session, "/wt/app/first")
			if !waitsForStart(out) {
				t.Fatal("first round bypassed authorization")
			}
			if _, err := g.s.startPlan(scheduleGranting(ownerStart(context.Background())), id, nil, false); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "first authorized start", func() bool { return fakeStarts(g.f) == 1 })
			old, _ := taskByID(g.s, "deps")
			if old.StartGrant == nil {
				t.Fatal("owner's grant was not recorded")
			}
			// Keep a pending card from the old round; it must not grant authorization for the edited model.
			pendingID, _ := g.plan(machine, session, "/wt/other/pending")
			pending, err := g.s.loadPlan(context.Background(), pendingID)
			if err != nil {
				t.Fatal(err)
			}
			value := "changed-model"
			if field == "effort" {
				value = "low"
			} else if field == "launcher" {
				value = "custom"
			}
			patch(map[string]any{field: value})
			edited, _ := taskByID(g.s, "deps")
			if edited.StartGrant != nil {
				t.Errorf("%s-only edit retained the old grant: %+v", field, edited.StartGrant)
			}
			g.s.grantScheduleKind(context.Background(), "deps", pending.Tasks[0])
			edited, _ = taskByID(g.s, "deps")
			if edited.StartGrant != nil || fakeStarts(g.f) != 1 {
				t.Errorf("old pending plan changed the grant or started work: %+v starts=%d", edited.StartGrant, fakeStarts(g.f))
			}
			g.s.endRound(machine, session, "deps")
			machine, session = g.round("deps", nil)
			if send := g.f.lastSend(t); send[field] != value || send["task_execution"] != true {
				t.Fatalf("next round did not consume the saved override: %v", send)
			}
			id, out = g.plan(machine, session, "/wt/app/edited")
			if !waitsForStart(out) || fakeStarts(g.f) != 1 {
				t.Fatalf("edited round consumed old authorization: %s starts=%d", out, fakeStarts(g.f))
			}
			if _, err := g.s.startPlan(scheduleGranting(ownerStart(context.Background())), id, nil, false); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "newly authorized start", func() bool { return fakeStarts(g.f) == 2 })
			patch(map[string]any{"time": "10:00"})
			if unchanged, _ := taskByID(g.s, "deps"); unchanged.StartGrant == nil {
				t.Fatal("a sparse timing edit revoked the unchanged model grant")
			}
			g.s.endRound(machine, session, "deps")
			// Reconstruct from persisted settings: a newly authorized binding remains usable.
			g.s = newServiceOn(t, g.f, g.s.store())
			machine, session = g.round("deps", nil)
			if _, out := g.plan(machine, session, "/wt/app/new-grant"); waitsForStart(out) {
				t.Fatalf("new grant did not cover the configured round: %s", out)
			}
			waitFor(t, "automatic start with new grant", func() bool { return fakeStarts(g.f) == 3 })
			// Explicit clearing restores inheritance and revokes the configured grant atomically.
			patch(map[string]any{"launcher": "", "model": "", "effort": "", "execution_machine_id": ""})
			cleared, _ := taskByID(g.s, "deps")
			record, err := g.s.modelRecord(context.Background(), "task.deps")
			if err != nil || record.Binding != (ModelBinding{}) || cleared.StartGrant != nil {
				t.Fatalf("clear did not restore inheritance/revoke: %+v %+v %v", cleared, record, err)
			}
			g.s.endRound(machine, session, "deps")
			machine, session = g.round("deps", nil)
			base, err := g.s.resolveModel(context.Background(), "assistant")
			if err != nil {
				t.Fatal(err)
			}
			send := g.f.lastSend(t)
			model, _ := send["model"].(string)
			effort, _ := send["effort"].(string)
			if send["launcher"] != strings.TrimPrefix(base.Provider, "launcher.") || model != base.Model || effort != base.Effort {
				t.Fatalf("cleared round did not consume inherited configuration: %v base=%+v", send, base)
			}
			if _, out := g.plan(machine, session, "/wt/app/cleared"); !waitsForStart(out) || fakeStarts(g.f) != 3 {
				t.Fatalf("cleared round reused the configured grant: %s", out)
			}
		})
	}
}

// Model configuration can write its own namespace without a task PUT. A retained persisted grant is still checked
// against that record at consumption, including after reconstructing the service from the same store.
func TestScheduleGrantChecksAuthoritativeModelAfterRestart(t *testing.T) {
	g := newGrantFixture(t, granted(depsTask()))
	if r := scheduleRequest(g.s, "PUT", "tasks/deps", `{"title":"Dependency update","prompt":"Update the app's dependencies and open a PR","repeat":"daily","time":"09:00","enabled":true}`); r.Code != http.StatusOK {
		t.Fatalf("persist legacy task: %d %s", r.Code, r.Body)
	}
	setModel(t, g.s, "task.deps", ModelBinding{Provider: "launcher.claude", Model: "changed-model", Effort: "low"})
	g.s = newServiceOn(t, g.f, g.s.store())
	machine, session := g.round("deps", nil)
	if send := g.f.lastSend(t); send["model"] != "changed-model" || send["effort"] != "low" {
		t.Fatalf("restart did not consume the model record: %v", send)
	}
	if _, out := g.plan(machine, session, "/wt/app/after-restart"); !waitsForStart(out) || fakeStarts(g.f) != 0 {
		t.Fatalf("persisted legacy grant authorized the edited model: %s", out)
	}
}

func TestScheduleGrantDoesNotRebindAnInFlightRound(t *testing.T) {
	g := newGrantFixture(t, granted(depsTask()))
	machine, session := g.round("deps", nil)
	// A different round consumes the new model and receives a new grant through the owner's card.
	g.mu.Lock()
	oldItems := append([]proto.Item(nil), g.items...)
	g.mu.Unlock()
	setModel(t, g.s, "task.deps", ModelBinding{Provider: "launcher.claude", Model: "changed-model"})
	newMachine, newSession := g.round("deps", nil)
	id, out := g.plan(newMachine, newSession, "/wt/app/new-authorization")
	if !waitsForStart(out) {
		t.Fatalf("new model reused old grant: %s", out)
	}
	if _, err := g.s.startPlan(scheduleGranting(ownerStart(context.Background())), id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "new model's authorized start", func() bool { return fakeStarts(g.f) == 1 })
	// The old conversation still consumed the original model: it cannot adopt that new grant.
	g.setItems(oldItems)
	if _, out := g.plan(machine, session, "/wt/app/old-round"); !waitsForStart(out) || fakeStarts(g.f) != 1 {
		t.Fatalf("old round adopted the new binding's grant: %s", out)
	}
	g.s.endRound(newMachine, newSession, "deps")
	machine, session = g.round("deps", nil)
	if _, out := g.plan(machine, session, "/wt/app/new-round"); waitsForStart(out) {
		t.Fatalf("new round could not consume its own grant: %s", out)
	}
	waitFor(t, "new configured round's start", func() bool { return fakeStarts(g.f) == 2 })
}
