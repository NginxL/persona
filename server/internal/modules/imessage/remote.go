package imessage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/imessagewire"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// The hub's side: the connector reads through the node of one Mac (Reader runs there), so it is a thin adapter over
// that node's connector.* RPCs. The app reads every conversation for the user (extmgr/imessage.go); with Hand to Persona on
// (persona.go) the real ones also go to the knowledge base, the inbox and the assistant.

// Defined in the contract package imessagewire (docs/architecture/core-and-modules.md "Provider seams"); the old names stay.
type (
	Config = imessagewire.Config
	Caller = imessagewire.Caller
	Detail = imessagewire.Detail
)

// Why the Mac could not be asked (Caller's errors, errors.Is); ErrNoPermission and ErrNoChat come through too.
var (
	ErrOffline    = imessagewire.ErrOffline
	ErrNodeTooOld = imessagewire.ErrNodeTooOld
)

// Remote is the connector on the hub.
type Remote struct {
	cfg     Config
	machine string // the Mac's name, for messages
	call    Caller

	mu     sync.Mutex
	status connectorwire.Status
	guide  []string
	via    string
	poke   chan struct{}

	// Zone is the user's time zone, for the times the models read (the hub's userLocation).
	Zone func() *time.Location

	ai       atomic.Bool // "Hand to Persona" (persona.go)
	inboxMu  sync.Mutex
	things   []connectorwire.InboxThing
	contexts map[string]string // conversation + newest message → its last messages as text
	newest   time.Time         // the newest conversation Watch saw
}

// NewRemote is the connector reading the Messages of cfg.Machine (named machine) through call.
func NewRemote(cfg Config, machine string, call Caller) *Remote {
	c := &Remote{cfg: cfg, machine: machine, call: call, poke: make(chan struct{}, 1), contexts: map[string]string{},
		status: connectorwire.Status{State: connectorwire.StateError, Error: "正在问 " + machine + "…"}}
	c.ai.Store(true)
	return c
}

// Provider is the module's factory for the extension manager's port (imessagewire.Factory).
func Provider(cfg Config, machine string, call Caller) imessagewire.Remote {
	return NewRemote(cfg, machine, call)
}

var _ imessagewire.Remote = (*Remote)(nil)

// BuiltinIMessage marks the module's connector (imessagewire.Remote).
func (c *Remote) BuiltinIMessage() {}

// SetZone sets Zone, the user's time zone.
func (c *Remote) SetZone(z func() *time.Location) { c.Zone = z }

// ChatsPage is Chats for the manager's route (imessagewire.Remote).
func (c *Remote) ChatsPage(ctx context.Context, limit int, before string) (any, error) {
	return c.Chats(ctx, limit, before)
}

// MessagesPage is Messages for the manager's route (imessagewire.Remote).
func (c *Remote) MessagesPage(ctx context.Context, chat string, limit int, before string) (any, error) {
	return c.Messages(ctx, chat, limit, before)
}

func (c *Remote) ID() string   { return ID }
func (c *Remote) Name() string { return "信息" }

// Manifest: it reads one Mac's Messages through the node; nothing is sent and nothing signed in to from the app.
func (c *Remote) Manifest() connectorwire.Manifest {
	return connectorwire.Manifest{
		Capabilities: []connectorwire.Capability{connectorwire.CapStatus, connectorwire.CapTools, connectorwire.CapEvents, connectorwire.CapInbox,
			connectorwire.CapArchive, connectorwire.CapMachines, connectorwire.CapCatalog},
		Kinds: []string{"dm", "group"},
	}
}

func (c *Remote) Status(context.Context) connectorwire.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.status
	st.Guide = c.guide
	st.Account = c.machine
	st.Via = c.machine + " 上的「信息」"
	return st
}

func (c *Remote) Detail(ctx context.Context) Detail {
	st := c.Status(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	return Detail{Machine: c.machine, Status: st.State, Error: st.Error, Guide: c.guide, Via: c.via, AI: c.AI()}
}

// Machine is the Mac it reads, as it was configured (an id, or hub.json's name).
func (c *Remote) Machine() string { return c.cfg.Machine }

// MachineOnline: the Mac just connected, so look again.
func (c *Remote) MachineOnline(id string) {
	if id == c.cfg.Machine {
		c.Refresh()
	}
}

// Refresh checks again soon (a read just failed for want of permission, or it may have been granted).
func (c *Remote) Refresh() {
	select {
	case c.poke <- struct{}{}:
	default:
	}
}

// How often Run checks: when all is well, and when it is not.
var (
	checkOK    = 5 * time.Minute
	checkRetry = time.Minute
)

// Run checks the Mac now and then until ctx ends.
func (c *Remote) Run(ctx context.Context) {
	for {
		c.Check(ctx)
		wait := checkOK
		if c.Status(ctx).State != connectorwire.StateOK {
			wait = checkRetry
		}
		select {
		case <-ctx.Done():
			return
		case <-c.poke:
		case <-time.After(wait):
		}
	}
}

// Check asks the Mac whether its Messages can be read, and keeps the answer for Status and Detail.
func (c *Remote) Check(ctx context.Context) {
	var list struct {
		Connectors []struct {
			ID string `json:"id"`
			Status
		} `json:"connectors"`
	}
	err := c.call(ctx, "connector.list", map[string]any{}, &list)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.guide, c.via = nil, ""
	if err != nil {
		c.status.State, c.status.Error = connectorwire.StateError, c.explain(err)
		return
	}
	for _, l := range list.Connectors {
		if l.ID == ID {
			c.status = connectorwire.Status{State: l.State, Error: l.Error, LastSync: c.status.LastSync}
			if l.State == connectorwire.StateOK {
				c.status.LastSync = time.Now().UTC()
			}
			c.guide, c.via = l.Guide, l.Via
			return
		}
	}
	c.status = connectorwire.Status{State: connectorwire.StateNotConfigured, Error: c.machine + " 不是 Mac，或者上面没有「信息」的数据"}
}

// explain is a failed call in words for the app.
func (c *Remote) explain(err error) string {
	switch {
	case errors.Is(err, ErrOffline):
		return c.machine + " 不在线"
	case errors.Is(err, ErrNodeTooOld):
		return c.machine + " 上的 personad 太旧，更新后才能读「信息」"
	case errors.Is(err, ErrNoPermission):
		return "macOS 不让 Persona 读 " + c.machine + " 上的「信息」：要给它「完全磁盘访问权限」"
	case errors.Is(err, ErrNoChat):
		return "没有这个会话"
	}
	return "读不了 " + c.machine + " 上的「信息」：" + err.Error()
}

// Explain is err in words for the app (the same as Status uses).
func (c *Remote) Explain(err error) string { return c.explain(err) }

var chatID = lazyre.New(`^` + ID + `-[0-9a-f]{12}$`)

// ValidChat says whether id looks like a conversation id: nothing else is sent to the Mac.
func ValidChat(id string) bool { return chatID.MatchString(id) }

// Chats is a page of the Mac's conversations (Reader.Chats there).
func (c *Remote) Chats(ctx context.Context, limit int, before string) (ChatsPage, error) {
	var page ChatsPage
	err := c.read(ctx, "connector.chats", map[string]any{"id": ID, "limit": limit, "before": before}, &page)
	return page, err
}

// Messages is a page of one conversation (Reader.Messages there).
func (c *Remote) Messages(ctx context.Context, chat string, limit int, before string) (MessagesPage, error) {
	if !ValidChat(chat) {
		return MessagesPage{}, ErrNoChat
	}
	var page MessagesPage
	err := c.read(ctx, "connector.older", map[string]any{"id": ID, "chat": chat, "limit": limit, "before": before}, &page)
	return page, err
}

func (c *Remote) read(ctx context.Context, method string, params map[string]any, out any) error {
	err := c.call(ctx, method, params, out)
	if errors.Is(err, ErrNoPermission) || errors.Is(err, ErrNodeTooOld) || errors.Is(err, ErrOffline) {
		c.Refresh() // the status says it too, and the settings page shows the steps
	}
	return err
}
