package extmgr

import (
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

// A row an older stream's client pressed carries `key/id`; the source registered the callback under the graph's or
// the scene's own key, so the event goes back under that one. Anything else keeps the key it came with.
func TestStreamEventKeyFindsGraphAndScene(t *testing.T) {
	native := []byte(`{"sections": [{"key": "s", "items": [
		{"type": "graph", "key": "work", "nodes": [{"id": "a", "title": "A", "action": {"id": "cb-node"}}]},
		{"type": "scene", "key": "home", "alt": "A flat", "shapes": [{"kind": "box", "center": [0, 0], "size": [1, 1], "bottom": 0, "top": 1}],
		 "pins": [{"id": "lamp", "at": [0, 0], "label": "Lamp", "action": {"id": "cb-pin"}}]},
		{"type": "button", "key": "b", "title": "B", "action": {"id": "cb-button"}}
	]}]}`)
	for _, c := range []struct{ key, action, want string }{
		{"work/a", "cb-node", "work"},
		{"home/lamp", "cb-pin", "home"},
		{"b", "cb-button", "b"},
		{"home/gone", "cb-unknown", "home/gone"},
	} {
		if got := streamEventKey(native, wire.UIStreamEvent{Key: c.key, Action: c.action}); got != c.want {
			t.Errorf("%s %s: got %q, want %q", c.key, c.action, got, c.want)
		}
	}
	if got := streamEventKey(nil, wire.UIStreamEvent{Key: "home/lamp", Action: "cb-pin"}); got != "home/lamp" {
		t.Errorf("without a view: %q", got)
	}
}
