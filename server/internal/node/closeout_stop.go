package node

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/closeout"
)

// closeoutStop holds an agent's stop once when its turn did work and its last reply has no closeout
// (docs/assistant/agent-work.md "Closeout"). Never twice in a row (stop_hook_active), never for an assistant
// conversation or a subagent, and never when the reply or the turn cannot be read: a stop held by mistake costs the
// user a turn, a closeout missed costs nothing new.
func (n *Node) closeoutStop(parent context.Context, req sockRequest, in hookInput) any {
	if req.ReportOnly || in.HookEventName != "Stop" || in.AgentID != "" || in.StopHookActive || n.peer == nil {
		return nil
	}
	reply := in.LastAssistantMessage
	if trimmed := strings.TrimSpace(reply); trimmed == "" || strings.HasPrefix(trimmed, "API Error") || closeout.Has(reply) || n.isAssistantSession(in.SessionID) {
		return nil
	}
	if !turnWorked(in.TranscriptPath) {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	var result assistantwire.CloseoutCheckResponse
	if err := n.peer.Call(ctx, assistantwire.MethodCloseoutCheck, assistantwire.CloseoutCheckRequest{SessionID: in.SessionID}, &result); err != nil || result.Reason == "" {
		return nil
	}
	return map[string]any{"decision": "block", "reason": result.Reason}
}

// closeoutTail is how much of a transcript's end turnWorked reads: a turn longer than this is taken by what its
// last part did.
const closeoutTail = 2 << 20

// workTools change files or run commands: a turn that called one did work worth a closeout. Reading and searching
// alone is answering a question.
var workTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true, "Bash": true}

// turnWorked reports whether the latest turn in a Claude Code transcript called a tool that changes files or runs
// commands. False when the file cannot be read.
func turnWorked(path string) bool {
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > closeoutTail {
		if _, err := f.Seek(-closeoutTail, io.SeekEnd); err != nil {
			return false
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, closeoutTail))
	if err != nil {
		return false
	}
	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		var l struct {
			Type        string `json:"type"`
			IsSidechain bool   `json:"isSidechain"`
			IsMeta      bool   `json:"isMeta"`
			Message     struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if len(lines[i]) == 0 || json.Unmarshal(lines[i], &l) != nil || l.IsSidechain || l.IsMeta {
			continue // a cut first line, a subagent's line or the CLI's own note
		}
		var blocks []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		switch l.Type {
		case "assistant":
			if json.Unmarshal(l.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type == "tool_use" && workTools[b.Name] {
					return true
				}
			}
		case "user":
			// A tool's result is a user line too; the turn starts at the one the user (or a message sent to it) wrote.
			if json.Unmarshal(l.Message.Content, &blocks) != nil {
				return false // plain text: the turn's prompt
			}
			for _, b := range blocks {
				if b.Type != "tool_result" {
					return false
				}
			}
		}
	}
	return false
}
