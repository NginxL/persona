package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/proto"
)

// replies is a model that answers each call with the next of its replies (the last one again when they run out).
func replies(t *testing.T, out ...string) (ai.Config, func() []capturedCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []capturedCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			System   string `json:"system"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		if in.System == "" && len(in.Messages) == 0 { // not a question (nothing asked): not counted
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": out[0]}}})
			return
		}
		c := capturedCall{System: in.System}
		if len(in.Messages) > 0 {
			c.User = in.Messages[0].Content
		}
		calls = append(calls, c)
		reply := out[min(len(calls), len(out))-1]
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": reply}}})
	}))
	t.Cleanup(srv.Close)
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("k"), 0o600)
	return ai.Config{BaseURL: srv.URL, APIKeyFile: key, Model: "m"}, func() []capturedCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedCall(nil), calls...)
	}
}

// A conversation the hub started is named once, after its first turn; the user's own name wins and stays; a
// conversation from before (not noted) is left alone; the list the apps get shows the title.
func TestAConversationIsNamedOnceAfterItsFirstTurn(t *testing.T) {
	h := newTestHub(t)
	putAssistantSettings(t, h, assistant.Settings{Timezone: "Asia/Shanghai"})
	n := newAssistantNode(t, h)
	n.m.info.Home = "/home/me"
	n.items = []proto.Item{{Role: "user", Kind: proto.KindText, Text: "帮我查一下竞品的定价"},
		{Role: "assistant", Kind: proto.KindText, Text: "查到三家……"}}
	model, calls := replies(t, "研究 | 竞品定价")
	useModel(t, h, model)
	connectAssistant(t, h)
	started := time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
	noteStarted(t, h, "server", "new", started)

	turnEnded(t, h, "server", "old") // from before: not noted, not named
	if len(calls()) != 0 {
		t.Fatal("an old conversation is not named")
	}
	turnEnded(t, h, "server", "new")
	if got := shown(h, "new").Title; got != "1101 | 研究 | 竞品定价" {
		t.Fatalf("%q", got)
	}
	turnEnded(t, h, "server", "new") // the second turn
	if len(calls()) != 1 {
		t.Fatal("named once")
	}
	n.m.setSessions([]proto.Session{{SessionID: "new", Cwd: "/home/me/.persona/assistant", Title: "帮我查一下竞品的定价"},
		{SessionID: "x", Cwd: "/code/app", Title: "帮我查一下竞品的定价"}})
	list := h.decorated(n.m)
	if list[0].Title != "1101 | 研究 | 竞品定价" || list[1].Title != "帮我查一下竞品的定价" {
		t.Fatalf("only the assistant conversation shows it: %+v", list)
	}

	// The user renames it in the app: theirs from now on.
	srv := httptest.NewServer(func() http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /r/{sid}", func(w http.ResponseWriter, r *http.Request) { h.renameSession(w, r, n.m) })
		return mux
	}())
	defer srv.Close()
	res, err := http.Post(srv.URL+"/r/new", "application/json", strings.NewReader(`{"title":"竞品  定价表"}`))
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("%v %v", res, err)
	}
	if got := shown(h, "new").Title; got != "竞品 定价表" {
		t.Fatalf("the user's title: %q", got)
	}
	if p := n.lastSend(t)["rename"].(map[string]any); p["title"] != "竞品  定价表" {
		t.Fatalf("the node renames a Claude conversation too: %v", p)
	}

	// Renamed before its first turn ended: nothing replaces it.
	noteStarted(t, h, "server", "early", started)
	userRenamed(t, h, "server", "early", "我自己起的")
	turnEnded(t, h, "server", "early")
	if got := shown(h, "early").Title; got != "我自己起的" || len(calls()) != 1 {
		t.Fatalf("%q, %d calls", got, len(calls()))
	}

	// A Codex conversation's title is the assistant's only: the node would write Claude's line into the rollout.
	n.m.setSessions(append(n.m.sessionList(), proto.Session{SessionID: "cx", Cwd: "/home/me/.persona/assistant", Agent: "codex"}))
	sends := n.sent()
	res, _ = http.Post(srv.URL+"/r/cx", "application/json", strings.NewReader(`{"title":"codex 的对话"}`))
	if res.StatusCode != http.StatusOK || n.sent() != sends || shown(h, "cx").Title != "codex 的对话" {
		t.Fatalf("%d, %d calls to the node, %q", res.StatusCode, n.sent()-sends, shown(h, "cx").Title)
	}
}

// An extension's rename (its own token, as the hub's auth found it) reaches the assistant as a hint: the conversation
// is still named after its first turn, with the hint in what the model reads. A client that says it is an extension,
// in a header or in the body, renames as the user (#3758).
func TestAnExtensionsRenameIsOnlyAHint(t *testing.T) {
	h := newTestHub(t)
	putAssistantSettings(t, h, assistant.Settings{Timezone: "Asia/Shanghai"})
	n := newAssistantNode(t, h)
	n.m.info.Home = "/home/me"
	n.items = []proto.Item{{Role: "user", Kind: proto.KindText, Text: "帮我查一下竞品的定价"},
		{Role: "assistant", Kind: proto.KindText, Text: "查到三家……"}}
	model, calls := replies(t, "研究 | 竞品定价")
	useModel(t, h, model)
	connectAssistant(t, h)
	started := time.Now() // a save forgets a conversation still waiting for its title a day on
	day := started.In(h.userLocation()).Format("0102")
	noteStarted(t, h, "server", "ext", started)
	noteStarted(t, h, "server", "app", started)
	n.m.setSessions([]proto.Session{{SessionID: "ext", Cwd: "/home/me/.persona/assistant"}, {SessionID: "app", Cwd: "/home/me/.persona/assistant"}})
	srv := httptest.NewServer(func() http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /ext/{sid}", func(w http.ResponseWriter, r *http.Request) {
			p := principal{ID: "extension:larkext", Role: roleExtension, Extension: "larkext"}
			h.renameSession(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)), n.m)
		})
		mux.HandleFunc("POST /app/{sid}", func(w http.ResponseWriter, r *http.Request) {
			p := principal{ID: "d1", Role: roleFull}
			h.renameSession(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)), n.m)
		})
		return mux
	}())
	defer srv.Close()
	post := func(path, body string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("X-Persona-Extension", "larkext")
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %v %v", path, res, err)
		}
		_ = res.Body.Close()
	}
	post("/ext/ext", `{"title":"/agent · 示例群 · 查竞品定价"}`)
	post("/app/app", `{"title":"我起的","extension":"larkext"}`)

	turnEnded(t, h, "server", "ext")
	if got := shown(h, "ext").Title; got != day+" | 研究 | 竞品定价" {
		t.Fatalf("an extension's conversation is named like any other: %q", got)
	}
	if c := calls(); len(c) != 1 || !strings.Contains(c[0].User, "/agent · 示例群 · 查竞品定价") {
		t.Fatalf("the model reads the extension's title: %+v", c)
	}
	turnEnded(t, h, "server", "app")
	if got := shown(h, "app").Title; got != "我起的" || len(calls()) != 1 {
		t.Fatalf("a client's word makes no extension: %q, %d calls", got, len(calls()))
	}
}

// A scheduled task's conversation is "MMDD | scheduled | task name" (in Chinese) from its first run, with no model call, and keeps it.
func TestATaskConversationIsTitledAfterTheTask(t *testing.T) {
	h := newTestHub(t)
	putAssistantSettings(t, h, assistant.Settings{Timezone: "Asia/Shanghai"})
	n := newAssistantNode(t, h)
	n.m.mu.Lock()
	n.m.online = true // runTask's execution guard sees this in-process peer as an online machine.
	n.m.mu.Unlock()
	model, calls := replies(t, "探索 | 不该用到")
	useModel(t, h, model)
	seedTasks(t, h, assistant.Task{ID: "r", Title: "早报", Prompt: "给我今天的早报", Repeat: "daily", Time: "09:00", Enabled: true})
	connectAssistant(t, h)
	first, err := runNow(t, h, "r")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().In(h.userLocation()).Format("0102")
	if got := shown(h, first.SessionID).Title; got != day+" | 定时 | 早报" {
		t.Fatalf("%q", got)
	}
	turnEnded(t, h, n.m.cfg.ID, first.SessionID)
	if _, err := runNow(t, h, "r"); err != nil {
		t.Fatal(err)
	}
	if got := shown(h, first.SessionID).Title; got != day+" | 定时 | 早报" || len(calls()) != 0 {
		t.Fatalf("the same title every run, no model: %q, %d calls", got, len(calls()))
	}
}

// The main conversation is "Persona" on the lists the apps get, whatever the node, a model or a rename would call it;
// it is not named after its first turn and the app cannot rename it (#1313).
func TestTheMainConversationIsAlwaysPersona(t *testing.T) {
	h, n := conversationsHub(t)
	model, calls := replies(t, "探索 | 不该用到")
	useModel(t, h, model)
	// The node's title for a transcript without one is its first message.
	editSessions(n.m, func(list []proto.Session) []proto.Session {
		list[0].Title = "我的邮箱里最近有哪些需要我处理或回复的邮件？挑重要的说。"
		return list
	})
	// Named after its first turn (by the model): what the assistant keeps, read when it first shows its titles.
	if err := h.store.PutSetting(context.Background(), "assistant.titles", map[string]any{
		"trip-0002": map[string]any{"machine_id": "server", "title": "1030 | 探索 | 周末出游", "by": "auto"}}); err != nil {
		t.Fatal(err)
	}
	restartAssistant(t, h) // it reads them when it starts
	byID := func() map[string]string {
		settleAssistant(h)
		out := map[string]string{}
		for _, s := range h.decorated(n.m) {
			out[s.SessionID] = s.Title
		}
		return out
	}
	if got := byID(); got["main-0001"] != "Persona" || got["trip-0002"] != "1030 | 探索 | 周末出游" || got["agent-0005"] != "不是助手的" {
		t.Fatalf("titles: %v", got)
	}

	// Noted when it started (POST /assistant/conversations with main): not named when its first turn ends.
	noteStarted(t, h, "server", "main-0001", time.Now())
	turnEnded(t, h, "server", "main-0001")
	if len(calls()) != 0 || shown(h, "main-0001").Title != "Persona" {
		t.Fatalf("named the main conversation: %d calls, %q", len(calls()), shown(h, "main-0001").Title)
	}
	if by := titleBy(t, h, "main-0001"); by != "" {
		t.Fatalf("the pending note stays: %q", by)
	}

	// Renaming it in the app is refused; a side conversation's rename still goes through.
	srv := httptest.NewServer(func() http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("POST /r/{sid}", func(w http.ResponseWriter, r *http.Request) { h.renameSession(w, r, n.m) })
		return mux
	}())
	defer srv.Close()
	sends := n.sent()
	res, err := http.Post(srv.URL+"/r/main-0001", "application/json", strings.NewReader(`{"title":"我的主对话"}`))
	if err != nil || res.StatusCode != http.StatusBadRequest || n.sent() != sends {
		t.Fatalf("renaming the main conversation: %v %v, %d calls to the node", res, err, n.sent()-sends)
	}
	if got := byID()["main-0001"]; got != "Persona" {
		t.Fatalf("after a rename: %q", got)
	}
	res, err = http.Post(srv.URL+"/r/trip-0002", "application/json", strings.NewReader(`{"title":"出游"}`))
	if err != nil || res.StatusCode != http.StatusOK || byID()["trip-0002"] != "出游" {
		t.Fatalf("a side conversation: %v %v %q", res, err, byID()["trip-0002"])
	}
}
