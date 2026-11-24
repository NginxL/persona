package assistant

import (
	"reflect"
	"testing"
)

// What the plan keeps of a start (storedLaunch): the task's own launcher, model, effort, speed and tier, never what the
// try resolved for it; what the try put in place of the resolved launch (a launcher the computer has for a default it
// lacks, what an engine ran instead) unless a policy decided it; the permission the try settled unless a policy's preset
// brought it; and the task's own ultracode where a policy filled it in. CallerPermission separately drives the next start.
func TestStoredLaunchIsTheTasksOwn(t *testing.T) {
	on := true
	emptyPermission, explicitPermission := "", "acceptEdits"
	for _, c := range []struct {
		name   string
		task   PlanTask // what the try left on the task (resolved, fitted, run)
		own    startPick
		caller *string
		filled startFill
		named  startNamed
		want   startPick
	}{
		{"read back before any try", PlanTask{Launcher: "first", Model: "model-one", Tier: "deep"}, startPick{}, nil, startFill{}, startNamed{},
			startPick{launcher: "first", model: "model-one", tier: "deep"}},
		{"a tier or the default resolved it",
			PlanTask{Launcher: "first", Model: "model-one", Effort: "high", Speed: "fast", Tier: "deep", Permission: "bypassPermissions"},
			startPick{tier: "deep", permission: "default"}, nil, startFill{}, startNamed{},
			startPick{tier: "deep", permission: "bypassPermissions"}},
		{"generated permission stays settled with empty caller provenance", PlanTask{Permission: "bypassPermissions"},
			startPick{}, &emptyPermission, startFill{}, startNamed{}, startPick{permission: "bypassPermissions"}},
		{"explicit caller provenance does not replace the settled mode", PlanTask{Permission: "bypassPermissions"},
			startPick{permission: "acceptEdits"}, &explicitPermission, startFill{}, startNamed{},
			startPick{permission: "bypassPermissions"}},
		{"policy preset keeps the current settled permission", PlanTask{Permission: "acceptEdits"},
			startPick{}, &emptyPermission, startFill{preset: true}, startNamed{},
			startPick{permission: "acceptEdits"}},
		{"policy preset keeps an explicit caller's own permission", PlanTask{Permission: "bypassPermissions"},
			startPick{permission: "acceptEdits"}, &explicitPermission, startFill{preset: true}, startNamed{},
			startPick{permission: "acceptEdits"}},
		{"the computer lacked the default", PlanTask{Launcher: "claude", Effort: "low"},
			startPick{model: "model-three", effort: "low"}, nil, startFill{}, startNamed{launcher: true, noModel: true},
			startPick{launcher: "claude", effort: "low"}},
		{"an engine stood in with the model it offers", PlanTask{Launcher: "second", Model: "model-one", Effort: "high"},
			startPick{tier: "deep"}, nil, startFill{}, startNamed{launcher: true},
			startPick{launcher: "second"}},
		{"an engine ran its own default model", PlanTask{Launcher: "first", Effort: "high"},
			startPick{model: "model-three"}, nil, startFill{}, startNamed{noModel: true},
			startPick{}},
		{"an engine ran another effort", PlanTask{Launcher: "first", Model: "model-one", Effort: "low"},
			startPick{effort: "high"}, nil, startFill{}, startNamed{effort: true},
			startPick{effort: "low"}},
		{"an engine lowered the policy's effort", PlanTask{Launcher: "first", Effort: "low"},
			startPick{}, nil, startFill{effort: true}, startNamed{effort: true},
			startPick{}},
		{"an engine replaced a policy preset's launcher", PlanTask{Launcher: "second", Model: "model-four", Tier: "", Permission: "bypassPermissions"},
			startPick{permission: "acceptEdits"}, nil, startFill{preset: true}, startNamed{launcher: true},
			startPick{permission: "acceptEdits"}},
		{"a policy's ultracode", PlanTask{Launcher: "first", Ultracode: &on}, startPick{}, nil, startFill{ultracode: true}, startNamed{},
			startPick{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			task := c.task
			if c.name != "read back before any try" {
				own := c.own
				task.callerPick = &own
			}
			task.startFilled, task.startNamed, task.CallerPermission = c.filled, c.named, c.caller
			if got := task.storedLaunch(); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("stored %+v, want %+v", got, c.want)
			}
		})
	}
}
