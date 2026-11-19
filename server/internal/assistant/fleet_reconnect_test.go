package assistant

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A node that connected again (an ssh node whose link dropped and that started over) lists a Codex turn still running
// as stopped. That stop is no turn end while the transcript has the turn open: no finished push, and the stop that
// does end it later is. A stop whose transcript shows the turn closed is the end it says.
func TestReconnectStopIsNoTurnEndUntilTheTranscriptCloses(t *testing.T) {
	for _, closed := range []bool{false, true} {
		name := "open"
		if closed {
			name = "closed"
		}
		t.Run(name, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			fastDone(ft)
			items := []proto.Item{{ID: "u1", Kind: proto.KindText, Role: "user", Text: "go on"}, {ID: "a1", Kind: proto.KindText, Role: "assistant", Text: "still at it"}}
			if closed {
				items = append(items, proto.Item{ID: "end1", Kind: "turn_duration", TurnDuration: &proto.TurnDuration{TurnID: "turn-1"}})
			}
			ft.f.setSessions("mac") // the node's transcript reads go through the hub
			ft.f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
				return map[string]any{"items": items}, nil
			})
			ft.d.mu.Lock()
			node := ft.d.machines["mac"]
			node.connected = time.Now().Add(-time.Hour)
			ft.d.mu.Unlock()

			work := proto.Session{SessionID: "s1", Agent: "codex", SourceKind: "codex_app_server", Status: proto.StatusWorking, UpdatedAt: time.Now(), Rev: 10}
			ft.sessions("mac", work)
			ft.d.mu.Lock()
			node.connected = time.Now() // the link dropped, the node is back
			ft.d.mu.Unlock()
			stopped := work
			stopped.Status, stopped.LastText = proto.StatusDone, "still at it"
			ft.sessions("mac", stopped)
			got := ft.pushesOf(assistantwire.NotifyDone, 1, 300*time.Millisecond)
			if closed {
				if len(got) != 1 {
					t.Fatalf("a stop whose transcript closed the turn: pushes = %+v", got)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("a turn still running was taken as ended after the reconnect: %+v", got)
			}
			// The node finds the turn running, then it ends on this connection: that is the turn's end.
			ft.sessions("mac", work)
			done := work
			done.Status, done.LastText, done.Rev = proto.StatusDone, "Verified result", 20
			ft.sessions("mac", done)
			if got := ft.pushesOf(assistantwire.NotifyDone, 1, time.Second); len(got) != 1 || got[0].Body != done.LastText {
				t.Fatalf("the real end: pushes = %+v", got)
			}
		})
	}
}
