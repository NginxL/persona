// Package recording is the meeting recording module (docs/architecture/core-and-modules.md "Recording", #13407): the
// manager's side of recordings, meetings, minutes and all-day recordings with their day recaps
// (docs/assistant/meeting-minutes.md, docs/assistant/day-recap.md). Its routes, the hub transcription of meetings,
// the meeting asks, photos and live view, the minutes' and day recaps' delivery and the recordings saved into tracks.
//
// The core reaches it through internal/recordinghost, filled by internal/modules/all; it reaches the core through its
// Host (host.go) and the knowledge base (internal/kb, which keeps the recordings' storage: a declared core-service
// dependency, archguard's testdata/module_services.txt).
package recording

import (
	_ "embed"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

//go:embed module.json
var manifest []byte

// Module is the recording module.
type Module struct{}

// Info is the embedded manifest (module.json, checked against schema/module.schema.json).
func (Module) Info() modapi.Info {
	var info modapi.Info
	if err := json.Unmarshal(manifest, &info); err != nil {
		panic("recording: module.json: " + err.Error()) // embedded and tested: never at run time
	}
	return info
}

// Register adds nothing: the kernel announces the module's feature flag (module.recording, its manifest's), and its
// routes, room tools and loops reach the core through internal/recordinghost, made per manager with the knowledge
// base's route guard (docs/architecture/core-and-modules.md "Recording").
func (Module) Register(modapi.Host) error { return nil }
