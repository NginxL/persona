//go:build !persona_without_office && !persona_core_only

package all

func init() { wantModules = append(wantModules, "office") }
