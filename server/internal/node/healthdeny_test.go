package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/identity"
)

// The assistant's own conversations are denied the health space's directory and its Claude project directory, for
// reading and writing; a health conversation is denied nothing; the rules go on the claude command line (#3282).
func TestHealthDenyRules(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	assistant := filepath.Join(home, ".persona", "assistant")
	health := filepath.Join(home, ".persona", "health")
	rules := n.healthDenyRules(assistant)
	project := filepath.Join(home, ".claude", "projects", nonAlnum.ReplaceAllString(health, "-"))
	for _, want := range []string{"Read(/" + health + "/**)", "Edit(/" + health + "/**)", "Read(/" + project + "/**)", "Edit(/" + project + "/**)"} {
		if !slices.Contains(rules, want) {
			t.Fatalf("missing %s in %q", want, rules)
		}
	}
	for _, r := range rules {
		if strings.Contains(r, "/.persona/assistant") || strings.Contains(r, "-assistant/") {
			t.Fatalf("the assistant's own directory denied: %s", r)
		}
	}
	if got := n.healthDenyRules(health); got != nil {
		t.Fatalf("a health conversation denied %q", got)
	}

	flags := n.claudeAssistantMCPAndTools("sess-123", assistant, true)
	i := slices.Index(flags, "--disallowedTools")
	if i < 0 || !slices.Contains(flags[i+1:], "Read(/"+health+"/**)") || slices.Contains(flags[i+1:], "--disallowedTools") {
		t.Fatalf("flags %q", flags)
	}
	if flags := n.claudeAssistantMCPAndTools("sess-123", health, true); slices.ContainsFunc(flags, func(f string) bool { return strings.HasPrefix(f, "Read(") }) {
		t.Fatalf("a health conversation's flags deny files: %q", flags)
	}
}

// A health conversation may write its reports' records without asking; nothing else gets that rule.
func TestHealthReportRules(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("HOME", home)
	health := filepath.Join(home, ".persona", "health")
	reports := "/" + filepath.Join(health, "reports") + "/**"
	// The resolved spelling follows when the directory resolves elsewhere (macOS's /var for a temporary directory).
	if got := healthReportRules(health); len(got) == 0 || len(got) > 2 || got[0] != "Edit("+reports+")" {
		t.Fatalf("rules %q", got)
	}
	if got := healthReportRules(filepath.Join(home, ".persona", "assistant")); got != nil {
		t.Fatalf("the assistant's own conversations got %q", got)
	}
	if got := healthReportRules("/home/a,b/.persona/health"); got != nil {
		t.Fatalf("a path with a comma got %q", got)
	}
	flags := n.claudeAssistantMCPAndTools("sess-123", health, true)
	i := slices.Index(flags, "--allowedTools")
	if i < 0 || !slices.Contains(strings.Split(flags[i+1], ","), "Edit("+reports+")") {
		t.Fatalf("flags %q", flags)
	}
}

// Every manager's health space is denied to a conversation outside it, its own or another's: health and health-<id>
// next to the conversation's directory. A health conversation is denied every other manager's health space, never
// its own.
func TestHealthDenyRulesEveryManager(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	health, other := filepath.Join(home, ".persona", "health"), filepath.Join(home, ".persona", "health-m-0b0b0b")
	for _, d := range []string{health, other, filepath.Join(home, ".persona", "health-notes")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	rules := n.healthDenyRules(filepath.Join(home, ".persona", "assistant-m-0b0b0b"))
	for _, want := range []string{"Read(/" + health + "/**)", "Read(/" + other + "/**)", "Edit(/" + other + "/**)"} {
		if !slices.Contains(rules, want) {
			t.Fatalf("missing %s in %q", want, rules)
		}
	}
	if slices.ContainsFunc(rules, func(r string) bool { return strings.Contains(r, "health-notes") }) {
		t.Fatalf("a directory that is no manager's health space denied: %q", rules)
	}
	third := filepath.Join(home, ".persona", "health-m-0c0c0c")
	if err := os.MkdirAll(third, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cwd, deny1, deny2 string }{
		{other, health, third}, // a manager's health conversation: the built-in's and the third's
		{health, other, third}, // the built-in's: every other manager's
	} {
		rules := n.healthDenyRules(c.cwd)
		for _, want := range []string{"Read(/" + c.deny1 + "/**)", "Read(/" + c.deny2 + "/**)"} {
			if !slices.Contains(rules, want) {
				t.Fatalf("%s: missing %s in %q", c.cwd, want, rules)
			}
		}
		if slices.ContainsFunc(rules, func(r string) bool { return strings.Contains(r, "/"+filepath.Base(c.cwd)+"/") }) {
			t.Fatalf("%s: its own space denied: %q", c.cwd, rules)
		}
	}
}

// The tracks a manager names (assistant.prepare deny_dirs, #13104) are denied like the health space, except to a
// conversation in that track, and a conversation in one is told as that track's; a name outside the state directory
// or with a bad slug is dropped; a later list from the same directory replaces the earlier one.
func TestDenyDirs(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Cleanup(func() { identity.SetTrackDirs(".persona/assistant", nil) })
	assistant := filepath.Join(home, ".persona", "assistant")
	finance := filepath.Join(home, ".persona", "tracks", "qa-finance")
	n.setDenyDirs(".persona/assistant", []denyDir{{".persona/tracks/qa-finance", "qa-finance"}, {"../elsewhere", "qa-x"}, {"/etc", "qa-y"},
		{".persona", "qa-z"}, {".persona/tracks/BAD", "BAD"}})
	if got := namedDenyDirs(); !slices.Equal(got, []string{finance}) {
		t.Fatalf("kept %q, want only %s", got, finance)
	}
	if got := identity.SpaceAt(filepath.Join(finance, "notes")); got != "qa-finance" {
		t.Fatalf("a folder in the track is told as %q", got)
	}
	if rules := n.healthDenyRules(assistant); !slices.Contains(rules, "Read(/"+finance+"/**)") || !slices.Contains(rules, "Edit(/"+finance+"/**)") {
		t.Fatalf("the assistant's conversation may read the track: %q", rules)
	}
	if rules := n.healthDenyRules(finance); slices.ContainsFunc(rules, func(r string) bool { return strings.Contains(r, "qa-finance") }) {
		t.Fatalf("a conversation of the track is denied its own: %q", rules)
	}
	n.setDenyDirs(".persona/assistant", []denyDir{})
	if rules := n.healthDenyRules(assistant); slices.ContainsFunc(rules, func(r string) bool { return strings.Contains(r, "qa-finance") }) {
		t.Fatalf("an emptied list still denies: %q", rules)
	}
	// What is named is forgotten; the default layout (tracks/<slug>) is still told by its shape (identity.SpaceAt).
	if got := identity.TrackDirs(); len(got) != 0 {
		t.Fatalf("an emptied list still names %v", got)
	}
}
