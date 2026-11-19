package node

import "github.com/nwverse/persona/server/internal/proto"

const (
	originInbox = "inbox"
	// inboxLarkRefusal is deliberately actionable: the model has a supported way to finish its turn without sending.
	inboxLarkRefusal = "Inbox conversations cannot make outbound Lark calls directly; use inbox_suggest so the owner can confirm the action."
)

// assistantOrigin is the manager-recorded origin for a model call. Session state covers Claude from before process
// start and every resumed conversation; the process record covers Codex while its proc-* key is becoming a thread id.
func (n *Node) assistantOrigin(id string) string {
	if n.state != nil {
		if st, ok := n.state.get(id); ok && st.Origin != "" {
			return st.Origin
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	read := func(p *assistantProc) string {
		if p == nil {
			return ""
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.origin
	}
	if p := n.procs[id]; p != nil {
		return read(p)
	}
	if p := n.akeys[id]; p != nil {
		return read(p)
	}
	if p := n.spare; p != nil && p.id == id {
		return read(p)
	}
	for _, p := range n.akeys {
		if p != nil && (p.id == id || p.cx != nil && p.cx.key == id) {
			return read(p)
		}
	}
	return ""
}

// inboxLarkOutbound says whether a parsed command can make a Lark change or is too opaque to prove read-only. It runs
// before configured allows, bypass-safe bot send/reply, and phone confirmation: none of those is an Inbox confirmation.
func inboxLarkOutbound(cfg proto.AssistantLark, v larkVerdict, command string) bool {
	for _, c := range v.calls {
		if !larkRead(c) {
			return true
		}
	}
	if v.cliWrite || v.unknown {
		return true
	}
	if _, write := gateWrite(cfg.Gates, command); write {
		return true
	}
	// A command substitution or unparsed fragment associated with a gated call cannot be shown to be read-only.
	return (v.unsure || v.unparsed) && (len(v.calls) > 0 || larkCLIRe.MatchString(command))
}
