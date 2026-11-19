package node

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// ---- a turn whose compactions make no room (#10278) ----
//
// When what survives a compaction (instructions, tool declarations, the retained request) is already above the model's
// compaction limit, Codex compacts before every step, the next request is as large as the last, and the turn never
// ends: one recorded turn compacted 42 times in 31 minutes, each request after a compaction 166–173k tokens against a
// 124,518-token window. Codex reports both halves to the app-server's client: the compaction, and every request's
// usage with the model's window (thread/tokenUsage/updated).

// codexCompactionLoop is what the turn running has shown of its compactions. Under codexConn.mu.
type codexCompactionLoop struct {
	compacted bool   // an automatic compaction came, and no request has reported its usage since
	fruitless int    // compactions in a row after which the next request still filled the window
	stop      string // the reason the turn is being stopped, for its turn/completed; "" when it is not
	model     string // the model the stopped turn ran on
}

const (
	// codexFruitlessCompactions: the compactions in a row that made no room before the turn is stopped. One can be a
	// compaction that just fell short; a second one in a row is the loop.
	codexFruitlessCompactions = 2
	// codexNoRoomPercent: a request after a compaction that takes this much of the window made no room. A compaction
	// that works leaves a summary and the fixed part, far below it.
	codexNoRoomPercent = 90
	// codexLoopInterruptTimeout bounds the interrupt the loop sends.
	codexLoopInterruptTimeout = 15 * time.Second
)

// compactedInTurn notes an automatic compaction in the turn running; the next request's usage says whether it made
// room. The user's own compaction (manual) runs no turn of theirs and is not counted.
func (c *codexConn) compactedInTurn(manual bool) {
	if manual {
		return
	}
	c.mu.Lock()
	if c.turnID != "" {
		c.loop.compacted = true
	}
	c.mu.Unlock()
}

// noteRequest reads a request's usage (thread/tokenUsage/updated): input is what it sent, window the model's. After
// an automatic compaction, a request at codexNoRoomPercent of the window or more made no room; the
// codexFruitlessCompactions-th in a row stops the turn.
func (c *codexConn) noteRequest(input, window int64, model string) {
	if input <= 0 || window <= 0 {
		return // the reset Codex reports with a compaction, or a provider that gives no window
	}
	c.mu.Lock()
	if !c.loop.compacted || c.turnID == "" {
		c.mu.Unlock()
		return
	}
	c.loop.compacted = false
	if input*100 < window*codexNoRoomPercent {
		c.loop.fruitless = 0
		c.mu.Unlock()
		return
	}
	c.loop.fruitless++
	stop := c.loop.fruitless >= codexFruitlessCompactions && c.loop.stop == ""
	if stop {
		c.loop.stop = i18n.T(i18n.Default, "node-codex-assistant.compaction_loop_stopped")
		c.loop.model = model
	}
	c.mu.Unlock()
	if stop {
		go c.stopCompactionLoop(input, window, model)
	}
}

// stopCompactionLoop interrupts the turn, as Stop does: its turn/completed then ends it with the loop's reason.
func (c *codexConn) stopCompactionLoop(input, window int64, model string) {
	n := c.p.ownerOr(c.n)
	n.logger.Printf("assistant codex %s: %d compactions in a row left the next request at %d of %d tokens on %s: stopping the turn",
		short(c.p.id), codexFruitlessCompactions, input, window, model)
	ctx, cancel := context.WithTimeout(context.Background(), codexLoopInterruptTimeout)
	defer cancel()
	if err := c.interrupt(ctx); err != nil {
		n.logger.Printf("assistant codex %s: stop the compaction loop: %v", short(c.p.id), err)
	}
}

// endTurnLoop is the reason the loop stopped the turn ending now, "" when it did not, and the model it ran on; the
// next turn starts counting afresh.
func (c *codexConn) endTurnLoop() (stop, model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stop, model = c.loop.stop, c.loop.model
	c.loop = codexCompactionLoop{}
	return stop, model
}

// leaveLoopingFallback drops the model recovery's switch to model when the loop stopped a turn on it: the persisted
// fallback would otherwise run the next message on the same model (applyModelRecoveryReturn) and loop again. The next
// message goes to the configured model, and a new chain starts from there if it still fails.
func (n *Node) leaveLoopingFallback(id, model string) {
	r := n.modelRecovery(id)
	if !modelRecoverySwitched(r) || model == "" || r.To != model {
		return
	}
	n.logger.Printf("assistant %s: leaving fallback %s, whose window cannot hold the conversation", short(id), model)
	n.releaseModelRecovery(id, r.From)
}

// turnModel is the model the turn running is on: the one the node started the process with, else the one Codex last
// said the thread runs (a launcher default).
func (c *codexConn) turnModel() string {
	c.p.mu.Lock()
	model := c.p.model.Model
	c.p.mu.Unlock()
	if model != "" {
		return model
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.speedModel
}
