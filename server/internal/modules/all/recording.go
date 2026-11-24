//go:build !persona_without_recording && !persona_core_only

package all

import (
	"github.com/nwverse/persona/server/internal/modules/recording"
	"github.com/nwverse/persona/server/internal/recordinghost"
)

// The recording module: its layer, which the assistant reaches through internal/recordinghost.
func init() {
	modules = append(modules, recording.Module{})
	recordinghost.Provide(func(h recordinghost.Host) recordinghost.Layer { return recording.New(h) })
}
