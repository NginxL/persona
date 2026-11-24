package conformance_test

import "github.com/nwverse/persona/sdk/go/wire"

// The memory_sources call (memory.schema.json, #13532): the manager will read a memory extension's entries into the
// generated wire types (assistant/memory_extension_wire.go maps them for the memory assembler). Kept in its own file
// so goTypes' literal is not edited for it.
func init() {
	goTypes["MemoryEntriesParams"] = wire.MemoryEntriesParams{}
	goTypes["MemoryEntriesResult"] = wire.MemoryEntriesResult{}
}
