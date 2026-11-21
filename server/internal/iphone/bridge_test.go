package iphone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeBridgeScript speaks the bridge's command line (its wire contract, #3451) well enough for these tests.
// serve answers every request, except op "drop", which loses the link without an answer.
const fakeBridgeScript = `#!/bin/sh
cmd=$1; shift
case "$cmd" in
version) echo "0.0.0-test" ;;
usb-list) echo '[{"udid":"00000000-TESTPHONE0001","name":"Test Phone","product_version":"26.1"}]' ;;
pair)
  udid=$2; out=$4
  if [ -n "$FAKE_PAIR_FAIL" ]; then
    echo "diagnostics" >&2
    echo '{"v":1,"ok":false,"error":{"code":"trust_pending","message":"tap Trust on the phone"}}' >&2
    exit 1
  fi
  # FAKE_PAIR_WAITS=<file>: each line in the file is one answer to give before pairing (trust_pending, locked, …).
  if [ -n "$FAKE_PAIR_WAITS" ] && [ -s "$FAKE_PAIR_WAITS" ]; then
    code=$(head -1 "$FAKE_PAIR_WAITS"); tail -n +2 "$FAKE_PAIR_WAITS" > "$FAKE_PAIR_WAITS.next"; mv "$FAKE_PAIR_WAITS.next" "$FAKE_PAIR_WAITS"
    echo '{"v":1,"ok":false,"error":{"code":"'"$code"'","message":"not yet"}}' >&2
    exit 1
  fi
  printf 'opaque' > "$out"
  if [ -n "$FAKE_SIMULATOR" ]; then
    echo '{"udid":"'"$udid"'","name":"Test Simulator","model":"iPhone99,1","product_version":"26.1","transport":"simulator"}'
    exit 0
  fi
  if [ -n "$FAKE_DEVMODE_OFF" ]; then
    echo '{"udid":"'"$udid"'","name":"Test Phone","model":"iPhone99,1","product_version":"27.0","developer_mode":false}'
    exit 0
  fi
  echo '{"udid":"'"$udid"'","name":"Test Phone","model":"iPhone99,1","product_version":"26.1"}'
  ;;
reveal-developer-mode)
  # reveal-developer-mode --udid <udid>: noted in FAKE_REVEAL_LOG
  if [ -n "$FAKE_REVEAL_LOG" ]; then echo "$2" >> "$FAKE_REVEAL_LOG"; fi
  if [ -n "$FAKE_REVEAL_FAIL" ]; then
    echo '{"v":1,"ok":false,"error":{"code":"failed","message":"no AMFI service"}}' >&2
    exit 1
  fi
  echo '{"udid":"'"$2"'","revealed":true}'
  ;;
match)
  # match --record <path> --identifier <id> --auth-tag <tag>: the record "knows" the tag TAG-1
  if [ "$6" = "TAG-1" ]; then echo '{"match":true}'; else echo '{"match":false}'; fi
  ;;
serve)
  if [ -n "$FAKE_SERVE_FAIL" ]; then
    echo '{"v":1,"ok":false,"error":{"code":"developer_mode_off","message":"turn on Developer Mode"}}' >&2
    exit 1
  fi
  echo "$@" > "$FAKE_SERVE_ARGS"
  # FAKE_DDI_UNMOUNTED: the phone comes up without the developer disk image, and device_info says so until mount_ddi.
  mounted=true
  if [ -n "$FAKE_DDI_UNMOUNTED" ]; then
    mounted=false
    echo '{"event":"ready","device":{"name":"Test Phone","developer_mode":true,"ddi_mounted":false}}'
  else
    echo '{"event":"ready","device":{"name":"Test Phone"}}'
  fi
  while IFS= read -r line; do
    id=$(printf '%s' "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/')
    op=$(printf '%s' "$line" | sed 's/.*"op":"\([^"]*\)".*/\1/')
    echo "$op" >> "$FAKE_SERVE_LOG"
    if [ -n "$FAKE_SERVE_REQUESTS" ]; then echo "$line" >> "$FAKE_SERVE_REQUESTS"; fi
    if [ "$op" = "drop" ]; then
      echo '{"event":"disconnected","reason":"wifi lost"}'
      exit 1
    fi
    if [ "$op" = "mount_ddi" ]; then
      mounted=true
      echo '{"v":1,"id":"'"$id"'","ok":true,"op":"mount_ddi","result":{"mounted":true,"already":false,"image":"x.dmg","ticket":"apple","services_refreshed":true}}'
      continue
    fi
    if [ "$op" = "device_info" ] && [ -n "$FAKE_DDI_UNMOUNTED" ]; then
      echo '{"v":1,"id":"'"$id"'","ok":true,"op":"device_info","result":{"name":"Test Phone","developer_mode":true,"ddi_mounted":'"$mounted"'}}'
      continue
    fi
    echo '{"v":1,"id":"'"$id"'","ok":true,"op":"'"$op"'","result":{"op":"'"$op"'"}}'
  done
  exit 0
  ;;
*) echo '{"v":1,"ok":false,"error":{"code":"bad_request","message":"unknown command"}}' >&2; exit 1 ;;
esac
`

// fakeBridge writes the script and points FAKE_SERVE_LOG / FAKE_SERVE_ARGS at files; it returns the script's path
// and the log's.
func fakeBridge(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake bridge is a shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, BridgeName)
	if err := os.WriteFile(path, []byte(fakeBridgeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "serve.log")
	t.Setenv("FAKE_SERVE_LOG", log)
	t.Setenv("FAKE_SERVE_ARGS", filepath.Join(dir, "serve.args"))
	return path, log
}

func TestBridgeOneShots(t *testing.T) {
	path, _ := fakeBridge(t)
	b := Bridge{Path: path}
	ctx := context.Background()
	if v, err := b.Version(ctx); err != nil || v != "0.0.0-test" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	list, err := b.USBList(ctx)
	if err != nil || len(list) != 1 || list[0].UDID != testUDID || list[0].ProductVersion != "26.1" {
		t.Fatalf("USBList = %+v, %v", list, err)
	}
	out := filepath.Join(t.TempDir(), "rec")
	info, err := b.Pair(ctx, testUDID, out)
	if err != nil || info.ProductType != "iPhone99,1" || info.Name != "Test Phone" {
		t.Fatalf("Pair = %+v, %v", info, err)
	}
	for tag, want := range map[string]bool{"TAG-1": true, "TAG-2": false} {
		svc := Service{TXT: map[string]string{"identifier": "ID", "authtag": tag}}
		if ok, err := b.Match(ctx, out, svc); err != nil || ok != want {
			t.Fatalf("Match(%s) = %v, %v", tag, ok, err)
		}
	}
	t.Setenv("FAKE_PAIR_FAIL", "1")
	if _, err := b.Pair(ctx, testUDID, out); Code(err) != "trust_pending" {
		t.Fatalf("want the bridge's code from stderr's last line, got %v", err)
	}
}

func TestFindBridge(t *testing.T) {
	path, _ := fakeBridge(t)
	t.Setenv(BridgeEnv, path)
	if got, err := FindBridge(); err != nil || got != path {
		t.Fatalf("FindBridge = %q, %v", got, err)
	}
	// Not in PATH: <PERSONA_HOME>/bin.
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv(BridgeEnv, "")
	_ = os.MkdirAll(filepath.Join(home, "bin"), 0o700)
	data, _ := os.ReadFile(path)
	inHome := filepath.Join(home, "bin", BridgeName)
	_ = os.WriteFile(inHome, data, 0o755)
	if got, err := FindBridge(); err != nil || got != inHome {
		t.Fatalf("FindBridge = %q, %v", got, err)
	}
	t.Setenv(BridgeEnv, filepath.Join(t.TempDir(), "missing"))
	if _, err := FindBridge(); Code(err) != CodeBridgeMissing {
		t.Fatalf("want bridge_missing, got %v", err)
	}
}

// bridgeManager is a Manager over the fake bridge script, with a paired phone at a manual address.
func bridgeManager(t *testing.T) (*Manager, string) {
	t.Helper()
	fastTiming(t)
	path, log := fakeBridge(t)
	m := New(t.TempDir())
	t.Cleanup(m.Close)
	m.findBridge = func() (string, error) { return path, nil }
	m.browse = func(context.Context, time.Duration, func([]Service) bool) ([]Service, error) { return nil, nil }
	m.logf = t.Logf
	return m, log
}

func TestSessionOverBridgeProcess(t *testing.T) {
	m, log := bridgeManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAddress(testUDID, "192.0.2.7:50000"); err != nil {
		t.Fatal(err)
	}
	res, err := m.Call(ctx, testUDID, "tap", json.RawMessage(`{"x":1,"y":2}`))
	if err != nil || string(res) != `{"op":"tap"}` {
		t.Fatalf("Call = %s, %v", res, err)
	}
	args, _ := os.ReadFile(os.Getenv("FAKE_SERVE_ARGS"))
	rec, _ := m.store.RecordPath(testUDID)
	if want := "--udid " + testUDID + " --record " + rec + " --host 192.0.2.7 --port 50000"; strings.TrimSpace(string(args)) != want {
		t.Fatalf("serve args %q, want %q", args, want)
	}
	if _, err := m.Call(ctx, testUDID, "drop", nil); Code(err) != CodeUncertain {
		t.Fatalf("want uncertain, got %v", err)
	}
	if res, err := m.Call(ctx, testUDID, "screenshot", nil); err != nil || string(res) != `{"op":"screenshot"}` {
		t.Fatalf("after reconnect: %s, %v", res, err)
	}
	ops, _ := os.ReadFile(log)
	if got := strings.Fields(string(ops)); strings.Join(got, ",") != "tap,drop,screenshot" {
		t.Fatalf("bridge got %v: drop must reach it once", got)
	}
	d := m.Devices(ctx)
	if len(d) != 1 || !d[0].Paired || !d[0].USB || d[0].State != StateConnected || d[0].ManualAddress != "192.0.2.7:50000" {
		t.Fatalf("Devices = %+v", d)
	}
}

func TestServeFailureBeforeReady(t *testing.T) {
	m, _ := bridgeManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	_ = m.SetAddress(testUDID, "192.0.2.7")
	t.Setenv("FAKE_SERVE_FAIL", "1")
	// Waiting won't fix this one: the call ends with the bridge's reason without waiting out its deadline.
	cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
	defer ccancel()
	started := time.Now()
	_, err := m.Call(cctx, testUDID, "tap", nil)
	if Code(err) != "developer_mode_off" || Sent(err) != "no" || time.Since(started) > 3*time.Second {
		t.Fatalf("want developer_mode_off at once, got %v after %s", err, time.Since(started))
	}
	if d := m.Devices(ctx); d[0].LastErrorCode != "developer_mode_off" {
		t.Fatalf("Devices = %+v", d[0])
	}
}

func TestPairKeepsAddressAndCleansUpOnFailure(t *testing.T) {
	m, _ := bridgeManager(t)
	ctx := context.Background()
	t.Setenv("FAKE_PAIR_FAIL", "1")
	// The phone never trusts this computer: Pair waits for it until its context ends.
	short, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, err := m.Pair(short, testUDID, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the wait to run out, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.store.Dir, testUDID)); !os.IsNotExist(err) {
		t.Fatalf("a failed first pairing left its directory: %v", err)
	}
	t.Setenv("FAKE_PAIR_FAIL", "")
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	_ = m.SetAddress(testUDID, "phone.example.com")
	rec, err := m.Pair(ctx, testUDID, nil)
	if err != nil || rec.Address != "phone.example.com" || rec.ProductType != "iPhone99,1" {
		t.Fatalf("pairing again: %+v, %v", rec, err)
	}
	path, _ := m.store.RecordPath(testUDID)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("record: %v %v", st, err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, []byte("opaque")) {
		t.Fatalf("record = %q", b)
	}
	entries, _ := os.ReadDir(filepath.Join(m.store.Dir, testUDID))
	if len(entries) != 2 {
		t.Fatalf("left over files: %v", entries)
	}
	// A failure pairing again keeps the pairing there was.
	t.Setenv("FAKE_PAIR_FAIL", "1")
	short2, cancel2 := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel2()
	if _, err := m.Pair(short2, testUDID, nil); err == nil {
		t.Fatal("want failure")
	}
	if _, err := m.store.Load(testUDID); err != nil {
		t.Fatalf("the earlier pairing is gone: %v", err)
	}
}

func TestPairJob(t *testing.T) {
	m, _ := bridgeManager(t)
	if err := m.StartPair(testUDID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.Job().State == JobRunning {
		if err := m.StartForget(testUDID); Code(err) != CodeBusy {
			t.Fatalf("a second job: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("pair job still running")
		}
		<-time.After(10 * time.Millisecond)
	}
	if j := m.Job(); j.State != JobDone || j.Kind != "pair" || j.UDID != testUDID {
		t.Fatalf("job = %+v", j)
	}
	// A phone that never trusts this computer: the job waits, saying so, until its time runs out.
	saved := pairTimeout
	pairTimeout = 300 * time.Millisecond
	t.Cleanup(func() { pairTimeout = saved })
	t.Setenv("FAKE_PAIR_FAIL", "1")
	_ = m.StartPair(testUDID)
	sawWaiting := false
	for m.Job().State == JobRunning {
		if m.Job().Waiting == CodeTrustPending {
			sawWaiting = true
		}
		<-time.After(5 * time.Millisecond)
	}
	if j := m.Job(); j.State != JobFailed || j.Code != "timeout" || j.Waiting != "" || !sawWaiting {
		t.Fatalf("job = %+v (saw waiting %v)", j, sawWaiting)
	}
	if err := m.StartForget(testUDID); err != nil {
		t.Fatal(err)
	}
	for m.Job().State == JobRunning {
		<-time.After(10 * time.Millisecond)
	}
	if _, err := m.store.Load(testUDID); err != ErrNotPaired {
		t.Fatalf("after forget: %v", err)
	}
}

// TestPairWaitsForThePhone: while the phone is locked, then doesn't trust this computer yet, Pair asks again and
// says what it waits for, once each, and pairs once the phone trusts it (#8199).
func TestPairWaitsForThePhone(t *testing.T) {
	m, _ := bridgeManager(t)
	waits := filepath.Join(t.TempDir(), "waits")
	if err := os.WriteFile(waits, []byte("locked\ntrust_pending\ntrust_pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_PAIR_WAITS", waits)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var said []string
	rec, err := m.Pair(ctx, testUDID, func(step, waiting string) { said = append(said, waiting) })
	if err != nil || rec.UDID != testUDID {
		t.Fatalf("Pair = %+v, %v", rec, err)
	}
	if got := strings.Join(said, ","); got != ",locked,trust_pending," {
		t.Fatalf("progress said waiting %q", got)
	}
	// Another refusal is not waited out.
	if err := os.WriteFile(waits, []byte("pairing_rejected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Pair(ctx, testUDID, nil); Code(err) != "pairing_rejected" {
		t.Fatalf("want pairing_rejected at once, got %v", err)
	}
}

// gatedWriter's Write finishes only when release is closed, then succeeds or fails.
type gatedWriter struct {
	release chan struct{}
	fail    bool
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	<-w.release
	if w.fail {
		return 0, errors.New("file already closed")
	}
	return len(p), nil
}

func TestSendLineAtTheDeadline(t *testing.T) {
	for _, fail := range []bool{false, true} {
		w := &gatedWriter{release: make(chan struct{}), fail: fail}
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // the deadline has passed while the write is under way
		closed := false
		// Closing the link is what ends a blocked write; here it lets the write finish, as it may in a real race.
		err := sendLine(ctx, w, func() { closed = true; close(w.release) }, []byte(`{"op":"tap"}`))
		if !closed {
			t.Fatal("the link wasn't closed")
		}
		if !fail && err != nil {
			t.Fatalf("the whole line was written: want nil (the caller waits for the answer), got %v", err)
		}
		if fail && err == nil {
			t.Fatal("a failed write is a line that never left")
		}
	}
}

func TestSimulatorRecordOverBridgeProcess(t *testing.T) {
	m, _ := bridgeManager(t)
	browsed := false
	m.browse = func(context.Context, time.Duration, func([]Service) bool) ([]Service, error) {
		browsed = true
		return nil, nil
	}
	t.Setenv("FAKE_SIMULATOR", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rec, err := m.Pair(ctx, testUDID, nil)
	if err != nil || !rec.Simulator() {
		t.Fatalf("Pair = %+v, %v", rec, err)
	}
	// No address anywhere, and none needed.
	if res, err := m.Call(ctx, testUDID, "tap", nil); err != nil || string(res) != `{"op":"tap"}` {
		t.Fatalf("Call = %s, %v", res, err)
	}
	args, _ := os.ReadFile(os.Getenv("FAKE_SERVE_ARGS"))
	path, _ := m.store.RecordPath(testUDID)
	if want := "--udid " + testUDID + " --record " + path; strings.TrimSpace(string(args)) != want {
		t.Fatalf("serve args %q, want %q (no --host for a Simulator device)", args, want)
	}
	if browsed {
		t.Fatal("a Simulator device was looked for on the network")
	}
	d := m.Devices(ctx)
	if len(d) != 1 || d[0].Transport != TransportSimulator || d[0].State != StateConnected || d[0].AddressSource != TransportSimulator {
		t.Fatalf("Devices = %+v", d)
	}
}

// TestDeveloperModeFollowsThePhone: a phone paired with Developer Mode off says so in Devices, and once its
// device_info says it is on (it was turned on after pairing), the record and Devices follow (#8199).
func TestDeveloperModeFollowsThePhone(t *testing.T) {
	m, _ := bridgeManager(t)
	t.Setenv("FAKE_DEVMODE_OFF", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	if d := m.Devices(ctx); len(d) != 1 || d[0].DeveloperMode == nil || *d[0].DeveloperMode {
		t.Fatalf("after pairing with it off: %+v", d)
	}
	m.remember(testUDID, Endpoint{}, json.RawMessage(`{"name":"Test Phone","developer_mode":true}`))
	if rec, err := m.store.Load(testUDID); err != nil || rec.DeveloperMode == nil || !*rec.DeveloperMode {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	if d := m.Devices(ctx); d[0].DeveloperMode == nil || !*d[0].DeveloperMode {
		t.Fatalf("Devices = %+v", d[0])
	}
	// A device_info that doesn't say leaves it as it was.
	m.remember(testUDID, Endpoint{}, json.RawMessage(`{"name":"Test Phone"}`))
	if rec, _ := m.store.Load(testUDID); rec.DeveloperMode == nil || !*rec.DeveloperMode {
		t.Fatalf("record = %+v", rec)
	}
}

// TestPrepareMountsAndThenAgainByItself: Prepare mounts the developer disk image through the session and allows this
// computer to mount it again by itself; a phone that then comes back without it (restarted) gets it mounted on
// connecting, once per link, and one whose owner never prepared it never does (#8199).
func TestPrepareMountsAndThenAgainByItself(t *testing.T) {
	m, log := bridgeManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	_ = m.SetAddress(testUDID, "192.0.2.7")
	ops := func() string { b, _ := os.ReadFile(log); return strings.Join(strings.Fields(string(b)), ",") }

	// Never prepared: a phone without the image is left as it is.
	t.Setenv("FAKE_DDI_UNMOUNTED", "1")
	if _, err := m.Call(ctx, testUDID, "tap", nil); err != nil {
		t.Fatal(err)
	}
	if got := ops(); got != "tap" {
		t.Fatalf("mounted without the owner: %s", got)
	}

	if err := m.StartPrepare(testUDID); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, m); j.State != JobDone || j.Kind != "prepare" || j.UDID != testUDID {
		t.Fatalf("job = %+v", j)
	}
	if rec, _ := m.store.Load(testUDID); !rec.AutoPrepare {
		t.Fatal("prepared, but not allowed to mount again")
	}
	// The phone's status says it at once: device_info is read again right after the mount, not at the next health
	// check a minute later (#12171).
	d := m.Devices(ctx)
	if !d[0].AutoPrepare || !bytes.Contains(d[0].Info, []byte(`"ddi_mounted":true`)) {
		t.Fatalf("Devices = %+v (info %s)", d[0], d[0].Info)
	}
	if got := ops(); got != "tap,mount_ddi,device_info" {
		t.Fatalf("ops %s", got)
	}

	// The phone restarted: the next link comes up without the image and mounts it by itself, once.
	m.disconnect(testUDID)
	if _, err := m.Call(ctx, testUDID, "screenshot", nil); err != nil {
		t.Fatal(err)
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for strings.Count(ops(), "mount_ddi") < 2 {
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("not mounted again: %s", ops())
		}
	}
	if _, err := m.Call(ctx, testUDID, "tap", nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(ops(), "mount_ddi"); n != 2 {
		t.Fatalf("mounted %d times on one link: %s", n, ops())
	}
	// Pairing again keeps the owner's word.
	if _, err := m.Pair(ctx, testUDID, nil); err != nil {
		t.Fatal(err)
	}
	if rec, _ := m.store.Load(testUDID); !rec.AutoPrepare {
		t.Fatal("pairing again dropped AutoPrepare")
	}
	if err := m.StartPrepare("00000000-NOTPAIRED"); err == nil {
		t.Fatal("preparing a phone that isn't paired")
	}
}

// TestPairShowsDeveloperModeWhenItIsOff: a phone that paired with Developer Mode off gets its hidden switch shown in
// Settings while it is still on the cable; one with it on, or a simulator, doesn't; a bridge that can't show it
// doesn't fail the pairing (#8199).
func TestPairShowsDeveloperModeWhenItIsOff(t *testing.T) {
	m, _ := bridgeManager(t)
	reveals := filepath.Join(t.TempDir(), "reveals")
	t.Setenv("FAKE_REVEAL_LOG", reveals)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	asked := func() string { b, _ := os.ReadFile(reveals); return strings.TrimSpace(string(b)) }
	if _, err := m.Pair(ctx, testUDID, nil); err != nil || asked() != "" {
		t.Fatalf("Developer Mode not off: %v, asked %q", err, asked())
	}
	t.Setenv("FAKE_DEVMODE_OFF", "1")
	var steps []string
	if _, err := m.Pair(ctx, testUDID, func(step, _ string) { steps = append(steps, step) }); err != nil || asked() != testUDID {
		t.Fatalf("Developer Mode off: %v, asked %q", err, asked())
	}
	if !strings.Contains(strings.Join(steps, "|"), "showing Developer Mode") {
		t.Fatalf("steps %v", steps)
	}
	t.Setenv("FAKE_REVEAL_FAIL", "1")
	if rec, err := m.Pair(ctx, testUDID, nil); err != nil || rec.DeveloperMode == nil || *rec.DeveloperMode {
		t.Fatalf("a bridge that can't show it: %+v, %v", rec, err)
	}
}
