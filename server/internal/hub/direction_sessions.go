package hub

import "github.com/nwverse/persona/server/internal/assistantwire"

// featureDirectionSessions: the default manager can file sessions under a direction and answer a single direction's
// read (#11391, docs/assistant/unified-entry.md "The direction tree"). Its hello alone decides, as for
// assistant_search; an extension's report cannot add the feature (features).
const featureDirectionSessions = assistantwire.CapabilityDirectionSessions

// directionSessionFilingRoute carries a machine and session, but a direction and its filing book belong to the
// explicitly selected or default manager, not necessarily to the manager that owns the session's computer.
const directionSessionFilingRoute = "PUT /api/v1/assistant/sessions/{mid}/{sid}/direction"

// directionSessionsFeatures is the feature while the default manager is connected and named the capability in its
// hello; nothing otherwise, so clients leave filing and the single-direction read out with an older manager.
func (h *Hub) directionSessionsFeatures() []string {
	if !h.defaultManagerNames(assistantwire.CapabilityDirectionSessions) {
		return nil
	}
	return []string{featureDirectionSessions}
}
