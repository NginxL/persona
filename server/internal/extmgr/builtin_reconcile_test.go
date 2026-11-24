package extmgr

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extmgr/imessageport"
)

// cua, ego and imessage are built-in programs on computers (node.command "builtin"): the hub never sends them a
// package nor counts states for them, the apps' settings see no node_machines nor added confirm (nothing shown
// changes), and a machine that connects still gets what it always got from cua and ego.
func TestBuiltinExtensionsAreBuiltinNodePrograms(t *testing.T) {
	h, _ := extensionTestHub(t)
	ids := []string{cuaExtensionID, egoExtensionID}
	if imessageport.Factory() != nil { // the iMessage extension is its module's: absent without it (persona_without_imessage, persona_core_only)
		ids = append(ids, imessageExtensionID)
	}
	for _, id := range ids {
		p := h.extensions.reg.Get(id)
		if p == nil || !p.Manifest.Node.Builtin() || p.Manifest.NodeProgram() != nil {
			t.Fatalf("%s: %+v", id, p)
		}
		if p.Record.GrantedDigest != extension.Digest(p.Manifest) {
			t.Fatalf("%s: digest", id)
		}
		d := h.extensions.detail(context.Background(), p)
		if d.NodeMachines != "" || d.Nodes != nil {
			t.Fatalf("%s: shows node programs: %q %v", id, d.NodeMachines, d.Nodes)
		}
		for _, f := range d.Form.Fields { // Form is the extension's own schema
			if strings.Contains(f.Confirm, "将在这台电脑上运行") {
				t.Fatalf("%s: a confirm was added to %s", id, f.Name)
			}
		}
		for _, l := range d.Permissions {
			if strings.HasPrefix(l.Key, "node_") {
				t.Fatalf("%s: permission line %s", id, l.Key)
			}
		}
	}
	if err := h.extensions.reg.SetEnabled(context.Background(), cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	mac, seen := computerMac(t, h, "mac")
	before, _ := seen()
	fakeNoLockMu.Lock()
	delete(fakeNoLock, "mac")
	delete(fakeCursor, "mac")
	fakeNoLockMu.Unlock()
	h.reconcileNodePrograms(context.Background(), mac)
	waitLong(t, "a connected Mac told what cua tells it", func() bool {
		n, _ := seen()
		fakeNoLockMu.Lock()
		defer fakeNoLockMu.Unlock()
		return n > before && len(fakeNoLock["mac"]) > 0 && len(fakeCursor["mac"]) > 0
	})
	time.Sleep(100 * time.Millisecond)
}
