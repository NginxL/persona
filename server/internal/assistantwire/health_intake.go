package assistantwire

import "encoding/json"

// WaterExtension is a local-only snapshot for migrating the legacy water extension.
// State is populated only for an explicit import, never for the Health home.
type WaterExtension struct {
	StorageKey string
	Version    string
	Enabled    bool
	Config     json.RawMessage
	State      json.RawMessage
}

// CapabilityHealthIntake: the manager stores water and protein entries and their undo markers.
// Only the default manager's hello enables the health_intake feature in the hub's hello.
const CapabilityHealthIntake = "health_intake"

func init() {
	Routes = append(Routes,
		"GET /api/v1/assistant/health/intake",
		"POST /api/v1/assistant/health/intake",
		"DELETE /api/v1/assistant/health/intake/{id}",
		"GET /api/v1/assistant/health/intake/changes",
		"POST /api/v1/assistant/health/intake/ui",
		"POST /api/v1/assistant/health/intake/import",
		"POST /api/v1/assistant/health/water/reminder",
	)
}

// NotifyHealthWater is an explicitly enabled water reminder, independent of a conversation.
const NotifyHealthWater = "health_water"

// NotifyHealth identifies the reminder whose fixed notification buttons may record water.
type NotifyHealth struct {
	Reminder string `json:"reminder"`
}
