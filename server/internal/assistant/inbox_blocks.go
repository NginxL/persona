package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// The blocks of inbox items a connector moving out of the hub hands over in its things' extra (connector.MigratableConnectors,
// docs/protocol/connector-protocol.md "Connectors migrated out of hub"): the apps draw a document item's rows and a feed post's notices from
// them, and a document item's status changes go back to the connector (inbox.reply's changes, through the hub).

// InboxDoc is a document item: which document, what happened, the rows about me and where a reply goes.
type InboxDoc struct {
	URL      string `json:"url"`            // as the notification links it (a wiki link stays one)
	Link     string `json:"link,omitempty"` // the document behind it (/sheets/…, /base/…, /docx/…)
	Type     string `json:"type"`           // docx | doc | sheet | base | other
	Notice   string `json:"notice"`         // mention | comment | reply | share
	NoticeID string `json:"notice_id"`
	// AccountID is the extension's account key for a document outside the legacy account; zero keeps the old unqualified thread.
	AccountID int        `json:"account_id,omitempty"`
	Quote     string     `json:"quote,omitempty"` // passage the comment is attached to; empty means whole document
	Rows      []DocRow   `json:"rows,omitempty"`
	Reply     *DocReply  `json:"reply,omitempty"` // where "Send" posts the reply; nil: nowhere
	Error     string     `json:"error,omitempty"` // the document could not be read
	ReadAt    *time.Time `json:"read_at,omitempty"`
}

// DocRow is a sheet row / Base record that mentions me.
type DocRow struct {
	Ref    string      `json:"ref"` // Base: tbl…/rec…; sheet: <tab id>!<row from 0>
	Table  string      `json:"table,omitempty"`
	Title  string      `json:"title"`
	Fields []DocField  `json:"fields,omitempty"`
	Status []DocStatus `json:"status,omitempty"`
}

type DocField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// DocStatus is one status column of a row: its value and what it can be moved to.
type DocStatus struct {
	Field   string      `json:"field"` // Base field id / sheet column from 0
	Name    string      `json:"name"`
	Value   string      `json:"value"`
	Options []DocOption `json:"options"`
}

type DocOption struct {
	Name  string `json:"name"`
	Color string `json:"color,omitempty"` // #rrggbb
}

// DocReply is where the reply to a document item goes.
type DocReply struct {
	Kind   string `json:"kind"` // thread | record | cell | doc
	Label  string `json:"label"`
	Thread string `json:"thread,omitempty"`
	Table  string `json:"table,omitempty"`
	Record string `json:"record,omitempty"`
	Sheet  string `json:"sheet,omitempty"`
	Row    int    `json:"row,omitempty"`
	Column int    `json:"column,omitempty"`
}

// DocChange moves one row's status (the user's tap, or the AI twin's proposal).
type DocChange struct {
	Ref   string `json:"ref"`
	Field string `json:"field"`
	Value string `json:"value"`
}

// KindMoment is the kind of a company-feed item (someone commented on, replied to or @ed the user there).
const KindMoment = "moment"

// InboxMoment is a company-feed item: which post, what happened, and which comment the reply answers.
type InboxMoment struct {
	PostID    string     `json:"post_id"`
	Notice    string     `json:"notice"`              // the newest one's kind: comment | reply | atInPost | atInComment
	Notices   []string   `json:"notices"`             // notification ids in the item, oldest first
	ReplyTo   string     `json:"reply_to,omitempty"`  // the comment "Send" replies to; "" comments on the post itself
	MyPost    bool       `json:"my_post,omitempty"`   // the post is mine
	PostText  string     `json:"post_text,omitempty"` // as the notification quotes it
	Anonymous bool       `json:"anonymous,omitempty"` // the newest actor posted anonymously
	ReadAt    *time.Time `json:"read_at,omitempty"`   // when the comments were last read
}

// KindDoc is the kind of a document item (someone @ed or commented on the user in a document).
const KindDoc = "doc"

// docOffers: the change moves a row the item lists to one of its status column's values.
func docOffers(d InboxDoc, ch DocChange) bool {
	for _, r := range d.Rows {
		for _, st := range r.Status {
			if r.Ref == ch.Ref && st.Field == ch.Field {
				for _, o := range st.Options {
					if o.Name == ch.Value {
						return true
					}
				}
			}
		}
	}
	return false
}

var errNoDocRow = i18n.ErrEnglish("inbox-errors.no_doc_row")

// setDocStatus moves one row's status of a document item (the user's tap): the item's connector does it
// (inbox.reply's changes, through the hub) and reads the document again.
func (s *Service) setDocStatus(ctx context.Context, id string, ch DocChange) (InboxItem, error) {
	b := &s.inbox
	b.mu.Lock()
	it, ok := b.items[id]
	var d InboxDoc
	if ok && it.Doc != nil {
		d = *it.Doc
	}
	b.mu.Unlock()
	if !ok || d.NoticeID == "" {
		return InboxItem{}, errNoInboxItem
	}
	if it.Connector != nil || migratedThread(it) == "" {
		return InboxItem{}, errCannotReply
	}
	if !docOffers(d, ch) {
		return InboxItem{}, errNoDocRow
	}
	return s.migratedDocStatus(ctx, it, ch)
}
