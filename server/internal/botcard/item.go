package botcard

import "time"

// Item is a message read back (GET /open-apis/im/v1/messages/:id): a quoted message, or a merged forward and the
// messages in it (those have UpperMessageID set).
type Item struct {
	MessageID      string `json:"message_id"`
	ChatID         string `json:"chat_id"`
	ParentID       string `json:"parent_id"`
	UpperMessageID string `json:"upper_message_id"`
	ThreadID       string `json:"thread_id"` // the topic it is in, empty outside one
	MsgType        string `json:"msg_type"`
	CreateTime     string `json:"create_time"` // Unix milliseconds
	Deleted        bool   `json:"deleted"`
	Sender         struct {
		ID         string `json:"id"`
		IDType     string `json:"id_type"`
		SenderType string `json:"sender_type"` // user | app
	} `json:"sender"`
	Body struct {
		Content string `json:"content"`
	} `json:"body"`
	Mentions []struct {
		Key    string `json:"key"`
		ID     string `json:"id"`
		IDType string `json:"id_type"`
		Name   string `json:"name"`
	} `json:"mentions"`
}

// At is when the message was sent.
func (it Item) At() time.Time { return MsTime(it.CreateTime) }

// MentionList preserves names and typed IDs so ParseContent can resolve both placeholder keys and rich-text at nodes.
func (it Item) MentionList() []Mention {
	var out []Mention
	for _, m := range it.Mentions {
		mention := Mention{Key: m.Key, Name: m.Name}
		switch m.IDType {
		case "open_id":
			mention.ID.OpenID = m.ID
		case "union_id":
			mention.ID.UnionID = m.ID
		case "user_id":
			mention.ID.UserID = m.ID
		}
		out = append(out, mention)
	}
	return out
}
