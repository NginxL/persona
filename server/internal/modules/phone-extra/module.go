// Package phoneextra is the iPhone control module (docs/extensions/iphone.md): the built-in program iphone that
// operates this computer's paired iPhones (phone-extra/nodeiphone over the device bridge, phone-extra/iphone and
// phone-extra/iphonectl) and `personad iphone …`, which pairs them (docs/extensions/iphone-pairing.md). The node's
// composition root (internal/modules/all/node) provides both through internal/node/iphoneport and
// internal/nodecmd/commandport; this package is what the manager's list (internal/modules/all) holds of it, its
// manifest, so a hub announces module.phone-extra.
package phoneextra

import (
	_ "embed"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

//go:embed module.json
var manifest []byte

// Module is the iPhone control module's manager side.
type Module struct{}

// Info is the embedded manifest (module.json, checked against schema/module.schema.json).
func (Module) Info() modapi.Info {
	var info modapi.Info
	if err := json.Unmarshal(manifest, &info); err != nil {
		panic("phone-extra: module.json: " + err.Error()) // embedded and tested: never at run time
	}
	return info
}

// Register adds nothing: the kernel announces the module's flag (module.phone-extra); the module's program runs on
// the computers (internal/modules/all/node).
func (Module) Register(modapi.Host) error { return nil }
