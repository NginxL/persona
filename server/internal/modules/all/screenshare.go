//go:build !persona_without_screen_share && !persona_core_only

package all

import screenshare "github.com/nwverse/persona/server/internal/modules/screen-share"

// The screen sharing module: its sessions run on the computers (internal/modules/all/node); the manager's list holds
// its manifest, so a hub announces module.screen-share.
func init() { modules = append(modules, screenshare.Module{}) }
