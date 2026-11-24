//go:build !persona_without_office && !persona_core_only

package node

func init() { want = append(want, "office") }
