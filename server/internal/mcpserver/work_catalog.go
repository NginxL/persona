package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// One catalog per MCP connection. Discovery only changes tools/list. Operations
// still arrive under their original names through the normal tools/call path.
const workSearchName = "work_tools_search"

type workCatalogKey struct{}
type workCatalog struct {
	mu       sync.Mutex
	selected map[string]bool
	notify   func()
}

var workSearchTool = map[string]any{
	"name":        workSearchName,
	"description": "Find Persona tools for to-dos, agents, tasks, conversations and repositories. Give exact names separated by spaces to load their definitions, or a name fragment to list matches. Then call the original tool.",
	"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]string{"type": "string"}}, "required": []string{"query"}},
	"_meta":       map[string]bool{"anthropic/alwaysLoad": true},
}

func (c *workCatalog) active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.selected != nil
}

func (c *workCatalog) list(s toolSnapshot) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(s.WorkGuide) == 0 {
		c.selected = nil
		return s.Tools
	}
	if c.selected == nil {
		c.selected = make(map[string]bool)
	}
	tools := make([]map[string]any, 0, len(s.Tools)-len(s.WorkGuide)+1)
	for _, t := range s.Tools {
		name, _ := t["name"].(string)
		if !s.WorkGuide[name] || c.selected[name] {
			tools = append(tools, t)
		}
	}
	return append(tools, workSearchTool)
}

func (c *workCatalog) search(ctx context.Context, args json.RawMessage) map[string]any {
	var p struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &p); err != nil || strings.TrimSpace(p.Query) == "" {
		return toolResult(proto.TextResult("Supply a tool name or name fragment.", true))
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	s, err := listTools(ctx)
	if err != nil {
		return toolResult(proto.TextResult(err.Error(), true))
	}
	fields := strings.Fields(strings.ToLower(p.Query))
	var matches, exact []string
	for name := range s.WorkGuide {
		for _, query := range fields {
			if strings.Contains(name, query) {
				matches = append(matches, name)
				break
			}
		}
		if slices.Contains(fields, name) {
			exact = append(exact, name)
		}
	}
	if len(matches) == 0 {
		return toolResult(proto.TextResult("No matching work tool in this conversation.", false))
	}
	slices.Sort(matches)
	if len(exact) == 0 {
		return toolResult(proto.TextResult("Matches: "+strings.Join(matches, ", ")+". Request exact names to load.", false))
	}
	slices.Sort(exact)
	c.mu.Lock()
	if c.selected == nil {
		c.mu.Unlock()
		return toolResult(proto.TextResult("Work tool discovery is no longer active.", true))
	}
	for _, name := range exact {
		c.selected[name] = true
	}
	c.mu.Unlock()
	c.notify()
	return toolResult(proto.TextResult("Loaded: "+strings.Join(exact, ", ")+". Call the named tools directly.", false))
}
