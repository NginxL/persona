package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/tmux"
)

func TestTmuxPaneOf(t *testing.T) {
	for _, c := range []struct{ target, socket, want string }{
		{"orc:@11.%11", "", "tmux:%11"},
		{"%3", "", "tmux:%3"},
		// A pane id repeats across servers: the one that is not the default travels with it.
		{"orc:@11.%11", "/tmp/tmux-501/work", "tmux:/tmp/tmux-501/work|%11"},
		{"", "", ""},
		{"orc:@0", "", ""}, // no pane: window only
	} {
		if got := tmuxPaneOf(c.target, c.socket); got != c.want {
			t.Errorf("tmuxPaneOf(%q, %q) = %q, want %q", c.target, c.socket, got, c.want)
		}
	}
}

// tmux names its server in TMUX; the default one needs no socket, and tmux runs against it without -S.
func TestTmuxSocket(t *testing.T) {
	for _, c := range []struct{ env, want string }{
		{"/private/tmp/tmux-501/default,4242,0", ""},
		{"/tmp/tmux-501/work,4242,0", "/tmp/tmux-501/work"},
		{"", ""},
	} {
		if got := tmuxSocket(c.env); got != c.want {
			t.Errorf("tmuxSocket(%q) = %q, want %q", c.env, got, c.want)
		}
	}
}

// A fake tmux prints a dialog for a capture and records the keys it is asked to press.
func TestTmuxHostReadsAndPresses(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
[ "$1" = "-S" ] && { echo "socket:$2" >> ` + calls + `; shift 2; }
case "$1" in
-V) echo 'tmux 3.6a' ;;
capture-pane) echo "$@" >> ` + calls + `; echo 'Do you want to proceed?' ;;
send-keys) echo "$@" >> ` + calls + ` ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &tmuxHost{c: tmux.Client{Bin: bin}}
	pane := host.PaneRef{Kind: host.Tmux, ID: "%3"}

	text, err := h.Read(context.Background(), pane, host.ReadOpts{Lines: 30})
	if err != nil || !strings.Contains(text, "Do you want to proceed?") {
		t.Fatalf("Read = %q, %v", text, err)
	}
	// The source travels to tmux: the scrollback with wrapped lines joined.
	if _, err := h.Read(context.Background(), pane, host.ReadOpts{Source: "recent-unwrapped", Lines: 30}); err != nil {
		t.Fatal(err)
	}
	if err := h.SendKeys(context.Background(), pane, "enter"); err != nil {
		t.Fatal(err)
	}
	// A pane on another server is reached through that server: -S names it, and the pane is still "%3".
	other := host.PaneRef{Kind: host.Tmux, ID: "/tmp/tmux-501/work|%3"}
	if err := h.SendKeys(context.Background(), other, "enter"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(b), "send-keys -t %3 Enter") {
		t.Fatalf("keys pressed = %q, %v", b, err)
	}
	if !strings.Contains(string(b), "capture-pane -p -t %3 -S -30 -J") {
		t.Fatalf("recent-unwrapped not read as such = %q", b)
	}
	if !strings.Contains(string(b), "socket:/tmp/tmux-501/work") {
		t.Fatalf("socket not passed = %q", b)
	}
}

func TestTmuxClientPaneInfo(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
[ "$1" = "-S" ] && { echo "socket:$2" >> ` + calls + `; shift 2; }
case "$1" in
display-message) echo "$@" >> ` + calls + `; printf '4242\t/dev/ttys009\t0\t1\n' ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := tmux.Client{Bin: bin, Socket: "/tmp/tmux-501/work"}
	info, err := c.PaneInfo(context.Background(), "%11")
	if err != nil {
		t.Fatal(err)
	}
	if info.PID != 4242 || info.TTY != "/dev/ttys009" || info.Dead || !info.InMode {
		t.Fatalf("PaneInfo = %+v", info)
	}
	b, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(b), "socket:/tmp/tmux-501/work") || !strings.Contains(string(b), "display-message -p -t %11") {
		t.Fatalf("calls = %q, err = %v", b, err)
	}
}
