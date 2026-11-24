package node

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Registered bot endpoints use the same Orca client as the structured-session bridge. The manager supplies an exact
// session ID; this node never selects a first/default bot and the manager never dials Orca.
func (n *Node) handleAgentBot(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case "agentbot.describe", "agentbot.history":
		var req agents.BotHistoryRequest
		if err := json.Unmarshal(raw, &req); err != nil || req.SessionID == "" {
			return nil, agentBotRPCError("invalid", "invalid bot history request")
		}
		limit := req.Limit
		if method == "agentbot.describe" {
			limit = 1
		}
		page, err := agentBotRead(ctx, orcaClient(), req.SessionID, req.Cursor, limit)
		if err != nil {
			return nil, agentBotReadError(err)
		}
		return page, nil
	case "agentbot.send":
		var req agents.BotSendRequest
		if err := json.Unmarshal(raw, &req); err != nil || req.SessionID == "" || req.OperationID == "" || req.Text == "" {
			return nil, agentBotRPCError("invalid", "invalid bot send request")
		}
		c := orcaClient()
		page, err := agentBotRead(ctx, c, req.SessionID, "", 1)
		if err != nil {
			return nil, agentBotReadError(err)
		}
		if !page.CanSend {
			return nil, agentBotRPCError("not_found", "bot session is not active")
		}
		// The operation ID and exact payload fingerprint were fixed before this call. No retryUnknown/auto-repost is used.
		err = c.SendSession(ctx, orcart.Write{SessionID: req.SessionID, Fence: page.Fence, OperationID: req.OperationID}, req.Text, nil)
		if err != nil {
			return nil, agentBotSendError(err)
		}
		return map[string]any{"accepted": true}, nil
	default:
		return nil, agentBotRPCError("invalid", "unsupported bot method")
	}
}

// agentBotPageWithFence carries the write fence out of band; it is not exposed in personal-Agent messages.
type agentBotPageWithFence struct {
	agents.BotPage
	Fence int64 `json:"-"`
}

func agentBotRead(ctx context.Context, c *orcart.Client, sessionID, cursor string, limit int) (*agentBotPageWithFence, error) {
	if limit == 0 {
		limit = 40
	}
	trees, _, _, err := c.Worktrees(ctx, "")
	if err != nil {
		return nil, err
	}
	var bound *orcart.AgentRow
	for _, tree := range trees {
		if tree.HostID != "" && tree.HostID != "local" {
			continue
		}
		for i := range tree.Agents {
			row := &tree.Agents[i]
			orcaID, ok := orcart.SessionOf(row.PaneKey)
			if !ok || orcaID != sessionID {
				continue
			}
			if bound != nil {
				return nil, agentBotRPCError("conflict", "bot session binding is ambiguous")
			}
			bound = row
		}
	}
	if bound == nil {
		return nil, agentBotRPCError("not_found", "bot session is not active")
	}
	history, err := c.History(ctx, sessionID, cursor, limit)
	if err != nil {
		return nil, err
	}
	page := &agentBotPageWithFence{BotPage: agents.BotPage{CanSend: true, Working: history.Working,
		Messages: []agents.Message{}, HasMore: history.HasOlder, NextCursor: history.NextCursor}, Fence: history.Fence}
	page.RankTimeMs = bound.UpdatedAt
	for _, item := range history.Items {
		if item.Body.Kind != "message" || item.ItemID == "" {
			continue
		}
		m := agents.Message{ExternalID: item.ItemID, Role: item.Body.Role, CreateTimeMs: item.ObservedAt, Position: item.Sequence,
			Blocks: make([]agents.Block, 0, len(item.Body.Blocks))}
		for _, block := range item.Body.Blocks {
			m.Blocks = append(m.Blocks, agents.Block{Type: block.Type, Text: block.Text, Name: block.Name, Status: block.State,
				Summary: block.Summary, Input: block.Input})
			if block.Type == "tool-result" {
				m.Blocks[len(m.Blocks)-1].Text = block.Output
			}
		}
		page.Messages = append(page.Messages, m)
		if item.ObservedAt > page.RankTimeMs {
			page.RankTimeMs = item.ObservedAt
		}
	}
	return page, nil
}

func agentBotRPCError(code, message string) *rpc.Error {
	return &rpc.Error{Code: code, Message: message}
}

func agentBotReadError(err error) error {
	var re *rpc.Error
	if errors.As(err, &re) {
		return err
	}
	if orcart.IsCode(err, "agent_session_identity_required") || orcart.IsCode(err, "agent_session_not_found") {
		return agentBotRPCError("not_found", "bot session is not active")
	}
	if errors.Is(err, orcart.ErrNotRunning) {
		return agentBotRPCError("unavailable", "bot runtime is unavailable")
	}
	return agentBotRPCError("unavailable", "bot history is unavailable")
}

func agentBotSendError(err error) error {
	switch {
	case orcart.IsCode(err, "agent_session_operation_unknown"):
		return agentBotRPCError("unknown", "bot send outcome is unknown")
	case orcart.IsCode(err, "agent_session_payload_mismatch"):
		return agentBotRPCError("conflict", "bot operation payload changed")
	case orcart.IsCode(err, "agent_session_identity_required"), orcart.IsCode(err, "agent_session_not_found"):
		return agentBotRPCError("not_found", "bot session is not active")
	case errors.Is(err, orcart.ErrNotRunning):
		return agentBotRPCError("unavailable", "bot runtime is unavailable")
	case orcart.Refused(err):
		return agentBotRPCError("refused", "bot refused the send")
	default:
		// A transport/decode failure after a write may leave its outcome unknown. Never turn it into a fresh send.
		return agentBotRPCError("unknown", "bot send outcome is unknown")
	}
}
