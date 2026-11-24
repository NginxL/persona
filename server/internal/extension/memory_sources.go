package extension

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Memory sources (docs/extensions/extensions.md "Memory sources", #13532, #13586): a memory extension's memory_sources
// contribution answers memory.entries (schema/extension/memory.schema.json), the entries it keeps about the user, one
// by one, so the assistant's memory assembler decides which reach which scene. The registry only says which extension
// declares one; reading the entries is the manager's (extmgr/memory_sources.go), and so is withholding the
// extension's own text outlets once it declares one.

// MemorySourceItem is a contribution to memory_sources: what settings call the source.
type MemorySourceItem = wire.MemorySourceItem

// MemorySourceNameMost is the longest memory source name (runes).
const MemorySourceNameMost = 80

func init() {
	Points["memory_sources"] = Point{Name: "memory_sources", Namespace: "memory", Version: 1, NeedsRPC: true, Item: func(raw json.RawMessage) (string, error) {
		var it MemorySourceItem
		if err := strict(raw, &it); err != nil {
			return "", err
		}
		if !idPattern.MatchString(it.ID) {
			return it.ID, fmt.Errorf("memory_sources id %q: a-z0-9_-", it.ID)
		}
		if !shortText(it.Name, MemorySourceNameMost) {
			return it.ID, fmt.Errorf("memory_sources %s: a name of 1 to %d characters, not only spaces, no control characters", it.ID,
				MemorySourceNameMost)
		}
		return it.ID, nil
	}}
	catalogItemTypes["memory_sources"] = reflect.TypeFor[MemorySourceItem]()
}

// MemorySources are the manifest's contributions to memory_sources (at most one: Parse refuses more).
func (m *Manifest) MemorySources() []MemorySourceItem {
	if m == nil {
		return nil
	}
	var out []MemorySourceItem
	_ = json.Unmarshal(m.Contributes["memory_sources"], &out)
	return out
}

// MemorySource is an installed memory_sources contribution.
type MemorySource struct {
	// Extension is the extension's id, Contribution the item's id.
	Extension, Contribution string
	// Name is the contribution's name; ExtensionName the extension's (what the Inbox's background calls it).
	Name, ExtensionName string
	// On: the extension is enabled, has no problem and the contribution is switched on. Whether its program runs is the
	// manager's to say.
	On bool
}

// DeclaresMemorySources says whether p, as installed, declares a memory_sources contribution, on or off: once it does,
// its text outlets (its assistant instructions, project context, Inbox context, its connectors' read tools in the
// assistant's conversations, its own MCP servers in the assistant and its data-directory skills) are no longer
// delivered directly, whether or not its entries can be read now, so no entry
// reaches a scene around the hub's filter.
func DeclaresMemorySources(p *Extension) bool {
	if p == nil {
		return false
	}
	m := p.Full
	if m == nil {
		m = p.Manifest
	}
	return len(m.MemorySources()) > 0
}

// MemorySources are every installed memory_sources contribution, on or off, by extension id.
func (r *Registry) MemorySources() []MemorySource {
	var out []MemorySource
	for _, p := range r.List() {
		full := p.Full
		if full == nil {
			full = p.Manifest
		}
		var on []string
		if p.Record.Enabled && p.Problem == "" && p.Manifest != nil {
			on = p.Manifest.Items("memory_sources")
		}
		for _, it := range full.MemorySources() {
			out = append(out, MemorySource{Extension: p.Record.ID, Contribution: it.ID, Name: it.Name, ExtensionName: full.Name,
				On: slices.Contains(on, it.ID)})
		}
	}
	return out
}
