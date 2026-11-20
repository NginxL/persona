//go:build !windows

package herdr

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPaneCloseIfDoesNotReconnectAfterFinalCheck(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "herdr-close-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "api.sock")
	current, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := current.Accept()
		accepted <- conn
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	client := &Client{Socket: path}
	var replacement *net.UnixListener
	attempted, err := client.PaneCloseIf(ctx, "pane", func() bool {
		select {
		case conn := <-accepted:
			if conn == nil {
				t.Fatal("original connection never opened")
			}
			_ = conn.Close()
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		_ = current.Close()
		replacement, err = net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		return true
	})
	if replacement != nil {
		defer replacement.Close()
	}
	if !attempted || err == nil {
		t.Fatalf("close after disconnect: attempted = %t, error = %v", attempted, err)
	}
	if err := replacement.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	conn, err := replacement.Accept()
	if err == nil {
		_ = conn.Close()
		t.Fatal("close reconnected to the replacement server")
	}
}
