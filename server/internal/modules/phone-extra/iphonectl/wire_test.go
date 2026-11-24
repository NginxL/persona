package iphonectl

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// wireFake answers the bridge's wire with the shapes of its contract.
type wireFake struct {
	sent    []string
	answers map[string]string
	errs    map[string]error
}

func (f *wireFake) call(_ context.Context, op string, args json.RawMessage) (json.RawMessage, error) {
	f.sent = append(f.sent, op+" "+string(args))
	if err := f.errs[op]; err != nil {
		return nil, err
	}
	if a, ok := f.answers[op]; ok {
		return json.RawMessage(a), nil
	}
	return json.RawMessage(`{}`), nil
}

const wireTree = `{
  "app": {"bundle_id": "com.apple.Preferences", "name": "Settings", "pid": 412},
  "orientation": "portrait", "point_width": 430, "point_height": 932, "truncated": false,
  "root": {
    "id": "e1", "role": "application", "label": "Settings", "frame": [0, 0, 430, 932],
    "children": [
      {"id": "e2", "role": "button", "label": "Wi-Fi", "value": "Home", "frame": [16, 210, 398, 52], "enabled": true, "children": []},
      {"id": "e3", "role": "other", "frame": [0, 300, 430, 400], "children": [
        {"id": "e4", "role": "switch", "label": "Airplane Mode", "value": "0", "frame": [16, 310, 398, 52], "enabled": false}
      ]}
    ]
  }
}`

const wireInfo = `{"udid": "<udid>", "name": "Test iPhone", "product_type": "iPhone16,2", "os_version": "18.1", "build": "22B83",
  "point_width": 430, "point_height": 932, "scale": 3, "orientation": "portrait", "locked": false, "developer_mode": true,
  "ddi_mounted": true, "battery": {"level": 0.81, "charging": false},
  "capabilities": {"screenshot": true, "ax_tree": true, "input": true, "apps": true}}`

func TestWireReadsTheBridgesAnswers(t *testing.T) {
	f := &wireFake{answers: map[string]string{"ax_tree": wireTree, "device_info": wireInfo}}
	w := &Wire{Call: f.call}
	tr, err := w.AXTree(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if tr.App.BundleID != "com.apple.Preferences" || tr.Size != (Size{430, 932}) {
		t.Fatalf("%+v", tr)
	}
	els := tr.Elements()
	if len(els) != 2 || els[0].Label != "Wi-Fi" || els[0].Value != "Home" || els[0].Frame != (Rect{16, 210, 398, 52}) ||
		els[1].Role != "switch" || !els[1].Disabled {
		t.Fatalf("%+v", els)
	}
	info, err := w.DeviceInfo(context.Background(), "")
	if err != nil || info.Name != "Test iPhone" || info.locked() || !info.can("input") || info.Size != (Size{430, 932}) {
		t.Fatalf("%+v %v", info, err)
	}

	f.answers["ax_tree"] = `{"app": null, "point_width": 430, "point_height": 932, "root": {"role": "application", "frame": [0,0,430,932]}}`
	if tr, err := w.AXTree(context.Background(), ""); err != nil || tr.App != (App{}) {
		t.Fatalf("the home screen: %+v %v", tr.App, err)
	}
	f.answers["ax_tree"] = `{"root": {"role": "application", "frame": null, "children": [{"role": "button", "label": "OK", "frame": []}]}}`
	if tr, err := w.AXTree(context.Background(), ""); err != nil || len(tr.Elements()) != 1 {
		t.Fatalf("frames that are null or empty: %+v %v", tr, err)
	}
	f.answers["ax_tree"] = `{"root": {"role": "button", "frame": [1, 2]}}`
	if _, err := w.AXTree(context.Background(), ""); err == nil {
		t.Fatal("a frame of two numbers")
	}
}

func TestWireSendsTheBridgesArguments(t *testing.T) {
	f := &wireFake{answers: map[string]string{"screenshot": `{"mime": "image/jpeg", "width": 1290, "height": 2796}`}}
	dir := t.TempDir()
	w := &Wire{Call: f.call, TempDir: dir}
	ctx := context.Background()
	_ = w.Tap(ctx, "", Point{120, 640}, 2)
	_ = w.LongPress(ctx, "", Point{1, 2}, LongPress)
	_ = w.Drag(ctx, "", Point{200, 700}, Point{200, 200}, DragTime, 0)
	_ = w.Swipe(ctx, "", Point{215, 466}, Point{215, 66}, SwipeTime)
	_ = w.Keys(ctx, "", "hello")
	_ = w.Key(ctx, "", "return", 1)
	_ = w.Button(ctx, "", "home", 0)
	_ = w.LaunchApp(ctx, "", "com.apple.Preferences")
	_ = w.TerminateApp(ctx, "", "com.apple.Preferences")
	img, err := w.Screenshot(ctx, "")
	if err != nil || filepath.Dir(img.File) != dir || !strings.HasPrefix(filepath.Base(img.File), "screenshot-") || img.Width != 1290 || len(img.Data) != 0 {
		t.Fatalf("%+v %v", img, err)
	}
	if other, _ := w.Screenshot(ctx, ""); other.File == img.File {
		t.Fatal("two screenshots share a file")
	}
	f.sent = f.sent[:len(f.sent)-1]
	f.sent[len(f.sent)-1] = strings.Replace(f.sent[len(f.sent)-1], img.File, "<file>", 1)
	want := []string{
		`tap {"count":2,"x":120,"y":640}`,
		`long_press {"duration_ms":800,"x":1,"y":2}`,
		`drag {"duration_ms":500,"from":{"x":200,"y":700},"hold_ms":0,"to":{"x":200,"y":200}}`,
		`swipe {"duration_ms":300,"from":{"x":215,"y":466},"to":{"x":215,"y":66}}`,
		`keys {"text":"hello"}`,
		`keys {"key":"return","repeat":1}`,
		`button {"duration_ms":0,"name":"home"}`,
		`launch_app {"bundle_id":"com.apple.Preferences"}`,
		`terminate_app {"bundle_id":"com.apple.Preferences"}`,
		`screenshot {"format":"jpeg","out_file":"<file>","quality":85}`,
	}
	if strings.Join(f.sent, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sent\n%s\nwant\n%s", strings.Join(f.sent, "\n"), strings.Join(want, "\n"))
	}
}

func TestAControllerOverTheWire(t *testing.T) {
	f := &wireFake{answers: map[string]string{"ax_tree": wireTree, "device_info": wireInfo,
		"screenshot": `{"mime": "image/jpeg", "data": "/9g=", "width": 1290, "height": 2796}`},
		errs: map[string]error{"tap": &Error{Code: CodeUnsupported, Message: "input is not available on this iOS"}}}
	c := NewController(Options{Limits: fastLimits})
	w := &Wire{Call: f.call}
	look := c.Handle(context.Background(), w, Request{Device: "phone-1", Session: "chat-1", Op: OpLook})
	if !strings.Contains(look.Text, `[1] button "Wi-Fi" = "Home" (16,210 398×52)`) || look.Image == nil || len(look.Image.Data) != 2 {
		t.Fatalf("%s", look.Text)
	}
	// Input not possible: the do says so, the look above still answered.
	do := c.Handle(context.Background(), w, Request{Device: "phone-1", Session: "chat-1", Op: OpDo,
		Args: json.RawMessage(`{"steps":[{"action":"tap","target":{"n":1}}]}`)})
	if !strings.Contains(do.Text, "not_sent") || !strings.Contains(do.Text, "unsupported: input is not available on this iOS") {
		t.Fatalf("%s", do.Text)
	}
}
