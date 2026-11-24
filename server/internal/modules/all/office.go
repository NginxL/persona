//go:build !persona_without_office && !persona_core_only

package all

import "github.com/nwverse/persona/server/internal/modules/office"

// The Office files module: its command runs on the computers (internal/modules/all/node); the manager's list holds its
// manifest, so a hub announces module.office.
func init() { modules = append(modules, office.Module{}) }
