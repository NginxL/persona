package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// #8268 over the bus: notify_user's copy to the Feishu bot follows what the hub's low-distraction mode decided for the
// same push. The hub's whole HTTP stack and push grading, the assistant manager over the bus with its state in
// Postgres, a fake node for the assistant's machine, and the Feishu bot faked where the manager reaches it (app.call).
// With the switch on: a side conversation's ordinary notice is held and not copied, its P0 notice and the main
// conversation's ordinary one are copied, and a notice whose related key alerted within 12 hours is held as a repeat
// and not copied. With the switch off every notice is copied, a repeated key too. A copied notice's answer says the
// copy was delivered.
func TestNotifyUserFeishuCopyFollowsPushGradingOverTheBus(t *testing.T) {
	h, srv, hubDB := newBusHub(t)
	store := managerDB(t, h)
	ctx := context.Background()
	// The push grading keeps its switch and held list in hub_settings: the test hub's pusher was built before it had a
	// database. Each run starts and ends with the switch off and nothing held.
	h.push.store = hubDB
	resetGrading := func() {
		for _, k := range []string{gradingSwitchSettingKey, heldPushesSettingKey, pushSentSettingKey} {
			_ = hubDB.PutSetting(context.Background(), k, nil)
		}
	}
	resetGrading()
	t.Cleanup(resetGrading)
	keys := []string{"assistant", "assistant.tasks", "assistant.routines", "assistant.followups", "assistant.runs",
		"assistant.titles", "assistant.archived", "assistant.relay"}
	clear := func() {
		for _, k := range keys {
			_ = store.PutSetting(context.Background(), k, nil)
		}
	}
	clear()
	t.Cleanup(clear)
	n := newAssistantNode(t, h)
	n.m.info.Home = "/home/me" // the conversations' folder (convDir) is the assistant's on this machine
	now := time.Now().UTC()
	editSessions(n.m, func([]proto.Session) []proto.Session {
		return []proto.Session{{SessionID: "main-1", Cwd: convDir, Title: "main", Status: proto.StatusIdle, UpdatedAt: now},
			{SessionID: "side-2", Cwd: convDir, Title: "side", Status: proto.StatusIdle, UpdatedAt: now}}
	})
	n.known["main-1"], n.known["side-2"] = true, true
	if err := store.PutSetting(ctx, "assistant", assistant.Settings{MachineID: "server", Main: "main-1", Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	// The Feishu bot, paired with its pushes switch on: it records the cards it is asked to post.
	var mu sync.Mutex
	var posts []string
	fx := fakeExtOf(h)
	fx.onApp(assistantwire.AppFeishu, assistantwire.FeishuStatusGet, func(json.RawMessage) (any, error) {
		return assistantwire.FeishuStatus{Ready: true, Enabled: true, Owner: "ou_owner", PairedAt: time.Unix(100, 0),
			Features: assistantwire.FeishuFeatures{Pushes: true}}, nil
	})
	fx.onApp(assistantwire.AppFeishu, assistantwire.FeishuPost, func(p json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		posts = append(posts, string(p))
		return map[string]any{"message_id": "om_push"}, nil
	})
	dialAssistant(t, h, srv.URL, false)

	posted := func(title string) int {
		mu.Lock()
		defer mu.Unlock()
		c := 0
		for _, p := range posts {
			if strings.Contains(p, title) {
				c++
			}
		}
		return c
	}
	gradingMode := func(on bool) {
		t.Helper()
		body := `{"enabled":false}`
		if on {
			body = `{"enabled":true}`
		}
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/push/ln-mode", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("PUT push grading switch %s: %d %s", body, res.StatusCode, b)
		}
		settleAssistant(h)
	}
	held := func(title string) string {
		for _, e := range h.push.heldPushesView("", 0) {
			if e.Title == title {
				return e.Reason
			}
		}
		return ""
	}
	notify := func(session, title, level, key string) string {
		t.Helper()
		args := map[string]any{"title": title, "body": "details of " + title}
		if level != "" {
			args["push_level"] = level
		}
		if key != "" {
			args["related_key"] = key
		}
		raw, _ := json.Marshal(args)
		params, _ := json.Marshal(map[string]any{"name": "notify_user", "args": json.RawMessage(raw), "session_id": session, "assistant": true})
		res, err := nodeAsks(ctx, n.m, "mcp.call", params)
		if err != nil {
			t.Fatalf("notify_user %s: %v", title, err)
		}
		r := res.(proto.ToolResult)
		if r.IsError {
			t.Fatalf("notify_user %s: %s", title, textOf(r))
		}
		return textOf(r)
	}

	steps := []struct {
		grading                  bool
		session, title, lvl, key string
		copied                   bool
		reason                   string // the hub's intercept reason when it held the push
	}{
		{true, "side-2", "side ordinary", "", "", false, "ln_p1_held"},
		{true, "side-2", "side P0", "P0", "pr-1", true, ""},
		{true, "main-1", "main ordinary", "", "", true, ""},
		{true, "side-2", "side repeat of pr-1", "", "pr-1", false, "ln_dedup_12h"},
		{true, "main-1", "main P1 repeat of pr-1", "P1", "pr-1", false, "ln_dedup_12h"},
		{false, "side-2", "side ordinary, switch off", "", "", true, ""},
		{false, "side-2", "side pr-2, switch off", "", "pr-2", true, ""},
		{false, "side-2", "side pr-2 again, switch off", "", "pr-2", true, ""},
	}
	gradingOn := false
	for i, st := range steps {
		if i == 0 || st.grading != gradingOn {
			gradingMode(st.grading)
			gradingOn = st.grading
		}
		// notify_user answers only after the copies it sent landed or their wait ran out (copies.wait(notifyCopyWait),
		// tools.go), so a copy sent anyway is already among the posts here.
		answer := notify(st.session, st.title, st.lvl, st.key)
		got, reason := posted(st.title), held(st.title)
		t.Logf("grading=%v %s %q level=%q key=%q: Feishu copies %d, hub held %q; answer: %s", st.grading, st.session, st.title, st.lvl, st.key, got, reason, answer)
		switch {
		case st.copied && got != 1:
			t.Errorf("%q: %d Feishu copies, want 1", st.title, got)
		case !st.copied && got != 0:
			t.Errorf("%q: %d Feishu copies of a notice the hub held (%s)", st.title, got, reason)
		case reason != st.reason:
			t.Errorf("%q: the hub held it as %q, want %q", st.title, reason, st.reason)
		}
		copied := strings.Contains(answer, "Copied to Feishu: delivered.") || strings.Contains(answer, "已抄送飞书，已送到。") // i18n-ignore: the answer in either language
		if copied != st.copied {
			t.Errorf("%q: answer %q, want it to say the Feishu copy was delivered: %v", st.title, answer, st.copied)
		}
	}
}
