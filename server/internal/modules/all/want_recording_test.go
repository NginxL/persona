//go:build !persona_without_recording && !persona_core_only

package all

func init() { wantModules = append(wantModules, "recording") }
