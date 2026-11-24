package extmgr

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// nudge_after (bots_nudge.go, #6084): minutes a card waits unanswered before the one nudge under it, 0 off, 60 when
// never set. The app sets it with PUT /api/v1/bots/feishu, reads it with GET, and the assistant reads it in feishu.status.
func TestBotNudgeAfterSetting(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	st := w.h.store.(*botStore)
	status := func() any { return w.feishu(t, assistantwire.FeishuStatusGet, nil)["nudge_after"] }
	view := func() any { _, out := w.do(t, "GET", "/api/v1/bots/feishu", ""); return out["nudge_after"] }

	// Never set: 60, in both the app's view and the assistant's status.
	if v, s := view(), status(); v != float64(60) || s != float64(60) {
		t.Fatalf("default: view %v, status %v, want 60", v, s)
	}
	st.takeAudit()
	for _, c := range []struct {
		body string
		want float64
	}{{`{"nudge_after":15}`, 15}, {`{"nudge_after":0}`, 0}, {`{"nudge_after":8640}`, 8640}, {`{"features":{"main":true}}`, 8640}} {
		if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", c.body); code != 200 || out["nudge_after"] != c.want {
			t.Fatalf("PUT %s: %d, %v", c.body, code, out["nudge_after"])
		}
		if v, s := view(), status(); v != c.want || s != c.want {
			t.Fatalf("after %s: view %v, status %v, want %v", c.body, v, s, c.want)
		}
	}
	// 0 is a value, not "unset": the status carries it, so the assistant does not fall back to its own default.
	w.do(t, "PUT", "/api/v1/bots/feishu", `{"nudge_after":0}`)
	if _, ok := w.feishu(t, assistantwire.FeishuStatusGet, nil)["nudge_after"]; !ok {
		t.Fatal("the status leaves nudge_after out at 0")
	}

	// Out of range or not a whole number of minutes: refused, and nothing of the request applied.
	for _, bad := range []string{`{"nudge_after":-1}`, `{"nudge_after":8641}`, `{"nudge_after":1.5}`, `{"nudge_after":"soon"}`,
		`{"nudge_after":-5,"features":{"agents":true}}`} {
		code, out := w.do(t, "PUT", "/api/v1/bots/feishu", bad)
		if code != 400 {
			t.Errorf("PUT %s: %d, want 400", bad, code)
		}
		// The same answer for each, naming the range: not the JSON decoder's complaint about a fraction or a string.
		if msg, _ := out["error"].(string); !strings.Contains(msg, "8640") {
			t.Errorf("PUT %s: error %q does not name the range", bad, msg)
		}
	}
	if f, _ := w.feishu(t, assistantwire.FeishuStatusGet, nil)["features"].(map[string]any); f["agents"] == true {
		t.Error("a refused request changed the features")
	}
}

// The setting is recorded when it changes, by whoever changed it, and not when it is sent again as it was.
func TestBotNudgeAfterIsAudited(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	st := w.h.store.(*botStore)
	st.takeAudit()
	for _, body := range []string{`{"nudge_after":30}`, `{"nudge_after":30}`, `{"nudge_after":0}`} {
		if code := w.doAs(t, "dev_phone", "PUT", "/api/v1/bots/feishu", body); code != 200 {
			t.Fatal(code)
		}
	}
	rows := rowsWith(st.takeAudit(), auditBotNudgeAfter)
	if len(rows) != 2 || rows[0].Detail["minutes"] != 30 && rows[0].Detail["minutes"] != float64(30) ||
		rows[1].Detail["minutes"] != 0 && rows[1].Detail["minutes"] != float64(0) || rows[0].Actor != "device:dev_phone" {
		t.Fatalf("audit rows: %+v", rows)
	}
}
