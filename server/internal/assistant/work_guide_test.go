package assistant

import (
	"encoding/json"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkGuideFreshChatSettings(t *testing.T) {
	s, f, _ := briefService(t, "Add preparing the release checklist to my to-dos")
	// Keep the fixture's machine list in the same order across the three prompt renders.
	f.devices = []Device{{ID: "box", Name: "box", Online: true, Reachable: true}, {ID: "server", Name: "server", Online: true, Reachable: true}}
	if out, bad := tool(t, s, "todo_create", map[string]any{"title": "Release checklist"}); bad {
		t.Fatal(out)
	}
	item, ok := todoByTitle(s, "Release checklist")
	if !ok {
		t.Fatal("missing checklist")
	}
	put := func(manager, body string, status int) {
		t.Helper()
		w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", manager, "full", body)
		if w.Code != status {
			t.Fatalf("PUT: %d, %s", w.Code, w.Body)
		}
	}
	var original string
	for i, enabled := range []bool{false, true, false} {
		switch i {
		case 1:
			put("another", `{"work_guide":true}`, http.StatusForbidden)
			put(s.managerIdentity(), `{"work_guide":true,"expected_work_guide":false}`, http.StatusOK)
			put(s.managerIdentity(), `{"work_guide":false,"expected_work_guide":false}`, http.StatusConflict)
		case 2:
			put(s.managerIdentity(), `{"work_guide":false,"expected_work_guide":true}`, http.StatusOK)
		}
		w, state := permissionSettingsRequest(t, s, "GET", "/api/v1/assistant", s.managerIdentity(), "full", "")
		if w.Code != http.StatusOK || state.WorkGuide != enabled {
			t.Fatalf("GET: %d, guide %t", w.Code, state.WorkGuide)
		}
		if _, err := s.prepare(t.Context(), "server"); err != nil {
			t.Fatal(err)
		}
		p := f.prepared[len(f.prepared)-1]
		full := p["claude_md"].(string)
		if i == 0 {
			original = full
		}
		if full != original {
			t.Fatal("legacy instructions changed")
		}
		short, present := p["work_guide_md"].(string)
		if present != enabled {
			t.Fatalf("unexpected native guide selection %t", present)
		}
		if enabled {
			for _, migrated := range []string{"todo_create", "continues_why", "task_scope", "concern_report"} {
				if !strings.Contains(full, migrated) || strings.Contains(short, migrated) {
					t.Errorf("procedure %s", migrated)
				}
			}
			t.Logf("Shared instructions in UTF-8 bytes: default %d, enabled %d, migrated %d", len(full), len(short), len(full)-len(short))
		}
		after, ok := todoByTitle(s, "Release checklist")
		if !ok || after.ID != item.ID || after.DoneAt != nil || len(after.Agents) != 0 {
			t.Fatal("setting change touched the saved checklist")
		}
	}
}

func TestWorkGuideLinkedDispatchAndApproval(t *testing.T) {
	s, f, _ := briefService(t, "Have someone prepare my release checklist")
	setSettings(t, s, func(v *Settings) { v.WorkGuide = ptr(true); v.StartRequestedTasks = ptr(false) })
	if out, bad := tool(t, s, "todo_create", map[string]any{"title": "Prepare the release checklist"}); bad {
		t.Fatal(out)
	}
	parent := onlyTodo(s)
	if parent.ID == "" || todoCount(s) != 1 || len(f.started()) != 0 {
		t.Fatal("expected one personal item and no Agent")
	}

	args := briefTask(map[string]any{"title": "Draft checklist.md", "prompt": "Prepare checklist.md for review", "goal": "Release checklist ready for review", "done_when": []string{"checklist.md ready to review"}, "part_of": parent.ID, "readonly": false, "user_requested": true})
	if out, bad := tool(t, s, "task_plan", args); bad || !strings.Contains(out, "nothing has started") {
		t.Fatal(out)
	}
	p := planOf(t, s, "conv1")
	if p.State != planOpen || p.Tasks[0].PartOf != parent.ID {
		t.Fatal("plan did not await approval for the recorded request")
	}
	if _, err := s.dismissPlan(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.started()) != 0 || todoCount(s) != 1 {
		t.Fatal("declining started work or added a task")
	}

	setSettings(t, s, func(v *Settings) { v.StartRequestedTasks = ptr(true) })
	if out, bad := tool(t, s, "task_plan", args); bad || !strings.Contains(out, "dispatch") {
		t.Fatal(out)
	}
	var child Todo
	waitFor(t, "linked dispatch", func() bool {
		child, _ = todoByTitle(s, "Draft checklist.md")
		return child.PartOf == parent.ID && len(child.Agents) == 1
	})
	setSettings(t, s, func(v *Settings) { v.WorkGuide = ptr(false) })
	if len(f.started()) != 1 || todoCount(s) != 2 || !reflect.DeepEqual(todoNow(s, parent.ID), parent) || !reflect.DeepEqual(todoNow(s, child.ID), child) {
		t.Fatal("unexpected starts or changed task after rollback")
	}
}

func TestWorkGuidePersistsForSelectedManager(t *testing.T) {
	st := &memStore{}
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	s.id = "example-manager"
	w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", s.id, "full", `{"work_guide":true,"expected_work_guide":false}`)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	reloaded := newServiceOn(t, f, st)
	reloaded.id = s.id
	w, view := permissionSettingsRequest(t, reloaded, "GET", "/api/v1/assistant", s.id, "full", "")
	if w.Code != http.StatusOK || view.Manager != s.id || !view.WorkGuide {
		t.Fatal("lost manager preference on reload")
	}
}

// Two native chats in one workspace share the same instruction and skill files.
func TestWorkGuideConcurrentPrepareAfterRollback(t *testing.T) {
	s, f, _ := briefService(t, "release checklist")
	setSettings(t, s, func(v *Settings) { v.WorkGuide = ptr(true) })
	first, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	f.answerAppBlocking(AppPlugins, PluginsAssistant, func(json.RawMessage) (any, error) {
		if count.Add(1) == 1 {
			close(first)
			<-release
		}
		return map[string]any{"skills": nil}, nil
	})
	start := func() <-chan error {
		done := make(chan error, 1)
		go func() { _, err := s.prepare(t.Context(), "server"); done <- err }()
		return done
	}
	earlier := start()
	<-first
	setSettings(t, s, func(v *Settings) { v.WorkGuide = ptr(false) })
	later := start()
	// Observe the new prepare waiting for the same directory before letting the old render finish.
	waited := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		waited = s.prepareLocks.waiting("server\x00"+s.assistantRel()) == 2
		if waited {
			break
		}
		runtime.Gosched()
	}
	close(release)
	for _, done := range []<-chan error{earlier, later} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if !waited {
		t.Fatal("concurrent writes to one assistant workspace")
	}
	ps := f.prepared
	if len(ps) < 2 || ps[len(ps)-2]["work_guide_md"] == nil || ps[len(ps)-1]["work_guide_md"] != nil {
		t.Fatal("stale prepared instructions after rollback")
	}
}
