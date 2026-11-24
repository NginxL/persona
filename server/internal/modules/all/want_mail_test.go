//go:build !persona_without_mail && !persona_core_only

package all

func init() { wantModules = append(wantModules, "mail") }
