package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The workbench (#1961, docs/extensions/bots.md "The workbench"): with the bot's `workbench` switch on, the chat gets a
// home card that gathers what Persona has going on and opens every other card (/home), the day at a glance (/today) and
// the Inbox with its actions (/inbox). The home card is the standing board (feishu_board.go) drawn as a home page,
// pinned and kept the only one (feishu_workbench_pin.go). These cards only read the assistant's state and call what
// the app's pages call; nothing here decides anything a model would. With the switch off none of it exists: the
// commands go on as text, no card carries a workbench button and /start has no permission field.

const (
	// wbInboxMost is the most Inbox rows one /inbox card lists: each has a row of buttons.
	wbInboxMost = 6
	// wbHomeRows is how many waiting things, agents at work and Inbox items the home card shows of each.
	wbHomeRows = 3
	// wbFilled is Card 2.0's filled primary button: the one action a card is about.
	wbFilled = "primary_filled"
)

func wbT(l i18n.Lang, key string, args ...i18n.Args) string {
	return i18n.T(l, "assistant-feishu_workbench."+key, args...)
}

// workbenchCommand answers /home, /today and /inbox with the switch on; false when cmd is none of them (or the switch
// is off), so the message goes on as it always did.
func (s *Service) workbenchCommand(ctx context.Context, f FeishuFeatures, cmd string) bool {
	if !f.Workbench {
		return false
	}
	// The cards stand on their own: quoting the one-word command above them only adds a line.
	reply := func(c *botcard.Card, fallback string) {
		if _, err := s.botPost(ctx, "", c, fallback); err != nil {
			s.logf("bots: workbench: %v", err)
		}
	}
	switch cmd {
	case "/home", "/工作台", "/主页": // i18n-ignore: command aliases typed by the user; the catalogs describe them in /help
		s.summonWorkbench(ctx)
	case "/today", "/今日", "/今天": // i18n-ignore: command aliases typed by the user
		reply(s.todayCard(ctx))
	case "/inbox", "/收件箱": // i18n-ignore: command aliases typed by the user
		reply(s.inboxCard(ctx))
	default:
		return false
	}
	return true
}

// workbenchNav answers a workbench button ({a: nav, to}): a switch of where messages go is a toast; a card is posted
// from a goroutine (Feishu gives the callback 3 seconds).
func (s *Service) workbenchNav(ctx context.Context, a *botcard.CardAction) *botcard.CardResponse {
	l := s.botLang(ctx)
	f, _ := s.feishuFeatures(ctx)
	if !f.Workbench {
		return toast("info", i18n.T(l, "assistant-feishu_cards.card_expired"))
	}
	to := a.Str("to")
	switch to {
	case "new", "main":
		if !f.Main {
			return toast("error", i18n.T(l, "assistant-feishu_cards.main_off"))
		}
		if to == "new" {
			s.setBotCurrent(botTarget{Kind: targetPersona})
			return toast("success", i18n.T(l, "assistant-feishu_cards.next_is_new"))
		}
		s.setBotCurrent(botTarget{Kind: targetMain})
		return toast("success", i18n.T(l, "assistant-feishu_cards.now_to_main"))
	case "agents", "start":
		if !f.Agents {
			return toast("error", i18n.T(l, "assistant-feishu_cards.agents_off_short"))
		}
	case "refresh":
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.workbenchRefresh(cctx)
		}()
		return toast("success", wbT(l, "refreshed"))
	case "ix":
		id := a.Str("id")
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.workbenchBringCard(cctx, id)
		}()
		return toast("info", wbT(l, "opening"))
	case "today", "inbox", "tasks", "home", "list":
	default:
		return toast("info", i18n.T(l, "assistant-feishu_cards.card_expired"))
	}
	go func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		post := func(c *botcard.Card, fallback string) {
			if _, err := s.botPost(cctx, "", c, fallback); err != nil {
				s.logf("bots: workbench %s: %v", to, err)
			}
		}
		switch to {
		case "agents":
			post(s.agentsCard(cctx, ""))
		case "start":
			post(s.startAgentCard(cctx, ""), "")
		case "list":
			post(s.conversationsCard(cctx))
		case "tasks":
			for _, oc := range s.tasksOverview(cctx) {
				post(oc.card, oc.fallback)
			}
		case "today":
			post(s.todayCard(cctx))
		case "inbox":
			post(s.inboxCard(cctx))
		case "home":
			s.summonWorkbench(cctx)
		}
	}()
	return toast("info", wbT(l, "opening"))
}

func wbNavButton(label, style, to string) botcard.Button {
	return botcard.Btn(label, style, map[string]any{"a": "nav", "to": to})
}

// wbCounts is what the home card sums up.
type wbCounts struct {
	working, unread, idle int // agents by state
	pending               int // questions and approvals waiting
	inboxNeed, inboxFYI   int
	inboxTop              []InboxItem
	online, machines      int
}

// wbAgent is an agent at work, as the home card lists it.
type wbAgent struct {
	d    Device
	sess proto.Session
}

// wbIdleFor is how recently an idle agent must have moved to count as one of the owner's.
const wbIdleFor = 48 * time.Hour

func (s *Service) workbenchCount(ctx context.Context, now time.Time) (wbCounts, []wbAgent) {
	var c wbCounts
	var busy []wbAgent
	for _, d := range s.devices(ctx) {
		c.machines++
		if d.Online {
			c.online++
		}
		list, _ := s.sessions(ctx, d.ID)
		for _, sess := range list {
			if sess.Assistant || sess.HiddenByDirectory() {
				continue
			}
			switch sess.State {
			case proto.StateNeedsYou, proto.StateWorking:
				c.working++
				busy = append(busy, wbAgent{d, sess})
			case proto.StateUnread:
				c.unread++
				busy = append(busy, wbAgent{d, sess})
			case proto.StateIdle:
				if now.Sub(sess.UpdatedAt) < wbIdleFor {
					c.idle++
				}
			}
		}
	}
	order := map[string]int{proto.StateNeedsYou: 0, proto.StateWorking: 1, proto.StateUnread: 2}
	slices.SortStableFunc(busy, func(a, b wbAgent) int { return order[a.sess.State] - order[b.sess.State] })
	c.pending = len(s.visiblePendingInteractions())
	for _, it := range s.workbenchInbox() {
		if wbNeedsYou(it) {
			c.inboxNeed++
			if len(c.inboxTop) < wbHomeRows {
				c.inboxTop = append(c.inboxTop, it)
			}
		} else {
			c.inboxFYI++
		}
	}
	return c, busy
}

func wbNeedsYou(it InboxItem) bool { return it.Tier == TierAction || it.Urgent }

// workbenchInbox is the open Inbox without its noise, what needs the owner first, newest first within.
func (s *Service) workbenchInbox() []InboxItem {
	var open []InboxItem
	for _, it := range s.inboxList(true) {
		if it.DoneAt == nil && it.Tier != TierNoise {
			open = append(open, it)
		}
	}
	slices.SortStableFunc(open, func(a, b InboxItem) int {
		if na, nb := wbNeedsYou(a), wbNeedsYou(b); na != nb {
			if na {
				return -1
			}
			return 1
		}
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	return open
}

// homeCard is the workbench: where messages go (its subtitle), what waits for the owner (each a row that brings its card
// down), the agents at work (each a row that switches the chat to it), the Inbox and the day, the week's tokens, and one
// row of buttons with a "more" menu for every other card and companion bot. said is the card without its "updated at"
// note, to tell a redraw that would change nothing (boardPut).
func (s *Service) homeCard(ctx context.Context, f FeishuFeatures) (card *botcard.Card, fallback, said string) {
	l, web := s.botLang(ctx), s.webBase(ctx)
	loc := s.location(ctx)
	now := s.standing.clock()
	c, busy := s.workbenchCount(ctx, now)
	rows := s.boardRows(ctx, l, web, now)
	title := wbT(l, "home_title")
	target := s.targetLabel(ctx, s.botTargetFor("", ""))
	card = botcard.NewCard(title, botcard.Blue, wbT(l, "home_summary")).Subtitle(wbT(l, "home_target", i18n.Args{"target": clip(target, 40)}))
	if len(rows) > 0 {
		card.Tag(wbT(l, "home_tag_waiting", i18n.Args{"n": len(rows)}), botcard.Orange)
	}
	if c.working > 0 {
		card.Tag(wbT(l, "home_tag_busy", i18n.Args{"n": c.working}), botcard.Blue)
	}
	var fb strings.Builder
	fb.WriteString(title + "\n")

	// What waits for the owner, as the board lists it: a row that holds something up brings its card down here.
	if len(rows) == 0 {
		card.Markdown("<font color='grey'>" + wbT(l, "home_nothing_waits") + "</font>")
	} else {
		card.Markdown("**" + wbT(l, "home_waits_head", i18n.Args{"count": len(rows)}) + "**")
		for i, r := range rows {
			if i == wbHomeRows {
				card.Note(wbT(l, "home_waits_more", i18n.Args{"count": len(rows) - wbHomeRows}))
				break
			}
			line := "**" + botcard.EscapeMarkdown(r.from) + "** · " + botcard.EscapeMarkdown(r.summary) + "\n<font color='grey'>" +
				botcard.EscapeMarkdown(i18n.T(l, "assistant-feishu_cards.board_waited", i18n.Args{"wait": boardWait(l, r.waited)})) + "</font>"
			switch {
			case r.blocking:
				card.Row(map[string]any{"a": "nav", "to": "ix", "id": r.id}, line)
			case boardLinkOK(r.link):
				card.LinkRow(r.link, line)
			default:
				card.Markdown(line)
			}
			fmt.Fprintf(&fb, "- %s · %s\n", r.from, r.summary)
		}
	}

	// The agents at work, each a row that switches the chat to it.
	card.Markdown("**" + wbAgentsLine(l, c) + "**")
	preferTab := s.useHerdrTabName(ctx)
	for i, r := range busy {
		if i == wbHomeRows {
			break
		}
		primary, secondary := feishuSessionNames(r.sess, preferTab)
		label := clip(oneLine(primary), 24)
		card.Row(map[string]any{"a": "switch", "k": targetAgent, "m": r.d.ID, "s": r.sess.SessionID, "t": label + " · " + r.d.Name},
			agentRowMarkdown(l, label, secondary, r.d.Name, r.sess, 70))
	}

	// The Inbox and the day.
	var day []string
	inbox := wbInboxLine(l, c)
	for i, it := range c.inboxTop {
		if i == 2 {
			break
		}
		inbox += "\n· " + wbT(l, "pair", i18n.Args{"a": botcard.EscapeMarkdown(clip(wbSender(it), 10)), "b": botcard.EscapeMarkdown(clip(wbGist(it), 40))})
	}
	day = append(day, inbox)
	if cal := snapshotCalendar(s.connectorSnapshots(ctx), now); cal != nil && len(cal.Events) > 0 {
		e := cal.Events[0]
		day = append(day, wbT(l, "home_next_meeting", i18n.Args{"time": e.Start.In(loc).Format("15:04"), "title": botcard.EscapeMarkdown(clip(e.Title, 30))}))
	}
	day = append(day, wbT(l, "home_machines", i18n.Args{"online": c.online, "count": c.machines}))
	card.Markdown(strings.Join(day, "\n"))
	fb.WriteString(strings.Join(day, "\n"))

	// One row: the Inbox (filled when something there needs the owner), the tasks, and a "more" menu with the rest.
	companions := s.companionBots(ctx)
	var bs []botcard.Button
	inboxBot := slices.IndexFunc(companions, func(b CompanionBot) bool { return b.Kind == CompanionInbox })
	if inboxBot < 0 {
		label, style := wbT(l, "btn_inbox"), botcard.Default
		if c.inboxNeed > 0 {
			label, style = wbT(l, "btn_inbox_count", i18n.Args{"count": c.inboxNeed}), wbFilled
		}
		bs = append(bs, wbNavButton(label, style, "inbox"))
	} else {
		b := companions[inboxBot]
		label := strings.TrimSpace(b.Name)
		if c.inboxNeed > 0 {
			label += " · " + fmt.Sprint(c.inboxNeed)
		}
		btn := botcard.LinkBtn(label, b.link())
		if c.inboxNeed > 0 {
			btn["type"] = wbFilled
		}
		bs = append(bs, btn)
	}
	bs = append(bs, wbNavButton(wbT(l, "btn_tasks"), botcard.Default, "tasks"))
	nav := func(key, to string) botcard.MenuItem {
		return botcard.MenuItem{Label: wbT(l, key), Value: map[string]any{"to": to}}
	}
	menu := []botcard.MenuItem{nav("btn_today", "today")}
	if f.Agents {
		menu = append(menu, nav("btn_agents", "agents"), nav("btn_start", "start"))
	}
	if f.Main {
		menu = append(menu, nav("btn_main", "main"), nav("btn_new", "new"))
	}
	menu = append(menu, nav("btn_refresh", "refresh"))
	for i, b := range companions {
		if i != inboxBot {
			menu = append(menu, botcard.MenuItem{Label: strings.TrimSpace(b.Name), URL: b.link()})
		}
	}
	card.Buttons(append(bs, botcard.Overflow(map[string]any{"a": "nav"}, menu...))...)
	said = card.JSON()
	card.Note(wbT(l, "home_note_pinned", i18n.Args{"time": now.In(loc).Format("15:04")}))
	return card, clip(strings.TrimSpace(fb.String()), reportFallbackMax), said
}

// agentStateTags are the tags an agent's state is shown with in a list: what the owner is to act on orange, at work
// blue, done and not yet read green, the rest grey.
var agentStateTags = map[string]struct{ key, color string }{
	proto.StateNeedsYou: {"assistant-feishu_cards.state_needs_you", botcard.Orange},
	proto.StateWorking:  {"assistant-feishu_cards.state_working", botcard.Blue},
	proto.StateUnread:   {"assistant-feishu_cards.state_unread", botcard.Green},
	proto.StateIdle:     {"assistant-feishu_cards.state_idle", botcard.Neutral},
}

// agentRowMarkdown is an agent as a list row shows it: its state as a tag, its label and computer, the session's other
// name (feishuSessionNames' secondary) under them, and its last words (up to last runes) in grey.
func agentRowMarkdown(l i18n.Lang, label, secondary, machine string, sess proto.Session, last int) string {
	line := "**" + botcard.EscapeMarkdown(label) + "** · " + botcard.EscapeMarkdown(machine)
	if st, ok := agentStateTags[sess.State]; ok {
		line = botcard.InlineTag(i18n.T(l, st.key), st.color) + " " + line
	}
	if secondary != "" {
		line += "\n" + botcard.EscapeMarkdown(secondary)
	}
	if sess.LastText != "" {
		line += "\n<font color='grey'>" + botcard.EscapeMarkdown(clip(oneLine(stripMarkdownMarks(sess.LastText)), last)) + "</font>"
	}
	return line
}

// stripMarkdownMarks is a line without its Markdown marks (an agent's last words on one line, not a renderer).
func stripMarkdownMarks(s string) string {
	r := strings.NewReplacer("```", "", "**", "", "`", "", "## ", "", "# ", "", "> ", "")
	return r.Replace(s)
}

func wbWho(it InboxItem) string {
	switch {
	case it.Sender != "" && it.Title != "" && it.Sender != it.Title:
		return it.Sender + " · " + it.Title
	case it.Sender != "":
		return it.Sender
	}
	return it.Title
}

func wbSender(it InboxItem) string {
	if it.Sender != "" {
		return it.Sender
	}
	return it.Title
}

func wbGist(it InboxItem) string {
	for _, t := range []string{it.Recap, it.Summary, it.Preview} {
		if t = strings.TrimSpace(t); t != "" {
			return t
		}
	}
	return ""
}

// wbSource is where an item came from, in words: a connector's label for it, else its source's name (a subscription's
// one row has no label, its title being the subscription's name, #5671), never an id when there is a name.
func wbSource(l i18n.Lang, it InboxItem) string {
	if it.Connector != nil && it.Connector.Label != "" {
		return it.Connector.Label
	}
	if it.Connector != nil && strings.TrimSpace(it.AccountName) != "" {
		return it.AccountName
	}
	if it.Mail != nil || it.Source == "mail" {
		return wbT(l, "source_mail")
	}
	return it.Source
}

// todayCard is the day at a glance: the main conversation's "Today" (the one the app shows), plus what waits.
func (s *Service) todayCard(ctx context.Context) (*botcard.Card, string) {
	l, loc := s.botLang(ctx), s.location(ctx)
	now := s.standing.clock().In(loc)
	title := wbT(l, "today_title", i18n.Args{"date": now.Format(wbT(l, "date_layout")), "weekday": wbT(l, "weekday_"+now.Weekday().String())})
	card := botcard.NewCard(title, botcard.Blue, title)
	var body todayBody
	if main := s.settings(ctx).Main; main != "" {
		if it, _, err := s.ensureToday(ctx, main); err == nil {
			_ = json.Unmarshal(it.Body, &body)
		}
	}
	c, _ := s.workbenchCount(ctx, now)
	var b strings.Builder
	section := func(head string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "**%s**\n", head)
		for _, ln := range lines {
			fmt.Fprintf(&b, "- %s\n", ln)
		}
		b.WriteString("\n")
	}
	var wait []string
	if c.pending > 0 {
		wait = append(wait, wbT(l, "today_pending", i18n.Args{"count": c.pending}))
	}
	for _, t := range body.Waiting {
		wait = append(wait, botcard.EscapeMarkdown(clip(t.Title, 60)))
	}
	for i, it := range s.workbenchInbox() {
		if i == wbHomeRows || !wbNeedsYou(it) {
			break
		}
		wait = append(wait, wbT(l, "pair", i18n.Args{"a": botcard.EscapeMarkdown(clip(wbWho(it), 16)), "b": botcard.EscapeMarkdown(clip(wbGist(it), 50))}))
	}
	section(wbT(l, "today_waiting"), wait)
	var meet []string
	for _, m := range body.Meetings {
		meet = append(meet, m.Start.In(loc).Format("15:04")+"–"+m.End.In(loc).Format("15:04")+" "+botcard.EscapeMarkdown(clip(m.Title, 40)))
	}
	section(wbT(l, "today_meetings"), meet)
	var done []string
	for _, t := range body.Done {
		done = append(done, botcard.EscapeMarkdown(clip(t.Title, 60)))
	}
	section(wbT(l, "today_done"), done)
	var fail []string
	for _, x := range body.Failed {
		line := botcard.EscapeMarkdown(clip(x.Title, 40))
		if x.Why != "" {
			line = wbT(l, "pair", i18n.Args{"a": line, "b": botcard.EscapeMarkdown(clip(x.Why, 60))})
		}
		fail = append(fail, line)
	}
	section(wbT(l, "today_failed"), fail)
	var at []string
	for _, ch := range body.AtMe {
		at = append(at, botcard.EscapeMarkdown(clip(ch.Name, 20))+" · "+botcard.EscapeMarkdown(clip(ch.Preview, 40)))
	}
	section(wbT(l, "today_at_me"), at)
	quiet := strings.TrimSpace(b.String()) == ""
	fmt.Fprintf(&b, "**%s**\n", wbT(l, "today_now"))
	fmt.Fprintf(&b, "%s\n", wbAgentsLine(l, c))
	fmt.Fprintf(&b, "%s\n", wbInboxLine(l, c))
	if body.Working > 0 {
		fmt.Fprintf(&b, "%s\n", wbT(l, "today_working", i18n.Args{"count": body.Working}))
	}
	if quiet {
		b.WriteString("\n" + wbT(l, "today_quiet"))
	}
	if c.pending > 0 {
		card.Tag(wbT(l, "home_tag_waiting", i18n.Args{"n": c.pending}), botcard.Orange)
	}
	card.Markdown(strings.TrimSpace(b.String()))
	inbox := wbNavButton(wbT(l, "btn_inbox"), botcard.Default, "inbox")
	if c.inboxNeed > 0 {
		inbox = wbNavButton(wbT(l, "btn_inbox_count", i18n.Args{"count": c.inboxNeed}), wbFilled, "inbox")
	}
	card.Buttons(inbox, wbNavButton(wbT(l, "btn_tasks"), botcard.Default, "tasks"),
		botcard.TextBtn(wbT(l, "btn_home"), map[string]any{"a": "nav", "to": "home"}))
	return card, title + "\n\n" + strings.TrimSpace(b.String())
}

// inboxCard lists the open Inbox with a row of actions per item.
func (s *Service) inboxCard(ctx context.Context) (*botcard.Card, string) {
	l := s.botLang(ctx)
	open := s.workbenchInbox()
	need := 0
	for _, it := range open {
		if wbNeedsYou(it) {
			need++
		}
	}
	title := wbT(l, "inbox_title", i18n.Args{"count": need})
	color := botcard.Blue
	if need > 0 {
		color = botcard.Orange
	}
	card := botcard.NewCard(title, color, title)
	var fb strings.Builder
	fb.WriteString(title + "\n")
	if len(open) == 0 {
		card.Markdown(wbT(l, "inbox_empty"))
		return card.Buttons(botcard.TextBtn(wbT(l, "btn_home"), map[string]any{"a": "nav", "to": "home"})), title
	}
	for i, it := range open {
		if i == wbInboxMost {
			card.Note(wbT(l, "inbox_more", i18n.Args{"count": len(open) - wbInboxMost}))
			break
		}
		state, color := wbT(l, "inbox_tag_need"), botcard.Orange
		if it.Urgent {
			state, color = wbT(l, "inbox_tag_urgent"), botcard.Red
		} else if !wbNeedsYou(it) {
			state, color = wbT(l, "inbox_tag_fyi"), botcard.Neutral
		}
		head := botcard.InlineTag(state, color) + " **" + botcard.EscapeMarkdown(clip(wbSender(it), 16)) + "**"
		if it.Sender != "" && it.Title != "" && it.Title != it.Sender {
			head += " · " + botcard.EscapeMarkdown(clip(it.Title, 16))
		}
		line := head
		if g := wbGist(it); g != "" {
			line += "\n" + botcard.EscapeMarkdown(clip(g, 120))
		}
		line += "\n<font color='grey'>" + botcard.EscapeMarkdown(wbSource(l, it)) + " · " + wbClock(s.location(ctx), it.UpdatedAt, s.standing.clock()) + "</font>"
		if i > 0 {
			card.HR()
		}
		card.Markdown(line)
		// One row: drafting a reply first where the item can be answered, then done, muting the sender in "more".
		var btns []botcard.Button
		if inboxCanReply(it) {
			btns = append(btns, botcard.Btn(wbT(l, "btn_draft"), botcard.Primary, map[string]any{"a": "ib", "op": "draft", "id": it.ID}))
		}
		btns = append(btns, botcard.Btn(wbT(l, "btn_done"), botcard.Default, map[string]any{"a": "ib", "op": "done", "id": it.ID}))
		if len(it.Mutes) > 0 {
			btns = append(btns, botcard.Overflow(map[string]any{"a": "ib", "id": it.ID},
				botcard.MenuItem{Label: wbT(l, "btn_mute"), Value: map[string]any{"op": "mute"}}))
		}
		card.Buttons(btns...)
		fmt.Fprintf(&fb, "- %s\n", wbT(l, "pair", i18n.Args{"a": wbWho(it), "b": clip(wbGist(it), 60)}))
	}
	card.Note(wbT(l, "inbox_note"))
	return card, strings.TrimSpace(fb.String())
}

// inboxCanReply: a reply can be sent to the item's source as the owner.
func inboxCanReply(it InboxItem) bool {
	if it.Connector != nil {
		return it.Connector.CanReply
	}
	return it.Lark != nil || it.Mail != nil
}

// workbenchItem is the item a card's button names, as listed, while it is open or done within inboxKeepDone (the list
// carries only the newest of those, inbox_list_done.go, so it is looked up by id rather than in a list).
func (s *Service) workbenchItem(id string) (InboxItem, bool) {
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	if !ok || it.DoneAt != nil && it.DoneAt.Before(time.Now().Add(-inboxKeepDone)) {
		return InboxItem{}, false
	}
	return it.listed(), true
}

// workbenchInboxAction answers an Inbox button ({a: ib, op, id}): done and mute change the list card in place;
// drafting posts a draft card (the model takes longer than the callback may), and sending it updates that card. The
// hub checked the owner pressed it; sending is the same reply the app's Send makes, once per draft card.
func (s *Service) workbenchInboxAction(ctx context.Context, a *botcard.CardAction) *botcard.CardResponse {
	l := s.botLang(ctx)
	if f, _ := s.feishuFeatures(ctx); !f.Workbench {
		return toast("info", i18n.T(l, "assistant-feishu_cards.card_expired"))
	}
	id, op, mid := a.Str("id"), a.Str("op"), a.Context.MessageID
	it, ok := s.workbenchItem(id)
	if !ok {
		return toast("info", wbT(l, "inbox_gone"))
	}
	refresh := func(msg string) *botcard.CardResponse {
		card, _ := s.inboxCard(ctx)
		return &botcard.CardResponse{Toast: &botcard.Toast{Type: "success", Content: msg}, Card: card.Raw()}
	}
	switch op {
	case "done":
		if _, err := s.setInboxDone(ctx, id, true, false); err != nil {
			return toast("error", clip(oneLine(err.Error()), 120))
		}
		return refresh(wbT(l, "done_ok"))
	case "mute":
		if len(it.Mutes) == 0 {
			return toast("info", wbT(l, "mute_none"))
		}
		m := it.Mutes[0]
		for _, x := range it.Mutes {
			if x.Tag == "sender" {
				m = x
			}
		}
		if _, _, err := s.addMute(ctx, InboxMute{Source: it.Source, Tag: m.Tag, Value: m.Value, Name: m.Name}); err != nil {
			return toast("error", clip(oneLine(err.Error()), 120))
		}
		_, _ = s.setInboxDone(ctx, id, true, true)
		return refresh(wbT(l, "mute_ok", i18n.Args{"name": clip(m.Name, 20)}))
	case "draft", "redraft":
		instruction := strings.TrimSpace(a.Form("instruction"))
		current := strings.TrimSpace(a.Form("text"))
		if op == "redraft" && current != "" {
			instruction = strings.TrimSpace(wbT(l, "redraft_prompt", i18n.Args{"draft": current}) + "\n" + instruction)
		}
		list := a.Str("list")
		if op == "draft" {
			list = mid // pressed on the Inbox card: it is redrawn once the reply goes out
		}
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			text, err := s.draftReply(cctx, id, instruction)
			card := s.draftCard(cctx, it, text, err, list)
			if op == "redraft" {
				if err := s.botUpdateCard(cctx, mid, card); err == nil {
					return
				}
			}
			if _, err := s.botPost(cctx, mid, card, ""); err != nil {
				s.logf("bots: draft card: %v", err)
			}
		}()
		return toast("info", wbT(l, "drafting"))
	case "send":
		text := strings.TrimSpace(a.Form("text"))
		if text == "" {
			return toast("warning", wbT(l, "send_empty"))
		}
		if it.DoneAt != nil {
			return toast("info", wbT(l, "inbox_gone"))
		}
		list := a.Str("list")
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			// One reply per draft card however many presses: the card's message is the send's id, and a press that
			// comes after the reply filed the item changes nothing (the first one drew the card).
			_, err := s.replyInboxFrom(cctx, id, text, "feishu-workbench-"+mid, true)
			if errors.Is(err, errInboxItemDone) {
				return
			}
			// Sent, the draft is over: grey, what went out.
			card := botcard.NewCard(wbT(l, "sent_title", i18n.Args{"who": clip(wbWho(it), 20)}), botcard.Grey, wbT(l, "sent_title", i18n.Args{"who": wbWho(it)})).
				Markdown(botcard.EscapeMarkdown(text))
			if err != nil {
				card = botcard.NewCard(wbT(l, "send_failed_title"), botcard.Red, wbT(l, "send_failed_title")).Markdown(botcard.EscapeMarkdown(clip(oneLine(err.Error()), 200)))
			}
			if err := s.botUpdateCard(cctx, mid, card); err != nil {
				s.logf("bots: sent card: %v", err)
			}
			if list != "" && err == nil {
				listCard, _ := s.inboxCard(cctx)
				_ = s.botUpdateCard(cctx, list, listCard)
			}
		}()
		return toast("info", wbT(l, "sending"))
	}
	return toast("info", i18n.T(l, "assistant-feishu_cards.card_expired"))
}

// draftCard is a draft to edit and send as the owner; list is the Inbox card to redraw once it is sent.
func (s *Service) draftCard(ctx context.Context, it InboxItem, text string, err error, list string) *botcard.Card {
	l := s.botLang(ctx)
	title := wbT(l, "draft_title", i18n.Args{"who": clip(wbWho(it), 20)})
	if err != nil || strings.TrimSpace(text) == "" {
		why := wbT(l, "draft_failed")
		if err != nil {
			why = wbT(l, "pair", i18n.Args{"a": why, "b": clip(oneLine(err.Error()), 200)})
		}
		return botcard.NewCard(title, botcard.Red, title).Markdown(botcard.EscapeMarkdown(why)).
			Buttons(botcard.Btn(wbT(l, "btn_draft"), botcard.Primary, map[string]any{"a": "ib", "op": "draft", "id": it.ID, "list": list}))
	}
	card := botcard.NewCard(title, botcard.Orange, title)
	if p := strings.TrimSpace(it.Preview); p != "" {
		card.Note(wbT(l, "draft_original", i18n.Args{"text": botcard.EscapeMarkdown(clip(p, 160))}))
	}
	box := botcard.Input("text", wbT(l, "draft_text"), "", true)
	box["default_value"] = text
	instr := botcard.Input("instruction", wbT(l, "draft_instruction"), wbT(l, "draft_instruction_placeholder"), false)
	// Sending goes out under the owner's name: Feishu's dialog asks first.
	card.Form("draft", []any{box, instr},
		botcard.Confirm(botcard.Submit(wbT(l, "btn_send"), wbFilled, "send_submit", map[string]any{"a": "ib", "op": "send", "id": it.ID, "list": list}),
			wbT(l, "send_confirm_title"), wbT(l, "send_confirm_text", i18n.Args{"who": clip(wbWho(it), 20)})),
		botcard.Submit(wbT(l, "btn_redraft"), botcard.Default, "redraft_submit", map[string]any{"a": "ib", "op": "redraft", "id": it.ID, "list": list}))
	reply := wbT(l, "draft_as_you")
	if it.Connector != nil && it.Connector.ReplyTo != "" {
		reply = it.Connector.ReplyTo
	}
	card.Note(botcard.EscapeMarkdown(reply))
	return card
}

// wbModeSelect is the /start form's permission field: one of proto.PermissionModes, or none (the launcher's own).
func wbModeSelect(l i18n.Lang) map[string]any {
	opts := make([]botcard.Option, 0, len(proto.PermissionModes))
	for _, m := range proto.PermissionModes {
		opts = append(opts, botcard.Option{Label: wbT(l, "mode_"+m), Value: m})
	}
	return botcard.Select("mode", wbT(l, "mode_placeholder"), opts, false)
}

// wbClock is when, as a time that stays true on a card nobody redraws: the clock today, the date before.
func wbClock(loc *time.Location, t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t, now = t.In(loc), now.In(loc)
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// wbAgentsLine says what the agents do, leaving out the states nobody is in.
func wbAgentsLine(l i18n.Lang, c wbCounts) string {
	var parts []string
	for _, p := range []struct {
		n   int
		key string
	}{{c.working, "agents_working"}, {c.unread, "agents_unread"}, {c.idle, "agents_idle"}} {
		if p.n > 0 {
			parts = append(parts, wbT(l, p.key, i18n.Args{"count": p.n}))
		}
	}
	if len(parts) == 0 {
		return wbT(l, "agents_none")
	}
	return wbT(l, "agents_head") + strings.Join(parts, " · ")
}

func wbInboxLine(l i18n.Lang, c wbCounts) string {
	switch {
	case c.inboxNeed == 0 && c.inboxFYI == 0:
		return wbT(l, "inbox_clear")
	case c.inboxFYI == 0:
		return wbT(l, "inbox_need", i18n.Args{"count": c.inboxNeed})
	case c.inboxNeed == 0:
		return wbT(l, "inbox_fyi", i18n.Args{"count": c.inboxFYI})
	}
	return wbT(l, "inbox_need", i18n.Args{"count": c.inboxNeed}) + " · " + wbT(l, "inbox_fyi_short", i18n.Args{"count": c.inboxFYI})
}
