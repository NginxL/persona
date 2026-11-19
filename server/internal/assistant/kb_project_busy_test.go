package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
)

// A project with an unfinished to-do filed under it is busy, so FinishProjects leaves it (#9427); a done to-do does not
// hold it.
func TestKBProjectBusyFollowsOpenTodos(t *testing.T) {
	s := &Service{}
	done := time.Now()
	s.todos.loaded = true
	s.todos.todos = map[string]*Todo{
		"t1": {ID: "t1", Project: "open-work"},
		"t2": {ID: "t2", Project: "finished-work", DoneAt: &done},
		"t3": {ID: "t3"},
	}
	busy := s.kbProjectBusy(context.Background(), nil)
	if !busy("open-work") || busy("finished-work") || busy("other") || busy("") {
		t.Fatalf("busy: open-work %v, finished-work %v, other %v", busy("open-work"), busy("finished-work"), busy("other"))
	}
}

// A to-do keeps the slug it was filed under when that project is merged into another: the project it went into is busy
// and is not finished under it (#9427 review). One merged into a project that was itself merged away holds every project
// on the way, so a lookup that lands on nothing live finishes nothing on a guess.
func TestKBProjectBusyFollowsMergedProjects(t *testing.T) {
	s := newKBService(t, newFakeHub())
	e := kbEng(t, s)
	for _, p := range []*kb.Project{
		{Slug: "keep", Name: "Keep", Status: "active"},
		{Slug: "old", Name: "Old", Status: "merged", MergedInto: "keep"},
		{Slug: "gone", Name: "Gone", Status: "merged", MergedInto: "middle"},
		{Slug: "middle", Name: "Middle", Status: "merged", MergedInto: "last"},
		{Slug: "last", Name: "Last", Status: "merged", MergedInto: "gone"},
		{Slug: "idle", Name: "Idle", Status: "active"},
	} {
		if err := e.Store.PutProject(p); err != nil {
			t.Fatal(err)
		}
	}
	s.todos.mu.Lock()
	s.todos.loaded = true
	s.todos.todos = map[string]*Todo{
		"t1": {ID: "t1", Project: "old"},
		"t2": {ID: "t2", Project: "gone"},
	}
	s.todos.mu.Unlock()
	busy := s.kbProjectBusy(context.Background(), e)
	for slug, want := range map[string]bool{"keep": true, "old": true, "gone": true, "middle": true, "last": true, "idle": false} {
		if got := busy(slug); got != want {
			t.Errorf("busy(%q) = %v, want %v", slug, got, want)
		}
	}
}
