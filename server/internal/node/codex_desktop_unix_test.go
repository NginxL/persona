//go:build !windows

package node

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// listenFakeCodexBus serves a fake App bus where the App on macOS does: a socket in the home's ipc directory.
func listenFakeCodexBus(t *testing.T) (home string, l net.Listener) {
	t.Helper()
	// A short path: a unix socket path over ~104 bytes (macOS's t.TempDir()) cannot be bound.
	root, err := os.MkdirTemp("/tmp", "cxbus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	home = filepath.Join(root, ".codex")
	os.MkdirAll(filepath.Join(home, "ipc"), 0o700)
	l, err = net.Listen("unix", codexDesktopSocket(home))
	if err != nil {
		t.Fatal(err)
	}
	return home, l
}

// noFakeCodexBus: no bus is served where the test looks (each test home has its own socket path).
func noFakeCodexBus(*testing.T) {}

// unavailableCodexBus leaves either no socket or the stale socket file an App update can leave behind.
func unavailableCodexBus(t *testing.T, stale bool) string {
	t.Helper()
	home, l := listenFakeCodexBus(t)
	l.(*net.UnixListener).SetUnlinkOnClose(!stale)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestCodexDesktopAvailableRequiresAListener(t *testing.T) {
	home, l := listenFakeCodexBus(t)
	t.Cleanup(func() { l.Close() })
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	if !codexDesktopAvailable(home) {
		t.Fatal("a listening legacy bus is unavailable")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	path := codexDesktopSocket(home)
	if st, err := os.Stat(path); err != nil || st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("missing stale socket fixture: %v, %v", st, err)
	}
	if codexDesktopAvailable(home) {
		t.Fatal("a stale socket advertises App control")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if codexDesktopAvailable(home) {
		t.Fatal("a regular file advertises App control")
	}
}

func TestCodexDesktopUnavailableKeepsChatsViewOnly(t *testing.T) {
	for _, transport := range []string{"missing", "stale"} {
		for _, history := range []string{"idle rollout", "working rollout", "paginated"} {
			t.Run(transport+"/"+history, func(t *testing.T) {
				home := unavailableCodexBus(t, transport == "stale")
				n := remoteTestNode(t)
				cwd := t.TempDir()
				n.home = filepath.Dir(home) // use an ordinary node; scoped nodes refuse all desktop starts
				n.configureCodex(proto.CodexConfig{})
				var path string
				if history == "paginated" {
					writeMigratedCodexApp(t, home, codexAppSchema+codexAppRow(appChat, "", cwd, "Saved chat", time.Now(), 0))
				} else {
					if err := os.WriteFile(filepath.Join(home, ".codex-global-state.json"), []byte(`{"projectless-thread-ids":["`+appChat+`"]}`), 0o600); err != nil {
						t.Fatal(err)
					}
					path = writeAppRollout(t, home, appChat, cwd, time.Now())
					appendLines(t, path, event("task_started"))
					if history == "idle rollout" {
						appendLines(t, path, event("task_complete"))
					}
				}
				id := n.registerCodex(home, appChat, path)
				src, _ := n.codexSource(id)
				withCodexAppModels(t, src.home, map[string]codexModelEfforts{})
				row := appChatRow(t, n)
				if row.AgentName != "Codex App" || row.SpaceKind != "codex_app" || row.SourceKind != "codex_history" {
					t.Fatalf("App identity or grouping lost: %+v", row)
				}
				caps := row.Capabilities
				if caps == nil || !caps.Read || caps.Send || caps.Interrupt || caps.Attach || caps.Approve || caps.Terminal || caps.Model != nil || caps.Takeover != nil || len(row.Efforts) != 0 {
					t.Fatalf("view-only chat advertises controls: %+v, efforts %v", caps, row.Efforts)
				}
				if row.ReadOnlyReason != i18n.T(i18n.Default, codexDesktopUnavailableKey) {
					t.Fatalf("view-only reason = %q", row.ReadOnlyReason)
				}
				if history == "working rollout" && row.Status != proto.StatusWorking {
					t.Fatalf("a running view-only chat lost its status: %q", row.Status)
				}
				if path != "" {
					if _, err := n.file(id); err != nil {
						t.Fatalf("the original history is unreadable: %v", err)
					}
				}

				// Requests from an old session list must refuse before opening a chat or accepting a queued send.
				oldOpen := openCodexThread
				openCodexThread = func(string) error { t.Fatal("opened a view-only chat"); return nil }
				t.Cleanup(func() { openCodexThread = oldOpen })
				ctx, queued := withSendQueued(context.Background())
				for name, run := range map[string]func() error{
					"send":       func() error { return n.sendCodex(ctx, id, "hello", nil) },
					"send image": func() error { return n.sendCodex(ctx, id, "look", []proto.Image{{Data: "invalid base64"}}) },
					"interrupt":  func() error { return n.interruptCodex(ctx, id) },
					"start turn": func() error { return n.startCodexDesktopTurn(ctx, src, nil) },
					"settings":   func() error { return modesCall(t, n, id, map[string]any{"effort": "high"}) },
					"new chat": func() error {
						_, err := n.newCodexAppChat(ctx, runtimeStartParams{Prompt: "hello"}, "")
						return err
					},
				} {
					err := run()
					if code, data := refusalOf(t, err); code != "codex_read_only" || data["delivered"] != deliveredNone || err.Error() != row.ReadOnlyReason {
						t.Fatalf("%s: %v, data %v", name, err, data)
					}
				}
				if *queued {
					t.Fatal("accepted a message for an unavailable App")
				}
				if _, added := n.withCodexAppLauncher(nil); added {
					t.Fatal("offered a launcher without an App bus")
				}

				// A live legacy bus restores capabilities on the next list, without a node restart or version gate.
				if transport == "stale" {
					if err := os.Remove(codexDesktopSocket(home)); err != nil {
						t.Fatal(err)
					}
				}
				l, err := net.Listen("unix", codexDesktopSocket(home))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { l.Close() })
				go func() {
					for {
						c, err := l.Accept()
						if err != nil {
							return
						}
						c.Close() // availability probes carry no bus requests
					}
				}()
				row = appChatRow(t, n)
				if !row.Capabilities.Send || row.ReadOnlyReason != "" || row.Capabilities.Interrupt != (history == "working rollout") {
					t.Fatalf("a live bus did not restore App controls: %+v", row)
				}
			})
		}
	}
}

func TestCodexDesktopUnavailableRefusalLanguages(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		err := codexDesktopUnavailable(i18n.With(context.Background(), lang))
		if code, data := refusalOf(t, err); code != "codex_read_only" || data["delivered"] != deliveredNone || err.Error() != i18n.T(lang, codexDesktopUnavailableKey) || err.Error() == codexDesktopUnavailableKey {
			t.Errorf("%s: %v, data %v", lang, err, data)
		}
	}
}
