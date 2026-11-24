package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// taskExecutionUpgradeHub models the node's pre-write capability gate. An upgrade makes strict calls acceptable;
// an attested inherited ordinary task can use the old protocol until then.
type taskExecutionUpgradeHub struct {
	*fakeHub
	compatible atomic.Bool
	mu         sync.Mutex
	strict     []DeviceCall
}

func (h *taskExecutionUpgradeHub) DeviceCall(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
	if call.Method != DeviceAssistantSend && call.Method != assistantwire.DeviceAssistantDirectory {
		return h.fakeHub.DeviceCall(ctx, call)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(call.Params, &fields); err != nil {
		return nil, err
	}
	taskExecution := string(fields["task_execution"]) == "true"
	resume := string(fields["resume_only"]) == "true"
	origin := fields["origin"]
	originKnown := string(fields["origin_known"]) == "true"
	legacy := string(fields[devicewire.LegacyTaskExecution]) == "true"
	attestedLegacy := taskExecution && legacy && originKnown && (string(origin) == `"task"` || string(origin) == "" && resume) &&
		string(fields["is_main"]) != "true" && string(fields["public"]) != "true" && fields["guest"] == nil &&
		fields["external"] == nil && fields["new_session_id"] == nil
	strict := call.Method == assistantwire.DeviceAssistantDirectory || taskExecution && !attestedLegacy
	if strict {
		h.mu.Lock()
		call.Params = append(json.RawMessage(nil), call.Params...)
		h.strict = append(h.strict, call)
		h.mu.Unlock()
		if !h.compatible.Load() {
			return nil, &rpc.Error{Code: proto.CodeNodeOutdated, Message: "node lacks scheduled task execution"}
		}
	}
	if call.Method == DeviceAssistantSend {
		if taskExecution && attestedLegacy && !h.compatible.Load() {
			devicewire.MarkLegacyTaskExecution(ctx)
			delete(fields, "task_execution")
			if resume {
				delete(fields, "cwd")
			}
		}
		delete(fields, devicewire.LegacyTaskExecution)
		call.Params, _ = json.Marshal(fields)
	}
	return h.fakeHub.DeviceCall(ctx, call)
}

func (h *taskExecutionUpgradeHub) strictCalls() []DeviceCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]DeviceCall(nil), h.strict...)
}

func newTaskExecutionWaitService(t *testing.T, f *fakeHub, h Hub, assistantConfig string) *Service {
	t.Helper()
	if assistantConfig != "" {
		file := filepath.Join(t.TempDir(), ManagerFile)
		raw := `{"assistant":` + assistantConfig + `}`
		if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := readOwnConfig(file)
		if err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.hello = c.over(f.hello)
		f.mu.Unlock()
	}
	st := &memStore{}
	return withSettings(t, f, New(Options{Hub: h, Store: func() Store { return st }, Logf: testLogf(t)}))
}

type taskExecutionWaitFixture struct {
	s    *Service
	f    *fakeHub
	h    *taskExecutionUpgradeHub
	task Task
	want map[string]any
}

func newTaskExecutionWaitFixture(t *testing.T, path, assistantConfig string) *taskExecutionWaitFixture {
	t.Helper()
	f := readyHub()
	h := &taskExecutionUpgradeHub{fakeHub: f}
	h.compatible.Store(true)
	s := newTaskExecutionWaitService(t, f, h, assistantConfig)
	task := Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatOnce, At: time.Now().Add(-time.Minute), Enabled: true}
	want := map[string]any{"task_execution": true}
	switch path {
	case "source":
		const cwd = "/home/me/.persona/assistant"
		f.setSessions("server", proto.Session{SessionID: "origin", Launcher: "claude", Model: "source-model", Cwd: cwd, Assistant: true, Space: SpaceHealth})
		f.known["origin"] = true
		task.MachineID, task.SessionID, task.Space = "server", "origin", SpaceHealth
		want["session_id"], want["resume_only"], want["cwd"], want["launcher"], want["model"] = "origin", true, cwd, "claude", "source-model"
	case "new", "directory", "resume":
		setModel(t, s, "task.job", ModelBinding{Provider: "launcher.claude", Model: "chosen-model", Effort: "high"})
		want["launcher"], want["model"], want["effort"], want["cwd"] = "claude", "chosen-model", "high", "/home/me/.persona/assistant"
		if path == "directory" {
			task.Cwd = "/work/project"
			want["cwd"] = "/work/resolved"
			f.answerNode(assistantwire.DeviceAssistantDirectory, func(_ string, raw json.RawMessage) (any, error) {
				var params struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(raw, &params); err != nil {
					return nil, err
				}
				if params.Path != "/work/project" {
					t.Errorf("directory preflight changed the selected path: %s", raw)
				}
				return map[string]any{"cwd": "/work/resolved"}, nil
			})
		}
		if path == "resume" {
			task.RunMachineID, task.RunSessionID, task.RunLauncher = "server", "own", "claude"
			setTasks(s, task)
			ex, err := s.taskExecution(t.Context(), task, true)
			if err != nil {
				t.Fatal(err)
			}
			ex.Cwd, err = s.prepareSpace(t.Context(), "server", "")
			if err != nil {
				t.Fatal(err)
			}
			task.RunExecution = &ex
			f.setSessions("server", proto.Session{SessionID: "own", Launcher: "claude", Model: "chosen-model", Cwd: ex.Cwd, Assistant: true})
			f.known["own"] = true
			want["session_id"], want["resume_only"] = "own", true
		}
	default:
		t.Fatalf("unknown scheduled send path %q", path)
	}
	setTasks(s, task)
	h.compatible.Store(false)
	return &taskExecutionWaitFixture{s: s, f: f, h: h, task: task, want: want}
}

func assertTaskExecutionParams(t *testing.T, got, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if got[key] != value {
			t.Errorf("accepted send changed required %s: got %#v, want %#v; send=%v", key, got[key], value, got)
		}
	}
	if got[devicewire.LegacyTaskExecution] != nil {
		t.Errorf("internal legacy attestation reached the node: %v", got)
	}
}

type taskExecutionResult struct {
	session proto.Session
	err     error
}

func taskExecutionRunResult(t *testing.T, s *Service, task Task) <-chan taskExecutionResult {
	t.Helper()
	done := make(chan taskExecutionResult, 1)
	go func() {
		session, err := s.runTask(task, task.At, time.Now(), false, false)
		done <- taskExecutionResult{session: session, err: err}
	}()
	return done
}

// settleTaskExecutionBubble drains the base implementation's existing retry before reporting an early return.
func settleTaskExecutionBubble(t *testing.T, fx *taskExecutionWaitFixture, result taskExecutionResult) {
	t.Helper()
	if result.session.SessionID != "" {
		fx.f.setSessions("server", proto.Session{SessionID: result.session.SessionID, Assistant: true, State: proto.StateNeedsYou})
	}
	<-time.After(2 * turnPollInterval)
	<-time.After(2 * runRetryDelay)
	synctest.Wait()
}

func TestTaskExecutionCompatibilityWaitDispatchesOnce(t *testing.T) {
	for _, path := range []string{"source", "resume", "new", "directory"} {
		t.Run(path, func(t *testing.T) {
			fx := newTaskExecutionWaitFixture(t, path, "")
			synctest.Test(t, func(t *testing.T) {
				done := taskExecutionRunResult(t, fx.s, fx.task)
				synctest.Wait()
				select {
				case got := <-done:
					settleTaskExecutionBubble(t, fx, got)
					t.Fatalf("run ended before compatible arrival instead of waiting: session=%q err=%v sends=%d runs=%+v", got.session.SessionID, got.err, fx.f.sent(), taskExecutionRuns(fx.s))
				default:
				}
				if fx.f.sent() != 0 {
					t.Fatalf("an old node accepted a strict send: %d", fx.f.sent())
				}
				<-time.After(2 * time.Minute)
				synctest.Wait()
				select {
				case got := <-done:
					t.Fatalf("run ended before the default 600-second bound: %+v", got)
				default:
				}

				fx.h.compatible.Store(true)
				<-time.After(2 * time.Minute)
				synctest.Wait()
				var result taskExecutionResult
				select {
				case result = <-done:
				default:
					t.Fatal("compatible arrival did not release the scheduled send")
				}
				if result.err != nil || result.session.SessionID == "" {
					t.Fatalf("compatible dispatch failed: session=%+v err=%v", result.session, result.err)
				}
				if fx.f.sent() != 1 {
					t.Fatalf("accepted sends = %d, want exactly one", fx.f.sent())
				}
				assertTaskExecutionParams(t, fx.f.lastSend(t), fx.want)
				if path == "new" || path == "directory" {
					if got := fx.f.lastSend(t); got["resume_only"] == true || got["launch"] != "headless" {
						t.Errorf("new scheduled conversation changed its launch semantics: %v", got)
					}
				}
				runs := taskExecutionRuns(fx.s)
				if len(runs) != 1 || runs[0].Status != runOK || runs[0].DeliveryUnknown || runs[0].SessionID != result.session.SessionID {
					t.Fatalf("compatible dispatch history: %+v", runs)
				}

				// End the synthetic turn, then cross the ordinary retry boundary to prove there is no duplicate execution.
				fx.f.setSessions("server", proto.Session{SessionID: result.session.SessionID, Assistant: true, State: proto.StateNeedsYou})
				<-time.After(2 * turnPollInterval)
				synctest.Wait()
				<-time.After(2 * runRetryDelay)
				synctest.Wait()
				if fx.f.sent() != 1 || len(taskExecutionRuns(fx.s)) != 1 {
					t.Fatalf("successful wait dispatched again: sends=%d runs=%+v", fx.f.sent(), taskExecutionRuns(fx.s))
				}
			})
		})
	}
}

func runTaskExecutionTimeout(t *testing.T, fx *taskExecutionWaitFixture, wait time.Duration) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		done := taskExecutionRunResult(t, fx.s, fx.task)
		synctest.Wait()
		var result taskExecutionResult
		if wait == 0 {
			select {
			case result = <-done:
			default:
				t.Fatal("zero-second compatibility policy did not refuse immediately")
			}
		} else {
			select {
			case got := <-done:
				settleTaskExecutionBubble(t, fx, got)
				t.Fatalf("run did not wait for a compatible node: session=%q err=%v sends=%d", got.session.SessionID, got.err, fx.f.sent())
			default:
			}
			<-time.After(wait - time.Second)
			synctest.Wait()
			select {
			case got := <-done:
				t.Fatalf("run timed out before %s: %+v", wait, got)
			default:
			}
			<-time.After(2 * time.Second)
			synctest.Wait()
			select {
			case result = <-done:
			default:
				t.Fatalf("run remained queued past its %s bound", wait)
			}
		}
		if result.err == nil || !errors.Is(result.err, context.DeadlineExceeded) || !errors.Is(result.err, rpc.ErrNotSent) ||
			!strings.Contains(result.err.Error(), "task_execution_wait_timeout") {
			t.Fatalf("timeout is not an explicit definitely-not-sent deadline: %v", result.err)
		}
		if result.session.SessionID != "" || fx.f.sent() != 0 {
			t.Fatalf("deadline produced a model turn: session=%+v sends=%d", result.session, fx.f.sent())
		}
		runs := taskExecutionRuns(fx.s)
		if len(runs) != 1 || runs[0].Status != runFailed || runs[0].DeliveryUnknown ||
			!strings.Contains(runs[0].Error, "task_execution_wait_timeout") {
			t.Fatalf("timeout history does not prove definite non-delivery: %+v", runs)
		}

		before := len(fx.h.strictCalls())
		fx.h.compatible.Store(true)
		<-time.After(2 * runRetryDelay)
		synctest.Wait()
		if after := len(fx.h.strictCalls()); after != before || fx.f.sent() != 0 || len(taskExecutionRuns(fx.s)) != 1 {
			t.Fatalf("timed-out attempt ran late: strict calls %d→%d sends=%d runs=%+v", before, after, fx.f.sent(), taskExecutionRuns(fx.s))
		}
	})
}

func TestTaskExecutionCompatibilityWaitTimeout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		wait   time.Duration
	}{
		{name: "documented default", wait: 600 * time.Second},
		{name: "configured bound", config: `{"task_execution_wait_seconds":2}`, wait: 2 * time.Second},
		{name: "configured zero", config: `{"task_execution_wait_seconds":0}`, wait: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runTaskExecutionTimeout(t, newTaskExecutionWaitFixture(t, "new", tc.config), tc.wait)
		})
	}
}

func TestTaskExecutionRequiredOptionsNeverUseLegacyFallback(t *testing.T) {
	for _, name := range []string{"computer", "directory", "launcher", "model", "effort", "health", "strict snapshot"} {
		t.Run(name, func(t *testing.T) {
			path := "new"
			if name == "directory" {
				path = "directory"
			} else if name == "health" {
				path = "source"
			} else if name == "strict snapshot" {
				path = "resume"
			}
			fx := newTaskExecutionWaitFixture(t, path, `{"task_execution_wait_seconds":2}`)
			switch name {
			case "computer":
				fx.task.ExecutionMachineID = "server"
			case "launcher":
				setModel(t, fx.s, "task.job", ModelBinding{Provider: "launcher.claude"})
				delete(fx.want, "model")
				delete(fx.want, "effort")
			case "effort":
				setModel(t, fx.s, "task.job", ModelBinding{Provider: "launcher.claude", Effort: "low"})
				fx.want["effort"] = "low"
				delete(fx.want, "model")
			}
			setTasks(fx.s, fx.task)
			runTaskExecutionTimeout(t, fx, 2*time.Second)
			for _, call := range fx.h.strictCalls() {
				if call.Method != DeviceAssistantSend {
					continue
				}
				var params map[string]any
				if err := json.Unmarshal(call.Params, &params); err != nil {
					t.Fatal(err)
				}
				assertTaskExecutionParams(t, params, fx.want)
			}
		})
	}
}

func TestTaskExecutionLegacyFallbackPolicyDisabled(t *testing.T) {
	fx := newTaskExecutionWaitFixture(t, "new", `{"task_execution_wait_seconds":2,"task_execution_legacy_fallback":false}`)
	setModel(t, fx.s, "task.job", ModelBinding{})
	setTasks(fx.s, fx.task)
	runTaskExecutionTimeout(t, fx, 2*time.Second)
}

func TestTaskExecutionCompatibilityWaitPreservesUncertainDelivery(t *testing.T) {
	for _, path := range []string{"source", "resume", "new", "directory"} {
		t.Run(path, func(t *testing.T) {
			fx := newTaskExecutionWaitFixture(t, path, "")
			fx.h.compatible.Store(true)
			var attempts atomic.Int32
			fx.f.answerNode(DeviceAssistantSend, func(string, json.RawMessage) (any, error) {
				attempts.Add(1)
				return nil, rpc.ErrClosed
			})
			synctest.Test(t, func(t *testing.T) {
				if _, err := fx.s.runTask(fx.task, fx.task.At, time.Now(), false, false); err == nil {
					t.Fatal("uncertain send reported success")
				}
				runs := taskExecutionRuns(fx.s)
				if len(runs) != 1 || runs[0].Status != runFailed || !runs[0].DeliveryUnknown {
					t.Fatalf("uncertain delivery was reclassified: %+v", runs)
				}
				<-time.After(2 * runRetryDelay)
				synctest.Wait()
				if attempts.Load() != 1 || fx.f.sent() != 0 || len(taskExecutionRuns(fx.s)) != 1 {
					t.Fatalf("uncertain delivery waited or replayed: attempts=%d sends=%d runs=%+v", attempts.Load(), fx.f.sent(), taskExecutionRuns(fx.s))
				}
			})
		})
	}
}

func TestTaskExecutionActualLegacyFallbackRunHistory(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		name := "new"
		if resumed {
			name = "resumed"
		}
		t.Run(name, func(t *testing.T) {
			f := readyHub()
			h := &taskExecutionUpgradeHub{fakeHub: f}
			h.compatible.Store(resumed)
			s := newTaskExecutionWaitService(t, f, h, "")
			setTasks(s, Task{ID: "job", Title: "Default task", Prompt: "Check files", Repeat: repeatOnce, At: time.Now(), Enabled: true})
			if resumed {
				first, err := runNow(t, s, "job")
				if err != nil || first.SessionID == "" || f.lastSend(t)["task_execution"] != true {
					t.Fatalf("initial compatible run: session=%+v err=%v send=%v", first, err, f.lastSend(t))
				}
				history := taskExecutionRunHistory(t, s)
				if len(history) != 1 || history[0]["compatibility_fallback"] == "legacy_task_execution" {
					t.Fatalf("eligibility alone was recorded as an actual fallback: %v", history)
				}
				h.compatible.Store(false)
			}
			got, err := runNow(t, s, "job")
			if err != nil || got.SessionID == "" {
				t.Fatalf("attested inherited ordinary run did not use legacy fallback: session=%+v err=%v", got, err)
			}
			send := f.lastSend(t)
			if send["task_execution"] != nil || send[devicewire.LegacyTaskExecution] != nil {
				t.Fatalf("old-node fallback did not consume exactly the optional capability: %v", send)
			}
			history := taskExecutionRunHistory(t, s)
			wantRuns := 1
			if resumed {
				wantRuns = 2
			}
			if len(history) != wantRuns {
				t.Fatalf("fallback duplicated run history: %v", history)
			}
			latest := history[0]
			if latest["compatibility_fallback"] != "legacy_task_execution" {
				t.Fatalf("actual legacy omission missing from run history JSON: %v", latest)
			}
			execution, _ := latest["execution"].(map[string]any)
			if execution["legacy_compatible"] != true {
				t.Fatalf("fallback lost its separate eligibility evidence: %v", latest)
			}
			if latest["conversation"] != name || latest["delivery_unknown"] == true {
				t.Fatalf("fallback run outcome: %v", latest)
			}
		})
	}
}

func TestTaskExecutionCompatibilityManagerConfig(t *testing.T) {
	t.Run("manager values reach the assistant configuration", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), ManagerFile)
		raw := `{"assistant":{"task_execution_wait_seconds":45,"task_execution_legacy_fallback":false}}`
		if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := readOwnConfig(file)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(c.over(Hello{}).Assistant)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if got["task_execution_wait_seconds"] != float64(45) || got["task_execution_legacy_fallback"] != false {
			t.Fatalf("manager.json scheduling policy was dropped: %s", encoded)
		}
	})

	// The configuration contract is 0–3600 seconds, default 600; zero refuses immediately. Legacy fallback defaults on and is a boolean.
	for _, value := range []string{"-1", "3601", "1.5", `"45"`} {
		t.Run("invalid wait "+value, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), ManagerFile)
			raw := `{"assistant":{"task_execution_wait_seconds":` + value + `}}`
			if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readOwnConfig(file); err == nil {
				t.Fatalf("accepted task_execution_wait_seconds=%s", value)
			}
		})
	}
	t.Run("invalid fallback policy", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), ManagerFile)
		if err := os.WriteFile(file, []byte(`{"assistant":{"task_execution_legacy_fallback":"sometimes"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readOwnConfig(file); err == nil {
			t.Fatal("accepted a non-boolean legacy fallback policy")
		}
	})
}

func TestNodeUpdateHandoffManagerConfig(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "configured", value: "45", want: 45 * time.Second},
		{name: "natural idle only", value: "0", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), ManagerFile)
			raw := `{"assistant":{"node_update_handoff_grace_seconds":` + tc.value + `}}`
			if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := readOwnConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			if got := nodeUpdateHandoffGraceOf(c.over(Hello{})); got != tc.want {
				t.Fatalf("handoff grace = %s, want %s", got, tc.want)
			}
		})
	}
	if got := nodeUpdateHandoffGraceOf(Hello{}); got != time.Minute {
		t.Fatalf("default handoff grace = %s, want one minute", got)
	}
	for _, value := range []string{"-1", "86401", "1.5", `"45"`} {
		t.Run("invalid grace "+value, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), ManagerFile)
			raw := `{"assistant":{"node_update_handoff_grace_seconds":` + value + `}}`
			if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readOwnConfig(file); err == nil {
				t.Fatalf("accepted node_update_handoff_grace_seconds=%s", value)
			}
		})
	}
}

func taskExecutionRuns(s *Service) []ScheduleRun {
	_, runs := scheduleState(s)
	return runs
}

// GET /api/v1/assistant/runs is the run-history JSON contract consumed by clients.
func taskExecutionRunHistory(t *testing.T, s *Service) []map[string]any {
	t.Helper()
	r := request(s, "GET", "/api/v1/assistant/runs?ref=job", "")
	if r.Code != http.StatusOK {
		t.Fatalf("run history: %d %s", r.Code, r.Body.String())
	}
	var body struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Runs
}
