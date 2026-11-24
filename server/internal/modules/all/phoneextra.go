//go:build !persona_without_phone_extra && !persona_core_only

package all

import phoneextra "github.com/nwverse/persona/server/internal/modules/phone-extra"

// The iPhone control module: its program runs on the computers (internal/modules/all/node); the manager's list holds
// its manifest, so a hub announces module.phone-extra.
func init() { modules = append(modules, phoneextra.Module{}) }
