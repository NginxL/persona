package assistant

import (
	"net/http"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Use the same predicate as plain archive before suggesting it as a fallback.
// A blocked state does not establish that a permission is being requested.
func archiveBusyRefusal(row proto.Session, closing bool) error {
	key := "running"
	if row.Status == proto.StatusBlocked || row.State == proto.StateNeedsYou || row.PendingInteractions > 0 {
		key = "waiting_recovery"
	} else if closing && !agentArchiveBusy(row) {
		key = "close_only_busy"
	}
	return archiveRefusal(http.StatusConflict, "agent_busy", i18n.ErrEnglish("hub-agent_archives."+key))
}
