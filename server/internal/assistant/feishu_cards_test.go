package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// listCardMaxBytes is the size the bot's own answer cards stay under (feishubot); the list cards stay under it too.
const listCardMaxBytes = 24000

func longListName(i int) string {
	return fmt.Sprintf("长名%02d-", i) + strings.Repeat("*_[`", 70) // 285 runes, all Markdown metacharacters
}

// /list and /agents clip long conversation / agent / device names so Feishu cannot refuse the card, and post the
// same list as the card's text fallback (with Markdown metacharacters stripped), so a refused card still delivers
// it (#4362).
func TestBotCommandListCardsClipAndFallback(t *testing.T) {
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID = "server"; v.Main = "main0" })
	now := time.Now().UTC()
	var cs []proto.Session
	for i := 0; i < 8; i++ {
		cs = append(cs, proto.Session{SessionID: fmt.Sprintf("c%d", i), MachineID: "server", Assistant: true,
			Title: longListName(i), UpdatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	f.setSessions("server", cs...)
	ctx := context.Background()
	if h, _ := s.botCommand(ctx, FeishuFeatures{Main: true}, "om", "/对话", "", ""); !h {
		t.Fatal("/list not handled")
	}
	post := (*posts)[len(*posts)-1]
	card, _ := post["card"].(string)
	fallback, _ := post["fallback"].(string)
	if len(card) >= listCardMaxBytes {
		t.Fatalf("/list card %d bytes, want under %d", len(card), listCardMaxBytes)
	}
	// Button values carry every name verbatim and JSON keeps "[", so its count proves the names were clipped: a
	// clipped name keeps <=14 groups (shown twice at most), an unclipped one 70.
	if n := strings.Count(card, "["); n > 8*25 {
		t.Fatalf("/list card carries %d unclipped name groups", n)
	}
	lines := strings.Split(fallback, "\n") // a title line plus eight "- …" rows, Markdown metacharacters stripped
	if len(lines) != 9 || !strings.HasPrefix(lines[1], "- ") || strings.ContainsAny(fallback, "*_[`") {
		t.Fatalf("/list fallback lines=%d: %q", len(lines), fallback)
	}

	f.devices = []Device{{ID: "box", Name: strings.Repeat("机", 280), Online: true, Reachable: true}}
	states := []string{proto.StateNeedsYou, proto.StateWorking, proto.StateUnread, proto.StateIdle}
	var as []proto.Session
	for i := 0; i < 12; i++ {
		as = append(as, proto.Session{SessionID: fmt.Sprintf("a%02d", i), MachineID: "box", Agent: "claude",
			Title: longListName(i), State: states[i%4], UpdatedAt: now})
	}
	f.setSessions("box", as...)
	if h, _ := s.botCommand(ctx, FeishuFeatures{Agents: true}, "om", "/agents", "", ""); !h {
		t.Fatal("/agents not handled")
	}
	post = (*posts)[len(*posts)-1]
	card, _ = post["card"].(string)
	fallback, _ = post["fallback"].(string)
	if len(card) >= listCardMaxBytes {
		t.Fatalf("/agents card %d bytes, want under %d", len(card), listCardMaxBytes)
	}
	if n := strings.Count(card, "["); n > 10*25 {
		t.Fatalf("/agents card carries %d unclipped name groups", n)
	}
	// Each row says its state in words (the card shows it as a tag), not as a coloured dot.
	if n := strings.Count(fallback, "\n- "); n != 10 || !strings.Contains(fallback, i18n.T("", "assistant-feishu_cards.state_needs_you")) ||
		strings.Contains(fallback, "🔴") || strings.ContainsAny(fallback, "*_[`") {
		t.Fatalf("/agents fallback rows=%d, state or escaping wrong: %q", n, fallback)
	}
	if !strings.Contains(card, `"tag":"interactive_container"`) {
		t.Fatalf("/agents rows are not pressed whole: %s", card)
	}
	// The card's tail offers starting a new agent without typing /start.
	if !strings.Contains(card, `"a":"start_form"`) {
		t.Fatalf("/agents card has no start-new button: %s", card)
	}
}

// Pressing "Chat here" on a list-card row points the chat at that target and binds the pressed card itself: a quote
// reply to the card has it as the parent and a topic started on it has it as the root, so botTargetFor routes both
// to the target (#5438).
func TestSwitchBindsPressedCard(t *testing.T) {
	s, _, _, _ := startCardHub(t)
	ctx := context.Background()
	var a feishubot.CardAction
	a.Action.Value = map[string]any{"a": "switch", "k": targetAgent, "m": "box", "s": "agent01", "t": "修登录 · box"}
	a.Context.MessageID = "om_agents"
	r := s.botCardAction(ctx, &a, "")
	wantToast := i18n.T("", "assistant-feishu_cards.now_to_reply", i18n.Args{"title": "修登录 · box"})
	if r.Toast == nil || r.Toast.Content != wantToast {
		t.Fatalf("toast %+v, want %q", r.Toast, wantToast)
	}
	want := botTarget{Kind: targetAgent, MachineID: "box", SessionID: "agent01"}
	if got := s.botTargetFor("", "om_agents"); got != want { // a quote reply to the list card
		t.Fatalf("reply to the card routes to %+v, want %+v", got, want)
	}
	if got := s.botTargetFor("om_agents", "om_agents"); got != want { // a topic started on the card
		t.Fatalf("topic on the card routes to %+v, want %+v", got, want)
	}
	if got := s.botTargetFor("", ""); got != want {
		t.Fatalf("current target %+v, want %+v", got, want)
	}
}

// The agents card's "Start new agent" button posts the same form /start builds (startAgentCard), quoting the
// pressed list card, and starts nothing itself (#5438).
func TestStartFormCallbackPostsStartCard(t *testing.T) {
	s, f, posts, _ := startCardHub(t)
	ctx := context.Background()
	var a feishubot.CardAction
	a.Action.Value = map[string]any{"a": "start_form"}
	a.Context.MessageID = "om_agents"
	r := s.botCardAction(ctx, &a, "")
	if r.Toast == nil || r.Toast.Content != i18n.T("", "assistant-feishu_cards.start_form_opening") {
		t.Fatalf("toast %+v", r.Toast)
	}
	waitForForm(t, posts, "the start form was not posted")
	ps := f.appParams(AppFeishu, FeishuPost)
	if len(ps) != 1 {
		t.Fatalf("posted %d start forms, want one", len(ps))
	}
	post := ps[0]
	if replyTo, _ := post["reply_to"].(string); replyTo != "om_agents" {
		t.Fatalf("the form replies to %q, want the pressed list card", replyTo)
	}
	card, _ := post["card"].(string)
	for _, want := range []string{
		i18n.T("", "assistant-feishu_cards.start_title"),
		`"name":"machine"`, `"name":"cwd"`, `"name":"prompt"`, `"a":"start"`,
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("start form card missing %q:\n%s", want, card)
		}
	}
	if got := f.startCount(); got != 0 {
		t.Fatalf("the form callback started %d agents", got)
	}
}

// startCardHub is a service whose feishu app has the Agents feature on and one machine "box"; posts counts the
// started-cards and says the failure notes the start goroutine sends.
func startCardHub(t *testing.T) (*Service, *fakeHub, chan struct{}, chan string) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.sessions["box"] = nil // the fake only answers a machine it knows
	f.devices = []Device{{ID: "box", Name: "box", Home: "/home/me", Online: true, Reachable: true}}
	s := newTestService(t, f)
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", Features: FeishuFeatures{Agents: true}}, nil
	})
	says := make(chan string, 8)
	f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(p, &m)
		says <- m.Text
		return map[string]any{"ok": true}, nil
	})
	posts := make(chan struct{}, 8)
	f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		posts <- struct{}{}
		return map[string]any{"message_id": "om_started"}, nil
	})
	return s, f, posts, says
}

func startCardAction(mid string) *feishubot.CardAction {
	var a feishubot.CardAction
	a.Action.Value = map[string]any{"a": "start"}
	a.Action.FormValue = map[string]any{"machine": "box", "cwd": "/home/me/work"} // prompt is optional on the card
	a.Context.MessageID = mid
	return &a
}

func (f *fakeHub) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func (f *fakeHub) agentCount() int { // successful session.start calls only (the fake counts failed attempts in starts)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.agents
}

// pressSlotHeld reports whether message mid's button kind slot is still taken (test only).
func (s *Service) pressSlotHeld(mid, kind string) bool {
	s.fbot.mu.Lock()
	defer s.fbot.mu.Unlock()
	_, ok := s.fbot.pressAt[mid+kind]
	return ok
}

// waitForStart polls until the fake saw wantStarts session starts and drains the newPosts started-cards that go
// with this step, so the goroutine botStartAgent spawns has posted before the test moves on.
func waitForStart(t *testing.T, f *fakeHub, posts chan struct{}, says chan string, wantStarts, newPosts int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && f.agentCount() < wantStarts {
		time.Sleep(2 * time.Millisecond)
	}
	if got := f.agentCount(); got != wantStarts {
		var gotSays []string
		for {
			select {
			case m := <-says:
				gotSays = append(gotSays, m)
			default:
				t.Fatalf("agent starts = %d, want %d; says: %v", got, wantStarts, gotSays)
			}
		}
	}
	for i := 0; i < newPosts; i++ {
		select {
		case <-posts:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d started cards posted", i, newPosts)
		}
	}
}

// A double press of the same /start card (a double tap, Feishu's redelivery, or the hub's resend) starts one agent:
// both presses get the same "Starting" toast and a different card can still start another agent (#4340).
func TestStartCardDoublePressStartsOneAgent(t *testing.T) {
	s, f, posts, says := startCardHub(t)
	ctx := context.Background()
	want := i18n.T("", "assistant-feishu_cards.starting")

	first := s.botCardAction(ctx, startCardAction("om_card1"), "")
	second := s.botCardAction(ctx, startCardAction("om_card1"), "")
	if first.Toast == nil || second.Toast == nil || first.Toast.Content != want || second.Toast.Content != want {
		t.Fatalf("toasts %+v %+v, want both %q", first.Toast, second.Toast, want)
	}
	waitForStart(t, f, posts, says, 1, 1)
	// The swallowed press must not start anything later either: give a wrongly-spawned goroutine time to land and
	// prove no second start, and no second started-card, exists.
	time.Sleep(50 * time.Millisecond)
	if got := f.agentCount(); got != 1 {
		t.Fatalf("started agents after grace = %d, want 1", got)
	}
	select {
	case <-posts:
		t.Fatal("the duplicate press posted a second started-card")
	default:
	}

	s.botCardAction(ctx, startCardAction("om_card2"), "")
	waitForStart(t, f, posts, says, 2, 1)
}

// A press whose form fails validation (no directory) answers the warning without taking the dedup slot: a corrected
// press of the same card right afterwards still starts one agent.
func TestStartCardInvalidFormDoesNotTakeSlot(t *testing.T) {
	s, f, posts, says := startCardHub(t)
	ctx := context.Background()

	bad := startCardAction("om_card1")
	bad.Action.FormValue["cwd"] = "   "
	r := s.botCardAction(ctx, bad, "")
	want := i18n.T("", "assistant-feishu_cards.pick_machine_dir")
	if r.Toast == nil || r.Toast.Content != want {
		t.Fatalf("toast %+v, want %q", r.Toast, want)
	}
	s.botCardAction(ctx, startCardAction("om_card1"), "")
	waitForStart(t, f, posts, says, 1, 1)
}

// When the start itself fails (the node refused it), the dedup slot is released: the owner sees the failure and can
// press the same card again to retry, instead of getting a bare "Starting" with nothing starting.
func TestStartCardFailedStartLetsNextPressThrough(t *testing.T) {
	s, f, posts, says := startCardHub(t)
	ctx := context.Background()
	f.failStart = errors.New("node is down")

	s.botCardAction(ctx, startCardAction("om_card1"), "")
	select {
	case <-says:
	case <-time.After(2 * time.Second):
		t.Fatal("the failure note was not sent")
	}
	deadline := time.Now().Add(2 * time.Second) // the note lands before the slot is released
	for s.pressSlotHeld("om_card1", "start") && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if s.pressSlotHeld("om_card1", "start") {
		t.Fatal("the failed start kept its dedup slot")
	}
	if got := f.agentCount(); got != 0 {
		t.Fatalf("started agents after failure = %d, want 0", got)
	}
	f.failStart = nil
	s.botCardAction(ctx, startCardAction("om_card1"), "")
	waitForStart(t, f, posts, says, 1, 1)
}

// After the dedup window passes, pressing the same card again starts another agent: the guard is not a permanent
// block on the machine and directory. The window is shrunk after the first start fully posted; botStartAgent's
// goroutine never reads the window, so shrinking it cannot race with that goroutine.
func TestStartCardPressAgainAfterWindow(t *testing.T) {
	s, f, posts, says := startCardHub(t)
	ctx := context.Background()

	s.botCardAction(ctx, startCardAction("om_card1"), "")
	waitForStart(t, f, posts, says, 1, 1)
	botStartCardWindow = 0 // the first press is now older than the window
	t.Cleanup(func() { botStartCardWindow = 60 * time.Second })
	s.botCardAction(ctx, startCardAction("om_card1"), "")
	waitForStart(t, f, posts, says, 2, 1)
}

// A press without a card message id is never deduped (the guard keys on the card message), so a malformed callback
// cannot lock the machine out.
func TestStartCardWithoutMessageIDIsNotDeduped(t *testing.T) {
	s, f, posts, says := startCardHub(t)
	ctx := context.Background()

	s.botCardAction(ctx, startCardAction(""), "")
	s.botCardAction(ctx, startCardAction(""), "")
	waitForStart(t, f, posts, says, 2, 2)
}

// A start that fails only after the window must not free a slot a later manual press has taken: the late failure
// callback carries the timestamp its own press stamped, and the compare-and-swap leaves the newer slot in place, so
// a third press is still swallowed instead of starting a third agent.
func TestStartCardLateFailureKeepsNewerSlot(t *testing.T) {
	s, _, _, _ := startCardHub(t)
	mid := "om_card1"

	botStartCardWindow = 0 // both presses are past the window, so the second press re-stamps the slot
	t.Cleanup(func() { botStartCardWindow = 60 * time.Second })
	_, first := s.startCardPressed(mid)
	time.Sleep(time.Millisecond)
	if blocked, second := s.startCardPressed(mid); blocked || !second.After(first) {
		t.Fatalf("second press after the window: blocked=%v second=%v first=%v, want it to take a fresh slot", blocked, second, first)
	}
	// The first start's failure arrives late; it owns the old timestamp and must not delete the newer slot.
	s.forgetStartCard(mid, first)
	if !s.pressSlotHeld(mid, "start") {
		t.Fatal("the late failure released the second press's slot")
	}
	// Inside the restored window a third press is therefore still swallowed.
	botStartCardWindow = 60 * time.Second
	if blocked, _ := s.startCardPressed(mid); !blocked {
		t.Fatal("third press was not deduped: the late failure had dropped the newer slot")
	}
}

// pressStartForm presses the agents card's "Start new agent" button on message mid, as Lark delivers a press, and
// checks the toast the owner gets for it.
func pressStartForm(t *testing.T, s *Service, mid string) {
	t.Helper()
	var a feishubot.CardAction
	a.Action.Value = map[string]any{"a": "start_form"}
	a.Context.MessageID = mid
	want := i18n.T("", "assistant-feishu_cards.start_form_opening")
	if r := s.botCardAction(context.Background(), &a, ""); r.Toast == nil || r.Toast.Content != want {
		t.Fatalf("press of %s: toast %+v, want %q", mid, r.Toast, want)
	}
}

// waitForForm is a start form posted (posts is startCardHub's).
func waitForForm(t *testing.T, posts chan struct{}, why string) {
	t.Helper()
	select {
	case <-posts:
	case <-time.After(2 * time.Second):
		t.Fatal(why)
	}
}

// A press of the agents card's "Start new agent" delivered twice (a double tap, or the hub resending a call it gave
// up on) opens one form: the second press gets the same toast and posts nothing, as the /start button does. A press
// of another list card is another press and opens its own form (#3789).
func TestStartFormPressOpensOneForm(t *testing.T) {
	s, _, posts, _ := startCardHub(t)

	pressStartForm(t, s, "om_agents")
	pressStartForm(t, s, "om_agents") // the same press again: a second tap, or the hub's resend
	waitForForm(t, posts, "the start form was not posted")
	// The swallowed press posts from no goroutine of its own, so nothing of it can land later either.
	time.Sleep(50 * time.Millisecond)
	select {
	case <-posts:
		t.Fatal("the duplicate press posted a second start form")
	default:
	}

	pressStartForm(t, s, "om_agents2")
	waitForForm(t, posts, "a press of another list card posted no start form")
}

// After the form window, pressing the same card again opens another form: the guard covers a press delivered twice,
// not the owner asking for a new form a moment later.
func TestStartFormPressAgainAfterWindow(t *testing.T) {
	s, _, posts, _ := startCardHub(t)

	pressStartForm(t, s, "om_agents")
	waitForForm(t, posts, "the first start form was not posted")
	botFormWindow = 0
	t.Cleanup(func() { botFormWindow = 10 * time.Second })
	pressStartForm(t, s, "om_agents")
	waitForForm(t, posts, "a press after the window posted no start form")
}

// A start form that could not be posted says so and frees its press slot: the owner can press the same button again
// instead of getting "Opening the start form" and nothing after it (#3789).
func TestStartFormFailedPostFreesThePress(t *testing.T) {
	s, f, posts, says := startCardHub(t)
	f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) { return nil, errors.New("the app is down") })

	pressStartForm(t, s, "om_agents")
	select {
	case <-says:
	case <-time.After(2 * time.Second):
		t.Fatal("the owner was not told that the start form could not be opened")
	}
	f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		posts <- struct{}{}
		return map[string]any{"message_id": "om_form"}, nil
	})
	pressStartForm(t, s, "om_agents") // a retry press of the same card, right away
	waitForForm(t, posts, "the retry press posted no start form: the failed one kept its slot")
}

// cardButtons is every button of a card's button rows (top level), by its text: what pressing it answers.
func cardButtons(t *testing.T, c *feishubot.Card) map[string]map[string]any {
	t.Helper()
	var m struct {
		Body struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(c.JSON()), &m); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, e := range m.Body.Elements {
		if e["tag"] != "column_set" {
			continue
		}
		cols, _ := e["columns"].([]any)
		for _, col := range cols {
			els, _ := col.(map[string]any)["elements"].([]any)
			for _, one := range els {
				b, _ := one.(map[string]any)
				if b["tag"] != "button" {
					continue
				}
				label, _ := b["text"].(map[string]any)["content"].(string)
				value := map[string]any{}
				if bs, ok := b["behaviors"].([]any); ok && len(bs) > 0 {
					value, _ = bs[0].(map[string]any)["value"].(map[string]any)
				}
				out[label] = value
			}
		}
	}
	return out
}

// A permission card's third button follows the app's rule (BUIPermissionForm, the web's PermissionCard): Always allow for this session
// for Claude on a conversation, and for another agent only what it says that always allows. A runtime cannot keep
// Claude's session rule (node/runtimes.go), so the button there would allow once and ask again.
func TestFeishuPermissionCardOffersAlwaysOnlyWhereItWorks(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	s := newTestService(t, f)
	ctx := context.Background()
	always := i18n.T(i18n.ZhHans, "assistant-feishu_cards.always_allow")
	allow := i18n.T(i18n.ZhHans, "assistant-feishu_cards.allow")
	deny := i18n.T(i18n.ZhHans, "assistant-feishu_cards.deny")

	claude := proto.Interaction{ID: "ix_p1", MachineID: "server", SessionID: "s1", Kind: proto.IxPermission, ToolName: "Bash"}
	bs := cardButtons(t, s.interactionCard(ctx, claude))
	if v, ok := bs[always]; !ok || bs[allow] == nil || bs[deny] == nil || v["always"] != "1" || v["allow"] != "1" {
		t.Fatalf("claude's card: %v", bs)
	}
	named := claude // the web's rule also takes agent "claude" as Claude
	named.ID, named.Agent = "ix_p5", "claude"
	if _, ok := cardButtons(t, s.interactionCard(ctx, named))[always]; !ok {
		t.Fatal(`agent "claude" lost 本会话总是允许`)
	}

	// A runtime's card (pi, DSH, opencode, Paseo): a session, no always_label, and no rule of Claude's to keep.
	runtime := claude
	runtime.ID, runtime.Agent, runtime.AgentLabel = "ix_p2", "pi", "pi"
	bs = cardButtons(t, s.interactionCard(ctx, runtime))
	if _, ok := bs[always]; ok || bs[allow] == nil || bs[deny] == nil {
		t.Fatalf("a runtime's card offers 本会话总是允许: %v", bs)
	}

	// Codex that does offer it: its own words, as the app shows them.
	codex := runtime
	codex.ID, codex.Agent, codex.AlwaysLabel = "ix_p3", "codex", "本会话不再问这个命令"
	if v, ok := cardButtons(t, s.interactionCard(ctx, codex))["本会话不再问这个命令"]; !ok || v["always"] != "1" {
		t.Fatalf("codex's own always label: %v", v)
	}

	// A card of the hub's own (no conversation) with nothing naming an always: nothing to offer.
	bare := proto.Interaction{ID: "ix_p4", MachineID: "server", Kind: proto.IxPermission, ToolName: "Bash"}
	if bs := cardButtons(t, s.interactionCard(ctx, bare)); len(bs) != 2 || bs[always] != nil {
		t.Fatalf("a card without a conversation: %v", bs)
	}

	// The manager's word wins (#3590): its own card in a conversation, which nothing would keep, offers none; one it
	// remembers offers it even for an agent that would not.
	yes, no := true, false
	own := claude
	own.ID, own.ToolName, own.AlwaysHonored = "ix_p6", "iphone_do", &no
	if bs := cardButtons(t, s.interactionCard(ctx, own)); len(bs) != 2 || bs[always] != nil {
		t.Fatalf("the manager's own card offers 本会话总是允许: %v", bs)
	}
	kept := runtime
	kept.ID, kept.AlwaysHonored = "ix_p7", &yes
	if v, ok := cardButtons(t, s.interactionCard(ctx, kept))[always]; !ok || v["always"] != "1" {
		t.Fatalf("a remembered card lost 本会话总是允许: %v", v)
	}
}

// cardMarkdown is a card's first Markdown element: the text it draws first.
func cardMarkdown(t *testing.T, c *feishubot.Card) string {
	t.Helper()
	var m struct {
		Body struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(c.JSON()), &m); err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Body.Elements {
		if e["tag"] == "markdown" {
			s, _ := e["content"].(string)
			return s
		}
	}
	t.Fatalf("no Markdown block on the plan card: %s", c.JSON())
	return ""
}

// A plan too long for its card is cut at a line break, and a code block the cut lands in is closed, so the clipped
// note after it reads as text, not as the code's last line. A plan that fits is shown as it is.
func TestFeishuPlanCardClipClosesItsCodeBlock(t *testing.T) {
	s := newTestService(t, newFakeHub())
	ctx := i18n.With(context.Background(), i18n.En)
	note := i18n.T(i18n.En, "assistant-feishu_cards.plan_clipped")
	planCard := func(plan string) string {
		return cardMarkdown(t, s.interactionCard(ctx, proto.Interaction{ID: "ix_plan", Kind: proto.IxPlan, Plan: plan}))
	}
	line := strings.Repeat("step ", 19) + "done\n" // 100 runes
	// prose is n runes of whole lines.
	prose := func(n int) string {
		p := strings.Repeat(line, n/100)
		if r := n % 100; r > 0 {
			p += strings.Repeat("x", r-1) + "\n"
		}
		return p
	}
	var code strings.Builder
	for i := range 200 {
		fmt.Fprintf(&code, "\tfmt.Println(%d) // line %d of the change\n", i, i)
	}
	withCode := func(n int) string { return prose(n) + "```go\n" + code.String() + "```\n\nThen run the tests.\n" }

	for _, c := range []struct{ name, plan string }{
		{"cut inside the code block", withCode(4800)},
		{"cut right after the opener", withCode(4990)},
		{"cut in the middle of a prose line", "1. Intro\n" + strings.Repeat(line, 60)},
	} {
		md := planCard(c.plan)
		head, ok := strings.CutSuffix(md, note)
		if !ok {
			t.Fatalf("%s: the clipped note is not last: %q", c.name, md[max(0, len(md)-200):])
		}
		if n := len([]rune(head)); n > maxPlanRunes {
			t.Fatalf("%s: %d runes of plan, over %d", c.name, n, maxPlanRunes)
		}
		fences := 0
		for _, l := range strings.Split(head, "\n") {
			if strings.HasPrefix(l, "```") {
				fences++
			}
		}
		if fences%2 != 0 {
			t.Fatalf("%s: %d fence lines, so the note shows as code: %q", c.name, fences, md[max(0, len(md)-200):])
		}
		body := strings.TrimSuffix(head, "\n```") // the close the cut adds to a code block
		if !strings.HasPrefix(c.plan, body) || c.plan[len(body)] != '\n' {
			t.Fatalf("%s: not cut at one of the plan's line breaks: %q", c.name, head[max(0, len(head)-120):])
		}
	}

	short := "1. Change `cache.go`\n```go\nx := 1\n```\n2. Test it\n"
	if md := planCard(short); md != feishubot.CardMarkdown(short) {
		t.Fatalf("a short plan changed: %q", md)
	}
	if full := prose(maxPlanRunes); planCard(full) != feishubot.CardMarkdown(full) {
		t.Fatal("a plan of exactly maxPlanRunes runes was clipped")
	}
	if head, ok := strings.CutSuffix(planCard(strings.Repeat("\n", 6000)), note); !ok || len([]rune(head)) > maxPlanRunes {
		t.Fatalf("a plan of line breaks: %d runes, note last %v", len([]rune(head)), ok)
	}
}

// The cards that ask the owner: who asks in the header (the conversation, the computer under it), orange, the kind as
// a tag; one row of actions ending in a "more" menu with dismissing and the conversation's page; an approval that
// lets the agent go on by itself asks first.
func TestInteractionCardsLook(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	withWebBot(f, "https://hub.example.com")
	s := newTestService(t, f)
	ctx := context.Background()
	for _, c := range []struct {
		ix      proto.Interaction
		tag     string
		confirm int
	}{
		{proto.Interaction{ID: "ix_q", MachineID: "server", SessionID: "g1", SessionTitle: "修登录", Kind: proto.IxQuestion,
			Questions: []proto.Question{{Question: "用哪个库？", Options: []proto.QuestionOption{{Label: "A"}, {Label: "B"}}, Recommend: "B"}}}, "提问", 0},
		{proto.Interaction{ID: "ix_plan", MachineID: "server", SessionID: "g1", SessionTitle: "修登录", Kind: proto.IxPlan, Plan: "1. 改\n2. 测"}, "计划", 1},
		{proto.Interaction{ID: "ix_p", MachineID: "server", SessionID: "g1", SessionTitle: "修登录", Kind: proto.IxPermission, ToolName: "Bash"}, "审批", 1},
		{proto.Interaction{ID: "ix_s", MachineID: "server", SessionID: "g1", SessionTitle: "修登录", Kind: proto.IxScreen, Screen: "$ "}, "终端", 0},
	} {
		js := s.interactionCard(ctx, c.ix).JSON()
		for _, want := range []string{`"template":"orange"`, `"content":"修登录"}`, `"content":"` + c.tag + `"}`, `"tag":"overflow"`,
			`"url":"https://hub.example.com/s/server/g1"`} {
			if !strings.Contains(js, want) {
				t.Errorf("%s card lacks %s: %s", c.ix.Kind, want, js)
			}
		}
		for _, bad := range []string{"❓", "📋", "🔐", "🖥"} {
			if strings.Contains(js, bad) {
				t.Errorf("%s card has %s: %s", c.ix.Kind, bad, js)
			}
		}
		if n := strings.Count(js, `"confirm":`); n != c.confirm {
			t.Errorf("%s card asks first %d times, want %d: %s", c.ix.Kind, n, c.confirm, js)
		}
	}
}

// An agent that goes by its herdr tab name is named by it on the card asking the owner and on the card once answered,
// its CLI title at the end of the subtitle.
func TestInteractionCardHerdrSessionNames(t *testing.T) {
	s := newTestService(t, newFakeHub())
	ctx := context.Background()
	if err := s.store().PutSetting(ctx, agentsUseHerdrTabNameKey, true); err != nil {
		t.Fatal(err)
	}
	d := newFleetDevs()
	d.add("server", "box")
	d.setSessions("server", []proto.Session{{SessionID: "g1", Title: "CLI session name", SpaceKind: "herdr", SlotID: "w1:t1",
		SlotName: "Export work", State: proto.StateNeedsYou, UpdatedAt: time.Now()}})
	s.devs = d
	ix := proto.Interaction{ID: "ix_p", MachineID: "server", SessionID: "g1", SessionTitle: "CLI session name",
		Kind: proto.IxPermission, ToolName: "Bash"}
	for _, c := range []struct {
		name string
		card *feishubot.Card
	}{{"asking", s.interactionCard(ctx, ix)}, {"answered", s.answeredCard(ctx, ix, "允许")}} {
		h := c.card.Header
		if h.Title.Content != "Export work" || h.Subtitle == nil || !strings.HasSuffix(h.Subtitle.Content, "CLI session name") {
			t.Errorf("%s card's header: %s", c.name, c.card.JSON())
		}
	}
}

// The option the asker recommends is the card's primary button.
func TestQuestionCardRecommendIsPrimary(t *testing.T) {
	s := newTestService(t, newFakeHub())
	ix := proto.Interaction{ID: "ix_q", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "用哪个库？",
		Options: []proto.QuestionOption{{Label: "A"}, {Label: "B"}}, Recommend: "B"}}}
	js := s.interactionCard(context.Background(), ix).JSON()
	if !strings.Contains(js, `"text":{"tag":"plain_text","content":"B"},"type":"primary"`) ||
		strings.Contains(js, `"text":{"tag":"plain_text","content":"A"},"type":"primary"`) {
		t.Fatalf("card %s", js)
	}
}

// With the option the asker recommends as the primary button, the written answer's submit is not one too.
func TestQuestionCardOnePrimary(t *testing.T) {
	s := newTestService(t, newFakeHub())
	ix := proto.Interaction{ID: "ix_q", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "用哪个库？",
		Options: []proto.QuestionOption{{Label: "A"}, {Label: "B"}}, Recommend: "B"}}}
	if n := strings.Count(s.interactionCard(context.Background(), ix).JSON(), `"type":"primary"`); n != 1 {
		t.Fatalf("%d primary buttons", n)
	}
	ix.Questions[0].Recommend = ""
	if n := strings.Count(s.interactionCard(context.Background(), ix).JSON(), `"type":"primary"`); n != 1 {
		t.Fatalf("%d primary buttons without a recommendation", n)
	}
}
