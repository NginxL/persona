package voice

import (
	_ "embed"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

// The voice module (docs/architecture/core-and-modules.md, #13431): full-duplex voice, the built-in extension
// program `personad voice` (Run), its meeting transcription contribution, and the stand-in upstream its tests use
// (voicetest). The extension host shows and starts the extension from what internal/modules/all provides of this
// package (internal/builtinext); the module adds nothing to the assistant yet, so Register adds nothing.

//go:embed module.json
var manifest []byte

// Module is the voice module.
type Module struct{}

// Info is the embedded manifest (module.json, checked against schema/module.schema.json).
func (Module) Info() modapi.Info {
	var info modapi.Info
	if err := json.Unmarshal(manifest, &info); err != nil {
		panic("voice: module.json: " + err.Error()) // embedded and tested: never at run time
	}
	return info
}

// Register adds nothing: the module's program runs as a built-in extension, which reaches the hub through its API.
func (Module) Register(modapi.Host) error { return nil }
