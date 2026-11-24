package extmgr

import (
	"encoding/json"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

func init() {
	onAssistantEvent(assistantwire.EventAssistantEntriesChanged, func(h *Manager, raw json.RawMessage) {
		var p assistantwire.AssistantEntriesChanged
		if json.Unmarshal(raw, &p) != nil || p.ID == "" || p.RecordID == "" || p.Status != "wrong" {
			return
		}
		if h.extensions.todoWanted(assistantwire.EventAssistantEntriesChanged) {
			h.extensions.queueOn(&h.extensions.events, extensionNotice{topic: assistantwire.EventAssistantEntriesChanged, entryPersonal: p.Personal, payload: map[string]any{"id": p.ID, "record_id": p.RecordID, "status": p.Status}})
		}
	})
}
