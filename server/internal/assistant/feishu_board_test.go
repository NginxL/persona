package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The standing "waiting for you" board (feishu_board.go, #6052): with the bot's `board` switch on, /tasks sends one card
// and later edits that same card in place. All ids are placeholders; the clock and the timer are fake.

type boardEnv struct {
	t   *testing.T
	s   *Service
	f   *fakeHub
	st  *memStore
	clk *fakeClock
	d   *fleetDevs

	mu        sync.Mutex
	pairedAt  time.Time
	postErr   error         // what the next sends fail with
	postDelay time.Duration // how long a send takes
	onPost    func()        // called while a send is under way
	features  FeishuFeatures
	posts     []map[string]any // feishu.post
	updates   []map[string]any // feishu.update_card
	texts     []map[string]any // feishu.text
	updateErr error            // what the next updates fail with
}

func newBoardEnv(t *testing.T) *boardEnv { return newBoardEnvOn(t, &memStore{}) }

// newBoardEnvOn is a bot and an assistant on the chat state st (a restart brings another one up on the same st).
func newBoardEnvOn(t *testing.T, st *memStore) *boardEnv {
	t.Helper()
	e := &boardEnv{t: t, f: newFakeHub().withMachine(), st: st, clk: newFakeClock(), pairedAt: time.Unix(100, 0),
		features: FeishuFeatures{Main: true, Board: true}}
	e.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: e.pairedAt, PublicURL: testWeb, Features: e.features}, nil
	})
	record := func(list *[]map[string]any, p json.RawMessage) map[string]any {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		e.mu.Lock()
		*list = append(*list, m)
		e.mu.Unlock()
		return m
	}
	e.f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		record(&e.posts, p)
		e.mu.Lock()
		delay, hook, fail := e.postDelay, e.onPost, e.postErr
		e.mu.Unlock()
		if hook != nil {
			hook()
		}
		time.Sleep(delay)
		e.mu.Lock()
		defer e.mu.Unlock()
		if fail != nil {
			return nil, fail
		}
		return map[string]any{"message_id": fmt.Sprintf("om_board_%d", len(e.posts))}, nil
	})
	e.f.answerApp(AppFeishu, FeishuUpdateCard, func(p json.RawMessage) (any, error) {
		record(&e.updates, p)
		e.mu.Lock()
		defer e.mu.Unlock()
		return map[string]any{}, e.updateErr
	})
	e.f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) { record(&e.texts, p); return map[string]any{}, nil })
	e.d = newFleetDevs()
	e.d.add("server", "srv")
	e.d.add("box", "box")
	e.d.setSessions("server", []proto.Session{{SessionID: "asm", Cwd: "/home/u/.persona/assistant"}})
	e.d.setSessions("box", []proto.Session{{SessionID: "ag1", Cwd: "/code", PaneID: "%1", Title: "登录修复"}, {SessionID: "ag2", Cwd: "/code2", PaneID: "%2"}})
	e.s = withSettings(t, e.f, New(Options{Hub: e.f, Store: func() Store { return e.st }, Logf: testLogf(t), Devices: e.d}))
	e.s.fleet.interactions = map[string]*pending{}
	e.s.todos.loaded, e.s.todos.todos, e.s.todos.activeBySession = true, map[string]*Todo{}, map[[2]string]string{}
	e.s.standing.now, e.s.standing.after = e.clk.Now, e.clk.After
	e.s.standing.fleetUp.Store(true) // the fleet is up; the tests of a start before it say otherwise
	return e
}

func (e *boardEnv) setFeatures(f FeishuFeatures) {
	e.mu.Lock()
	e.features = f
	e.mu.Unlock()
	e.s.feishuForgetStatus()
}

func (e *boardEnv) failUpdates(err error) {
	e.mu.Lock()
	e.updateErr = err
	e.mu.Unlock()
}

// tasks is the owner sending /tasks (or an alias) to the bot.
func (e *boardEnv) tasks(cmd string) {
	e.t.Helper()
	e.s.feishuForgetStatus()
	e.mu.Lock()
	f := e.features
	e.mu.Unlock()
	_, _ = e.s.feishuStatus(context.Background()) // what a message from the owner does first: the pairing is kept
	if h, rest := e.s.botCommand(context.Background(), f, "om_cmd", cmd, "", ""); !h || rest != "" {
		e.t.Fatalf("botCommand(%q) = %v, %q", cmd, h, rest)
	}
}

// card is something waiting: a question card of an Agent's, asked age ago.
func (e *boardEnv) card(id, machine, session, question string, age time.Duration) proto.Interaction {
	ix := proto.Interaction{ID: id, MachineID: machine, SessionID: session, Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: question}}, CreatedAt: e.clk.Now().Add(-age)}
	e.add(ix)
	return ix
}

func (e *boardEnv) add(ix proto.Interaction) {
	e.s.fleet.mu.Lock()
	e.s.fleet.interactions[ix.ID] = &pending{ix: ix}
	e.s.fleet.mu.Unlock()
}

func (e *boardEnv) answer(ix proto.Interaction) {
	e.s.fleet.mu.Lock()
	delete(e.s.fleet.interactions, ix.ID)
	e.s.fleet.mu.Unlock()
}

func (e *boardEnv) announce(ix proto.Interaction) {
	e.s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: ix})
}

func (e *boardEnv) end(ix proto.Interaction) {
	e.answer(ix)
	e.s.InteractionEnded(context.Background(), InteractionEnded{Interaction: ix})
}

func (e *boardEnv) counts() (posts, updates int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.posts), len(e.updates)
}

func (e *boardEnv) lastUpdate() map[string]any {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.updates) == 0 {
		e.t.Fatal("the board was not edited")
	}
	return e.updates[len(e.updates)-1]
}

func (e *boardEnv) boardID() string {
	var saved botState
	if _, err := e.st.Setting(context.Background(), botStateKey, &saved); err != nil || saved.Board == nil {
		return ""
	}
	return saved.Board.MessageID
}

func cardOf(m map[string]any) string { c, _ := m["card"].(string); return c }

// bodyOf is the card's body: the Markdown of its elements, without the header and the summary the chat list shows.
func bodyOf(t *testing.T, card string) string {
	t.Helper()
	var v struct {
		Body struct {
			Elements []struct {
				Content string `json:"content"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(card), &v); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, el := range v.Body.Elements {
		b.WriteString(el.Content + "\n")
	}
	return b.String()
}

// index is where want first appears in the card; the test fails when it is not there.
func index(t *testing.T, card, want string) int {
	t.Helper()
	i := strings.Index(card, want)
	if i < 0 {
		t.Fatalf("the board lacks %q:\n%s", want, card)
	}
	return i
}

// With `board` on, /tasks sends one card; a second /tasks (or an alias) edits it and sends no other card, and says so in
// a line that replies to the board.
func TestBoardIsSentOnceThenEditedInPlace(t *testing.T) {
	e := newBoardEnv(t)
	e.card("ix1", "box", "ag1", "要发布吗？", time.Minute)
	e.tasks("/tasks")
	if p, u := e.counts(); p != 1 || u != 0 {
		t.Fatalf("first /tasks: %d posts, %d edits, want 1 and 0", p, u)
	}
	if got := e.boardID(); got != "om_board_1" {
		t.Fatalf("the board is %q in the chat's state", got)
	}
	if c := cardOf(e.posts[0]); !strings.Contains(c, "要发布吗？") || !strings.Contains(c, "等你处理") {
		t.Fatalf("the board:\n%s", c)
	}
	for _, cmd := range []string{"/tasks", "/进展", "/总览"} {
		e.tasks(cmd)
	}
	if p, u := e.counts(); p != 1 || u != 3 {
		t.Fatalf("later /tasks: %d posts, %d edits, want 1 and 3", p, u)
	}
	if got := e.lastUpdate()["message_id"]; got != "om_board_1" {
		t.Fatalf("edited %v, not the board", got)
	}
	if n := len(e.texts); n != 3 || e.texts[2]["reply_to"] != "om_board_1" || !strings.Contains(textOf(e.texts[2]), "已更新") {
		t.Fatalf("pointer lines %v", e.texts)
	}
}

// A new card from an Agent is on the board within a few seconds, and answering it (anywhere) takes it off.
func TestBoardTakesANewCardAndDropsAnAnsweredOne(t *testing.T) {
	e := newBoardEnv(t)
	e.tasks("/tasks")
	ix := e.card("ix1", "box", "ag1", "先发预发还是直接上线？", 0)
	e.announce(ix)
	e.clk.advance(boardSettle - time.Second)
	if _, u := e.counts(); u != 0 {
		t.Fatalf("edited %d times before the settle time", u)
	}
	e.clk.advance(time.Second)
	if c := cardOf(e.lastUpdate()); !strings.Contains(c, "先发预发还是直接上线？") {
		t.Fatalf("the new card is not on the board:\n%s", c)
	}
	e.end(ix)
	e.clk.advance(boardSettle)
	if c := cardOf(e.lastUpdate()); strings.Contains(c, "先发预发还是直接上线？") || !strings.Contains(bodyOf(t, c), "现在没有等你的事") {
		t.Fatalf("the answered card is still on the board:\n%s", c)
	}
	if p, _ := e.counts(); p != 1 {
		t.Fatalf("%d cards sent, want only the board", p)
	}
}

// The rows are sorted: what holds up an Agent and has waited longest first, then the rest, then the to-dos waiting for
// the owner's check, however long they have waited. Each row says how long, and who asked.
func TestBoardOrdersByWaitAndSaysWhoAsked(t *testing.T) {
	e := newBoardEnv(t)
	e.card("ix_short", "box", "ag1", "短的问题", 2*time.Minute)
	e.card("ix_long", "server", "asm", "长的问题", 80*time.Minute)
	e.card("ix_mid", "box", "ag2", "中的问题", 20*time.Minute)
	e.s.accept.loaded = true
	e.s.accept.asks = map[string]*acceptAsk{"todo_v": {MessageID: "om_v", AskedAt: e.clk.Now().Add(-72 * time.Hour), MachineID: "box", SessionID: "ag1"}}
	e.s.todos.todos["todo_v"] = &Todo{ID: "todo_v", Title: "修登录崩溃"}
	e.tasks("/tasks")
	card := cardOf(e.posts[0])
	order := []int{index(t, card, "长的问题"), index(t, card, "中的问题"), index(t, card, "短的问题"), index(t, card, "请验收：修登录崩溃")}
	for i := 1; i < len(order); i++ {
		if order[i] <= order[i-1] {
			t.Fatalf("rows out of order %v:\n%s", order, card)
		}
	}
	for _, want := range []string{"已等 1 小时 20 分", "已等 20 分钟", "已等 2 分钟", "已等 3 天", "来自 Persona", "来自 Agent「登录修复」", "来自某个 Agent",
		"[打开](" + testWeb + "/persona/asm)", "[打开](" + testWeb + "/s/box/ag1)", "[打开](" + testWeb + "/todos/todo_v)"} {
		index(t, card, want)
	}
}

func TestBoardWaitReadsLikeAPerson(t *testing.T) {
	for _, c := range []struct {
		l    i18n.Lang
		d    time.Duration
		want string
	}{
		{i18n.ZhHans, 30 * time.Second, "不到 1 分钟"}, {i18n.ZhHans, 2 * time.Minute, "2 分钟"}, {i18n.ZhHans, 80 * time.Minute, "1 小时 20 分"},
		{i18n.ZhHans, 2 * time.Hour, "2 小时"}, {i18n.ZhHans, 3*24*time.Hour + 5*time.Hour, "3 天"},
		{i18n.En, 2 * time.Minute, "2 min"}, {i18n.En, 80 * time.Minute, "1 h 20 min"}, {i18n.En, 72 * time.Hour, "3 d"},
	} {
		if got := boardWait(c.l, c.d); got != c.want {
			t.Errorf("boardWait(%s, %v) = %q, want %q", c.l, c.d, got, c.want)
		}
	}
}

// A permission's line says only that it needs approval: not the command, not the path (the board sits in a chat that may
// be seen in a screenshot). A terminal dialog's line is generic too.
func TestBoardPermissionLineHasNoCommand(t *testing.T) {
	e := newBoardEnv(t)
	e.add(proto.Interaction{ID: "ix_perm", MachineID: "box", SessionID: "ag1", Kind: proto.IxPermission, ToolName: "Bash",
		Summary: "rm -rf /data/secret-project", ToolInput: json.RawMessage(`{"command":"rm -rf /data/secret-project"}`),
		CreatedAt: e.clk.Now().Add(-time.Minute)})
	e.add(proto.Interaction{ID: "ix_screen", MachineID: "box", SessionID: "ag2", Kind: proto.IxScreen,
		Summary: "Run curl https://example.test/token=abc123 ?", CreatedAt: e.clk.Now().Add(-time.Minute)})
	e.tasks("/tasks")
	card := cardOf(e.posts[0])
	index(t, card, "需要你批准")
	for _, leaked := range []string{"rm -rf", "secret-project", "Bash", "curl", "abc123"} {
		if strings.Contains(card, leaked) {
			t.Fatalf("the board shows %q:\n%s", leaked, card)
		}
	}
	for _, p := range e.posts {
		if f, _ := p["fallback"].(string); strings.Contains(f, "rm -rf") || strings.Contains(f, "abc123") {
			t.Fatalf("the plain-text fallback shows the command: %q", f)
		}
	}
}

// Nothing waiting: the board says so, and is still the board (edited, not removed).
func TestBoardWithNothingWaitingSaysSo(t *testing.T) {
	e := newBoardEnv(t)
	e.tasks("/tasks")
	if c := cardOf(e.posts[0]); !strings.Contains(bodyOf(t, c), "现在没有等你的事") {
		t.Fatalf("the empty board:\n%s", c)
	}
	e.tasks("/tasks")
	if c := cardOf(e.lastUpdate()); !strings.Contains(bodyOf(t, c), "现在没有等你的事") {
		t.Fatalf("the empty board after an edit:\n%s", c)
	}
}

// A board that can no longer be edited (older than Feishu's 14 days, deleted, or Feishu says so) is replaced by a new one,
// and the bot remembers the new one: the next edit goes to it.
func TestBoardIsReplacedWhenItCannotBeEdited(t *testing.T) {
	for _, c := range []struct {
		name  string
		fail  error
		age   time.Duration
		edits int // how many edits of the old board are tried
	}{
		{"edit period over", nil, 14*24*time.Hour + time.Minute, 0},
		{"edit period over by Feishu's word", errors.New("feishu update card: 230031 Message can only be modified within 14 days"), time.Hour, 1},
		{"deleted", errors.New("feishu update card: 230110 message was deleted"), time.Hour, 1},
		{"withdrawn", errors.New("feishu update card: 230011 message was recalled"), time.Hour, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newBoardEnv(t)
			e.tasks("/tasks")
			e.clk.advance(c.age)
			e.failUpdates(c.fail)
			e.tasks("/tasks")
			p, u := e.counts()
			if p != 2 || u != c.edits {
				t.Fatalf("%d posts, %d edits; want 2 posts and %d edits", p, u, c.edits)
			}
			if got := e.boardID(); got != "om_board_2" {
				t.Fatalf("the chat remembers board %q, want the new one", got)
			}
			e.failUpdates(nil)
			ix := e.card("ix1", "box", "ag1", "新问题", 0)
			e.announce(ix)
			e.clk.advance(boardSettle)
			if got := e.lastUpdate()["message_id"]; got != "om_board_2" {
				t.Fatalf("the next edit went to %v, not the new board", got)
			}
			if !strings.Contains(cardOf(e.lastUpdate()), "新问题") {
				t.Fatal("the new board is not kept current")
			}
		})
	}
}

// An edit that fails for a passing reason (the network, a timeout, a rate limit, a server error, a token) is not a reason
// for a second board: the board stays, and /tasks answers with the snapshot as it does with the switch off.
func TestBoardStaysWhenAnEditFailsForAPassingReason(t *testing.T) {
	for _, err := range []error{
		errors.New("feishu update card: 230020 too frequent"),
		errors.New("feishu update card: 99991400 request trigger frequency limit"),
		errors.New("dial tcp: lookup api.example.test: no such host"),
		context.DeadlineExceeded,
		errors.New("feishu update card: 503 Service Unavailable"),
		errors.New("feishu update card: 99991663 tenant access token invalid"),
	} {
		t.Run(err.Error(), func(t *testing.T) {
			e := newBoardEnv(t)
			e.tasks("/tasks")
			e.failUpdates(err)
			e.tasks("/tasks")
			if got := e.boardID(); got != "om_board_1" {
				t.Fatalf("the board became %q", got)
			}
			if p, _ := e.counts(); p != 2 { // the first board and the snapshot, no second board
				t.Fatalf("%d posts", p)
			}
			if c := cardOf(e.posts[1]); !strings.Contains(c, "任务总览") {
				t.Fatalf("the answer was not the snapshot:\n%s", c)
			}
		})
	}
}

// A board that was sent as rich text (Feishu refused the card) has the id of a text message, and every edit of it fails
// the same way for good: it is replaced, not kept for ever. So is a board Feishu refuses for any other answer.
func TestBoardIsReplacedWhenFeishuWillNeverTakeTheEdit(t *testing.T) {
	for _, err := range []error{
		errors.New("feishu update card: 230001 Only interactive messages can be updated"),
		errors.New("feishu update card: 230099 Failed to create card content"),
		errors.New("feishu update card: 230027 Lack of necessary permissions"),
	} {
		t.Run(err.Error(), func(t *testing.T) {
			e := newBoardEnv(t)
			e.tasks("/tasks")
			e.failUpdates(err)
			e.tasks("/tasks")
			if p, u := e.counts(); p != 2 || u != 1 {
				t.Fatalf("%d posts, %d edits, want a second board after one failed edit", p, u)
			}
			if got := e.boardID(); got != "om_board_2" {
				t.Fatalf("the chat remembers %q", got)
			}
		})
	}
}

// With `board` off nothing changes: /tasks sends its snapshot every time, no board is made or edited, and a board from
// when the switch was on is left alone.
func TestBoardOffChangesNothing(t *testing.T) {
	e := newBoardEnv(t)
	e.setFeatures(FeishuFeatures{Main: true})
	e.card("ix1", "box", "ag1", "要发布吗？", time.Minute)
	e.tasks("/tasks")
	e.tasks("/tasks")
	if p, u := e.counts(); p != 2 || u != 0 {
		t.Fatalf("%d posts, %d edits, want two snapshots", p, u)
	}
	for _, p := range e.posts {
		if !strings.Contains(cardOf(p), "任务总览") {
			t.Fatalf("not the snapshot:\n%s", cardOf(p))
		}
	}
	if e.boardID() != "" || len(e.texts) != 0 {
		t.Fatalf("a board was made: %q, %v", e.boardID(), e.texts)
	}
	// A board made while the switch was on is not touched once it is off.
	e.setFeatures(FeishuFeatures{Main: true, Board: true})
	e.tasks("/tasks")
	e.setFeatures(FeishuFeatures{Main: true})
	_, before := e.counts()
	ix := e.card("ix2", "box", "ag2", "另一个问题", 0)
	e.announce(ix)
	e.clk.advance(time.Minute)
	if _, u := e.counts(); u != before {
		t.Fatalf("the board was edited with the switch off (%d -> %d)", before, u)
	}
}

// Changes that come together are one edit: five cards in a row are one change of the board, and a later one is another.
func TestBoardEditsAreDebounced(t *testing.T) {
	e := newBoardEnv(t)
	e.tasks("/tasks")
	var ixs []proto.Interaction
	for i := 0; i < 5; i++ {
		ix := e.card(fmt.Sprintf("ix%d", i), "box", "ag1", fmt.Sprintf("问题%d", i), 0)
		ixs = append(ixs, ix)
		e.announce(ix)
		e.clk.advance(300 * time.Millisecond)
	}
	e.clk.advance(boardSettle)
	if _, u := e.counts(); u != 1 {
		t.Fatalf("%d edits for five changes, want 1", u)
	}
	for i := range ixs {
		index(t, cardOf(e.lastUpdate()), fmt.Sprintf("问题%d", i))
	}
	e.end(ixs[0])
	e.clk.advance(boardSettle)
	if _, u := e.counts(); u != 2 {
		t.Fatalf("%d edits after another change, want 2", u)
	}
	e.clk.advance(time.Hour)
	if _, u := e.counts(); u != 2 {
		t.Fatalf("%d edits with nothing changed, want 2", u)
	}
}

// A chat with no board schedules nothing, whatever happens.
func TestBoardNothingScheduledWithoutABoard(t *testing.T) {
	e := newBoardEnv(t)
	ix := e.card("ix1", "box", "ag1", "问题", 0)
	e.announce(ix)
	e.end(ix)
	e.s.todosChanged(context.Background())
	if n := e.clk.pending(); n != 0 {
		t.Fatalf("%d timers without a board", n)
	}
}

// When the assistant connects to the hub, the board is brought up to date once, but only after the fleet has read the
// cards that are pending: before that the list is empty because it is not read, and a board written then would say that
// nothing is waiting.
func TestBoardIsRefreshedWhenTheFleetIsUp(t *testing.T) {
	e := newBoardEnv(t)
	e.tasks("/tasks")
	e.s.standing.fleetUp.Store(false) // a restart: the fleet is not up yet
	e.clk.advance(boardSettle)
	_, before := e.counts()
	ix := e.card("ix1", "box", "ag1", "趁你不在来的问题", 0)
	e.s.channelsReconnected(context.Background()) // the hello
	e.announce(ix)
	e.s.todosChanged(context.Background())
	e.clk.advance(time.Minute)
	if _, u := e.counts(); u != before {
		t.Fatalf("the board was written before the fleet was up (%d -> %d edits)", before, u)
	}
	e.s.FleetUp(context.Background())
	e.clk.advance(boardSettle)
	if _, u := e.counts(); u != before+1 {
		t.Fatalf("%d edits after the fleet is up, want %d", u, before+1)
	}
	index(t, cardOf(e.lastUpdate()), "趁你不在来的问题")
}

// A to-do enters the board when its "Please verify" card goes out and leaves it when it is passed or closed.
func TestBoardFollowsPleaseVerify(t *testing.T) {
	e := newBoardEnv(t)
	e.setFeatures(FeishuFeatures{Main: true, Board: true, Reports: true})
	e.tasks("/tasks")
	ctx := context.Background()
	e.s.todos.todos["todo_v"] = &Todo{ID: "todo_v", Title: "修登录崩溃"}
	e.s.todos.todos["todo_w"] = &Todo{ID: "todo_w", Title: "改文案"}
	// Both are at the stage already, so passing one moves no stage: the board must follow the pass itself.
	e.s.reports.mu.Lock()
	e.s.reports.loaded, e.s.reports.stages = true, map[string]todoStage{"todo_v": {Stage: "verify"}, "todo_w": {Stage: "verify"}}
	e.s.reports.mu.Unlock()
	e.s.accept.mu.Lock()
	e.s.loadAcceptLocked(ctx)
	e.s.accept.asks["todo_v"] = &acceptAsk{MessageID: "om_v", Messages: []string{"om_v"}, AskedAt: e.clk.Now().Add(-time.Hour)}
	e.s.accept.asks["todo_w"] = &acceptAsk{MessageID: "om_w", Messages: []string{"om_w"}, AskedAt: e.clk.Now().Add(-time.Hour)}
	e.s.accept.mu.Unlock()
	e.s.boardChanged() // what askAcceptance does once the card is out
	e.clk.advance(boardSettle)
	for _, want := range []string{"请验收：修登录崩溃", "请验收：改文案"} {
		index(t, cardOf(e.lastUpdate()), want)
	}
	if !e.s.accepted(ctx, "todo_v", "om_v") { // passed
		t.Fatal("not accepted")
	}
	e.clk.advance(boardSettle)
	if c := cardOf(e.lastUpdate()); strings.Contains(c, "修登录崩溃") || !strings.Contains(c, "改文案") {
		t.Fatalf("after passing one:\n%s", c)
	}
	// Closed by the owner: it can never be passed, so it is not waiting any more.
	now := e.clk.Now()
	e.s.todos.mu.Lock()
	e.s.todos.todos["todo_w"].DoneAt, e.s.todos.todos["todo_w"].DoneBy = &now, ""
	e.s.todos.mu.Unlock()
	e.s.todosChanged(ctx)
	e.clk.advance(boardSettle)
	if c := cardOf(e.lastUpdate()); strings.Contains(c, "改文案") || !strings.Contains(bodyOf(t, c), "现在没有等你的事") {
		t.Fatalf("after closing the other:\n%s", c)
	}
}

// More rows than one card holds: the board lists as many as fit and counts the rest.
func TestBoardCountsWhatDoesNotFit(t *testing.T) {
	e := newBoardEnv(t)
	for i := 0; i < boardRowsMax+5; i++ {
		e.card(fmt.Sprintf("ix%02d", i), "box", "ag1", fmt.Sprintf("问题%02d", i), time.Duration(i+1)*time.Minute)
	}
	e.tasks("/tasks")
	card := cardOf(e.posts[0])
	if len(card) > reportCardMaxBytes {
		t.Fatalf("the board is %d bytes", len(card))
	}
	index(t, card, "还有 5 项")
	index(t, card, fmt.Sprintf("问题%02d", boardRowsMax+4)) // the longest waiting is kept
	if strings.Contains(card, "问题00") {
		t.Fatalf("the shortest waiting is listed though the board is full:\n%s", card)
	}
}

// A card that ended is not on the board even while the Agent's entry for it is still being taken away.
func TestBoardLeavesWhatEndedAtOnce(t *testing.T) {
	e := newBoardEnv(t)
	ix := e.card("ix1", "box", "ag1", "已经回答了的问题", time.Minute)
	e.tasks("/tasks")
	index(t, cardOf(e.posts[0]), "已经回答了的问题")
	e.s.InteractionEnded(context.Background(), InteractionEnded{Interaction: ix}) // the entry is still pending
	e.clk.advance(boardSettle)
	if c := cardOf(e.lastUpdate()); strings.Contains(c, "已经回答了的问题") {
		t.Fatalf("an ended card is on the board:\n%s", c)
	}
}

// A task whose "Please verify" card goes out (askAcceptance) is on the board within the settle time, and off it once
// the owner passes it: the whole path, not only the board's own state.
func TestBoardTakesADeployedTaskAndDropsItWhenPassed(t *testing.T) {
	ctx := context.Background()
	s, f, _ := acceptService(t)
	clk := newFakeClock()
	s.standing.now, s.standing.after = clk.Now, clk.After
	s.standing.fleetUp.Store(true)
	var mu sync.Mutex
	var edits []string
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", PublicURL: "https://hub.example.com",
			Features: FeishuFeatures{Main: true, Cards: true, Reports: true, Board: true}}, nil
	})
	f.answerApp(AppFeishu, FeishuUpdateCard, func(p json.RawMessage) (any, error) {
		var m struct {
			MessageID string `json:"message_id"`
			Card      string `json:"card"`
		}
		_ = json.Unmarshal(p, &m)
		if m.MessageID == "om_board" {
			mu.Lock()
			edits = append(edits, m.Card)
			mu.Unlock()
		}
		return map[string]any{}, nil
	})
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(edits) == 0 {
			return ""
		}
		return edits[len(edits)-1]
	}
	s.setBoardRef(ctx, &botBoardRef{MessageID: "om_board", At: clk.Now()})
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	clk.advance(time.Minute) // what the start changed is through
	mu.Lock()
	edits = nil
	mu.Unlock()
	s.askAcceptance(onlyTodo(s), "box", "s1", deployedReply)
	clk.advance(boardSettle)
	if c := last(); !strings.Contains(c, "请验收：修登录崩溃") {
		t.Fatalf("the task is not on the board:\n%s", c)
	}
	if !s.acceptReply(ctx, "om_pass", "通过", false, askOf(s, todo.ID).MessageID) {
		t.Fatal("not passed")
	}
	clk.advance(boardSettle)
	if c := last(); strings.Contains(c, "修登录崩溃") || !strings.Contains(bodyOf(t, c), "现在没有等你的事") {
		t.Fatalf("a passed task is still on the board:\n%s", c)
	}
}

// A change the board does not show (a to-do's title, its agents) costs no edit; the board is edited again when what it
// shows differs, which includes how long things have waited.
func TestBoardSkipsAnEditThatSaysTheSame(t *testing.T) {
	e := newBoardEnv(t)
	e.tasks("/tasks")
	ix := e.card("ix1", "box", "ag1", "要发布吗？", 0)
	e.announce(ix)
	e.clk.advance(boardSettle)
	if _, u := e.counts(); u != 1 {
		t.Fatalf("%d edits, want 1", u)
	}
	e.s.todosChanged(context.Background()) // nothing the board shows
	e.clk.advance(boardSettle)
	if _, u := e.counts(); u != 1 {
		t.Fatalf("%d edits for a change the board does not show, want 1", u)
	}
	e.clk.advance(3 * time.Minute) // the question has waited longer now
	e.s.todosChanged(context.Background())
	e.clk.advance(boardSettle)
	if _, u := e.counts(); u != 2 {
		t.Fatalf("%d edits, want 2", u)
	}
	e.tasks("/tasks") // asked for: always written
	if _, u := e.counts(); u != 3 {
		t.Fatalf("%d edits after /tasks, want 3", u)
	}
}

// Two /tasks at once make one board: the second finds the first's and edits it.
func TestBoardTwoTasksAtOnceSendOneBoard(t *testing.T) {
	e := newBoardEnv(t)
	e.mu.Lock()
	e.postDelay = 50 * time.Millisecond
	e.mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.tasks("/tasks")
		}()
	}
	wg.Wait()
	if p, u := e.counts(); p != 1 || u != 1 {
		t.Fatalf("%d boards sent and %d edits, want 1 and 1", p, u)
	}
}

// The settle-time edit waits for a write that is under way (a /tasks, a replacement) instead of running beside it.
func TestBoardRefreshWaitsForAWriteInProgress(t *testing.T) {
	e := newBoardEnv(t)
	e.tasks("/tasks")
	e.card("ix1", "box", "ag1", "等一等再写", 0)
	done := make(chan struct{})
	e.s.standing.done = func() { close(done) }
	e.s.standing.run.Lock() // a write in progress
	go e.s.boardRefresh()
	time.Sleep(100 * time.Millisecond)
	if _, u := e.counts(); u != 0 {
		e.s.standing.run.Unlock()
		t.Fatalf("the refresh wrote beside a write in progress (%d edits)", u)
	}
	e.s.standing.run.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the refresh never ran")
	}
	if _, u := e.counts(); u != 1 {
		t.Fatalf("%d edits after the write, want 1", u)
	}
}

// After a restart the board is the one in the chat's state: /tasks edits it and sends no second.
func TestBoardSurvivesARestart(t *testing.T) {
	e := newBoardEnv(t)
	e.tasks("/tasks")
	e2 := newBoardEnvOn(t, e.st)
	e2.tasks("/tasks")
	if p, u := e2.counts(); p != 0 || u != 1 {
		t.Fatalf("after the restart %d boards sent and %d edits, want 0 and 1", p, u)
	}
	if got := e2.lastUpdate()["message_id"]; got != "om_board_1" {
		t.Fatalf("edited %v", got)
	}
}

// A card heavier than the 30 rows allow is cut by the card's size, not by the count: the board stays inside it and counts
// what it left out, so rows shown and rows counted are all of them.
func TestBoardIsCutByTheCardsSize(t *testing.T) {
	e := newBoardEnv(t)
	const total = 30
	long := strings.Repeat("s", 900)
	for i := 0; i < total; i++ {
		e.card(fmt.Sprintf("ix%02d", i), "box", fmt.Sprintf("%s%02d", long, i), strings.Repeat("问", 60), time.Duration(i+1)*time.Minute)
	}
	e.tasks("/tasks")
	card := cardOf(e.posts[0])
	if len(card) > reportCardMaxBytes {
		t.Fatalf("the board is %d bytes, over %d", len(card), reportCardMaxBytes)
	}
	shown := strings.Count(bodyOf(t, card), "[打开](")
	if shown == 0 || shown >= boardRowsMax {
		t.Fatalf("%d rows shown: the size should cut before the count of %d", shown, boardRowsMax)
	}
	var more int
	if _, err := fmt.Sscanf(bodyOf(t, card)[strings.Index(bodyOf(t, card), "还有"):], "还有 %d 项", &more); err != nil || shown+more != total {
		t.Fatalf("%d shown and %d counted (%v), want %d in all", shown, more, err, total)
	}
}

// With `board` off /tasks says exactly what it said before the board: the posts are the snapshot's cards, replying to the
// message, byte for byte, and nothing else is called.
func TestBoardOffTasksIsTheSnapshotByteForByte(t *testing.T) {
	e := newBoardEnv(t)
	e.setFeatures(FeishuFeatures{Main: true})
	e.card("ix1", "box", "ag1", "要发布吗？", 5*time.Minute)
	e.card("ix2", "server", "asm", "主对话的问题", time.Minute)
	e.s.todos.todos["todo_1"] = &Todo{ID: "todo_1", Title: "手工建的任务"}
	want := e.s.tasksOverview(context.Background()) // the snapshot, which this change does not touch
	if len(want) == 0 {
		t.Fatal("no snapshot")
	}
	e.tasks("/tasks")
	if len(e.posts) != len(want) || len(e.updates) != 0 || len(e.texts) != 0 {
		t.Fatalf("%d posts, %d edits, %d lines; want %d posts only", len(e.posts), len(e.updates), len(e.texts), len(want))
	}
	for i, oc := range want {
		got := e.posts[i]
		if got["card"] != oc.card.JSON() || got["fallback"] != oc.fallback || got["reply_to"] != "om_cmd" {
			t.Fatalf("post %d differs from the snapshot:\n%v\nwant card %s", i, got, oc.card.JSON())
		}
		for k := range got {
			if k != "card" && k != "fallback" && k != "reply_to" {
				t.Fatalf("post %d carries %q beyond what the snapshot sends", i, k)
			}
		}
	}
}

// A bot paired again is a new chat: the board of the old owner is not edited, and /tasks sends a new one.
func TestBoardOfAnOldPairingIsNotEdited(t *testing.T) {
	e := newBoardEnv(t)
	e.tasks("/tasks")
	e.mu.Lock()
	e.pairedAt = time.Unix(200, 0) // paired again while the assistant was away
	e.mu.Unlock()
	e.s.feishuForgetStatus()
	e.s.boardChanged()
	e.card("ix1", "box", "ag1", "新主人的问题", 0)
	e.clk.advance(boardSettle)
	if _, u := e.counts(); u != 0 {
		t.Fatalf("the old owner's board was edited %d times", u)
	}
	if e.boardID() != "" {
		t.Fatalf("the chat still remembers %q", e.boardID())
	}
	e.tasks("/tasks")
	if p, _ := e.counts(); p != 2 {
		t.Fatalf("%d boards sent, want a new one", p)
	}
}

// A board sent again after a send that timed out has the key of the first try, so Feishu makes one message of the two; the
// next replacement has another key.
func TestBoardResendKeepsItsKey(t *testing.T) {
	e := newBoardEnv(t)
	e.tasks("/tasks")
	e.clk.advance(14*24*time.Hour + time.Hour)
	e.mu.Lock()
	e.postErr = context.DeadlineExceeded
	e.mu.Unlock()
	e.tasks("/tasks") // times out
	e.mu.Lock()
	e.postErr = nil
	e.mu.Unlock()
	e.tasks("/tasks") // tried again
	var keys []any
	for _, p := range e.posts {
		if k, _ := p["uuid"].(string); strings.HasPrefix(k, "bd:") { // the snapshot that answers a failed /tasks has none
			keys = append(keys, k)
		}
	}
	if len(keys) != 3 {
		t.Fatalf("%d board sends, want 3 (the first, the one that timed out, its retry)", len(keys))
	}
	if keys[1] != keys[2] || keys[0] == keys[1] {
		t.Fatalf("keys %v: the retry must repeat the key of the first try, and differ from the first board's", keys)
	}
	if n := len(fmt.Sprint(keys[1])); n > 50 {
		t.Fatalf("a key of %d characters", n)
	}
	e.clk.advance(14*24*time.Hour + time.Hour)
	e.tasks("/tasks")
	last := e.posts[len(e.posts)-1]["uuid"]
	if last == keys[2] || last == nil {
		t.Fatalf("the next replacement has key %v, the retry's was %v", last, keys[2])
	}
}

// What changes while the board is going out is on it afterwards: the look that finds the new board is scheduled when it is
// remembered.
func TestBoardChangeDuringTheFirstSendIsOnTheBoard(t *testing.T) {
	e := newBoardEnv(t)
	var ix proto.Interaction
	e.mu.Lock()
	e.onPost = func() {
		e.mu.Lock()
		e.onPost = nil
		e.mu.Unlock()
		ix = e.card("ix1", "box", "ag1", "发出去的时候来的问题", 0)
		e.announce(ix) // no board yet: nothing is scheduled
	}
	e.mu.Unlock()
	e.tasks("/tasks")
	e.clk.advance(boardSettle)
	if _, u := e.counts(); u != 1 {
		t.Fatalf("%d edits, want 1", u)
	}
	index(t, cardOf(e.lastUpdate()), "发出去的时候来的问题")
}

// Without a board, or with the switch off, the to-dos changing all the time cost no call for the bot's status (the off
// switch is looked at once a minute at most).
func TestBoardTodoChangesCostNoStatusCalls(t *testing.T) {
	e := newBoardEnv(t)
	statusCalls := func() int { return len(e.f.appParams(AppFeishu, FeishuStatusGet)) }
	for i := 0; i < 20; i++ { // no board yet
		e.s.todosChanged(context.Background())
	}
	e.clk.advance(time.Hour)
	if n := statusCalls(); n != 0 {
		t.Fatalf("%d status calls with no board", n)
	}
	e.tasks("/tasks")
	e.clk.advance(boardSettle)
	e.setFeatures(FeishuFeatures{Main: true}) // the switch goes off
	e.s.todosChanged(context.Background())
	e.clk.advance(boardSettle) // looked at: found off
	base := statusCalls()
	for i := 0; i < 20; i++ {
		e.s.todosChanged(context.Background())
		e.clk.advance(2 * time.Second)
	}
	if n := statusCalls(); n != base {
		t.Fatalf("%d status calls while the switch is off, want none beyond %d", n, base)
	}
	e.clk.advance(2 * boardOffFor) // and looked at again after a while
	e.s.todosChanged(context.Background())
	e.clk.advance(boardSettle)
	if n := statusCalls(); n != base+1 {
		t.Fatalf("%d status calls after a minute, want %d", n, base+1)
	}
}

// A row whose card the bot nudged under says so (#6084); one it did not nudge says nothing. Nudging a card brings the
// board up to date, so the row is marked without anyone asking for the board.
func TestBoardMarksANudgedRow(t *testing.T) {
	e := newBoardEnv(t)
	e.s.fbot.now = e.clk.Now
	e.f.answerApp(AppNotify, NotifyQuietGet, func(json.RawMessage) (any, error) { return map[string]bool{"quiet": false}, nil })
	rowOf := func(card, q string) string {
		for _, row := range strings.Split(bodyOf(t, card), "\n**") {
			if strings.Contains(row, q) {
				return row
			}
		}
		t.Fatalf("no row for %q:\n%s", q, card)
		return ""
	}
	old := e.card("ix_old", "box", "ag1", "旧的问题", 61*time.Minute)
	e.card("ix_new", "box", "ag2", "新的问题", time.Minute)
	e.s.fbot.mu.Lock()
	e.s.keepBotCardLocked(old.ID, &botCard{MessageID: "om_" + old.ID, Ix: old, At: e.clk.Now().Add(-61 * time.Minute)})
	e.s.keepBotCardLocked("ix_new", &botCard{MessageID: "om_ix_new", Ix: proto.Interaction{ID: "ix_new", MachineID: "box", SessionID: "ag2", Kind: proto.IxQuestion}, At: e.clk.Now().Add(-time.Minute)})
	e.s.fbot.mu.Unlock()
	e.tasks("/tasks")
	card := cardOf(e.posts[0])
	if strings.Contains(card, "已催过") {
		t.Fatalf("a row is marked before any nudge:\n%s", card)
	}

	e.clk.advance(boardSettle) // whatever the first board scheduled has run
	_, before := e.counts()
	e.s.feishuForgetStatus()
	e.s.nudgeTick(context.Background()) // ix_old waited 61 minutes, ix_new one
	if len(e.texts) != 1 || e.texts[0]["reply_to"] != "om_ix_old" {
		t.Fatalf("nudges: %v", e.texts)
	}
	e.clk.advance(boardSettle)
	if _, u := e.counts(); u != before+1 {
		t.Fatalf("%d board edits after a nudge, want %d", u, before+1)
	}
	card = cardOf(e.lastUpdate())
	if r := rowOf(card, "旧的问题"); !strings.Contains(r, "已催过") {
		t.Errorf("the nudged row is not marked:\n%s", card)
	}
	if r := rowOf(card, "新的问题"); strings.Contains(r, "已催过") {
		t.Errorf("a row that was not nudged is marked:\n%s", card)
	}
}

// A nudge that was let go, refused or given up, or that is only being tried, is not shown as sent: the mark is for what
// went out.
func TestBoardDoesNotMarkWhatWasNotNudged(t *testing.T) {
	e := newBoardEnv(t)
	skipped := e.card("ix_skipped", "box", "ag1", "放过的问题", time.Hour)
	trying := e.card("ix_trying", "box", "ag2", "重试中的问题", time.Hour)
	at := e.clk.Now().Add(-time.Hour)
	e.s.fbot.mu.Lock()
	e.s.keepBotCardLocked(skipped.ID, &botCard{MessageID: "om_s", Ix: skipped, At: at, SkippedAt: e.clk.Now(), Tries: 3, TriedAt: at})
	e.s.keepBotCardLocked(trying.ID, &botCard{MessageID: "om_t", Ix: trying, At: at, Tries: 1, TriedAt: at})
	e.s.fbot.mu.Unlock()
	e.tasks("/tasks")
	if card := cardOf(e.posts[0]); !strings.Contains(card, "放过的问题") || !strings.Contains(card, "重试中的问题") || strings.Contains(card, "已催过") {
		t.Fatalf("the board:\n%s", card)
	}
}
