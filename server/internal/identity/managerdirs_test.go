package identity

import (
	"path/filepath"
	"testing"
)

// The built-in manager's directories keep their names; another manager's are named after it, and are told apart
// from it and from other names that start the same.
func TestManagerDirNames(t *testing.T) {
	for id, want := range map[string]string{"": "assistant", "assistant": "assistant", "m-0b0b0b": "assistant-m-0b0b0b"} {
		if got := AssistantDirName(id); got != want {
			t.Errorf("AssistantDirName(%q) = %q, want %q", id, got, want)
		}
	}
	if got := HealthDirName("m-0b0b0b"); got != "health-m-0b0b0b" {
		t.Errorf("HealthDirName: %q", got)
	}
	for name, want := range map[string]bool{"assistant": true, "assistant-m-0b0b0b": true, "assistant-bin": false,
		"assistant-bins": false, "assistant-hooks": false, "assistant-m-": false, "assistant-m-xyz": false, "assistants": false} {
		if got := IsManagerDirName(name, "assistant"); got != want {
			t.Errorf("IsManagerDirName(%q) = %v", name, got)
		}
	}
	home := t.TempDir()
	in := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	for p, want := range map[string]bool{in(".persona", "assistant"): true, in(".persona", "assistant-m-0b0b0b"): true,
		in(".persona", "health-m-0b0b0b"): true, in(".persona", "assistant-bin"): false, in("code", "assistant-m-0b0b0b"): false,
		in(".persona", "tracks", "qa-plain"): true, in(".persona", "tracks-m-0b0b0b", "qa-plain"): true, in(".persona", "tracks"): false,
		in(".persona", "tracks", "Bad Slug"): false, in(".persona", "tracks", "qa-plain", "inbox"): false, in("code", "tracks", "qa-plain"): false} {
		if got := IsAssistantDir(p); got != want {
			t.Errorf("IsAssistantDir(%s) = %v", p, got)
		}
	}
	if SpaceOf(in(".persona", "health-m-0b0b0b")) != SpaceHealth || SpaceOf(in(".persona", "assistant-m-0b0b0b")) != "" {
		t.Error("SpaceOf")
	}
	// Each manager's own: another's left on the machine is not.
	for _, c := range []struct {
		path, id string
		want     bool
	}{
		{in(".persona", "assistant"), "assistant", true},
		{in(".persona", "health"), "", true},
		{in(".persona", "assistant-m-0b0b0b"), "assistant", false},
		{in(".persona", "assistant"), "m-0b0b0b", false},
		{in(".persona", "assistant-m-0b0b0b"), "m-0b0b0b", true},
		{in(".persona", "health-m-0b0b0b"), "m-0b0b0b", true},
		{in(".persona", "health-m-0b0b0b"), "m-0c0c0c", false},
		{in(".persona", "tracks", "qa-plain"), "", true},
		{in(".persona", "tracks-m-0b0b0b", "qa-plain"), "m-0b0b0b", true},
		{in(".persona", "tracks-m-0b0b0b", "qa-plain"), "", false},
	} {
		if got := IsAssistantDirOf(c.path, c.id); got != c.want {
			t.Errorf("IsAssistantDirOf(%s, %q) = %v", c.path, c.id, got)
		}
	}
	// A track whose template is space keeps its directory in its manager's tracks directory, one level (#13472).
	for _, c := range []struct {
		dir, id string
		want    bool
	}{
		{"tracks/qa-plain", "", true},
		{"tracks-m-0b0b0b/qa-plain", "m-0b0b0b", true},
		{"tracks/qa-plain", "m-0b0b0b", false},
		{"tracks/qa-plain/inbox", "", false},
		{"tracks/health", "", false},
		{"kept/finance", "", false},
		{"assistant", "", false},
	} {
		if got := TrackDirOf(c.dir, c.id); got != c.want {
			t.Errorf("TrackDirOf(%q, %q) = %v", c.dir, c.id, got)
		}
	}
}
