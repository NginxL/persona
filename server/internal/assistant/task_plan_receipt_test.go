package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

func TestTaskPlanReportsEachTaskStartDisposition(t *testing.T) {
	ctx := context.Background()
	readonly := PlanTask{Title: "Inspect tests", Prompt: "List unit tests", Machine: "box", Cwd: "/code/app", Readonly: true, UserRequested: true}
	requested := PlanTask{Title: "Update docs", Prompt: "Update the usage example", Machine: "box", Cwd: "/code/other", UserRequested: true}
	trusted := PlanTask{Title: "Format source", Prompt: "Format the example", Machine: "box", Cwd: "/wt/app/format"}
	inferred := PlanTask{Title: "Upgrade dependencies", Prompt: "Update dependencies", Machine: "box", Cwd: "/code/other"}
	for _, tc := range []struct {
		name       string
		tasks      []PlanTask
		disable    bool
		want       []string
		wantStarts int
	}{
		{
			name:  "automatic reasons",
			tasks: []PlanTask{readonly, requested, trusted},
			want: []string{
				"- Inspect tests: automatic start initiated (read-only)",
				"- Update docs: automatic start initiated (requested by the user this turn)",
				"- Format source: automatic start initiated (trusted directory or repository)",
			},
			wantStarts: 3,
		},
		{
			name:  "mixed plan waits as a whole",
			tasks: []PlanTask{readonly, inferred},
			want: []string{
				"- Inspect tests: waiting for the user to tap Start",
				"- Upgrade dependencies: waiting for the user to tap Start",
			},
		},
		{
			name:    "direct start disabled",
			tasks:   []PlanTask{requested},
			disable: true,
			want:    []string{"- Update docs: waiting for the user to tap Start"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}
			s, f := newTaskServiceOn(t, st)
			repoMachine(f, nil)
			s.trustKinds(ctx, []PlanTask{resolvedTask(s, "/code/app", false)})
			if tc.disable {
				setSettings(t, s, func(v *Settings) { off := false; v.StartRequestedTasks = &off })
			}
			raw, err := json.Marshal(map[string]any{"tasks": tc.tasks})
			if err != nil {
				t.Fatal(err)
			}
			out, err := s.toolTaskPlan(ctx, "conv1", raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("receipt missing %q:\n%s", want, out)
				}
			}
			starts := func() int {
				f.mu.Lock()
				defer f.mu.Unlock()
				return len(f.starts)
			}
			if tc.wantStarts > 0 {
				waitFor(t, "all automatic starts", func() bool { return starts() == tc.wantStarts })
				if strings.Contains(out, "nothing has started") {
					t.Errorf("automatic receipt reports waiting:\n%s", out)
				}
			} else {
				p, err := s.loadPlan(ctx, planIDPattern.FindString(out))
				if err != nil || p.State != planOpen || starts() != 0 {
					t.Fatalf("waiting plan = %+v, err = %v, launches = %d", p, err, starts())
				}
			}
		})
	}
}
