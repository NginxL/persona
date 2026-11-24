package node

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Shared-server observers never auto-answer, including errors, declines, timeouts and unsupported requests.
func (c *codexRemote) request(m codexMessage) {
	var r codexRequest
	if json.Unmarshal(m.Params, &r) != nil {
		return
	}
	key := codexRequestKey(m.ID)
	if key == "" {
		return
	}
	c.mu.Lock()
	t, ok := c.threads[r.ThreadID]
	if !c.ready || !ok || !t.joined || c.finished[key] || c.asks[key] != nil {
		c.mu.Unlock()
		return
	}
	paths := append([]string(nil), t.files[r.ItemID]...)
	c.mu.Unlock()
	c.n.mu.Lock()
	machineID := c.n.machineID
	c.n.mu.Unlock()
	ix := proto.Interaction{ID: remoteInteractionID(machineID, c.spec.ID, c.generation, r.ThreadID, m.ID), SessionID: t.publicID, Cwd: firstNonEmpty(r.Cwd, t.cwd), SessionTitle: t.title, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(nodeclient.HookTimeout).UTC(), ToolUseID: r.ItemID,
		Agent: "codex", Reason: strings.TrimSpace(r.Reason)}
	if c.n.sessionBlocked(ix.SessionID, ix.Cwd) {
		return // a blocked Agent folder (agentdirs.go): no card; an observer never answers, Codex's own client decides
	}
	var response func(proto.Resolution) (any, bool)
	switch m.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		ix.Kind = proto.IxPermission
		if m.Method == "item/commandExecution/requestApproval" {
			ix.ToolName, ix.AlwaysLabel = "Bash", "本会话不再问这个命令"
			ix.Summary = unwrapShell(r.Command)
			ix.ToolInput, _ = json.Marshal(map[string]any{"command": unwrapShell(r.Command)})
		} else {
			ix.ToolName, ix.AlwaysLabel = "Edit", "本会话不再问这些文件"
			ix.Summary = strings.Join(paths, "\n")
			ix.ToolInput, _ = json.Marshal(map[string]any{"file_path": firstNonEmpty(append(paths, r.GrantRoot)...)})
			if ix.Summary == "" {
				ix.Summary = firstNonEmpty(r.GrantRoot, "修改文件")
			}
		}
		response = func(res proto.Resolution) (any, bool) {
			if res.Allow == nil {
				return nil, false
			}
			d := "decline"
			if *res.Allow {
				d = "accept"
				if res.Always {
					d = "acceptForSession"
				}
			}
			return map[string]any{"decision": d}, true
		}
	case "item/permissions/requestApproval":
		ix.Kind, ix.ToolName, ix.Summary, ix.ToolInput = proto.IxPermission, "权限", "Codex 要更多权限："+compactJSON(r.Permissions), r.Permissions
		ix.AlwaysLabel = "本会话都给这些权限"
		response = func(res proto.Resolution) (any, bool) {
			if res.Allow == nil {
				return nil, false
			}
			var p map[string]any
			scope := "turn"
			if *res.Allow {
				_ = json.Unmarshal(r.Permissions, &p)
				for k, v := range p {
					if v == nil {
						delete(p, k)
					}
				}
				if res.Always {
					scope = "session"
				}
			}
			if p == nil {
				p = map[string]any{}
			}
			return map[string]any{"permissions": p, "scope": scope}, true
		}
	case "item/tool/requestUserInput":
		ix.Kind, ix.ToolName, ix.CanCancel = proto.IxQuestion, "request_user_input", true
		for _, q := range r.Questions {
			qq := proto.Question{Question: q.Question, Header: q.Header}
			for _, o := range q.Options {
				qq.Options = append(qq.Options, proto.QuestionOption{Label: o.Label, Description: o.Description})
			}
			ix.Questions = append(ix.Questions, qq)
		}
		response = func(res proto.Resolution) (any, bool) {
			answers := map[string]any{}
			if res.Cancel {
				return map[string]any{"answers": answers}, true
			}
			for _, q := range r.Questions {
				a, ok := res.Answers[q.Question]
				if !ok {
					return nil, false
				}
				answers[q.ID] = map[string]any{"answers": []string{a}}
			}
			return map[string]any{"answers": answers}, len(answers) > 0
		}
	case "mcpServer/elicitation/request":
		if r.Meta.Kind != "mcp_tool_call" {
			return
		}
		ix.Kind, ix.ToolName, ix.Summary = proto.IxPermission, "mcp__"+r.ServerName, r.Message
		ix.ToolInput, _ = json.Marshal(r.Meta.ToolParams)
		response = func(res proto.Resolution) (any, bool) {
			if res.Allow == nil {
				return nil, false
			}
			action := "decline"
			var content any
			if *res.Allow {
				action = "accept"
				content = map[string]any{}
			}
			return map[string]any{"action": action, "content": content, "_meta": nil}, true
		}
	default:
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	ask := &remoteAsk{cancel: cancel, thread: r.ThreadID, interaction: ix.ID}
	c.mu.Lock()
	if !c.ready || c.finished[key] || c.asks[key] != nil {
		c.mu.Unlock()
		cancel()
		return
	}
	c.asks[key] = ask
	c.mu.Unlock()
	go func() {
		defer cancel()
		if ctx.Err() != nil {
			return
		}
		res, ok, err := c.n.ask(ctx, ix)
		if err == nil && ok && !res.Dismiss {
			c.mu.Lock()
			waitReady := c.threads[r.ThreadID].ready
			c.mu.Unlock()
			if waitReady != nil {
				select {
				case <-waitReady:
				case <-ctx.Done():
				}
			}
		}
		c.mu.Lock()
		current := c.asks[key] == ask
		if current {
			delete(c.asks, key)
		}
		cur := c.threads[r.ThreadID]
		ready := c.ready && cur.joined && !cur.provisional
		c.mu.Unlock()
		if !current || !ready || ctx.Err() != nil || err != nil || !ok || res.Dismiss {
			return
		}
		owner, _, err := c.n.codexOwner(t.publicID)
		if err != nil || owner != c {
			return
		}
		result, valid := response(res)
		if !valid {
			return
		}
		c.mu.Lock()
		c.finished[key] = true
		c.mu.Unlock()
		c.wire.reply(m.ID, result)
		// A refusal with the user's words: Codex's decline carries none, so they go into the turn after it (as the
		// assistant's tellModel does).
		if ix.Kind == proto.IxPermission && res.Allow != nil && !*res.Allow && strings.TrimSpace(res.Message) != "" {
			go func() {
				sctx, scancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer scancel()
				if err := c.n.sendCodex(sctx, t.publicID, prompts.RenderRefused(strings.TrimSpace(res.Message)), nil); err != nil {
					c.n.logf("codex %s: pass the refusal on: %v", t.publicID, err)
				}
			}()
		}
	}()
}

// compactJSON is a raw JSON value on one line ("" for none).
func compactJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}
