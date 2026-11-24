package extension

import (
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// memoryContrib makes items the manifest's only contributions.
func memoryContrib(items ...map[string]any) func(map[string]any) {
	return func(m map[string]any) { m["contributes"] = map[string]any{"memory_sources": items} }
}

// memory_sources is registered (#13586): a program that speaks the protocol, namespace memory, one item per extension
// with an id and a name of 1 to 80 characters; the install page says the hub reads its memory, the digest does not
// change for it.
func TestMemorySourcesPoint(t *testing.T) {
	p := Points["memory_sources"]
	if p.Name != "memory_sources" || p.Namespace != "memory" || p.Version != 1 || !p.NeedsRPC || p.NeedsHTTP || catalogItemTypes["memory_sources"] == nil {
		t.Fatalf("point %+v", p)
	}
	m, err := Parse(with(t, memoryContrib(map[string]any{"id": "memory", "name": "QA memory"})))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.MemorySources(); len(got) != 1 || got[0].ID != "memory" || got[0].Name != "QA memory" {
		t.Fatalf("%+v", got)
	}
	if items := m.Items("memory_sources"); !slices.Equal(items, []string{"memory"}) {
		t.Fatal(items)
	}
	if !DeclaresMemorySources(&Extension{Full: m}) || DeclaresMemorySources(&Extension{Full: &Manifest{}}) || DeclaresMemorySources(nil) {
		t.Fatal("DeclaresMemorySources")
	}
	var lines []string
	for _, l := range ExplainIn(i18n.En, m) {
		lines = append(lines, l.Key+": "+l.Title)
	}
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "memory_source: Give Persona its memory") }) {
		t.Fatalf("the install page: %v", lines)
	}
	plain, err := Parse(with(t, func(map[string]any) {}))
	if err != nil {
		t.Fatal(err)
	}
	withMemory, err := Parse(with(t, func(raw map[string]any) {
		raw["contributes"].(map[string]any)["memory_sources"] = []any{map[string]any{"id": "memory", "name": "QA memory"}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if Digest(plain) != Digest(withMemory) || MorePermissions(&Extension{Full: plain, Record: Record{Granted: plain.Permissions}}, withMemory) {
		t.Error("declaring memory_sources asks for nothing more")
	}
	for name, items := range map[string][]map[string]any{
		"two":        {{"id": "memory", "name": "QA memory"}, {"id": "other", "name": "QA other"}},
		"no name":    {{"id": "memory"}},
		"blank name": {{"id": "memory", "name": "  "}},
		"long name":  {{"id": "memory", "name": strings.Repeat("a", MemorySourceNameMost+1)}},
		"bad id":     {{"id": "Memory", "name": "QA memory"}},
		"extra":      {{"id": "memory", "name": "QA memory", "capabilities": []string{"recall"}}},
	} {
		if _, err := Parse(with(t, memoryContrib(items...))); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}
