package node

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func (b *cindyBridge) request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var p struct {
		SessionID      string        `json:"session_id"`
		Limit          int           `json:"limit"`
		BeforeSeq      *int          `json:"before_seq"`
		Text           string        `json:"text"`
		Images         []proto.Image `json:"images"`
		ClientMsgID    string        `json:"client_msg_id"`
		PermissionMode string        `json:"permission_mode"`
		Effort         string        `json:"effort"`
		Cwd            string        `json:"cwd"`
		Prompt         string        `json:"prompt"`
		Launcher       string        `json:"launcher"`
		Model          string        `json:"model"`
		WorkspaceID    string        `json:"workspace_id"`
		Resume         string        `json:"resume"`
		OpenElsewhere  bool          `json:"open_elsewhere"`
	}
	_ = json.Unmarshal(params, &p)
	if (method == "session.send" || method == "session.start") && len(p.Images) > 0 {
		return nil, &rpc.Error{Code: "image_input_unsupported", Message: "Cindy does not support image input"}
	}
	ok := map[string]any{"ok": true}

	switch method {
	case "session.watch":
		b.mu.Lock()
		b.watched[p.SessionID] = true
		b.mu.Unlock()
		return ok, nil
	case "session.unwatch":
		b.mu.Lock()
		delete(b.watched, p.SessionID)
		b.mu.Unlock()
		return ok, nil
	case "session.start":
		return b.startSession(ctx, p.Cwd, p.Prompt, p.Launcher, p.Model, p.PermissionMode, p.Effort, p.WorkspaceID, p.Resume, p.OpenElsewhere)
	}

	b.mu.Lock()
	cindyID := b.cindyOf[p.SessionID]
	b.mu.Unlock()
	if cindyID == "" {
		return nil, &rpc.Error{Code: "session_gone", Message: "这个会话已经不在 Cindy 里了"}
	}

	if method == "session.items" {
		return b.sessionItems(ctx, cindyID, p.Limit, p.BeforeSeq)
	}
	if !b.writable() {
		return nil, &rpc.Error{Code: "runtime_away", Message: "Cindy 服务没有在运行"}
	}

	switch method {
	case "session.send":
		cid := p.ClientMsgID
		if cid == "" {
			cid = newUUID()
		}
		var steerRes struct {
			OK bool `json:"ok"`
		}
		if b.postJSON(ctx, "/api/input/steer", map[string]any{"sessionId": cindyID, "item": map[string]any{"text": p.Text}}, &steerRes) == nil && steerRes.OK {
			return ok, nil
		}
		err := b.postJSON(ctx, "/api/input/enqueue", map[string]any{
			"sessionId": cindyID,
			"item": map[string]any{
				"clientId":         cid,
				"text":             p.Text,
				"persistedContent": p.Text,
			},
		}, nil)
		if err != nil {
			return nil, err
		}
		return ok, nil
	case "session.interrupt":
		if err := b.postJSON(ctx, "/api/input/stop", map[string]any{"sessionId": cindyID}, nil); err != nil {
			return nil, err
		}
		return ok, nil
	case "session.close":
		if err := b.postJSON(ctx, "/api/sessions/update", map[string]any{
			"sessionId": cindyID,
			"patch":     map[string]any{"status": "archived"},
		}, nil); err != nil {
			return nil, err
		}
		return ok, nil
	case "session.modes":
		patch := map[string]any{}
		if p.PermissionMode != "" {
			cm := personaToCindyMode[p.PermissionMode]
			if cm == "" {
				return nil, &rpc.Error{Code: "unsupported", Message: "unsupported: Cindy 没有这个权限模式（" + p.PermissionMode + "）"}
			}
			patch["permissionMode"] = cm
			patch["planModeEnabled"] = p.PermissionMode == "plan"
		}
		if p.Effort != "" {
			patch["effort"] = p.Effort
		}
		if len(patch) > 0 {
			if err := b.postJSON(ctx, "/api/sessions/update", map[string]any{"sessionId": cindyID, "patch": patch}, nil); err != nil {
				return nil, err
			}
		}
		return ok, nil
	default:
		return nil, &rpc.Error{Code: "unsupported", Message: "unsupported: " + method}
	}
}

func (b *cindyBridge) startSession(ctx context.Context, cwd, prompt, launcher, model, mode, effort, workspaceID, resume string, openElsewhere bool) (any, error) {
	if !b.writable() {
		return nil, &rpc.Error{Code: "runtime_away", Message: "Cindy 服务没有在运行"}
	}
	if resume != "" {
		b.mu.Lock()
		cindyID := b.cindyOf[resume]
		row := b.rowByID[cindyID]
		b.mu.Unlock()
		if openElsewhere && cindyID == "" {
			return nil, &rpc.Error{Code: openElsewhereCode, Message: "这个会话正在别处运行"}
		}
		if cindyID == "" {
			cindyID = resume
			createReq := map[string]any{
				"id":         cindyID,
				"workingDir": firstOf(cwd, workspaceID),
				"agentKind":  "claude-code",
			}
			if model != "" {
				createReq["model"] = model
			}
			if effort != "" {
				createReq["effort"] = effort
			}
			if cm := personaToCindyMode[mode]; cm != "" {
				createReq["permissionMode"] = cm
				createReq["planModeEnabled"] = mode == "plan"
			}
			_ = b.postJSON(ctx, "/api/sessions/create", createReq, &row)
		} else {
			_ = b.postJSON(ctx, "/api/sessions/update", map[string]any{
				"sessionId": cindyID,
				"patch":     map[string]any{"status": "active"},
			}, &row)
		}
		if strings.TrimSpace(prompt) != "" {
			_ = b.postJSON(ctx, "/api/input/enqueue", map[string]any{
				"sessionId": cindyID,
				"item": map[string]any{
					"clientId":         newUUID(),
					"text":             prompt,
					"persistedContent": prompt,
				},
			}, nil)
		}
		b.mu.Lock()
		b.cindyOf[resume] = cindyID
		b.mu.Unlock()
		return map[string]any{
			"session": map[string]any{
				"session_id": resume,
				"title":      firstOf(row.Title, filepath.Base(cwd), "Cindy"),
				"cwd":        firstOf(row.WorkingDir, cwd),
				"status":     proto.StatusIdle,
				"agent":      "claude",
				"transcript": resume,
			},
		}, nil
	}

	agentKind := "claude-code"
	switch {
	case strings.HasSuffix(launcher, "-codex") || launcher == "codex":
		agentKind = "codex"
	case strings.HasSuffix(launcher, "-pi") || launcher == "pi":
		agentKind = "pi"
	}
	dir := firstOf(cwd, workspaceID)
	createReq := map[string]any{
		"workingDir": dir,
		"agentKind":  agentKind,
	}
	if model != "" {
		createReq["model"] = model
	}
	if effort != "" {
		createReq["effort"] = effort
	}
	if cm := personaToCindyMode[mode]; cm != "" {
		createReq["permissionMode"] = cm
		createReq["planModeEnabled"] = mode == "plan"
	}
	var created cindyDBRow
	if err := b.postJSON(ctx, "/api/sessions/create", createReq, &created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, errors.New("Cindy did not return a session id")
	}
	if strings.TrimSpace(prompt) != "" {
		if err := b.postJSON(ctx, "/api/input/enqueue", map[string]any{
			"sessionId": created.ID,
			"item": map[string]any{
				"clientId":         newUUID(),
				"text":             prompt,
				"persistedContent": prompt,
			},
		}, nil); err != nil {
			return nil, err
		}
	}
	mapped, sid := mapCindySessionRow(created, strings.TrimSpace(prompt) != "", false, nil)
	if sid == "" {
		sid = "cindy-" + created.ID
		mapped = map[string]any{
			"session_id": sid,
			"title":      firstOf(created.Title, filepath.Base(dir), "Cindy"),
			"cwd":        dir,
			"status":     proto.StatusIdle,
			"agent":      cindyAgentName(agentKind),
		}
	}
	b.mu.Lock()
	b.cindyOf[sid] = created.ID
	b.rowByID[created.ID] = created
	b.mu.Unlock()
	return map[string]any{"session": mapped}, nil
}

const openElsewhereCode = "open_elsewhere"
