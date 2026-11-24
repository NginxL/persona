package iphonectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// Wire is a Bridge over the device bridge's JSON wire (docs/extensions/iphone-control.md, "The core's interface"):
// Call sends one operation to the phone it is bound to and returns its result, or its error as an *Error with the
// bridge's code. The node supplies Call; the device argument of the Bridge methods is not used, since Call already
// knows the phone.
type Wire struct {
	Call func(ctx context.Context, op string, args json.RawMessage) (json.RawMessage, error)
	// TempDir, when set, is where screenshots are written (the bridge's out_file) instead of coming back inline;
	// the caller removes it after the call.
	TempDir string
}

var _ Bridge = (*Wire)(nil)

func (w *Wire) call(ctx context.Context, op string, args any, result any) error {
	raw := json.RawMessage("{}")
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return &Error{Code: CodeBadRequest, Message: err.Error()}
		}
		raw = b
	}
	out, err := w.Call(ctx, op, raw)
	if err != nil {
		return err
	}
	if result == nil || len(out) == 0 {
		return nil
	}
	if err := json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("the bridge's %s answer: %w", op, err)
	}
	return nil
}

type wirePoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

func wp(p Point) wirePoint { return wirePoint{p.X, p.Y} }

func ms(d time.Duration) int64 { return d.Milliseconds() }

// wireFrame is a frame as the bridge writes it: [x, y, width, height].
type wireFrame Rect

func (f *wireFrame) UnmarshalJSON(b []byte) error {
	var v []float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if len(v) == 0 { // null or [], a node with no frame
		*f = wireFrame{}
		return nil
	}
	if len(v) != 4 {
		return errors.New("a frame is [x, y, width, height]")
	}
	*f = wireFrame{v[0], v[1], v[2], v[3]}
	return nil
}

type wireNode struct {
	Role       string      `json:"role"`
	Label      string      `json:"label"`
	Value      string      `json:"value"`
	Identifier string      `json:"identifier"`
	Hint       string      `json:"hint"`
	Frame      wireFrame   `json:"frame"`
	Enabled    *bool       `json:"enabled"`
	Selected   bool        `json:"selected"`
	Focused    bool        `json:"focused"`
	Children   []*wireNode `json:"children"`
}

func (n *wireNode) node() *Node {
	if n == nil {
		return nil
	}
	out := &Node{Role: n.Role, Label: n.Label, Value: n.Value, Identifier: n.Identifier, Hint: n.Hint,
		Frame: Rect(n.Frame), Enabled: n.Enabled, Selected: n.Selected, Focused: n.Focused}
	for _, c := range n.Children {
		out.Children = append(out.Children, c.node())
	}
	return out
}

func (w *Wire) DeviceInfo(ctx context.Context, _ string) (DeviceInfo, error) {
	var r struct {
		Name         string            `json:"name"`
		ProductType  string            `json:"product_type"`
		OSVersion    string            `json:"os_version"`
		PointWidth   float64           `json:"point_width"`
		PointHeight  float64           `json:"point_height"`
		Orientation  string            `json:"orientation"`
		Locked       *bool             `json:"locked"`
		Capabilities map[string]bool   `json:"capabilities"`
		Notes        map[string]string `json:"notes"`
	}
	if err := w.call(ctx, "device_info", nil, &r); err != nil {
		return DeviceInfo{}, err
	}
	return DeviceInfo{Name: r.Name, ProductType: r.ProductType, OSVersion: r.OSVersion,
		Size: Size{r.PointWidth, r.PointHeight}, Orientation: r.Orientation, Locked: r.Locked,
		Capabilities: r.Capabilities, Notes: r.Notes}, nil
}

func (w *Wire) Screenshot(ctx context.Context, _ string) (Image, error) {
	args := map[string]any{"format": "jpeg", "quality": 85}
	file := ""
	if w.TempDir != "" {
		// A name of its own, so phones and calls sharing the directory never write over each other's.
		f, err := os.CreateTemp(w.TempDir, "screenshot-*.jpg")
		if err != nil {
			return Image{}, fmt.Errorf("a file for the screenshot: %w", err)
		}
		file = f.Name()
		_ = f.Close()
		args["out_file"] = file
	}
	var r struct {
		Mime   string `json:"mime"`
		Data   []byte `json:"data"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	if err := w.call(ctx, "screenshot", args, &r); err != nil {
		return Image{}, err
	}
	img := Image{Mime: r.Mime, Data: r.Data, Width: r.Width, Height: r.Height}
	if file != "" && len(r.Data) == 0 {
		img.File = file
	}
	return img, nil
}

func (w *Wire) AXTree(ctx context.Context, _ string) (Tree, error) {
	var r struct {
		App *struct {
			BundleID string `json:"bundle_id"`
			Name     string `json:"name"`
		} `json:"app"`
		PointWidth  float64   `json:"point_width"`
		PointHeight float64   `json:"point_height"`
		Truncated   bool      `json:"truncated"`
		Root        *wireNode `json:"root"`
	}
	if err := w.call(ctx, "ax_tree", nil, &r); err != nil {
		return Tree{}, err
	}
	t := Tree{Size: Size{r.PointWidth, r.PointHeight}, Truncated: r.Truncated, Root: r.Root.node()}
	if r.App != nil {
		t.App = App{BundleID: r.App.BundleID, Name: r.App.Name}
	}
	return t, nil
}

func (w *Wire) ListApps(ctx context.Context, _ string) ([]AppInfo, error) {
	var r struct {
		Apps []AppInfo `json:"apps"`
	}
	err := w.call(ctx, "list_apps", map[string]any{"include_system": true}, &r)
	return r.Apps, err
}

func (w *Wire) Tap(ctx context.Context, _ string, p Point, count int) error {
	return w.call(ctx, "tap", map[string]any{"x": p.X, "y": p.Y, "count": count}, nil)
}

func (w *Wire) LongPress(ctx context.Context, _ string, p Point, d time.Duration) error {
	return w.call(ctx, "long_press", map[string]any{"x": p.X, "y": p.Y, "duration_ms": ms(d)}, nil)
}

func (w *Wire) Drag(ctx context.Context, _ string, from, to Point, d, hold time.Duration) error {
	return w.call(ctx, "drag", map[string]any{"from": wp(from), "to": wp(to), "duration_ms": ms(d), "hold_ms": ms(hold)}, nil)
}

func (w *Wire) Swipe(ctx context.Context, _ string, from, to Point, d time.Duration) error {
	return w.call(ctx, "swipe", map[string]any{"from": wp(from), "to": wp(to), "duration_ms": ms(d)}, nil)
}

func (w *Wire) Keys(ctx context.Context, _ string, text string) error {
	return w.call(ctx, "keys", map[string]any{"text": text}, nil)
}

func (w *Wire) Key(ctx context.Context, _ string, key string, repeat int) error {
	return w.call(ctx, "keys", map[string]any{"key": key, "repeat": repeat}, nil)
}

func (w *Wire) Button(ctx context.Context, _ string, name string, d time.Duration) error {
	return w.call(ctx, "button", map[string]any{"name": name, "duration_ms": ms(d)}, nil)
}

func (w *Wire) LaunchApp(ctx context.Context, _ string, bundleID string) error {
	return w.call(ctx, "launch_app", map[string]any{"bundle_id": bundleID}, nil)
}

func (w *Wire) TerminateApp(ctx context.Context, _ string, bundleID string) error {
	return w.call(ctx, "terminate_app", map[string]any{"bundle_id": bundleID}, nil)
}
