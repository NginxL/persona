// Package briefing is the daily briefing module (docs/architecture/core-and-modules.md, the pilot of #13408): the
// facts a scheduled routine with facts on opens with, and its wait-row setting. It reaches the core only through
// modapi.
//
// What it owns so far: the assistant.briefing_waits setting (its value, defaults, checks and card text) and the wait
// rules the briefing's per-direction rows follow (when stopped work is waiting, how the rows are ordered and bounded).
// The facts' gathering, the Lark card and the delivery still live in internal/assistant (briefing*.go) and move here
// as the host learns to serve them.
package briefing

import (
	_ "embed"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

//go:embed module.json
var manifest []byte

// Module is the briefing module.
type Module struct{}

// Info is the embedded manifest (module.json, checked against schema/module.schema.json).
func (Module) Info() modapi.Info {
	var info modapi.Info
	if err := json.Unmarshal(manifest, &info); err != nil {
		panic("briefing: module.json: " + err.Error()) // embedded and tested: never at run time
	}
	return info
}

// Register adds the wait setting and the routine facts' wait rules.
func (Module) Register(h modapi.Host) error {
	if err := h.AddSetting(modapi.Setting{Key: SettingKey, Value: value, Check: check, Describe: describe}); err != nil {
		return err
	}
	return h.AddScheduleFacts(modapi.ScheduleFacts{Setting: SettingKey, Waits: func(stored json.RawMessage) modapi.WaitRules {
		return waitsOf(stored)
	}})
}
