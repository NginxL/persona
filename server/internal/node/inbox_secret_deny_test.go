package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/identity"
)

func inboxTestReadRule(path string, directory bool) string {
	path = filepath.ToSlash(filepath.Clean(path))
	if path[0] != '/' {
		path = "/" + path
	}
	if directory {
		path += "/**"
	}
	return "Read(/" + path + ")"
}

func inboxDeniedRules(flags []string) []string {
	var denied []string
	if i := slices.Index(flags, "--disallowedTools"); i >= 0 {
		for _, f := range flags[i+1:] {
			if strings.HasPrefix(f, "--") {
				break
			}
			denied = append(denied, f)
		}
	}
	return denied
}

// Independent review reproduction (#12036): the bare Read allow must not reach the data directory's credential files
// or the owner's other secrets when an Inbox chat is driven by untrusted mail/Lark content.
func TestReview12036InboxBareReadHasNoSecretDeny(t *testing.T) {
	n, home := testNode(t)
	n.dir = filepath.Join(home, ".persona")
	cwd := filepath.Join(n.dir, "assistant")
	const id = "review-12036"
	n.state.put(id, sessionState{Origin: originInbox})
	flags := n.claudeAssistantMCPAndTools(id, cwd, true)
	allowed := strings.Split(mcpFlag(t, flags, "--allowedTools"), ",")
	if !slices.Contains(allowed, "Read") {
		t.Fatalf("precondition: bare Read not allowed: %q", allowed)
	}
	denied := inboxDeniedRules(flags)
	secrets := []string{
		filepath.Join(n.dir, "hub.json"), filepath.Join(n.dir, "secret.key"), filepath.Join(n.dir, "launch.key"),
		filepath.Join(n.dir, "link.json"), filepath.Join(n.dir, "managers", "assistant", "manager.json"),
		filepath.Join(home, ".ssh", "id_ed25519"),
	}
	for _, s := range secrets {
		covered := false
		for _, d := range denied {
			if !strings.HasPrefix(d, "Read(") {
				continue
			}
			pat := strings.TrimSuffix(strings.TrimPrefix(d, "Read(/"), ")")
			if pat == s || strings.HasSuffix(pat, "/**") && strings.HasPrefix(s, strings.TrimSuffix(pat, "**")) {
				covered = true
			}
		}
		if !covered {
			t.Errorf("Inbox bare Read reaches %s with no Read deny rule (denied=%q)", s, denied)
		}
	}
}

// The launch contract enumerates the shared data, home and agent credential lists. Ordinary conversations must not
// inherit these Inbox-only rules; their existing health denies are separate.
func TestInboxSecretDenyRulesCoverSharedLists(t *testing.T) {
	n, home := testNode(t)
	n.dir = filepath.Join(home, ".persona")
	cwd := filepath.Join(n.dir, "assistant")
	const id = "inbox-secret-list"
	n.state.put(id, sessionState{Origin: originInbox})
	flags := n.claudeAssistantMCPAndTools(id, cwd, true)
	denied := inboxDeniedRules(flags)
	var expected []string
	for _, name := range identity.DataSecrets {
		expected = append(expected, inboxTestReadRule(filepath.Join(n.dir, name), false),
			inboxTestReadRule(filepath.Join(n.dir, name+"*"), false))
	}
	for _, name := range []string{"secret.key", "launch.key"} {
		expected = append(expected, inboxTestReadRule(filepath.Join(n.dir, name), false))
	}
	for _, glob := range identity.DataCredentialGlobs() {
		expected = append(expected, inboxTestReadRule(filepath.Join(n.dir, glob), false))
	}
	for _, dir := range append(slices.Clone(identity.DataSecretDirs), "managers", "run", "extensions/run") {
		expected = append(expected, inboxTestReadRule(filepath.Join(n.dir, dir), true))
	}
	for _, dir := range secretDirs {
		expected = append(expected, inboxTestReadRule(filepath.Join(home, dir), true))
	}
	for _, name := range secretFiles {
		expected = append(expected, inboxTestReadRule(filepath.Join(home, name), false))
	}
	for _, rule := range expected {
		if !slices.Contains(denied, rule) {
			t.Errorf("Inbox --disallowedTools lacks %q", rule)
		}
	}
	if !slices.Contains(denied, inboxTestReadRule(filepath.Join(home, ".claude", "settings.json"), false)) ||
		!slices.Contains(denied, inboxTestReadRule(filepath.Join(home, ".codex", "auth.json"), false)) {
		t.Errorf("Inbox launch lost an agent credential deny: %q", denied)
	}

	const ordinaryID = "ordinary-secret-list"
	n.state.put(ordinaryID, sessionState{Origin: "user"})
	ordinary := inboxDeniedRules(n.claudeAssistantMCPAndTools(ordinaryID, cwd, true))
	for _, rule := range expected {
		if slices.Contains(ordinary, rule) {
			t.Errorf("ordinary conversation gained Inbox-only deny %q", rule)
		}
	}
}

// Path rules are individual --disallowedTools arguments, not part of the legacy comma-joined MCP rule string.
func TestInboxSecretDenyRulesPreserveCommaPaths(t *testing.T) {
	n, home := testNode(t)
	n.dir = filepath.Join(home, ".persona,instance")
	const id = "inbox-comma-secret"
	n.state.put(id, sessionState{Origin: originInbox})
	flags := n.claudeAssistantMCPAndTools(id, filepath.Join(n.dir, "assistant"), true)
	rule := inboxTestReadRule(filepath.Join(n.dir, "hub.json"), false)
	if denied := inboxDeniedRules(flags); !slices.Contains(denied, rule) {
		t.Fatalf("comma-containing path was split or lost: rule=%q denied=%q", rule, denied)
	}
}

// A missing credential below a linked home still gets both its written and physical rules. Resolution stops at the
// deepest existing parent and restores the missing suffix; no credential file is created or read by this check.
func TestInboxSecretDenyRulesResolveMissingTargets(t *testing.T) {
	n, root := testNode(t)
	physical := filepath.Join(root, "physical-home")
	if err := os.MkdirAll(filepath.Join(physical, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked-home")
	if err := os.Symlink(physical, linked); err != nil {
		t.Fatal(err)
	}
	n.home, n.dir = linked, filepath.Join(linked, ".persona")
	rules := n.inboxSecretDenyRules()
	for _, tc := range []struct {
		rel       string
		directory bool
	}{
		{rel: ".persona/hub.json"},
		{rel: ".persona/secret.key"},
		{rel: ".persona/managers", directory: true},
		{rel: ".ssh", directory: true},
		{rel: ".claude/.credentials.json"},
	} {
		for _, base := range []string{linked, physical} {
			rule := inboxTestReadRule(filepath.Join(base, tc.rel), tc.directory)
			if !slices.Contains(rules, rule) {
				t.Errorf("missing resolved-path rule %q", rule)
			}
		}
	}
}
