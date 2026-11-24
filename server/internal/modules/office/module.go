// Package office is the Office files module (docs/assistant/office.md): `personad office …` writes Word documents and
// decks from Markdown and workbooks from JSON / CSV, and says what is in one. Its code (office/office) runs on the
// computers, as a device command the node's composition root (internal/modules/all/node) provides through
// internal/nodecmd/commandport; this package is what the manager's list (internal/modules/all) holds of it, its manifest.
package office

import (
	_ "embed"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

//go:embed module.json
var manifest []byte

// Module is the Office files module's manager side.
type Module struct{}

// Info is the embedded manifest (module.json, checked against schema/module.schema.json).
func (Module) Info() modapi.Info {
	var info modapi.Info
	if err := json.Unmarshal(manifest, &info); err != nil {
		panic("office: module.json: " + err.Error()) // embedded and tested: never at run time
	}
	return info
}

// Register adds nothing: the kernel announces the module's flag (module.office); the module's command runs on the
// computers (internal/modules/all/node).
func (Module) Register(modapi.Host) error { return nil }
