package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// serveJSON is a request as the hub relays it, in lang ("": none said), and its answer's body.
func serveJSON(t *testing.T, s *Service, method, path, lang, body string) (int, map[string]any) {
	t.Helper()
	header := map[string][]string{}
	if lang != "" {
		header["Accept-Language"] = []string{lang}
	}
	res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{Method: method, Path: path, Header: header, Body: []byte(body)}, s.lang)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("%s %s: %s", method, path, res.Body)
	}
	return res.Status, out
}

// With no machine the main conversation says why in plain words, in the request's language, with what to do; the
// words it had are the detail (docs/assistant/onboarding.md 5.2). It used to be 502 internal "assistant machine is not
// configured". The memory refuses with the same code.
func TestMainConversationWithoutMachineSaysWhy(t *testing.T) {
	s := newStorelessService(t, newFakeHub())
	for _, tc := range []struct{ lang, want string }{
		{"", "Persona 要在你的一台电脑上干活，现在还没接上电脑。"},
		{"en", "Persona works on one of your computers, and none is connected yet."},
	} {
		status, body := serveJSON(t, s, "POST", "/api/v1/assistant/conversations", tc.lang, `{"prompt":"你好","main":true}`)
		if status != 409 || body["code"] != proto.CodeNoMachine || body["error"] != tc.want ||
			body["detail"] != "assistant machine is not configured" || body["action"] != "add_computer" || body["delivered"] != "none" {
			t.Fatalf("%q: %d %v", tc.lang, status, body)
		}
	}
	if status, body := serveJSON(t, s, "GET", "/api/v1/assistant/memory", "", ""); status != 409 || body["code"] != proto.CodeNoMachine {
		t.Fatalf("memory: %d %v", status, body)
	}
}

// A launcher that cannot start because Claude Code or the launcher is missing on the machine (the node's coded
// refusal) is said with the machine's name; the shell's words stay in the detail.
func TestMainConversationSaysWhatIsMissing(t *testing.T) {
	for _, tc := range []struct {
		code, launcher, want, action string
	}{
		{proto.CodeClaudeMissing, "claude", "server 上还没装 Claude Code。", "install_claude"},
		{proto.CodeLauncherMissing, "cc_gone", "在 server 上找不到「cc_gone」这个模型入口，可能被删了。", "connect_model"},
	} {
		f := newFakeHub().withMachine()
		raw := tc.launcher + " 没有启动起来（0s 后退出），消息没有送达：zsh:1: command not found: " + tc.launcher
		data, _ := json.Marshal(map[string]string{"delivered": "none", "launcher": tc.launcher})
		f.failAssistant = &rpc.Error{Code: tc.code, Message: raw, Data: data}
		s := newStorelessService(t, f)
		status, body := serveJSON(t, s, "POST", "/api/v1/assistant/conversations", "", `{"prompt":"你好","main":true}`)
		if status != 409 || body["code"] != tc.code || body["error"] != tc.want || body["detail"] != raw || body["action"] != tc.action ||
			body["machine_name"] != "server" {
			t.Fatalf("%s: %d %v", tc.code, status, body)
		}
	}
}

func TestMainConversationSaysPrivateProfileNeedsSignIn(t *testing.T) {
	const raw = `the new conversation's dedicated Claude Code profile is not signed in`
	f := newFakeHub().withMachine()
	data, _ := json.Marshal(map[string]any{"delivered": "none", "retryable": false, "launcher": "claude"})
	f.failAssistant = &rpc.Error{Code: proto.TurnErrorNotLoggedIn, Message: raw, Data: data}
	s := newStorelessService(t, f)
	status, body := serveJSON(t, s, "POST", "/api/v1/assistant/conversations", "", `{"prompt":"你好","main":true}`)
	if status != 409 || body["code"] != proto.TurnErrorNotLoggedIn ||
		body["error"] != "这个对话的 Claude Code 配置还没有登录。请登录该配置，或者改用 API Key。" ||
		body["detail"] != raw || body["action"] != "connect_model" || body["machine_name"] != "server" || body["delivered"] != "none" {
		t.Fatalf("%d %v", status, body)
	}
}

// What a turn's model API error says is wrong, for the card that says what to fix; sign-in, the key and the model are
// the user's (turn_error_kind user), a rate limit is not.
func TestTurnErrorCodes(t *testing.T) {
	for text, want := range map[string]string{
		"Not logged in · Please run /login":                                            proto.TurnErrorNotLoggedIn,
		"Invalid API key · Please run /login":                                          proto.TurnErrorAuthFailed,
		"Failed to authenticate. API Error: 401 invalid x-api-key":                     proto.TurnErrorAuthFailed,
		"OAuth token has expired. Please obtain a new token":                           proto.TurnErrorAuthFailed,
		"There's an issue with the selected model (x). It may not exist.":              proto.TurnErrorModelRoute,
		`API Error: 400 {"error":{"message":"no account match for model speech_o50"}}`: proto.TurnErrorModelRoute,
		"API Error: 429 rate limit exceeded":                                           proto.TurnErrorRateLimited,
		"API Error: 529 Overloaded":                                                    "",
		"API Error: Connection error.":                                                 "",
	} {
		if got := turnErrorCode(text); got != want {
			t.Errorf("turnErrorCode(%q) = %q, want %q", text, got, want)
		}
	}
	if !userTurnError(proto.TurnErrorNotLoggedIn) || userTurnError(proto.TurnErrorRateLimited) || userTurnError("") {
		t.Error("userTurnError")
	}
}

// Claude Code without credentials answers "Not logged in · Please run /login" as if it were a reply: the apps get a
// turn error the user must fix, with its code, and the Agents business asks the user rather than sending "continue".
func TestNotLoggedInIsTheUsersTurnError(t *testing.T) {
	const text = "Not logged in · Please run /login"
	if !assistantwire.CutOff(text) || assistantwire.CutOff("修好了 Not logged in · Please run /login 时的崩溃") {
		t.Fatal("CutOff reads the answer by its start")
	}
	ft := newFleetTestNoStore(t)
	ft.d.setSessions("mac", []proto.Session{stopped("s1", text)})
	sess := listed(t, ft.s, "mac", "s1")
	if sess.TurnError != text || sess.TurnErrorKind != proto.TurnErrorUser || sess.TurnErrorCode != proto.TurnErrorNotLoggedIn {
		t.Fatalf("listed: %q %q %q", sess.TurnError, sess.TurnErrorKind, sess.TurnErrorCode)
	}
	w, by, _ := triageRules(sess, time.UTC, "")
	if w == nil || w.On != proto.WaitUser || by != "rule" {
		t.Fatalf("triage: %+v %s", w, by)
	}
	// A gateway's 5xx that mentions the model keeps its code for the card but stays transient: agents_work retries it.
	const gateway = "API Error: 503 model claude-x not available, service unavailable"
	ft.d.setSessions("mac", []proto.Session{stopped("s3", gateway)})
	if sess := listed(t, ft.s, "mac", "s3"); sess.TurnError != gateway || sess.TurnErrorKind == proto.TurnErrorUser {
		t.Fatalf("a gateway 5xx: %q %q %q", sess.TurnError, sess.TurnErrorKind, sess.TurnErrorCode)
	}
	// A reply is a reply: no turn error, no code.
	ft.d.setSessions("mac", []proto.Session{stopped("s2", "改好了")})
	if sess := listed(t, ft.s, "mac", "s2"); sess.TurnError != "" || sess.TurnErrorKind != "" || sess.TurnErrorCode != "" {
		t.Fatalf("a reply: %+v", sess)
	}
}

// A scheduled run whose turn Claude Code answers with "Not logged in" did not run: it is failed and pushed, as a
// model API error is.
func TestTaskTurnEndingNotLoggedInFails(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	h.runRetry = time.Hour // the retry is another test's
	h.turnPoll = 200 * time.Millisecond
	n.items = []proto.Item{{Role: "assistant", Kind: "text", Text: "Not logged in · Please run /login"}}
	setTasks(h, Task{ID: "p", Title: "早报", Prompt: "go", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(-time.Minute), Enabled: true, MachineID: "server", CreatedBy: "assistant"})
	n.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, Assistant: true,
		Cwd: "/home/me/.persona/assistant"})
	if _, err := runNow(t, h, "p"); err != nil {
		t.Fatal(err)
	}
	// The turn ends after the first poll saw it working (started after the run went, so the watcher's polls line up).
	go func() {
		time.Sleep(3 * h.turnPoll / 2)
		n.mu.Lock()
		for i := range n.sessions["server"] {
			n.sessions["server"][i].Status, n.sessions["server"][i].State = proto.StatusOffline, proto.StateIdle
		}
		n.mu.Unlock()
	}()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		sc := h.loadSchedule(context.Background())
		sc.mu.Lock()
		failed := slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool {
			return r.RefID == "p" && r.Status == runFailed && strings.Contains(r.Error, "Not logged in")
		})
		sc.mu.Unlock()
		if failed {
			if p := n.pushedBy(deadline); len(p) == 0 || p[0].Title != "定时任务失败：早报" {
				t.Fatalf("the failure is pushed: %+v", p)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the run that was not logged in was not recorded as failed")
}
