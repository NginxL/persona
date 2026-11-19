package herdrcore

import "testing"

func TestPipeName(t *testing.T) {
	for _, tc := range []struct{ user, session, want string }{
		{"alice", "default", `\\.\pipe\herdr-core-alice-default.sock`},
		{"alice", "iso", `\\.\pipe\herdr-core-alice-iso.sock`},
		{"", "default", ""},
		{"alice", "", ""},
	} {
		if got := pipeName(tc.user, tc.session); got != tc.want {
			t.Errorf("pipeName(%q, %q) = %q, want %q", tc.user, tc.session, got, tc.want)
		}
	}
}

func TestSessionName(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"", "default"},
		{"  ", "default"},
		{" iso ", "iso"},
		{"a/b", ""},
		{`a\b`, ""},
		{"..", ""},
	} {
		t.Setenv("HC_SESSION", tc.env)
		if got := sessionName(); got != tc.want {
			t.Errorf("HC_SESSION=%q: sessionName() = %q, want %q", tc.env, got, tc.want)
		}
	}
}
