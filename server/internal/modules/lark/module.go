// Package lark is the Feishu / Lark module (docs/architecture/core-and-modules.md, #13404): the bot's client
// (feishubot), which the extension host drives through internal/botport, and the assistant's layer (Layer), which the
// assistant reaches through internal/larkhost. internal/modules/all provides both; the module registers nothing with
// the kernel, so Register adds nothing.
package lark

import (
	_ "embed"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

//go:embed module.json
var manifest []byte

// Module is the Feishu / Lark module.
type Module struct{}

// Info is the embedded manifest (module.json, checked against schema/module.schema.json).
func (Module) Info() modapi.Info {
	var info modapi.Info
	if err := json.Unmarshal(manifest, &info); err != nil {
		panic("lark: module.json: " + err.Error()) // embedded and tested: never at run time
	}
	return info
}

// Register adds nothing: the extension host reaches the bot's client through internal/botport, the assistant the
// layer through internal/larkhost.
func (Module) Register(modapi.Host) error { return nil }
