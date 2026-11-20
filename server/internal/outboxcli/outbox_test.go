package outboxcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/devicewire"
)

const testToken = "device-fixture-secret"
const testPage = `{
	"machine_id":"mac",
	"computer":"Studio",
	"messages":[{
		"id":"m1",
		"session_id":"s1",
		"state":"failed",
		"sent_at":"2026-01-02T03:04:05Z",
		"held_at":"2026-01-02T03:04:06Z",
		"failure":"unclear",
		"detail":"Might not have arrived.\u001b[2J",
		"can_retry":true,
		"can_discard":true
	}],
	"next_cursor":"next-token"
}`

func TestOutboxListFiltersOutputAndTokenFile(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv(tokenEnv, "must-not-use-environment")
	tokenFile := filepath.Join(t.TempDir(), "device-token")
	if err := os.WriteFile(tokenFile, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/hub%20prefix/api/v1/machines/my%2Fmac/held-messages" ||
			r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("Accept-Language") != "en" {
			t.Errorf("unexpected request %s %s, auth present %v", r.Method, r.URL, r.Header.Get("Authorization") != "")
		}
		q := r.URL.Query()
		if q.Get("state") != "failed" || q.Get("session") != "s?1" || q.Get("after") != "opaque" || q.Get("limit") != "7" {
			t.Errorf("query %v", q)
		}
		_, _ = w.Write([]byte(testPage))
	}))
	defer hub.Close()

	args := []string{"list", "--hub", hub.URL + "/hub%20prefix", "--token-file", tokenFile,
		"--session", "s?1", "--state", "failed", "--limit", "7", "--after", "opaque"}
	var out bytes.Buffer
	if err := Run(context.Background(), append(args, "my/mac"), &out); err != nil {
		t.Fatal(err)
	}
	report := out.String()
	for _, want := range []string{"Held messages on Studio (mac)", "Failed · Session s1 · Message m1",
		"Might not have arrived.<ESC>[2J", "retry or discard", "--after next-token"} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q: %s", want, report)
		}
	}
	if strings.ContainsAny(report, "\x1b") || strings.Contains(report, testToken) {
		t.Fatal("untrusted terminal control or token in output")
	}
	out.Reset()
	if err := Run(context.Background(), append(args, "--json", "my/mac"), &out); err != nil {
		t.Fatal(err)
	}
	var page devicewire.HeldOutboxPage
	if err := json.Unmarshal(out.Bytes(), &page); err != nil || len(page.Messages) != 1 ||
		page.Messages[0].State != "failed" || page.Messages[0].Failure != "unclear" || page.NextCursor != "next-token" {
		t.Fatalf("JSON page %+v: %v", page, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("%d requests, want one per explicit list", calls.Load())
	}
}

func TestOutboxActionsUseExistingRoutes(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv(tokenEnv, testToken)
	for _, tc := range []struct{ command, method, suffix, output string }{
		{"retry", http.MethodPost, "/retry", "Retry accepted"},
		{"discard", http.MethodDelete, "", "Message removed"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			var calls atomic.Int32
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				path := "/api/v1/sessions/mac/s%2F1/held/m%3F1" + tc.suffix
				if r.Method != tc.method || r.URL.EscapedPath() != path || r.ContentLength != 0 ||
					r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Errorf("unexpected %s %s (length %d)", r.Method, r.URL, r.ContentLength)
				}
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer hub.Close()
			var out bytes.Buffer
			err := Run(context.Background(), []string{tc.command, "--hub", hub.URL, "mac", "s/1", "m?1"}, &out)
			if err != nil || !strings.Contains(out.String(), tc.output) || calls.Load() != 1 {
				t.Fatalf("output %q, %d calls, error %v", &out, calls.Load(), err)
			}
		})
	}
}

func TestOutboxActionFailuresDoNotReplay(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv(tokenEnv, testToken)
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"conflict", 409, `{"error":"Currently sending","code":"conflict"}`, "Currently sending"},
		{"unavailable", 503, `{"error":"Assistant offline","code":"assistant_offline"}`, "List the outbox before trying again"},
		{"truncated confirmation", 200, `{"ok":`, "List the outbox before trying again"},
		{"missing confirmation", 200, `{}`, "List the outbox before trying again"},
		{"lost connection", 0, "", "List the outbox before trying again"},
		{"redirect", 307, "", "HTTP 307"},
		{"redacted error", 403, `{"error":"refused device-fixture-secret\u001b[2J"}`, "[redacted]<ESC>[2J"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.status == 0 {
					conn, _, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				w.Header().Set("Location", "/discarded")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer hub.Close()
			var out bytes.Buffer
			err := Run(context.Background(), []string{"retry", "--hub", hub.URL, "mac", "s1", "m1"}, &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) || out.Len() != 0 || calls.Load() != 1 ||
				strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "\x1b") {
				t.Fatalf("output %q, %d calls, error %v", &out, calls.Load(), err)
			}
		})
	}
}

func TestOutboxValidatesBeforeConnecting(t *testing.T) {
	t.Setenv("PERSONA_LANG", "ja")
	t.Setenv(tokenEnv, "")
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"list", "--help"}, &out); err != nil ||
		!strings.Contains(out.String(), "使用方法") || !strings.Contains(out.String(), "PERSONA_OUTBOX_TOKEN") {
		t.Fatalf("help %q: %v", &out, err)
	}
	for _, args := range [][]string{
		{"list", "--hub", "http://hub.example.test", "mac"},
		{"list", "--hub", "https://user@hub.example.test", "mac"},
		{"list", "--hub", "https://hub.example.test/?secret=value", "mac"},
		{"list", "--limit", "101", "mac"},
		{"list", "--state", "done", "mac"},
		{"retry", "--hub", "http://127.0.0.1:1", "..", "s1", "m1"},
		{"retry", "mac", "s1"},
		{"discard", "mac", "s1", "m1", "extra"},
	} {
		var usage clitext.UsageError
		if err := Run(context.Background(), args, &out); !errors.As(err, &usage) {
			t.Errorf("arguments %v got %v, want a usage error", args, err)
		}
	}
	if err := Run(context.Background(), []string{"list", "--hub", "https://hub.example.test", "mac"}, &out); err == nil ||
		!strings.Contains(err.Error(), "PERSONA_OUTBOX_TOKEN") {
		t.Fatalf("missing credentials: %v", err)
	}
}
