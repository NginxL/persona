package volc

import (
	_ "embed"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

// The Volcengine speech module (docs/architecture/core-and-modules.md, #13431): the built-in extension program
// `personad volc` (Run) and the stand-in service its tests use (volctest). It is a provider module of its own, apart
// from voice (#13430 section 2). The extension host shows the extension from what internal/modules/all provides of
// this package (internal/builtinext); the module adds nothing to the assistant yet, so Register adds nothing.

//go:embed module.json
var manifest []byte

// Module is the Volcengine speech module.
type Module struct{}

// Info is the embedded manifest (module.json, checked against schema/module.schema.json).
func (Module) Info() modapi.Info {
	var info modapi.Info
	if err := json.Unmarshal(manifest, &info); err != nil {
		panic("volc: module.json: " + err.Error()) // embedded and tested: never at run time
	}
	return info
}

// Register adds nothing: the module's program runs as a built-in extension, which reaches the hub through its API.
func (Module) Register(modapi.Host) error { return nil }
