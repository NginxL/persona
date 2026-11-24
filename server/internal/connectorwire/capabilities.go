package connectorwire

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/sdk/go/wire"
)

// The connector's optional capabilities beyond those of connector.go, each declared in its Manifest
// (manifest.go): the archive and its history, the snapshot, the inbox, and the catalog.

// Archive is a connector whose conversations go into the knowledge base by one cursor for the whole account
// (GitHub's updated_at, mail's UID, iMessage's ROWID) rather than one per conversation: Changes gives what moved
// since cursor ("" the first time) and the cursor to go on from; more says another call has more now. The hub
// keeps the cursor (cursors/<id>.json in the data dir) and ingests every batch as a chat "<id>-<chat id>".
type Archive interface {
	Changes(ctx context.Context, cursor string, limit int) (batches []ChatBatch, next string, more bool, err error)
}

// ArchiveMedia is an Archive whose messages' Media (refs the connector made) can be fetched, so the knowledge base
// keeps a conversation's pictures and files with it as it does for the hub's own sources. Optional: without it the
// lines say [attachment] (in Chinese) and nothing is kept. At most ArchiveMediaMost bytes.
type ArchiveMedia interface {
	ArchiveMedia(ctx context.Context, ref string) (Media, error)
}

// History is an Archive that can also read a conversation's older messages, for importing its past into the knowledge
// base (docs/protocol/connector-protocol.md "History"): HistoryChats lists the conversations it can import, History
// reads one page of one of them. Read-only, and asked only when the manifest declares CapHistory, which needs
// CapArchive: the history is filed as the archive files its conversations (HistoryChat.Source).
//
// A page's limit is a hint (a source may return more); a conversation the account can no longer read is a page with
// Unreadable, not an error; Pace is how the source slows the caller down or stops it. An error is passed to the
// caller as it is (its rpc code too, through a program), and a caller never retries a call that timed out.
type History interface {
	HistoryChats(ctx context.Context) ([]HistoryChat, error)
	History(ctx context.Context, q HistoryQuery) (HistoryPage, error)
}

// HistoryAnchorsReader is a History that can search (the framework calls it HistoryAnchors): the messages the owner sent or was mentioned in over a time window,
// so a conversation can be imported as windows around them instead of read in full. Optional (CapHistoryAnchors,
// which needs CapHistory), read-only.
type HistoryAnchorsReader interface {
	HistoryAnchors(ctx context.Context, q HistoryAnchorsQuery) (HistoryAnchors, error)
}

// Snapshot is a connector that knows the user's live state worth a glance: the chats waiting on them and the rest of
// today's meetings. The assistant's snapshot (what it knows when a conversation starts) and the "Today" card read it;
// it answers quickly (the hub waits a few seconds, then goes on without it).
type Snapshot interface {
	Snapshot(ctx context.Context, tz string) (SnapshotData, error)
}

// Grouper is an inbox whose new messages are related to events (CapGroup): the hub's model decides which event each
// message belongs to, reading the conversation through GroupTool, and the connector groups them by structure itself
// when the model can't (GroupFallback). Both may answer that they have no such thing (an unknown method): the model
// is told the tool is not there; without a fallback the batch waits for the model. The hub writes the events itself,
// unless the connector also keeps their meta (CapMeta, EventWriter).
type Grouper interface {
	GroupTool(ctx context.Context, call GroupToolCall) (json.RawMessage, error)
	GroupFallback(ctx context.Context, f GroupFallback) (GroupFallbackResult, error)
}

// EventWriter is a grouping inbox that keeps each event's meta (CapMeta): the hub has it write every event it
// composed (GroupAppend: one event a call, meta in, meta out) and stores what comes back.
type EventWriter interface {
	GroupAppend(ctx context.Context, a GroupAppend) (InboxThing, error)
}

// Inbox is a connector with things for the inbox. Collect answers from the connector's memory (it is called on
// every inbox collection; a connector that polls does so in its own Watch and calls changed(ScopeInbox)). Reply
// sends text the user confirmed (the send button is the confirmation) to the thread.
type Inbox interface {
	Collect(ctx context.Context) ([]InboxThing, error)
	Reply(ctx context.Context, r InboxReply) error
}

// ErrCannotReply is a Reply refused before anything was sent because the thing has no way to be answered through the
// connector (a read-only document, a thread with no chat to post to). Wrap it (fmt.Errorf("%w: …")) without the
// thread or other identifiers: the apps get only its kind (inbox_cannot_reply, #9125).
var ErrCannotReply = errors.New("this item cannot be replied to through its connector")

// InboxSettler is an inbox whose rules can close items the hub still has open (made under older rules, or of a
// thread the connector no longer brings in): Settle gets the open items' threads and returns, for each that is
// over, why. Nothing is deleted: the hub files them as done (read).
type InboxSettler interface {
	Settle(ctx context.Context, threads []string) map[string]string
}

// Catalog is an optional collection of subscribable read-only sources. Find it
// through ReadCatalog and dispatch through CallCatalog, which also serve older
// connectors' declared ReadTools without exposing any Actions.
type Catalog interface {
	Catalog(context.Context) ([]CatalogSource, error)
	CatalogCall(context.Context, CatalogQuery) (string, error)
}

// Catalog payloads share the schema-generated SDK types with extension authors.
type (
	CatalogSource      = wire.ConnectorCatalogSource
	CatalogTool        = wire.ConnectorCatalogTool
	CatalogAnnotations = wire.ConnectorCatalogAnnotations
	SubscriptionRecipe = wire.ConnectorSubscriptionRecipe
	SubscriptionFields = wire.ConnectorSubscriptionFields
	CatalogResult      = wire.ConnectorCatalog
)

// ReadJSON is optional for a connector whose catalog comes from its ReadTools (docs/extensions/subscriptions.md
// "Existing connectors"). CallCatalog calls CallJSON first: a read tool answers subscriptions with JSON, a list of
// items with stable ids that they can follow item by item, while the models keep the text Call gives them. ok is
// false for a tool without a JSON answer, and CallCatalog calls Call instead. It is not a capability: nothing else
// asks for it.
type ReadJSON interface {
	CallJSON(ctx context.Context, name string, args json.RawMessage) (out json.RawMessage, ok bool, err error)
}

type CatalogQuery struct {
	Source string          `json:"source"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args,omitempty"`
}
