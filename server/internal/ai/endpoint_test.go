package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/modelhttp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func okStreamResponse(r *http.Request, api string) *http.Response {
	var body string
	switch api {
	case "responses":
		body = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"
	case "chat":
		body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	default:
		body = `{"content":[{"type":"text","text":"ok"}]}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestDirectAPIEndpointWithVersionedAndPastedBases(t *testing.T) {
	cases := []struct {
		name string
		api  string
		suf  string
		want string
	}{
		{"messages versioned", "messages", "/prefix/v1/", "/prefix/v1/messages"},
		{"messages full v1", "messages", "/prefix/v1/messages", "/prefix/v1/messages"},
		{"messages prefix named messages", "messages", "/messages", "/messages/v1/messages"},
		{"messages prefix named messages v1", "messages", "/messages/v1", "/messages/v1/messages"},
		{"messages prefix named messages full", "messages", "/messages/v1/messages", "/messages/v1/messages"},
		{"messages double v1 full", "messages", "/v1/v1/messages", "/v1/v1/messages"},
		{"responses versioned", "responses", "/prefix/v1/", "/prefix/v1/responses"},
		{"responses full v1", "responses", "/prefix/v1/responses/", "/prefix/v1/responses"},
		{"responses prefix named responses", "responses", "/responses", "/responses/v1/responses"},
		{"responses prefix named responses v1", "responses", "/responses/v1", "/responses/v1/responses"},
		{"responses prefix named responses full", "responses", "/responses/v1/responses", "/responses/v1/responses"},
		{"responses double v1 full", "responses", "/v1/v1/responses", "/v1/v1/responses"},
		{"chat versioned", "chat", "/inference/go/openai/v1", "/inference/go/openai/v1/chat/completions"},
		{"chat full v1", "chat", "/inference/go/openai/v1/chat/completions", "/inference/go/openai/v1/chat/completions"},
		{"chat double v1 full", "chat", "/v1/v1/chat/completions", "/v1/v1/chat/completions"},
		{"chat full slash", "chat", "/zen/go/v1/chat/completions/", "/zen/go/v1/chat/completions"},
		{"chat full bare", "chat", "/chat/completions", "/v1/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				resp := okStreamResponse(r, tc.api)
				defer resp.Body.Close()
				_, _ = io.Copy(w, resp.Body)
			}))
			defer server.Close()
			client := New(Config{API: tc.api, BaseURL: server.URL + tc.suf, APIKey: "key", Model: "model"})
			text, err := client.Complete(context.Background(), "system", "hello", 10)
			if err != nil || text != "ok" || path != tc.want {
				t.Fatalf("text=%q err=%v path=%s want=%s", text, err, path, tc.want)
			}
		})
	}
}

func TestOpenCodeGoDirectAPISessionHeader(t *testing.T) {
	for _, tc := range []struct {
		name     string
		api      string
		base     string
		wantPath string
		wantHdr  bool
	}{
		{"opencode go chat pasted url", "chat", "https://opencode.ai/inference/go/openai/v1/chat/completions", "/inference/go/openai/v1/chat/completions", true},
		{"opencode zen go chat", "chat", "https://opencode.ai/zen/go/v1", "/zen/go/v1/chat/completions", true},
		{"opencode go messages", "messages", "https://opencode.ai/inference/go/anthropic", "/inference/go/anthropic/v1/messages", true},
		{"opencode zen go responses", "responses", "https://sub.opencode.ai/zen/go/v1/responses", "/zen/go/v1/responses", true},
		{"non-go opencode path", "chat", "https://opencode.ai/v1", "/v1/chat/completions", false},
		{"lookalike domain", "chat", "https://notopencode.ai/inference/go/openai/v1", "/inference/go/openai/v1/chat/completions", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths, sessions, uas []string
			client := NewWith(Config{API: tc.api, BaseURL: tc.base, APIKey: "key", Model: "glm-5", Bearer: true}, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.URL.Path)
				sessions = append(sessions, r.Header.Get(modelhttp.OpenCodeSessionHeader))
				uas = append(uas, r.Header.Get("User-Agent"))
				return okStreamResponse(r, tc.api), nil
			}))
			for range 2 {
				text, err := client.Complete(context.Background(), "sys", "ping", 8)
				if err != nil || text != "ok" {
					t.Fatalf("Complete: text=%q err=%v", text, err)
				}
			}
			copied := *client
			if text, err := copied.Complete(context.Background(), "sys", "ping", 8); err != nil || text != "ok" {
				t.Fatalf("copied Complete: text=%q err=%v", text, err)
			}
			if len(paths) != 3 || paths[0] != tc.wantPath || paths[1] != tc.wantPath || paths[2] != tc.wantPath {
				t.Fatalf("paths=%v want %s", paths, tc.wantPath)
			}
			if uas[0] != modelhttp.UserAgent() {
				t.Fatalf("User-Agent=%q want %q", uas[0], modelhttp.UserAgent())
			}
			if tc.wantHdr {
				if sessions[0] == "" || sessions[0] != sessions[1] || sessions[0] != sessions[2] {
					t.Fatalf("expected stable non-empty session header across calls and shallow copies, got %v", sessions)
				}
			} else if sessions[0] != "" || sessions[1] != "" || sessions[2] != "" {
				t.Fatalf("unexpected session header: %v", sessions)
			}
		})
	}
}

func TestOpenCodeGoSessionScopingAndCrossOriginRedirect(t *testing.T) {
	var seen []string
	client := NewWith(Config{API: "chat", BaseURL: "https://opencode.ai/inference/go/openai/v1", APIKey: "key", Model: "glm-5"}, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.URL.Host+"="+r.Header.Get(modelhttp.OpenCodeSessionHeader))
		if r.URL.Host == "opencode.ai" && r.URL.Query().Get("hop") == "1" {
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Header:     http.Header{"Location": {"https://relay.example.com/v1/chat/completions"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		}
		return okStreamResponse(r, "chat"), nil
	}))

	_, err := client.Chat(WithSessionID(context.Background(), "thread-alpha"), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	wantScoped := modelhttp.OpenCodeSessionID("thread-alpha")
	if len(seen) != 1 || seen[0] != "opencode.ai="+wantScoped {
		t.Fatalf("ctx session: got %v, want opencode.ai=%s", seen, wantScoped)
	}

	seen = nil
	_, err = client.Chat(WithSessionID(context.Background(), "thread-alpha"), Request{SessionID: "turn-override", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "opencode.ai=" + modelhttp.OpenCodeSessionID("turn-override"); len(seen) != 1 || seen[0] != want {
		t.Fatalf("request session override: got %v, want %s", seen, want)
	}

	// Cross-origin redirect drops x-opencode-session on the redirected hop.
	redirectClient := NewWith(Config{API: "chat", BaseURL: "https://opencode.ai/inference/go/openai/v1/chat/completions", APIKey: "key", Model: "glm-5"}, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.URL.Host+"="+r.Header.Get(modelhttp.OpenCodeSessionHeader))
		if r.URL.Host == "opencode.ai" {
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Header:     http.Header{"Location": {"https://relay.example.com/v1/chat/completions"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		}
		return okStreamResponse(r, "chat"), nil
	}))
	seen = nil
	if _, err := redirectClient.Complete(context.Background(), "sys", "hi", 8); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !strings.HasPrefix(seen[0], "opencode.ai=") || seen[0] == "opencode.ai=" || seen[1] != "relay.example.com=" {
		t.Fatalf("redirect headers: %v", seen)
	}
}

func TestGateKeyAndChatEffortCacheKeepDeploymentPrefixesApart(t *testing.T) {
	if a, b := gateKeyFor("responses", "https://gw.example/openai", "k"), gateKeyFor("responses", "https://gw.example/openai/responses", "k"); a == b {
		t.Fatalf("distinct Responses prefixes collided: %q", a)
	}
	if a, b := gateKeyFor("responses", "https://gw.example/openai/v1", "k"), gateKeyFor("responses", "https://gw.example/openai/v1/responses", "k"); a != b {
		t.Fatalf("pasted /v1/responses did not share gate with /v1: %q != %q", a, b)
	}
	if a, b := gateKeyFor("chat", "https://gw.example/openai/v1", "k"), gateKeyFor("chat", "https://gw.example/openai/v1/chat/completions", "k"); a != b {
		t.Fatalf("pasted /chat/completions did not share gate with /v1: %q != %q", a, b)
	}
	RememberChatEffortRejection("https://gw.example/openai/responses", "k", "m", "", false)
	if !OmitChatEffort("https://gw.example/openai/responses", "k", "m", "", "medium") {
		t.Fatal("expected rejection on exact chat prefix")
	}
	if OmitChatEffort("https://gw.example/openai", "k", "m", "", "medium") {
		t.Fatal("chat effort cache collided across /openai and /openai/responses")
	}
}
