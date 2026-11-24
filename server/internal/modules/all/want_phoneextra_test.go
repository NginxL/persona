//go:build !persona_without_phone_extra && !persona_core_only

package all

func init() { wantModules = append(wantModules, "phone-extra") }
