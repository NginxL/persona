// Package all is the composition root of the built-in modules (docs/architecture/core-and-modules.md): the one list of
// the modules compiled into the manager. Each module is added by a file of its own whose build constraint leaves it
// out with persona_without_<id> or persona_core_only; the manager links this package (internal/manager,
// internal/managercli) and the kernel (internal/assistant/kernel) loads what it provides. No other package imports a
// module.
package all

import "github.com/nwverse/persona/server/internal/modapi"

// Builtin are the modules this build has, in load order.
func Builtin() []modapi.Module { return modules }

// modules is filled by the per-module files' init, in file-name order.
var modules []modapi.Module
