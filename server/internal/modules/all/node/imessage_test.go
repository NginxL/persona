//go:build !persona_without_imessage && !persona_core_only

package node

func init() { want = append(want, "imessage") }
