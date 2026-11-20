package extmgr

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/feishubot"
)

func TestBotSourceReportDelaySetting(t *testing.T) {
	w := newBotWorld(t)
	const key = "source_report_delay_minutes"
	const path = "/api/v1/bots/feishu"
	read := func(want float64) {
		t.Helper()
		_, view := w.do(t, "GET", path, "")
		status := w.feishu(t, assistantwire.FeishuStatusGet, nil)
		if view[key] != want || status[key] != want {
			t.Fatalf("GET = %v, wire = %v, want %v", view[key], status[key], want)
		}
	}
	read(10)
	for _, minutes := range []int{1, 60, 0} {
		body := fmt.Sprintf(`{"source_report_delay_minutes":%d}`, minutes)
		if code, _ := w.do(t, "PUT", path, body); code != 200 {
			t.Fatalf("PUT %s: %d", body, code)
		}
		read(float64(minutes))
	}
	if code, _ := w.do(t, "PUT", path, `{"features":{"main":true}}`); code != 200 {
		t.Fatalf("PUT without delay: %d", code)
	}
	read(0)
	var persisted FeishuBotSettings
	if ok, err := w.h.store.Setting(t.Context(), botSettingsKey, &persisted); err != nil || !ok || persisted.SourceReportDelayMinutes == nil || *persisted.SourceReportDelayMinutes != 0 {
		t.Fatalf("explicit zero not saved: %+v, %v", persisted.SourceReportDelayMinutes, err)
	}
	for _, bad := range []string{"-1", "61", "1.5", `"5"`} {
		body := fmt.Sprintf(`{"source_report_delay_minutes":%s,"enabled":false}`, bad)
		if code, _ := w.do(t, "PUT", path, body); code != 400 {
			t.Errorf("PUT %s: %d, want 400", body, code)
		}
	}
	read(0)
	if _, view := w.do(t, "GET", path, ""); view["enabled"] != true {
		t.Fatal("an invalid request disabled the bot")
	}
	if code, _ := w.do(t, "PUT", path, `{"source_report_delay_minutes":10}`); code != 200 {
		t.Fatalf("restore default: %d", code)
	}
	read(10)
}

func TestBotSourceReportFullReplySetting(t *testing.T) {
	w := newBotWorld(t)
	const path = "/api/v1/bots/feishu"
	const setting = "source_report_full_reply"
	_, view := w.do(t, "GET", path, "")
	if view[setting] != false {
		t.Fatalf("default full reply: %v", view[setting])
	}
	if code, view := w.do(t, "PUT", path, `{"source_report_full_reply":true,"source_report_delay_minutes":0}`); code != 200 || view[setting] != true || view["source_report_delay_minutes"] != float64(0) {
		t.Fatalf("could not save both settings: %d %v", code, view)
	}
	if status := w.feishu(t, assistantwire.FeishuStatusGet, nil); status[setting] != true {
		t.Fatalf("full reply was not carried to the assistant: %v", status)
	}
	if code, _ := w.do(t, "PUT", path, `{"source_report_full_reply":false,"source_report_delay_minutes":61}`); code != 400 || !w.settings().SourceReportFullReply {
		t.Fatalf("an invalid delay applied its full reply change: %d", code)
	}
	if code, view := w.do(t, "PUT", path, `{"source_report_full_reply":false}`); code != 200 || view[setting] != false || view["source_report_delay_minutes"] != float64(0) {
		t.Fatalf("could not turn off full replies: %d %v", code, view)
	}
}

// ignoreProbe answers a request that is not the open platform's: a Persona node on the machine
// running these tests probes the fake's port (GET /, node/ports.go), as newFakeOpenAPI notes.
func ignoreProbe(rw http.ResponseWriter, req *http.Request) bool {
	if strings.HasPrefix(req.URL.Path, "/open-apis/") {
		return false
	}
	http.NotFound(rw, req)
	return true
}

func TestBotSourceReportLongReplyRetriesSameAttachment(t *testing.T) {
	w := newBotWorld(t)
	w.pair(t)
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"source_report_full_reply":true}`); code != http.StatusOK {
		t.Fatalf("enable full replies: HTTP %d", code)
	}
	const source = "om_long_report"
	w.h.takeAISource(askChat, source)

	var uploads atomic.Int32
	var loseResponse atomic.Bool
	loseResponse.Store(true)
	var mu sync.Mutex
	var replies []string
	api := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if ignoreProbe(rw, req) {
			return
		}
		reply := func(v any) { _ = json.NewEncoder(rw).Encode(v) }
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			reply(map[string]any{"code": 0, "tenant_access_token": "fake_token", "expire": 7200})
		case "/open-apis/im/v1/files":
			key := fmt.Sprintf("file_%d", uploads.Add(1))
			reply(map[string]any{"code": 0, "data": map[string]string{"file_key": key}})
		case "/open-apis/im/v1/messages/" + source + "/reply":
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Errorf("read reply: %v", err)
			}
			mu.Lock()
			replies = append(replies, string(body))
			mu.Unlock()
			if loseResponse.Load() {
				conn, _, err := rw.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("drop the reply response: %v", err)
					return
				}
				_ = conn.Close() // Feishu recorded the request, but its response never reached the hub.
				return
			}
			reply(map[string]any{"code": 0, "data": map[string]string{"message_id": "om_answer"}})
		default:
			t.Errorf("unexpected Feishu request: %s %s", req.Method, req.URL.Path)
			http.NotFound(rw, req)
		}
	}))
	defer api.Close()

	w.h.bots.mu.Lock()
	w.h.bots.api = feishubot.New("cli_test", "test_secret", api.URL)
	w.h.bots.mu.Unlock()
	raw, err := json.Marshal(map[string]any{
		"chat_id": askChat, "reply_to": source, "uuid": "sr:long", "full_answer": true,
		"text": strings.Repeat("Answer paragraph.\n", 2200),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := assistantwire.AppCall{AppID: assistantwire.AppFeishu, Method: assistantwire.FeishuText, Params: raw}
	if _, handled, err := w.h.AppCall(t.Context(), req); !handled || !feishubot.Transient(err) {
		t.Fatalf("expected an uncertain reply, handled %v, error %v", handled, err)
	}

	// A new hub loads the preparation from the same store before retrying the report.
	loseResponse.Store(false)
	restarted := newTestManager(t)
	restarted.store = w.h.store
	restarted.bots.api = feishubot.New("cli_test", "test_secret", api.URL)
	if out, handled, err := restarted.AppCall(t.Context(), req); !handled || err != nil || string(out) != `{"message_id":"om_answer"}` {
		t.Fatalf("retry after restart: %s, handled %v, error %v", out, handled, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := uploads.Load(); got != 1 || len(replies) < 2 {
		t.Fatalf("got %d uploads and %d reply attempts, wanted one upload across attempts", got, len(replies))
	}
	want := `{"content":"{\"file_key\":\"file_1\"}","msg_type":"file","uuid":"sr:long"}`
	for _, reply := range replies {
		if reply != want {
			t.Errorf("replayed reply %s, want %s", reply, want)
		}
	}
}

func TestBotSourceReportLongReplyRetriesAfterBotRestart(t *testing.T) {
	w := newBotWorld(t)
	w.pair(t)
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"source_report_full_reply":true}`); code != http.StatusOK {
		t.Fatalf("enable full replies: HTTP %d", code)
	}
	const source = "om_restart_report"
	w.h.takeAISource(askChat, source)

	var replies atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if ignoreProbe(rw, req) {
			return
		}
		reply := func(v any) { _ = json.NewEncoder(rw).Encode(v) }
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			reply(map[string]any{"code": 0, "tenant_access_token": "fake_token", "expire": 7200})
		case "/open-apis/im/v1/files":
			// The bot restarts with another client while the answer uploads.
			w.h.bots.mu.Lock()
			w.h.bots.api = feishubot.New("cli_test", "test_secret", "http://"+req.Host)
			w.h.bots.mu.Unlock()
			reply(map[string]any{"code": 0, "data": map[string]string{"file_key": "file_1"}})
		case "/open-apis/im/v1/messages/" + source + "/reply":
			replies.Add(1)
			reply(map[string]any{"code": 0, "data": map[string]string{"message_id": "om_answer"}})
		default:
			t.Errorf("unexpected Feishu request: %s %s", req.Method, req.URL.Path)
			http.NotFound(rw, req)
		}
	}))
	defer api.Close()

	w.h.bots.mu.Lock()
	w.h.bots.api = feishubot.New("cli_test", "test_secret", api.URL)
	w.h.bots.mu.Unlock()
	raw, err := json.Marshal(map[string]any{
		"chat_id": askChat, "reply_to": source, "uuid": "sr:restart", "full_answer": true,
		"text": strings.Repeat("Answer paragraph.\n", 2200),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := assistantwire.AppCall{AppID: assistantwire.AppFeishu, Method: assistantwire.FeishuText, Params: raw}
	_, handled, err := w.h.AppCall(t.Context(), req)
	if !handled || err == nil || err == errBotSourceReply || !feishubot.Transient(err) {
		t.Fatalf("a restart during the upload must fail for now, handled %v, error %v", handled, err)
	}
	if got := replies.Load(); got != 0 {
		t.Fatalf("replied with the old client's file: %d replies", got)
	}
	if out, handled, err := w.h.AppCall(t.Context(), req); !handled || err != nil || string(out) != `{"message_id":"om_answer"}` {
		t.Fatalf("retry with the current client: %s, handled %v, error %v", out, handled, err)
	}
}
