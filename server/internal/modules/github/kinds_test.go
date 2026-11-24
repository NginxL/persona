package github

import (
	"testing"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Every inbox kind the manifest declares has its own description for the models (prompts/inbox/kinds/github.md).
func TestKindsAreDescribed(t *testing.T) {
	m := (&Connector{}).Manifest()
	if len(m.Kinds) == 0 {
		t.Fatal("no kinds declared")
	}
	err := connectorwire.CheckKinds(m.Kinds, func(kind string) string {
		return prompts.RenderInboxItem(prompts.InboxItem{Source: "github", Kind: kind})
	})
	if err != nil {
		t.Fatal(err)
	}
}
