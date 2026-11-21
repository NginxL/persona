package proto

import "strings"

// AssistantEngineKind recognizes supported adapters, never a launcher display
// name. Runtime/connection identity is kept separately in the opaque suffix.
func AssistantEngineKind(identity string) string {
	kind, path, hasPath := strings.Cut(identity, "|")
	switch kind {
	case "claude", "codex", "agy", "pi", "opencode", "omp", "dsh", "grok", "gemini", "qwen", "kimi", "droid", "cursor", "traex":
	default:
		return ""
	}
	if hasPath {
		if strings.HasPrefix(path, "runtime:") {
			id := strings.TrimPrefix(path, "runtime:")
			if id == "" || len(id) > 128 {
				return ""
			}
			for _, r := range id {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
					return ""
				}
			}
		} else if strings.HasPrefix(path, "external:") {
			if !IsExternalLauncher(strings.TrimPrefix(path, "external:")) {
				return ""
			}
		} else {
			return ""
		}
	}
	return kind
}

func AssistantExecutionIdentity(kind, source, instance, launcher string) string {
	if AssistantEngineKind(kind) != kind || strings.Contains(kind, "|") {
		return ""
	}
	identity := kind
	if source == SourceRuntime {
		identity += "|runtime:" + instance
	} else if IsExternalLauncher(launcher) {
		identity += "|external:" + launcher
	}
	if AssistantEngineKind(identity) == "" {
		return ""
	}
	return identity
}
