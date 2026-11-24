package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/tailscale/go-winio"
)

func TestClaudeDesktopWindowsRedirectedAppData(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(data, "Local"))
	t.Setenv("APPDATA", filepath.Join(data, "Roaming"))
	dirs := claudeDesktopDirs(home)
	for _, base := range []string{"Local", "Roaming"} {
		if !slices.Contains(dirs, filepath.Join(data, base, "Claude-3p")) {
			t.Fatalf("redirected %s not found in %v", base, dirs)
		}
	}
}

func TestOpenClaudeDesktopLinkWindows(t *testing.T) {
	const link = "claude://claude.ai/epitaxy/local_eeeeeeee-0000-4000-8000-000000000001"
	registered, opener := claudeDesktopLinkRegistered, shellOpenClaudeDesktop
	t.Cleanup(func() { claudeDesktopLinkRegistered, shellOpenClaudeDesktop = registered, opener })
	var opened []string
	available := true
	claudeDesktopLinkRegistered = func() bool { return available }
	shellOpenClaudeDesktop = func(link string) error { opened = append(opened, link); return nil }
	if err := openClaudeDesktopLink(context.Background(), link); err != nil || len(opened) != 1 || opened[0] != link {
		t.Fatalf("open = %v, links = %v", err, opened)
	}
	resume := "claude://resume?session=eeeeeeee-0000-4000-8000-000000000001"
	if err := openClaudeDesktopLink(context.Background(), resume); err != nil || len(opened) != 2 || opened[1] != resume {
		t.Fatalf("resume = %v, links = %v", err, opened)
	}
	available = false
	if err := openClaudeDesktopLink(context.Background(), link); err == nil || len(opened) != 2 {
		t.Fatalf("unregistered handler = %v, links = %v", err, opened)
	}
	available = true
	for _, bad := range []string{"https://claude.ai", "claude://claude.ai/epitaxy/local_../bad", link + "?prompt=x", resume + "&prompt=x", "claude-3p://claude.ai/epitaxy/local_eeeeeeee-0000-4000-8000-000000000001"} {
		if err := openClaudeDesktopLink(context.Background(), bad); err == nil || len(opened) != 2 {
			t.Errorf("invalid link %q = %v", bad, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := openClaudeDesktopLink(ctx, link); !errors.Is(err, context.Canceled) || len(opened) != 2 {
		t.Fatalf("cancelled open = %v", err)
	}
	failed := errors.New("handler unavailable")
	shellOpenClaudeDesktop = func(string) error { return failed }
	if err := openClaudeDesktopLink(context.Background(), link); !errors.Is(err, failed) {
		t.Fatalf("handler error = %v", err)
	}
}

func TestClaudeDesktopInboxProbeDoesNotConnectWindows(t *testing.T) {
	name := `\\.\pipe\LOCAL\cc-msg-test-` + fakeSocketName()
	if peerInboxExists(name) {
		t.Fatal("absent inbox was found")
	}
	listener, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan bool, 1)
	go func() {
		if conn, err := listener.Accept(); err == nil {
			conn.Close()
			accepted <- true
		}
	}()
	if !peerInboxExists(name) {
		t.Fatal("listening inbox was not found")
	}
	select {
	case <-accepted:
		t.Fatal("the existence check connected to the inbox")
	case <-time.After(100 * time.Millisecond):
	}
}

// Exercise the Windows directory discovery, app opener and authenticated pipe as one send to the original session.
func TestSendThroughClaudeDesktopWindows(t *testing.T) {
	n, home := testNode(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	noPaneEnv(t)
	shortPeerWait(t, 3*time.Second)
	const id = "eeeeeeee-0000-4000-8000-000000000031"
	app := filepath.Join(home, "AppData", "Local", "Claude-3p")
	writeDesktopEntry(t, app, id, id, false, nil, "")
	dir := filepath.Join(home, ".claude", "projects", "test-project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"hello"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.paths[id] = path
	inbox := newFakeInbox(t, path)
	old := openDesktopLink
	defer func() { openDesktopLink = old }()
	opened := 0
	openDesktopLink = func(_ context.Context, link string) error {
		if link != "claude://claude.ai/epitaxy/local_"+id {
			t.Fatalf("opened %q", link)
		}
		opened++
		writeOutsideClaude(t, home, id, "claude-desktop-3p", "idle", inbox, nil)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	handled, err := n.sendThroughDesktopApp(ctx, id, "continue this conversation", nil)
	if !handled || err != nil || opened != 1 || len(inbox.users()) != 1 {
		t.Fatalf("send handled=%v err=%v opens=%d messages=%d", handled, err, opened, len(inbox.users()))
	}
}
