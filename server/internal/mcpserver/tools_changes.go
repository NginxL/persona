package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

type toolsListedKey struct{}

type toolSnapshot struct {
	Tools     []map[string]any
	WorkGuide map[string]bool
	Revision  string
	Session   string
}

// Ignore ordering, but not schema/description changes. A task update that did not
// change this Agent's tools must not refresh the client's model context.
func (s toolSnapshot) fingerprint() string {
	items := make([]string, 0, len(s.Tools))
	for _, tool := range s.Tools {
		b, _ := json.Marshal(tool)
		name, _ := tool["name"].(string)
		item := string(b)
		if s.WorkGuide[name] {
			item = "discover:" + item
		}
		items = append(items, item)
	}
	slices.Sort(items)
	return strings.Join(items, "\n")
}

// A single long poll per MCP process waits on task-link changes, with a heartbeat
// for late pane identity. Errors retain the last list and retry with bounded
// backoff, so a hub/node restart cannot permanently remove the leader's tools.
func watchTools(ctx context.Context, snapshot toolSnapshot, send func(notification)) {
	backoff := time.Second
	for ctx.Err() == nil {
		callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		next, err := listToolsOnce(callCtx, snapshot)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil || next.Revision == "" {
			// A temporary rollback can remove watch support. Keep the last cursor
			// and retry slowly so a later upgrade does not strand this session.
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			backoff = min(2*backoff, 30*time.Second)
			continue
		}
		if next.fingerprint() != snapshot.fingerprint() {
			send(notification{JSONRPC: "2.0", Method: "notifications/tools/list_changed", Params: map[string]any{}})
		}
		snapshot, backoff = next, time.Second
	}
}
