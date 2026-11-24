//go:build !persona_without_github && !persona_core_only

package all

func init() { wantModules = append(wantModules, "github") }
