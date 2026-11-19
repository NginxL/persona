package herdrcore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/go-winio"
)

func TestSocketPathIsPipe(t *testing.T) {
	t.Setenv("USERNAME", "alice")
	t.Setenv("HC_SESSION", "")
	t.Setenv("HC_SOCKET", `C:\Users\alice\.local\state\herdr-core\default\default.sock`) // a unix path: not used here
	if got, want := SocketPath(), `\\.\pipe\herdr-core-alice-default.sock`; got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
	t.Setenv("HC_SESSION", "iso")
	if got, want := SocketPath(), `\\.\pipe\herdr-core-alice-iso.sock`; got != want {
		t.Errorf("HC_SESSION: SocketPath() = %q, want %q", got, want)
	}
	t.Setenv("HC_SOCKET", `\\.\pipe\mine`)
	if got := SocketPath(); got != `\\.\pipe\mine` {
		t.Errorf("pipe HC_SOCKET: SocketPath() = %q", got)
	}
}

// No pipe: not running at once, without the unix socket file path the node used to check.
func TestMissingPipeIsNotRunning(t *testing.T) {
	c := &Client{Socket: fmt.Sprintf(`\\.\pipe\herdr-core-persona-test-missing-%d`, os.Getpid())}
	_, err := c.List(context.Background())
	if !errors.Is(err, ErrNoSocket) {
		t.Fatalf("List = %v, want ErrNoSocket", err)
	}
	if strings.Contains(err.Error(), "default.sock") {
		t.Errorf("error still names a socket file: %v", err)
	}
}

// A running core's pipe answers a call.
func TestCallOverPipe(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\herdr-core-persona-test-%d`, os.Getpid())
	ln, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadBytes('\n')
				if !strings.Contains(string(line), `"terminal.list"`) {
					return
				}
				_, _ = conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"terminals":[]}}` + "\n"))
			}()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := (&Client{Socket: name}).List(ctx); err != nil {
		t.Fatalf("List over %s: %v", name, err)
	}
}
