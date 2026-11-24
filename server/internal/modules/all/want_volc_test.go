//go:build !persona_without_volc && !persona_core_only

package all

func init() { wantModules = append(wantModules, "volc") }
