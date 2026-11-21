package node

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func (b *t3codeBridge) request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string        `json:"session_id"`
		Limit     int           `json:"limit"`
		BeforeSeq *int          `json:"before_seq"`
		AfterSeq  *int          `json:"after_seq"`
		Text      string        `json:"text"`
		Images    []proto.Image `json:"images"`
		Cwd       string        `json:"cwd"`
		Prompt    string        `json:"prompt"`
		Resume    string        `json:"resume"`
	}
	_ = json.Unmarshal(params, &p)
	if (method == "session.send" || method == "session.start") && len(p.Images) > 0 {
		return nil, &rpc.Error{Code: "image_input_unsupported", Message: "T3 Code does not take pictures from Persona"}
	}
	ok := map[string]any{"ok": true}

	switch method {
	case "session.watch", "session.unwatch":
		return ok, nil // the session list's rev says when to read again
	case "session.start":
		if p.Resume != "" {
			if sid, threadID := b.known(p.Resume); threadID != "" {
				return b.resumed(ctx, sid, threadID, p.Prompt)
			}
			return nil, &rpc.Error{Code: "unsupported", Message: "unsupported: 只能接着 T3 Code 里还在的线程"}
		}
		return b.startThread(ctx, p.Cwd, p.Prompt)
	}

	b.mu.Lock()
	threadID := b.threadOf[p.SessionID]
	b.mu.Unlock()
	if threadID == "" {
		return nil, &rpc.Error{Code: "session_gone", Message: "这个会话已经不在 T3 Code 里了"}
	}

	switch method {
	case "session.items":
		return b.sessionItems(ctx, threadID, p.Limit, p.BeforeSeq, p.AfterSeq)
	case "session.send":
		if strings.TrimSpace(p.Text) == "" {
			return ok, nil
		}
		if _, err := t3codeRun(ctx, p.Text, "thread", "send", "--json", threadID, "-"); err != nil {
			return nil, err
		}
		b.wakeSoon()
		return ok, nil
	case "session.interrupt":
		var res struct {
			Status string `json:"status"`
		}
		err := b.mcp.call(ctx, "t3_thread_interrupt", map[string]any{"threadId": threadID, "stop": true}, &res)
		var te *t3codeToolError
		if errors.As(err, &te) && strings.HasSuffix(te.Code, "_escalation_denied") {
			return nil, &rpc.Error{Code: "unsupported", Message: "unsupported: 这个线程的权限比 Persona 拿到的高，请在 T3 Code 里停下它"}
		}
		if err != nil {
			return nil, err
		}
		b.wakeSoon()
		return ok, nil
	case "session.close":
		if err := b.mcp.call(ctx, "t3_thread_organize", map[string]any{"threadId": threadID, "action": "archive"}, nil); err != nil {
			return nil, err
		}
		b.wakeSoon()
		return ok, nil
	default:
		return nil, &rpc.Error{Code: "unsupported", Message: "unsupported: " + method}
	}
}

// known is the Persona session id and T3 thread id of id, a Persona session id or a T3 thread id.
func (b *t3codeBridge) known(id string) (string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t := b.threadOf[id]; t != "" {
		return id, t
	}
	if _, ok := b.threads[id]; ok {
		return t3codeSessionID(id), id
	}
	return "", ""
}

// resumed answers a start that resumes a thread T3 Code still has: its prompt, if any, is sent to it.
func (b *t3codeBridge) resumed(ctx context.Context, sid, threadID, prompt string) (any, error) {
	if strings.TrimSpace(prompt) != "" {
		if _, err := t3codeRun(ctx, prompt, "thread", "send", "--json", threadID, "-"); err != nil {
			return nil, err
		}
	}
	b.mu.Lock()
	th := b.threads[threadID]
	pr := b.projects[th.ProjectID]
	b.mu.Unlock()
	row, _ := mapT3CodeThread(th, pr)
	if row == nil {
		row = map[string]any{"session_id": sid, "status": proto.StatusIdle, "agent": "codex"}
	}
	b.wakeSoon()
	return map[string]any{"session": row}, nil
}

// projectFor is the T3 project a new thread in cwd belongs to: the one whose workspace root is the deepest that holds
// cwd. T3 Code starts a thread only in one of its projects.
func (b *t3codeBridge) projectFor(cwd string) (t3codeProject, bool) {
	if cwd == "" {
		return t3codeProject{}, false
	}
	cwd = filepath.Clean(cwd)
	b.mu.Lock()
	defer b.mu.Unlock()
	var best t3codeProject
	for _, p := range b.projects {
		root := strings.TrimSpace(p.WorkspaceRoot)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if cwd != root && !strings.HasPrefix(cwd, root+string(filepath.Separator)) {
			continue
		}
		if len(root) > len(filepath.Clean(best.WorkspaceRoot)) || best.ID == "" {
			best = p
		}
	}
	return best, best.ID != ""
}

// startThread starts a thread in the T3 project that holds cwd, with T3 Code's defaults for that project (its default
// model and mode, the project's root as its workspace).
func (b *t3codeBridge) startThread(ctx context.Context, cwd, prompt string) (any, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, &rpc.Error{Code: "unsupported", Message: "unsupported: T3 Code 新开线程需要一句话"}
	}
	p, found := b.projectFor(cwd)
	if !found {
		return nil, &rpc.Error{Code: "unsupported", Message: "unsupported: 这个文件夹不在 T3 Code 的项目里，先在 T3 Code 里把它加成项目"}
	}
	out, err := t3codeRun(ctx, prompt, "thread", "start", "--json", p.ID, "-")
	if err != nil {
		return nil, err
	}
	var launched struct {
		ThreadID  string  `json:"threadId"`
		ProjectID string  `json:"projectId"`
		RunID     *string `json:"runId"`
	}
	if json.Unmarshal(out, &launched) != nil || launched.ThreadID == "" {
		return nil, errors.New("t3 thread start 没有给出线程")
	}
	sid := t3codeSessionID(launched.ThreadID)
	if sid == "" {
		return nil, errors.New("T3 Code 给出的线程 id 用不了")
	}
	status := proto.StatusIdle
	if launched.RunID != nil {
		status = proto.StatusWorking
	}
	th := t3codeThreadListed{ThreadID: launched.ThreadID, ProjectID: p.ID, Title: t3codeTitleOf(prompt),
		Status:    map[bool]string{true: "starting", false: "idle"}[status == proto.StatusWorking],
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	row, _ := mapT3CodeThread(th, p)
	row["started_by_persona"] = true
	b.mu.Lock()
	b.threadOf[sid] = launched.ThreadID
	b.threads[launched.ThreadID] = th
	b.mu.Unlock()
	b.wakeSoon()
	return map[string]any{"session": row}, nil
}

// t3codeTitleOf is the title T3 Code gives a thread started without one: its message's first line.
func t3codeTitleOf(prompt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	return strings.TrimSpace(line)
}

// t3codeTimelineItem is an item of a thread's timeline as t3_thread_read answers it (OrchestratorMcpThreadTimelineItem).
type t3codeTimelineItem struct {
	Position      int     `json:"position"`
	Visibility    string  `json:"visibility"`
	ItemID        string  `json:"itemId"`
	Type          string  `json:"type"`
	Status        string  `json:"status"`
	Title         *string `json:"title"`
	Text          *string `json:"text"`
	TextTruncated bool    `json:"textTruncated"`
	UpdatedAt     string  `json:"updatedAt"`
}

type t3codeReadResult struct {
	Thread struct {
		ItemCount int `json:"itemCount"`
	} `json:"thread"`
	Items        []t3codeTimelineItem `json:"items"`
	NextPosition *int                 `json:"nextPosition"`
	HasMore      bool                 `json:"hasMore"`
}

// t3codeToolTypes are the timeline items that show as a tool's call.
var t3codeToolTypes = map[string]string{
	"command_execution": "Bash",
	"file_change":       "Edit",
	"file_search":       "Grep",
	"web_search":        "WebSearch",
	"dynamic_tool":      "Tool",
	"tool_call":         "Tool",
	"subagent":          "Task",
}

// t3codeItem is the Persona item of a timeline item; ok false for an item the chat does not show.
func t3codeItem(it t3codeTimelineItem) (proto.Item, bool) {
	ts, err := time.Parse(time.RFC3339Nano, it.UpdatedAt)
	if err != nil {
		ts = time.Time{}
	}
	text := ""
	if it.Text != nil {
		text = *it.Text
	}
	title := ""
	if it.Title != nil {
		title = strings.TrimSpace(*it.Title)
	}
	out := proto.Item{ID: it.ItemID, Seq: it.Position + 1, TS: ts.UTC()}
	switch it.Type {
	case "user_message":
		out.Role, out.Kind, out.Text = "user", proto.KindText, text
	case "assistant_message", "proposed_plan":
		out.Role, out.Kind, out.Text = "assistant", proto.KindText, text
	case "reasoning":
		out.Role, out.Kind, out.Text = "assistant", proto.KindThinking, text
	case "approval_request", "user_input_request":
		out.Role, out.Kind, out.Text = "assistant", proto.KindNotice, firstOf(text, title)
	case "error":
		out.Role, out.Kind, out.Text, out.IsError = "assistant", proto.KindText, firstOf(text, title), true
	default:
		name, isTool := t3codeToolTypes[it.Type]
		if !isTool {
			return proto.Item{}, false
		}
		out.Role, out.Kind, out.ToolName, out.ToolUseID = "assistant", proto.KindToolUse, firstOf(title, name), it.ItemID
		out.Text = text
		out.IsError = it.Status == "failed"
	}
	if strings.TrimSpace(out.Text) == "" && out.Kind != proto.KindToolUse {
		return proto.Item{}, false
	}
	return out, true
}

// sessionItems reads a page of a thread's timeline. Seq is a timeline position plus one, so to_seq is the thread's item
// count; the page holds the positions of the last limit (before before_seq), or those after after_seq.
func (b *t3codeBridge) sessionItems(ctx context.Context, threadID string, limit int, beforeSeq, afterSeq *int) (any, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	var head t3codeReadResult
	if err := b.mcp.call(ctx, "t3_thread_read", map[string]any{"threadId": threadID, "limit": 1, "runLimit": 1}, &head); err != nil {
		return nil, err
	}
	total := head.Thread.ItemCount
	start, end := max(0, total-limit), total // positions [start, end)
	switch {
	case afterSeq != nil:
		start = min(max(0, *afterSeq), total)
	case beforeSeq != nil:
		end = min(max(0, *beforeSeq-1), total)
		start = max(0, end-limit)
	}
	items := []proto.Item{}
	after := start - 1
	for page := 0; page < t3codeReadPages && after < end-1; page++ {
		var res t3codeReadResult
		args := map[string]any{"threadId": threadID, "view": "activity", "limit": 100, "maxCharsPerItem": 20000, "runLimit": 1}
		if after >= 0 {
			args["afterPosition"] = after
		}
		if err := b.mcp.call(ctx, "t3_thread_read", args, &res); err != nil {
			return nil, err
		}
		total = max(total, res.Thread.ItemCount)
		if afterSeq != nil {
			end = total
		}
		for _, it := range res.Items {
			if it.Position < start || it.Position >= end {
				continue
			}
			if pi, ok := t3codeItem(it); ok {
				items = append(items, pi)
			}
		}
		if !res.HasMore || res.NextPosition == nil || *res.NextPosition <= after {
			break
		}
		after = *res.NextPosition
	}
	return map[string]any{
		"items":    items,
		"to_seq":   total,
		"has_more": start > 0 && afterSeq == nil,
		"history":  "t3code:" + threadID,
	}, nil
}

// wakeSoon has the next refresh come now, so a change shows without waiting for the poll.
func (b *t3codeBridge) wakeSoon() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}
