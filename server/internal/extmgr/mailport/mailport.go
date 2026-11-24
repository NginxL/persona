// Package mailport is how the mail module reaches the extension manager (docs/architecture/core-and-modules.md "Provider seams"):
// the composition root (internal/modules/all) provides the module's factory, and extmgr opens mail with it when the
// manager runs. A build without the module (persona_without_mail, persona_core_only) provides none, and the manager
// runs without mail. It is its own package so that the composition root need not import extmgr.
package mailport

import "github.com/nwverse/persona/server/internal/mailwire"

var factory mailwire.Factory

// Provide sets the mail module's factory; the composition root calls it from init.
func Provide(f mailwire.Factory) { factory = f }

// Factory is the mail module's factory, nil when this build has no mail module.
func Factory() mailwire.Factory { return factory }
