package ai

import (
	"context"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/modelhttp"
)

type sourceTransport func(*http.Request) (*http.Response, error)

func (f sourceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDirectModelSourceAllProtocolsAndDestinations(t *testing.T) {
	for _, api := range []string{"messages", "responses", "chat"} {
		for _, tc := range []struct{ hosts, base, role string }{
			{"", "https://gw.example.com", ""},
			{"*.example.com", "https://gw.example.com", "background"},
			{"*.example.com", "https://api.anthropic.com", ""},
			{"*.example.com", "https://api.openai.com", ""},
		} {
			t.Run(api+"/"+tc.hosts+"/"+tc.base, func(t *testing.T) {
				t.Setenv(modelhttp.SourceHostsEnv, tc.hosts)
				calls := 0
				c := NewWith(Config{API: api, BaseURL: tc.base, APIKey: "example-key", Model: "model", Headers: map[string]string{"X-Team": "blue", "x-persona-role": "stale", "X-Persona-Todo": "todo_old"}}, sourceTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Header.Get(modelhttp.RoleHeader) != tc.role || r.Header.Get(modelhttp.TodoHeader) != "" || r.Header.Get("X-Team") != "blue" {
						t.Fatalf("headers=%v", r.Header)
					}
					return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
				}))
				_, _ = c.Complete(context.Background(), "system", "example", 8)
				if calls != 1 {
					t.Fatalf("calls=%d", calls)
				}
			})
		}
	}
}

func TestSourceHeadersDoNotFollowCrossOriginRedirect(t *testing.T) {
	t.Setenv(modelhttp.SourceHostsEnv, "*.example.com")
	previous, _ := http.NewRequest("POST", "https://gw.example.com/v1/messages", nil)
	next, _ := http.NewRequest("GET", "https://api.anthropic.com/", nil)
	modelhttp.Source{Role: "routine", Todo: "todo_1", Routine: "routine_1"}.Apply(next.Header)
	if err := checkRedirect(next, []*http.Request{previous}); err != nil {
		t.Fatal(err)
	}
	for name := range next.Header {
		if modelhttp.SourceHeader(name) {
			t.Fatal("source leaked across origins")
		}
	}
}
