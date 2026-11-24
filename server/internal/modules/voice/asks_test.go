package voice

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/proto"
)

// Cards by voice (docs/assistant/voice.md 4.7): a question is asked a question at a time by its options' names, what the user
// says while it is asked is its answer, and the card is resolved; other cards are only said by their kind.

// dbQuestion is a question card in conversation sid: which database, Postgres or SQLite (each with what it means).
func dbQuestion(id, sid string) proto.Interaction {
	return proto.Interaction{ID: id, MachineID: "m", SessionID: sid, Kind: proto.IxQuestion, ToolName: "AskUserQuestion",
		Questions: []proto.Question{{Question: "数据库用哪个？", Header: "数据库", Options: []proto.QuestionOption{
			{Label: "Postgres", Description: "生产环境常用"}, {Label: "SQLite", Description: "单个文件，适合本地"}}}}}
}

// askFrom matches the app's ask frame for card in state (by: who answered, "" any).
func askFrom(card, state, by string) func(map[string]any) bool {
	return func(f map[string]any) bool {
		return f["type"] == "ask" && f["interaction_id"] == card && f["state"] == state && (by == "" || f["by"] == by)
	}
}

// saying matches what the extension says by itself with text in it.
func saying(text string) (string, func(map[string]any) bool) {
	return "said: " + text, func(f map[string]any) bool { return said(f) && strings.Contains(content(f), text) }
}

// A question that comes up in the bound conversation is asked by voice: the options by their names (not what they mean
// read out, not the typing hints of a text channel), the app told which card; "the second one" picks SQLite, the card is
// resolved through the hub, nothing is sent to the conversation, and the model is told on that delegation.
func TestAskAQuestionByVoice(t *testing.T) {
	hub := agentHub(t)
	hub.sessions["a1"].status = "working"
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	hub.subscribed("a1") // the call follows its conversation, run or not
	hub.raise(dbQuestion("q1", "a1"))
	asked := content(up.nextMatch(saying("数据库用哪个")))
	for _, want := range []string{"Claude Code 停下来在等用户回答一个问题", "还没做完", "问题（数据库）：数据库用哪个？", "选项 1：Postgres（意思：生产环境常用）",
		"选项 2：SQLite", "只念选项的名字", "「回答：」开头", "「停止：」"} {
		if !strings.Contains(asked, want) {
			t.Errorf("asked without %q: %s", want, asked)
		}
	}
	if strings.Contains(asked, "回复序号") {
		t.Errorf("asked as a text channel does: %s", asked)
	}
	a.until(askFrom("q1", askAsking, ""))

	a.send(map[string]any{"type": "text", "text": "第二个"})
	// The card was not asked of a turn the call handed over, but the call follows its bound conversation (observed
	// turns): what it does next is told by itself.
	if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "用户的回答已经交给 Claude Code 了") || !strings.Contains(got, "还没做完") ||
		strings.Contains(got, "这次通话没有跟着它接下来做的事") {
		t.Fatalf("told: %s", got)
	}
	a.until(askFrom("q1", askAnswered, byVoice))
	res := hub.resolutions()
	if len(res) != 1 || res[0].ID != "q1" || res[0].Res.Answers["数据库用哪个？"] != "SQLite" {
		t.Fatalf("resolved %+v", res)
	}
	if msgs := hub.messages(); len(msgs) != 0 {
		t.Fatalf("sent %+v", msgs)
	}
}

// A card of several questions is asked one at a time: an answer not among the options is the answer itself (the "answer:" prefix
// taken off), the next question is told on that answer's delegation, a multi-select takes several names, and the card
// is resolved with every answer once the last is in.
func TestAskQuestionsOneAtATime(t *testing.T) {
	hub := agentHub(t)
	hub.cards = []proto.Interaction{{ID: "q2", MachineID: "m", SessionID: "a1", Kind: proto.IxQuestion, Questions: []proto.Question{
		{Question: "数据库用哪个？", Options: []proto.QuestionOption{{Label: "Postgres"}, {Label: "SQLite"}}},
		{Question: "要哪些功能？", MultiSelect: true, Options: []proto.QuestionOption{{Label: "登录"}, {Label: "搜索"}, {Label: "导出"}}},
	}}}
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	if got := content(up.nextMatch(saying("数据库用哪个"))); !strings.Contains(got, "第 1 个问题（一共 2 个）") {
		t.Fatalf("asked: %s", got)
	}
	a.until(askFrom("q2", askAsking, ""))
	a.send(map[string]any{"type": "text", "text": "回答：用 MySQL 吧"})
	next := content(up.nextMatch(on("dlg1")))
	for _, want := range []string{"第 2 个问题（一共 2 个）", "要哪些功能？", "选项 3：导出", "告诉用户可以选几个"} {
		if !strings.Contains(next, want) {
			t.Errorf("next question without %q: %s", want, next)
		}
	}
	if len(hub.resolutions()) != 0 {
		t.Fatal("resolved before its last question")
	}
	a.send(map[string]any{"type": "text", "text": "登录和导出"})
	if got := content(up.nextMatch(on("dlg2"))); !strings.Contains(got, "用户的回答已经交给") {
		t.Fatalf("told: %s", got)
	}
	res := hub.resolutions()
	if len(res) != 1 || res[0].Res.Answers["数据库用哪个？"] != "用 MySQL 吧" || res[0].Res.Answers["要哪些功能？"] != "登录, 导出" {
		t.Fatalf("resolved %+v", res)
	}
}

// The card asked is answered on the screen: the app is told it was released, answered elsewhere, and the model
// confirms it in a sentence. A card not asked yet that goes the same way goes without a word.
func TestAskAnsweredOnTheScreen(t *testing.T) {
	hub := agentHub(t)
	q2 := dbQuestion("q2", "a1")
	q2.CreatedAt = time.Now().Add(time.Second)
	hub.cards = []proto.Interaction{dbQuestion("q1", "a1"), q2}
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	up.nextMatch(saying("数据库用哪个"))
	a.until(askFrom("q1", askAsking, ""))
	hub.subscribed("a1")
	hub.answerOnScreen("q2") // queued, never asked
	time.Sleep(300 * time.Millisecond)
	hub.answerOnScreen("q1")
	a.until(askFrom("q1", askReleased, byElsewhere))
	if got := content(up.nextMatch(saying("已经在屏幕上或者别的地方答了"))); !strings.Contains(got, "不要再问这个问题") {
		t.Fatalf("said: %s", got)
	}
	time.Sleep(300 * time.Millisecond)
	if n := up.count(func(f map[string]any) bool { return said(f) && strings.Contains(content(f), "别的地方答了") }); n != 1 {
		t.Fatalf("said %d times that it was answered", n)
	}
	if n := up.count(func(f map[string]any) bool { return said(f) && strings.Contains(content(f), "数据库用哪个") }); n != 1 {
		t.Fatalf("asked %d times", n)
	}
	if len(hub.resolutions()) != 0 {
		t.Fatal("resolved")
	}
}

// A resolve the hub refuses because the card is over (410, 404, a 200 that says already, a 409 prompt_changed: Codex
// no longer asks it), or a node's refusal of a card that is gone by then, is an answer given elsewhere first.
func TestAskResolveRefused(t *testing.T) {
	for _, c := range []struct {
		name, body string
		status     int
		gone       bool // the card is gone when the hub answers
	}{
		{"gone", `{"error":"no longer pending"}`, http.StatusGone, true},
		{"not found", `{"error":"unknown interaction"}`, http.StatusNotFound, true},
		{"prompt changed", `{"error":"Codex 已经不在问这个了","code":"prompt_changed"}`, http.StatusConflict, false},
		{"refused and gone", `{"error":"电脑上的 Codex 没回到输入框","code":"dialog_open"}`, http.StatusConflict, true},
		{"already", `{"ok":true,"already":true,"status":"resolved"}`, http.StatusOK, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			hub := agentHub(t)
			hub.cards = []proto.Interaction{dbQuestion("q1", "a1")}
			hub.onResolve = func(id string) (int, string) {
				if c.gone {
					hub.answerOnScreen(id)
				}
				return c.status, c.body
			}
			up := boundUpstream(t, false)
			pl := testPlugin(t, liveProvider(up), hub.server(t))
			a := dialApp(t, pl, boundStart("a1", nil))
			a.until(ofType("ready"))
			up.nextMatch(saying("数据库用哪个"))
			a.send(map[string]any{"type": "text", "text": "Postgres"})
			if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "已经在屏幕上或者别的地方答过了") {
				t.Fatalf("told: %s", got)
			}
			a.until(askFrom("q1", askReleased, byElsewhere))
		})
	}
}

// A resolve that does not get through keeps the question asked, and the user's next answer goes through: the hub's
// own reason (503), a node's refusal of a card still open (409 input_draft, dialog_open: said in fixed words, never the
// hub's, which hold the box's draft), or no answer at all while the card still waits. With no answer and the card
// gone, it is not known who answered: the question is let go without a word of who.
func TestAskResolveFails(t *testing.T) {
	for _, c := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"hub", `{"error":"助手暂时连不上","code":"assistant_offline"}`, http.StatusServiceUnavailable, "没能交给 Claude Code：助手暂时连不上"},
		{"input draft", `{"error":"电脑上 Codex 的输入框里有没发出去的内容：「部署到 prod-secret」。","code":"input_draft","draft":"部署到 prod-secret"}`,
			http.StatusConflict, "它在电脑上的输入框里有没发出去的字"},
		{"dialog open", `{"error":"电脑上的 Codex 正开着一道选择题：先答完它","code":"dialog_open"}`, http.StatusConflict, "电脑上先开着另一个对话框"},
		{"no answer", "", -1, "没能交给 Claude Code。"},
	} {
		t.Run(c.name, func(t *testing.T) {
			hub := agentHub(t)
			hub.cards = []proto.Interaction{dbQuestion("q1", "a1")}
			var fail atomic.Bool
			fail.Store(true)
			hub.onResolve = func(string) (int, string) {
				if fail.Load() {
					return c.status, c.body
				}
				return 0, ""
			}
			up := boundUpstream(t, false)
			pl := testPlugin(t, liveProvider(up), hub.server(t))
			a := dialApp(t, pl, boundStart("a1", nil))
			a.until(ofType("ready"))
			up.nextMatch(saying("数据库用哪个"))
			a.send(map[string]any{"type": "text", "text": "Postgres"})
			got := content(up.nextMatch(on("dlg1")))
			if !strings.Contains(got, c.want) || !strings.Contains(got, "还没做完") {
				t.Fatalf("told: %s", got)
			}
			for _, not := range []string{"prod-secret", "选择题", "别的地方"} {
				if strings.Contains(got, not) {
					t.Fatalf("told %q: %s", not, got)
				}
			}
			fail.Store(false)
			a.send(map[string]any{"type": "text", "text": "第一个"})
			if got := content(up.nextMatch(on("dlg2"))); !strings.Contains(got, "用户的回答已经交给") {
				t.Fatalf("told: %s", got)
			}
			if res := hub.resolutions(); len(res) != 1 || res[0].Res.Answers["数据库用哪个？"] != "Postgres" {
				t.Fatalf("resolved %+v", res)
			}
		})
	}

	t.Run("no answer, gone", func(t *testing.T) {
		hub := agentHub(t)
		hub.cards = []proto.Interaction{dbQuestion("q1", "a1")}
		hub.onResolve = func(id string) (int, string) {
			hub.answerOnScreen(id) // it got there, or someone else answered: nobody can tell
			return -1, ""
		}
		up := boundUpstream(t, false)
		pl := testPlugin(t, liveProvider(up), hub.server(t))
		a := dialApp(t, pl, boundStart("a1", nil))
		a.until(ofType("ready"))
		up.nextMatch(saying("数据库用哪个"))
		a.send(map[string]any{"type": "text", "text": "Postgres"})
		if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "不确定用户的回答有没有交到") || !strings.Contains(got, "屏幕上") {
			t.Fatalf("told: %s", got)
		}
		if f := a.until(askFrom("q1", askReleased, "")); f["by"] != nil {
			t.Fatalf("released: %v", f)
		}
	})
}

// A resolve takes as long as the hub takes (a node types a Codex card's answer, up to a minute): meanwhile the hub has
// taken the card out of its list and said so, which is not an answer given elsewhere; how the resolve went is.
func TestAskResolveTakesItsTime(t *testing.T) {
	setVar(t, &lookEvery, 100*time.Millisecond)
	hub := agentHub(t)
	hub.cards = []proto.Interaction{dbQuestion("q1", "a1")}
	hub.onResolve = func(id string) (int, string) {
		hub.answerOnScreen(id) // taken: out of the list, interaction_resolved to everyone
		time.Sleep(time.Second)
		return http.StatusOK, `{"ok":true}`
	}
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	up.nextMatch(saying("数据库用哪个"))
	a.send(map[string]any{"type": "text", "text": "SQLite"})
	if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "用户的回答已经交给 Claude Code 了") {
		t.Fatalf("told: %s", got)
	}
	a.until(askFrom("q1", askAnswered, byVoice))
	if n := up.count(func(f map[string]any) bool { return said(f) && strings.Contains(content(f), "别的地方") }); n != 0 {
		t.Fatal("said it was answered elsewhere")
	}
}

// "stop:" while a question is asked closes it (Esc): the card goes before the hub answers the interrupt, and the
// tracker may see that first. It ends quietly, as the stop's, not as an answer given elsewhere.
func TestAskEndsQuietlyOnAStop(t *testing.T) {
	setVar(t, &lookEvery, 100*time.Millisecond)
	hub := agentHub(t)
	hub.sessions["a1"].status, hub.sessions["a1"].pending = "blocked", 1
	hub.cards = []proto.Interaction{dbQuestion("q1", "a1")}
	hub.onInterrupt = func(sid string) {
		hub.answerOnScreen("q1") // the Esc withdrew it, and the hub said so
		time.Sleep(500 * time.Millisecond)
	}
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	up.nextMatch(saying("数据库用哪个"))
	a.until(askFrom("q1", askAsking, ""))
	a.send(map[string]any{"type": "text", "text": "停止：先别做了"})
	if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "已经叫停") {
		t.Fatalf("told: %s", got)
	}
	if f := a.until(askFrom("q1", askReleased, "")); f["by"] != nil {
		t.Fatalf("released: %v", f)
	}
	time.Sleep(300 * time.Millisecond)
	if n := up.count(func(f map[string]any) bool { return strings.Contains(content(f), "别的地方") }); n != 0 {
		t.Fatal("told it was answered elsewhere")
	}
	if len(hub.resolutions()) != 0 {
		t.Fatal("resolved")
	}
}

// A bare "answer:" while the question is asked (the model did not copy the words) is the user's caption: "the second one".
func TestAskBareAnswerMarkTakesTheCaption(t *testing.T) {
	hub := agentHub(t)
	hub.cards = []proto.Interaction{dbQuestion("q1", "a1")}
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		switch f["type"] {
		case "session.start":
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		case "session.commentary.append":
			if f["delegation_id"] == nil && f["event_id"] == nil && content(f) == "第二个" {
				delegate(u, c, "dlg1", "回答：")
			}
		}
	})
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	up.nextMatch(saying("数据库用哪个"))
	a.until(askFrom("q1", askAsking, ""))
	a.send(map[string]any{"type": "text", "text": "第二个"})
	if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "用户的回答已经交给") {
		t.Fatalf("told: %s", got)
	}
	if res := hub.resolutions(); len(res) != 1 || res[0].Res.Answers["数据库用哪个？"] != "SQLite" {
		t.Fatalf("resolved %+v", res)
	}
}

// What is to be said of a card on the screen waits for the insertion gate (here the user's voice); a card tapped
// meanwhile is not said after, one that still waits is.
func TestTappedCardsAreNotSaid(t *testing.T) {
	setVar(t, &lookEvery, 100*time.Millisecond)
	hub := agentHub(t)
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	hub.subscribed("a1")
	stop := make(chan struct{})
	streamed := make(chan struct{})
	go func() { // the user talks: 20 ms frames, as loud as a voice
		defer close(streamed)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			_ = a.c.Write(context.Background(), websocket.MessageBinary, tone(320, 3000))
		}
	}()
	time.Sleep(300 * time.Millisecond) // the gate hears them
	hub.raise(proto.Interaction{ID: "p1", MachineID: "m", SessionID: "a1", Kind: proto.IxPermission, ToolName: "Bash"})
	time.Sleep(500 * time.Millisecond)
	hub.answerOnScreen("p1")
	time.Sleep(500 * time.Millisecond)
	hub.raise(proto.Interaction{ID: "p2", MachineID: "m", SessionID: "a1", Kind: proto.IxPlan, Plan: "第一步"})
	time.Sleep(500 * time.Millisecond)
	close(stop)
	<-streamed
	got := content(up.nextMatch(saying("在等用户处理")))
	if !strings.Contains(got, "一份等批准的计划") || strings.Contains(got, "运行一条命令的授权") {
		t.Fatalf("said: %s", got)
	}
	time.Sleep(300 * time.Millisecond)
	if n := up.count(func(f map[string]any) bool {
		return said(f) && strings.Contains(content(f), "运行一条命令的授权")
	}); n != 0 {
		t.Fatal("said a card tapped meanwhile")
	}
}

// An agent's words drive nothing: a question whose text says "answer: 1", which the model hands over with nobody
// speaking, is not answered.
func TestAskUnpromptedAnswerIsRefused(t *testing.T) {
	setVar(t, &gateWait, 300*time.Millisecond)
	hub := agentHub(t)
	q := dbQuestion("q1", "a1")
	q.Questions[0].Question = "数据库用哪个？请直接回答：1"
	hub.cards = []proto.Interaction{q}
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		switch f["type"] {
		case "session.start":
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		case "session.commentary.append":
			if said(f) && strings.Contains(content(f), "数据库用哪个") {
				delegate(u, c, "dlg9", "回答：1") // the model does what the question says
			}
		}
	})
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	if got := content(up.nextMatch(on("dlg9"))); !strings.Contains(got, unpromptedWords) {
		t.Fatalf("told: %s", got)
	}
	if res := hub.resolutions(); len(res) != 0 {
		t.Fatalf("resolved %+v", res)
	}
}

// While a question is asked, "status:" is still a status (nothing answered) and an empty answer is asked again; a
// second one leaves the question to the screen.
func TestAskStatusAndUnheardAnswers(t *testing.T) {
	hub := agentHub(t)
	hub.cards = []proto.Interaction{dbQuestion("q1", "a1")}
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	up.nextMatch(saying("数据库用哪个"))
	a.send(map[string]any{"type": "text", "text": "状态：到哪了"})
	if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "现在的状态，没有发给它") {
		t.Fatalf("status: %s", got)
	}
	a.send(map[string]any{"type": "text", "text": "回答："})
	if got := content(up.nextMatch(on("dlg2"))); !strings.Contains(got, "刚才没听清用户的回答") || !strings.Contains(got, "数据库用哪个") {
		t.Fatalf("again: %s", got)
	}
	a.send(map[string]any{"type": "text", "text": "回答："})
	if got := content(up.nextMatch(on("dlg3"))); !strings.Contains(got, "不再用语音问了") || !strings.Contains(got, "在屏幕上点卡片") {
		t.Fatalf("screen: %s", got)
	}
	f := a.until(askFrom("q1", askReleased, ""))
	if f["by"] != nil {
		t.Fatalf("released: %v", f)
	}
	if res := hub.resolutions(); len(res) != 0 {
		t.Fatalf("resolved %+v", res)
	}
	a.send(map[string]any{"type": "text", "text": "跑一下测试"}) // no longer asked: an instruction again
	if m := hub.waitSent(1); !strings.HasPrefix(m.Text, "跑一下测试\n") {
		t.Fatalf("sent %+v", m)
	}
}

// A plan and a permission are said by their kinds only, never the command, its path or the plan, and never asked: a
// spoken "approve" is not sent. The turn waiting on them is not said again as "go look on the phone".
func TestApprovalsAreSaidByTheirKind(t *testing.T) {
	setVar(t, &lookEvery, 100*time.Millisecond)
	hub := agentHub(t)
	hub.manual = true
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.send(map[string]any{"type": "text", "text": "清理一下临时目录"})
	m := hub.waitSent(1)
	hub.status("a1", "working")
	a.runStates(m.ID, runWorking)
	input, _ := json.Marshal(map[string]string{"command": "rm -rf /tmp/build-cache"})
	hub.raise(proto.Interaction{ID: "p1", MachineID: "m", SessionID: "a1", Kind: proto.IxPermission, ToolName: "Bash",
		ToolInput: input, Summary: "rm -rf /tmp/build-cache"})
	hub.raise(proto.Interaction{ID: "p2", MachineID: "m", SessionID: "a1", Kind: proto.IxPlan, Plan: "第一步：删掉 build-cache"})
	hub.edit("a1", func(s *fakeSession) { s.status, s.pending = "blocked", 2 })
	a.runStates(m.ID, runNeedsYou)
	var told strings.Builder
	told.WriteString(content(up.nextMatch(saying("在等用户处理"))))
	if !strings.Contains(told.String(), "一份等批准的计划") { // the two may come in one sentence or two
		told.WriteString(content(up.nextMatch(saying("一份等批准的计划"))))
	}
	got := told.String()
	for _, want := range []string{"运行一条命令的授权", "一份等批准的计划", "屏幕上", "还没做完"} {
		if !strings.Contains(got, want) {
			t.Errorf("said without %q: %s", want, got)
		}
	}
	for _, not := range []string{"rm -rf", "build-cache", "第一步"} {
		if strings.Contains(got, not) {
			t.Errorf("said %q: %s", not, got)
		}
	}
	a.send(map[string]any{"type": "text", "text": "批准"})
	if f := a.until(runIn(runRefused)); f["code"] != "on_screen" {
		t.Fatalf("run: %v", f)
	}
	time.Sleep(300 * time.Millisecond)
	if n := up.count(func(f map[string]any) bool {
		return said(f) && strings.Contains(content(f), "等用户回答或批准")
	}); n != 0 {
		t.Fatal("the turn said it waits too")
	}
	a.mu.Lock()
	ask := a.types["ask"]
	a.mu.Unlock()
	if ask || len(hub.resolutions()) != 0 || len(hub.messages()) != 1 {
		t.Fatalf("asked %v, resolved %+v, sent %+v", ask, hub.resolutions(), hub.messages())
	}
}

// Protocol 1 (Android, the main conversation): a full device answers a question the main conversation asks while a
// run waits on it, with no ask frame; another extension's call is told to look on the phone, and answers nothing.
func TestProtocolOneAsks(t *testing.T) {
	for _, full := range []bool{true, false} {
		name := "full"
		var headers []http.Header
		if !full {
			name, headers = "extension", []http.Header{{"X-Persona-Role": {"extension"}}}
		}
		t.Run(name, func(t *testing.T) {
			setVar(t, &lookEvery, 100*time.Millisecond)
			hub := newFakeHub(t)
			hub.manual = true
			up := delegatingUpstream(t)
			pl := testPlugin(t, liveProvider(up), hub.server(t))
			a := dialApp(t, pl, map[string]any{"type": "start"}, headers...)
			a.until(ofType("ready"))
			a.send(map[string]any{"type": "text", "text": "帮我建个项目"})
			hub.waitSent(1)
			hub.status("main1", "working")
			q := dbQuestion("u1", "main1")
			q.ToolName = "ask_user"
			hub.raise(q)
			hub.edit("main1", func(s *fakeSession) { s.status, s.pending = "idle", 1 })
			if !full {
				up.nextMatch("go look on the phone", func(f map[string]any) bool {
					return f["type"] == "session.commentary.append" && f["delegation_id"] == nil && strings.Contains(content(f), "等用户回答或批准")
				})
				time.Sleep(300 * time.Millisecond)
				if n := up.count(func(f map[string]any) bool { return strings.Contains(content(f), "选项 1") }); n != 0 {
					t.Fatal("asked by voice")
				}
				a.protocolOne()
				return
			}
			asked := up.nextMatch("the question", func(f map[string]any) bool {
				return f["type"] == "session.commentary.append" && f["delegation_id"] == nil && strings.Contains(content(f), "数据库用哪个")
			})
			if got := content(asked); !strings.Contains(got, "Persona 主对话停下来在等用户回答") || !strings.Contains(got, "委派一句「回答：」") ||
				strings.Contains(got, "「状态：」") {
				t.Fatalf("asked: %s", got)
			}
			a.send(map[string]any{"type": "text", "text": "SQLite"})
			if got := content(up.nextMatch(on("dlg2"))); !strings.Contains(got, "用户的回答已经交给 Persona 主对话了") ||
				strings.Contains(got, "没有跟着") { // the run follows its turn on
				t.Fatalf("told: %s", got)
			}
			if res := hub.resolutions(); len(res) != 1 || res[0].Res.Answers["数据库用哪个？"] != "SQLite" {
				t.Fatalf("resolved %+v", res)
			}
			if n := up.count(func(f map[string]any) bool { return strings.Contains(content(f), "等用户回答或批准") }); n != 0 {
				t.Fatal("the turn said it waits too")
			}
			hub.edit("main1", func(s *fakeSession) { s.status, s.pending = "working", 0 })
			hub.add("main1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "项目建好了，用的 SQLite。"})
			hub.end("main1")
			if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "项目建好了") {
				t.Fatalf("result: %s", got)
			}
			if msgs := hub.messages(); len(msgs) != 1 {
				t.Fatalf("sent %+v", msgs)
			}
			a.protocolOne()
		})
	}
}

// A new upstream session after a drop is asked the question again: the last one heard it, this one did not.
func TestAskAgainAfterAReconnect(t *testing.T) {
	hub := agentHub(t)
	hub.cards = []proto.Interaction{dbQuestion("q1", "a1")}
	var sessions atomic.Int32
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		switch f["type"] {
		case "session.start":
			sessions.Add(1)
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		case "session.commentary.append":
			if said(f) && strings.Contains(content(f), "数据库用哪个") && sessions.Load() == 1 {
				_ = c.Close(websocket.StatusGoingAway, "restart")
			}
		}
	})
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	up.nextMatch(saying("数据库用哪个"))
	a.until(func(f map[string]any) bool { return f["type"] == "link" && f["state"] == "live" })
	up.nextMatch(saying("数据库用哪个"))
	if n := sessions.Load(); n != 2 {
		t.Fatalf("%d sessions", n)
	}
}

// The tracker runs from the app's socket on, its 1 s tick and the hub link's first event before the app's start is
// read: it does not look at the cards (nor read what the start sets) until the call is set up. Run with -race.
func TestAsksWaitForTheStart(t *testing.T) {
	hub := agentHub(t)
	hub.cards = []proto.Interaction{dbQuestion("q1", "a1")}
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, nil)
	time.Sleep(1300 * time.Millisecond) // the tick fires, the hub link says it connected
	a.send(boundStart("a1", nil))
	a.until(ofType("ready"))
	up.nextMatch(saying("数据库用哪个"))
	a.until(askFrom("q1", askAsking, ""))
	a.send(map[string]any{"type": "text", "text": "第二个"})
	a.until(askFrom("q1", askAnswered, byVoice))
}

// The call follows its bound conversation from start to end; the hub link drops (a redeploy) and connects again: it
// subscribes again from the last line it saw, not from where the call began (that would replay every turn's end since
// as if each had just ended).
func TestBoundCatchesUpFromTheLinesSeen(t *testing.T) {
	setVar(t, &linkBackoff, []time.Duration{50 * time.Millisecond})
	hub := agentHub(t)
	hub.add("a1", proto.Item{Role: "user", Kind: proto.KindText, Text: "修登录页的测试"}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "好"})
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	hub.subscribed("a1")
	if _, sub := hub.subscribes("a1"); sub["since_seq"] != float64(2) {
		t.Fatalf("subscribed with %v (its last line is 2)", sub)
	}
	hub.add("a1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "第一轮"})
	hub.end("a1") // seq 4
	time.Sleep(200 * time.Millisecond)
	hub.dropWS()
	waitFor(t, "a second subscribe", func() bool { n, _ := hub.subscribes("a1"); return n >= 2 })
	if _, sub := hub.subscribes("a1"); sub["since_seq"] != float64(4) {
		t.Fatalf("subscribed again with %v (the link saw up to 4)", sub)
	}
}
