package connector

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

type mcpLanguageConnector struct{ tools []proto.Tool }

func (*mcpLanguageConnector) ID() string   { return "mail" }
func (*mcpLanguageConnector) Name() string { return "Example mail" }
func (*mcpLanguageConnector) Manifest() Manifest {
	return Manifest{Capabilities: []Capability{CapTools}, AgentTools: []string{"mail_*"}}
}
func (c *mcpLanguageConnector) Tools() []proto.Tool { return c.tools }
func (*mcpLanguageConnector) Call(context.Context, string, json.RawMessage) (string, error) {
	return "read", nil
}

func TestBuiltinMCPCatalogAndRegistryLanguages(t *testing.T) {
	original := prompts.Tool("mail_read")
	c := &mcpLanguageConnector{tools: []proto.Tool{{Name: original.Name, Description: original.Description, InputSchema: original.InputSchema}}}
	r := NewRegistry(nil)
	if err := r.Add(c); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.Ja, i18n.ZhHans} {
		ctx := i18n.With(context.Background(), lang)
		catalog, err := ReadCatalog(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if len(catalog) != 1 || len(catalog[0].Tools) != 1 {
			t.Fatal("catalog membership changed")
		}
		tool := catalog[0].Tools[0]
		if tool.Name != "read" || tool.Description != prompts.Tool("mail_read", string(lang)).Description {
			t.Fatalf("catalog language %s: %+v", lang, tool)
		}
		tools := r.ToolsFor(ctx, true)
		if len(tools) != 1 || tools[0].Description != tool.Description {
			t.Fatalf("registry language %s", lang)
		}
	}
	if c.tools[0].Description != original.Description {
		t.Fatal("mutated connector source declaration")
	}
	c.tools[0].Description = "The connector's own words, even with a built-in name"
	for _, lang := range []i18n.Lang{i18n.En, i18n.Ja} {
		tools := r.ToolsFor(i18n.With(context.Background(), lang), true)
		if len(tools) != 1 || tools[0].Description != c.tools[0].Description {
			t.Fatal("rewrote authored tool")
		}
	}
}
