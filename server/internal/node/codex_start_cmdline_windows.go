package node

import (
	"fmt"

	"golang.org/x/sys/windows"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Check the final native command line, including quoting and its NUL terminator.
// Never truncate or retry a too-large message through terminal paste.
func codexStartCommandLine(argv []string) error {
	line, err := windows.UTF16FromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return err
	}
	if len(line) > 32767 {
		return fmt.Errorf("%s", i18n.T(i18n.Default, "node-codex-start.command_line_too_long", i18n.Args{"units": len(line), "limit": 32767}))
	}
	return nil
}
