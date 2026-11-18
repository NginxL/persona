package proto

import "testing"

func TestMessagesBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://relay.example.net/v1":                           "https://relay.example.net",
		" https://relay.example.net/v1/ ":                        "https://relay.example.net",
		"https://relay.example.net":                              "https://relay.example.net",
		"https://relay.example.net/":                             "https://relay.example.net",
		"https://relay.example.net/team/v1":                      "https://relay.example.net/team",
		"https://relay.example.net/team":                         "https://relay.example.net/team",
		"https://api.deepseek.com/anthropic/v1":                  "https://api.deepseek.com/anthropic",
		"https://api.deepseek.com/anthropic":                     "https://api.deepseek.com/anthropic",
		"http://127.0.0.1:8080/v1":                               "http://127.0.0.1:8080",
		"https://relay.example.net/v1/v1":                        "https://relay.example.net",
		"https://relay.example.net/v1//v1/":                      "https://relay.example.net",
		"https://relay.example.net/v10":                          "https://relay.example.net/v10",
		"https://relay.example.net/api/v1beta":                   "https://relay.example.net/api/v1beta",
		"https://relay.example.net/apiv1":                        "https://relay.example.net/apiv1",
		"https://relay.example.net/v1/team":                      "https://relay.example.net/v1/team",
		"https://relay.example.net/v1/messages":                  "https://relay.example.net",
		"https://relay.example.net/v1//messages/":                "https://relay.example.net",
		"https://relay.example.net/messages":                     "https://relay.example.net/messages",
		"https://relay.example.net/messages/v1":                  "https://relay.example.net/messages",
		"https://relay.example.net/messages/v1/messages":         "https://relay.example.net/messages",
		"https://opencode.ai/inference/go/anthropic/v1/messages": "https://opencode.ai/inference/go/anthropic",
		"https://v1":                             "https://v1", // a host named v1 is not a path
		"https://messages":                       "https://messages",
		"https://relay.example.net/v1?team=a":    "https://relay.example.net/v1?team=a",
		"https://relay.example.net/v1#top":       "https://relay.example.net/v1#top",
		"relay.example.net/v1":                   "relay.example.net/v1",
		"":                                       "",
		"https://relay.example.net/%E4%B8%AD/v1": "https://relay.example.net/%E4%B8%AD",
		"https://relay.example.net:8443/gateway/v1/": "https://relay.example.net:8443/gateway",
	} {
		got := MessagesBaseURL(in)
		if got != want {
			t.Errorf("MessagesBaseURL(%q) = %q, want %q", in, got, want)
		}
		if again := MessagesBaseURL(got); again != got {
			t.Errorf("not idempotent: %q → %q → %q", in, got, again)
		}
	}
}

func TestMessagesBase(t *testing.T) {
	for _, c := range []struct {
		l    ManagedLauncher
		want bool
	}{
		{ManagedLauncher{}, true},
		{ManagedLauncher{API: "messages"}, true},
		{ManagedLauncher{Agent: "claude", API: "messages"}, true},
		{ManagedLauncher{Agent: "omp"}, true}, // gets ANTHROPIC_BASE_URL too (node managedEnv)
		{ManagedLauncher{Kind: "api", API: "messages"}, true},
		{ManagedLauncher{Kind: "api", API: "responses"}, false},
		{ManagedLauncher{Kind: "api", API: "chat"}, false},
		{ManagedLauncher{Agent: "codex", API: "messages"}, false}, // OPENAI_BASE_URL keeps its /v1
		{ManagedLauncher{Agent: "agy"}, false},
	} {
		if got := c.l.MessagesBase(); got != c.want {
			t.Errorf("%+v: MessagesBase() = %v, want %v", c.l, got, c.want)
		}
	}
}

func TestPiAPI(t *testing.T) {
	for _, c := range []struct {
		l    ManagedLauncher
		want string
	}{
		{ManagedLauncher{}, "anthropic-messages"},
		{ManagedLauncher{Agent: "omp"}, "anthropic-messages"},
		{ManagedLauncher{Kind: "api", API: "messages"}, "anthropic-messages"},
		{ManagedLauncher{Kind: "api"}, "anthropic-messages"},
		{ManagedLauncher{Agent: "codex"}, "openai-responses"},
		{ManagedLauncher{Agent: "agy"}, ""},
		{ManagedLauncher{Kind: "api", API: "responses"}, ""},
		{ManagedLauncher{Kind: "api", API: "chat"}, ""},
		{ManagedLauncher{Proxy: "http://127.0.0.1:7890"}, ""}, // pi would not go through it
	} {
		if got := c.l.PiAPI(); got != c.want {
			t.Errorf("%+v: PiAPI() = %q, want %q", c.l, got, c.want)
		}
	}
}

func TestAPIBaseURLAndLaunchBaseURL(t *testing.T) {
	for _, c := range []struct {
		l    ManagedLauncher
		want string
	}{
		{
			ManagedLauncher{Kind: "api", API: "chat", BaseURL: "https://opencode.ai/inference/go/openai/v1/chat/completions"},
			"https://opencode.ai/inference/go/openai/v1",
		},
		{
			ManagedLauncher{Kind: "api", API: "chat", BaseURL: "https://opencode.ai/zen/go/v1/chat/completions/"},
			"https://opencode.ai/zen/go/v1",
		},
		{
			ManagedLauncher{Kind: "api", API: "chat", BaseURL: "https://api.example.com/chat/completions"},
			"https://api.example.com",
		},
		{
			ManagedLauncher{Kind: "api", API: "chat", BaseURL: "https://api.example.com/v1"},
			"https://api.example.com/v1",
		},
		{
			ManagedLauncher{Kind: "api", API: "chat", BaseURL: "https://api.example.com/v1/v1/chat/completions"},
			"https://api.example.com/v1/v1",
		},
		{
			ManagedLauncher{Kind: "api", API: "responses", BaseURL: "https://api.openai.com/v1/responses"},
			"https://api.openai.com/v1",
		},
		{
			ManagedLauncher{Kind: "api", API: "responses", BaseURL: "https://api.openai.com/v1/v1/responses"},
			"https://api.openai.com/v1/v1",
		},
		{
			ManagedLauncher{Kind: "api", API: "responses", BaseURL: "https://api.openai.com/responses/v1/responses"},
			"https://api.openai.com/responses/v1",
		},
		{
			ManagedLauncher{Kind: "api", API: "responses", BaseURL: "https://gw.example/responses/"},
			"https://gw.example/responses",
		},
		{
			ManagedLauncher{Agent: "codex", BaseURL: "https://gw.example/responses"},
			"https://gw.example/responses",
		},
		{
			ManagedLauncher{Agent: "codex", BaseURL: "https://api.openai.com/v1/responses/"},
			"https://api.openai.com/v1",
		},
		{
			ManagedLauncher{Kind: "api", API: "messages", BaseURL: "https://opencode.ai/inference/go/anthropic/v1/messages"},
			"https://opencode.ai/inference/go/anthropic",
		},
	} {
		got := c.l.LaunchBaseURL()
		if got != c.want {
			t.Errorf("%+v: LaunchBaseURL() = %q, want %q", c.l, got, c.want)
		}
		if again := (ManagedLauncher{Kind: c.l.Kind, Agent: c.l.Agent, API: c.l.API, BaseURL: got}).LaunchBaseURL(); again != got {
			t.Errorf("not idempotent: %q → %q", got, again)
		}
	}
}
