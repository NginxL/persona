//go:build !persona_without_screen_share && !persona_core_only

package all

func init() { wantModules = append(wantModules, "screen-share") }
