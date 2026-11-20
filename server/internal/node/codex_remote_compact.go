package node

import (
	"context"
	"errors"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// The user's /compact to a Codex session Persona drives through its app-server (#10188, docs/integrations/codex.md
// "Sessions on the daemon (app-server route)"). The app-server runs no slash commands: a turn's text is the model's
// input, and the TUI runs its own commands before it calls the app-server. Sent as a turn, /compact was answered by
// the model as words. It goes as thread/compact/start instead, the call the TUI's /compact makes and the assistant's
// own compaction uses (assistant_compact.go): Codex writes the rollout's compacted line and its ContextCompaction item,
// which the session shows as it does a terminal's compaction, and no user message.

// codexCompactCommand: text is Codex's /compact and nothing else, with no photos. A skill would be named in the text,
// so none goes with it either.
func codexCompactCommand(text string, images []proto.Image) bool {
	return len(images) == 0 && strings.TrimSpace(text) == "/compact"
}

// compactCodex compacts the thread of session id, which its app-server runs and Persona has joined, while no turn runs
// in it. Codex's compaction is a task of its own that takes the place of a running turn instead of waiting for it,
// and the TUI refuses /compact mid-turn too. So a thread the node saw busy is refused, and one it saw idle is read from
// the app-server just before: a turn begun since the node last heard is not cut short. Refused, nothing is sent; it is
// never steered into the turn.
func (n *Node) compactCodex(ctx context.Context, id string) error {
	c, t, err := n.codexOwner(id)
	if err != nil {
		return refusal("codex_read_only", err.Error(), nil)
	}
	busy := t.status == proto.StatusWorking || t.status == proto.StatusBlocked
	if !busy {
		var reply struct {
			Thread remoteThreadData `json:"thread"`
		}
		if err := c.wire.call(ctx, "thread/read", map[string]any{"threadId": t.id, "includeTurns": false}, &reply); err != nil {
			return refusal("codex_read_only", "没能读到会话在 app-server 上的状态，没有压缩："+err.Error(), nil)
		}
		busy = reply.Thread.Status.Type == "active"
	}
	if busy {
		return refusal("codex_compact_busy", "Codex 正在运行这一轮，等它结束再压缩；这次什么都没发", nil)
	}
	err = c.wire.call(ctx, "thread/compact/start", map[string]any{"threadId": t.id}, nil)
	var re *codexError
	if errors.As(err, &re) {
		if re.Code == -32601 || re.Code == -32602 { // an app-server without it, or that does not take it for this thread
			return refusal("codex_refused", re.Error(), nil)
		}
		return uncertain(re)
	}
	if err != nil {
		return uncertain(err)
	}
	n.logf("codex %s: compacting, as the user asked", short(id))
	n.notifyCodex()
	return nil
}
