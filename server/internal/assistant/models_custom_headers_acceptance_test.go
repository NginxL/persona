package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	issue11075Header       = "X-Custom-Header"
	issue11075Value        = "example-value"
	issue11075RouteHeader  = "X-Route"
	issue11075RouteValue   = "route-secret-value"
	issue11075HeaderSecret = "header-secret-value"
)

var issue11075APIs = []string{"messages", "responses", "chat"}

// issue11075Request is one synthetic gateway request. The recorder keeps the
// wire evidence independent of the provider response the manager returns.
type issue11075Request struct {
	Method string
	Path   string
	Header http.Header
}

type issue11075Recorder struct {
	mu       sync.Mutex
	requests []issue11075Request
}

func (r *issue11075Recorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, issue11075Request{Method: req.Method, Path: req.URL.Path, Header: req.Header.Clone()})
}

func (r *issue11075Recorder) all() []issue11075Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]issue11075Request(nil), r.requests...)
}

func issue11075Reply(w http.ResponseWriter, api string) {
	switch api {
	case "responses":
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
	case "chat":
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	default:
		_, _ = fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}
}

func issue11075GatewayReply(w http.ResponseWriter, r *http.Request, api string) {
	if r.Method == http.MethodGet {
		if strings.HasSuffix(r.URL.Path, "/model-capabilities") {
			_, _ = fmt.Fprint(w, `{"data":[]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
		return
	}
	issue11075Reply(w, api)
}

func issue11075StoreDirect(t *testing.T, s *Service, api, baseURL, headers, via string) {
	t.Helper()
	key, err := s.sealKey(fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	l := storedLauncher{ManagedLauncher: proto.ManagedLauncher{
		Name: "direct", Kind: "api", API: api, BaseURL: baseURL, Model: "m",
	}, KeySealed: key, ViaMachine: via}
	if headers != "" {
		l.HeadersSealed, err = s.sealKey(headers)
		if err != nil {
			t.Fatal(err)
		}
	}
	putLaunchers(t, s, l)
}

// issue11075Call runs one of the three effective manager paths against the
// same stored provider. Its string result is also what redaction tests inspect.
func issue11075Call(t *testing.T, s *Service, operation string) (bool, string) {
	t.Helper()
	ctx := i18n.With(context.Background(), i18n.En)
	switch operation {
	case "test":
		out := s.providerTest(ctx, probeInput{Name: "direct"})
		return out["ok"] == true, jsonText(out)
	case "discover":
		out := s.providerDiscover(ctx, probeInput{Name: "direct"})
		return out["error"] == nil, jsonText(out)
	case "inference":
		cfg, err := s.apiConfig(ctx, ModelBinding{Provider: "launcher.direct"})
		if err != nil {
			return false, fmt.Sprintf("resolve: %v", err)
		}
		answer, err := s.aiClient(cfg).Complete(ctx, "system", "hello", 8)
		return err == nil && answer == "ok", fmt.Sprintf("answer=%q err=%v", answer, err)
	default:
		t.Fatalf("unknown operation %q", operation)
		return false, ""
	}
}

func TestIssue11075DirectAPIHeadersSaveAndPrivacy(t *testing.T) {
	for _, api := range issue11075APIs {
		t.Run(api, func(t *testing.T) {
			s, mux := launcherTestService(t)
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				issue11075GatewayReply(w, r, api)
			}))
			defer gw.Close()

			base := map[string]any{
				"name": "direct", "kind": "api", "api": api, "base_url": gw.URL, "model": "m", "api_key": fakeKey,
			}
			save := func(method, path string, extra map[string]any) (int, string) {
				body := map[string]any{}
				for k, v := range base {
					body[k] = v
				}
				for k, v := range extra {
					body[k] = v
				}
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				return launcherCall(t, mux, method, path, string(raw))
			}
			assertRead := func(wantHeaders bool, secrets ...string) {
				t.Helper()
				code, raw := launcherCall(t, mux, http.MethodGet, "/api/v1/launchers", "")
				if code != http.StatusOK {
					t.Fatalf("read: %d %s", code, raw)
				}
				for _, secret := range append(secrets, fakeKey) {
					if strings.Contains(raw, secret) {
						t.Fatalf("read API exposed %q: %s", secret, raw)
					}
				}
				var got struct {
					Launchers []LauncherView `json:"launchers"`
				}
				if err := json.Unmarshal([]byte(raw), &got); err != nil {
					t.Fatal(err)
				}
				if len(got.Launchers) != 1 || got.Launchers[0].HasHeaders != wantHeaders {
					t.Fatalf("read header state: %+v", got.Launchers)
				}
				named := false
				for _, name := range got.Launchers[0].HeaderNames {
					if name == issue11075Header {
						named = true
					}
				}
				if named != wantHeaders {
					t.Fatalf("read header names exposed the wrong state: %+v", got.Launchers[0])
				}
			}

			original := issue11075Header + ": " + issue11075HeaderSecret + "\n" + issue11075RouteHeader + ": " + issue11075RouteValue
			code, raw := save(http.MethodPost, "/api/v1/launchers", map[string]any{"headers": original})
			if code != http.StatusOK {
				t.Fatalf("save custom headers: %d %s", code, raw)
			}
			if strings.Contains(raw, issue11075HeaderSecret) || strings.Contains(raw, issue11075RouteValue) || strings.Contains(raw, fakeKey) {
				t.Fatalf("save response exposed a secret: %s", raw)
			}
			stored := s.storedLaunchers(context.Background())
			if len(stored) != 1 || stored[0].HeadersSealed == "" || stored[0].Headers != "" {
				t.Fatalf("headers were not kept sealed: %+v", stored)
			}
			if opened, err := s.openKey(stored[0].HeadersSealed); err != nil || opened != original {
				t.Fatalf("sealed headers = %q, %v; want %q", opened, err, original)
			}
			if raw := jsonText(stored); strings.Contains(raw, issue11075HeaderSecret) || strings.Contains(raw, issue11075RouteValue) || strings.Contains(raw, fakeKey) {
				t.Fatalf("stored record contains plaintext: %s", raw)
			}
			assertRead(true, issue11075HeaderSecret, issue11075RouteValue)

			replacement := issue11075Header + ": replacement-header-secret"
			delete(base, "api_key") // an edit without the key retains it, as older clients do
			code, raw = save(http.MethodPut, "/api/v1/launchers/direct", map[string]any{"headers": replacement})
			if code != http.StatusOK || strings.Contains(raw, "replacement-header-secret") {
				t.Fatalf("replace headers: %d %s", code, raw)
			}
			stored = s.storedLaunchers(context.Background())
			if opened, err := s.openKey(stored[0].HeadersSealed); err != nil || opened != replacement {
				t.Fatalf("replacement = %q, %v", opened, err)
			}
			if stored[0].KeySealed == "" {
				t.Fatal("header replacement removed the retained API key")
			}
			assertRead(true, issue11075HeaderSecret, issue11075RouteValue, "replacement-header-secret")

			// An omitted editor field retains the replacement; an explicit empty
			// value removes it without ever returning either saved value.
			code, raw = save(http.MethodPut, "/api/v1/launchers/direct", nil)
			if code != http.StatusOK {
				t.Fatalf("retain replacement: %d %s", code, raw)
			}
			if opened, err := s.openKey(s.storedLaunchers(context.Background())[0].HeadersSealed); err != nil || opened != replacement {
				t.Fatalf("omitted headers changed the replacement: %q, %v", opened, err)
			}
			code, raw = save(http.MethodPut, "/api/v1/launchers/direct", map[string]any{"headers": ""})
			if code != http.StatusOK {
				t.Fatalf("remove headers: %d %s", code, raw)
			}
			stored = s.storedLaunchers(context.Background())
			if stored[0].HeadersSealed != "" || stored[0].Headers != "" {
				t.Fatalf("headers were not removed: %+v", stored[0])
			}
			assertRead(false, issue11075HeaderSecret, issue11075RouteValue, "replacement-header-secret")
		})
	}
}

func TestIssue11075DirectAPIHeaderProbes(t *testing.T) {
	for _, api := range issue11075APIs {
		for _, operation := range []string{"test", "discover"} {
			t.Run(api+"/"+operation, func(t *testing.T) {
				var recorder issue11075Recorder
				gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					recorder.record(r)
					if r.Header.Get(issue11075Header) != issue11075Value {
						http.Error(w, "required custom header missing", http.StatusUnauthorized)
						return
					}
					issue11075GatewayReply(w, r, api)
				}))
				defer gw.Close()
				s, _ := launcherTestService(t)
				issue11075StoreDirect(t, s, api, gw.URL, issue11075Header+": "+issue11075Value, "")
				if ok, details := issue11075Call(t, s, operation); !ok {
					t.Fatalf("header-dependent %s failed: %s", operation, details)
				}
				requests := recorder.all()
				if len(requests) == 0 {
					t.Fatal("the gateway received no request")
				}
				for _, req := range requests {
					if got := req.Header.Get(issue11075Header); got != issue11075Value {
						t.Errorf("%s %s header = %q, want %q", req.Method, req.Path, got, issue11075Value)
					}
				}
			})
		}
	}
}

func TestIssue11075DirectAPIHeaderInference(t *testing.T) {
	for _, api := range issue11075APIs {
		t.Run(api, func(t *testing.T) {
			var recorder issue11075Recorder
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorder.record(r)
				if r.Header.Get(issue11075Header) != issue11075Value {
					http.Error(w, "required custom header missing", http.StatusUnauthorized)
					return
				}
				issue11075Reply(w, api)
			}))
			defer gw.Close()
			s, _ := launcherTestService(t)
			issue11075StoreDirect(t, s, api, gw.URL, issue11075Header+": "+issue11075Value, "")
			if ok, details := issue11075Call(t, s, "inference"); !ok {
				t.Fatalf("header-dependent inference failed: %s", details)
			}
			requests := recorder.all()
			wantPath := map[string]string{"messages": "/v1/messages", "responses": "/v1/responses", "chat": "/v1/chat/completions"}[api]
			if len(requests) != 1 || requests[0].Path != wantPath || requests[0].Header.Get(issue11075Header) != issue11075Value {
				t.Fatalf("inference requests: %+v", requests)
			}
		})
	}
}

func TestIssue11075DirectAPINoHeaderWireDefaults(t *testing.T) {
	for _, api := range issue11075APIs {
		t.Run(api, func(t *testing.T) {
			var recorder issue11075Recorder
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorder.record(r)
				issue11075GatewayReply(w, r, api)
			}))
			defer gw.Close()
			s, _ := launcherTestService(t)
			issue11075StoreDirect(t, s, api, gw.URL, "", "")
			for _, operation := range []string{"test", "discover", "inference"} {
				before := len(recorder.all())
				if ok, details := issue11075Call(t, s, operation); !ok {
					t.Fatalf("no-header %s changed behavior: %s", operation, details)
				}
				requests := recorder.all()[before:]
				if len(requests) == 0 {
					t.Fatalf("no-header %s sent no request", operation)
				}
				for _, req := range requests {
					if got := req.Header.Get(issue11075Header); got != "" {
						t.Errorf("%s gained custom header %q", operation, got)
					}
					if got := req.Header.Get("User-Agent"); got != modelhttp.UserAgent() {
						t.Errorf("%s User-Agent = %q, want %q", operation, got, modelhttp.UserAgent())
					}
					if api == "messages" {
						if got := req.Header.Get("x-api-key"); got != fakeKey || req.Header.Get("Authorization") != "" {
							t.Errorf("%s Messages credentials = x-api-key %q, Authorization %q", operation, got, req.Header.Get("Authorization"))
						}
					} else if got := req.Header.Get("Authorization"); got != "Bearer "+fakeKey || req.Header.Get("x-api-key") != "" {
						t.Errorf("%s OpenAI credentials = Authorization %q, x-api-key %q", operation, got, req.Header.Get("x-api-key"))
					}
					if req.Method == http.MethodPost && req.Header.Get("Content-Type") != "application/json" {
						t.Errorf("%s Content-Type = %q", operation, req.Header.Get("Content-Type"))
					}
				}
			}
		})
	}
}

func TestIssue11075ManagerLauncherFallbackSendsAllHeaders(t *testing.T) {
	for _, withHeaders := range []bool{false, true} {
		t.Run(fmt.Sprint(withHeaders), func(t *testing.T) {
			var recorder issue11075Recorder
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorder.record(r)
				if withHeaders && (r.Header.Get(issue11075Header) != issue11075Value || r.Header.Get(issue11075RouteHeader) != issue11075RouteValue || r.UserAgent() != "gateway-agent/1") {
					http.Error(w, "required launcher headers missing", http.StatusUnauthorized)
					return
				}
				issue11075Reply(w, "messages")
			}))
			defer gw.Close()
			s, _, _ := modelService(t)
			key, err := s.sealKey(fakeKey)
			if err != nil {
				t.Fatal(err)
			}
			l := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "direct", Kind: "launcher", API: "messages", BaseURL: gw.URL, Model: "m"}, KeySealed: key}
			if withHeaders {
				l.HeadersSealed, err = s.sealKey("User-Agent: gateway-agent/1\n" + issue11075Header + ": " + issue11075Value + "\n" + issue11075RouteHeader + ": " + issue11075RouteValue)
				if err != nil {
					t.Fatal(err)
				}
			}
			putLaunchers(t, s, l)
			if ok, details := issue11075Call(t, s, "inference"); !ok {
				t.Fatalf("manager HTTP fallback: %s", details)
			}
			requests := recorder.all()
			if len(requests) != 1 {
				t.Fatalf("fallback requests = %d", len(requests))
			}
			got := requests[0].Header
			if withHeaders {
				if got.Get(issue11075Header) != issue11075Value || got.Get(issue11075RouteHeader) != issue11075RouteValue || got.Get("User-Agent") != "gateway-agent/1" {
					t.Fatalf("fallback headers: %v", got)
				}
			} else if got.Get(issue11075Header) != "" || got.Get(issue11075RouteHeader) != "" || got.Get("User-Agent") != modelhttp.UserAgent() {
				t.Fatalf("no-header fallback defaults changed: %v", got)
			}
		})
	}
}

func TestIssue11075DirectAPIHeadersSendThrough(t *testing.T) {
	for _, api := range issue11075APIs {
		t.Run(api, func(t *testing.T) {
			var recorder issue11075Recorder
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorder.record(r)
				if r.Header.Get(issue11075Header) != issue11075Value || r.Header.Get("X-Sent-By") != "node" {
					http.Error(w, "Send Through dropped a header", http.StatusUnauthorized)
					return
				}
				issue11075GatewayReply(w, r, api)
			}))
			defer gw.Close()
			s, _, _ := modelService(t)
			devs := &viaDevs{}
			waitFor(t, "the launcher refresh", func() bool {
				s.models.zshrcMu.Lock()
				defer s.models.zshrcMu.Unlock()
				return !s.models.zshrcRefreshing
			})
			s.devs = devs
			t.Cleanup(func() { s.devs = nil })
			issue11075StoreDirect(t, s, api, gw.URL, issue11075Header+": "+issue11075Value, "server")
			for _, operation := range []string{"test", "discover", "inference"} {
				beforeRequests, beforeNodes := len(recorder.all()), devs.calls.Load()
				if ok, details := issue11075Call(t, s, operation); !ok {
					t.Fatalf("Send Through %s: %s", operation, details)
				}
				requests := recorder.all()[beforeRequests:]
				nodeCalls := devs.calls.Load() - beforeNodes
				if len(requests) == 0 || int(nodeCalls) != len(requests) {
					t.Fatalf("%s used %d node calls for %d gateway requests", operation, nodeCalls, len(requests))
				}
				for _, req := range requests {
					if req.Header.Get(issue11075Header) != issue11075Value || req.Header.Get("X-Sent-By") != "node" {
						t.Errorf("%s request headers: %v", operation, req.Header)
					}
				}
			}
		})
	}
}

func TestIssue11075DirectAPIHeaderRedirects(t *testing.T) {
	for _, api := range issue11075APIs {
		for _, operation := range []string{"test", "discover", "inference"} {
			for _, crossOrigin := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/cross=%v", api, operation, crossOrigin), func(t *testing.T) {
					var origin, destination issue11075Recorder
					serveDestination := func(w http.ResponseWriter, r *http.Request) {
						destination.record(r)
						want := issue11075Value
						if crossOrigin {
							want = ""
						}
						if r.Header.Get(issue11075Header) != want || r.Header.Get(issue11075RouteHeader) != want {
							http.Error(w, "redirect custom-header policy violated", http.StatusBadRequest)
							return
						}
						issue11075GatewayReply(w, r, api)
					}
					var elsewhere *httptest.Server
					if crossOrigin {
						elsewhere = httptest.NewServer(http.HandlerFunc(serveDestination))
						defer elsewhere.Close()
					}
					gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if crossOrigin {
							origin.record(r)
							if r.Header.Get(issue11075Header) != issue11075Value || r.Header.Get(issue11075RouteHeader) != issue11075Value {
								http.Error(w, "initial custom headers missing", http.StatusUnauthorized)
								return
							}
							http.Redirect(w, r, elsewhere.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
							return
						}
						if r.URL.Query().Get("redirected") == "1" {
							serveDestination(w, r)
							return
						}
						origin.record(r)
						if r.Header.Get(issue11075Header) != issue11075Value || r.Header.Get(issue11075RouteHeader) != issue11075Value {
							http.Error(w, "initial custom headers missing", http.StatusUnauthorized)
							return
						}
						target := *r.URL
						query := target.Query()
						query.Set("redirected", "1")
						target.RawQuery = query.Encode()
						http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
					}))
					defer gw.Close()
					s, _ := launcherTestService(t)
					headers := issue11075Header + ": " + issue11075Value + "\n" + issue11075RouteHeader + ": " + issue11075Value
					issue11075StoreDirect(t, s, api, gw.URL, headers, "")
					ok, details := issue11075Call(t, s, operation)
					first, last := origin.all(), destination.all()
					if len(first) == 0 {
						t.Fatalf("%s sent no initial request: %s", operation, details)
					}
					for _, req := range first {
						if req.Header.Get(issue11075Header) != issue11075Value || req.Header.Get(issue11075RouteHeader) != issue11075Value {
							t.Errorf("initial request lost custom headers: %v", req.Header)
						}
					}
					if operation != "inference" {
						if ok {
							t.Fatalf("%s unexpectedly followed the redirect: %s", operation, details)
						}
						if len(last) != 0 {
							t.Fatalf("%s followed the redirect and forwarded request headers: %+v", operation, last)
						}
						return
					}
					if !ok {
						t.Fatalf("redirected inference failed: %s", details)
					}
					if len(last) != len(first) {
						t.Fatalf("redirect hops: origin=%d destination=%d", len(first), len(last))
					}
					for _, req := range last {
						want := issue11075Value
						if crossOrigin {
							want = ""
						}
						if req.Header.Get(issue11075Header) != want || req.Header.Get(issue11075RouteHeader) != want {
							t.Errorf("redirected request custom headers = %q / %q, want %q", req.Header.Get(issue11075Header), req.Header.Get(issue11075RouteHeader), want)
						}
					}
				})
			}
		}
	}
}

func TestIssue11075DirectAPIRejectsProtocolManagedHeaders(t *testing.T) {
	for _, api := range issue11075APIs {
		t.Run(api, func(t *testing.T) {
			cases := []string{
				"Host: header-secret-value",
				"Content-Length: header-secret-value",
				"Content-Type: header-secret-value",
				"Authorization: Bearer header-secret-value",
			}
			if api == "messages" {
				cases = append(cases, "X-Api-Key: header-secret-value", "AnThRoPiC-VeRsIoN: header-secret-value")
			}
			for _, headers := range cases {
				t.Run(strings.Split(headers, ":")[0], func(t *testing.T) {
					s, mux := launcherTestService(t)
					body, err := json.Marshal(map[string]any{
						"name": "direct", "kind": "api", "api": api, "base_url": "https://gateway.example.com", "model": "m", "headers": headers,
					})
					if err != nil {
						t.Fatal(err)
					}
					code, raw := launcherCall(t, mux, http.MethodPost, "/api/v1/launchers", string(body))
					if code != http.StatusBadRequest {
						t.Fatalf("reserved or protocol-managed header %q: status %d, body %s", headers, code, raw)
					}
					var response struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal([]byte(raw), &response); err != nil {
						t.Fatal(err)
					}
					legacy := errAPITransport
					if api == "chat" {
						legacy = errChatTransport
					}
					if response.Error == i18n.Localize(legacy, s.lang(context.Background())) {
						t.Fatalf("reserved header was rejected only by the blanket transport ban, not by name validation: %s", raw)
					}
					if strings.Contains(raw, issue11075HeaderSecret) {
						t.Fatalf("validation error exposed the header value: %s", raw)
					}
					if stored := s.storedLaunchers(context.Background()); len(stored) != 0 {
						t.Fatalf("rejected header changed storage: %+v", stored)
					}
				})
			}
			in := launcherInput{Name: "direct", Kind: "api", API: api, BaseURL: "https://gateway.example.com", Model: "m", Headers: ptr(issue11075Header + ": " + issue11075Value)}
			if err := in.check(); err != nil {
				t.Fatalf("ordinary custom header rejected: %v", err)
			}
		})
	}
}

func TestIssue11075DirectAPIHeaderErrorsRedacted(t *testing.T) {
	for _, api := range issue11075APIs {
		for _, operation := range []string{"test", "discover", "inference"} {
			t.Run(api+"/"+operation, func(t *testing.T) {
				var recorder issue11075Recorder
				gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					recorder.record(r)
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
						"message": "gateway echoed " + r.Header.Get(issue11075Header) + " and " + fakeKey,
					}})
				}))
				defer gw.Close()
				s, _ := launcherTestService(t)
				issue11075StoreDirect(t, s, api, gw.URL, issue11075Header+": "+issue11075HeaderSecret, "")
				if ok, details := issue11075Call(t, s, operation); ok {
					t.Fatalf("gateway rejection unexpectedly succeeded: %s", details)
				} else {
					if len(recorder.all()) == 0 {
						t.Fatalf("the failing request never reached the gateway: %s", details)
					}
					if strings.Contains(details, issue11075HeaderSecret) || strings.Contains(details, fakeKey) {
						t.Fatalf("error exposed request secrets: %s", details)
					}
				}
			})
		}
	}
}

func TestIssue11075DirectAPIShortHeaderErrorsRedacted(t *testing.T) {
	for _, api := range issue11075APIs {
		for _, operation := range []string{"test", "discover", "inference"} {
			t.Run(api+"/"+operation, func(t *testing.T) {
				gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"message":"echo z9"}}`))
				}))
				defer gw.Close()
				s, _ := launcherTestService(t)
				issue11075StoreDirect(t, s, api, gw.URL, issue11075Header+": z9", "")
				if ok, details := issue11075Call(t, s, operation); ok {
					t.Fatalf("gateway rejection unexpectedly succeeded: %s", details)
				} else if strings.Contains(details, "z9") {
					t.Fatalf("error exposed a short header value: %s", details)
				}
			})
		}
	}
}

func TestIssue11075DirectAPIHeaderLogsRedacted(t *testing.T) {
	var recorder issue11075Recorder
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code": "account_quota_exhausted", "message": "quota response echoed " + r.Header.Get(issue11075Header),
		}})
	}))
	defer gw.Close()
	s, _ := launcherTestService(t)
	issue11075StoreDirect(t, s, "messages", gw.URL, issue11075Header+": "+issue11075HeaderSecret, "")

	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldWriter) })
	if ok, details := issue11075Call(t, s, "inference"); ok {
		t.Fatalf("quota rejection unexpectedly succeeded: %s", details)
	} else if strings.Contains(details, issue11075HeaderSecret) {
		t.Fatalf("inference error exposed the header value: %s", details)
	}
	if len(recorder.all()) == 0 {
		t.Fatal("the quota request never reached the gateway")
	}
	if logs.Len() == 0 {
		t.Fatal("the quota gate did not log its hold")
	}
	if strings.Contains(logs.String(), issue11075HeaderSecret) || strings.Contains(logs.String(), fakeKey) {
		t.Fatalf("logs exposed request secrets: %s", logs.String())
	}
}
