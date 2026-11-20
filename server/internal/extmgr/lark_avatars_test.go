package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/extwire"
)

// The test plugin has two gateway slots whose avatars have the same key but different dimensions.
// Its chat page reports a Feishu user identity and its media endpoint serves the exact thumbnail requested.
func serveTestLarkAvatars(w http.ResponseWriter, r *http.Request, reads *atomic.Int32) bool {
	switch r.URL.EscapedPath() {
	case "/api/v1/lark/accounts":
		io.WriteString(w, `{"default":2,"accounts":[{"id":2,"avatar":"photo/one"},{"id":3,"avatar":"photo/one"}]}`)
	case "/api/v1/lark/conversations":
		fmt.Fprintf(w, `{"account_id":"user-68","slot":%q,"conversations":[{"avatar":"/agent/v1/media/photo%%2Fone","vendor":{"face":"x"}}]}`, r.URL.Query().Get("ol_account"))
	case "/api/v1/lark/users/person":
		io.WriteString(w, `{"avatarUrl":"photo/one","unrelated":9007199254740993}`)
	case "/api/v1/lark/media/photo%2Fone":
		if r.Header.Get("X-Persona-Role") != roleFull || r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected caller", http.StatusForbidden)
			return true
		}
		edge, _ := strconv.Atoi(r.URL.Query().Get("size"))
		slot := r.URL.Query().Get("ol_account")
		if edge < 24 || edge > 624 || edge%24 != 0 || (slot != "2" && slot != "3") {
			http.Error(w, "bad thumbnail query", http.StatusBadRequest)
			return true
		}
		reads.Add(1)
		height := edge
		if slot == "2" {
			height /= 2
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(avatarPNG(edge, height))
	case "/api/v1/examplefeed/avatar-reads":
		fmt.Fprintf(w, `{"reads":%d}`, reads.Load())
	default:
		return false
	}
	return true
}

func TestLarkAvatarPagesAcrossTheRegisteredGateway(t *testing.T) {
	h, _ := extensionTestHub(t)
	dir := t.TempDir()
	h.avatars = newAvatars(dir, nil, t.Logf)
	mux := h.routes()
	token := h.cfg.Token
	_, preview := call(t, mux, "POST", "/api/v1/plugins/-/preview", token, `{"source":"dir:`+apiPackage(t)+`"}`)
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", token,
		fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"])); code != 200 {
		t.Fatalf("install: %d %v", code, out)
	}
	waitLong(t, "Lark API ready", func() bool {
		hp := h.extensions.get("example-feed")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})
	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	_, accounts := call(t, mux, "GET", "/api/v1/lark/accounts", token, "")
	rows := accounts["accounts"].([]any)
	for i, n := range []int{2, 3} {
		row := rows[i].(map[string]any)
		if row["avatar_url"] != extwire.LarkAvatarURL("photo/one", n) || row["avatar"] != "photo/one" {
			t.Fatalf("gateway account %d: %+v", n, row)
		}
	}
	_, chats := call(t, mux, "GET", "/api/v1/lark/conversations", token, "")
	chat := chats["conversations"].([]any)[0].(map[string]any)
	if chats["account_id"] != "user-68" || chats["slot"] != "2" || chat["avatar_url"] != extwire.LarkAvatarURL("photo/one", 2) || chat["avatar"] != "/agent/v1/media/photo%2Fone" {
		t.Fatalf("default gateway account: %+v", chats)
	}
	profile := get(token, "/api/v1/lark/users/person?ol_account=3").Body.String()
	if !strings.Contains(profile, extwire.LarkAvatarURL("photo/one", 3)) || !strings.Contains(profile, "9007199254740993") {
		t.Fatalf("explicit account and original numeric payload: %s", profile)
	}
	for _, tt := range []struct{ slot, query, width, height, reads int }{
		{2, 60, 64, 32, 1}, {2, 64, 64, 32, 1}, {2, 500, 512, 256, 2}, {3, 50, 64, 64, 3},
	} {
		path := extwire.LarkAvatarURL("photo/one", tt.slot) + "&size=" + strconv.Itoa(tt.query)
		w := get(token, path)
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("picture %s: %d, %v", path, w.Code, w.Header())
		}
		cfg, _, err := image.DecodeConfig(w.Body)
		if err != nil || cfg.Width != tt.width || cfg.Height != tt.height {
			t.Fatalf("%s: %v, %v", path, cfg, err)
		}
		_, out := call(t, mux, "GET", "/api/v1/examplefeed/avatar-reads", token, "")
		if out["reads"] != float64(tt.reads) {
			t.Fatalf("%s fetched %v times", path, out)
		}
	}
	cache := filepath.Join(dir, "cache", "avatars", larkAvatarCache("example-feed"))
	if entries, err := os.ReadDir(cache); err != nil || len(entries) == 0 {
		t.Fatalf("no cached Lark pictures: %v", err)
	}
	for _, path := range []string{extwire.AvatarURL("lark", "photo/one") + "?lark_account=not-a-number", extwire.AvatarURL("lark", "not/a/../key") + "?lark_account=2"} {
		if w := get(token, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if w := get(deviceToken("watch", roleAnswer), extwire.LarkAvatarURL("photo/one", 3)); w.Code != http.StatusForbidden {
		t.Errorf("limited device: %d", w.Code)
	}
	if code, out := call(t, mux, "DELETE", "/api/v1/plugins/example-feed/-/", token, ""); code != 200 {
		t.Fatalf("remove gateway: %d %v", code, out)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("gateway avatar cache after removal: %v", err)
	}
	if w := get(token, extwire.LarkAvatarURL("photo/one", 3)); w.Code != http.StatusNotFound {
		t.Errorf("removed gateway: %d", w.Code)
	}
}

func TestLarkAvatarProjectsOnlyTypedPictures(t *testing.T) {
	for _, tt := range []struct{ route, body, want string }{
		{"conversations/x/members", `{"members":[{"avatar":"a"}]}`, "avatar_url"},
		{"messages/x/thread", `{"root":{"sender_avatar":"a"},"messages":[{"sender_avatar":"b","parts":[{"avatar":"opaque"}]}]}`, "sender_avatar_url"},
		{"conversations/x/pins", `{"items":[{"message":{"sender_avatar":"a"}}]}`, "sender_avatar_url"},
		{"conversations/x/pin-messages/y", `{"target":{"root":{"sender_avatar":"a"},"page":{"messages":[{"sender_avatar":"b"}]}}}`, "sender_avatar_url"},
		{"flags", `{"messages":[{"chat_avatar":"a","message":{"sender_avatar":"b"}}]}`, "chat_avatar_url"},
	} {
		p := larkAvatarPage{larkAvatarPageShape("/api/v1/lark/" + tt.route), 2}
		var body map[string]any
		if json.Unmarshal([]byte(tt.body), &body) != nil || !p.add(body, p.shape) {
			t.Fatalf("no avatar added to %s", tt.route)
		}
		b, _ := json.Marshal(body)
		if !bytes.Contains(b, []byte(tt.want)) || bytes.Contains(b, []byte(extwire.LarkAvatarURL("opaque", 2))) {
			t.Errorf("%s: %s", tt.route, b)
		}
	}
	for _, raw := range []string{"raw/users/p", "media/file", "messages/x/media/image", "conversations/x/labels", "users/p/chat"} {
		if shape := larkAvatarPageShape("/api/v1/lark/" + raw); shape != "" {
			t.Errorf("%s has a typed avatar page %s", raw, shape)
		}
	}
}

func TestLargeLarkAvatarPageStillStreamsAllBytes(t *testing.T) {
	b := bytes.Repeat([]byte("x"), (4<<20)+55)
	req := httptest.NewRequest("GET", "/api/v1/lark/pinned", nil)
	req = req.WithContext(context.WithValue(req.Context(), larkAvatarPageKey{}, larkAvatarPage{"conversations", 2}))
	res := &http.Response{StatusCode: 200, Request: req, Header: http.Header{}, ContentLength: -1, Body: io.NopCloser(bytes.NewReader(b))}
	if err := addLarkAvatarResponse(res); err != nil {
		t.Fatal(err)
	}
	all, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || !bytes.Equal(all, b) {
		t.Fatal("large plugin reply changed")
	}
}
