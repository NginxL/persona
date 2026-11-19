package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestChatProviderUpdateProtocolGuard(t *testing.T) {
	s, mux := launcherTestService(t)
	create := `{"name":"chat","kind":"api","api":"chat","base_url":"https://api.example.com","model":"m","api_key":"synthetic-key"}`
	if code, body := launcherCall(t, mux, "POST", "/api/v1/launchers", create); code != 200 {
		t.Fatalf("create: %d %s", code, body)
	}
	for _, api := range []string{"messages", "responses", ""} {
		body := fmt.Sprintf(`{"kind":"api","api":%q,"base_url":"https://api.example.com","model":"changed"}`, api)
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			res, err := serveHTTP(context.Background(), mux, assistantwire.HTTPRequest{Method: "PUT", Path: "/api/v1/launchers/chat", Body: []byte(body), Header: map[string][]string{"Accept-Language": {string(lang)}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(res.Body, &response); err != nil {
				t.Fatal(err)
			}
			if res.Status != http.StatusBadRequest || response.Error != i18n.T(lang, "assistant-models_launchers.err_chat_protocol_change") {
				t.Fatalf("api=%s lang=%s: %d %s", api, lang, res.Status, res.Body)
			}
		}
	}
	if list := s.storedLaunchers(context.Background()); len(list) != 1 || list[0].API != "chat" || list[0].Model != "m" {
		t.Fatalf("mutated: %+v", list)
	}
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/chat", `{"kind":"api","api":"chat","base_url":"https://api.example.com","model":"changed"}`); code != 200 {
		t.Fatalf("same protocol edit: %d %s", code, body)
	}
	if code, body := launcherCall(t, mux, "POST", "/api/v1/launchers", `{"name":"messages","kind":"api","api":"messages","base_url":"https://api.example.com","model":"m"}`); code != 200 {
		t.Fatalf("messages create: %d %s", code, body)
	}
}

func TestChatProviderTransportGuard(t *testing.T) {
	s, hub, _ := modelService(t)
	mux := http.Handler(s)
	for _, field := range []string{`"proxy":"http://proxy.example.com"`, `"no_proxy":"example.com"`} {
		create := `{"name":"chat","kind":"api","api":"chat","base_url":"https://api.example.com","model":"m",` + field + `}`
		if code, body := launcherCall(t, mux, "POST", "/api/v1/launchers", create); code != 400 || !strings.Contains(body, "Chat Completions") {
			t.Fatalf("create %s: %d %s", field, code, body)
		}
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		body := `{"name":"chat","kind":"api","api":"chat","base_url":"https://api.example.com","model":"m","no_proxy":"example.com"}`
		res, err := serveHTTP(context.Background(), mux, assistantwire.HTTPRequest{Method: "POST", Path: "/api/v1/launchers", Body: []byte(body), Header: map[string][]string{"Accept-Language": {string(lang)}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(res.Body, &response); err != nil {
			t.Fatal(err)
		}
		if res.Status != http.StatusBadRequest || response.Error != i18n.T(lang, "assistant-models_launchers.err_chat_transport") {
			t.Fatalf("lang=%s status=%d body=%s", lang, res.Status, res.Body)
		}
	}
	if code, body := launcherCall(t, mux, "POST", "/api/v1/launchers", `{"name":"chat","kind":"api","api":"chat","base_url":"https://api.example.com","model":"m"}`); code != 200 {
		t.Fatalf("create: %d %s", code, body)
	}
	for _, field := range []string{`"proxy":"http://proxy.example.com"`, `"no_proxy":"example.com"`} {
		update := `{"kind":"api","api":"chat","base_url":"https://api.example.com","model":"m",` + field + `}`
		if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/chat", update); code != 400 {
			t.Fatalf("update %s: %d %s", field, code, body)
		}
	}
	hub.presets = []LauncherPreset{{ID: "team/chat", Kind: "api", API: "chat", BaseURL: "https://api.example.com", Model: "m"}}
	if code, body := launcherCall(t, mux, "POST", "/api/v1/launchers", `{"name":"preset","preset":"team/chat","proxy":"http://proxy.example.com"}`); code != 400 {
		t.Fatalf("preset: %d %s", code, body)
	}
	if code, body := launcherCall(t, mux, "POST", "/api/v1/launchers", `{"name":"messages","kind":"launcher","api":"messages","base_url":"https://api.example.com","model":"m","headers":"X-Example: value","proxy":"http://proxy.example.com","no_proxy":"example.com"}`); code != 200 {
		t.Fatalf("messages: %d %s", code, body)
	}
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/messages", `{"kind":"api","api":"chat","base_url":"https://api.example.com","model":"m"}`); code != 400 {
		t.Fatalf("conversion retained unsupported settings: %d %s", code, body)
	}
	// A previously stored invalid chat configuration cannot silently retain ignored settings.
	sealed, err := s.sealKey("X-Example: value")
	if err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "old", Kind: "api", API: "chat", BaseURL: "https://api.example.com", Model: "m", Proxy: "http://proxy.example.com"}, HeadersSealed: sealed})
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/old", `{"kind":"api","api":"chat","base_url":"https://api.example.com","model":"m"}`); code != 400 {
		t.Fatalf("retained settings: %d %s", code, body)
	}
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/old", `{"kind":"api","api":"chat","base_url":"https://api.example.com","model":"m","headers":"","proxy":""}`); code != 200 {
		t.Fatalf("clear settings: %d %s", code, body)
	}
}

func TestChatProviderRejectsLaunchSettings(t *testing.T) {
	fields := []string{
		`"env":"EXAMPLE=value"`, `"timeout_ms":1000`, `"compact_window":1000`, `"max_context":1000`,
		`"opus_model":"m"`, `"sonnet_model":"m"`, `"haiku_model":"m"`,
		`"fable_model":"m"`, `"subagent_force":true`,
	}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			_, mux := launcherTestService(t)
			create := `{"name":"p","kind":"api","api":"chat","base_url":"https://api.example.com","model":"m",` + field + `}`
			if code, body := launcherCall(t, mux, "POST", "/api/v1/launchers", create); code != 400 {
				t.Fatalf("create: %d %s", code, body)
			}
			create = `{"name":"p","kind":"api","api":"messages","base_url":"https://api.example.com","model":"m",` + field + `}`
			if code, body := launcherCall(t, mux, "POST", "/api/v1/launchers", create); code != 200 {
				t.Fatalf("messages: %d %s", code, body)
			}
			update := `{"kind":"api","api":"chat","base_url":"https://api.example.com","model":"m"}`
			if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/p", update); code != 400 {
				t.Fatalf("retained %s: %d %s", field, code, body)
			}
		})
	}
	s, mux := launcherTestService(t)
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		body := `{"name":"localized","kind":"api","api":"chat","base_url":"https://api.example.com","model":"m","timeout_ms":1000}`
		res, err := serveHTTP(context.Background(), mux, assistantwire.HTTPRequest{Method: "POST", Path: "/api/v1/launchers", Body: []byte(body), Header: map[string][]string{"Accept-Language": {string(lang)}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(res.Body, &response); err != nil {
			t.Fatal(err)
		}
		if res.Status != 400 || response.Error != i18n.T(lang, "assistant-models_launchers.err_chat_launch_settings") {
			t.Fatalf("lang=%s status=%d body=%s", lang, res.Status, res.Body)
		}
	}
	create := `{"name":"p","kind":"api","api":"messages","base_url":"https://api.example.com","model":"m","env":"EXAMPLE=value","timeout_ms":1000,"compact_window":1000,"max_context":1000,"effort":"high","opus_model":"m","sonnet_model":"m","haiku_model":"m","fable_model":"m","subagent_force":true}`
	if code, body := launcherCall(t, mux, "POST", "/api/v1/launchers", create); code != 200 {
		t.Fatalf("create: %d %s", code, body)
	}
	clear := `{"kind":"api","api":"chat","base_url":"https://api.example.com","model":"m","env":"","timeout_ms":0,"compact_window":0,"max_context":0,"effort":"","opus_model":"","sonnet_model":"","haiku_model":"","fable_model":"","subagent_force":false,"models":[{"id":"m","context_window":32000,"efforts":["high"],"default_effort":"high"}]}`
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/p", clear); code != 200 {
		t.Fatalf("clear settings and retain metadata: %d %s", code, body)
	}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "old", Kind: "api", API: "chat", BaseURL: "https://api.example.com", Model: "m", TimeoutMS: 1000}})
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/old", `{"kind":"api","api":"chat","base_url":"https://api.example.com","model":"m"}`); code != 400 {
		t.Fatalf("retained timeout: %d %s", code, body)
	}
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/old", `{"kind":"api","api":"chat","base_url":"https://api.example.com","model":"m","timeout_ms":0}`); code != 200 {
		t.Fatalf("clear timeout: %d %s", code, body)
	}
}

func TestChatProviderAndPurposeAcceptEffort(t *testing.T) {
	s, mux := launcherTestService(t)
	create := `{"name":"chat","kind":"api","api":"chat","base_url":"https://api.example.com","model":"m","effort":"high"}`
	if code, body := launcherCall(t, mux, "POST", "/api/v1/launchers", create); code != 200 {
		t.Fatalf("create with default effort: %d %s", code, body)
	}
	if got := s.storedLaunchers(context.Background())[0].Effort; got != "high" {
		t.Fatalf("saved default effort = %q", got)
	}
	if !slices.ContainsFunc(s.modelProviders(context.Background()), func(p ModelProvider) bool {
		return p.ID == "launcher.chat" && p.ChatEffort
	}) {
		t.Fatal("Chat provider does not advertise purpose effort support")
	}
	update := `{"kind":"api","api":"chat","base_url":"https://api.example.com","model":"m","effort":"max"}`
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/chat", update); code != 200 {
		t.Fatalf("edit default effort: %d %s", code, body)
	}
	if got := s.storedLaunchers(context.Background())[0].Effort; got != "max" {
		t.Fatalf("edited default effort = %q", got)
	}
	if r := modelRequest(s, "PATCH", "kb", `{"revision":0,"provider":"launcher.chat","effort":"low"}`); r.Code != http.StatusOK {
		t.Fatalf("purpose effort: %d %s", r.Code, r.Body.String())
	}
	p := purposeOf(t, s, "kb")
	if p.Effective.Effort != "low" || !slices.Contains(p.Fields, "effort") {
		t.Fatalf("purpose after save: %+v", p)
	}
	c, err := s.apiConfig(context.Background(), p.Effective)
	if err != nil || c.ReasoningEffort != "low" {
		t.Fatalf("resolved Chat effort = %q, %v", c.ReasoningEffort, err)
	}
	if r := modelRequest(s, "PATCH", "kb", `{"revision":1,"effort":null}`); r.Code != http.StatusOK {
		t.Fatalf("clear purpose effort: %d %s", r.Code, r.Body.String())
	}
	if r := modelRequest(s, "PATCH", "kb", `{"revision":2,"effort":"ultra"}`); r.Code != http.StatusBadRequest {
		t.Fatalf("Chat purpose accepted ultra, unlike Responses: %d %s", r.Code, r.Body.String())
	}
	update = `{"kind":"api","api":"chat","base_url":"https://api.example.com","model":"m","effort":""}`
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/chat", update); code != 200 {
		t.Fatalf("clear default effort: %d %s", code, body)
	}
	if got := s.storedLaunchers(context.Background())[0].Effort; got != "" {
		t.Fatalf("cleared default effort = %q", got)
	}
}

func TestChatModelProviderIncludesStoredCapabilities(t *testing.T) {
	s, mux := launcherTestService(t)
	create := `{"name":"chat","kind":"api","api":"chat","base_url":"https://api.example.com","model":"m","effort":"high","models":[{"id":"m","no_effort":true}]}`
	if code, body := launcherCall(t, mux, "POST", "/api/v1/launchers", create); code != 200 {
		t.Fatalf("create: %d %s", code, body)
	}
	providers := s.modelProviders(context.Background())
	i := slices.IndexFunc(providers, func(p ModelProvider) bool { return p.ID == "launcher.chat" })
	if i < 0 || len(providers[i].Models) != 1 || !providers[i].Models[0].NoEffort || providers[i].Effort != "high" {
		t.Fatalf("stored capabilities or default effort missing from model configuration: %+v", providers)
	}
}

func TestChatModelProviderIncludesTierAliases(t *testing.T) {
	s, _ := launcherTestService(t)
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{
		Name: "old", Kind: "api", API: "chat", BaseURL: "https://api.example.com", Model: "sonnet", SonnetModel: "gw/sonnet",
	}})
	providers := s.modelProviders(context.Background())
	i := slices.IndexFunc(providers, func(p ModelProvider) bool { return p.ID == "launcher.old" })
	if i < 0 || providers[i].ModelAliases["sonnet"] != "gw/sonnet" {
		t.Fatalf("Chat tier alias missing from model configuration: %+v", providers)
	}
}

func TestChatProbeRejectsUnsupportedTransport(t *testing.T) {
	for _, operation := range []string{"test", "discover"} {
		for _, tc := range []struct {
			name, api, storedHeaders, inputHeaders, apiOverride string
			kind                                                string
			storedProxy, overrideHeaders, allowed, direct       bool
		}{
			{name: "stored proxy", api: "chat", storedProxy: true},
			{name: "clear headers", api: "chat", storedHeaders: "X-Example: value", overrideHeaders: true, allowed: true},
			{name: "messages launcher headers and proxy", kind: "launcher", api: "messages", storedHeaders: "X-Example: value", storedProxy: true, allowed: true},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				var calls atomic.Int32
				gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method == http.MethodGet {
						_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
					} else {
						_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\n"))
					}
				}))
				defer gw.Close()
				s, _ := launcherTestService(t)
				kind := tc.kind
				if kind == "" {
					kind = "api"
				}
				l := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "stored", Kind: kind, API: tc.api, BaseURL: gw.URL, Model: "m"}}
				if tc.storedHeaders != "" {
					var err error
					l.HeadersSealed, err = s.sealKey(tc.storedHeaders)
					if err != nil {
						t.Fatal(err)
					}
				}
				if tc.storedProxy {
					l.Proxy = gw.URL
				}
				putLaunchers(t, s, l)
				in := probeInput{Name: "stored"}
				if tc.direct {
					in = probeInput{API: tc.api, BaseURL: gw.URL, Model: "m"}
				}
				if tc.overrideHeaders {
					in.Headers = &tc.inputHeaders
				}
				if tc.apiOverride != "" {
					in.API = tc.apiOverride
				}
				ctx := i18n.With(context.Background(), i18n.En)
				var out map[string]any
				if operation == "test" {
					out = s.providerTest(ctx, in)
				} else {
					out = s.providerDiscover(ctx, in)
				}
				want := int32(1)
				if operation == "discover" {
					want = 2 // the model list, then the capability report
				}
				if tc.allowed {
					if calls.Load() != want || out["error"] != nil {
						t.Fatalf("calls=%d out=%v", calls.Load(), out)
					}
				} else {
					key := "assistant-models_launchers.err_chat_transport"
					if tc.apiOverride == "messages" {
						key = "assistant-models_launchers.err_api_transport"
					}
					if calls.Load() != 0 || out["error"] != i18n.T(i18n.En, key) {
						t.Fatalf("blocked: calls=%d out=%v", calls.Load(), out)
					}
				}
			})
		}
	}
}

func TestChatProbeDoesNotRetryStreamError(t *testing.T) {
	calls := 0
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"max_tokens not supported; use max_completion_tokens\"}}\n\n"))
	}))
	defer gw.Close()
	s, _ := launcherTestService(t)
	out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL, Model: "m", API: "chat"})
	if calls != 1 || out["ok"] != false || out["error"] == nil {
		t.Fatalf("calls=%d out=%v", calls, out)
	}
}

func TestChatProbeDoesNotRetryAfterRetryFailure(t *testing.T) {
	calls := 0
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"max_tokens not supported; use max_completion_tokens"}}`))
	}))
	defer gw.Close()
	s, _ := launcherTestService(t)
	out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL, Model: "m", API: "chat"})
	if calls != 2 || out["ok"] != false || out["error"] == nil {
		t.Fatalf("calls=%d out=%v", calls, out)
	}
}

func TestChatProbeRecognizedErrorCodeSuppressesCompatibilityRequest(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantCode, wantHead string
	}{
		{
			name:     "quota code beats stream options wording",
			body:     `{"error":{"code":"budget_exhausted","message":"Unrecognized request argument supplied: stream_options"}}`,
			wantCode: "",
			wantHead: "The gateway returned 400",
		},
		{
			name:     "model code beats token wording",
			body:     `{"error":{"code":"model_not_found","message":"max_tokens is not supported; use max_completion_tokens"}}`,
			wantCode: "model_not_found",
			wantHead: "The gateway is reachable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer gw.Close()
			s, _ := launcherTestService(t)
			out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL, Model: "m", API: "chat"})
			msg, _ := out["error"].(string)
			code, _ := out["code"].(string)
			if calls != 1 || out["ok"] != false || code != tc.wantCode || !strings.Contains(msg, tc.wantHead) {
				t.Fatalf("calls=%d out=%v, want one request with code %q and head %q", calls, out, tc.wantCode, tc.wantHead)
			}
		})
	}
}

func TestChatProbeTokenRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		retry  bool
	}{
		{"supported", 200, "", false},
		{"unsupported", 400, `{"error":{"message":"max_tokens is not supported; use max_completion_tokens"}}`, true},
		{"unsupported alias", 400, `{"error":{"message":"unsupported max_tokens; use max_completion_tokens"}}`, true},
		{"unknown code still allows compatibility", 400, `{"error":{"code":"future_code","message":"max_tokens is not supported; use max_completion_tokens"}}`, true},
		{"generic", 400, `{"error":{"message":"bad request"}}`, false},
		{"missing replacement", 400, `{"error":{"message":"max_tokens is not supported"}}`, false},
		{"missing rejection", 400, `{"error":{"message":"max_tokens and max_completion_tokens"}}`, false},
		{"wrong shape", 400, `{"message":"max_tokens not supported use max_completion_tokens"}`, false},
		{"other status", 429, `{"error":{"message":"max_tokens not supported use max_completion_tokens"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var b map[string]any
				if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
					t.Error(err)
				}
				if calls == 1 && (b["max_tokens"] != float64(256) || b["max_completion_tokens"] != nil) {
					t.Errorf("initial payload: %v", b)
				}
				if calls == 2 && (b["max_completion_tokens"] != float64(256) || b["max_tokens"] != nil) {
					t.Errorf("retry payload: %v", b)
				}
				if calls == 1 && tc.status != 200 {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\n"))
			}))
			defer gw.Close()
			s, _ := launcherTestService(t)
			out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL, Model: "m", API: "chat"})
			wantCalls := 1
			if tc.retry {
				wantCalls = 2
			}
			if calls != wantCalls || out["ok"] != (tc.status == 200 || tc.retry) {
				t.Fatalf("calls=%d out=%v", calls, out)
			}
		})
	}
}
