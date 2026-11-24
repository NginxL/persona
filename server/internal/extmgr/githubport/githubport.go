// Package githubport is how the GitHub connector module reaches the extension manager (docs/architecture/core-and-modules.md
// "Provider seams"): the composition root (internal/modules/all) provides the module's factory, and extmgr makes the
// built-in GitHub connector with it when the manager runs. A build without the module (persona_without_github,
// persona_core_only) provides none, and the manager runs without the built-in GitHub connector (an extension's GitHub
// connector still works). It is its own package so that the composition root need not import extmgr.
package githubport

import "github.com/nwverse/persona/server/internal/githubwire"

var factory githubwire.Factory

// Provide sets the GitHub module's factory; the composition root calls it from init.
func Provide(f githubwire.Factory) { factory = f }

// Factory is the GitHub module's factory, nil when this build has no GitHub module.
func Factory() githubwire.Factory { return factory }
