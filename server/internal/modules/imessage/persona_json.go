package imessage

import "time"

// The read tools' answers as JSON, for source subscriptions (connectorwire.ReadJSON, docs/extensions/subscriptions.md
// "Existing connectors"): an object with one top-level list, whose items carry the names a recipe maps (id, thread,
// title, sender, time, body). Codes are masked as in the text the models read (persona.go); people's names and
// handles are the same as there, and nothing the user did not hand to Persona is read.

type chatJSON struct {
	// ID is the chat's and its latest message's time: each new latest message is a new item, one row a chat
	// (thread) in the Inbox.
	ID      string   `json:"id"`
	Thread  string   `json:"thread"` // the chat's id, for imessage_read
	Title   string   `json:"title"`  // the chat's name
	Group   bool     `json:"group,omitempty"`
	Members []Person `json:"members"` // the others
	Time    string   `json:"time"`    // of its latest message
	FromMe  bool     `json:"from_me,omitempty"`
	Unread  int      `json:"unread,omitempty"`
	Body    string   `json:"body,omitempty"` // its latest message's start
}

type chatsJSON struct {
	More   bool       `json:"more"`
	Before string     `json:"before,omitempty"` // for the older ones
	Chats  []chatJSON `json:"chats"`
}

func chatsData(page ChatsPage) chatsJSON {
	out := chatsJSON{More: page.More, Before: page.Before, Chats: make([]chatJSON, 0, len(page.Chats))}
	for _, ch := range page.Chats {
		id := ch.ID
		if !ch.Last.IsZero() {
			id += "@" + ch.Last.UTC().Format(time.RFC3339Nano)
		}
		out.Chats = append(out.Chats, chatJSON{ID: id, Thread: ch.ID, Title: ch.Name, Group: ch.Group, Members: members(ch),
			Time: rfc3339(ch.Last), FromMe: ch.FromMe, Unread: ch.Unread, Body: MaskCodes(ch.Preview)})
	}
	return out
}

func members(ch Chat) []Person {
	if ch.Members == nil {
		return []Person{}
	}
	return ch.Members
}

type messageJSON struct {
	ID          string       `json:"id"`
	Thread      string       `json:"thread"` // the chat's id
	Title       string       `json:"title"`  // the chat's name
	Kind        string       `json:"kind"`   // message | event (a line about the group)
	Sender      string       `json:"sender,omitempty"`
	Handle      string       `json:"handle,omitempty"`
	FromMe      bool         `json:"from_me,omitempty"`
	Time        string       `json:"time"`
	Body        string       `json:"body,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ReplyTo     string       `json:"reply_to,omitempty"`
	Edited      bool         `json:"edited,omitempty"`
	Unsent      bool         `json:"unsent,omitempty"`
	Undecoded   bool         `json:"undecoded,omitempty"`
}

func messageData(m Message, ch Chat) messageJSON {
	out := messageJSON{ID: m.ID, Thread: ch.ID, Title: ch.Name, Kind: m.Kind, FromMe: m.FromMe, Time: rfc3339(m.At),
		Body: MaskCodes(m.Text), Attachments: m.Attachments, ReplyTo: MaskCodes(m.ReplyTo), Edited: m.Edited, Unsent: m.Unsent,
		Undecoded: m.Undecoded}
	if !m.FromMe && m.Sender != nil {
		out.Sender, out.Handle = m.Sender.shown(), m.Sender.Handle
	}
	return out
}

type chatInfoJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Group   bool     `json:"group,omitempty"`
	Members []Person `json:"members"`
}

type messagesJSON struct {
	Chat     chatInfoJSON  `json:"chat"`
	More     bool          `json:"more"`
	Before   string        `json:"before,omitempty"` // for the older ones
	Messages []messageJSON `json:"messages"`         // oldest first
}

func messagesData(page MessagesPage) messagesJSON {
	ch := page.Chat
	out := messagesJSON{Chat: chatInfoJSON{ID: ch.ID, Name: ch.Name, Group: ch.Group, Members: members(ch)}, More: page.More,
		Before: page.Before, Messages: make([]messageJSON, 0, len(page.Messages))}
	for _, m := range page.Messages {
		out.Messages = append(out.Messages, messageData(m, ch))
	}
	return out
}

type hitsJSON struct {
	Hits []messageJSON `json:"hits"` // newest first
}

func hitsData(list []Hit) hitsJSON {
	out := hitsJSON{Hits: make([]messageJSON, 0, len(list))}
	for _, h := range list {
		out.Hits = append(out.Hits, messageData(h.Message, h.Chat))
	}
	return out
}

// rfc3339 is t in RFC 3339, UTC; "" for none.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
