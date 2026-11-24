package extmgr

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The Lark bot's switches that came after the others: the board (#6052), only_conclusions (#1226), answer_new_card (#5396) and read_group (#11803).
var laterBotSwitches = []string{"board", "only_conclusions", "answer_new_card", "read_group"}

// Each is off by default and for settings saved before it; an app that does not know it leaves it as it was; the
// assistant's feishu.status says it and feishu.configure sets it.
func TestBotLaterSwitches(t *testing.T) {
	needsLarkModule(t)
	for _, id := range laterBotSwitches {
		t.Run(id, func(t *testing.T) {
			w := newBotWorld(t)
			w.pair(t)
			feats := func() map[string]any {
				f, _ := w.feishu(t, assistantwire.FeishuStatusGet, nil)["features"].(map[string]any)
				return f
			}
			if f := feats(); f[id] != false {
				t.Fatalf("default features %v: %s must be off", f, id)
			}
			if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"features":{"main":true,"cards":true,"`+id+`":true}}`); code != 200 {
				t.Fatal(code)
			}
			if f := feats(); f[id] != true || f["reports"] != true {
				t.Fatalf("%s on: %v", id, f)
			}
			// An app from before the switch sends the others only, and leaves it as it was.
			if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"features":{"main":true,"cards":true,"agents":true}}`); code != 200 {
				t.Fatal(code)
			}
			if f := feats(); f[id] != true || f["agents"] != true {
				t.Fatalf("an older app's PUT: %v", f)
			}
			w.feishu(t, assistantwire.FeishuConfigure, map[string]any{"features": map[string]bool{id: false}})
			if f := feats(); f[id] != false || f["agents"] != true {
				t.Fatalf("%s off by the assistant: %v", id, f)
			}
		})
	}
}

// The Lark bot's channel page draws its switches from the list the manager gives: each of these is one of them, off.
func TestChannelListHasTheLaterSwitches(t *testing.T) {
	needsLarkModule(t)
	for _, id := range laterBotSwitches {
		t.Run(id, func(t *testing.T) {
			w := newChannelWorld(t)
			out, err := w.channels(t, assistantwire.ChannelsList, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range out["channels"].([]any) {
				m := c.(map[string]any)
				if m["id"] != "feishu" {
					continue
				}
				for _, f := range m["features"].([]any) {
					if fm := f.(map[string]any); fm["id"] == id {
						if fm["on"] == true || fm["title"] == "" || fm["description"] == "" {
							t.Fatalf("the %s switch %v", id, fm)
						}
						return
					}
				}
				t.Fatalf("no %s switch in %v", id, m["features"])
			}
			t.Fatal("no Lark bot channel")
		})
	}
}
