//go:build !persona_without_briefing && !persona_core_only

package all

func init() { wantModules = append(wantModules, "briefing") }
