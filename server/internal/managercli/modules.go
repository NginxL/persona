package managercli

// The built-in feature modules (docs/architecture/core-and-modules.md): this package is a composition root, so the
// manager it runs loads the modules internal/modules/all lists (and its build tags keep).
import _ "github.com/nwverse/persona/server/internal/modules/all"
