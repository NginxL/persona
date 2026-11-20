package hub

import (
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The model configuration is the assistant's (docs/assistant/model-config.md "Who owns models"): its records, the providers' keys, every
// model call, the routes of Settings › Model providers / Models and "New agent"'s pickers (assistantwire.Routes, relayed). The
// hub hands over what hub.json says about models (the hello) and what its plugins have (their providers, their voice
// models, which of them need info filtering), and asks the assistant only what its plugins need: whether info filtering can be
// called, which launchers have a gateway, and requests to a launcher's gateway sent with its key (models.usable,
// models.launchers, models.launcher_http).

// hubModels is what hub.json's machines say about models (their launchers), for the hello; the models themselves
// (ai, inbox's, kb's) are the manager's manager.json's.
func (h *Hub) hubModels() assistantwire.HubModels {
	var out assistantwire.HubModels
	for _, m := range h.machinesNow() {
		if len(m.cfg.Launchers) > 0 {
			if out.Launchers == nil {
				out.Launchers = map[string][]assistantwire.ConfiguredLauncher{}
			}
			out.Launchers[m.cfg.ID] = m.cfg.Launchers
		}
		if m.cfg.LauncherRecovery != nil {
			if out.LauncherRecovery == nil {
				out.LauncherRecovery = map[string]assistantwire.LauncherRecovery{}
			}
			out.LauncherRecovery[m.cfg.ID] = *m.cfg.LauncherRecovery
		}
	}
	return out
}
