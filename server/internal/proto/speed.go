package proto

import "slices"

// Speeds are the explicit per-start Agent speeds. Empty and standard request no
// override, so the launcher's own setting applies.
var Speeds = []string{"standard", "fast", "ultrafast"}

// ValidSpeed reports whether speed is an omitted or known Agent start speed.
func ValidSpeed(speed string) bool { return speed == "" || slices.Contains(Speeds, speed) }

// FasterSpeed reports whether speed asks for a billable faster start rather than
// the launcher's own setting.
func FasterSpeed(speed string) bool { return speed == "fast" || speed == "ultrafast" }

// CodexServiceTier maps an Agent start speed to Codex's native service_tier.
// Standard, omitted and unknown speeds request no native override.
func CodexServiceTier(speed string) string {
	switch speed {
	case "fast":
		return "priority"
	case "ultrafast":
		return "ultrafast"
	default:
		return ""
	}
}

// SpeedSupported reports whether an agent can apply speed at a new terminal
// start. Other agents and every standard start need no speed-specific handling.
func SpeedSupported(agent, speed string) bool {
	if !FasterSpeed(speed) {
		return true
	}
	switch agent {
	case "claude":
		return speed == "fast"
	case "codex":
		return true
	default:
		return false
	}
}
