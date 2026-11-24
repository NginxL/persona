// Package screenshare is the screen sharing module (docs/extensions/computer-use.md): the node's live screen sessions
// (#6398; screen-share/screenlink), which the node's composition root (internal/modules/all/node) provides through
// internal/node/screenport, and vncbench (screen-share/vncbench), the tool that measures a VNC server the way the app
// uses it. This package is what the manager's list (internal/modules/all) holds of it: its manifest, so a hub
// announces module.screen-share, which the clients' screen sharing is gated on.
package screenshare

import (
	_ "embed"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

//go:embed module.json
var manifest []byte

// Module is the screen sharing module's manager side.
type Module struct{}

// Info is the embedded manifest (module.json, checked against schema/module.schema.json).
func (Module) Info() modapi.Info {
	var info modapi.Info
	if err := json.Unmarshal(manifest, &info); err != nil {
		panic("screen-share: module.json: " + err.Error()) // embedded and tested: never at run time
	}
	return info
}

// Register adds nothing: the kernel announces the module's flag (module.screen-share); the module's sessions run on
// the computers (internal/modules/all/node).
func (Module) Register(modapi.Host) error { return nil }
