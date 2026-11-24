package extmgr

import (
	"context"
	"net/http"
	"net/netip"

	"github.com/nwverse/persona/server/internal/botport"
)

// Shortcuts the tests use into the manager's own code paths, with the defaults a test wants. Production calls the
// functions underneath (removeChatRuleKey, feishuReplyUUIDIn, botNoteIn, the inbox's kicks) with its own arguments, and
// reads the caller's address from principalOf.

// removeChatRule is removeChatRuleKey by the rule's id (connector.UserRule.ID) rather than its key; false when there
// was none.
func (h *Manager) removeChatRule(ctx context.Context, id string) (bool, error) {
	h.userRules.mu.Lock()
	defer h.userRules.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, chatRuleStoreTime)
	defer cancel()
	return h.removeLocked(ctx, id)
}

// feishuReplyUUID is feishuReplyUUIDIn for a reply that does not go into a thread.
func feishuReplyUUID(messageID, kind, content string) string {
	return feishuReplyUUIDIn(messageID, kind, content, false)
}

// botNote is botNoteIn not in a thread.
func (h *Manager) botNote(ctx context.Context, api botport.Client, chatID, messageID, text string) {
	h.botNoteIn(ctx, api, chatID, messageID, text, false)
}

// kickLarkChats asks for a collection of the Feishu chats soon (a Feishu realtime event arrived).
func (h *Manager) kickLarkChats() {
	h.inbox.init()
	h.inbox.signal()
}

// newMachine is a computer (tests put it in h.machines, h.byID).
func newMachine(_ *Manager, cfg MachineConfig) *Machine { return &Machine{cfg: cfg} }

// remoteIP is the caller's address the hub saw.
func remoteIP(r *http.Request) netip.Addr {
	if a := principalOf(r.Context()).Remote; a.IsValid() {
		return a
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr()
}
