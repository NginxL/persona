package main

// The built-in modules this binary runs: the manager it starts loads them, and their extension programs are its
// subcommands (internal/builtinext). A composition root (internal/archguard/testdata/module_roots.txt).
import _ "github.com/nwverse/persona/server/internal/modules/all"
