package devices

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSourcePermissionCannotBeClaimedThroughParams(t *testing.T) {
	proof := proto.SourcePermission{At: time.Now(), Session: "sender", Generation: "generation", Class: "bypass", Digest: strings.Repeat("a", 64)}
	for _, raw := range []string{
		`{"source_permission":{"class":"bypass"},"SOURCE_PERMISSION":{},"From_Mode":"bypass","text":"hi"}`,
		`{"source_permission":{},"source_permission":{"class":"bypass"},"text":"hi"}`,
		`{"source\u005fpermission":{},"FROM_MODE":"bypass","text":"hi"}`,
	} {
		for _, supported := range []bool{false, true} {
			for _, trusted := range []bool{false, true} {
				ctx := context.Background()
				if trusted {
					ctx = devicewire.WithSourcePermission(ctx, proof)
				}
				out, source, err := sourcePermissionParams(ctx, json.RawMessage(raw), supported)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(out, &fields)
				n := 0
				for k := range fields {
					if strings.EqualFold(k, "from_mode") {
						t.Errorf("forged class survived: %s", out)
					}
					if strings.EqualFold(k, "source_permission") {
						n++
					}
				}
				want := supported && trusted
				if source != want || (want && n != 1) || (!want && n != 0) || string(fields["text"]) != `"hi"` {
					t.Fatalf("supported=%v trusted=%v: %s", supported, trusted, out)
				}
			}
		}
	}
}

func TestSourcePermissionUsesReservedDeliveryOnlyForNewNodes(t *testing.T) {
	d := newTestDevices(t)
	modern := &testNode{info: &proto.NodeInfo{Protocol: nodeproto.Version, Capabilities: nodeproto.Announced}}
	old := &testNode{}
	connectedMachine(t, d, "modern", modern)
	connectedMachine(t, d, "old", old)
	proof := proto.SourcePermission{At: time.Now(), Session: "sender", Generation: "generation", Class: "bypass", Digest: strings.Repeat("a", 64)}
	ctx := devicewire.WithSourcePermission(context.Background(), proof)
	raw := json.RawMessage(`{"session_id":"target","text":"hi","SOURCE_PERMISSION":{},"from_mode":"bypass","OWNER_INPUT":true}`)
	for _, id := range []string{"modern", "old"} {
		if _, err := d.CallNode(ctx, id, "session.send", raw); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: id, Method: nodeproto.SourceSend, Params: raw}); err != errReservedOwnerControl {
			t.Fatalf("caller reached reserved path: %v", err)
		}
		if _, err := d.CallNode(ctx, id, nodeproto.SourceSend, raw); err != errReservedOwnerControl {
			t.Fatalf("raw internal method accepted: %v", err)
		}
	}
	got := modern.seen(nodeproto.SourceSend)
	if len(got) != 1 || !strings.Contains(got[0], `"source_permission"`) || strings.Contains(strings.ToLower(got[0]), "owner_input") {
		t.Fatalf("modern: %v", got)
	}
	got = old.seen("session.send")
	if len(got) != 1 || strings.Contains(strings.ToLower(got[0]), "source_permission") || strings.Contains(strings.ToLower(got[0]), "from_mode") {
		t.Fatalf("legacy: %v", got)
	}
}
