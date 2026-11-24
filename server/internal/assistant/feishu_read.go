package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Reading a group with /read (#11803, docs/extensions/bots.md "Reading a group with /read"). The owner writes
// "@bot /read <task>" in an alert or problem group; the main conversation reads the group as the owner with lark-cli
// on its own computer (the last readGroupDefault messages, or the range the task names), downloads its images and
// files into a directory of their own, does the task, and the answer goes back as a reply under that message.
//
// The bot is given no way to read a group's history: the owner is in the group, and lark-cli reads as the owner, so no
// sensitive "read every group message" permission is needed. The hub let the message through (extmgr/bots_read.go);
// this is the second line of checks.
//
// The run is quiet in the group: the message gets OnIt while it works and DONE at the end, and no progress card, steps
// or web link go there. Where it goes is kept in the run's own keys (readChatKey, readThreadKey), not a topic's
// ("chat", "thread"): a topic's progress card, agent message and question cards never apply to it, and a question or
// approval the run raises goes to the owner's private chat (interactionRuns).

const (
	// readGroupDefault is how many messages are read when the task names no range; readGroupMost the most for any
	// range ("read everything" included).
	readGroupDefault = 40
	readGroupMost    = 500
	// readFileMB and readTotalMB bound what is downloaded: the bot's own limit for a file it passes on (50 MB), and
	// four of them in all. readImages is how many pictures the model looks at.
	readFileMB  = 50
	readTotalMB = 200
	readImages  = 20
	// readDirRoot is where the downloads go on the conversation's computer, one directory per /read message.
	readDirRoot = "~/.persona/lark-read/"

	readChatKey   = "read_chat"
	readThreadKey = "read_thread"
)

// readDirName keeps a message id usable as a directory name in a shell command unquoted.
var readDirName = lazyre.New(`[^A-Za-z0-9_-]`)

// readRun says a run answers a /read: its answer goes under the message in that group, quietly.
func readRun(ext map[string]string) bool { return ext[readChatKey] != "" }

// feishuRead takes the owner's "@bot /read …" from a group chat (FeishuFromRead): the hub's checks again, then the
// request goes to the main conversation as a run that answers under the message. What keeps it from going is said
// under the message, and nothing else is said in the group.
func (s *Service) feishuRead(ev botcard.MessageEvent, st FeishuStatus) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	msg := ev.Message
	if ev.Sender.SenderType != "user" || st.Owner == "" || ev.Sender.SenderID.OpenID != st.Owner {
		s.logf("bots: a /read that was not the owner's was dropped")
		return
	}
	if !st.Features.ReadGroup {
		s.logf("bots: a /read with the switch off was dropped")
		return
	}
	if msg.ChatType == "p2p" || msg.ChatID == "" || msg.MessageID == "" || msg.MessageType != "text" && msg.MessageType != "post" {
		s.logf("bots: a message marked as a /read was not one from a group: dropped")
		return
	}
	content := botcard.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
	if content.Forward {
		return
	}
	task, ok := botcard.ReadCommand(openingCommandWords(ev, content))
	if !ok {
		s.logf("bots: a message marked as a /read was not one: dropped")
		return
	}
	if s.botRecalled(msg.MessageID) {
		return
	}
	l := s.botLang(ctx)
	where := feishuTarget{ChatID: msg.ChatID, InThread: msg.ThreadID != "", NoDetour: true, Read: true}
	t := botTarget{Kind: targetMain}
	if err := botAllowed(l, st.Features, t); err != nil {
		s.botNoteTo(ctx, where, msg.MessageID, err.Error())
		return
	}
	if err := s.readSend(ctx, ev, task, t); err != nil {
		s.logf("bots: /read: %v", err)
		s.botNoteTo(ctx, where, msg.MessageID, i18n.T(l, "assistant-feishu.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
	}
}

// readSend sends the request into the main conversation (or starts it) and starts the run that answers under the
// message. The reaction says it was taken; nothing more is posted until the answer.
func (s *Service) readSend(ctx context.Context, ev botcard.MessageEvent, task string, t botTarget) error {
	rt, err := s.resolveBotTarget(ctx, t)
	if err != nil {
		return err
	}
	msg := ev.Message
	reaction, err := s.botReact(ctx, msg.MessageID, "OnIt")
	if err != nil {
		s.logf("bots: /read: react: %v", err)
	}
	hello := s.hello(ctx)
	text := prompts.RenderBotReadGroup(readPrompt(strings.TrimSpace(hello.Owner), answerLanguage(hello.Locale), msg.ChatID, msg.MessageID, msg.ThreadID, task))
	ext := map[string]string{readChatKey: msg.ChatID}
	if msg.ThreadID != "" {
		ext[readThreadKey] = msg.ThreadID
	}
	if err := s.startBotRun(ctx, rt, t, msg.MessageID, text, nil, channelProbe(task, msg.MessageID), reaction, ext); err != nil {
		if reaction != "" {
			s.botUnreact(context.WithoutCancel(ctx), msg.MessageID, reaction)
		}
		return err
	}
	return nil
}

// readPrompt is what the main conversation is sent for a /read: where to read, the limits, and the task.
func readPrompt(owner, lang, chat, message, thread, task string) prompts.BotReadGroup {
	return prompts.BotReadGroup{Owner: owner, Language: lang, ChatID: chat, MessageID: message, ThreadID: thread, Task: task,
		Dir: readDirRoot + readDirName.ReplaceAllString(message, "_"), Default: readGroupDefault, Most: readGroupMost,
		FileMB: readFileMB, TotalMB: readTotalMB, Images: readImages}
}

// channelProbe is what of a /read to look for in the transcript: the task's first line, else the message id the
// prompt carries.
func channelProbe(task, message string) string {
	if strings.TrimSpace(task) == "" {
		return message
	}
	return botProbe(task, prompts.BotMessage{})
}
