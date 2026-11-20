package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// --- pushes and the sync mirror ---

// feishuPush sends what notify_user pushed to the phone to Feishu too (the pushes switch).
func (s *Service) feishuPush(machine, session, title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // as feishuMirror: a push may be several cards
	defer cancel()
	_ = s.feishuPushKeyed(ctx, machine, session, title, body, "")
}

// feishuPushKeyed is feishuPush within ctx, for a push that may be sent again when key is set (card i goes with key
// and i, so one that landed lands once): an error when it may yet get out and did not (the bot's status not read
// while the hub is away, a card Feishu did not take for now), nil when it went or has nowhere to go (no bot, the
// pushes switch off, a card Feishu refuses however often it is sent).
func (s *Service) feishuPushKeyed(ctx context.Context, machine, session, title, body, key string) error {
	return s.feishuPushTold(ctx, machine, session, title, body, key, nil)
}

// feishuPushTold is feishuPushKeyed telling r whether the push goes to the bot at all and, when it does, whether its
// first card landed (notify_user's answer, channels_copies.go).
func (s *Service) feishuPushTold(ctx context.Context, machine, session, title, body, key string, r *copyReport) error {
	return s.feishuPushToldTodo(ctx, machine, session, title, body, key, r, nil)
}

// feishuTodoReminder sends a reminder in its own bounded background context, as feishuPush does for notify_user.
func (s *Service) feishuTodoReminder(machine, session, title, body string, todo Todo) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.feishuTodoPush(ctx, machine, session, title, body, todo)
}

// feishuTodoPush is a to-do reminder's Feishu copy. Its body has the complete stored notes (answerCards still applies
// the card budget), its button opens that to-do, and a reply carries its id back into the same main conversation.
func (s *Service) feishuTodoPush(ctx context.Context, machine, session, title, body string, todo Todo) error {
	return s.feishuPushToldTodo(ctx, machine, session, title, body, "", nil, &todo)
}

// feishuPushToldTodo is feishuPushTold, with a to-do only on the reminder path. Ordinary notify_user passes nil and
// keeps its conversation link, context-free reply routing and body exactly as before.
func (s *Service) feishuPushToldTodo(ctx context.Context, machine, session, title, body, key string, r *copyReport, todo *Todo) error {
	private := todo != nil && s.todoInHealth(ctx, *todo)
	if private {
		title, body = healthNotice(s.botLang(ctx))
	}
	st, err := s.feishuStatus(ctx)
	ready := err == nil && st.Ready
	s.workbenchOn(st.Features, ready) // as feishuFeatures
	if err != nil && !answered(err) {
		r.going()
		return err
	}
	if !ready || !st.Features.Pushes {
		r.going()
		return nil
	}
	r.going(pushCopy{channel: channel.Feishu})
	defer r.went(channel.Feishu, errNoCopy) // nothing landed when it returns without saying so
	// The body laid out like the bot's answers (answerCards: sections, tables, link buttons, more cards when long, at
	// most botAnswerCards); the push's title heads the first card, the open button goes under the last.
	l := s.botLang(ctx)
	ctx = feishuSendAs(ctx, "push", machine, session)
	cards := answerCards(l, s.linkNamer(ctx), body, false, nil, "")
	head := feishubot.NewCard("", "", title)
	for i, ac := range cards {
		if i == 0 {
			// The push's title heads the card, blue (only to know), its kind as a tag; the body's own heading and status
			// ("Please verify" / "No changes") are kept (Retitle).
			ac.card.Retitle("", title, feishubot.Blue)
			ac.card.Config = head.Config
			kind := "assistant-feishu_cards.tag_notice"
			if todo != nil {
				kind = "assistant-feishu_cards.tag_todo"
			}
			ac.card.Tag(i18n.T(l, kind), feishubot.Neutral)
		}
		if i == len(cards)-1 && session != "" {
			if todo != nil {
				web := todoLink(strings.TrimRight(st.PublicURL, "/"), todo.ID)
				ac.card.AppendToLinks(s.openLinkButton(ctx, web))
			} else {
				ac.card.AppendToLinks(s.openButton(ctx, machine, session))
			}
		}
	}
	if s.divertAnswer(ctx, "push", cards) { // a notifications companion takes it (feishu_companions.go)
		s.markPushed(channel.Feishu, machine, session)
		r.went(channel.Feishu, nil)
		return nil
	}
	var missing []int // the pieces that did not get out (1-based): said at the end
	var again error   // the first that may get out when sent again
	for i, ac := range cards {
		k := ""
		if key != "" {
			k = key + strconv.Itoa(i)
		}
		mid, err := s.botPostKeyed(ctx, "", ac.card, ac.fallback, k)
		if err != nil {
			s.logf("bots: push: %v", err)
			if again == nil && !feishubot.Refused(err) {
				again = err
			}
			if i == 0 {
				r.went(channel.Feishu, err)
				return again // nothing went out: do not mark pushed, so the turn-end mirror delivers the turn
			}
			missing = append(missing, i+1)
			continue
		}
		if i == 0 {
			s.markPushed(channel.Feishu, machine, session) // only after the first card lands: a failed push must not suppress the mirror
			r.went(channel.Feishu, nil)
		}
		if session != "" {
			target := botTarget{Kind: targetPersona, MachineID: machine, SessionID: session}
			if todo != nil && !private {
				target.TodoID = todo.ID
			}
			s.rememberBotThread(mid, target, title)
		}
	}
	if len(missing) > 0 && (key == "" || again == nil) { // one sent again says nothing of the pieces it will send
		s.botSay(ctx, missingPiecesNote(l, missing))
	}
	return again
}

// feishuMirror sends a turn that ended without being asked for in Feishu: the main conversation's when sync is on
// (what was said in the app, a scheduled task's run), a scheduled task's own conversation's when pushes is on.
func (s *Service) feishuMirror(ctx context.Context, machine, session string) {
	if s.justMeNow(machine, session) {
		return // a Just me turn stays off Feishu (meeting_justme.go)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, ready := s.feishuFeatures(ctx)
	if !ready || !(f.Sync || f.Pushes) {
		return
	}
	main := s.isMainOn(ctx, machine, session)
	task := s.taskOfConversation(ctx, machine, session)
	switch {
	case main && f.Sync:
	case task != "" && f.Pushes:
	default:
		return
	}
	if s.pushedLately(channel.Feishu, machine, session) && f.Pushes {
		return // its notify_user already went to Feishu
	}
	sess, ok := s.session(ctx, machine, session)
	if !ok || sess.WokeByTask && !main || spaceOf(sess) == SpaceHealth { // a health task's runs stay off Feishu (health.go)
		return
	}
	if s.transientRetryTurn(ctx, sess) {
		return
	}
	asked, answer, turn, ok := s.channelLastTurnOf(ctx, machine, session)
	if !ok {
		return
	}
	if strings.Contains(asked, "title=\"飞书机器人\"") { // the context tag of the bot's messages (prompts/channels), not text for people
		return // its own message: answered by the run
	}
	l := s.botLang(ctx)
	label := i18n.T(l, "assistant-feishu_cards.main_chat")
	if !main {
		label = i18n.T(l, "assistant-feishu_cards.scheduled_task", i18n.Args{"task": task})
		cut := sess.TurnError != "" || cutOff(answer)
		if s.briefFailure(session, asked, cut) {
			return // watchScheduledRun sends the persisted one-per-occurrence facts fallback instead
		}
		run, claimed := s.briefClaim(session, asked, turn, cut)
		if claimed {
			return // the same retained turn already owns a briefing card, including its pending retries
		}
		if run != nil { // a routine with facts: fixed cards Persona lays out (briefing.go)
			s.briefMirror(ctx, machine, session, label, run, answer, turn)
			return
		}
	}
	// The whole answer, laid out like the bot's own answers (answerCards: header, tables, link buttons, at most
	// botAnswerCards cards, a note when cut short); where it came from and the button to open it under the last.
	foot := label
	if asked != "" && main {
		foot = i18n.T(l, "assistant-feishu_cards.asked_elsewhere", i18n.Args{"asked": clip(oneLine(stripContext(asked)), 80)}) + "\n" + label
	}
	cards := answerCards(l, s.linkNamer(ctx), answer, false, nil, foot)
	if len(cards) > 0 {
		cards[len(cards)-1].card.AppendToLinks(s.openButton(ctx, machine, session))
	}
	// The turn is in the key: two runs an hour apart that asked and answered alike (a task's "no change") are two
	// sends, not one Feishu drops as a repeat.
	sum := sha256.Sum256([]byte(machine + "\x00" + session + "\x00" + turn + "\x00" + asked + "\x00" + answer))
	m := &mirrorSend{l: l, cards: cards, target: botTarget{Kind: targetPersona, MachineID: machine, SessionID: session}, label: label,
		main: main, answer: answer, turn: turn, key: "m" + hex.EncodeToString(sum[:8])}
	s.feishuMirrorPost(ctx, m, 0)
}

// mirrorSend is one mirrored answer, as it is sent again when its first card did not get out.
type mirrorSend struct {
	l      i18n.Lang
	cards  []botAnswerCard
	target botTarget
	label  string
	main   bool   // the main conversation's (the sync switch), else a scheduled task's own (pushes)
	answer string // the turn's answer: a retry goes only while it is still the conversation's last
	turn   string // the turn's last item: with answer, what tells it from a later turn that answered alike
	key    string // the answer's key: card i is sent with key + i, the same on every try
	sent   func() // when set, called once the first card is out
}

// mirrorStillDue: a retry of m may go now: the switch its conversation needs is on, its notify_user did not go to Feishu
// meanwhile, and its turn is still the conversation's last (an old morning brief must not land under a newer answer).
func (s *Service) mirrorStillDue(ctx context.Context, m *mirrorSend) (bool, error) {
	st, err := s.feishuStatus(ctx)
	ready, f := err == nil && st.Ready, st.Features
	s.workbenchOn(f, ready)
	if err != nil && !answered(err) {
		return false, err // unknown readiness is temporary, not evidence that the owner switched the channel off
	}
	switch {
	case !ready, m.main && !f.Sync, !m.main && !f.Pushes:
		return false, nil
	case f.Pushes && s.pushedLately(channel.Feishu, m.target.MachineID, m.target.SessionID):
		return false, nil
	}
	_, answer, turn, ok := s.channelLastTurnOf(ctx, m.target.MachineID, m.target.SessionID)
	return ok && answer == m.answer && turn == m.turn, nil
}

// feishuMirrorRetry is how long a mirrored answer whose first card did not get out waits before it is sent again, and
// feishuMirrorRetries how many times: the mirror is the last way a morning brief reaches Feishu, so one error must not lose it.
var (
	feishuMirrorRetry   = 30 * time.Second
	feishuMirrorRetries = 3
)

// feishuMirrorPost sends a mirrored answer's cards, each with its own key that is the same on every try (Feishu lands a
// key once: a card that went out although its reply was lost is not shown twice). When the first does not get out,
// nothing did: the same cards go again later, while mirrorStillDue, up to feishuMirrorRetries times; not when Feishu
// refused it for good. Later ones that fail are named at the end.
func (s *Service) feishuMirrorPost(ctx context.Context, m *mirrorSend, attempt int) {
	kind := "task"
	if m.main {
		kind = "sync"
	}
	ctx = feishuSendAs(ctx, kind, m.target.MachineID, m.target.SessionID)
	// A scheduled task's result goes to a notifications companion when one takes the notices (feishu_companions.go);
	// the main conversation's turns stay in the chat, where a reply continues them.
	if !m.main && s.divertAnswer(ctx, "task", m.cards) {
		if m.sent != nil {
			m.sent()
		}
		return
	}
	var missing []int // the pieces that did not get out (1-based): said at the end
	for i, ac := range m.cards {
		mid, err := s.botPostKeyed(ctx, "", ac.card, ac.fallback, m.key+strconv.Itoa(i))
		if err != nil {
			s.logf("bots: mirror: %v", err)
			if i == 0 {
				// Nothing went out: the rest would read as an answer without its beginning. Tried again later.
				if feishubot.Refused(err) || attempt >= feishuMirrorRetries {
					s.logf("bots: mirror: given up after %d tries", attempt+1)
					return
				}
				s.retryFeishuMirror(m, attempt+1)
				return
			}
			missing = append(missing, i+1)
			continue
		}
		s.rememberBotThread(mid, m.target, m.label) // a reply to any of them goes to this conversation
		if i == 0 && m.sent != nil {
			m.sent()
		}
	}
	if len(missing) > 0 {
		s.botSay(ctx, missingPiecesNote(m.l, missing))
	}
}

// retryFeishuMirror consumes the same bounded retry slots for status transport failures as for failed posts. Unknown
// readiness never authorizes a post; a later slot must confirm the switches and original turn again.
func (s *Service) retryFeishuMirror(m *mirrorSend, attempt int) {
	time.AfterFunc(feishuMirrorRetry, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		due, err := s.mirrorStillDue(ctx, m)
		if err != nil {
			s.logf("bots: mirror retry status: %v", err)
			if attempt < feishuMirrorRetries {
				s.retryFeishuMirror(m, attempt+1)
			}
			return
		}
		if due {
			s.feishuMirrorPost(ctx, m, attempt)
		}
	})
}

// agentDoneClip bounds the result text in a background agent's finished card; the rest is read in the app, so the
// notice is always one card. agentDoneLead is about how much of it shows open, the rest folded under it.
const (
	agentDoneClip = 1000
	agentDoneLead = 300
)

// agentDoneState is a finished turn's card as its push decided it (doneAlert): its title without the push's mark, the
// header's colour and the tag that says the state — failed or cut off red, blocked or asking the owner orange, done
// green.
func agentDoneState(head string, sess proto.Session) (title, color, tag string) {
	switch {
	case strings.HasPrefix(head, "⚠️"):
		return strings.TrimSpace(strings.TrimPrefix(head, "⚠️")), feishubot.Red, "assistant-feishu_cards.agent_tag_failed"
	case strings.HasPrefix(head, "⏳"):
		return strings.TrimSpace(strings.TrimPrefix(head, "⏳")), feishubot.Orange, "assistant-feishu_cards.agent_tag_blocked"
	case sess.AwaitsReply:
		return head, feishubot.Orange, "assistant-feishu_cards.agent_tag_asks"
	}
	return head, feishubot.Green, "assistant-feishu_cards.agent_tag_done"
}

// agentDoneHead is the finished card's head for an agent that works on a task: the task's direction and title first,
// then the agent's own title when it is not the task's; the push's mark (⚠️, ⏳) stays in front, where agentDoneState
// reads the state from it.
func (s *Service) agentDoneHead(ctx context.Context, machine, session, head string) string {
	t, ok := s.taskOf(ctx, s.canonical(machine), session)
	if !ok {
		return head
	}
	mark := ""
	for _, m := range []string{"⚠️", "⏳"} {
		if rest, found := strings.CutPrefix(head, m); found {
			mark, head = m+" ", strings.TrimSpace(rest)
			break
		}
	}
	label := s.feishuDirection(ctx, t, s.feishuDirections(ctx)) + clip(oneLine(t.Title), 70)
	if head != t.Title {
		label += " · " + head
	}
	return mark + label
}

// leadAndRest cuts Markdown at the first paragraph break past n runes outside code: what shows open and what is folded
// ("" when there is no such break).
func leadAndRest(md string, n int) (lead, rest string) {
	lines := strings.Split(md, "\n")
	runes, fence := 0, ""
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
		} else if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = t[:3]
		} else if t == "" && runes >= n && i < len(lines)-1 {
			if r := strings.TrimSpace(strings.Join(lines[i+1:], "\n")); r != "" {
				return strings.TrimSpace(strings.Join(lines[:i], "\n")), r
			}
		}
		runes += len([]rune(line))
	}
	return md, ""
}

// feishuAgentDone posts the one Feishu card for a background agent's finished turn (completed, failed or cut off)
// that notifyFinished already decided the phone pushes (#4674). Riding that decision keeps it in step with the push:
// seen, muted, a recent notify_user, a turn asked from the bot, an inbox agent's, a quiet task's, a supervision turn's
// and the turn of a supervised task's Agent (#8270) are all dropped before this is called. The sender still leaves out
// the conversations that have their own Feishu path: the main conversation (the sync mirror), a scheduled task's own
// conversation (its pushes mirror) and health, and posts nothing without the pushes switch.
func (s *Service) feishuAgentDone(machine string, sess proto.Session, head, text, place string) {
	ctx, cancel := context.WithTimeout(feishuSendAs(context.Background(), "agent_done", machine, sess.SessionID), 30*time.Second)
	defer cancel()
	f, ready := s.feishuFeatures(ctx)
	if !ready || !f.Pushes {
		return
	}
	session := sess.SessionID
	if sess.Assistant || s.mainSessionRep(machine, session) || s.taskSessionRep(machine, session) ||
		spaceOf(sess) == SpaceHealth {
		return
	}
	l := s.botLang(ctx)
	preferTab := s.useHerdrTabName(ctx)
	label, secondary := feishuSessionNames(sess, preferTab)
	if tab := sess.DisplayTabName(preferTab); tab != "" {
		head = strings.Replace(head, pushTitle(sess), clip(oneLine(tab), 70), 1)
	}
	head = s.agentDoneHead(ctx, machine, session, head)
	card, fallback := agentDoneCard(l, head, text, place, secondary, sess, s.openButton(ctx, machine, session))
	mid, err := s.botPost(ctx, "", card, fallback)
	if err != nil {
		s.logf("bots: agent done: %v", err)
		return
	}
	// A reply to the card talks to that agent, as with one picked with /agents.
	s.rememberBotThread(mid, botTarget{Kind: targetAgent, MachineID: machine, SessionID: session},
		label+" · "+s.machineName(machine))
}

// agentDoneCard is a background agent's finished card and its plain-text fallback: the push's head and text, the
// place it ran, the session's other name (feishuSessionNames' secondary, "" for none), and open (the conversation's
// button, nil without a web address) as its one button.
func agentDoneCard(l i18n.Lang, head, text, place, secondary string, sess proto.Session, open feishubot.Button) (*feishubot.Card, string) {
	title, color, tag := agentDoneState(head, sess)
	note := func(clipped bool) string {
		n := i18n.T(l, "assistant-feishu_cards.agent_done_note", i18n.Args{"place": place})
		if secondary != "" {
			n = secondary + "\n" + n
		}
		if clipped {
			n = i18n.T(l, "assistant-feishu_cards.only_beginning") + "\n" + n
		}
		return n
	}
	limit := agentDoneClip
	hint := i18n.T(l, "assistant-feishu_cards.agent_done_reply_hint")
	if open != nil {
		open["type"] = feishubot.Primary // the one thing to do with it, besides replying
	}
	var (
		card    *feishubot.Card
		body    string
		clipped bool
	)
	for { // a chart-laden answer can render wide: halve the body until the card fits Feishu's limit
		body = clip(text, limit)
		clipped = len([]rune(text)) > limit
		lead, rest := leadAndRest(body, agentDoneLead)
		card = feishubot.NewCard(title, color, title).Subtitle(dotJoin(i18n.T(l, "assistant-feishu_cards.agent_done_note", i18n.Args{"place": place}), secondary)).
			Tag(i18n.T(l, tag), color).Markdown(feishubot.CardMarkdown(lead)).
			Panel(i18n.T(l, "assistant-feishu_cards.agent_done_more"), false, feishubot.CardMarkdown(rest))
		meta := []string{feishubot.EscapeMarkdown(hint)}
		if clipped {
			meta = append([]string{feishubot.EscapeMarkdown(i18n.T(l, "assistant-feishu_cards.only_beginning"))}, meta...)
		}
		card.Meta(meta...).Buttons(open)
		if len(card.JSON()) <= answerCardMaxBytes || limit <= agentDoneClip/8 {
			break
		}
		limit /= 2
	}
	fallback := fallbackText.Replace(head) + "\n\n" + fallbackText.Replace(body) + "\n" + fallbackText.Replace(note(clipped)) +
		"\n" + fallbackText.Replace(hint)
	return card, strings.TrimSpace(fallback)
}
