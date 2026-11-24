package extmgr

import (
	"context"

	"github.com/nwverse/persona/server/internal/connector"
)

// legacyConnectors had rows of their own in Settings › Connectors before the app drew them from GET /api/v1/connectors
// (migration M6): an app from before draws every connector listed as a row of its own, so they are listed only when
// asked for (?all=1), or it would show them twice.
var legacyConnectors = map[string]bool{"lark": true, "mail": true}

func (h *Manager) listedConnectors(ctx context.Context, all bool) []connector.Info {
	h.requestLarkCLIRecovery(ctx)
	out := []connector.Info{}
	for _, info := range h.connectors.Infos(ctx) {
		if info.ID == "github" && info.Options != nil {
			info.Options = h.withGitHubMutes(ctx, info.Options)
		}
		if all || !legacyConnectors[info.ID] {
			out = append(out, info)
		}
	}
	return out
}
