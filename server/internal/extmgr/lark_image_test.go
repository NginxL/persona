package extmgr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestLarkImageReadsSelectedAccountAndMessage(t *testing.T) {
	var pic bytes.Buffer
	if err := png.Encode(&pic, image.NewGray(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ args, account string }{
		{`{"message":"m1","account":7}`, "7"},
		{`{"message":"m1"}`, "2"},
	} {
		var calls []string
		hp := &hubExtension{proxy: &httputil.ReverseProxy{Transport: feishuResourceTransport(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.Method+" "+r.URL.RequestURI())
			var data []byte
			switch r.URL.Path {
			case "/api/v1/messaging/lark/accounts":
				data = []byte(`{"default":"2","accounts":[]}`)
			case "/api/v1/messaging/lark/messages/m1":
				data = []byte(fmt.Sprintf(`{"account":%q,"message":{"id":"m1","position":"1","sender":{"name":"Peer"},"mine":false,"text":"See screenshot","parts":[{"kind":"image","media":"messages/m1/media/screenshot?ol_account=99"}]}}`, tc.account))
			case "/api/v1/messaging/lark/messages/m1/media/screenshot":
				data = pic.Bytes()
			default:
				t.Fatalf("unexpected request %s", r.URL)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
		})}}
		res := readLarkImages(t.Context(), hp, true, json.RawMessage(tc.args))
		if res.IsError || len(res.Content) != 3 || res.Content[2].Data != base64.StdEncoding.EncodeToString(pic.Bytes()) || res.Content[2].MimeType != "image/png" || !strings.Contains(res.Content[0].Text, "See screenshot") {
			t.Fatalf("answer: %+v", res)
		}
		var want []string
		if tc.account == "2" {
			want = append(want, "GET /api/v1/messaging/lark/accounts")
		}
		want = append(want, "GET /api/v1/messaging/lark/messages/m1?account="+tc.account,
			"GET /api/v1/messaging/lark/messages/m1/media/screenshot?account="+tc.account)
		if !slices.Equal(calls, want) {
			t.Fatal(calls)
		}
	}
}

func TestLarkImagePageBoundsAttemptsAndKeepsText(t *testing.T) {
	for _, tc := range []struct{ args, path string }{
		{`{"chat_id":"c","before":"42","account":2}`, "conversations/c/messages"},
		{`{"thread":"topic","before":"42","account":2}`, "messages/topic/thread"},
	} {
		var messages []string
		for i := 0; i < 13; i++ {
			messages = append(messages, fmt.Sprintf(`{"id":"m%d","text":"body-%02d","parts":[{"kind":"image","media":"media/%d"}],"private":%v,"recalled":%v}`, i, i, i, i == 12, i == 11))
		}
		var calls []string
		var oversizedRead, closed int
		hp := &hubExtension{proxy: &httputil.ReverseProxy{Transport: feishuResourceTransport(func(r *http.Request) (*http.Response, error) {
			path := strings.TrimPrefix(r.URL.Path, "/api/v1/messaging/lark/")
			calls = append(calls, path)
			if r.Method != "GET" || r.URL.Query().Get("account") != "2" {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL)
			}
			res := &http.Response{StatusCode: 200}
			switch path {
			case tc.path:
				if r.URL.Query().Get("limit") != "10" || r.URL.Query().Get("before") != "42" {
					t.Fatal(r.URL)
				}
				res.Body = io.NopCloser(strings.NewReader(`{"has_older":true,"older_cursor":"7","messages":[` + strings.Join(messages, ",") + `]}`))
			case "media/10":
				res.Body = feishuResourceBody{Reader: strings.NewReader(strings.Repeat("x", 2<<20)), read: &oversizedRead, closed: &closed}
			case "media/9":
				res.StatusCode = 404
				res.Body = io.NopCloser(strings.NewReader("private error detail"))
			case "media/8":
				res.Body = io.NopCloser(strings.NewReader("corrupt picture"))
			default:
				t.Fatalf("unexpected read %s", r.URL)
			}
			return res, nil
		})}}
		res := readLarkImages(t.Context(), hp, true, json.RawMessage(tc.args))
		if res.IsError || len(res.Content) != 1 {
			t.Fatalf("want only the message text: %+v", res)
		}
		got := res.Content[0].Text
		for _, want := range []string{"body-10", "body-03", `before="7"`, "size limit exceeded", "HTTP 404", "not a whole JPEG", "more image"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %s", want, got)
			}
		}
		for _, hidden := range []string{"body-12", "body-11", "body-02", "private error detail"} {
			if strings.Contains(got, hidden) {
				t.Errorf("unexpected %q in %s", hidden, got)
			}
		}
		if !slices.Equal(calls, []string{tc.path, "media/10", "media/9", "media/8"}) || oversizedRead != 1<<20+1 || closed != 1 {
			t.Fatalf("calls %v; read %d, closed %d", calls, oversizedRead, closed)
		}
	}
}

func TestLarkImageReadsThreadRootAfterAnOversizedReply(t *testing.T) {
	var pic bytes.Buffer
	if err := png.Encode(&pic, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	var calls []string
	var read, closed int
	hp := &hubExtension{proxy: &httputil.ReverseProxy{Transport: feishuResourceTransport(func(r *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/messaging/lark/")
		calls = append(calls, r.Method+" "+path)
		switch path {
		case "messages/root/thread":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"root":{"id":"root","text":"Original screenshot","parts":[{"kind":"image","media":"media/pic"}]},"messages":[{"id":"reply","text":"See update","parts":[{"kind":"image","media":"media/big"}]}]}`))}, nil
		case "media/big":
			return &http.Response{StatusCode: 200, ContentLength: 1<<20 + 1, Body: feishuResourceBody{Reader: strings.NewReader(""), read: &read, closed: &closed}}, nil
		case "media/pic":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(pic.Bytes()))}, nil
		default:
			t.Fatalf("unexpected read %s", r.URL)
			return nil, nil
		}
	})}}
	got := readLarkImages(t.Context(), hp, false, json.RawMessage(`{"thread":"root"}`))
	if !slices.Equal(calls, []string{"GET messages/root/thread", "GET media/big", "GET media/pic"}) || read != 0 || closed != 1 {
		t.Fatalf("calls %v, read %d, closed %d", calls, read, closed)
	}
	if got.IsError || len(got.Content) != 3 || got.Content[2].Data != base64.StdEncoding.EncodeToString(pic.Bytes()) ||
		!strings.Contains(got.Content[0].Text, "size limit exceeded") || !strings.Contains(got.Content[1].Text, `Message "root", image 1`) {
		t.Fatalf("thread result %+v", got)
	}
}

func TestLarkImageRejectsInvalidSelectorsAndMedia(t *testing.T) {
	hp := &hubExtension{proxy: &httputil.ReverseProxy{Transport: feishuResourceTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected source request %v", r.URL)
		return nil, nil
	})}}
	for _, args := range []string{`{}`, `{"message":"../x"}`, `{"chat_id":"a","message":"b"}`, `{"message":"a","before":"2"}`, `{"account":0,"message":"m"}`, `{"account":"other","message":"m"}`} {
		if res := readLarkImages(t.Context(), hp, true, json.RawMessage(args)); !res.IsError {
			t.Fatal(args)
		}
	}
	for _, ref := range []string{"https://example.org/pic", "//example.org/pic", "messages/other/media/x", "media/../conversations", "media/%2e%2e/x", "media/%252e%252e/x", "messages/m/media/../actions/x", "/api/v1/lark/raw/write"} {
		if _, err := larkImagePart(t.Context(), hp, "m", ref, "2"); err == nil {
			t.Fatal(ref)
		}
	}
}

func TestLarkImageDoesNotFetchFromAmbiguousAccountOrMessage(t *testing.T) {
	for _, tc := range []struct{ args, account, id string }{
		{`{"message":"m","account":7}`, "1", "m"},
		{`{"message":"m"}`, "", "m"},
		{`{"message":"m","account":1}`, "1", "different"},
	} {
		calls := 0
		hp := &hubExtension{proxy: &httputil.ReverseProxy{Transport: feishuResourceTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			raw := fmt.Sprintf(`{"account":%q,"message":{"id":%q,"parts":[{"kind":"image","media":"media/x"}]}}`, tc.account, tc.id)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))}, nil
		})}}
		if got := readLarkImages(t.Context(), hp, true, json.RawMessage(tc.args)); !got.IsError || calls != 1 {
			t.Fatalf("%+v calls=%d", got, calls)
		}
	}
}

// The fixture supplies both sides of one installed extension: its connector tool
// catalog and the Messaging source. No gateway process or credentials are needed.
func serveLarkImageFixture() {
	ln, err := net.Listen("unix", os.Getenv("PERSONA_PLUGIN_SOCKET"))
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/messaging/lark/accounts", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"default":"2","accounts":[{"id":"2","signed_in":true}]}`)
	})
	mux.HandleFunc("GET /api/v1/messaging/lark/conversations/chat/messages", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"messages":[{"id":"m","position":"1","sender":{"name":"Peer"},"mine":false,"text":"[Image]","parts":[{"kind":"image","media":"media/pic"}]}],"has_older":false}`)
	})
	mux.HandleFunc("GET /api/v1/messaging/lark/media/pic", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("account") != "2" {
			http.Error(w, "account", 400)
			return
		}
		png.Encode(w, image.NewGray(image.Rect(0, 0, 4, 3)))
	})
	go http.Serve(ln, mux)
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{
		ID: "sample-chat", Version: "1.0.0", Namespaces: map[string]pserve.Handler{
			"connector": func(_ context.Context, method string, _ json.RawMessage) (any, error) {
				switch method {
				case "status":
					return map[string]string{"state": "ok"}, nil
				case "tools.list":
					return map[string]any{"tools": []proto.Tool{{Name: "lark_messages", Description: "Read chat text", InputSchema: json.RawMessage(`{"type":"object"}`)}}}, nil
				default:
					return nil, pserve.UnknownMethod(method)
				}
			},
		},
	})
}

func TestLarkImageDiscoveryAndDispatch(t *testing.T) {
	h, _ := extensionTestHub(t)
	if known, read := h.ToolAccess("lark_image"); known || read {
		t.Fatal("image tool available before installation")
	}
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat"), []byte(fmt.Sprintf("#!/bin/sh\nPERSONA_TEST_PLUGIN=lark_images exec %q \"$@\"\n", exe)), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"manifest_version":1,"id":"sample-chat","name":"Sample Chat","version":"1.0.0","engines":{"plugin_protocol":1},"runtime":{"command":{%q:"chat"},"http":true},"activation_events":["on_startup"],"contributes":{"connectors":[{"id":"lark","name":"Sample Chat","capabilities":["status","tools"]}],"messaging":[{"id":"lark","name":"Sample Chat","icon":"chat","capabilities":["accounts"]}]}}`, runtime.GOOS+"-"+runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	installForTest(t, h, dir)
	waitLong(t, "Lark message reader", func() bool { known, read := h.ToolAccess("lark_image"); return known && read })
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		ctx := i18n.With(t.Context(), lang)
		_, tools := h.AgentTools(ctx, true)
		index := slices.IndexFunc(tools, func(t proto.Tool) bool { return t.Name == "lark_image" })
		if index < 0 {
			t.Fatal("assistant discovery omitted lark_image")
		}
		raw, err := json.MarshalIndent(tools[index], "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "lark_image."+string(lang), string(raw)+"\n")
	}
	args := json.RawMessage(`{"chat_id":"chat"}`)
	if _, handled := h.CallTool(t.Context(), "computer", "session", "lark_image", args, false); handled {
		t.Fatal("assistant-only tool dispatched for ordinary agent")
	}
	got, handled := h.CallTool(t.Context(), "computer", "session", "lark_image", args, true)
	if !handled || got.IsError || len(got.Content) != 3 || got.Content[2].Type != "image" || !strings.Contains(got.Content[1].Text, `Message "m", image 1`) {
		t.Fatalf("model result %+v, handled %v", got, handled)
	}
}
