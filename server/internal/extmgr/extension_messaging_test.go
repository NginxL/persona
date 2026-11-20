package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/extwire"
)

// serveMessagingExtensionForTest is a messaging source "examplechat": a good page of messages, a page that does not
// hold to the schema, a send that counts, media bytes; it pushes messaging.changed for its own source and for one it
// does not have.
func serveMessagingExtensionForTest() {
	ln, err := net.Listen("unix", os.Getenv("PERSONA_PLUGIN_SOCKET"))
	if err != nil {
		os.Exit(1)
	}
	var sends atomic.Int32
	var pictures atomic.Int32
	var identity atomic.Int32
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/messaging/examplechat/")
		switch {
		case rest == "me":
			account := "north"
			switch identity.Load() {
			case 1:
				account = "south + west"
			case 2:
				http.Error(w, "no default account", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"account": account, "user_id": "me"})
		case rest == "users/portrait":
			account := r.URL.Query().Get("account")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "portrait", "name": account, "avatar": "media/portrait.png?version=2"})
		case rest == "messages/switch/actions/default" && r.Method == http.MethodPost:
			identity.Add(1)
			_, _ = w.Write([]byte(`{}`))
		case rest == "accounts":
			_ = json.NewEncoder(w).Encode(map[string]any{"default": "north", "accounts": []any{
				map[string]any{"id": "north", "signed_in": true, "avatar": "media/portrait.png?version=2"},
				map[string]any{"id": "south + west", "signed_in": true, "avatar": "media/portrait.png?version=2"}}})
		case rest == "conversations":
			account := r.URL.Query().Get("account")
			if account == "" {
				account = "north"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"account": account, "conversations": []any{
				map[string]any{"id": "grp", "name": "Group", "kind": "group", "unread": 0, "preview": "", "avatar": "media/portrait.png?version=2"},
				map[string]any{"id": "dm", "name": "Friend", "kind": "direct", "unread": 0, "preview": "", "avatar": "https://photos.example.invalid/peer.png"},
				map[string]any{"id": "empty", "name": "Initial", "kind": "direct", "unread": 0, "preview": ""}}})
		case rest == "media/portrait.png":
			if r.URL.Query().Get("version") != "2" || r.URL.Query().Has("size") || r.Header.Get("Authorization") != "" ||
				r.Header.Get("X-Persona-Device") == "" || r.Header.Get("X-Persona-Role") != roleFull {
				http.Error(w, "wrong picture request", http.StatusBadRequest)
				return
			}
			pictures.Add(1)
			pixel := color.RGBA{R: 220, A: 255}
			if r.URL.Query().Get("account") == "south + west" {
				pixel = color.RGBA{B: 220, A: 255}
			}
			img := image.NewRGBA(image.Rect(0, 0, 300, 180))
			draw.Draw(img, img.Bounds(), image.NewUniform(pixel), image.Point{}, draw.Src)
			w.Header().Set("Content-Type", "image/png")
			_ = png.Encode(w, img)
		case rest == "status":
			_ = json.NewEncoder(w).Encode(map[string]any{"configured": true, "connected": true, "signed_in": true, "pictures": pictures.Load()})
		case r.Method == http.MethodPost && strings.HasSuffix(rest, "/messages"):
			_ = json.NewEncoder(w).Encode(map[string]any{"message_id": fmt.Sprintf("s%d", sends.Add(1))})
		case rest == "conversations/good/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"account": "1", "has_older": false, "messages": []any{
				map[string]any{"id": "m1", "position": "1", "sender": map[string]any{"name": "李雷"}, "mine": false, "text": "你好",
					"parts": []any{map[string]any{"kind": "text", "text": "你好"}}, "device": r.Header.Get("X-Persona-Device")}}})
		case rest == "conversations/bad/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"has_older": false, "messages": []any{
				map[string]any{"id": "m1", "position": "first", "sender": map[string]any{"name": "x"}, "mine": false, "text": "", "parts": []any{}}}})
		case rest == "conversations/gone/messages":
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "没登录", "code": "not_signed_in"})
		case strings.HasPrefix(rest, "posts/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "7", "author": map[string]any{"name": "李雷"}, "parts": []any{}})
		case strings.HasPrefix(rest, "media/"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG not json"))
		case rest == "vendor/raw":
			_, _ = w.Write([]byte(`{"secret": "a vendor's own route"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-chat", Version: "1.0.0",
		Init: func(_ context.Context, _ extension.InitializeParams, h *pserve.Hub) (map[string]any, error) {
			go func() {
				time.Sleep(300 * time.Millisecond)
				_ = h.Notify("messaging.changed", map[string]any{"source": "examplechat", "conversations": []string{"good"}})
				_ = h.Notify("messaging.changed", map[string]any{"source": "examplechat", "conversations": []string{"other"}, "settings_changed": true})
				_ = h.Notify("messaging.changed", map[string]any{"source": "someoneelse", "conversations": []string{"x"}})
			}()
			return nil, nil
		}})
}

// The connector and the messaging source can declare the same public ID.
type sameIDPictureConnector struct{ *pictureConnector }

func (*sameIDPictureConnector) ID() string { return "examplechat" }

func messagingPackage(t *testing.T) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "chat"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=messaging exec "+self+" \"$@\"\n"), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-chat", "name": "Example chat", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/chat"}, "http": true},
		"activation_events": ["on_startup"],
		"contributes": {
			"messaging": [{"id": "examplechat", "name": "Chat", "icon": "chat", "capabilities": ["accounts", "reactions", "nonsense_later"],
			               "composer_modes": [{"id": "sealed", "title": "加密", "attachments": false, "mentions": false, "prepare": true}],
			               "settings_ui": "display", "links": [{"hosts": ["*.example.com"], "opens": "document"}]}],
			"ui": [{"id": "display", "title": "显示设置", "placement": "extension_page"}]}}`, runtime.GOOS+"-"+runtime.GOARCH)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// A messaging source: listed for the apps, its routes relayed with who is asking, an answer that does not hold to the
// schema stopped at the hub, errors and bytes passed as they are, routes outside the protocol refused, sends left to
// the extension, and its changes coalesced into one push per source.
func TestExtensionMessaging(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token

	if _, out := call(t, mux, "GET", "/api/v1/messaging", master, ""); fmt.Sprint(out["sources"]) != "[]" {
		t.Fatalf("no source before the extension: %v", out)
	}
	code0, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+messagingPackage(t)+`"}`)
	if code0 != 200 {
		t.Fatalf("preview: %d %v", code0, pv)
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	waitLong(t, "the extension up", func() bool {
		hp := h.extensions.get("example-chat")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})

	_, out := call(t, mux, "GET", "/api/v1/messaging", master, "")
	sources, _ := out["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources %v", out)
	}
	src := sources[0].(map[string]any)
	if src["extension"] != "example-chat" || src["id"] != "examplechat" || src["name"] != "Chat" || src["settings_ui"] != "display" {
		t.Fatalf("the source: %v", src)
	}
	if !slices.Contains(h.features(), "messaging") {
		t.Fatal("the hub has the messaging feature while a source is there")
	}

	code, out := call(t, mux, "GET", "/api/v1/messaging/examplechat/conversations/good/messages?before=10", master, "")
	if code != 200 || out["account"] != "1" {
		t.Fatalf("a good page: %d %v", code, out)
	}
	if code, out := call(t, mux, "GET", "/api/v1/messaging/examplechat/conversations/bad/messages", master, ""); code != http.StatusBadGateway || out["code"] != "extension_bad_response" {
		t.Fatalf("a page that does not hold to the schema: %d %v", code, out)
	}
	if code, out := call(t, mux, "GET", "/api/v1/messaging/examplechat/conversations/gone/messages", master, ""); code != http.StatusBadGateway || out["code"] != "not_signed_in" {
		t.Fatalf("the extension's error as it is: %d %v", code, out)
	}
	req, _ := http.NewRequest("GET", "/api/v1/messaging/examplechat/media/a.png?size=96", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+master)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "\x89PNG") {
		t.Fatalf("media bytes: %d %q", w.Code, w.Body.String())
	}
	t.Run("avatars", func(t *testing.T) { checkMessagingSourcePictures(t, mux, master) })
	// A link escaped into one segment stays one: posts/{id} gets it.
	if code, _ := call(t, mux, "GET", "/api/v1/messaging/examplechat/posts/https%3A%2F%2Fapplink.example.com%2Fp%3FpostId%3D7", master, ""); code == http.StatusNotFound {
		t.Fatalf("an escaped link as a post id: %d", code)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/messaging/examplechat/vendor/raw", master, ""); code != http.StatusNotFound {
		t.Fatalf("a route outside the protocol: %d", code)
	}
	if code, _ := call(t, mux, "GET", "/api/v1/messaging/nosuch/conversations", master, ""); code != http.StatusNotFound {
		t.Fatalf("a source nobody has: %d", code)
	}

	// A send reaches the extension every time (it drops a repeat by client_id itself): no receipt replays a refusal
	// the user can fix (the hub keeps none for these routes: they are no extension's api prefix).
	send := `{"client_id": "0b6d3c1e-7f2a-4a55-9d8e-2c1a4f5b6e7d", "text": "hi"}`
	for i, want := range []string{"s1", "s2"} {
		if code, out := call(t, mux, "POST", "/api/v1/messaging/examplechat/conversations/good/messages", master, send); code != 200 || out["message_id"] != want {
			t.Fatalf("send %d: %d %v", i, code, out)
		}
	}

	th := hubOf(h)
	eventually(t, 10*time.Second, "the source's changes reaching the app", func() bool { return len(th.broadcastsOf("messaging")) > 0 })
	ev := th.broadcastsOf("messaging")[0]
	if ev["source"] != "examplechat" || fmt.Sprint(ev["conversations"]) != "[good other]" || ev["settings_changed"] != true {
		t.Fatalf("one push for the source's changes: %v", ev)
	}
	time.Sleep(1500 * time.Millisecond)
	if got := th.broadcastsOf("messaging"); len(got) != 1 {
		t.Fatalf("a change for a source it does not have went out: %v", got)
	}
	t.Run("removed source", func(t *testing.T) {
		path := messagingAvatarURL("examplechat", "media/portrait.png?version=2", "north")
		other := &sameIDPictureConnector{&pictureConnector{}}
		other.state.Store(connector.StateOK)
		if err := h.connectors.Add(other); err != nil {
			t.Fatal(err)
		}
		if code, _ := call(t, mux, "GET", path, master, ""); code != 200 || other.fetched.Load() != 0 {
			t.Fatalf("messaging path reached the connector: %d, %d reads", code, other.fetched.Load())
		}
		if code, body := call(t, mux, "PUT", "/api/v1/extensions/example-chat/-/enabled", master, `{"enabled":false}`); code != 200 {
			t.Fatalf("disable: %d %v", code, body)
		}
		if code, _ := call(t, mux, "GET", path, master, ""); code != 404 {
			t.Fatalf("disabled source still answers: %d", code)
		}
		if code, _ := call(t, mux, "GET", extwire.AvatarURL("examplechat", "a.u1.501"), master, ""); code != 200 || other.fetched.Load() != 1 {
			t.Fatalf("the connector still owns its original path: %d, %d reads", code, other.fetched.Load())
		}
		cache := filepath.Join(h.cfg.dir, "cache", "avatars", messagingAvatarCache("examplechat"))
		if files, err := os.ReadDir(cache); err != nil || len(files) == 0 {
			t.Fatalf("no source pictures to uninstall: %d files, %v", len(files), err)
		}
		if code, body := call(t, mux, "DELETE", "/api/v1/extensions/example-chat/-/", master, ""); code != 200 {
			t.Fatalf("uninstall: %d %v", code, body)
		}
		other.state.Store(connector.StateNotSignedIn)
		if code, _ := call(t, mux, "GET", extwire.AvatarURL("examplechat", "a.u1.501")+"?size=64", master, ""); code != 200 || other.fetched.Load() != 1 {
			t.Fatalf("uninstalling messaging must keep the signed-out connector's original: %d, %d reads", code, other.fetched.Load())
		}
		if files, err := os.ReadDir(cache); len(files) > 0 || err != nil && !os.IsNotExist(err) {
			t.Fatalf("removed source cache: %d files, %v", len(files), err)
		}
	})
}

// Use the installed program's ordinary app paths. The two accounts give the
// same ref distinct pixels, and listing any page fetches zero pictures.
func checkMessagingSourcePictures(t *testing.T, mux http.Handler, master string) {
	t.Helper()
	const source = "/api/v1/messaging/examplechat/"
	const ref = "media/portrait.png?version=2"
	rows := func(path, key string) []any {
		t.Helper()
		code, page := call(t, mux, "GET", source+path, master, "")
		if code != 200 {
			t.Fatalf("%s: %d %v", path, code, page)
		}
		return page[key].([]any)
	}
	accounts := rows("accounts", "accounts")
	chats := rows("conversations", "conversations")
	pic := func(row any) string {
		t.Helper()
		x := row.(map[string]any)
		path, ok := x["avatar_url"].(string)
		if !ok || x["avatar"] != ref || !strings.HasPrefix(path, extwire.AvatarURL("examplechat", ref)+"?") {
			t.Fatalf("expected the source value and the manager's account-bound path: %v", x)
		}
		return path
	}
	north, south := pic(accounts[0]), pic(accounts[1])
	if north == south || pic(chats[0]) != north || pic(rows("conversations?account="+url.QueryEscape("south + west"), "conversations")[0]) != south {
		t.Fatalf("account paths: north %q, south %q", north, south)
	}
	profile := func(want string) {
		t.Helper()
		code, body := call(t, mux, "GET", source+"users/portrait", master, "")
		if code != 200 || pic(body) != want || body["name"] == "" {
			t.Fatalf("an unqualified profile must belong to its explicitly pinned account: %d %v", code, body)
		}
	}
	profile(north)
	if code, body := call(t, mux, "POST", source+"messages/switch/actions/default", master, "{}"); code != 200 {
		t.Fatalf("switch default: %d %v", code, body)
	}
	profile(south)
	if code, body := call(t, mux, "POST", source+"messages/switch/actions/default", master, "{}"); code != 200 {
		t.Fatalf("clear default: %d %v", code, body)
	}
	if code, body := call(t, mux, "GET", source+"users/portrait", master, ""); code != 200 || body["avatar"] != ref || body["avatar_url"] != nil {
		t.Fatalf("an unavailable default must keep the original profile without an account-bound picture: %d %v", code, body)
	}
	if code, _ := call(t, mux, "GET", messagingAvatarURL("examplechat", ref, ""), master, ""); code != 404 {
		t.Fatalf("an unidentified multi-account picture must not read the current default: %d", code)
	}
	https := chats[1].(map[string]any)
	if https["avatar_url"] != extwire.AvatarURL("examplechat", https["avatar"].(string))+"?messaging=1" || chats[2].(map[string]any)["avatar_url"] != nil {
		t.Fatalf("public picture and initial: %v", chats)
	}
	count := func(want float64) {
		t.Helper()
		_, status := call(t, mux, "GET", source+"status", master, "")
		if status["pictures"] != want {
			t.Fatalf("fetched %v originals, want %v", status["pictures"], want)
		}
	}
	count(0)
	get := func(path string, size int, blue bool) {
		t.Helper()
		req := httptest.NewRequest("GET", fmt.Sprintf("%s&size=%d", path, size), nil)
		req.Header.Set("Authorization", "Bearer "+deviceToken("avatar-phone", roleFull))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("avatar %s: %d %s", path, w.Code, w.Body)
		}
		img, _, err := image.Decode(w.Body)
		if err != nil || img.Bounds().Dx() != size {
			t.Fatalf("size %d: %v %v", size, img, err)
		}
		r, _, b, _ := img.At(2, 2).RGBA()
		if (b > r) != blue {
			t.Fatalf("wrong account picture for %s", path)
		}
	}
	get(north, 64, false)
	get(south, 64, true)
	get(north, 256, false)
	get(south, 64, true)
	count(2)
}

// Changes folded within the second keep what each said: the chats of both, and every chat when one of them names none
// (read everything again) or together they name more than a push can.
func TestMergeMessagingChanged(t *testing.T) {
	many := make([]string, maxMessagingChanged)
	for i := range many {
		many[i] = fmt.Sprint("c", i)
	}
	for _, c := range []struct {
		first, then, want []string
	}{
		{[]string{"a"}, []string{"b", "a"}, []string{"a", "b"}},
		{nil, []string{"b"}, nil},
		{[]string{"a"}, nil, nil},
		{many, []string{"one more"}, nil},
	} {
		have := &wire.MessagingChanged{Source: "examplechat", Conversations: slices.Clone(c.first)}
		mergeMessagingChanged(have, &wire.MessagingChanged{Source: "examplechat", Conversations: c.then, SettingsChanged: true})
		if !slices.Equal(have.Conversations, c.want) || !have.SettingsChanged {
			t.Fatalf("%v then %v: %v (settings %v), want %v", len(c.first), c.then, have.Conversations, have.SettingsChanged, c.want)
		}
	}
}

// A manifest's messaging is checked: its settings page and the pages its links open are its own extension_page.
func TestMessagingManifestChecks(t *testing.T) {
	base := `{"manifest_version": 1, "id": "x-chat", "name": "X", "version": "1.0.0", "engines": {"plugin_protocol": 1},
		"runtime": {"command": {"linux-amd64": "bin/x"}, "http": true}, "activation_events": [%s],
		"contributes": {"messaging": [%s], "ui": [{"id": "display", "title": "显示", "placement": "extension_page"}]}}`
	for name, c := range map[string]struct {
		activation, item string
		ok               bool
	}{
		"fine":            {`"on_startup"`, `{"id": "x", "name": "X", "settings_ui": "display", "links": [{"hosts": ["x.com"], "opens": "ui:display"}]}`, true},
		"not on startup":  {`"on_call"`, `{"id": "x", "name": "X"}`, false},
		"no such page":    {`"on_startup"`, `{"id": "x", "name": "X", "settings_ui": "other"}`, false},
		"link to nowhere": {`"on_startup"`, `{"id": "x", "name": "X", "links": [{"hosts": ["x.com"], "opens": "ui:nope"}]}`, false},
		"bad id":          {`"on_startup"`, `{"id": "X!", "name": "X"}`, false},
		"unknown field":   {`"on_startup"`, `{"id": "x", "name": "X", "colour": "red"}`, false},
	} {
		_, err := extension.Parse([]byte(fmt.Sprintf(base, c.activation, c.item)))
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A newer extension's part and block kinds: in their plainest form, not a refused page.
func TestFitMessagingAnswer(t *testing.T) {
	raw := `{"has_older": false, "messages": [{"id": "m1", "position": "1", "sender": {"name": "x"}, "mine": false, "text": "t",
		"parts": [{"kind": "text", "text": "a"}, {"kind": "poll", "text": "投票：午饭吃什么", "options": ["面"]}, {"kind": "hologram"}]}]}`
	if wire.ValidateJSON("MessagingMessagePage", []byte(raw)) == nil {
		t.Fatal("an unknown kind does not hold as it is")
	}
	out, changed := fitMessagingAnswer([]byte(raw))
	if !changed || wire.ValidateJSON("MessagingMessagePage", out) != nil {
		t.Fatalf("fitted: %v %s", changed, out)
	}
	if !strings.Contains(string(out), `{"kind":"text","text":"投票：午饭吃什么"}`) || strings.Contains(string(out), "hologram") {
		t.Fatalf("fitted: %s", out)
	}
	doc := `{"title": "d", "url": "https://x", "blocks": [{"kind": "mindmap", "parts": [{"kind": "text", "text": "x"}]}]}`
	if out, changed := fitMessagingAnswer([]byte(doc)); !changed || wire.ValidateJSON("MessagingDocument", out) != nil || !strings.Contains(string(out), `"paragraph"`) {
		t.Fatalf("a block: %v %s", changed, out)
	}
}

// The routes the hub passes on: exactly the protocol's, a media path of any depth, nothing else.
func TestMessagingRouteMatch(t *testing.T) {
	for _, c := range []struct {
		method, path, answer string
		ok                   bool
	}{
		{"GET", "conversations", "MessagingConversationPage", true},
		{"GET", "conversations/7123/messages", "MessagingMessagePage", true},
		{"POST", "conversations/7123/messages", "MessagingSendReceipt", true},
		{"GET", "messages/m1", "MessagingMessageAnswer", true},
		{"GET", "messages/m1/thread", "MessagingMessagePage", true},
		{"GET", "media/a/b/c.png", "", true},
		{"GET", "media/", "", false},
		{"GET", "conversations//messages", "", false},
		{"DELETE", "conversations/7123/messages", "", false},
		{"GET", "raw/accounts", "", false},
	} {
		rt, ok := lookupMessagingRoute(c.method, c.path)
		if ok != c.ok || (ok && rt.Answer != c.answer) {
			t.Errorf("%s %s: %v %+v", c.method, c.path, ok, rt)
		}
	}
}
