package node

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/identity"
)

// closeoutStartWait bounds the one ask a starting session makes of the manager: Claude Code waits for its
// SessionStart hooks before the first prompt runs, so a manager that does not answer costs the start no more than
// this, and the session then learns the form only if a stop of its is held (closeout_stop.go).
const closeoutStartWait = 1500 * time.Millisecond

// closeoutBriefs keeps the manager's answer per session: SessionStart fires again when a session is resumed, cleared
// or compacted, each of which needs the context again and none of which is worth another ask.
type closeoutBriefs struct {
	mu   sync.Mutex
	told map[string]string // session id → the context ("" when the manager said none)
}

// closeoutBriefsMost bounds the answers kept; past it they are all dropped and asked for again.
const closeoutBriefsMost = 512

func (b *closeoutBriefs) get(id string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	text, ok := b.told[id]
	return text, ok
}

func (b *closeoutBriefs) put(id, text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.told == nil || len(b.told) >= closeoutBriefsMost {
		b.told = map[string]string{}
	}
	b.told[id] = text
}

// closeoutStart is what a SessionStart hook answers so that the agent knows the closeout's form before it works
// (docs/assistant/agent-work.md "Closeout"): the manager's text as the session's additional context, or nil. Only
// for an agent in a pane with Persona's own hooks: never a session that only reports (its hook is async and prints
// nothing), a subagent, or an assistant conversation (headless, or in an assistant directory). The manager decides
// by its switch and is asked until it gives a text, then no more for that session; no answer in closeoutStartWait is no context, and is asked again at
// the session's next start.
func (n *Node) closeoutStart(parent context.Context, req sockRequest, in hookInput) any {
	if req.ReportOnly || in.HookEventName != "SessionStart" || in.AgentID != "" || in.SessionID == "" || req.Pane == "" || n.peer == nil {
		return nil
	}
	if identity.IsAssistantDir(in.Cwd) || n.isAssistantSession(in.SessionID) {
		return nil
	}
	if st, ok := n.state.get(in.SessionID); ok && (strings.Contains(st.TaskInstructions, "<closeout") || closeout.HasMarkdownInstructions(st.TaskInstructions)) {
		return nil
	}
	text, ok := n.closeoutBriefs.get(in.SessionID)
	if !ok {
		ctx, cancel := context.WithTimeout(parent, closeoutStartWait)
		defer cancel()
		var result assistantwire.CloseoutBriefResponse
		if err := n.peer.Call(ctx, assistantwire.MethodCloseoutBrief, assistantwire.CloseoutBriefRequest{SessionID: in.SessionID, Cwd: in.Cwd}, &result); err != nil {
			return nil
		}
		text = result.Context
		if text != "" { // "none" is not kept: the switch may be turned on while the session lives
			n.closeoutBriefs.put(in.SessionID, text)
		}
	}
	if text == "" {
		return nil
	}
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "SessionStart", "additionalContext": text}}
}
