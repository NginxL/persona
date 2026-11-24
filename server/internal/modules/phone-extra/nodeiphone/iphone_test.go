package nodeiphone

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/modules/phone-extra/iphone"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeIPhones is an iphoneHost whose bridge, devices and answers the test sets.
type fakeIPhones struct {
	ddi       bool // the developer disk image is on this computer
	installed bool
	links     []iphoneLink
	info      map[string]string // device_info's answer by device
	infoErr   error
	mu        sync.Mutex
	calls     []string
	jobs      []string
	connected []string
	job       *iphoneJob
}

func (f *fakeIPhones) Bridge(context.Context) (bool, string) { return f.installed, "0.1.0" }
func (f *fakeIPhones) DDIPresent() bool                      { return f.ddi }
func (f *fakeIPhones) Devices(context.Context) []iphoneLink  { return f.links }
func (f *fakeIPhones) Call(ctx context.Context, device, op string, args json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, device+" "+op+" "+string(args))
	f.mu.Unlock()
	if !slices.ContainsFunc(f.links, func(l iphoneLink) bool { return l.ID == device && l.Paired }) {
		return nil, &rpc.Error{Code: "not_paired", Message: "no record"} // as the session refuses it, before sending
	}
	if op == "device_info" {
		if f.infoErr != nil {
			return nil, f.infoErr
		}
		return json.RawMessage(f.info[device]), nil
	}
	return json.RawMessage(`{}`), nil
}
func (f *fakeIPhones) Connect(device string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = append(f.connected, device)
	return nil
}
func (f *fakeIPhones) Pair(device string) error {
	f.jobs = append(f.jobs, "pair "+device)
	f.job = &iphoneJob{Kind: "pair", State: "failed", ErrorCode: "trust_pending", Error: "tap Trust"}
	return nil
}
func (f *fakeIPhones) Forget(device string) error {
	f.jobs = append(f.jobs, "forget "+device)
	return nil
}
func (f *fakeIPhones) Prepare(device string) error {
	f.jobs = append(f.jobs, "prepare "+device)
	f.job = &iphoneJob{Kind: "prepare", State: "running", Stage: "mount", Device: device, Step: "mounting the developer disk image"}
	return nil
}
func (f *fakeIPhones) Install(src iphone.InstallSource) error {
	f.jobs = append(f.jobs, "install "+strings.Join(src.URLs, " "))
	f.job = &iphoneJob{Kind: "install", State: "running", Stage: "download", Step: "downloading the iPhone bridge"}
	return nil
}
func (f *fakeIPhones) Cancel()         { f.job = nil }
func (f *fakeIPhones) Job() *iphoneJob { return f.job }

// fakeCore is an iphoneDriver that answers what the test set, after running ops with the bridge.
type fakeCore struct {
	runs  []iphoneRun
	ops   []string // the bridge's operations it runs first, each with {"x":1,"y":2}
	out   iphoneOutcome
	err   error
	shot  string // a screenshot it writes into TempDir and answers by this name ("" none)
	block bool   // wait for the call's context
	wait  time.Duration
}

func (c *fakeCore) Run(ctx context.Context, r iphoneRun) (iphoneOutcome, error) {
	c.runs = append(c.runs, r)
	if d, ok := ctx.Deadline(); ok {
		c.wait = time.Until(d)
	}
	for _, op := range c.ops {
		if _, err := r.Bridge(ctx, op, json.RawMessage(`{"x":1,"y":2}`)); err != nil {
			return iphoneOutcome{}, err
		}
	}
	if c.block {
		<-ctx.Done()
		return iphoneOutcome{}, ctx.Err()
	}
	out := c.out
	if c.shot != "" {
		png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
		if err := os.WriteFile(filepath.Join(r.TempDir, "shot.png"), png, 0o600); err != nil {
			return iphoneOutcome{}, err
		}
		out.ImageFile = c.shot
		if c.shot == "full" {
			out.ImageFile = filepath.Join(r.TempDir, "shot.png")
		}
	}
	return out, c.err
}

func useIPhones(t *testing.T, f *fakeIPhones, c *fakeCore) {
	t.Helper()
	oldHost, oldCore := iphones, iphoneCore
	iphones = f
	if c != nil {
		iphoneCore = c
	}
	t.Cleanup(func() { iphones, iphoneCore = oldHost, oldCore })
}

func iphoneCallErr(t *testing.T, err error) (code, sent string) {
	t.Helper()
	var e *rpc.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not an rpc.Error", err)
	}
	var d proto.IPhoneErrorData
	if json.Unmarshal(e.Data, &d) != nil {
		t.Fatalf("error %v has no sent data: %s", err, e.Data)
	}
	return e.Code, d.Sent
}

const readyInfo = `{"name":"Phone","product_type":"iPhone16,2","os_version":"18.1","locked":false,"developer_mode":true,"ddi_mounted":true,
  "capabilities":{"screenshot":true,"ax_tree":true,"input":true,"apps":true}}`

// A real iPhone has no element tree (only the simulator's bridge reads one): it is operated from the screenshot, by
// coordinates, every input asking the user. So it is ready with screenshot and input; the tree's absence is a
// capability, not a note (#3891). One without remote input (iOS before 27) still is not, nor is a locked one.
func TestIPhoneReadyWithoutElementTree(t *testing.T) {
	const real = `{"name":"Phone","product_type":"iPhone17,1","os_version":"27.0","locked":false,
  "capabilities":{"screenshot":true,"input":true,"apps":true,"ax_tree":false},"notes":{"ax_tree":"unsupported"}}`
	f := &fakeIPhones{installed: true, links: []iphoneLink{
		{ID: "real", Paired: true, State: "connected"},
		{ID: "old", Paired: true, State: "connected"},
		{ID: "locked", Paired: true, State: "connected"},
	}, info: map[string]string{
		"real": real,
		"old": `{"locked":false,"capabilities":{"screenshot":true,"input":false,"apps":true,"ax_tree":false},
  "notes":{"input":"unsupported","ax_tree":"unsupported"}}`,
		"locked": strings.Replace(real, `"locked":false`, `"locked":true`, 1),
	}}
	useIPhones(t, f, nil)
	byID := map[string]proto.IPhoneDevice{}
	for _, d := range Status(context.Background()).Devices {
		byID[d.ID] = d
	}
	if d := byID["real"]; !d.Ready || d.Note != "" || !d.Connected || d.Capabilities["ax_tree"] || !d.Capabilities["input"] {
		t.Fatalf("a real iPhone without an element tree: %+v", d)
	}
	if d := byID["old"]; d.Ready || d.Note != iphoneNotes["unsupported"] {
		t.Fatalf("no remote input: %+v", d)
	}
	if d := byID["locked"]; d.Ready || !d.Locked || d.Note != iphoneNotes["locked"] {
		t.Fatalf("locked: %+v", d)
	}
}

func TestIPhoneStatusSaysWhatIsMissing(t *testing.T) {
	ctx := context.Background()
	useIPhones(t, &fakeIPhones{}, nil)
	if p := Status(ctx); p.Installed || p.Note != iphoneNoteNoBridge || p.Code != "bridge_missing" || p.Devices == nil || p.Checked.IsZero() ||
		p.DDIPresent == nil || *p.DDIPresent {
		t.Fatalf("no bridge: %+v", p)
	}
	useIPhones(t, &fakeIPhones{ddi: true}, nil)
	if p := Status(ctx); p.DDIPresent == nil || !*p.DDIPresent {
		t.Fatalf("with the developer disk image: %+v", p)
	}
	useIPhones(t, &fakeIPhones{installed: true, links: []iphoneLink{{ID: "u1", Name: "Phone", USB: true}}}, nil)
	if p := Status(ctx); p.Note != iphoneNoteNonePaired || p.Code != "none_paired" || len(p.Devices) != 1 || p.Devices[0].Note != iphoneNoteUSB ||
		p.Devices[0].Code != "usb_unpaired" || p.Version != "0.1.0" {
		t.Fatalf("plugged in, nothing paired: %+v", p)
	}

	off := false
	f := &fakeIPhones{installed: true, links: []iphoneLink{
		{ID: "ready", Paired: true, State: "connected", Transport: "network"},
		{ID: "locked", Paired: true, State: "connected"},
		{ID: "devmode", Paired: true, State: "connected"},
		{ID: "backoff", Paired: true, State: "backoff", ErrorCode: "pairing_rejected"},
		{ID: "connecting", Paired: true, State: "connecting", ErrorCode: "unreachable"},
		{ID: "idle", Paired: true, State: "idle", DeveloperMode: &off},
	}, info: map[string]string{
		"ready":   readyInfo,
		"locked":  strings.Replace(readyInfo, `"locked":false`, `"locked":true`, 1),
		"devmode": `{"locked":false,"developer_mode":false,"capabilities":{"screenshot":true,"ax_tree":true,"input":false},"notes":{"input":"developer_mode_off"}}`,
	}}
	useIPhones(t, f, nil)
	p := Status(ctx)
	if p.Note != "" || len(p.Devices) != 6 {
		t.Fatalf("paired: %+v", p)
	}
	byID := map[string]proto.IPhoneDevice{}
	for _, d := range p.Devices {
		byID[d.ID] = d
	}
	if d := byID["ready"]; !d.Ready || !d.Connected || d.Note != "" || d.Code != "" || d.ProductType != "iPhone16,2" || d.IOS != "18.1" || !d.Capabilities["input"] || d.Transport != "network" {
		t.Fatalf("ready: %+v", d)
	}
	if d := byID["locked"]; d.Ready || !d.Locked || d.Note != iphoneNotes["locked"] || d.Code != "locked" {
		t.Fatalf("locked: %+v", d)
	}
	if d := byID["devmode"]; d.Ready || d.Note != iphoneNotes["developer_mode_off"] || d.Code != "developer_mode_off" ||
		d.DeveloperMode == nil || *d.DeveloperMode {
		t.Fatalf("developer mode off: %+v", d)
	}
	if d := byID["backoff"]; d.Connected || d.Note != iphoneNotes["pairing_rejected"] || d.Code != "pairing_rejected" {
		t.Fatalf("rejected: %+v", d)
	}
	if d := byID["connecting"]; d.Note != iphoneNoteConnecting || d.Code != "connecting" {
		t.Fatalf("connecting: %+v", d)
	}
	if d := byID["idle"]; d.Note != iphoneNoteConnecting || d.Code != "connecting" || strings.Join(f.connected, ",") != "idle" ||
		d.DeveloperMode == nil || *d.DeveloperMode {
		t.Fatalf("an idle session is started, saying what the record knows: %+v %v", d, f.connected)
	}
	if strings.Count(strings.Join(f.calls, "\n"), "device_info") != 3 {
		t.Fatalf("only connected ones are probed: %v", f.calls)
	}
	// What the session last read of a device is enough for the page; the assistant's status asks it again.
	f = &fakeIPhones{installed: true, links: []iphoneLink{{ID: "a", Paired: true, State: "connected", Info: json.RawMessage(readyInfo)}},
		info: map[string]string{"a": strings.Replace(readyInfo, `"locked":false`, `"locked":true`, 1)}}
	useIPhones(t, f, nil)
	if d := Status(ctx).Devices[0]; !d.Ready || len(f.calls) != 0 {
		t.Fatalf("the session's device_info: %+v %v", d, f.calls)
	}
	res, err := Call(ctx, json.RawMessage(`{"op":"status"}`))
	var fresh proto.IPhoneDevice
	if err != nil || json.Unmarshal(res.Output, &fresh) != nil || !fresh.Locked || len(f.calls) != 1 {
		t.Fatalf("asked again: %+v %v %v", fresh, err, f.calls)
	}

	useIPhones(t, &fakeIPhones{installed: true, links: []iphoneLink{{ID: "a", Paired: true, State: "connected"}},
		infoErr: &rpc.Error{Code: "ddi_not_mounted", Message: "x"}}, nil)
	if d := Status(ctx).Devices[0]; d.Connected || d.Ready || d.Note != iphoneNotes["ddi_not_mounted"] || d.Code != "ddi_not_mounted" {
		t.Fatalf("the probe failed: %+v", d)
	}
	useIPhones(t, &fakeIPhones{installed: true, links: []iphoneLink{{ID: "a", Paired: true, State: "connected"}},
		infoErr: errors.New("odd")}, nil)
	if d := Status(ctx).Devices[0]; d.Note != iphoneNoteFailed || d.Code != "failed" {
		t.Fatalf("an error without a code: %+v", d)
	}
}

// Every note is one the hub knows to translate: a code's, or one of the fixed ones.
func TestIPhoneNotesAreFixedText(t *testing.T) {
	for code, n := range iphoneNotes {
		if n == "" || strings.ContainsAny(n, "{}") {
			t.Errorf("%s: %q", code, n)
		}
	}
	if iphoneNote("something new") != iphoneNoteFailed {
		t.Fatal("an unknown code")
	}
}

func TestIPhoneCallLookGoesToTheCoreOnTheOnlyPairedDevice(t *testing.T) {
	f := &fakeIPhones{installed: true, links: []iphoneLink{{ID: "usb", USB: true}, {ID: "d1", Paired: true, State: "connected"}}}
	c := &fakeCore{ops: []string{"tap"}, out: iphoneOutcome{Output: json.RawMessage(`{"elements":[]}`), NeedsApproval: true, Approval: &proto.IPhoneApproval{Step: 1, Action: "tap", Needs: []string{"payment"}}, ApprovalID: "a1"}, shot: "full"}
	useIPhones(t, f, c)
	params := `{"session":"s1","op":"do","args":{"tap":3},"approved":"a0","confirm":true}`
	res, err := Call(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	r := res
	if string(r.Output) != `{"elements":[]}` || !r.NeedsApproval || r.Approval == nil || r.Approval.Needs[0] != "payment" || r.ApprovalID != "a1" {
		t.Fatalf("result %+v", r)
	}
	if r.Image == nil || r.Image.Mime == "" || r.Image.Data == "" || r.Image.Width != 1 {
		t.Fatalf("screenshot %+v", r.Image)
	}
	run := c.runs[0]
	if run.Device != "d1" || run.Session != "s1" || run.Op != "do" || string(run.Args) != `{"tap":3}` || run.Approved != "a0" || !run.Confirm {
		t.Fatalf("run %+v", run)
	}
	if _, err := os.Stat(run.TempDir); !os.IsNotExist(err) {
		t.Fatalf("the temporary directory stays: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0] != `d1 tap {"x":1,"y":2}` {
		t.Fatalf("the bridge on that device: %v", f.calls)
	}
	// A screenshot named by its file name alone is the one in the call's directory.
	c = &fakeCore{shot: "shot.png"}
	useIPhones(t, f, c)
	if res, err := Call(context.Background(), json.RawMessage(`{"device":"d1","op":"look"}`)); err != nil || res.Image == nil {
		t.Fatalf("by name: %+v %v", res, err)
	}
}

func TestIPhoneCallKeepsOnlyItsOwnScreenshot(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "elsewhere.png")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	useIPhones(t, &fakeIPhones{installed: true, links: []iphoneLink{{ID: "d1", Paired: true}}}, &fakeCore{out: iphoneOutcome{ImageFile: outside}})
	res, err := Call(context.Background(), json.RawMessage(`{"op":"look"}`))
	if err != nil || res.Image != nil {
		t.Fatalf("a file outside the call's directory is not read: %+v %v", res, err)
	}
}

func TestIPhoneCallStatusAnswersHere(t *testing.T) {
	f := &fakeIPhones{installed: true, links: []iphoneLink{{ID: "d1", Paired: true, State: "connected"}}, info: map[string]string{"d1": readyInfo}}
	c := &fakeCore{}
	useIPhones(t, f, c)
	res, err := Call(context.Background(), json.RawMessage(`{"op":"status"}`))
	if err != nil {
		t.Fatal(err)
	}
	var d proto.IPhoneDevice
	if json.Unmarshal(res.Output, &d) != nil || d.ID != "d1" || !d.Ready {
		t.Fatalf("status: %s", res.Output)
	}
	if len(c.runs) != 0 {
		t.Fatal("status is not the core's")
	}
}

func TestIPhoneCallRefusalsAreNotSent(t *testing.T) {
	ctx := context.Background()
	two := []iphoneLink{{ID: "a", Paired: true}, {ID: "b", Paired: true}}
	for _, c := range []struct {
		name   string
		phones *fakeIPhones
		core   *fakeCore
		params string
		code   string
	}{
		{"unknown op", &fakeIPhones{installed: true, links: two}, nil, `{"device":"a","op":"tap"}`, "bad_op"},
		{"args not an object", &fakeIPhones{installed: true, links: two}, nil, `{"device":"a","op":"do","args":[1]}`, "bad_args"},
		{"no bridge", &fakeIPhones{}, nil, `{"op":"look"}`, "bridge_missing"},
		{"none paired", &fakeIPhones{installed: true, links: []iphoneLink{{ID: "a", USB: true}}}, nil, `{"op":"look"}`, "not_paired"},
		{"not that one", &fakeIPhones{installed: true, links: two}, &fakeCore{ops: []string{"tap"}}, `{"device":"c","op":"do"}`, "not_paired"},
		{"looked, then refused", &fakeIPhones{installed: true, links: two}, &fakeCore{ops: []string{"screenshot"}, err: &rpc.Error{Code: "locked"}}, `{"device":"a","op":"do"}`, "locked"},
		{"which one", &fakeIPhones{installed: true, links: two}, nil, `{"op":"look"}`, "ambiguous_device"},
		{"bridge refused", &fakeIPhones{installed: true, links: two}, &fakeCore{err: &rpc.Error{Code: "locked", Message: "locked"}}, `{"device":"a","op":"do"}`, "locked"},
	} {
		core := c.core
		if core == nil {
			core = &fakeCore{}
		}
		useIPhones(t, c.phones, core)
		_, err := Call(ctx, json.RawMessage(c.params))
		if code, sent := iphoneCallErr(t, err); code != c.code || sent != proto.IPhoneNotSent {
			t.Errorf("%s: %s %s (%v)", c.name, code, sent, err)
		}
	}
}

func TestIPhoneCallFailuresMayHaveBeenSent(t *testing.T) {
	links := []iphoneLink{{ID: "a", Paired: true, State: "connected"}}
	for _, c := range []struct {
		name string
		err  error
		code string
		sent string
	}{
		{"link dropped", &rpc.Error{Code: "disconnected", Message: "link dropped\nmore"}, "disconnected", proto.IPhoneMaybeSent},
		{"plain error", errors.New("bridge crashed"), "failed", proto.IPhoneMaybeSent},
		{"the core says", &rpc.Error{Code: "partial", Message: "x", Data: json.RawMessage(`{"sent":"no"}`)}, "partial", proto.IPhoneNotSent},
		{"no code", &rpc.Error{Message: "x", Data: json.RawMessage(`{"sent":"unknown"}`)}, "failed", proto.IPhoneMaybeSent},
	} {
		useIPhones(t, &fakeIPhones{installed: true, links: links}, &fakeCore{err: c.err})
		_, err := Call(context.Background(), json.RawMessage(`{"op":"do"}`))
		if code, sent := iphoneCallErr(t, err); code != c.code || sent != c.sent || strings.Contains(err.Error(), "more") {
			t.Errorf("%s: %s %s %v", c.name, code, sent, err)
		}
	}

	// A tap went through, then a refusal: the step as a whole may have been done.
	useIPhones(t, &fakeIPhones{installed: true, links: links}, &fakeCore{ops: []string{"tap"}, err: &rpc.Error{Code: "locked", Data: json.RawMessage(`{"sent":"no"}`)}})
	_, err := Call(context.Background(), json.RawMessage(`{"op":"do"}`))
	if code, sent := iphoneCallErr(t, err); code != "locked" || sent != proto.IPhoneMaybeSent {
		t.Fatalf("after acting: %s %s", code, sent)
	}

	core := &fakeCore{block: true}
	useIPhones(t, &fakeIPhones{installed: true, links: links}, core)
	start := time.Now()
	_, err = Call(context.Background(), json.RawMessage(`{"op":"do","timeout_ms":1}`)) // clamped up to a second
	if code, sent := iphoneCallErr(t, err); code != "timeout" || sent != proto.IPhoneMaybeSent {
		t.Fatalf("timeout: %s %s %v", code, sent, err)
	}
	if took := time.Since(start); took < iphoneCallLeast || took > 5*time.Second {
		t.Fatalf("waited %v", took)
	}

	// A huge timeout is the most, not an overflow.
	core = &fakeCore{}
	useIPhones(t, &fakeIPhones{installed: true, links: links}, core)
	if _, err := Call(context.Background(), json.RawMessage(`{"op":"look","timeout_ms":9000000000000000}`)); err != nil || core.wait < iphoneCallMost-5*time.Second {
		t.Fatalf("waits %v (%v)", core.wait, err)
	}

	core = &fakeCore{block: true}
	useIPhones(t, &fakeIPhones{installed: true, links: links}, core)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err = Call(ctx, json.RawMessage(`{"op":"do"}`))
	if code, sent := iphoneCallErr(t, err); code != "cancelled" || sent != proto.IPhoneMaybeSent {
		t.Fatalf("the manager gave up: %s %s %v", code, sent, err)
	}
}

func TestIPhonePairStartsAJob(t *testing.T) {
	f := &fakeIPhones{installed: true}
	useIPhones(t, f, nil)
	out, err := Pair(context.Background(), "pair", json.RawMessage(`{"device":"u1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p := out; p.Job == nil || p.Job.Kind != "pair" || p.Job.Error != iphoneNotes["trust_pending"] || p.Job.Code != "trust_pending" {
		t.Fatalf("status %+v", p.Job)
	}
	// While it runs: the step in words the hub translates, what it waits for at the phone, the iPhone it is for.
	for waiting, step := range map[string]string{"": iphoneNotePairing, "trust_pending": iphoneNotePairTrust, "locked": iphoneNotePairUnlock} {
		f.job = &iphoneJob{Kind: "pair", State: "running", Step: "waiting for the phone", Waiting: waiting, Device: "u1"}
		if j := Status(context.Background()).Job; j.Step != step || j.Code != waiting || j.Device != "u1" || j.Error != "" {
			t.Fatalf("running, waiting %q: %+v", waiting, j)
		}
	}
	f.job = &iphoneJob{Kind: "pair", State: "failed", Error: "odd", Device: "u1"}
	if j := Status(context.Background()).Job; j.Code != "failed" || j.Error != "odd" {
		t.Fatalf("failed without a code: %+v", j)
	}
	if _, err := Pair(context.Background(), "forget", json.RawMessage(`{"device":"u1"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := Pair(context.Background(), "pair", json.RawMessage(`{}`)); err == nil {
		t.Fatal("pair without a device")
	}
	Cancel()
	if out := Status(context.Background()); out.Job != nil {
		t.Fatal("cancel kept the job")
	}
	if strings.Join(f.jobs, ",") != "pair u1,forget u1" {
		t.Fatalf("jobs %v", f.jobs)
	}
}

// TestIPhoneInstallStartsAJob: the app's Install starts the job with the release's build, and the status says each
// stage, and why it failed, in words the hub translates (#8199).
func TestIPhoneInstallStartsAJob(t *testing.T) {
	f := &fakeIPhones{}
	useIPhones(t, f, nil)
	out, err := Install(context.Background(), json.RawMessage(`{"urls":["https://example.com/dl/b.gz"],"size":3,"sha256":"x","binary_sha256":"y"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.Job == nil || out.Job.Kind != "install" || out.Job.Step != iphoneInstallSteps["download"] || out.Job.Code != "" {
		t.Fatalf("status %+v", out.Job)
	}
	if strings.Join(f.jobs, ",") != "install https://example.com/dl/b.gz" {
		t.Fatalf("jobs %v", f.jobs)
	}
	for stage, step := range iphoneInstallSteps {
		f.job = &iphoneJob{Kind: "install", State: "running", Stage: stage, Step: "english words"}
		if j := Status(context.Background()).Job; j.Step != step {
			t.Fatalf("stage %s: %+v", stage, j)
		}
	}
	for code, note := range iphoneInstallErrors {
		f.job = &iphoneJob{Kind: "install", State: "failed", ErrorCode: code, Error: "english words"}
		if j := Status(context.Background()).Job; j.Error != note || j.Code != code {
			t.Fatalf("failed with %s: %+v", code, j)
		}
	}
	if _, err := Install(context.Background(), json.RawMessage(`[]`)); err == nil {
		t.Fatal("unreadable params")
	}
}

// TestIPhonePrepareStartsAJob: Prepare starts the job for that iPhone; the status says it in words the hub translates
// and why it failed, and each iPhone says whether its owner allowed this computer to prepare it again (#8199).
func TestIPhonePrepareStartsAJob(t *testing.T) {
	f := &fakeIPhones{installed: true, links: []iphoneLink{{ID: "u1", Paired: true, State: "backoff", ErrorCode: "ddi_not_mounted", AutoPrepare: true}}}
	useIPhones(t, f, nil)
	out, err := Pair(context.Background(), "prepare", json.RawMessage(`{"device":"u1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.Job == nil || out.Job.Kind != "prepare" || out.Job.Step != iphonePrepareStep || out.Job.Device != "u1" {
		t.Fatalf("status %+v", out.Job)
	}
	if d := out.Devices[0]; !d.AutoPrepare || d.Code != "ddi_not_mounted" {
		t.Fatalf("device %+v", d)
	}
	for code, note := range iphonePrepareErrors {
		f.job = &iphoneJob{Kind: "prepare", State: "failed", ErrorCode: code, Error: "english words", Device: "u1"}
		if j := Status(context.Background()).Job; j.Error != note || j.Code != code {
			t.Fatalf("failed with %s: %+v", code, j)
		}
	}
	// A code the phone's notes word already: developer Mode off.
	f.job = &iphoneJob{Kind: "prepare", State: "failed", ErrorCode: "developer_mode_off", Error: "english words"}
	if j := Status(context.Background()).Job; j.Error != iphoneNotes["developer_mode_off"] {
		t.Fatalf("developer mode off: %+v", j)
	}
	if strings.Join(f.jobs, ",") != "prepare u1" {
		t.Fatalf("jobs %v", f.jobs)
	}
}
