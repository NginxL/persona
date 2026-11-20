package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Replies where a conversation wrote as the user (#10777, docs/assistant/assistant.md "Replies where it wrote as the
// user"). One of the assistant's conversations writes into a Lark chat in the user's name (the node's gate records it
// in the sent-as-user ledger), and the other side answers: the conversation hears of it. The Lark connector hands
// other people's new messages in a chat as a batch (Inbox event grouping, collectSource); each conversation of this
// manager's machine that wrote a message into that chat in the last chatReplyWindow, before the batch's newest
// message, gets the messages it was not told yet through its relay queue (chat_reply), as other people's words. One
// conversation hears of one chat at most once every chatReplyGap; what comes in between merges into the message that
// waits. The Inbox's own handling of the batch is unchanged.

const (
	// chatReplyTool is the relay kind of a chat's new messages told to a conversation that wrote there as the user.
	chatReplyTool = "chat_reply"
	// chatReplyWindow: a send older than this opens nothing, and what was told is forgotten after it.
	chatReplyWindow = 24 * time.Hour
	// chatReplyGap: one conversation hears of one chat at most this often.
	chatReplyGap = 10 * time.Minute
	// chatReplyContextMax is how many characters of the messages' text one message carries: the newest.
	chatReplyContextMax = 3000
)

// chatReply is what a chat_reply message tells, kept with it in the queue (relayMsg.Chat) so that a later batch of the
// same chat merges into it, after a restart too.
type chatReply struct {
	Thread string    `json:"thread"`
	Title  string    `json:"title,omitempty"`
	Group  bool      `json:"group,omitempty"`  // a group chat; else a direct one
	Sender string    `json:"sender,omitempty"` // who wrote the newest message
	Sent   time.Time `json:"sent"`             // the conversation's latest send there
	IDs    []string  `json:"ids"`              // the messages it tells, by the connector's ids
	// Earlier: Context also has messages a chat_reply that went in before told.
	Earlier bool   `json:"earlier,omitempty"`
	Context string `json:"context"`
}

// chatReplyBook is what each conversation was told of each chat, in memory: after a restart, a batch the Inbox has not
// put in an event yet may be told once more (one queued then is not: its ids are in the queue).
type chatReplyBook struct {
	running sync.Mutex // one round's batches at a time
	mu      sync.Mutex
	told    map[string]*chatTold // by chatReplyKey
	// refused: by conversation, when the owner was pushed that it refused a chat_reply for good (chatReplyRefused); one
	// push until a chat_reply goes into it again or chatReplyWindow is over. In memory: a restart may push once more.
	refused map[string]time.Time
	wg      sync.WaitGroup // the rounds being told
}

// chatTold is what one conversation was told of one chat: the messages told or queued (when last handed), and when a
// chat_reply last went in.
type chatTold struct {
	ids  map[string]time.Time
	went time.Time
}

func chatReplyKey(to, thread string) string { return to + "\x00" + thread }

// toldLocked is conversation to's record of thread, made when there is none; b.mu is held.
func (b *chatReplyBook) toldLocked(to, thread string) *chatTold {
	if b.told == nil {
		b.told = map[string]*chatTold{}
	}
	key := chatReplyKey(to, thread)
	told := b.told[key]
	if told == nil {
		told = &chatTold{ids: map[string]time.Time{}}
		b.told[key] = told
	}
	return told
}

// pruneLocked forgets messages last handed more than chatReplyWindow ago, and a record left with none once its gap is
// over; b.mu is held.
func (b *chatReplyBook) pruneLocked(now time.Time) {
	for key, told := range b.told {
		for id, at := range told.ids {
			if now.Sub(at) > chatReplyWindow {
				delete(told.ids, id)
			}
		}
		if len(told.ids) == 0 && now.Sub(told.went) > chatReplyGap {
			delete(b.told, key)
		}
	}
	for to, at := range b.refused {
		if now.Sub(at) > chatReplyWindow {
			delete(b.refused, to)
		}
	}
}

// chatSentStore is the part of the assistant's store that reads the sent-as-user ledger (the manager's Postgres).
type chatSentStore interface {
	SentAsUsersForArchive(context.Context, string, time.Time, time.Time) ([]db.SentAsUser, error)
}

// larkChatFeed is the chat a Lark thread names: "chat:<feed>", "chat:<account>:<feed>" (migratedThread).
func larkChatFeed(thread string) (string, bool) {
	rest, ok := strings.CutPrefix(thread, "chat:")
	if !ok || rest == "" {
		return "", false
	}
	if _, feed, two := strings.Cut(rest, ":"); two {
		return feed, feed != ""
	}
	return rest, true
}

// tellChatWriters takes the batches of new messages the Lark connector handed in a round (people's chats, not a
// bot's) and tells the conversations that wrote into their chats as the user, in the background (tellChatBatches).
func (s *Service) tellChatWriters(ctx context.Context, src assistantwire.InboxSource) {
	if src.ID != migratedConnectorID {
		return
	}
	var batches []connector.InboxThing
	for _, t := range src.Things {
		if g := t.Group; g == nil || g.Batch == "" || len(g.New) == 0 || t.Done != "" || t.Kind == KindBot {
			continue
		}
		if _, ok := larkChatFeed(t.Thread); ok {
			batches = append(batches, t)
		}
	}
	if len(batches) == 0 {
		return
	}
	b := &s.chatReplies
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		s.tellChatBatches(context.WithoutCancel(ctx), batches, time.Now())
	}()
}

// tellChatBatches tells each conversation that wrote a message into a batch's chat as the user, in the last
// chatReplyWindow and before the batch's newest message, the batch's messages it was not told yet. Only the
// assistant's own conversations count: on this manager's machine, not archived, neither the Inbox's (its AI twin
// hears of the chat through the Inbox) nor the background supervisor.
func (s *Service) tellChatBatches(ctx context.Context, batches []connector.InboxThing, now time.Time) {
	ledger, ok := s.store().(chatSentStore)
	machine := s.settings(ctx).MachineID
	if !ok || machine == "" {
		return
	}
	b := &s.chatReplies
	b.running.Lock()
	defer b.running.Unlock()
	rows, err := ledger.SentAsUsersForArchive(ctx, connector.Lark, now.Add(-chatReplyWindow), now.Add(time.Minute))
	if err != nil {
		s.logf("chat replies: the sent-as-user ledger could not be read: %v", err)
		return
	}
	var list []proto.Session
	listed := false
	for _, t := range batches {
		feed, _ := larkChatFeed(t.Thread)
		for _, w := range chatWriters(rows, machine, feed) {
			if !t.At.After(w.at) || now.Sub(w.at) > chatReplyWindow {
				continue
			}
			if !listed {
				if list, err = s.sessions(ctx, machine); err != nil {
					s.logf("chat replies: the conversations could not be listed: %v", err) // the batch comes again
					return
				}
				listed = true
			}
			if s.chatWriterOpen(ctx, machine, w.session, list) {
				s.tellChatBatch(ctx, w.session, w.at, t, now)
			}
		}
	}
}

// chatWriter is a conversation that wrote into a chat as the user, and when it last did.
type chatWriter struct {
	session string
	at      time.Time
}

// chatWriters are the conversations of machine that wrote a message into chat feed as the user, by the ledger's rows,
// each with its latest such send. A recalled message counts as not sent.
func chatWriters(rows []db.SentAsUser, machine, feed string) []chatWriter {
	var out []chatWriter
	for _, r := range rows {
		if r.RecalledAt != nil || !wroteInto(r, feed) {
			continue
		}
		var o struct {
			Machine string `json:"machine_id"`
			Session string `json:"session_id"`
		}
		if json.Unmarshal(r.Origin, &o) != nil || o.Session == "" || o.Machine != machine {
			continue
		}
		if i := slices.IndexFunc(out, func(w chatWriter) bool { return w.session == o.Session }); i < 0 {
			out = append(out, chatWriter{o.Session, r.At})
		} else if r.At.After(out[i].at) {
			out[i].at = r.At
		}
	}
	return out
}

// chatMessageRoutes are the gated routes that put a message into the chat their path names (extmgr's createsMessage).
var chatMessageRoutes = []string{"messages", "images", "files", "attachments"}

// wroteInto says whether ledger row r is a message written into chat feed: a gated POST /conversations/<feed>/<route>
// of chatMessageRoutes, or a connector's send whose target names the chat (conversation or chat) and no one message.
// A reaction, an edit or a read receipt is not, and a reply to one message names no chat.
func wroteInto(r db.SentAsUser, feed string) bool {
	var t map[string]any
	if json.Unmarshal(r.Target, &t) != nil {
		return false
	}
	str := func(k string) string { v, _ := t[k].(string); return v }
	if p, _, _ := strings.Cut(str("path"), "?"); p != "" {
		segs := strings.Split(strings.Trim(p, "/"), "/")
		return strings.EqualFold(str("method"), "POST") && len(segs) == 3 && segs[0] == "conversations" && segs[1] == feed &&
			slices.Contains(chatMessageRoutes, segs[2])
	}
	return str("message") == "" && (str("conversation") == feed || str("chat") == feed)
}

// chatWriterOpen: conversation id is one of the assistant's on machine (list), not archived, neither one the Inbox
// opened nor the background supervisor.
func (s *Service) chatWriterOpen(ctx context.Context, machine, id string, list []proto.Session) bool {
	i := slices.IndexFunc(list, func(c proto.Session) bool { return c.SessionID == id })
	if i < 0 {
		return false
	}
	c := &list[i]
	return c.Assistant && !c.Archived && !c.IsDeleted() && !s.isArchived(id) && !s.isInboxModelCall(ctx, machine, id, "") &&
		!s.isBackground(ctx, id)
}

// tellChatBatch tells conversation to, which last wrote into t's chat at sent, of t's messages it was not told yet:
// in the chat_reply about that chat still queued for it, else in a new one, which waits until chatReplyGap after the
// last one went in.
func (s *Service) tellChatBatch(ctx context.Context, to string, sent time.Time, t connector.InboxThing, now time.Time) {
	b := &s.chatReplies
	b.mu.Lock()
	b.pruneLocked(now)
	told := b.toldLocked(to, t.Thread)
	var fresh []string
	for _, id := range t.Group.New {
		if _, seen := told.ids[id]; !seen {
			fresh = append(fresh, id)
		}
		told.ids[id] = now // handed again: kept while the batch keeps coming
	}
	went := told.went
	b.mu.Unlock()
	if len(fresh) == 0 {
		return // told already, or waiting to be
	}
	c := &chatReply{Thread: t.Thread, Title: clip(oneLine(t.Title), 60), Group: t.Kind != KindDM, Sender: clip(oneLine(t.Sender), 40),
		Sent: sent.UTC(), IDs: fresh, Earlier: len(fresh) < len(t.Group.New), Context: lastRunes(strings.TrimSpace(t.Context), chatReplyContextMax)}
	loc, lang := s.location(ctx), answerLanguage(s.hello(ctx).Locale)
	if s.mergeChatReply(to, c, t.Group.New, loc, lang, now) {
		s.logf("chat replies: %d new message(s) merged into the one waiting for %s", len(fresh), short(to))
		s.publish(ctx)
		return
	}
	msg := chatReplyMsg(c, loc, lang, now)
	if due := went.Add(chatReplyGap); !went.IsZero() && due.After(now) {
		msg.Due = due.UTC()
	} else if s.chatReplyGoing(to, t.Thread) {
		msg.Due = now.Add(chatReplyGap).UTC()
	}
	s.logf("chat replies: %d new message(s) for %s", len(fresh), short(to))
	s.relayOrQueue(ctx, to, msg)
}

// mergeChatReply puts c into the chat_reply about the same chat still queued for conversation to (not one going in
// right now) and says whether there was one. batch is every message of c's batch: when it has all of the queued
// one's, its text has theirs too and takes its place; otherwise the texts are put together, the newest kept.
func (s *Service) mergeChatReply(to string, c *chatReply, batch []string, loc *time.Location, lang string, now time.Time) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return false
	}
	q := b.st.Queue[to]
	for i := range q {
		old := q[i].Chat
		if q[i].Tool != chatReplyTool || old == nil || old.Thread != c.Thread || i == 0 && b.draining[to] {
			continue
		}
		m := *c
		m.IDs = slices.Clone(old.IDs)
		for _, id := range c.IDs {
			if !slices.Contains(m.IDs, id) {
				m.IDs = append(m.IDs, id)
			}
		}
		if len(m.IDs) == len(old.IDs) {
			return true // all of them wait in it already (queued before a restart)
		}
		if old.Sent.After(m.Sent) {
			m.Sent = old.Sent
		}
		earlier := slices.ContainsFunc(batch, func(id string) bool { return !slices.Contains(m.IDs, id) })
		if slices.ContainsFunc(old.IDs, func(id string) bool { return !slices.Contains(batch, id) }) {
			m.Context = lastRunes(old.Context+"\n"+c.Context, chatReplyContextMax)
			earlier = earlier || old.Earlier
		}
		m.Earlier = earlier
		msg := chatReplyMsg(&m, loc, lang, now)
		msg.Due = q[i].Due // the gap it waits for stays
		q[i] = msg.replacing(q[i], now)
		s.saveRelayLocked()
		return true
	}
	return false
}

// chatReplyGoing: the chat_reply about thread is going into conversation to right now (the head of its draining
// queue); one that comes meanwhile waits its gap from now.
func (s *Service) chatReplyGoing(to, thread string) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.st.Queue[to]
	return len(q) > 0 && b.draining[to] && q[0].Tool == chatReplyTool && q[0].Chat != nil && q[0].Chat.Thread == thread
}

// chatReplyDelivered: a chat_reply went into conversation to; the next one about that chat waits chatReplyGap. Its
// messages count as told, also when it was queued before a restart. A refusal of to's is over: the next one is pushed.
func (s *Service) chatReplyDelivered(to string, msg relayMsg) {
	b := &s.chatReplies
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.refused, to)
	b.wentLocked(to, msg, time.Now())
}

// chatReplyRefused: conversation to refused chat_reply msg for good (relayRefused). It counts as gone for the gap, so
// the chat's next batch waits chatReplyGap and what comes meanwhile merges into that one, not a try and a refusal per
// batch; its messages count as told. True when the owner is to be pushed: the first such refusal by to since a
// chat_reply last went into it, within chatReplyWindow.
func (s *Service) chatReplyRefused(to string, msg relayMsg) bool {
	b := &s.chatReplies
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.wentLocked(to, msg, now)
	if at, pushed := b.refused[to]; pushed && now.Sub(at) <= chatReplyWindow {
		return false
	}
	if b.refused == nil {
		b.refused = map[string]time.Time{}
	}
	b.refused[to] = now
	return true
}

// wentLocked notes chat_reply msg as gone into conversation to at now: the gap starts, its messages are told. b.mu is
// held.
func (b *chatReplyBook) wentLocked(to string, msg relayMsg, now time.Time) {
	if msg.Chat == nil {
		return
	}
	told := b.toldLocked(to, msg.Chat.Thread)
	told.went = now
	for _, id := range msg.Chat.IDs {
		if _, ok := told.ids[id]; !ok {
			told.ids[id] = now
		}
	}
}

// chatReplyMsg is the relay message that tells c, in the owner's language (lang, answerLanguage) with the time of the
// send in their zone.
func chatReplyMsg(c *chatReply, loc *time.Location, lang string, now time.Time) relayMsg {
	// i18n-ignore: the queue's label, as the task notices'
	label := "群「" + c.Title + "」"
	if !c.Group {
		label = "私聊（" + c.Title + "）" // i18n-ignore: the queue's label, as the task notices'
	}
	text := prompts.RenderChatReply(prompts.ChatReply{Language: lang, Title: c.Title, Group: c.Group, Sender: c.Sender,
		Sent: c.Sent.In(loc).Format("01-02 15:04"), Count: len(c.IDs), Earlier: c.Earlier, Context: c.Context})
	return relayMsg{From: c.Thread, FromTitle: label, Tool: chatReplyTool, Text: text, At: now.UTC(), Chat: c}
}

// lastRunes is the end of s, at most n characters, starting with … where it was cut.
func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n+1:])
}
