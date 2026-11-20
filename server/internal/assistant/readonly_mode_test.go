package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestReadonlyModeSettingsHTTP(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	f := newFakeHub().withMachine()
	f.settings = nil
	s := newServiceOn(t, f, st)
	w, view := permissionSettingsRequest(t, s, "GET", "/api/v1/assistant", "assistant", "full", "")
	if w.Code != http.StatusOK || view.ReadonlyMode != readonlySandbox {
		t.Fatalf("default: %d %+v", w.Code, view)
	}
	before, _ := s.savedSettings(ctx)
	for _, bad := range []string{`""`, `null`, `true`, `17`, `{}`, `"unknown"`, `"PROMPT"`} {
		w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", "assistant", "full",
			`{"readonly_mode":`+bad+`,"notes":"must not be saved"}`)
		if got, _ := s.savedSettings(ctx); w.Code != http.StatusBadRequest || !reflect.DeepEqual(got, before) {
			t.Fatalf("invalid %s: %d %+v", bad, w.Code, got)
		}
	}
	for _, tc := range []struct {
		path, target, role string
		status             int
	}{
		{"/api/v1/assistant", "", "full", http.StatusBadRequest},
		{"/api/v1/assistant", "m-other", "full", http.StatusForbidden},
		{"/api/v1/assistant?manager=m-other", "assistant", "full", http.StatusBadRequest},
		{"/api/v1/assistant", "assistant", "answer", http.StatusForbidden},
		{"/api/v1/assistant", "assistant", "extension", http.StatusForbidden},
	} {
		w, _ := permissionSettingsRequest(t, s, "PUT", tc.path, tc.target, tc.role, `{"readonly_mode":"prompt"}`)
		if got, _ := s.savedSettings(ctx); w.Code != tc.status || !reflect.DeepEqual(got, before) {
			t.Fatalf("scope %+v: %d %+v", tc, w.Code, got)
		}
	}
	for _, mode := range []string{readonlyPrompt, readonlySandbox} {
		w, view := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", "assistant", "full",
			`{"readonly_mode":"`+mode+`"}`)
		if w.Code != http.StatusOK || view.ReadonlyMode != mode {
			t.Fatalf("save %s: %d %+v", mode, w.Code, view)
		}
		s = newServiceOn(t, f, st)
		if got := s.view(ctx, false).ReadonlyMode; got != mode {
			t.Fatalf("reload = %q, want %q", got, mode)
		}
	}
	setSettings(t, s, func(v *Settings) { v.ReadonlyMode = "unknown" })
	if got := s.view(ctx, false).ReadonlyMode; got != readonlySandbox {
		t.Fatalf("unknown saved mode did not fail safe: %q", got)
	}
}

func TestReadonlyModeManagersKeepIndependentSettings(t *testing.T) {
	f := newFakeHub().withMachine()
	f.settings = nil
	stores := []*memStore{{}, {}}
	ids := []string{"assistant", "m-example"}
	for pass := 0; pass < 2; pass++ {
		for i, id := range ids {
			store := stores[i]
			s := withSettings(t, f, New(Options{ID: id, Hub: f, Store: func() Store { return store }, Logf: testLogf(t)}))
			if pass == 0 && i == 0 {
				w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", id, "full", `{"readonly_mode":"prompt"}`)
				if w.Code != http.StatusOK {
					t.Fatalf("save: %d %s", w.Code, w.Body)
				}
			}
			want := readonlySandbox
			if i == 0 {
				want = readonlyPrompt
			}
			if got := s.view(context.Background(), false).ReadonlyMode; got != want {
				t.Fatalf("pass %d manager %s: %q, want %q", pass, id, got, want)
			}
		}
	}
}

func TestReadonlyModeToolConfirmation(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name    string
		answer  proto.Resolution
		applied bool
	}{
		{"allow", proto.Resolution{Allow: &yes}, true},
		{"deny", proto.Resolution{Allow: &no}, false},
		{"assistant cannot approve", proto.Resolution{Allow: &yes, AnsweredBy: proto.AnsweredByAssistant}, false},
		{"dismiss", proto.Resolution{Dismiss: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestService(t, newFakeHub().withMachine())
			tool := func(args string) proto.ToolResult {
				return s.Tool(ctx, ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "c1", Arguments: json.RawMessage(args)})
			}
			for _, action := range []string{`{"action":"list"}`, `{"action":"get","key":"assistant.readonly_mode"}`} {
				r := tool(action)
				if r.IsError || !strings.Contains(r.Content[0].Text, readonlySandbox) {
					t.Fatalf("%s: %+v", action, r)
				}
			}
			for _, value := range []string{`null`, `""`, `true`, `"bad"`} {
				r := tool(`{"action":"set","key":"assistant.readonly_mode","value":` + value + `}`)
				if !r.IsError || len(s.pendingQuestions()) != 0 {
					t.Fatalf("invalid value %s: %+v", value, r)
				}
			}
			done := make(chan proto.ToolResult, 1)
			go func() {
				done <- tool(`{"action":"set","key":"assistant.readonly_mode","value":"prompt"}`)
			}()
			card, ok := awaitCard(s)
			if !ok {
				t.Fatal("no owner confirmation card")
			}
			if readonlyModeOf(s.settings(ctx)) != readonlySandbox || !strings.Contains(card.Summary, readonlyPrompt) ||
				!strings.Contains(card.Summary, readonlySandbox) || card.ToolName != "persona_settings" {
				t.Fatalf("changed before approval, or missing warning: %+v", card)
			}
			raw, _ := json.Marshal(tc.answer)
			if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: card.ID, Answer: raw}); err != nil {
				t.Fatal(err)
			}
			select {
			case r := <-done:
				if r.IsError {
					t.Fatalf("set: %+v", r)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("settings tool did not return")
			}
			want := readonlySandbox
			if tc.applied {
				want = readonlyPrompt
			}
			r := tool(`{"action":"get","key":"assistant.readonly_mode"}`)
			if r.IsError || r.Content[0].Text != want {
				t.Fatalf("confirmed setting = %+v, want %q", r, want)
			}
		})
	}
}

func TestReadonlyModeTaskPlan(t *testing.T) {
	for _, mode := range []string{"", readonlySandbox, readonlyPrompt, "unknown"} {
		for _, launcher := range []proto.Launcher{
			{Name: "task-runtime", Agent: "codex"},
			{Name: "task-runtime", Agent: "claude"},
			{Name: "task-runtime", Agent: "paseo", Source: "paseo", AutoAccept: true},
		} {
			for _, permission := range []string{"", "plan", "bypassPermissions"} {
				t.Run(mode+"/"+launcher.Agent+"/"+permission, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						s, f := readonlyTaskService(t)
						f.launcherList = []proto.Launcher{launcher}
						setSettings(t, s, func(v *Settings) { v.ReadonlyMode, v.DefaultPermissionMode = mode, "plan" })
						task := readonlyTestTask(permission)
						task.Launcher = launcher.Name
						if _, err := s.toolTaskPlan(context.Background(), "readonly-chat", readonlyTaskArgs(t, task)); err != nil {
							t.Fatal(err)
						}
						synctest.Wait()
						want := "plan"
						if launcher.Agent == "claude" || launcher.AutoAccept {
							want = "bypassPermissions"
						} else if mode == readonlyPrompt {
							want = "default"
						}
						p := readonlyFeedPlan(t, s, "readonly-chat")
						if p.State != planStarted || !p.Tasks[0].Readonly || p.Tasks[0].Permission != want ||
							len(f.starts) != 1 || f.starts[0]["permission_mode"] != want {
							t.Fatalf("plan=%+v starts=%v, want mode %s", p.Tasks, f.starts, want)
						}
						prompt, _ := f.starts[0]["prompt"].(string)
						if strings.Count(prompt, prompts.RenderReadonlyTaskConstraint("")) != 1 {
							t.Fatalf("missing or repeated read-only instruction: %q", prompt)
						}
					})
				})
			}
		}
	}
}

func TestReadonlyModeKeepsUnsupportedLaunchersRefused(t *testing.T) {
	for _, mode := range []string{readonlySandbox, readonlyPrompt} {
		for _, launcher := range []string{"pi", "cursor", "agy"} {
			t.Run(mode+"/"+launcher, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s, f := readonlyTaskService(t)
					f.launcherList = readonlyEngineLaunchers()
					setSettings(t, s, func(v *Settings) { v.ReadonlyMode = mode })
					task := readonlyTestTask("")
					task.Launcher = launcher
					ctx := context.Background()
					_, err := s.toolTaskPlan(ctx, "readonly-chat", readonlyTaskArgs(t, task))
					var refusal *rpc.Error
					if !errors.As(err, &refusal) || refusal.Code != "readonly_unsupported" {
						t.Fatalf("mode %s, launcher %s: got %v, want readonly_unsupported", mode, launcher, err)
					}
					synctest.Wait()
					if _, plans, err := s.feed(ctx, "readonly-chat", time.Time{}); err != nil || len(plans) != 0 {
						t.Fatalf("refused task was saved: %v, error = %v", plans, err)
					}
					f.mu.Lock()
					defer f.mu.Unlock()
					if len(f.starts) != 0 {
						t.Fatalf("refused task started: %v", f.starts)
					}
				})
			})
		}
	}
}

func TestReadonlyModeStartAndRetryRecheckSetting(t *testing.T) {
	for _, mode := range []string{readonlyPrompt, readonlySandbox} {
		for _, retry := range []bool{false, true} {
			t.Run(mode+"/"+map[bool]string{false: "start", true: "retry"}[retry], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s, f := readonlyTaskService(t)
					f.launcherList = []proto.Launcher{{Name: "codex", Agent: "codex"}}
					ctx := context.Background()
					savedMode, want := "plan", "default"
					if mode == readonlySandbox {
						savedMode, want = "default", "plan"
					}
					task := readonlyTestTask(savedMode)
					task.Launcher = "codex"
					p := &TaskPlan{ID: "plan_readonly_mode", Conversation: "readonly-chat", State: planOpen, Tasks: []PlanTask{task}}
					if retry {
						todo, err := s.createTodo(ctx, task.Title, task.Prompt, p.Conversation, p.ID)
						if err != nil {
							t.Fatal(err)
						}
						if err := s.failTodo(ctx, todo.ID, "Start failed"); err != nil {
							t.Fatal(err)
						}
						p.State, p.Tasks[0].TodoID = planStarted, todo.ID
					}
					if err := s.savePlan(ctx, p); err != nil {
						t.Fatal(err)
					}
					setSettings(t, s, func(v *Settings) { v.ReadonlyMode = mode })
					if retry {
						if _, err := s.retryTask(ctx, p.Tasks[0].TodoID); err != nil {
							t.Fatal(err)
						}
					} else if _, err := s.startPlan(ctx, p.ID, nil, false); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					if len(f.starts) != 1 || f.starts[0]["permission_mode"] != want {
						t.Fatalf("starts=%v, want mode %s", f.starts, want)
					}
					prompt, _ := f.starts[0]["prompt"].(string)
					if strings.Count(prompt, prompts.RenderReadonlyTaskConstraint("")) != 1 {
						t.Fatalf("retry lost the instruction: %q", prompt)
					}
				})
			})
		}
	}
}

func TestReadonlyModeLeavesExplicitPlanAndSpawnPermissionsAlone(t *testing.T) {
	for _, spawned := range []bool{false, true} {
		s, f := readonlyTaskService(t)
		f.launcherList = []proto.Launcher{{Name: "codex", Agent: "codex"}}
		setSettings(t, s, func(v *Settings) { v.ReadonlyMode = readonlyPrompt })
		task := readonlyTestTask("plan")
		task.Readonly, task.preservePermission, task.Launcher = spawned, spawned, "codex"
		if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
			t.Fatal(err)
		}
		if len(f.starts) != 1 || f.starts[0]["permission_mode"] != "plan" {
			t.Fatalf("spawned=%v: ordinary plan permissions changed: %v", spawned, f.starts)
		}
	}
}
