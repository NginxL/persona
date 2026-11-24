//go:build !persona_without_mail && !persona_core_only

package all

import (
	"github.com/nwverse/persona/server/internal/extmgr/mailport"
	"github.com/nwverse/persona/server/internal/modules/mail"
)

// The mail module: its service reaches the extension manager through its port (docs/architecture/core-and-modules.md
// "Provider seams"); it registers nothing with the kernel yet.
func init() {
	modules = append(modules, mail.Module{})
	mailport.Provide(mail.Open)
}
