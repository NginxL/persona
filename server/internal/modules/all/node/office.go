//go:build !persona_without_office && !persona_core_only

package node

import (
	"context"
	"io"

	"github.com/nwverse/persona/server/internal/modules/office/office"
	"github.com/nwverse/persona/server/internal/nodecmd/commandport"
)

// The Office files module: `personad office …` writes Word, PowerPoint and Excel files and reads them back
// (docs/assistant/office.md).
func init() {
	commandport.AddCommand("office", office.Usage, func(_ context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) int {
		return office.Main(args, stdout, stderr)
	})
}
