//go:build !persona_without_briefing && !persona_core_only

package all

import "github.com/nwverse/persona/server/internal/modules/briefing"

func init() { modules = append(modules, briefing.Module{}) }
