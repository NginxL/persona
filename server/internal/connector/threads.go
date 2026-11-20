package connector

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// Threads is a connector whose inbox threads the app opens as a conversation of their own (CapThreads): a page of
// messages at a time, newest page first, and the pictures they show (avatars, images, stickers, video thumbnails)
// through the hub (GET /api/v1/connectors/{id}/thread, …/media). Read-only: a reply goes through the action Chat.Reply
// names, behind the gate like any write. Telegram's chats draw as bubbles; GitHub's pull requests and issues as
// GitHub's own thread (the app picks by Chat.Kind).
type Threads interface {
	// Thread is a page of the thread's messages, oldest first: the newest ones for before "", else those before the
	// cursor the previous page gave (ThreadPage.Before). limit is at most ThreadMost.
	Thread(ctx context.Context, thread, before string, limit int) (ThreadPage, error)
	// Media is the bytes a ref (in a page's avatars and media) names, at most MediaMost. A ref always names the same
	// bytes, so the hub keeps them for good: a picture that can change (an avatar) has its version in the ref.
	Media(ctx context.Context, ref string) (Media, error)
}

// Limits of a thread's page and of one media's bytes.
const (
	ThreadMost = 50
	MediaMost  = 4 << 20
)

// ThreadPage is a page of a thread's messages, oldest first; Before goes back one more page ("": none).
type ThreadPage struct {
	Chat     ThreadChat      `json:"chat"`
	Messages []ThreadMessage `json:"messages"`
	Before   string          `json:"before,omitempty"`
}

// ThreadKindNone is the kind of the page the hub answers for a thread with no conversation of its own (a
// subscription's item, a connector's without threads): no messages, and the apps show what the hub collected about
// the item, as for any kind they do not draw.
const ThreadKindNone = "none"

// ThreadChat is the conversation the thread is.
type ThreadChat struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // dm | group | channel (bubbles), pr | issue (GitHub's thread), none (ThreadKindNone)
	URL     string `json:"url,omitempty"`
	Avatar  string `json:"avatar,omitempty"`  // connector media ref or HTTPS address
	Members int    `json:"members,omitempty"` // a group's
	// Reply is the action that answers in this thread, with {chat: <thread>, text, reply_to}; "" when the user cannot
	// answer here.
	Reply string `json:"reply,omitempty"`
	// A pull request's or issue's (GitHub; the others leave them empty): its state (open | merged | closed | draft),
	// who opened it, labels, branches, and its description, shown first whichever page is read.
	State      string        `json:"state,omitempty"`
	Author     *ThreadPerson `json:"author,omitempty"`
	Labels     []ThreadLabel `json:"labels,omitempty"`
	Branch     *ThreadBranch `json:"branch,omitempty"`
	Body       string        `json:"body,omitempty"`
	BodyFormat string        `json:"body_format,omitempty"` // "" | markdown
}

// ThreadMessage is one message of a thread.
type ThreadMessage struct {
	ID     string       `json:"id"`
	At     time.Time    `json:"at"`
	Sender ThreadPerson `json:"sender"`
	Mine   bool         `json:"mine,omitempty"`
	// Event: a line of the thread's own (joined, renamed, merged): centred, no bubble.
	Event  bool          `json:"event,omitempty"`
	Text   string        `json:"text,omitempty"`
	Format string        `json:"format,omitempty"` // "" | markdown
	Media  []ThreadMedia `json:"media,omitempty"`
	// ReplyTo is the message this one answers, as much of it as the page could tell.
	ReplyTo *ThreadQuote `json:"reply_to,omitempty"`
	Edited  bool         `json:"edited,omitempty"`
	URL     string       `json:"url,omitempty"`
	// Kind is "" for a message; GitHub's: review, code (a comment on a line), check (CI).
	Kind   string       `json:"kind,omitempty"`
	Review string       `json:"review,omitempty"` // approved | changes_requested | commented | dismissed
	Code   *ThreadCode  `json:"code,omitempty"`
	Check  *ThreadCheck `json:"check,omitempty"`
}

// ThreadPerson is who wrote a message.
type ThreadPerson struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Avatar string `json:"avatar,omitempty"` // connector media ref or HTTPS address
}

// ThreadMedia is a picture, a sticker, a video, a voice message or a file in a message. Ref (the media, when the hub
// can fetch it: a picture, a static sticker) and Thumb (a smaller picture of it: a video's, an animated sticker's)
// are media refs; a file or a voice message is only described.
type ThreadMedia struct {
	Kind     string `json:"kind"` // image | sticker | video | voice | file
	Ref      string `json:"ref,omitempty"`
	Thumb    string `json:"thumb,omitempty"`
	Mime     string `json:"mime,omitempty"`
	Name     string `json:"name,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Duration int    `json:"duration,omitempty"` // seconds
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Emoji    string `json:"emoji,omitempty"` // a sticker's
}

// ThreadQuote is the message another one answers.
type ThreadQuote struct {
	ID   string `json:"id"`
	From string `json:"from,omitempty"`
	Text string `json:"text,omitempty"`
}

type (
	ThreadLabel struct {
		Name  string `json:"name"`
		Color string `json:"color,omitempty"` // six hex digits ("d73a4a")
	}
	ThreadBranch struct {
		Head string `json:"head"`
		Base string `json:"base"`
	}
	// ThreadCode is the line a code comment is on and the last lines of the diff before it.
	ThreadCode struct {
		Path string `json:"path"`
		Line int    `json:"line,omitempty"`
		Hunk string `json:"hunk,omitempty"`
	}
	// ThreadCheck is a CI run: its state (success | failure | pending | skipped | cancelled) and, failed, the end of
	// its log (at most 8 KB).
	ThreadCheck struct {
		Name  string `json:"name"`
		State string `json:"state"`
		URL   string `json:"url,omitempty"`
		Log   string `json:"log,omitempty"`
	}
)

// Defined in the contract package connectorwire (docs/server/architecture.md "Contract packages"); the old names stay.
type Media = connectorwire.Media
