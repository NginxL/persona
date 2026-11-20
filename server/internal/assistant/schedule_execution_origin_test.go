package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestTaskExecutionOriginEdits(t *testing.T) {
	for _, tc := range []struct {
		name, patch string
		want        int
		own         bool
	}{
		{"inherit computer only", `"execution_machine_id":""`, http.StatusOK, false},
		{"clear defaults", `"execution_machine_id":"","cwd":"","launcher":"","model":"","effort":""`, http.StatusOK, false},
		{"pin inherited computer", `"execution_machine_id":"server"`, http.StatusBadRequest, false},
		{"set directory", `"cwd":"/work/project"`, http.StatusBadRequest, false},
		{"set model", `"model":"test-model"`, http.StatusBadRequest, false},
		{"own chat can pin computer", `"execution_machine_id":"server"`, http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			s := newTestService(t, f)
			setTasks(s, Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, MachineID: "server", SessionID: "origin"})
			sessionID := "origin"
			machine := ""
			if tc.own {
				sessionID, machine = "", "server"
			}
			body := fmt.Sprintf(`{"title":"Check","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"session_id":%q,%s}`, sessionID, tc.patch)
			r := request(s, "PUT", "/api/v1/assistant/tasks/job", body)
			if r.Code != tc.want {
				t.Fatalf("save: %d %s", r.Code, r.Body.String())
			}
			if tc.want == http.StatusBadRequest && !strings.Contains(r.Body.String(), "execution_own_chat") && !strings.Contains(r.Body.String(), "原来的对话") {
				t.Fatalf("unrelated validation: %s", r.Body.String())
			}
			got, _ := taskByID(s, "job")
			if got.Title != "Check" || got.Prompt != "Check files" || got.Repeat != repeatInterval || got.EveryMinutes != 30 || !got.Enabled {
				t.Fatalf("execution edit changed task content or schedule: %+v", got)
			}
			if got.SessionID != sessionID || got.ExecutionMachineID != machine {
				t.Fatalf("execution edit changed the wrong context: %+v", got)
			}
		})
	}
}

func TestTaskExecutionFreshRecurringTaskKeepsOriginAndUsesOverrides(t *testing.T) {
	f := readyHub()
	f.sessions["remote"] = nil
	f.known["origin"] = true
	f.zshrc = map[string][]proto.Launcher{
		"server": {{Name: "claude", Agent: "claude"}, {Name: "codex", Agent: "codex"}},
		"remote": {{Name: "codex", Agent: "codex"}},
	}
	f.answerNode("assistant.directory", func(machine string, raw json.RawMessage) (any, error) {
		var params struct{ Path string }
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		if machine != "remote" || params.Path != "/work/project" {
			t.Errorf("validated directory on %s: %s", machine, raw)
		}
		return map[string]any{"cwd": "/work/resolved"}, nil
	})
	s := newTestService(t, f)
	setTasks(s, Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatOnce, At: time.Now().Add(time.Hour), Enabled: true, MachineID: "server", SessionID: "origin"})
	const fields = `,"launcher":"codex","model":"test-model","execution_machine_id":"remote","cwd":"/work/project"`
	save := func(rule, fields string, fresh bool, revision int, want int) {
		t.Helper()
		body := fmt.Sprintf(`{"title":"Check","prompt":"Check files","enabled":true,"session_id":"origin","fresh_each_run":%t,"model_revision":%d,%s%s}`, fresh, revision, rule, fields)
		if r := request(s, "PUT", "/api/v1/assistant/tasks/job", body); r.Code != want {
			t.Fatalf("save: got %d, want %d: %s", r.Code, want, r.Body)
		}
	}
	once := fmt.Sprintf(`"repeat":"once","at":%q`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	save(once, fields, true, 0, http.StatusBadRequest)
	const interval = `"repeat":"interval","every_minutes":30`
	save(interval, fields, true, 0, http.StatusOK)
	save(interval, "", false, 1, http.StatusBadRequest)

	last := "origin"
	for i := 1; i <= 3; i++ {
		task, _ := taskByID(s, "job")
		if i == 3 {
			if r := request(s, "POST", "/api/v1/assistant/tasks/job/run", `{}`); r.Code != http.StatusOK {
				t.Fatalf("manual run: %d %s", r.Code, r.Body)
			}
		} else {
			s.runDue(t.Context(), task.At)
			s.sched.inflight.Wait()
		}
		got := waitRuns(t, s, i)[i-1]
		if got.Status != runOK || got.SessionID == "" || got.SessionID == "origin" || got.SessionID == last ||
			got.MachineID != "remote" || got.Conversation != "new" || got.Manual != (i == 3) {
			t.Fatalf("run %d did not make a fresh task conversation: %+v", i, got)
		}
		if send := f.lastSend(t); send["launcher"] != "codex" || send["model"] != "test-model" || send["cwd"] != "/work/resolved" ||
			send["task_execution"] != true || send["resume_only"] == true {
			t.Fatalf("run %d did not use the validated configuration: %v", i, send)
		}
		last = got.SessionID
	}
	stored, _ := taskByID(s, "job")
	if stored.SessionID != "origin" || !stored.FreshEachRun || stored.Cwd != "/work/project" || stored.RunSessionID != last {
		t.Fatalf("dispatch changed the task's saved configuration: %+v", stored)
	}
	if _, err := s.toolScheduleRoutine(t.Context(), "server", "origin", json.RawMessage(`{"id":"job","fresh_each_run":false}`)); err == nil || !strings.Contains(err.Error(), "原来的对话") {
		t.Fatalf("routine tool restored a source while overrides were set: %v", err)
	}
	save(interval, `,"launcher":"","model":"","execution_machine_id":"","cwd":""`, false, 1, http.StatusOK)
	save(interval, `,"launcher":"codex"`, true, 2, http.StatusOK)
	save(interval, "", false, 3, http.StatusBadRequest)
	save(interval, `,"launcher":""`, false, 3, http.StatusOK)
	if got, err := runNow(t, s, "job"); err != nil || got.SessionID != "origin" {
		t.Fatalf("restoring the source: %+v %v", got, err)
	}
}

func TestTaskExecutionOriginClearsHistoricalOverrides(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	setTasks(s, Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, MachineID: "server", SessionID: "origin", ExecutionMachineID: "server", Cwd: "/work/project"})
	setModel(t, s, "task.job", ModelBinding{Provider: "launcher.claude", Model: "test-model"})
	body := `{"title":"Changed","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"session_id":"origin"}`
	if r := request(s, "PUT", "/api/v1/assistant/tasks/job", body); r.Code != http.StatusBadRequest {
		t.Fatalf("historical overrides silently retained: %d %s", r.Code, r.Body.String())
	}
	body = strings.TrimSuffix(body, "}") + `,"execution_machine_id":"","cwd":"","launcher":"","model":"","effort":""}`
	// Clearing overrides to continue the source needs no live model or directory validation.
	f.devices = []Device{{ID: "server", Online: false}}
	if r := request(s, "PUT", "/api/v1/assistant/tasks/job", body); r.Code != http.StatusOK {
		t.Fatalf("clear historical overrides: %d %s", r.Code, r.Body.String())
	}
	task, _ := taskByID(s, "job")
	record, err := s.modelRecord(context.Background(), "task.job")
	if err != nil || task.SessionID != "origin" || task.Cwd != "" || task.ExecutionMachineID != "" || record.Binding != (ModelBinding{}) {
		t.Fatalf("did not restore inheritance: %+v %+v %v", task, record, err)
	}
}

func TestTaskSourceFallbackDeliveryBoundary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		fallback bool
	}{
		{"gone", fmt.Errorf("conversation origin is gone"), true},
		{"offline before dispatch", fmt.Errorf("source: %w", apierr.ErrOffline), true},
		{"old node before dispatch", fmt.Errorf("source: %w", &rpc.Error{Code: proto.CodeNodeOutdated, Message: "update the node"}), true},
		{"link closed", rpc.ErrClosed, false},
		{"reply lost", context.DeadlineExceeded, false},
		{"other refusal", errors.New("send failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			s := newTestService(t, f)
			s.runRetry = 10 * time.Millisecond
			var source, own atomic.Int32
			f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
				var p struct {
					SessionID string `json:"session_id"`
				}
				_ = json.Unmarshal(raw, &p)
				if p.SessionID == "origin" {
					source.Add(1)
					return nil, tc.err
				}
				own.Add(1)
				return map[string]string{"session_id": "own"}, nil
			})
			// The fixture's HTTP server stays outside the virtual-time bubble.
			check := func(t *testing.T) {
				setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatOnce, Enabled: true, At: time.Now().Add(-time.Minute), MachineID: "server", SessionID: "origin"})
				task, _ := taskByID(s, "job")
				now := time.Now()
				got, err := s.runTask(task, now, now, false, false)
				if tc.fallback {
					if err != nil || got.SessionID != "own" || own.Load() != 1 {
						t.Fatalf("safe fallback: %+v %v source=%d own=%d", got, err, source.Load(), own.Load())
					}
				} else {
					if err == nil || own.Load() != 0 {
						t.Fatalf("uncertain send replaced: %+v %v own=%d", got, err, own.Load())
					}
					if len(f.pushed()) == 0 {
						t.Fatal("uncertain delivery lost the failure notice")
					}
					// Advance past the delayed retry on the virtual clock; uncertain sends must stay single.
					<-time.After(10 * s.runRetry)
					synctest.Wait()
					if source.Load() != 1 || own.Load() != 0 {
						t.Fatalf("uncertain source retried: source=%d own=%d", source.Load(), own.Load())
					}
				}
			}
			if tc.fallback {
				check(t)
			} else {
				synctest.Test(t, check)
			}
		})
	}
}

func TestTaskSourceOnOlderNodeFallsBackBeforeDelivery(t *testing.T) {
	f := newFakeHub().withMachine()
	f.devices = []Device{{ID: "server", Online: true}, {ID: "old", Online: true}}
	f.setSessions("old", proto.Session{SessionID: "origin", Launcher: "claude", Model: "source-model"})
	s := newTestService(t, f)
	var source, own atomic.Int32
	f.answerNode(DeviceAssistantSend, func(machine string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if machine == "old" {
			source.Add(1)
			if p["session_id"] != "origin" || p["resume_only"] != true || p["model"] != "source-model" {
				t.Errorf("source request did not exercise the model capability gate: %+v", p)
			}
			// devices.Machine.callOnce refuses model parameters on a pre-model-configuration node before RPC dispatch.
			return nil, &rpc.Error{Code: proto.CodeNodeOutdated, Message: "update the node"}
		}
		own.Add(1)
		if machine != "server" || p["session_id"] == "origin" || p["resume_only"] == true {
			t.Errorf("fallback reused the unavailable source: %s %+v", machine, p)
		}
		return map[string]string{"session_id": "own"}, nil
	})
	setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, MachineID: "old", SessionID: "origin"})
	got, err := runNow(t, s, "job")
	if err != nil || got.MachineID != "server" || got.SessionID != "own" || source.Load() != 1 || own.Load() != 1 {
		t.Fatalf("old source fallback: %+v %v source=%d own=%d", got, err, source.Load(), own.Load())
	}
	if run := waitRuns(t, s, 1)[0]; run.Conversation != "new" || run.ConversationReason != "source_unavailable" || run.Execution.MachineID != "server" {
		t.Fatalf("fallback execution missing from history: %+v", run)
	}
}

func TestTaskSourceGoneOnAnExistingMachine(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, MachineID: "server", SessionID: "gone"})
	got, err := runNow(t, s, "job")
	if err != nil || got.SessionID == "" || got.SessionID == "gone" {
		t.Fatalf("missing source did not fall back: %s %v", got.SessionID, err)
	}
	runs := waitRuns(t, s, 1)
	if runs[0].Conversation != "new" || runs[0].ConversationReason != "source_unavailable" {
		t.Fatalf("source fallback absent from history: %+v", runs[0])
	}
}

func TestTaskSourceModelPreparationFallsBackBeforeSending(t *testing.T) {
	s, f := pickFixture(t)
	// Recurring runs are isolated from the main chat; a one-off can still continue it with its model pick.
	setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatOnce, Enabled: true, MachineID: "server", SessionID: "main"})
	// This fixture supplies an authoritative launcher list: "gone" is refused before assistant.send.
	if err := s.setModelPick(context.Background(), "main", &modelPick{MachineID: "server", Launcher: "gone", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	got, err := runNow(t, s, "job")
	if err != nil || got.SessionID == "" || got.SessionID == "main" || f.sent() != 1 {
		t.Fatalf("model preparation fallback: %s %v sends=%d", got.SessionID, err, f.sent())
	}
	if runs := waitRuns(t, s, 1); runs[0].ConversationReason != "source_unavailable" {
		t.Fatalf("source model preparation was not exercised: %+v", runs[0])
	}
}

func TestTaskSourceRejectsExplicitExecutionBeforeSending(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, MachineID: "server", SessionID: "origin", ExecutionMachineID: "server"})
	if _, err := runNow(t, s, "job"); err == nil || f.sent() != 0 {
		t.Fatalf("explicit source context ignored: %v sent=%d", err, f.sent())
	}
}

func TestTaskExecutionKnownMissingLauncherIsNotSubstituted(t *testing.T) {
	s, f := pickFixture(t)
	setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	setModel(t, s, "task.job", ModelBinding{Provider: "launcher.gone"})
	if _, err := runNow(t, s, "job"); err == nil || f.sent() != 0 {
		t.Fatalf("missing launcher was substituted: %v sent=%d", err, f.sent())
	}
}
