package assistant

import (
	"testing"
	"time"
)

func TestTaskExecutionProgressUsesAdmittedSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, selectedTodo, currentTodo, repeat string
	}{
		{"rebound", "old-todo", "current-todo", repeatInterval},
		{"associated", "", "current-todo", repeatInterval},
		{"cleared", "old-todo", "", repeatInterval},
		{"now one-off", "old-todo", "old-todo", repeatOnce},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			st := newSettingsFaultStore()
			s := newServiceOn(t, f, st)
			current := Task{ID: "snapshot-progress", Title: "Current check", Prompt: "Check progress",
				Repeat: tc.repeat, EveryMinutes: 30, Enabled: true, TodoID: tc.currentTodo}
			selected := current
			selected.TodoID, selected.Repeat = tc.selectedTodo, repeatInterval
			setTasks(s, current)
			// The scheduler selected its copy before an edit. Admission reads the current task and model together.
			// Fail model admission before any send so history must identify precisely that admitted task.
			st.failRead = modelRecordKey("task." + current.ID)
			now := time.Now()
			if _, err := s.runTask(selected, now, now, true, false); err == nil || f.sent() != 0 {
				t.Fatalf("failed admission sent a turn: err=%v sends=%d", err, f.sent())
			}
			_, runs := scheduleState(s)
			if len(runs) != 1 || runs[0].Status != runFailed || runs[0].DeliveryUnknown {
				t.Fatalf("lost confirmed pre-delivery failure: %+v", runs)
			}
			progress := runs[0].TodoProgress
			if tc.currentTodo == "" || tc.repeat == repeatOnce {
				if progress != nil {
					t.Fatalf("unlinked or one-off admitted task retained stale progress: %+v", progress)
				}
			} else if progress == nil || progress.TodoID != tc.currentTodo || progress.State != "not_run" {
				t.Fatalf("history describes the selected copy instead of the admitted association: %+v", progress)
			}
		})
	}
}

func TestTaskExecutionBusyRunKeepsProgressAssessment(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	task := Task{ID: "busy-progress", Title: "Progress check", Prompt: "Check progress",
		Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, TodoID: "current-todo"}
	setTasks(s, task)
	if !s.sched.begin(task.ID) {
		t.Fatal("could not reserve the task's existing send")
	}
	defer s.sched.end(task.ID)
	now := time.Now()
	if _, err := s.runTask(task, now, now, true, false); err != errTaskBusy || f.sent() != 0 {
		t.Fatalf("busy task was sent: err=%v sends=%d", err, f.sent())
	}
	_, runs := scheduleState(s)
	if len(runs) != 1 || runs[0].Status != runSkipped || runs[0].TodoProgress == nil ||
		runs[0].TodoProgress.TodoID != task.TodoID || runs[0].TodoProgress.State != "not_run" {
		t.Fatalf("busy skip lost its selected task's progress assessment: %+v", runs)
	}
}
