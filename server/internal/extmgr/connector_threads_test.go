package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// threadsConnector is a chat app with one made-up conversation of 40 messages, a picture, a reply action, and a
// state the test sets.
type threadsConnector struct {
	state   atomic.Value // string
	fetched atomic.Int32 // Media calls
	mu      sync.Mutex
	sent    []string
}

func newThreadsConnector() *threadsConnector {
	c := &threadsConnector{}
	c.state.Store(connector.StateOK)
	return c
}

func (*threadsConnector) ID() string   { return "example" }
func (*threadsConnector) Name() string { return "Example" }
func (*threadsConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus, connector.CapActions, connector.CapThreads}}
}
func (c *threadsConnector) Status(context.Context) connector.Status {
	st := c.state.Load().(string)
	if st == connector.StateRateLimited {
		return connector.Status{State: st, Error: "限流：还要 30 秒"}
	}
	return connector.Status{State: st}
}

func (c *threadsConnector) Thread(_ context.Context, thread, before string, limit int) (connector.ThreadPage, error) {
	if thread != "u1" {
		return connector.ThreadPage{}, errors.New("没有这个会话")
	}
	end := 40
	if before != "" {
		fmt.Sscan(before, &end)
	}
	start := max(end-limit, 0)
	page := connector.ThreadPage{Chat: connector.ThreadChat{ID: "u1", Name: "示例用户", Kind: "dm", Reply: "example_send"}}
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	for i := start; i < end; i++ {
		m := connector.ThreadMessage{ID: fmt.Sprint(i + 1), At: at.Add(time.Duration(i) * time.Minute), Text: fmt.Sprintf("示例消息 %d", i+1),
			Sender: connector.ThreadPerson{ID: "u1", Name: "示例用户", Avatar: "avatar:u1:1"}}
		if i == 39 {
			m.Media = []connector.ThreadMedia{{Kind: "image", Ref: "photo:40", Width: 4, Height: 3}}
		}
		page.Messages = append(page.Messages, m)
	}
	if start > 0 {
		page.Before = fmt.Sprint(start)
	}
	return page, nil
}

func (c *threadsConnector) Media(_ context.Context, ref string) (connector.Media, error) {
	c.fetched.Add(1)
	time.Sleep(20 * time.Millisecond) // the connector's own limits: concurrent asks wait for the one fetch
	if ref == "photo:missing" {
		return connector.Media{}, errors.New("没有这张图")
	}
	return connector.Media{Mime: "image/png", Data: connectorTestPNG()}, nil
}

func (*threadsConnector) Actions() []proto.Tool { return []proto.Tool{{Name: "example_send"}} }
func (*threadsConnector) Describe(_ context.Context, _ string, args json.RawMessage) (connector.Action, string, error) {
	return connector.Action{Visible: "示例用户"}, "发给示例用户：" + string(args), nil
}
func (c *threadsConnector) Do(_ context.Context, _ string, args json.RawMessage) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, string(args))
	return `{"sent":{"id":41}}`, nil
}

func threadsHub(t *testing.T) (*Manager, *threadsConnector, http.Handler) {
	t.Helper()
	return threadsHubWith(t, nil)
}

// threadsHubWith is threadsHub with hub.json's connector_gate rules.
func threadsHubWith(t *testing.T, rules []connector.Rule) (*Manager, *threadsConnector, http.Handler) {
	t.Helper()
	h := newTestManager(t)
	h.store = newMemStore()
	h.connectors = connector.NewRegistry(rules)
	c := newThreadsConnector()
	if err := h.addConnector(c); err != nil {
		t.Fatal(err)
	}
	return h, c, h.routes()
}

type avatarPageConnector struct {
	*pictureConnector
	source string
	page   connector.ThreadPage
}

func (c *avatarPageConnector) ID() string { return c.source }

func (c *avatarPageConnector) Thread(context.Context, string, string, int) (connector.ThreadPage, error) {
	return c.page, nil
}

func TestConnectorThreadAvatarURLs(t *testing.T) {
	for _, source := range []string{"telegram", "github"} {
		t.Run(source, func(t *testing.T) {
			h := newTestManager(t)
			h.store = newMemStore()
			h.connectors = connector.NewRegistry(nil)
			c := &avatarPageConnector{pictureConnector: &pictureConnector{}, source: source}
			c.state.Store(connector.StateOK)
			// Read the same shape an external connector can return, including unknown fields.
			if err := json.Unmarshal([]byte(`{
              "chat":{"id":"demo","avatar":"a.u1.501","avatar_url":"/plugin-path","author":{"name":"Author","avatar":"avatar:author:1","avatar_url":"/plugin-path"}},
              "messages":[
                {"id":"1","text":"Review","sender":{"name":"Reviewer","avatar":"https://example.com/avatar.png","avatar_url":"/plugin-path"}},
                {"id":"2","sender":{"name":"Guest","avatar_url":"/plugin-path"}}
              ],"before":"older"
            }`), &c.page); err != nil {
				t.Fatal(err)
			}
			original, err := json.Marshal(c.page)
			if err != nil || bytes.Contains(original, []byte(`"avatar_url"`)) {
				t.Fatalf("connector page contains a hub field: %s, %v", original, err)
			}
			if err := h.addConnector(c); err != nil {
				t.Fatal(err)
			}
			mux := h.routes()
			token := deviceToken("phone", roleFull)
			code, out := call(t, mux, "GET", "/api/v1/connectors/"+source+"/thread?thread=demo", token, "")
			if code != http.StatusOK {
				t.Fatalf("thread: %d %v", code, out)
			}
			chat := out["chat"].(map[string]any)
			messages := out["messages"].([]any)
			check := func(row map[string]any, ref string) {
				t.Helper()
				if row["avatar"] != ref || row["avatar_url"] != extwire.AvatarURL(source, ref) {
					t.Errorf("avatar: %v, want %q and its %s hub path", row, ref, source)
				}
			}
			check(chat, "a.u1.501")
			check(chat["author"].(map[string]any), "avatar:author:1")
			check(messages[0].(map[string]any)["sender"].(map[string]any), "https://example.com/avatar.png")
			if out["before"] != "older" || messages[0].(map[string]any)["text"] != "Review" {
				t.Fatalf("missing thread data in the app response: %v", out)
			}
			if after, err := json.Marshal(c.page); err != nil || !bytes.Equal(original, after) {
				t.Fatalf("the app response changed the connector's page: %s, %v", after, err)
			}
			if guest := messages[1].(map[string]any)["sender"].(map[string]any); guest["avatar"] != nil || guest["avatar_url"] != nil {
				t.Errorf("guest without a picture: %v", guest)
			}
			if c.fetched.Load() != 0 {
				t.Fatal("listing the thread fetched an image")
			}
			path, _ := chat["avatar_url"].(string)
			if path == "" {
				t.Fatal("no chat picture to open")
			}
			req := httptest.NewRequest("GET", path+"?size=80", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			cfg, format, err := image.DecodeConfig(w.Body)
			if w.Code != http.StatusOK || err != nil || format != "jpeg" || cfg.Width != 128 || cfg.Height != 128 || c.fetched.Load() != 1 {
				t.Fatalf("opening the listed picture: %d, %s %v, %v, media reads %d", w.Code, format, cfg, err, c.fetched.Load())
			}
		})
	}
}

// A thread reads a page at a time, newest first, each page going back from the one before; the app is told why it
// cannot read one (no such connector, no threads, signed out, rate limited).
func TestConnectorThreadPages(t *testing.T) {
	h, c, mux := threadsHub(t)
	code, out := call(t, mux, "GET", "/api/v1/connectors/example/thread?thread=u1&limit=30", h.cfg.Token, "")
	msgs, _ := out["messages"].([]any)
	if code != 200 || len(msgs) != 30 || out["before"] != "10" || msgs[0].(map[string]any)["id"] != "11" || msgs[29].(map[string]any)["id"] != "40" {
		t.Fatalf("first page: %d %v", code, out)
	}
	if chat := out["chat"].(map[string]any); chat["reply"] != "example_send" || chat["kind"] != "dm" {
		t.Fatalf("chat %v", chat)
	}
	code, out = call(t, mux, "GET", "/api/v1/connectors/example/thread?thread=u1&before=10&limit=500", h.cfg.Token, "")
	if msgs, _ := out["messages"].([]any); code != 200 || len(msgs) != 10 || out["before"] != nil {
		t.Fatalf("the last page: %d %v", code, out)
	}
	for _, c := range []struct {
		path string
		code int
	}{
		{"/api/v1/connectors/nope/thread?thread=u1", 404},
		{"/api/v1/connectors/example/thread", 400},
		{"/api/v1/connectors/example/thread?thread=u9", 502},
	} {
		if code, _ := call(t, mux, "GET", c.path, h.cfg.Token, ""); code != c.code {
			t.Errorf("%s: %d, want %d", c.path, code, c.code)
		}
	}
	c.state.Store(connector.StateRateLimited)
	if code, out := call(t, mux, "GET", "/api/v1/connectors/example/thread?thread=u1", h.cfg.Token, ""); code != 429 || out["code"] != "rate_limited" ||
		!strings.Contains(fmt.Sprint(out["error"]), "还要 30 秒") {
		t.Fatalf("rate limited: %d %v", code, out)
	}
	c.state.Store(connector.StateNotSignedIn)
	if code, out := call(t, mux, "GET", "/api/v1/connectors/example/thread?thread=u1", h.cfg.Token, ""); code != 409 || out["code"] != "unavailable" {
		t.Fatalf("signed out: %d %v", code, out)
	}
}

// An Inbox item with no conversation of its own (#5537) reads as a page that is not one, with no messages: a
// subscription's item (its source is the subscription, not a connector; deleted since or not) and a connector's
// without threads. The apps show what the hub collected for it, older ones too, with no error card. Replies and media
// still say there is none; an id that is neither is still no connector.
func TestThreadWithoutConversation(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.store = newMemStore()
	h.connectors = connector.NewRegistry(nil)
	if err := h.addConnector(&exampleConnector{}); err != nil {
		t.Fatal(err)
	}
	store := subscriptions.NewMemStore()
	h.subs = subscriptions.New(subscriptions.Options{Store: store})
	if err := store.CreateSubscription(ctx, subscriptions.Subscription{ID: "sub_example1", Connector: "mail", Label: "示例邮件"}); err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	none := func(path, thread string) {
		t.Helper()
		code, out := call(t, mux, "GET", path, h.cfg.Token, "")
		chat, _ := out["chat"].(map[string]any)
		if msgs, ok := out["messages"].([]any); code != 200 || !ok || len(msgs) != 0 || chat["kind"] != connector.ThreadKindNone || chat["id"] != thread ||
			out["before"] != nil {
			t.Fatalf("%s: %d %v", path, code, out)
		}
		if _, ok := chat["name"]; !ok {
			t.Fatalf("%s: chat without a name (older apps need one): %v", path, chat)
		}
	}
	none("/api/v1/connectors/sub_example1/thread?thread=sub:sub_example1:t1&limit=30", "sub:sub_example1:t1")
	none("/api/v1/connectors/sub_example1/thread?thread=other", "other")
	none("/api/v1/connectors/sub_gone/thread?thread=sub:sub_gone:t1", "sub:sub_gone:t1")
	none("/api/v1/connectors/example/thread?thread=example-org/example-repo%2312", "example-org/example-repo#12")
	for _, c := range []struct {
		method, path, body string
		code               int
	}{
		{"GET", "/api/v1/connectors/nope/thread?thread=u1", "", 404},
		{"GET", "/api/v1/connectors/nope/thread?thread=sub:sub_example1:t1", "", 404},
		{"GET", "/api/v1/connectors/sub_example1/media?ref=avatar:u1:1", "", 404},
		{"POST", "/api/v1/connectors/sub_example1/thread/reply", `{"thread":"sub:sub_example1:t1","action":"x","text":"好的"}`, 404},
		{"POST", "/api/v1/connectors/example/thread/reply", `{"thread":"t1","action":"example_send","text":"好的"}`, 409},
	} {
		if code, _ := call(t, mux, c.method, c.path, h.cfg.Token, c.body); code != c.code {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, code, c.code)
		}
	}
}

// A media is fetched once however many ask for it at once, kept on disk, and served from there afterwards (even with
// the connector signed out); one that cannot be fetched is not kept.
func TestConnectorMedia(t *testing.T) {
	h, c, mux := threadsHub(t)
	get := func(ref string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/connectors/example/media?ref="+ref, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := get("avatar:u1:1"); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), connectorTestPNG()) || w.Header().Get("Content-Type") != "image/png" ||
				!strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
				t.Errorf("%d %q %v", w.Code, w.Body.String(), w.Header())
			}
		}()
	}
	wg.Wait()
	if n := c.fetched.Load(); n != 1 {
		t.Fatalf("fetched %d times", n)
	}
	c.state.Store(connector.StateNotSignedIn)
	if w := get("avatar:u1:1"); w.Code != 200 || c.fetched.Load() != 1 {
		t.Fatalf("not served from disk: %d, %d fetches", w.Code, c.fetched.Load())
	}
	c.state.Store(connector.StateOK)
	if w := get("photo:missing"); w.Code != 502 {
		t.Fatalf("missing: %d", w.Code)
	}
	if _, ok := h.media.get(h.mediaDir(), "example", "photo:missing"); ok {
		t.Fatal("a failed fetch was kept")
	}
	if w := get(""); w.Code != 400 {
		t.Fatalf("no ref: %d", w.Code)
	}
}

// The oldest media go once more than the most is kept.
func TestPruneMedia(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	for i, name := range []string{"a", "b", "c"} {
		p := filepath.Join(dir, "example", name)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, make([]byte, 100), 0o600)
		_ = os.Chtimes(p, old.Add(time.Duration(i)*time.Minute), old.Add(time.Duration(i)*time.Minute))
	}
	pruneMedia(dir, 250)
	for name, want := range map[string]bool{"a": false, "b": true, "c": true} {
		if _, err := os.Stat(filepath.Join(dir, "example", name)); (err == nil) != want {
			t.Errorf("%s kept %v, want %v", name, err == nil, want)
		}
	}
}

// A reply that does not come from one of the user's devices (here the pairing token) goes through the action the page
// named and the gate: declined, nothing is sent; allowed, the action gets {chat, text, reply_to}. Another connector's
// action, or none, is refused.
func TestConnectorThreadReply(t *testing.T) {
	h, c, mux := threadsHub(t)
	post := func(body string, allow bool) (int, map[string]any) {
		var carded []string
		hubOf(h).answer = func(ix proto.Interaction) proto.Resolution { // the card on the phone (the hub's)
			carded = append(carded, ix.ToolName)
			return proto.Resolution{Allow: &allow}
		}
		code, out := call(t, mux, "POST", "/api/v1/connectors/example/thread/reply", h.cfg.Token, body)
		if len(carded) != 1 || carded[0] != "example_send" {
			t.Errorf("the cards on the phone: %v", carded)
		}
		return code, out
	}
	if code, out := post(`{"thread":"u1","action":"example_send","text":"示例回复","reply_to":"39"}`, false); code != 403 || out["code"] != "declined" {
		t.Fatalf("declined: %d %v", code, out)
	}
	if len(c.sent) != 0 {
		t.Fatalf("declined, yet sent %v", c.sent)
	}
	code, out := post(`{"thread":"u1","action":"example_send","text":"示例回复","reply_to":"39"}`, true)
	if code != 200 || out["ok"] != true || len(c.sent) != 1 || c.sent[0] != `{"chat":"u1","reply_to":39,"text":"示例回复"}` {
		t.Fatalf("allowed: %d %v, sent %v", code, out, c.sent)
	}
	for _, body := range []string{`{"thread":"u1","action":"other_send","text":"x"}`, `{"thread":"u1","action":"example_nope","text":"x"}`,
		`{"thread":"u1","action":"example_send","text":"  "}`, `{"action":"example_send","text":"x"}`} {
		if code, _ := call(t, mux, "POST", "/api/v1/connectors/example/thread/reply", h.cfg.Token, body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
}

// A reply the user sends from their own app (a phone's or the web's device token) runs as they sent it: no card, and
// not even a deny rule in connector_gate stops it, the gate being for the AI's writes. With the same rule the pairing
// token, a pairing code and the assistant's mcp.call are refused; with none they would be asked about.
func TestConnectorThreadReplyFromTheUsersDevice(t *testing.T) {
	device := func(_ *Manager, id, _, role string) string { return deviceToken(id, role) } // the hub's auth found it
	const body = `{"thread":"u1","action":"example_send","text":"示例回复"}`

	h, c, mux := threadsHub(t)
	// A card that shows up anyway is declined at once, so the reply fails here instead of waiting for it.
	carded := make(chan string, 4)
	hubOf(h).answer = func(ix proto.Interaction) proto.Resolution {
		carded <- ix.ToolName
		no := false
		return proto.Resolution{Allow: &no}
	}
	for _, tok := range []string{device(h, "dev_phone", "phone", roleFull), device(h, "dev_web", "other", roleFull)} {
		if code, out := call(t, mux, "POST", "/api/v1/connectors/example/thread/reply", tok, body); code != 200 || out["ok"] != true {
			t.Fatalf("from the device: %d %v", code, out)
		}
	}
	if len(carded) != 0 {
		t.Fatalf("a card for the user's own reply: %s", <-carded)
	}
	if len(c.sent) != 2 || c.sent[0] != `{"chat":"u1","text":"示例回复"}` {
		t.Fatalf("sent %v", c.sent)
	}

	h, c, mux = threadsHubWith(t, []connector.Rule{{Connector: "example", Action: "example_send", Decision: connector.Deny}})
	if code, out := call(t, mux, "POST", "/api/v1/connectors/example/thread/reply", device(h, "dev_phone", "phone", roleFull), body); code != 200 || out["ok"] != true || len(c.sent) != 1 {
		t.Fatalf("a deny rule stopped the user's own reply: %d %v, sent %v", code, out, c.sent)
	}
	pairing := "jp_" + randomHex(8) // a pairing code: a full role, but not one of the user's own signed-in apps
	testDevices.Store(pairing, principal{ID: "pair", Role: roleFull, Paired: true})
	for name, tok := range map[string]string{"the pairing token": h.cfg.Token, "a pairing code": pairing} {
		if status, out := call(t, mux, "POST", "/api/v1/connectors/example/thread/reply", tok, body); status != 403 || out["code"] != "declined" {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
	// The assistant's mcp.call (the hub hands it to the manager: hubConnectorTool).
	params, _ := json.Marshal(map[string]any{"name": "example_send", "arguments": map[string]any{"chat": "u1", "text": "示例回复"},
		"assistant": true, "machine_id": "box"})
	out, err := testHubCall(context.Background(), h, hubConnectorTool, params)
	if res, _ := out.(map[string]any); err != nil || res["handled"] != true || !res["result"].(proto.ToolResult).IsError {
		t.Fatalf("the assistant got past the gate: %v %+v", err, out)
	}
	if len(c.sent) != 1 {
		t.Fatalf("sent %v", c.sent)
	}
}
