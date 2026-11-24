package cliwire

import (
	"encoding/json"
	"time"
)

// MinimumVersion is the first lark-cli release whose shortcut JSON contracts this
// adapter accepts. Keep this pinned and raise it only with fixture updates.
const MinimumVersion = "1.0.72"

// AuthStatus is the user identity and bound app reported by `lark-cli auth status --json`.
// GrantedScopes and MissingScopes are best-effort because older CLI releases did
// not include both fields.
type AuthStatus struct {
	AppID            string    `json:"app_id,omitempty"`
	Brand            string    `json:"brand,omitempty"`
	LoggedIn         bool      `json:"logged_in"`
	Expired          bool      `json:"expired"`
	UserName         string    `json:"user_name,omitempty"`
	OpenID           string    `json:"open_id,omitempty"`
	TokenStatus      string    `json:"token_status,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitempty"`
	GrantedScopes    []string  `json:"granted_scopes,omitempty"`
	MissingScopes    []string  `json:"missing_scopes,omitempty"`
}

// Profile is the non-secret summary printed by `lark-cli profile list`.
// Keep this deliberately smaller than the CLI's output: profile credentials
// and token material must never cross the manager API boundary.
type Profile struct {
	Name        string `json:"name"`
	AppID       string `json:"app_id,omitempty"`
	Brand       string `json:"brand,omitempty"`
	User        string `json:"user,omitempty"`
	TokenStatus string `json:"token_status,omitempty"`
}

// DaysUntilExpiry returns the rounded-up number of calendar days remaining for
// the refresh token. The access token can be renewed automatically before this
// deadline, so its expires_at value is not a login expiry.
func (a AuthStatus) DaysUntilExpiry(now time.Time) int {
	if a.RefreshExpiresAt.IsZero() {
		return 0
	}
	d := a.RefreshExpiresAt.Sub(now)
	if d <= 0 {
		return 0
	}
	days := int((d + 24*time.Hour - time.Nanosecond) / (24 * time.Hour))
	if days < 1 {
		return 1
	}
	return days
}

// RefreshWindowNeedsReminder reports when the rolling refresh-token window is
// nearly exhausted. A healthy login refreshes that window on user API calls,
// so its ordinary seven-day deadline is not an expiry warning; only a window
// with less than one day remaining indicates that refreshes may have stopped.
func (a AuthStatus) RefreshWindowNeedsReminder(now time.Time) bool {
	if a.RefreshExpiresAt.IsZero() {
		return false
	}
	remaining := a.RefreshExpiresAt.Sub(now)
	return remaining > 0 && remaining < 24*time.Hour
}

// Chat is the stable subset of a shortcut chat item used by Persona.
type Chat struct {
	ChatID        string `json:"chat_id"`
	Name          string `json:"name,omitempty"`
	Description   string `json:"description,omitempty"`
	OwnerID       string `json:"owner_id,omitempty"`
	ChatType      string `json:"chat_type,omitempty"`
	ChatMode      string `json:"chat_mode,omitempty"`
	P2PTargetType string `json:"p2p_target_type,omitempty"`
	P2PTargetID   string `json:"p2p_target_id,omitempty"`
	External      bool   `json:"external,omitempty"`
	ChatStatus    string `json:"chat_status,omitempty"`
}

// ChatList is the normalized result of +chat-list or +chat-search.
type ChatList struct {
	Chats     []Chat `json:"chats"`
	Total     int    `json:"total,omitempty"`
	HasMore   bool   `json:"has_more,omitempty"`
	PageToken string `json:"page_token,omitempty"`
}

// Mention is one mention in a message.
type Mention struct {
	ID     string `json:"id,omitempty"`
	IDType string `json:"id_type,omitempty"`
	Key    string `json:"key,omitempty"`
	Name   string `json:"name,omitempty"`
}

// Sender is the sender data surfaced by the CLI shortcuts.
type Sender struct {
	ID              string            `json:"id,omitempty"`
	IDType          string            `json:"id_type,omitempty"`
	SenderType      string            `json:"sender_type,omitempty"`
	AppID           string            `json:"app_id,omitempty"`
	Name            string            `json:"name,omitempty"`
	OpenBotID       string            `json:"open_bot_id,omitempty"`
	SenderI18nNames map[string]string `json:"sender_i18n_names,omitempty"`
}

// Message is the common message shape returned by chat reads and searches.
// Content is retained as JSON because the CLI returns either a JSON string or a
// structured value depending on msg_type.
type Message struct {
	MessageID       string          `json:"message_id"`
	MessagePosition string          `json:"message_position,omitempty"`
	MessageAppLink  string          `json:"message_app_link,omitempty"`
	MsgType         string          `json:"msg_type,omitempty"`
	CreateTime      string          `json:"create_time,omitempty"`
	UpdateTime      string          `json:"update_time,omitempty"`
	Sender          Sender          `json:"sender,omitempty"`
	Content         json.RawMessage `json:"content,omitempty"`
	ChatID          string          `json:"chat_id,omitempty"`
	ChatName        string          `json:"chat_name,omitempty"`
	ChatType        string          `json:"chat_type,omitempty"`
	ChatMode        string          `json:"chat_mode,omitempty"`
	ChatPartner     json.RawMessage `json:"chat_partner,omitempty"`
	Deleted         bool            `json:"deleted,omitempty"`
	Updated         bool            `json:"updated,omitempty"`
	Mentions        []Mention       `json:"mentions,omitempty"`
	ThreadID        string          `json:"thread_id,omitempty"`
	ParentID        string          `json:"parent_id,omitempty"`
	RootID          string          `json:"root_id,omitempty"`
	Reactions       json.RawMessage `json:"reactions,omitempty"`
}

// MessagePage is the normalized paginated result of a message shortcut.
type MessagePage struct {
	Messages  []Message `json:"messages"`
	Total     int       `json:"total,omitempty"`
	HasMore   bool      `json:"has_more,omitempty"`
	PageToken string    `json:"page_token,omitempty"`
}

// SearchOptions are the read-only filters supported by +messages-search.
type SearchOptions struct {
	ChatID    string
	Sender    string
	ChatType  string
	Start     string
	End       string
	AtMe      bool
	Limit     int
	PageToken string
}

// Document is the small metadata record returned by drive +inspect. The CLI
// accepts both ordinary document links and wiki links; URL is kept as the link
// the user chose while CanonicalURL is the underlying Drive document when the
// CLI supplies it.
type Document struct {
	URL          string `json:"url,omitempty"`
	CanonicalURL string `json:"canonical_url,omitempty"`
	Token        string `json:"token,omitempty"`
	Type         string `json:"type,omitempty"`
	Title        string `json:"title,omitempty"`
}

// CommentPage is the normalized output of drive +list-comments. Cursor is an
// opaque CLI page token and is only meaningful while HasMore is true.
type CommentPage struct {
	Comments  []Comment `json:"comments"`
	HasMore   bool      `json:"has_more,omitempty"`
	PageToken string    `json:"page_token,omitempty"`
}

// Comment is one unresolved document comment thread. The API calls the
// comment body a reply: the first reply on the first replies page is the root
// comment, followed by ordinary replies.
type Comment struct {
	ID         string          `json:"comment_id"`
	UserID     string          `json:"user_id,omitempty"`
	CreateTime json.RawMessage `json:"create_time,omitempty"`
	UpdateTime json.RawMessage `json:"update_time,omitempty"`
	Solved     bool            `json:"is_solved,omitempty"`
	Whole      bool            `json:"is_whole,omitempty"`
	Quote      string          `json:"quote,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	ReplyList  ReplyList       `json:"reply_list,omitempty"`
	ReplyMore  bool            `json:"has_more,omitempty"`
	ReplyToken string          `json:"page_token,omitempty"`
}

type ReplyList struct {
	Replies   []Reply `json:"replies,omitempty"`
	HasMore   bool    `json:"has_more,omitempty"`
	PageToken string  `json:"page_token,omitempty"`
}

// Reply is a comment body or a reply. Content is kept raw because Lark uses a
// structured elements array for mentions and text.
type Reply struct {
	ID         string          `json:"reply_id,omitempty"`
	UserID     string          `json:"user_id,omitempty"`
	CreateTime json.RawMessage `json:"create_time,omitempty"`
	UpdateTime json.RawMessage `json:"update_time,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
}

type ReplyPage struct {
	Replies   []Reply `json:"replies"`
	HasMore   bool    `json:"has_more,omitempty"`
	PageToken string  `json:"page_token,omitempty"`
}

// WikiSpace and WikiNode are the bounded expansion records used by the
// connector's document watch list. The CLI has returned both camelCase and
// snake_case spellings across releases, so the adapter normalizes both.
type WikiSpace struct {
	ID   string `json:"space_id,omitempty"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

type WikiSpaceList struct {
	Spaces    []WikiSpace `json:"spaces"`
	HasMore   bool        `json:"has_more,omitempty"`
	PageToken string      `json:"page_token,omitempty"`
}

type WikiNode struct {
	NodeToken       string `json:"node_token,omitempty"`
	ObjectToken     string `json:"obj_token,omitempty"`
	ObjectType      string `json:"obj_type,omitempty"`
	Title           string `json:"title,omitempty"`
	URL             string `json:"url,omitempty"`
	HasChild        bool   `json:"has_child,omitempty"`
	ParentNodeToken string `json:"parent_node_token,omitempty"`
}

type WikiNodeList struct {
	Nodes     []WikiNode `json:"nodes"`
	HasMore   bool       `json:"has_more,omitempty"`
	PageToken string     `json:"page_token,omitempty"`
}

// NormalizedLimit is the search page size: 20 when unset, at most 50.
func (o SearchOptions) NormalizedLimit() int {
	if o.Limit <= 0 {
		return 20
	}
	if o.Limit > 50 {
		return 50
	}
	return o.Limit
}
