package node

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// An assistant process learns its track's kind (#13472): the kind the manager named for its directory on
// assistant.prepare goes into PERSONA_TRACK_KIND next to PERSONA_SPACE; a directory prepared without one (a manager
// from before) or with a value that is no kind's slug gives none; an Agent started from the assistant's shell never
// takes it over.
func TestAssistantProcessKnowsItsTrackKind(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n, _ := testNode(t)
	n.dir, n.bin, n.logger = filepath.Join(n.home, ".persona"), "/opt/example/personad", log.New(io.Discard, "", 0)
	if err := os.MkdirAll(n.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	prepare := func(dir string, kind any) string {
		t.Helper()
		p := map[string]any{"dir": dir, "claude_md": "# Example"}
		if kind != nil {
			p["track_kind"] = kind
		}
		params, _ := json.Marshal(p)
		out, err := n.handle(t.Context(), "assistant.prepare", params)
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)["cwd"].(string)
	}
	envOfDir := func(cwd string) []string {
		t.Helper()
		env, release := n.assistantLarkEnv(cwd, "")
		release()
		return env
	}

	own := prepare(".persona/assistant", "orchestration")
	if env := envOfDir(own); !slices.Contains(env, "PERSONA_TRACK_KIND=orchestration") || !slices.Contains(env, "PERSONA_SPACE=main") {
		t.Fatalf("the assistant's own: %v", env)
	}
	health := prepare(".persona/health", "space")
	if env := envOfDir(health); !slices.Contains(env, "PERSONA_TRACK_KIND=space") || !slices.Contains(env, "PERSONA_SPACE=health") {
		t.Fatalf("the health space: %v", env)
	}
	// A manager from before says no kind; a value that is no slug is not put into the environment.
	for _, kind := range []any{nil, "Bad Kind", "qa\nX=1", ""} {
		cwd := prepare(".persona/assistant", kind)
		for _, kv := range envOfDir(cwd) {
			if strings.HasPrefix(kv, trackKindEnv+"=") {
				t.Errorf("track_kind %q: %s in the environment", kind, kv)
			}
		}
	}
	if !slices.Contains(assistantOnlyEnvNames(), trackKindEnv) {
		t.Error("an Agent would take PERSONA_TRACK_KIND over from the assistant's pane")
	}
}
