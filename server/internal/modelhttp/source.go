package modelhttp

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

// SourceHostsEnv is an opt-in hostname allowlist. Empty means no attribution leaves Persona.
const SourceHostsEnv = "PERSONA_MODEL_SOURCE_HOSTS"

func SourceAllowed(base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return false
	}
	for _, entry := range strings.Split(os.Getenv(SourceHostsEnv), ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if suffix, wildcard := strings.CutPrefix(entry, "*."); wildcard {
			if suffix != "" && !strings.ContainsAny(suffix, "*/:@ ") && strings.HasSuffix(host, "."+suffix) && host != suffix {
				return true
			}
		} else if entry != "" && !strings.ContainsAny(entry, "*/:@ ") && host == entry {
			return true
		}
	}
	return false
}

// Source carries accounting metadata, never prompts, titles, paths or credentials.
type Source struct {
	Role    string `json:"role"`
	Todo    string `json:"todo,omitempty"`
	Routine string `json:"routine,omitempty"`
	Session string `json:"session,omitempty"`
}

const (
	RoleHeader    = "X-Persona-Role"
	TodoHeader    = "X-Persona-Todo"
	RoutineHeader = "X-Persona-Routine"
	SessionHeader = "X-Persona-Session"
)

func (s Source) Safe() Source {
	switch s.Role {
	case "main", "leader", "routine", "task_agent", "background":
	default:
		s.Role = "background"
	}
	s.Todo, s.Routine, s.Session = sourceID(s.Todo), sourceID(s.Routine), sourceID(s.Session)
	return s
}

func sourceID(id string) string {
	if len(id) > 256 {
		return ""
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_-.:", c)) {
			return ""
		}
	}
	return id
}

func SourceHeader(name string) bool {
	return strings.EqualFold(name, RoleHeader) || strings.EqualFold(name, TodoHeader) || strings.EqualFold(name, RoutineHeader) || strings.EqualFold(name, SessionHeader)
}

func ClearSource(h http.Header) {
	for name := range h {
		if SourceHeader(name) {
			delete(h, name)
		}
	}
}

func (s Source) Apply(h http.Header) {
	ClearSource(h)
	s = s.Safe()
	h.Set(RoleHeader, s.Role)
	if s.Todo != "" {
		h.Set(TodoHeader, s.Todo)
	}
	if s.Routine != "" {
		h.Set(RoutineHeader, s.Routine)
	}
	if s.Session != "" {
		h.Set(SessionHeader, s.Session)
	}
}

func (s Source) ApplyForURL(h http.Header, base string) {
	ClearSource(h)
	if SourceAllowed(base) {
		s.Apply(h)
	}
}

// MergeClaude preserves unrelated newline-separated Name: Value fields, including credentials.
// Reserved fields are replaced case-insensitively, including absent optional IDs.
func (s Source) MergeClaude(raw string) string {
	return s.mergeClaude(raw, true)
}

func (s Source) MergeClaudeForURL(raw, base string) string {
	return s.mergeClaude(raw, SourceAllowed(base))
}

func (s Source) mergeClaude(raw string, inject bool) string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		name, _, _ := strings.Cut(line, ":")
		if strings.TrimSpace(line) != "" && !SourceHeader(strings.TrimSpace(name)) {
			lines = append(lines, strings.TrimSuffix(line, "\r"))
		}
	}
	if !inject {
		return strings.Join(lines, "\n")
	}
	s = s.Safe()
	lines = append(lines, RoleHeader+": "+s.Role)
	if s.Todo != "" {
		lines = append(lines, TodoHeader+": "+s.Todo)
	}
	if s.Routine != "" {
		lines = append(lines, RoutineHeader+": "+s.Routine)
	}
	if s.Session != "" {
		lines = append(lines, SessionHeader+": "+s.Session)
	}
	return strings.Join(lines, "\n")
}
