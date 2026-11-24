package node

import (
	"context"
	"testing"
)

func TestScopedOfflineSendDoesNotInspectOrOpenDesktop(t *testing.T) {
	n, home := testNode(t)
	n.sessionScope = &nodeSessionScope{dir: t.TempDir()}
	n.home = home
	oldDirs, oldOpen := claudeDesktopDirs, openDesktopLink
	claudeDesktopDirs = func(string) []string {
		t.Fatal("a scoped send inspected the user's desktop profile")
		return nil
	}
	openDesktopLink = func(context.Context, string) error {
		t.Fatal("a scoped send opened the user's desktop app")
		return nil
	}
	t.Cleanup(func() { claudeDesktopDirs, openDesktopLink = oldDirs, oldOpen })
	if n.desktopSession(sidA) {
		t.Fatal("a scoped session was classified through the global desktop index")
	}
	if handled, err := n.sendThroughDesktopApp(context.Background(), sidA, "continue", nil); handled || err != nil {
		t.Fatalf("a scoped offline send must remain on its regular route: handled=%v, err=%v", handled, err)
	}
}
