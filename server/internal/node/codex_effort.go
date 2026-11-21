package node

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Thinking effort of a Codex thread Persona drives through its app-server (docs/integrations/codex.md "Thinking effort"): a session listed
// from an app-server (source_kind codex_app_server) or a herdr pane joined on one (codex_daemon.go). The phone's
// messages are turn/start / turn/steer there, so the effort is the thread's own setting, not keys typed into a TUI:
//
//   - Shown: what the app-server says the thread is configured with (thread/read's reasoningEffort, every refresh, and
//     thread/settings/updated), else what its rollout last ran with, then its model's default from model/list; the
//     choices are what the thread's model takes (model/list's supportedReasoningEfforts), Codex's whole list when the
//     model is not listed.
//   - Set: thread/settings/update, which applies from the next turn whoever starts it (the TUI's too). Only an
//     app-server that answers it with -32601 (method not found: it is experimental in 0.163) gets the choice kept for
//     later (codexPending): it goes with the phone's next turn/start (turn/steer takes none) and stays shown as the
//     session's effort until then. Any other failure (the connection gone, no answer in time, Codex refusing it) is
//     the phone's error, and nothing is kept for a later turn to carry.
//
// Permission modes stay Claude's: a Codex thread's approvals and sandbox are not changed from the phone. Panes on the
// terminal route, subagents, the Codex App's own chats and a compatible CLI keep refusing both (modes.go).

// codexEffortLevels are Codex's reasoning efforts, weakest first: the Codex settings page's choices (codexconfig.go)
// and what a thread offers when its app-server's model/list does not name its model.
var codexEffortLevels = proto.CodexEffortLevels

// codexModelEfforts is what model/list says about a model's efforts.
type codexModelEfforts struct {
	levels []string            // weakest first; empty: it takes none
	def    string              // what it runs with when the thread sets none
	tiers  []proto.ServiceTier // native model/list speed choices
}

// codexEffortThread is the app-server thread of a Codex session the phone drives through the protocol (sendCodex): a
// session an app-server lists (joined when the phone first writes to it) or a herdr pane already joined. Not a pane on
// the terminal route (its messages are typed), not a subagent (its parent drives it).
func (n *Node) codexEffortThread(id string) (*codexRemote, remoteThread, bool) {
	c, t, err := n.codexOwner(id)
	if err != nil || t.parent != "" {
		return nil, remoteThread{}, false
	}
	if a, live := n.agent(id); live && a.Agent == "codex" && !t.joined {
		return nil, remoteThread{}, false
	}
	return c, t, true
}

// efforts are the levels a thread on this app-server may be set to: its model's, from model/list; Codex's whole list
// when the model is unknown or not listed (or the list could not be read). Empty: the model takes no effort.
func (c *codexRemote) efforts(model string) []string {
	c.mu.Lock()
	e, ok := c.models[model]
	c.mu.Unlock()
	if model != "" && ok {
		return e.levels
	}
	return codexEffortLevels
}

// defaultEffort is what a model runs with when its thread sets no effort; "" unknown.
func (c *codexRemote) defaultEffort(model string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.models[model].def
}

// loadModels reads model/list once per connection (hidden models too: a thread may run one) for the efforts each model
// takes. Best effort: without it every thread offers codexEffortLevels.
func (c *codexRemote) loadModels() {
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	models, err := readCodexModels(ctx, c.wire.call)
	if err != nil {
		c.n.logf("codex %s: model/list: %v; threads offer every effort level", c.spec.ID, err)
		return
	}
	c.mu.Lock()
	c.models = models
	c.mu.Unlock()
	c.n.notifyCodex()
}

// Both external and Persona-owned app-servers advertise the same model metadata.
func readCodexModels(ctx context.Context, call func(context.Context, string, any, any) error) (map[string]codexModelEfforts, error) {
	models := map[string]codexModelEfforts{}
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{"includeHidden": true}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res struct {
			Data []struct {
				Model   string              `json:"model"`
				ID      string              `json:"id"`
				Default string              `json:"defaultReasoningEffort"`
				Tiers   []proto.ServiceTier `json:"serviceTiers"`
				Efforts []struct {
					Effort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := call(ctx, "model/list", params, &res); err != nil {
			return nil, err
		}
		for _, m := range res.Data {
			e := []string{}
			for _, x := range m.Efforts {
				if x.Effort != "" && !slices.Contains(e, x.Effort) {
					e = append(e, x.Effort)
				}
			}
			for _, k := range []string{m.Model, m.ID} {
				if k != "" {
					models[k] = codexModelEfforts{levels: e, def: m.Default, tiers: m.Tiers}
				}
			}
		}
		if res.NextCursor == nil || *res.NextCursor == "" || *res.NextCursor == cursor {
			break
		}
		cursor = *res.NextCursor
	}
	return models, nil
}

// codexModelNow is the model a thread is shown with: the phone's choice still waiting for a turn/start (-32601), else
// what the app-server says the thread is configured with, else what its rollout last ran with.
func (n *Node) codexModelNow(id string, c *codexRemote, t remoteThread, rollout string) string {
	if m := n.codexPendingModel(id, c, t); m != "" {
		return m
	}
	return firstNonEmpty(t.model, rollout)
}

// codexEffortNow is the effort a thread is shown with: the phone's choice still waiting for a turn/start, else what
// the app-server says the thread is configured with, else what its rollout last ran with, then its model's default.
func (n *Node) codexEffortNow(id string, c *codexRemote, t remoteThread, rollout string) string {
	if e := n.codexPendingEffort(id, c, t); e != "" {
		return e
	}
	if t.effort != "" {
		return t.effort
	}
	if rollout != "" {
		return rollout
	}
	if m := n.codexModelNow(id, c, t, ""); m != "" { // an app-server that reports the thread's settings, and it sets no effort
		if d := c.defaultEffort(m); d != "" {
			return d
		}
	}
	return rollout
}

// codexPending is the phone's choice of model and/or effort for a thread whose app-server answered
// thread/settings/update with -32601: the phone's next turn/start on it carries the choice. It holds only for the
// connection (conn: its generation) and thread it was made on.
type codexPending struct {
	model, effort, conn, thread string
}

func (n *Node) codexPendingTurn(id string, c *codexRemote, t remoteThread) (model, effort string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	m, ok := n.modes[id]
	if !ok || m.Codex == nil {
		return "", ""
	}
	if p := m.Codex; p.conn == c.generation && p.thread == t.id {
		return p.model, p.effort
	}
	m.Codex = nil
	n.modes[id] = m
	return "", ""
}

func (n *Node) codexPendingModel(id string, c *codexRemote, t remoteThread) string {
	model, _ := n.codexPendingTurn(id, c, t)
	return model
}

// codexPendingEffort is the phone's choice still waiting for a turn/start on thread t of connection c; "" none. A
// choice made on another connection or thread is stale and dropped here.
func (n *Node) codexPendingEffort(id string, c *codexRemote, t remoteThread) string {
	_, effort := n.codexPendingTurn(id, c, t)
	return effort
}

// setCodexPending keeps (p) or drops (nil) the phone's choice waiting for a turn/start.
func (n *Node) setCodexPending(id string, p *codexPending) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.modes == nil && p != nil {
		n.modes = map[string]sessionModes{}
	}
	m, ok := n.modes[id]
	if !ok && p == nil {
		return
	}
	m.Codex = p
	n.modes[id] = m
}

func (n *Node) mergeCodexPending(id, conn, thread, model, effort string, clearEffort bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.modes == nil {
		n.modes = map[string]sessionModes{}
	}
	m := n.modes[id]
	var next codexPending
	if m.Codex != nil && m.Codex.conn == conn && m.Codex.thread == thread {
		next = *m.Codex
	} else {
		next = codexPending{conn: conn, thread: thread}
	}
	if model != "" {
		next.model = model
	}
	if clearEffort {
		next.effort = ""
	} else if effort != "" {
		next.effort = effort
	}
	if next.model == "" && next.effort == "" {
		m.Codex = nil
	} else {
		m.Codex = &next
	}
	n.modes[id] = m
}

// dropCodexPending drops a choice waiting on thread of connection conn (its app-server no longer has the thread
// loaded: the session ended there).
func (n *Node) dropCodexPending(id, conn, thread string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if m, ok := n.modes[id]; ok && m.Codex != nil && m.Codex.conn == conn && m.Codex.thread == thread {
		m.Codex = nil
		n.modes[id] = m
	}
}

// codexEffortRow fills in a Codex session's effort and the levels the phone may pick, when the phone drives it through
// its app-server; m is its rollout's meta (zero without one).
func (n *Node) codexEffortRow(id string, efforts *[]string, effort *string, m quickMeta) {
	// Readback is independent of control support: a terminal-only session still records its actual effort.
	*effort = m.effort
	c, t, ok := n.codexEffortThread(id)
	if !ok {
		return
	}
	model := n.codexModelNow(id, c, t, m.model)
	*efforts = c.efforts(model)
	if len(*efforts) > 0 {
		*effort = n.codexEffortNow(id, c, t, m.effort)
	} else {
		*effort = ""
	}
}

// setCodexEffort sets a thread's effort from the phone (no permission mode: modes.go refuses one first).
func (n *Node) setCodexEffort(ctx context.Context, id string, c *codexRemote, t remoteThread, effort string) error {
	return n.setCodexSettings(ctx, id, c, t, "", effort)
}

// setCodexSettings sets a thread's model and/or effort on its app-server (with -32601 turn/start fallback).
func (n *Node) setCodexSettings(ctx context.Context, id string, c *codexRemote, t remoteThread, model, effort string) error {
	model, effort = strings.TrimSpace(model), strings.TrimSpace(effort)
	if model == "" && effort == "" {
		return nil
	}
	if model != "" && !proto.ValidModelID(model) {
		return fmt.Errorf("invalid model %q", model)
	}
	metaRollout := n.metaOf(id)
	curModel := n.codexModelNow(id, c, t, metaRollout.model)
	targetModel := cmp.Or(model, curModel)

	sendEffort := effort
	clearEffort := false
	if effort != "" {
		levels := c.efforts(targetModel)
		if len(levels) == 0 {
			return fmt.Errorf("这个会话的模型不支持设置思考强度")
		}
		if !slices.Contains(levels, effort) {
			return fmt.Errorf("这个会话的模型不支持思考强度 %s（可选：%s）", effort, strings.Join(levels, " / "))
		}
	} else if model != "" {
		c.mu.Lock()
		meta, known := c.models[model]
		c.mu.Unlock()
		if known {
			if len(meta.levels) == 0 {
				clearEffort = true
			} else if curEffort := n.codexEffortNow(id, c, t, metaRollout.effort); curEffort != "" && !slices.Contains(meta.levels, curEffort) {
				sendEffort = reconcileCodexEffort(meta)
			}
		}
	}

	params := map[string]any{"threadId": t.id}
	if model != "" {
		params["model"] = model
	}
	if sendEffort != "" {
		params["effort"] = sendEffort
	}

	what := "思考强度"
	if model != "" {
		what = "模型"
	}
	call, cancel := context.WithTimeout(ctx, codexEffortTimeout)
	err := c.wire.call(call, "thread/settings/update", params, nil)
	cancel()
	var re *codexError
	isCodex := errors.As(err, &re)
	switch {
	case err == nil:
		n.setCodexPending(id, nil)
		c.noteThreadSettings(t.id, model, sendEffort, sendEffort != "" || clearEffort)
	case isCodex && re.Code == -32601:
		// An app-server without the method: the phone's next turn/start on this thread carries the choice.
		n.logf("codex %s: thread/settings/update: %v; settings go with the next message from the phone", id, err)
		n.mergeCodexPending(id, c.generation, t.id, model, sendEffort, clearEffort)
	default:
		// Not set: an earlier choice still waiting is dropped too, so no later turn/start carries what the phone was
		// just told did not take. What the thread runs with stays what the session shows (its next read).
		n.logf("codex %s: thread/settings/update: %v", id, err)
		n.setCodexPending(id, nil)
		n.notifyCodex()
		switch {
		case isCodex && re.Code == -32602:
			return refusal("codex_refused", "Codex 没有接受这个"+what+"："+re.Message, nil)
		case isCodex:
			return refusal("codex_refused", what+"没有改成：Codex 回复「"+re.Message+"」", nil)
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			return fmt.Errorf("%s可能没有改成：Codex app-server 没有及时回应，会话里显示的是它现在的设置", what)
		default:
			return fmt.Errorf("%s没有改成：和这个会话的 Codex app-server 断开了，稍后再试", what)
		}
	}
	n.notifyCodex()
	return nil
}

// codexEffortTimeout bounds thread/settings/update: a settings change answers at once, and the phone waits for it.
const codexEffortTimeout = 15 * time.Second

// metaOf is a session's transcript (rollout) meta, zero without one.
func (n *Node) metaOf(id string) quickMeta {
	if p := n.transcriptPath(id); p != "" {
		if st, err := os.Stat(p); err == nil {
			return n.meta(p, st)
		}
	}
	return quickMeta{}
}

// codexTurnSettingsTaken: a turn/start carried the pending model and/or effort; the thread runs with them from now on.
func (n *Node) codexTurnSettingsTaken(id string, c *codexRemote, thread, model, effort string) {
	n.mu.Lock()
	if m, ok := n.modes[id]; ok && m.Codex != nil {
		if model != "" && m.Codex.model == model {
			m.Codex.model = ""
		}
		if effort != "" && m.Codex.effort == effort {
			m.Codex.effort = ""
		}
		if m.Codex.model == "" && m.Codex.effort == "" {
			m.Codex = nil
		}
		n.modes[id] = m
	}
	n.mu.Unlock()
	c.noteThreadSettings(thread, model, effort, effort != "")
}

// codexEffortTaken: a turn/start carried the pending effort; the thread runs with it from now on.
func (n *Node) codexEffortTaken(id string, c *codexRemote, thread, effort string) {
	n.codexTurnSettingsTaken(id, c, thread, "", effort)
}

// noteThreadSettings records a thread's configured model and/or effort as this connection last learned them.
func (c *codexRemote) noteThreadSettings(thread, model, effort string, setEffort bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.threads[thread]
	if !ok {
		return
	}
	changed := false
	if model != "" && t.model != model {
		t.model = model
		changed = true
	}
	if setEffort && t.effort != effort {
		t.effort = effort
		changed = true
	}
	if changed {
		t.revision++
		c.threads[thread] = t
	}
}

// noteEffort records a thread's configured effort as this connection last learned it.
func (c *codexRemote) noteEffort(thread, effort string) {
	c.noteThreadSettings(thread, "", effort, true)
}
