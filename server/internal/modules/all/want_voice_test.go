//go:build !persona_without_voice && !persona_core_only

package all

func init() { wantModules = append(wantModules, "voice") }
