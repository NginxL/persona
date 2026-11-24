package github

import (
	_ "embed"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

//go:embed module.json
var manifest []byte

// Module is the GitHub connector module (docs/architecture/core-and-modules.md "Provider seams"). The extension
// manager registers its connector through its port (internal/extmgr/githubport), where the composition root provides
// Provider; it registers nothing with the kernel yet, and its module.json declares no slot.
type Module struct{}

// Info is the embedded manifest (module.json, checked against schema/module.schema.json).
func (Module) Info() modapi.Info {
	var info modapi.Info
	if err := json.Unmarshal(manifest, &info); err != nil {
		panic("github: module.json: " + err.Error()) // embedded and tested: never at run time
	}
	return info
}

// Register adds nothing: what GitHub adds reaches the core through the extension manager's port.
func (Module) Register(modapi.Host) error { return nil }
