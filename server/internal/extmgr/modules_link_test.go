package extmgr

// The tests link the built-in modules as the manager does (docs/architecture/core-and-modules.md "Guards"), so the
// built-in extensions a module provides (internal/builtinext) are there in a default build and absent in one without
// the module.
import _ "github.com/nwverse/persona/server/internal/modules/all"
