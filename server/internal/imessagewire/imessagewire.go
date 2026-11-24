// Package imessagewire is the contract between the core and the iMessage connector module (internal/modules/imessage):
// manager.json's section, the connector's id, how the manager asks the Mac's node and the errors that come back, the
// connector the extension manager drives, and the node's handler of the hub's connector.* RPCs on a Mac
// (docs/architecture/core-and-modules.md "Provider seams"). Types, constants and small pure helpers only
// (docs/server/architecture.md "Contract packages").
package imessagewire

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// ID is the connector's id: the key in manager.json, the node RPCs' "id".
const ID = "imessage"

// Config is manager.json's "imessage".
type Config struct {
	// Machine is the Mac whose Messages are read: one only (Macs with the same Apple ID hold the same messages).
	Machine string `json:"machine,omitempty"`
}

// Configured says whether the hub should run the connector at all.
func (c *Config) Configured() bool { return c != nil && c.Machine != "" }

// Why the Mac could not be asked (Caller's errors, errors.Is); ErrNoPermission and ErrNoChat come from the Mac.
var (
	ErrOffline    = errors.New("the Mac is offline")
	ErrNodeTooOld = errors.New("the Mac's personad is too old")
	// ErrNoPermission: macOS does not let this process read the Messages data (Full Disk Access).
	ErrNoPermission = errors.New("no permission to read the Messages data")
	// ErrNoChat: no conversation has that id.
	ErrNoChat = errors.New("no such conversation")
)

// Caller asks the Mac's node: method with params, the result decoded into out.
type Caller func(ctx context.Context, method string, params, out any) error

// Detail is the status with what the app needs to fix it: the steps on the Mac, and how its node runs.
type Detail struct {
	Machine string   `json:"machine"`
	Status  string   `json:"status"`
	Error   string   `json:"error,omitempty"`
	Guide   []string `json:"guide,omitempty"`
	Via     string   `json:"via,omitempty"` // ssh | launchd
	// AI: "Hand to Persona" is on (the knowledge base, the inbox, the assistant's tools).
	AI bool `json:"ai"`
}

// Remote is the connector on the hub as the extension manager drives it: a connector (connectorwire) with its status,
// plus "Hand to Persona", the app's own pages of conversations and messages, and the words for a failure.
type Remote interface {
	connectorwire.Connector
	connectorwire.Accounts
	// BuiltinIMessage marks the module's connector.
	BuiltinIMessage()
	// Machine is the Mac it reads, as it was configured.
	Machine() string
	// SetZone gives it the user's time zone, for the times the models read.
	SetZone(func() *time.Location)
	SetAI(on bool)
	AI() bool
	// Check asks the Mac again now; Detail is the status for the app.
	Check(ctx context.Context)
	Detail(ctx context.Context) Detail
	// ChatsPage and MessagesPage are the app's pages, JSON as the Mac's node answers them.
	ChatsPage(ctx context.Context, limit int, before string) (any, error)
	MessagesPage(ctx context.Context, chat string, limit int, before string) (any, error)
	// Explain is err in words for the app.
	Explain(err error) string
}

// Factory makes the connector reading the Messages of cfg.Machine (named machine) through call.
type Factory func(cfg Config, machine string, call Caller) Remote

// LocalConnector answers the hub's connector.* RPCs (connector.list, .chats, .older, .changes, .search) on the node:
// what the Messages data on this computer holds, read-only.
type LocalConnector func(ctx context.Context, method string, params json.RawMessage) (any, error)
