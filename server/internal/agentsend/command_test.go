package agentsend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	fixtureToken   = "fixture-device-credential"
	fixtureID      = "01234567-89ab-4cde-8fab-0123456789ab"
	fixtureMachine = "node/a"
	fixtureSession = "conversation#1"
	fixturePath    = "/api/v1/sessions/node/a/conversation#1/agent-messages"
	fixtureText    = "A private synthetic message 完整\n"
)

type fixture struct {
	t     *testing.T
	hub   *httptest.Server
	token string
}

func newFixture(t *testing.T, features []string, handler http.HandlerFunc, preflight ...http.HandlerFunc) fixture {
	t.Helper()
	t.Setenv("LC_ALL", "en_US.UTF-8")
	f := fixture{t: t, token: filepath.Join(t.TempDir(), "token")}
	if err := os.WriteFile(f.token, []byte(fixtureToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fixtureToken || r.URL.Query().Has("token") {
			t.Errorf("wrong authentication for %s", r.Method)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/mount/api/v1/ws" {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.CloseNow()
			if err := wsjson.Write(r.Context(), conn, map[string]any{"type": "hello", "features": features}); err != nil {
				t.Error(err)
			}
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/mount/api/v1/") {
			t.Error("request escaped the mount")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/mount"+fixturePath {
			if len(preflight) != 0 {
				preflight[0](w, r)
			} else {
				jsonReply(w, 200, `{"machine_id":"node/a","session_id":"conversation#1"}`)
			}
			return
		}
		handler(w, r)
	}))
	t.Cleanup(f.hub.Close)
	return f
}

func (f fixture) args(command string, flags ...string) []string {
	args := []string{command, "--hub", f.hub.URL + "/mount", "--token-file", f.token, "--json"}
	return append(args, flags...)
}

func (f fixture) sendArgs(flags ...string) []string {
	args := f.args("send", "--request-id", strings.ToUpper(fixtureID), "--message-file", "-")
	args = append(args, flags...)
	return append(args, fixtureMachine, fixtureSession)
}

func (f fixture) run(stdin string, args []string) (string, string, int) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var out, diagnostic bytes.Buffer
	if bin := os.Getenv("PERSONA_TEST_AGENT_SEND_BIN"); bin != "" {
		cmd := exec.CommandContext(ctx, bin, append([]string{"agent"}, args...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(stdin), &out, &diagnostic
		err := cmd.Run()
		var failed *exec.ExitError
		if errors.As(err, &failed) {
			return out.String(), diagnostic.String(), failed.ExitCode()
		}
		if err != nil {
			f.t.Fatal(err)
		}
		return out.String(), diagnostic.String(), 0
	}
	err := Run(ctx, args, strings.NewReader(stdin), &out, &diagnostic)
	code := 0
	if err != nil {
		code = 1
		if !errors.Is(err, ErrUnconfirmed) {
			diagnostic.WriteString(err.Error())
		}
	}
	return out.String(), diagnostic.String(), code
}

func readResult(t *testing.T, out, diagnostic string) result {
	t.Helper()
	if strings.Contains(out+diagnostic, fixtureToken) || strings.Contains(out+diagnostic, strings.TrimSpace(fixtureText)) {
		t.Fatal("the output echoed private input")
	}
	var got result
	if err := json.Unmarshal([]byte(out), &got); err != nil || !uuid.MatchString(got.ID) {
		t.Fatalf("expected exactly one outcome object: %v %q %q", err, out, diagnostic)
	}
	return got
}

func jsonReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func receiptReply(w http.ResponseWriter, id, kind, path, digest, receiptState string, status int, body string) {
	value := map[string]any{"id": id, "kind": kind, "target": map[string]string{"path": path, "query": ""},
		"digest": digest, "state": receiptState}
	if status != 0 {
		value["result"] = map[string]any{"status": status, "body": []byte(body)}
	}
	data, _ := json.Marshal(value)
	jsonReply(w, 200, string(data))
}

func expectedDigest(text string) string {
	return devicewire.ClientOpDigest("message", devicewire.ClientOpTarget(fixturePath, ""), true,
		map[string]any{"text": text, "client_msg_id": fixtureID, "hold_when_away": false})
}

func checkPost(t *testing.T, r *http.Request, hold bool) {
	t.Helper()
	var read struct {
		Text string
		Hold *bool `json:"hold_when_away"`
	}
	if r.Method != http.MethodPost ||
		r.URL.EscapedPath() != "/mount/api/v1/sessions/node%2Fa/conversation%231/agent-messages" ||
		r.Header.Get("X-Persona-Op-Id") != fixtureID ||
		(r.Header.Get("X-Persona-Features") == holdFeature) != hold ||
		json.NewDecoder(r.Body).Decode(&read) != nil || read.Text != fixtureText || read.Hold == nil || *read.Hold != hold {
		t.Errorf("incorrect message request: method=%s path=%s", r.Method, r.URL.EscapedPath())
	}
}

func TestAgentSendSeparatesAuthoritativeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		state  state
		code   int
		hold   bool
	}{
		{"delivered", 200, `{"ok":true}`, delivered, 0, false},
		{"older delivered", 200, `{}`, delivered, 0, false},
		{"agent queue", 200, `{"ok":true,"waits_on_computer":true}`, queued, 0, false},
		{"offline hold", 202, `{"delivered":"later","undelivered":{"text":"A private synthetic message 完整"}}`, held, 0, true},
		{"pending", 202, `{"state":"pending"}`, pending, 1, false},
		{"refusal", 409, `{"code":"launcher_missing","delivered":"none"}`, refused, 1, false},
		{"ID conflict", 409, `{"code":"op_id_reused"}`, conflict, 1, false},
		{"unconfirmed", 404, `{"error":"not found"}`, unknown, 1, false},
		{"sparse server failure", 503, `{}`, unknown, 1, false},
		{"invalid success", 200, `null`, unknown, 1, false},
		{"unrelated success", 200, `{"notice":"unrelated"}`, unknown, 1, false},
		{"contradictory success", 200, `{"ok":true,"state":"pending"}`, unknown, 1, false},
		{"failure after dispatch", 502, `{"delivered":"yes"}`, unknown, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int32
			f := newFixture(t, []string{"client_ops", proto.FeatureAgentTerminalSend, holdFeature}, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					jsonReply(w, 404, `{"error":"no such operation"}`)
					return
				}
				posts.Add(1)
				checkPost(t, r, tc.hold)
				jsonReply(w, tc.status, tc.body)
			})
			var flags []string
			if tc.hold {
				flags = append(flags, "--hold")
			}
			out, diagnostic, code := f.run(fixtureText, f.sendArgs(flags...))
			got := readResult(t, out, diagnostic)
			if got.State != tc.state || got.HTTPStatus != tc.status || code != tc.code || posts.Load() != 1 ||
				!strings.Contains(diagnostic, fixtureID) {
				t.Fatalf("result=%+v exit=%d posts=%d", got, code, posts.Load())
			}
		})
	}
}

func TestAgentSendRecoversOnlyTheSameOperationAfterResponseLoss(t *testing.T) {
	for _, scenario := range []string{"reset", "timeout", "different input", "different target", "different operation"} {
		t.Run(scenario, func(t *testing.T) {
			var posts, reads atomic.Int32
			accepted := make(chan struct{})
			f := newFixture(t, []string{"client_ops", proto.FeatureAgentTerminalSend}, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					checkPost(t, r, false)
					if posts.Add(1) == 1 {
						close(accepted)
					}
					if scenario == "timeout" {
						<-r.Context().Done()
					} else if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
						conn.Close()
					} else {
						t.Error(err)
					}
					return
				}
				reads.Add(1)
				if r.URL.Path != "/mount/api/v1/ops/"+fixtureID {
					t.Error("recovery read used another ID")
				}
				select {
				case <-accepted:
				default:
					t.Error("recovery read preceded send")
				}
				path, kind, digest := fixturePath, "message", expectedDigest(fixtureText)
				switch scenario {
				case "different target":
					path = "/api/v1/sessions/other/s/agent-messages"
				case "different operation":
					kind = "start"
				case "different input":
					digest = expectedDigest("unrelated input")
				}
				receiptReply(w, fixtureID, kind, path, digest, "done", 200, `{"ok":true}`)
			})
			out, diagnostic, code := f.run(fixtureText, f.sendArgs("--timeout", "800ms"))
			got := readResult(t, out, diagnostic)
			want, exit := unknown, 1
			if scenario == "reset" || scenario == "timeout" {
				want, exit = delivered, 0
			}
			if got.State != want || code != exit || posts.Load() != 1 || reads.Load() != 1 {
				t.Fatalf("result=%+v exit=%d posts=%d reads=%d", got, code, posts.Load(), reads.Load())
			}
		})
	}
}

func TestAgentReceiptWaitReadsOnlyAndKeepsAConfirmedHold(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, []string{"client_ops", proto.FeatureAgentTerminalSend}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/mount/api/v1/ops/"+fixtureID {
			t.Error("wait made a write or checked another ID")
		}
		switch calls.Add(1) {
		case 1:
			receiptReply(w, fixtureID, "message", fixturePath, "", "pending", 0, "")
		case 2:
			jsonReply(w, 503, "{}")
		default:
			receiptReply(w, fixtureID, "message", fixturePath, "", "done", 202, `{"delivered":"later"}`)
		}
	})
	out, diagnostic, code := f.run("", f.args("receipt", "--wait", fixtureID))
	if got := readResult(t, out, diagnostic); got.State != held || code != 0 || calls.Load() != 3 {
		t.Fatalf("result=%+v exit=%d calls=%d", got, code, calls.Load())
	}
}

func TestAgentReceiptReportsUnavailableUnknownAndExpiredWait(t *testing.T) {
	for _, scenario := range []string{"missing", "wrong ID", "unknown", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t, []string{"client_ops", proto.FeatureAgentTerminalSend}, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("receipt attempted a write")
				}
				switch scenario {
				case "missing":
					jsonReply(w, 404, "{}")
				case "wrong ID":
					receiptReply(w, "00000000-0000-4000-8000-000000000001", "message", fixturePath, "",
						"done", 200, "{}")
				case "unknown":
					receiptReply(w, fixtureID, "message", fixturePath, "", "unknown", 502, `{"delivered":"yes"}`)
				case "timeout":
					receiptReply(w, fixtureID, "message", fixturePath, "", "pending", 0, "")
				}
			})
			out, diagnostic, code := f.run("", f.args("receipt", "--wait", "--timeout", "750ms", fixtureID))
			got := readResult(t, out, diagnostic)
			want, reason := unavailable, ""
			switch scenario {
			case "unknown":
				want = unknown
			case "timeout":
				want, reason = pending, "timeout"
			}
			if got.State != want || got.Reason != reason || code != 1 {
				t.Fatalf("result=%+v exit=%d", got, code)
			}
		})
	}
}

func TestAgentSendRequiresCapabilitiesAndPrintsIDBeforePosting(t *testing.T) {
	for _, scenario := range []string{"no receipts", "no Agent guard", "no hold", "broken diagnostics"} {
		t.Run(scenario, func(t *testing.T) {
			var features, flags []string
			switch scenario {
			case "no Agent guard":
				features = []string{"client_ops"}
			case "no hold":
				features, flags = []string{"client_ops", proto.FeatureAgentTerminalSend}, []string{"--hold"}
			case "broken diagnostics":
				features = []string{"client_ops", proto.FeatureAgentTerminalSend}
			}
			f := newFixture(t, features, func(w http.ResponseWriter, r *http.Request) {
				t.Error("POST attempted without a usable receipt setup")
				w.WriteHeader(500)
			})
			if scenario == "broken diagnostics" {
				err := Run(context.Background(), f.sendArgs(), strings.NewReader(fixtureText), io.Discard, brokenOutput{})
				if err == nil {
					t.Fatal("ignored broken diagnostics")
				}
				return
			}
			out, diagnostic, code := f.run(fixtureText, f.sendArgs(flags...))
			if out != "" || diagnostic == "" || code == 0 {
				t.Fatalf("out=%q diagnostic=%q exit=%d", out, diagnostic, code)
			}
		})
	}
}

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAgentSendRedirectNeverCarriesTokenOrPost(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("redirect target received a request")
		w.WriteHeader(500)
	}))
	defer target.Close()
	f := newFixture(t, []string{"client_ops", proto.FeatureAgentTerminalSend}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			jsonReply(w, 404, "{}")
			return
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	out, diagnostic, code := f.run(fixtureText, f.sendArgs())
	if got := readResult(t, out, diagnostic); got.State != unknown || code != 1 {
		t.Fatalf("result=%+v exit=%d", got, code)
	}
}

func TestReadAgentMessagePreservesTextAndBoundsInput(t *testing.T) {
	for _, value := range []string{"", " \t\n", "\xff", strings.Repeat("a", maxMessageBytes+1)} {
		if _, err := readMessage("-", strings.NewReader(value)); err == nil {
			t.Fatal("accepted empty, invalid or oversized text")
		}
	}
	path := filepath.Join(t.TempDir(), "message.txt")
	if err := os.WriteFile(path, []byte(fixtureText), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readMessage(path, nil); err != nil || got != fixtureText {
		t.Fatalf("read changed user text: %v", err)
	}
}

func TestAgentSendGeneratesOneExposedRequestID(t *testing.T) {
	ids := make(chan string, 2)
	f := newFixture(t, []string{"client_ops", proto.FeatureAgentTerminalSend}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Error("unexpected request while sending")
		}
		ids <- r.Header.Get("X-Persona-Op-Id")
		jsonReply(w, 200, `{"ok":true}`)
	})
	args := f.args("send", "--message-file", "-", fixtureMachine, fixtureSession)
	out, diagnostic, code := f.run(fixtureText, args)
	got := readResult(t, out, diagnostic)
	if code != 0 || got.State != delivered || len(ids) != 1 || <-ids != got.ID ||
		strings.Count(diagnostic, got.ID) != 1 {
		t.Fatalf("request ID was not exposed consistently: %+v, exit=%d", got, code)
	}
}

func TestAgentSendStopsBeforePostingToAnUnprovenTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		hold bool
		code int
		body string
	}{
		{"Persona", false, 404, `{"code":"agent_target_required"}`},
		{"Persona with hold", true, 404, `{"code":"agent_target_required"}`},
		{"other session", false, 200, `{"machine_id":"node/a","session_id":"other"}`},
		{"non-full actor", true, 403, `{"code":"full_device_required"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, []string{"client_ops", proto.FeatureAgentTerminalSend, holdFeature},
				func(w http.ResponseWriter, r *http.Request) {
					t.Error("request passed the preflight without an exact Agent")
					w.WriteHeader(500)
				}, func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-Persona-Op-Id") != "" {
						t.Error("preflight used a write receipt")
					}
					jsonReply(w, tc.code, tc.body)
				})
			var flags []string
			if tc.hold {
				flags = append(flags, "--hold")
			}
			out, diagnostic, code := f.run(fixtureText, f.sendArgs(flags...))
			if out != "" || code == 0 || strings.Contains(diagnostic, fixtureID) {
				t.Fatalf("out=%q diagnostic=%q exit=%d", out, diagnostic, code)
			}
		})
	}
}
