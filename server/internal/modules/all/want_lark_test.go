//go:build !persona_without_lark && !persona_core_only

package all

func init() { wantModules = append(wantModules, "lark") }
