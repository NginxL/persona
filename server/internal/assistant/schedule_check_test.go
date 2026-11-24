package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

type routineCheckHub struct {
	*fakeHub
	result  proto.RoutineCheckResult
	err     error
	calls   int
	machine string
}

func (h *routineCheckHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method != proto.MethodRoutineCheck {
		return h.fakeHub.DeviceCall(ctx, p)
	}
	h.calls++
	h.machine = p.Device
	if h.err != nil {
		return nil, h.err
	}
	return json.Marshal(h.result)
}

func TestRoutineCheckRun(t *testing.T) {
	for _, tc := range []struct {
		name                                   string
		result                                 proto.RoutineCheckResult
		err                                    error
		policy                                 string
		manual, retried, once, skipped, warned bool
	}{
		{name: "pass"},
		{name: "skip", result: proto.RoutineCheckResult{ExitCode: 1, Reason: "holiday"}, skipped: true},
		{name: "continue after timeout", err: context.DeadlineExceeded, warned: true},
		{name: "skip when offline", err: errors.New("offline"), policy: "skip", skipped: true},
		{name: "manual ignores skip", result: proto.RoutineCheckResult{ExitCode: 1}, manual: true},
		{name: "one-off ignores skip", result: proto.RoutineCheckResult{ExitCode: 1}, once: true},
		{name: "retry continues the admitted run", result: proto.RoutineCheckResult{ExitCode: 1}, retried: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := readyHub()
			h := &routineCheckHub{fakeHub: f, result: tc.result, err: tc.err}
			st := &memStore{}
			s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return st }, Logf: testLogf(t)}))
			task := Task{ID: "test", Title: "Brief", Prompt: "Summarize", Repeat: repeatDaily, Time: "09:00", Facts: tc.skipped,
				RunIf: &routineCheck{RoutineCheckParams: proto.RoutineCheckParams{Command: "check", Timeout: 1}, OnError: tc.policy}}
			now := time.Now()
			if tc.once {
				task.Repeat, task.At = repeatOnce, now
			}
			setTasks(s, task)
			if _, err := s.runTask(task, now, now, tc.manual, tc.retried); err != nil {
				t.Fatal(err)
			}
			r := lastRunOf(t, s, task.ID)
			if r.ConditionSkipped != tc.skipped || (r.RunIfError != "") != tc.warned || (r.Status == runSkipped) != tc.skipped {
				t.Fatalf("unexpected run: %+v", r)
			}
			if (h.calls == 0) != (tc.manual || tc.once || tc.retried) {
				t.Fatalf("unexpected check count: %d", h.calls)
			}
			if !tc.skipped && (len(f.sends) == 0 || r.Retried != tc.retried) {
				t.Fatalf("run was not dispatched as expected: %+v", r)
			}
			if tc.skipped && (len(f.sends) != 0 || len(f.pushed()) != 0 || counted(r)) {
				t.Fatalf("skip started work or counted as failure: %+v", r)
			}
			if tc.skipped {
				failures, total := s.todayFailures(t.Context(), "*", now, time.UTC)
				if len(failures) != 0 || total != 0 {
					t.Fatalf("condition skip counted in failure summary: total=%d, failures=%+v", total, failures)
				}
			}
			if tc.name == "skip" && r.Error != "holiday" {
				t.Fatalf("lost skip reason: %+v", r)
			}
		})
	}
}

func TestRoutineCheckApprovalUnaffectedByReversibleSettingsSwitch(t *testing.T) {
	s, _ := newReversibleSettingService(t, true)
	shown := make(chan proto.Interaction, 1)
	go answerArchiveCard(t, s, false, shown)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	approved, err := s.approveRoutineCheck(ctx, "server", "conv1", &routineCheck{RoutineCheckParams: proto.RoutineCheckParams{Command: "check", Timeout: 1}})
	if err != nil || approved {
		t.Fatalf("run_if approval = %v, %v", approved, err)
	}
	if card := <-shown; card.Kind != proto.IxPermission || card.ToolName != "schedule_routine" {
		t.Fatalf("run_if card: %+v", card)
	}
}

func TestRoutineCheckUsesTaskExecutionComputer(t *testing.T) {
	for _, previous := range []string{"", "previous"} {
		t.Run("previous="+previous, func(t *testing.T) {
			f := readyHub()
			f.devices = append(f.devices, Device{ID: "other", Name: "Other computer", Online: true})
			h := &routineCheckHub{fakeHub: f, result: proto.RoutineCheckResult{ExitCode: 1, Reason: "nothing new"}}
			st := &memStore{}
			s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return st }, Logf: testLogf(t)}))
			task := Task{ID: "scoped", Title: "Check files", Prompt: "Summarize", Repeat: repeatDaily, Time: "09:00",
				ExecutionMachineID: "other", RunMachineID: previous,
				RunIf: &routineCheck{RoutineCheckParams: proto.RoutineCheckParams{Command: "check", Timeout: 1}}}
			setTasks(s, task)
			now := time.Now()
			if _, err := s.runTask(task, now, now, false, false); err != nil {
				t.Fatal(err)
			}
			if h.calls != 1 || h.machine != "other" {
				t.Fatalf("pre-run command used %q instead of the selected computer; calls=%d", h.machine, h.calls)
			}
			if r := lastRunOf(t, s, task.ID); !r.ConditionSkipped || f.sent() != 0 {
				t.Fatalf("pre-run skip started a model turn: %+v sends=%d", r, f.sent())
			}
		})
	}
}

func TestRoutineCheckUpdatePreservesAndClears(t *testing.T) {
	s, _, st := scheduleStorageFixture(t)
	const body = `{"title":"Daily","prompt":"Example","repeat":"daily","time":"09:00","enabled":true`
	for _, tc := range []struct {
		field string
		want  bool
	}{
		{field: `,"run_if":{"command":"check"}`, want: true},
		{want: true},
		{field: `,"run_if":null`},
	} {
		w := scheduleRequest(s, "PUT", "tasks/daily", body+tc.field+"}")
		task, _ := taskByID(s, "daily")
		if w.Code != http.StatusOK || (task.RunIf != nil) != tc.want {
			t.Fatalf("PUT %s: %d %s, check=%+v", tc.field, w.Code, w.Body, task.RunIf)
		}
		if tc.want && task.RunIf.Timeout != proto.RoutineCheckDefaultTimeout {
			t.Fatalf("wrong default: %+v", task.RunIf)
		}
		if tc.field != "" && tc.want {
			if _, err := s.toolScheduleRoutine(t.Context(), "", "", json.RawMessage(`{"id":"daily","enabled":false}`)); err != nil {
				t.Fatal(err)
			}
			task, _ := taskByID(s, "daily")
			if task.RunIf == nil {
				t.Fatal("tool update cleared the check")
			}
		}
		assertScheduleStored(t, s, st)
	}
	w := scheduleRequest(s, "PUT", "tasks/daily", body+`,"run_if":{"command":"check","timeout_s":0}}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "timeout_s") {
		t.Fatalf("invalid check: %d %s", w.Code, w.Body)
	}
}
