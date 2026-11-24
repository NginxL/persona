package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestParseDuration(t *testing.T) {
	if d, err := parseDuration("20m"); err != nil || d.Minutes() != 20 {
		t.Fatalf("20m: %v %v", d, err)
	}
	for _, bad := range []string{"0", "-5m", "20", "soon"} {
		if _, err := parseDuration(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestHubConfig(t *testing.T) {
	cases := []struct {
		name, file, env, addr, wantErr string
	}{
		{"missing", "", "", "", "run pushrec on the hub's machine"},
		{"bad json", "{", "", "", "not valid JSON"},
		{"no token", `{"listen": ":18660"}`, "", "", "has no token"},
		{"no listen: the hub's default", `{"token": "secret-token"}`, "", "127.0.0.1:18660", ""},
		{"bad listen", `{"listen": "18660", "token": "secret-token"}`, "", "", "no usable listen address"},
		{"port only", `{"listen": ":18660", "token": "secret-token"}`, "", "127.0.0.1:18660", ""},
		{"ipv4 wildcard", `{"listen": "0.0.0.0:18661", "token": "secret-token"}`, "", "127.0.0.1:18661", ""},
		{"ipv6 wildcard", `{"listen": "[::]:18661", "token": "secret-token"}`, "", "127.0.0.1:18661", ""},
		{"one address", `{"listen": "192.0.2.7:18661", "token": "secret-token"}`, "", "192.0.2.7:18661", ""},
		{"ipv6 address", `{"listen": "[::1]:18661", "token": "secret-token"}`, "", "[::1]:18661", ""},
		{"the environment over hub.json", `{"listen": ":18660", "token": "secret-token"}`, ":18662", "127.0.0.1:18662", ""},
		{"bad listen in the environment", `{"token": "secret-token"}`, "18662", "", "PERSONA_LISTEN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PERSONA_LISTEN", "")
			t.Setenv("PERSONA_LISTEN", c.env)
			dir := t.TempDir()
			if c.file != "" {
				if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(c.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			addr, token, err := hubConfig(dir)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				if strings.Contains(err.Error(), "secret-token") {
					t.Fatalf("the error quotes the token: %v", err)
				}
				return
			}
			if err != nil || addr != c.addr || token != "secret-token" {
				t.Fatalf("got %q %q %v", addr, token, err)
			}
		})
	}
}

// hub serves a WebSocket that checks the token, sends msgs, then closes if hangUp or else waits for the client.
func hub(t *testing.T, msgs []string, hangUp bool) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for _, m := range msgs {
			c.Write(r.Context(), websocket.MessageText, []byte(m))
		}
		if hangUp {
			c.Close(websocket.StatusGoingAway, "restarting")
			return
		}
		c.Read(r.Context()) // until the client goes
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestCaptureUntilTimeUp(t *testing.T) {
	url := hub(t, []string{`{"a":1}`, `{"b":2}`}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	n, _, err := capture(ctx, url, "secret-token", &out)
	if err != nil || n != 2 || strings.Count(out.String(), "\n") != 2 || !strings.Contains(out.String(), `"msg":{"a":1}`) {
		t.Fatalf("n=%d err=%v out=%q", n, err, out.String())
	}
}

func TestCaptureHubGoesAway(t *testing.T) {
	url := hub(t, []string{`{"a":1}`, `{"b":2}`}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	n, _, err := capture(ctx, url, "secret-token", &out)
	var cut *cutShort
	if !errors.As(err, &cut) || !strings.Contains(err.Error(), "restarting") {
		t.Fatalf("err = %v, want the hub's close", err)
	}
	if n != 2 || strings.Count(out.String(), "\n") != 2 {
		t.Fatalf("kept %d messages: %q", n, out.String())
	}
}

func TestCaptureWrongToken(t *testing.T) {
	url := hub(t, nil, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, err := capture(ctx, url, "stale-token", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "refused the token (HTTP 401)") || strings.Contains(err.Error(), "stale-token") {
		t.Fatalf("err = %v", err)
	}
}
