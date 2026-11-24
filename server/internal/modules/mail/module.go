package mail

import (
	_ "embed"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

//go:embed module.json
var manifest []byte

// Module is the mail module (docs/architecture/core-and-modules.md "Provider seams"). The extension manager serves its
// routes, tools and settings through its port (internal/extmgr/mailport), where the composition root provides Open;
// it registers nothing with the kernel yet, which serves no hub_routes or mcp_tools (#13844), and its module.json
// declares no slot.
type Module struct{}

// Info is the embedded manifest (module.json, checked against schema/module.schema.json).
func (Module) Info() modapi.Info {
	var info modapi.Info
	if err := json.Unmarshal(manifest, &info); err != nil {
		panic("mail: module.json: " + err.Error()) // embedded and tested: never at run time
	}
	return info
}

// Register adds nothing: what mail adds reaches the core through the extension manager's port.
func (Module) Register(modapi.Host) error { return nil }
