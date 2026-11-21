package nodeclient

import (
	"os"
	"testing"

	"github.com/nwverse/persona/server/internal/testhome"
)

// TestMain runs the tests in a throw-away HOME with no PERSONA_HOME (testhome). Run from a shell Persona started,
// PERSONA_HOME names the running node's directory: TestRunHookWithoutANodePrintsNothing, which only moves HOME, sent
// its Stop hook to that node's socket, under the shell's own pane, and waited for its answer.
func TestMain(m *testing.M) { os.Exit(testhome.Main(m.Run)) }

// The socket the tests reach by default is under the throw-away home, never the caller's.
func TestTestsRunInAThrowAwayHome(t *testing.T) {
	if !testhome.Active() || os.Getenv("PERSONA_HOME") != "" {
		t.Fatalf("not isolated: active %v, PERSONA_HOME %q", testhome.Active(), os.Getenv("PERSONA_HOME"))
	}
}
