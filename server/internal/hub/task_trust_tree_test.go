package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/proto"
)

// #8095 over the hub stack, with the assistant's state in Postgres: the computer's home is reached through a link
// (its node reports home and home_real), the owner ticks "Start these directly" on a plan in one folder, and the next
// writable tasks in its subfolders and in its other spellings start without a card, while one elsewhere still waits
// for Start. persona_settings adds a folder after the owner approves its card (the apps' resolve), and tasks there start too.
func TestRememberedKindCoversItsFolderTreeOverTheBus(t *testing.T) {
	h, srv, _ := newBusHub(t)
	store := managerDB(t, h)
	ctx := context.Background()
	keys := []string{"assistant", "assistant.tasks", "assistant.runs", "tasks.trust"}
	clear := func() {
		for _, k := range keys {
			_ = store.PutSetting(context.Background(), k, nil)
		}
	}
	clear()
	t.Cleanup(clear)
	old := taskSettleDelay
	taskSettleDelay = func(NotifyConfig) time.Duration { return 0 }
	t.Cleanup(func() { taskSettleDelay = old })
	box := connectTaskNode(t, h, "box")
	m := h.machineByID("box")
	m.mu.Lock()
	m.info.Home, m.info.HomeReal = "/home/me", "/data/home/me"
	m.mu.Unlock()
	dialAssistant(t, h, srv.URL, false)

	do := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	plan := func(cwd string) (startedAtOnce bool, id string) {
		t.Helper()
		task := map[string]any{"title": "改 " + cwd, "prompt": "在 " + cwd + " 里改一处并补测试", "machine": "box", "cwd": cwd,
			"why": "这个目录在 box 上", "role": "worker", "goal": "改好", "expect_minutes": 10, "done_when": []string{"改好并补了测试"}}
		raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{task}})
		res, err := taskPlan(h, "conv1", raw)
		if err != nil {
			t.Fatalf("task_plan %s: %v", cwd, err)
		}
		_, plans := feed(h, "conv1")
		for pid, p := range plans {
			if len(p.Tasks) == 1 && p.Tasks[0].Cwd == strings.Replace(cwd, "~", "/home/me", 1) && p.State != "" && (id == "" || p.CreatedAt.After(plans[id].CreatedAt)) {
				id = pid
			}
		}
		started := strings.Contains(res, "without asking")
		state := ""
		if id != "" {
			state = plans[id].State
		}
		t.Logf("task_plan writable in %-32s → plan %s state=%s (started at once: %v)", cwd, id, state, started)
		return started, id
	}

	// Nothing remembered: a card. The owner starts it with "Start these directly" ticked (the app's Start).
	started, id := plan("/home/me/src/tool")
	if started || id == "" {
		t.Fatalf("nothing remembered, yet the plan started (or has no card): %v %q", started, id)
	}
	if code, body := do("POST", "/api/v1/task-plans/"+id+"/start", `{"remember":true}`); code != http.StatusOK {
		t.Fatalf("start with remember: %d %s", code, body)
	}
	eventually(t, 10*time.Second, "the started task's agent", func() bool { s, _ := box.calls(); return len(s) == 1 })
	var trust []string
	if _, err := store.Setting(ctx, "tasks.trust", &trust); err != nil {
		t.Fatal(err)
	}
	t.Logf("tasks.trust after the owner's Start with remember: %q", trust)

	for _, cwd := range []string{"/home/me/src/tool/cli", "/data/home/me/src/tool", "/data/home/me/src/tool/cli/", "/home/me//src/tool/docs", "~/src/tool/web"} {
		if started, _ := plan(cwd); !started {
			t.Errorf("%s is the remembered folder or under it, yet its task waits for Start", cwd)
		}
	}
	for _, cwd := range []string{"/home/me/src/other", "/home/me/src/toolbox", "/home/me/src"} {
		if started, _ := plan(cwd); started {
			t.Errorf("%s is not under the remembered folder, yet its task started without asking", cwd)
		}
	}

	// persona_settings: the assistant adds a folder the owner named; the card on the app says the computer and the
	// folder with its subfolders; approved, tasks there start.
	call := func(args string) proto.ToolResult {
		return h.asst().Tool(ctx, assistant.ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "box", SessionID: "conv1"})
	}
	got := make(chan proto.ToolResult, 1)
	go func() {
		got <- call(`{"action":"add","key":"tasks.trust","value":{"machine":"box","directory":"/data/home/me/offline/"}}`)
	}()
	var ix proto.Interaction // the card as the hub holds it for the apps
	eventually(t, 10*time.Second, "the setting's card on the hub", func() bool {
		for _, p := range h.pendingInteractions() {
			if p.ToolName == "persona_settings" {
				ix = p
				return true
			}
		}
		return false
	})
	t.Logf("the card the owner approves: %s", ix.Summary)
	if !strings.Contains(ix.Summary, "/home/me/offline") || !strings.Contains(ix.Summary, "box") ||
		!strings.Contains(ix.Summary, "subfolders") && !strings.Contains(ix.Summary, "子目录") { // in the hub's language
		t.Fatalf("the card does not name the computer and the folder with its subfolders: %q", ix.Summary)
	}
	if code, body := do("POST", "/api/v1/interactions/"+ix.ID+"/resolve", `{"allow":true}`); code != http.StatusOK {
		t.Fatalf("resolve: %d %s", code, body)
	}
	select {
	case r := <-got:
		t.Logf("persona_settings add: %s", textOf(r))
		if r.IsError || !strings.Contains(textOf(r), "Remembered") {
			t.Fatalf("add: %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the approval did not reach the assistant")
	}
	if started, _ := plan("/home/me/offline/tool-a"); !started {
		t.Error("a task under the folder persona_settings added waits for Start")
	}
	if started, _ := plan("/home/me/online/tool-b"); started {
		t.Error("a task outside every remembered folder started without asking")
	}
	t.Logf("persona_settings get:\n%s", textOf(call(`{"action":"get","key":"tasks.trust"}`)))
	if r := call(`{"action":"add","key":"tasks.trust","value":{"machine":"box","directory":"~"}}`); !r.IsError {
		t.Fatalf("the whole home was accepted: %s", textOf(r))
	} else {
		t.Logf("persona_settings add ~ (refused): %s", textOf(r))
	}
}
