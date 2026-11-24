// Command pushrec records what an app gets from a hub over its WebSocket, for replaying it through the hub's push
// logic (internal/hub/pushreplay_test.go, docs/operations/testing.md "Push replay"). Run it on the hub's machine:
//
//	go run ./scripts/pushrec 20m ~/push.jsonl
//
// It connects like an app with the hub token from ~/.persona/hub.json (never printed), at the address the hub listens
// on (hub.json's listen, PERSONA_LISTEN over it), registers as nothing and opens
// no chat, and writes one line per message: {"t": Unix ms, "msg": the message}. The file holds real sessions (titles,
// last lines): it is created 0600 and stays on that machine.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/identity"
)

const usage = "usage: pushrec <duration> <file>, e.g. pushrec 20m ~/push.jsonl (run it on the hub's machine)"

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	dur, err := parseDuration(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err := record(dur, os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// parseDuration takes how long to record: a Go duration (20m, 1h30m) above zero.
func parseDuration(s string) (time.Duration, error) {
	dur, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration: write it like 20m or 1h30m", s)
	}
	if dur <= 0 {
		return 0, fmt.Errorf("the duration must be above zero, got %s", s)
	}
	return dur, nil
}

// hubConfig reads the hub's address and token from dir's hub.json. Its errors never quote the token.
func hubConfig(dir string) (addr, token string, err error) {
	path := filepath.Join(dir, "hub.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("no %s: run pushrec on the hub's machine, as the user that runs the hub", path)
	}
	if err != nil {
		return "", "", err
	}
	cfg := struct{ Listen, Token string }{Listen: ":18660"} // the hub's default when hub.json has none
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", "", fmt.Errorf("%s is not valid JSON: %v", path, err)
	}
	if cfg.Token == "" {
		return "", "", fmt.Errorf("%s has no token: start the hub once so it writes one", path)
	}
	from := path
	// The hub takes its listen address from the environment over hub.json's (Docker, docs/operations/docker.md): so does pushrec.
	if v := identity.Getenv("PERSONA_LISTEN"); v != "" {
		cfg.Listen, from = v, "PERSONA_LISTEN"
	}
	addr, err = dialAddr(cfg.Listen)
	if err != nil {
		return "", "", fmt.Errorf("%s has no usable listen address (%q): expected host:port, e.g. :18660", from, cfg.Listen)
	}
	return addr, cfg.Token, nil
}

// dialAddr turns the hub's listen address into one to connect to on the same machine: a wildcard host (none,
// 0.0.0.0, ::) listens on loopback too, any other host is dialed as is.
func dialAddr(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return "", fmt.Errorf("no port in %q", listen)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

func record(dur time.Duration, path string) error {
	home, _ := os.UserHomeDir()
	addr, token, err := hubConfig(identity.DataDir(home))
	if err != nil {
		return err
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	start := time.Now()
	n, size, err := capture(ctx, "ws://"+addr+"/api/v1/ws", token, out)
	if cerr := out.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("write %s: %v", path, cerr)
	}
	var cut *cutShort
	switch {
	case errors.As(err, &cut):
		fmt.Printf("recorded %d messages, %d KB\n", n, size/1024)
		return fmt.Errorf("the hub closed the connection after %s of %s (%v): %s has only that part, record again for a full run",
			time.Since(start).Round(time.Second), dur, cut.err, path)
	case err != nil:
		return err
	}
	fmt.Printf("recorded %d messages, %d KB\n", n, size/1024)
	return nil
}

// cutShort is the hub ending the connection before ctx was done: what was read is written.
type cutShort struct{ err error }

func (c *cutShort) Error() string { return c.err.Error() }

// capture connects to the hub's WebSocket at url like an app and writes one line per message to out until ctx is
// done (nil) or the hub goes away (*cutShort). Its errors never quote the request, which holds the token.
func capture(ctx context.Context, url, token string, out io.Writer) (n, size int, err error) {
	conn, resp, err := websocket.Dial(ctx, url,
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return 0, 0, fmt.Errorf("the hub at %s refused the token (HTTP %d): hub.json's token is not the running hub's; restart the hub or fix hub.json", url, resp.StatusCode)
		}
		return 0, 0, fmt.Errorf("connect to the hub at %s failed: check that the hub is running on this machine and listens on that port", url)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 20)
	w := bufio.NewWriter(out)
	var readErr error
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil { // the hub went away before the time was up
				readErr = &cutShort{err}
			}
			break
		}
		line, _ := json.Marshal(struct {
			T   int64           `json:"t"`
			Msg json.RawMessage `json:"msg"`
		}{time.Now().UnixMilli(), data})
		w.Write(line)
		w.WriteByte('\n')
		n, size = n+1, size+len(data)
	}
	if err := w.Flush(); err != nil {
		return n, size, fmt.Errorf("write the recording: %v", err)
	}
	return n, size, readErr
}
