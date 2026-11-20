package node

import (
	"os"
	"strings"
	"testing"
)

// Opt-in acceptance probe for threads created by the locally installed TRAE CLI.
// It only reads CLI history; generated transcript projections stay in t.TempDir.
func TestTraexLiveHistory(t *testing.T) {
	ids := strings.Fields(os.Getenv("PERSONA_TEST_TRAEX_THREADS"))
	if len(ids) == 0 {
		t.Skip("set PERSONA_TEST_TRAEX_THREADS to locally created smoke-test thread IDs")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	n := remoteTestNode(t)
	n.home = home
	markers := strings.Fields(os.Getenv("PERSONA_TEST_TRAEX_MARKERS"))
	if len(markers) != 0 && len(markers) != len(ids) {
		t.Fatal("provide one PERSONA_TEST_TRAEX_MARKERS value per thread")
	}
	for i, id := range ids {
		t.Run(id, func(t *testing.T) {
			if !validID(id) {
				t.Fatal("invalid thread ID")
			}
			f, err := n.file(id)
			if err != nil {
				t.Fatal(err)
			}
			path := n.traexPath(id)
			t.Logf("TRAE thread %s: transcript_found=%t", id, path != "")
			if path == "" {
				t.Fatal("the current node cannot read this live TRAE CLI thread")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			if len(markers) != 0 {
				items, err := f.Refresh()
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, item := range items {
					if item.Role == "assistant" && strings.Contains(item.Text, markers[i]) {
						found = true
					}
				}
				t.Logf("TRAE thread %s: assistant_marker_found=%t", id, found)
				if !found {
					t.Fatal("the expected assistant reply is absent from the parsed transcript")
				}
			}
		})
	}
}
