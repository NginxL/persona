//go:build !persona_without_imessage && !persona_core_only

package all

func init() { wantModules = append(wantModules, "imessage") }
