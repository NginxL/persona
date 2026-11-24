package iphone

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runCLI(t *testing.T, m *Manager, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	// command closes the Manager it is given; hand it a fresh one over the same store and bridge.
	c := New(m.store.Dir)
	c.findBridge, c.browse = m.findBridge, m.browse
	code := command(context.Background(), c, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCommand(t *testing.T) {
	m, _ := bridgeManager(t)
	if code, out, _ := runCLI(t, m, "list"); code != 0 || !strings.Contains(out, testUDID) || !strings.Contains(out, "Test Phone") {
		t.Fatalf("list: %d %q", code, out)
	}
	code, out, errOut := runCLI(t, m, "pair")
	if code != 0 || !strings.Contains(out, "Paired Test Phone") || !strings.Contains(out, "iphone address "+testUDID) {
		t.Fatalf("pair: %d %q %q", code, out, errOut)
	}
	if code, out, _ := runCLI(t, m, "address", testUDID, "[2001:db8::5]:49152"); code != 0 || !strings.Contains(out, "Saved") {
		t.Fatalf("address: %d %q", code, out)
	}
	code, out, _ = runCLI(t, m, "list", "--json")
	var devices []Device
	if code != 0 || json.Unmarshal([]byte(out), &devices) != nil || len(devices) != 1 || !devices[0].Paired || devices[0].ManualAddress != "[2001:db8::5]:49152" {
		t.Fatalf("list --json: %d %s", code, out)
	}
	if code, _, _ := runCLI(t, m, "address", testUDID, "--clear"); code != 0 {
		t.Fatal("address --clear failed")
	}
	if rec, _ := m.store.Load(testUDID); rec.Address != "" {
		t.Fatalf("address not cleared: %q", rec.Address)
	}
	if code, _, _ := runCLI(t, m, "address", testUDID, "host:0"); code != 1 {
		t.Fatal("a bad address was accepted")
	}
	if code, _, _ := runCLI(t, m, "forget", testUDID); code != 0 {
		t.Fatal("forget failed")
	}
	if code, _, errOut := runCLI(t, m, "forget", testUDID); code != 1 || !strings.Contains(errOut, "isn't paired") {
		t.Fatalf("forget twice: %d %q", code, errOut)
	}
	t.Setenv("FAKE_PAIR_FAIL", "1")
	saved := pairTimeout
	pairTimeout = 100 * time.Millisecond // the phone never trusts this computer: the command waits this long
	t.Cleanup(func() { pairTimeout = saved })
	if code, _, errOut := runCLI(t, m, "pair", testUDID); code != 1 || !strings.Contains(errOut, "Trust") {
		t.Fatalf("pair failure: %d %q", code, errOut)
	}
	for _, args := range [][]string{{}, {"nope"}, {"pair", "../x"}, {"forget"}, {"call", testUDID}, {"call", testUDID, "tap", "{bad"}} {
		if code, _, _ := runCLI(t, m, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if code, out, _ := runCLI(t, m, "-h"); code != 0 || !strings.Contains(out, "iphone pair") {
		t.Fatalf("-h: %d %q", code, out)
	}
}

func TestCommandSimulator(t *testing.T) {
	m, _ := bridgeManager(t)
	t.Setenv("FAKE_SIMULATOR", "1")
	if code, out, _ := runCLI(t, m, "pair", testUDID); code != 0 || !strings.Contains(out, "Simulator device") {
		t.Fatalf("pair: %d %q", code, out)
	}
	if code, out, _ := runCLI(t, m, "list"); code != 0 || !strings.Contains(out, "(simulator)") {
		t.Fatalf("list: %d %q", code, out)
	}
}

func TestCommandBridgeMissing(t *testing.T) {
	m, _ := bridgeManager(t)
	m.findBridge = func() (string, error) { return "", &Error{Code: CodeBridgeMissing, Message: "x"} }
	if code, _, errOut := runCLI(t, m, "pair"); code != 1 || !strings.Contains(errOut, BridgeName) {
		t.Fatalf("pair: %d %q", code, errOut)
	}
}

func TestCommandMountDDI(t *testing.T) {
	m, _ := bridgeManager(t)
	requests := filepath.Join(t.TempDir(), "requests")
	t.Setenv("FAKE_SERVE_REQUESTS", requests)
	if code, _, _ := runCLI(t, m, "pair", testUDID); code != 0 {
		t.Fatal("pair failed")
	}
	if code, _, _ := runCLI(t, m, "address", testUDID, "192.0.2.7:49152"); code != 0 {
		t.Fatal("address failed")
	}
	code, out, errOut := runCLI(t, m, "mount-ddi", testUDID, "--ddi-dir", "/tmp/ddi", "--force")
	if code != 0 || !strings.Contains(out, "Mounted the developer disk image") || !strings.Contains(out, "image: x.dmg") || !strings.Contains(out, "from Apple's signing server") || !strings.Contains(out, "services: read again") {
		t.Fatalf("mount-ddi: %d %q %q", code, out, errOut)
	}
	sent, _ := os.ReadFile(requests)
	if !strings.Contains(string(sent), `"op":"mount_ddi"`) || !strings.Contains(string(sent), `"args":{"ddi_dir":"/tmp/ddi","force":true}`) {
		t.Fatalf("bridge got %s", sent)
	}
	for _, args := range [][]string{{"mount-ddi"}, {"mount-ddi", testUDID, "--ddi-dir"}, {"mount-ddi", testUDID, "--nope"}, {"mount-ddi", testUDID, "other"}} {
		if code, _, _ := runCLI(t, m, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if code, _, errOut := runCLI(t, m, "mount-ddi", "00000000-NOTPAIRED0001"); code != 1 || !strings.Contains(errOut, "isn't paired") {
		t.Fatalf("not paired: %d %q", code, errOut)
	}
}

func TestMountArgs(t *testing.T) {
	for _, c := range []struct {
		in    []string
		udid  string
		dir   string
		force bool
		ok    bool
	}{
		{[]string{"U"}, "U", "", false, true},
		{[]string{"--force", "U", "--ddi-dir=/d"}, "U", "/d", true, true},
		{[]string{"U", "--ddi-dir", "/d"}, "U", "/d", false, true},
		{[]string{"--ddi-dir="}, "", "", false, false},
		{[]string{}, "", "", false, false},
	} {
		udid, dir, force, ok := mountArgs(c.in)
		if udid != c.udid || dir != c.dir || force != c.force || ok != c.ok {
			t.Errorf("%v: got %q %q %v %v", c.in, udid, dir, force, ok)
		}
	}
}

func TestCommandDeveloperModeOff(t *testing.T) {
	m, _ := bridgeManager(t)
	t.Setenv("FAKE_DEVMODE_OFF", "1")
	code, out, _ := runCLI(t, m, "pair", testUDID)
	if code != 0 || !strings.Contains(out, "Developer Mode is off") {
		t.Fatalf("pair: %d %q", code, out)
	}
	rec, err := m.store.Load(testUDID)
	if err != nil || rec.DeveloperMode == nil || *rec.DeveloperMode {
		t.Fatalf("record: %+v %v", rec, err)
	}
	// Nothing on the network answers: the failure says why.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = m.Call(ctx, testUDID, "screenshot", nil)
	if Code(err) != "developer_mode_off" || !strings.Contains(err.Error(), "Developer Mode") {
		t.Fatalf("call: %v", err)
	}
}
