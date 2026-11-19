package assistant

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Opening a to-do from a topic (#5279, docs/assistant/todos.md "Opening a to-do from a topic"): in a group chat the hub
// registered, a topic the owner starts opens a to-do bound to it and hands the first words to the main conversation,
// the way a new topic starts a session in a bot that auto-starts on one. In a topic that exists but is bound to nothing,
// "/ai" followed by a line does the same.

// openNotesMax bounds the first words kept as the to-do's notes, in characters.
const openNotesMax = 4000

// isTopicRoot says whether ev is the message that starts its topic: it is in a topic (it has a thread_id) and either
// replies to nothing (no root_id) or is its own root (root_id is its own message id). Checked with the message API on
// a real topic group, not with the event push (the two carry the same fields): the first message has an empty
// root_id and parent_id and a thread_id, a reply has both set. The second form is kept as a second accepted shape.
// A wrong guess either opens nothing (the owner sends "/ai") or opens a to-do for a reply.
func isTopicRoot(ev feishubot.MessageEvent) bool {
	m := ev.Message
	return m.ThreadID != "" && (m.RootID == "" || m.RootID == m.MessageID)
}

// aiCommand is the line after "/ai" (or the full-width "／ai", whatever its case) when text starts with it as a word of
// its own ("/ai fix it", "/ai", but not "/aim"); ok is false for any other text.
func aiCommand(text string) (rest string, ok bool) { return feishubot.AICommand(text) }

// openingWords are the owner's words in ev as an opening reads them: only a text or a rich-text message has any (a
// voice message, a shared group or card, a location, a card, a file and a message of a type not known come as a
// placeholder like "[语音消息]", which are no words); the bot's @ at the start is taken off (in a group where the bot
// only gets what @s it, every message starts with one; content has it as the name, the message's own text as
// @_user_N), and an @ further in is the person's name when the message's mentions give it, else left as it stands.
// A rich-text message of only pictures or videos has no words either. "" when there are none.
func openingWords(ev feishubot.MessageEvent, content feishubot.Content) string {
	m := ev.Message
	var text string
	switch m.MessageType {
	case "text":
		text = feishubot.ParseContent(m.MessageType, m.Content, nil).Text // with the @_user_N keys still in it
	case "post":
		text = content.Text
	default:
		return ""
	}
	text = strings.TrimSpace(text)
	for cut := true; cut; {
		cut = false
		for _, mn := range m.Mentions {
			for _, tok := range []string{mn.Key, "@" + mn.Name} {
				if tok == "" || tok == "@" {
					continue
				}
				// A whole word: "@_user_1" is not the start of "@_user_10".
				if rest, ok := strings.CutPrefix(text, tok); ok && (rest == "" || unicode.IsSpace([]rune(rest)[0])) {
					text, cut = strings.TrimSpace(rest), true
				}
			}
		}
	}
	for _, mn := range m.Mentions {
		if mn.Key != "" && mn.Name != "" {
			// A whole key: "@_user_1" is not the start of "@_user_10".
			text = regexp.MustCompile(regexp.QuoteMeta(mn.Key)+`\b`).ReplaceAllLiteralString(text, "@"+mn.Name)
		}
	}
	// The placeholders feishubot.ParseContent writes for a picture and a video in rich text.
	if strings.TrimSpace(strings.NewReplacer("[图片]", "", "[视频]", "").Replace(text)) == "" { // i18n-ignore: matched against the text parsePost writes
		return ""
	}
	return text
}

// openTopicIfAsked opens a to-do for the message when it asks for one: it starts a topic that is bound to nothing, or
// it is "/ai …" in such a topic. It says whether it took the message (everything the owner is to be told is said).
func (s *Service) openTopicIfAsked(ctx context.Context, ev feishubot.MessageEvent, st FeishuStatus, content feishubot.Content, topic *feishuTopic) bool {
	text := openingWords(ev, content)
	rest, asked := aiCommand(text)
	if !asked && !isTopicRoot(ev) {
		return false
	}
	if asked {
		text = rest
	}
	s.openTopicTodo(ctx, ev, st, content, text, topic)
	return true
}

// openTopicTodo makes the to-do for the topic ev is in, with text (the first words, the command taken off) as its
// notes, and hands the words to the main conversation (openTodoIn); what keeps it from doing so is said in the topic.
func (s *Service) openTopicTodo(ctx context.Context, ev feishubot.MessageEvent, st FeishuStatus, content feishubot.Content, text string, topic *feishuTopic) {
	if _, fail := s.openTodoIn(ctx, ev, st, content, text, "", topic, nil); fail != "" {
		s.botNoteTo(ctx, topic.target(), ev.Message.MessageID, fail)
	}
}

// openTarget is who takes a to-do opened from a topic or from elsewhere: the main conversation, which origin is the id
// of. fail says why not, in the bot's language, "" when it can.
func (s *Service) openTarget(ctx context.Context, st FeishuStatus) (target botTarget, origin, fail string) {
	l := s.botLang(ctx)
	failed := func(err error) string {
		return i18n.T(l, "assistant-feishu.thread_open_failed", i18n.Args{"error": clip(oneLine(err.Error()), 200)})
	}
	target = botTarget{Kind: targetMain}
	if err := botAllowed(l, st.Features, target); err != nil {
		return target, "", failed(err)
	}
	rt, err := s.resolveBotTarget(ctx, target)
	if err != nil {
		return target, "", failed(err)
	}
	origin = s.settings(ctx).Main
	if _, ok := s.session(ctx, rt.machine, origin); origin == "" || rt.isNew || !ok {
		return target, "", i18n.T(l, "assistant-feishu.thread_open_no_main")
	}
	return target, origin, ""
}

// openTodoIn makes the to-do for the topic, with text (the first words, the command taken off) as its title and notes,
// and hands the words to the main conversation. Who takes it is worked out before anything is made, and a topic that
// turns out to be taken (db.ErrThreadTaken) leaves no to-do behind. The first words are not a decision: the to-do is made
// of them. ev is the message the run answers under in the topic: the owner's own, or the one the bot opened it with
// (openFromElsewhere). quote, when not empty, is the text of a message the words reply to: it follows them in the notes
// and in the hand-over, as context. src, when given, is where the request came from, recorded on the to-do. A nil topic
// is a to-do bound to no topic (a request taken in the owner's private chat, openInOwnerChat): ev is then a message of that
// chat, and what is said goes there. fail says why no to-do was made, in the bot's language, "" when one was; what goes
// wrong after the to-do exists is said in the topic (or the private chat).
func (s *Service) openTodoIn(ctx context.Context, ev feishubot.MessageEvent, st FeishuStatus, content feishubot.Content, text, quote string,
	topic *feishuTopic, src *TodoSource) (todo Todo, fail string) {
	msg := ev.Message
	l := s.botLang(ctx)
	to := topic.target()
	where := "private chat" // for the log
	if topic != nil {
		where = "topic " + topic.Thread
	}
	say := func(key string, args ...i18n.Args) {
		s.botNoteTo(ctx, to, msg.MessageID, i18n.T(l, "assistant-feishu."+key, args...))
	}
	failed := func(key string, err error) string {
		return i18n.T(l, "assistant-feishu."+key, i18n.Args{"error": clip(oneLine(err.Error()), 200)})
	}
	if text == "" {
		// A to-do is named after what was said: a picture, a merged forward, a voice message or the like alone has no
		// words to name it by.
		return Todo{}, i18n.T(l, "assistant-feishu.thread_open_needs_text")
	}
	target, origin, why := s.openTarget(ctx, st)
	if why != "" {
		return Todo{}, why
	}
	// What the owner said, and apart from it what someone else said in the message it replied to: the notes carry both, the
	// second under a label that says whose it is and with nothing in it that closes a context block.
	words := text
	if quote != "" {
		words += "\n\n" + i18n.T(l, "assistant-feishu.elsewhere_quote_label") + "\n" + prompts.ContextBody(quote)
	}
	title, retitle := s.taskTitle(ctx, "", text)
	if title == "" {
		title = cutRunes(oneLine(text), todoTitleMax(s.hello(ctx).Locale))
	}
	todo, err := s.createTodo(ctx, title, clip(words, openNotesMax), origin, "")
	if err != nil {
		s.logf("bots: %s: open a to-do: %v", where, err)
		return Todo{}, failed("thread_open_failed", err)
	}
	if topic != nil {
		var th TodoThread
		if th, err = cleanThread(TodoThread{Channel: channel.Feishu, ChatID: topic.Chat, ThreadID: topic.Thread, Title: title}); err == nil {
			_, err = s.bindThread(ctx, todo.ID, th, nil)
		}
	}
	if err == nil && src != nil {
		if src.Anchor == "" {
			src.Handoff = msg.MessageID
		}
		if err = s.setTodoSource(ctx, todo.ID, *src); err == nil {
			s.todosChanged(ctx)
		}
	}
	if err != nil {
		// Taken by another to-do meanwhile (or not a thread that can be bound): the to-do just made goes again.
		if rerr := s.removeTodo(context.WithoutCancel(ctx), todo.ID); rerr != nil {
			s.logf("todo %s: take back: %v", todo.ID, rerr)
		}
		s.logf("bots: %s: bind: %v", where, err)
		return Todo{}, failed("thread_open_failed", err)
	}
	if retitle {
		s.nameTodoLater(ctx, todo.ID, title, text)
	}
	// The main conversation follows up what it hands out (supervise.go), and its feed shows the task as task_plan's do.
	s.superviseTodo(ctx, todo.ID, origin, nil, 0)
	if _, err := s.appendFeed(ctx, origin, feedTask, todo.ID, nil); err != nil {
		s.logf("task %s: feed: %v", todo.ID, err)
	}
	s.logf("bots: %s opened todo %s", where, todo.ID)

	line := i18n.T(l, "assistant-feishu.thread_open_done", i18n.Args{"title": title})
	if link := todoLink(strings.TrimRight(st.PublicURL, "/"), todo.ID); isWebLink(link) {
		line += "\n" + link
	}
	s.botNoteTo(ctx, to, msg.MessageID, line)

	content.Text = prompts.RenderTodoThreadOpen(prompts.TodoThreadOpen{Language: answerLanguage(s.hello(ctx).Locale), Title: contextText(clip(oneLine(title), 80)), TodoID: todo.ID, Text: text, Quote: quote, NoTopic: topic == nil})
	if err := s.botSendIn(ctx, &ev, content, target, topic); err != nil {
		s.logf("bots: %s: todo %s to the main conversation: %v", where, todo.ID, err)
		say("thread_open_not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)})
	}
	if cur, ok := s.todoNow(todo.ID); ok {
		todo = cur // with its topic and source
	}
	return todo, ""
}

// isWebLink: link is an http(s) address with a host (what a message can carry as a link; "" is none).
func isWebLink(link string) bool {
	u, err := url.Parse(link)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
