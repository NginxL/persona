package modelhttp

import (
	"net/http"
	"strings"
	"testing"
)

func TestSourceHostAllowlist(t *testing.T) {
	for _, tc := range []struct {
		hosts, url string
		want       bool
	}{
		{"", "https://gw.example.com/v1", false},
		{"gw.example.com", "https://gw.example.com:8443/v1", true},
		{"GW.EXAMPLE.COM", "https://gw.example.com/v1", true},
		{"*.example.com", "https://gw.example.com/v1", true},
		{"*.example.com", "https://a.gw.example.com/v1", true},
		{"*.example.com", "https://example.com/v1", false},
		{"*.example.com", "https://example.com.evil.test/v1", false},
		{"*.example.com", "https://evil-example.com/v1", false},
		{"*.example.com", "https://api.anthropic.com/v1", false},
		{"*.example.com", "https://api.openai.com/v1", false},
		{"other.example, gw.example.com", "https://gw.example.com/v1", true},
		{"*", "https://api.openai.com/v1", false},
		{"example.com", "https://me@example.com/v1", false},
		{"gw.example.com", "file://gw.example.com/v1", false},
	} {
		t.Run(tc.hosts+"/"+tc.url, func(t *testing.T) {
			t.Setenv(SourceHostsEnv, tc.hosts)
			if got := SourceAllowed(tc.url); got != tc.want {
				t.Fatalf("allowed=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestSourceRolesAndHeaderMerge(t *testing.T) {
	t.Setenv(SourceHostsEnv, "*.example.com")
	for _, role := range []string{"main", "leader", "routine", "task_agent", "background"} {
		s := Source{Role: role, Todo: "todo_123", Routine: "routine-456", Session: "session_789"}
		h := http.Header{"x-persona-role": {"stale"}, "X-Team": {"blue"}}
		s.ApplyForURL(h, "https://gw.example.com/v1")
		if h.Get(RoleHeader) != role || h.Get(TodoHeader) != s.Todo || h.Get(RoutineHeader) != s.Routine || h.Get(SessionHeader) != s.Session || h.Get("X-Team") != "blue" {
			t.Fatalf("bad attribution: %v", h)
		}
	}
	raw := "Authorization: example-credential\nX-Team: blue\nx-persona-role: leader\nX-Persona-Todo: todo_old\nX-Persona-Routine: old\nX-Persona-Session: old"
	got := (Source{Role: "background"}).MergeClaudeForURL(raw, "https://gw.example.com")
	if got != "Authorization: example-credential\nX-Team: blue\nX-Persona-Role: background" {
		t.Fatalf("merge=%q", got)
	}
	got = (Source{Role: "main"}).MergeClaudeForURL(raw, "https://api.anthropic.com")
	if strings.Contains(strings.ToLower(got), "x-persona-") || !strings.Contains(got, "X-Team: blue") {
		t.Fatalf("official merge=%q", got)
	}
	for _, id := range []string{"bad\r\nX-Injected: yes", "user message", "caf\u00e9", strings.Repeat("a", 257)} {
		if got := (Source{Role: "unknown", Todo: id, Routine: id, Session: id}).Safe(); got.Role != "background" || got.Todo != "" || got.Routine != "" || got.Session != "" {
			t.Fatal("unsafe metadata accepted")
		}
	}
}
