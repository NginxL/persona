package iphonectl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBridge is a phone in memory: AXTree gives the screens queued in turn (the last one stays), inputs are logged,
// and any op can be made slow, failing or hanging (ignoring its context, as a stuck bridge would).
type fakeBridge struct {
	mu      sync.Mutex
	screens []Tree
	info    DeviceInfo
	log     []string
	delay   map[string]time.Duration
	hang    map[string]bool
	errs    map[string][]error // returned in turn, one per call
	release chan struct{}      // closed at the end of a test, so hanging calls end
	started chan string        // when set, hears each op as it starts
	// screen is what the screenshots show: it changes with every input unless inert.
	screen int
	inert  bool
}

func newFake(t *testing.T, screens ...Tree) *fakeBridge {
	f := &fakeBridge{screens: screens, info: DeviceInfo{Name: "Test iPhone", Size: screenSize},
		delay: map[string]time.Duration{}, hang: map[string]bool{}, errs: map[string][]error{}, release: make(chan struct{})}
	t.Cleanup(func() { close(f.release) })
	return f
}

// show queues screens for the following reads; set replaces the screen from now on.
func (f *fakeBridge) show(s ...Tree) {
	f.mu.Lock()
	f.screens = append(f.screens, s...)
	f.mu.Unlock()
}

func (f *fakeBridge) set(s Tree) {
	f.mu.Lock()
	f.screens = []Tree{s}
	f.mu.Unlock()
}

func (f *fakeBridge) fail(op string, errs ...error) {
	f.mu.Lock()
	f.errs[op] = append(f.errs[op], errs...)
	f.mu.Unlock()
}

func (f *fakeBridge) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fakeBridge) count(op string) int {
	n := 0
	for _, c := range f.calls() {
		if c == op || strings.HasPrefix(c, op+" ") {
			n++
		}
	}
	return n
}

func (f *fakeBridge) op(ctx context.Context, name, entry string) error {
	f.mu.Lock()
	f.log = append(f.log, entry)
	d, hang, started := f.delay[name], f.hang[name], f.started
	var err error
	if errs := f.errs[name]; len(errs) > 0 {
		err, f.errs[name] = errs[0], errs[1:]
	}
	f.mu.Unlock()
	if started != nil {
		started <- name
	}
	if hang {
		<-f.release // ignores ctx on purpose
		return nil
	}
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (f *fakeBridge) DeviceInfo(ctx context.Context, _ string) (DeviceInfo, error) {
	err := f.op(ctx, "device_info", "device_info")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info, err
}

func (f *fakeBridge) Screenshot(ctx context.Context, _ string) (Image, error) {
	err := f.op(ctx, "screenshot", "screenshot")
	f.mu.Lock()
	n := f.screen
	f.mu.Unlock()
	return Image{Mime: "image/png", Data: screenPNG(n), Width: 4, Height: 8}, err
}

// screenPNG is a screen that looks different for every n.
func screenPNG(n int) []byte {
	m := image.NewGray(image.Rect(0, 0, 48, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 48; x++ {
			m.SetGray(x, y, color.Gray{Y: uint8((x*7 + y*3 + n*40) % 256)})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, m)
	return b.Bytes()
}

// input notes an input reaching the screen.
func (f *fakeBridge) input(ctx context.Context, name, entry string) error {
	err := f.op(ctx, name, entry)
	f.mu.Lock()
	if !f.inert && err == nil {
		f.screen++
	}
	f.mu.Unlock()
	return err
}

func (f *fakeBridge) AXTree(ctx context.Context, _ string) (Tree, error) {
	if err := f.op(ctx, "ax_tree", "ax_tree"); err != nil {
		return Tree{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.screens) == 0 {
		return Tree{}, nil
	}
	s := f.screens[0]
	if len(f.screens) > 1 {
		f.screens = f.screens[1:]
	}
	return s, nil
}

func (f *fakeBridge) ListApps(ctx context.Context, _ string) ([]AppInfo, error) {
	return nil, f.op(ctx, "list_apps", "list_apps")
}

func (f *fakeBridge) Tap(ctx context.Context, _ string, p Point, count int) error {
	return f.input(ctx, "tap", fmt.Sprintf("tap %s,%s ×%d", num(p.X), num(p.Y), count))
}

func (f *fakeBridge) LongPress(ctx context.Context, _ string, p Point, d time.Duration) error {
	return f.input(ctx, "long_press", fmt.Sprintf("long_press %s,%s %s", num(p.X), num(p.Y), d))
}

func (f *fakeBridge) Drag(ctx context.Context, _ string, a, b Point, d, hold time.Duration) error {
	return f.input(ctx, "drag", fmt.Sprintf("drag %s,%s → %s,%s %s %s", num(a.X), num(a.Y), num(b.X), num(b.Y), d, hold))
}

func (f *fakeBridge) Swipe(ctx context.Context, _ string, a, b Point, d time.Duration) error {
	return f.input(ctx, "swipe", fmt.Sprintf("swipe %s,%s → %s,%s %s", num(a.X), num(a.Y), num(b.X), num(b.Y), d))
}

func (f *fakeBridge) Keys(ctx context.Context, _ string, text string) error {
	return f.input(ctx, "keys", "keys "+text)
}

func (f *fakeBridge) Key(ctx context.Context, _ string, key string, repeat int) error {
	return f.input(ctx, "key", fmt.Sprintf("key %s ×%d", key, repeat))
}

func (f *fakeBridge) Button(ctx context.Context, _ string, name string, d time.Duration) error {
	return f.input(ctx, "button", "button "+name)
}

func (f *fakeBridge) LaunchApp(ctx context.Context, _ string, bundleID string) error {
	return f.input(ctx, "launch_app", "launch_app "+bundleID)
}

func (f *fakeBridge) TerminateApp(ctx context.Context, _ string, bundleID string) error {
	return f.input(ctx, "terminate_app", "terminate_app "+bundleID)
}

// Screens used across tests: placeholder apps and labels only.

func el(role, label string, x, y, w, h float64) Element {
	return Element{Role: role, Label: label, Frame: Rect{x, y, w, h}}
}

var (
	exampleApp = App{BundleID: "com.example.notes", Name: "Notes"}
	otherApp   = App{BundleID: "com.example.other", Name: "Other"}
	smsApp     = App{BundleID: "com.apple.MobileSMS", Name: "Messages"}
	selfApp    = App{BundleID: "com.example.persona", Name: "Persona"}
	screenSize = Size{W: 390, H: 844}
)

// tree is an ax_tree answer: elements under the application node, inside a container that says nothing.
func tree(app App, els ...Element) Tree {
	box := &Node{Role: "other", Frame: Rect{0, 0, screenSize.W, screenSize.H}}
	for _, e := range els {
		var enabled *bool
		if e.Disabled {
			f := false
			enabled = &f
		}
		box.Children = append(box.Children, &Node{Role: e.Role, Label: e.Label, Value: e.Value, Identifier: e.Identifier,
			Frame: e.Frame, Enabled: enabled, Selected: e.Selected, Focused: e.Focused})
	}
	return Tree{App: app, Size: screenSize, Root: &Node{Role: "application", Label: app.Name, Children: []*Node{box}}}
}

func notesElements() []Element {
	return []Element{
		el("button", "Back", 8, 52, 44, 44),
		el("heading", "Shopping", 140, 60, 110, 30),
		el("cell", "Milk", 0, 120, 390, 44),
		el("cell", "Bread", 0, 164, 390, 44),
		el("button", "Delete", 300, 760, 80, 44),
	}
}

func notes() Tree { return tree(exampleApp, notesElements()...) }

// fastLimits make limits tests can wait out.
var fastLimits = Limits{Primitive: 60 * time.Millisecond, Action: 150 * time.Millisecond, Call: 500 * time.Millisecond,
	Busy: 30 * time.Millisecond, Pending: time.Minute, Settle: time.Millisecond}

var testRules = Rules{SelfApps: []string{selfApp.BundleID}}

// harness drives one controller and one fake phone as the node would.
type harness struct {
	t *testing.T
	c *Controller
	f *fakeBridge
}

func newHarness(t *testing.T, o Options, screens ...Tree) *harness {
	if o.Limits == (Limits{}) {
		o.Limits = fastLimits
	}
	if o.Rules.SelfApps == nil {
		o.Rules = testRules
	}
	return &harness{t: t, c: NewController(o), f: newFake(t, screens...)}
}

func (h *harness) handle(req Request) Response {
	if req.Device == "" {
		req.Device = "phone-1"
	}
	if req.Session == "" {
		req.Session = "chat-1"
	}
	return h.c.Handle(context.Background(), h.f, req)
}

func (h *harness) look() (Response, LookResult) {
	resp := h.handle(Request{Op: OpLook})
	var r LookResult
	if err := json.Unmarshal(resp.Output, &r); err != nil {
		h.t.Fatalf("look output: %v", err)
	}
	return resp, r
}

func (h *harness) do(args string, opts ...func(*Request)) (Response, DoResult) {
	req := Request{Op: OpDo, Args: json.RawMessage(args)}
	for _, o := range opts {
		o(&req)
	}
	resp := h.handle(req)
	var r DoResult
	if err := json.Unmarshal(resp.Output, &r); err != nil {
		h.t.Fatalf("do output %s: %v", resp.Output, err)
	}
	return resp, r
}

func approved(id string) func(*Request) { return func(r *Request) { r.Approved = id } }

func withConfirm(r *Request) { r.Confirm = true }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
