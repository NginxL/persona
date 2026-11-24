package iphonectl

import (
	"fmt"
	"math"
	"strings"
	"unicode"
)

// Point is a position on the screen in points, in its current orientation, origin at the top left.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Rect is an element's frame in points.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Center is the middle of r, where an element is tapped.
func (r Rect) Center() Point { return Point{r.X + r.W/2, r.Y + r.H/2} }

// Contains says whether p is inside r (edges included).
func (r Rect) Contains(p Point) bool {
	return p.X >= r.X && p.X <= r.X+r.W && p.Y >= r.Y && p.Y <= r.Y+r.H
}

// Area is r's area; an empty or inverted frame has none.
func (r Rect) Area() float64 {
	if r.W <= 0 || r.H <= 0 {
		return 0
	}
	return r.W * r.H
}

// near says whether r and o are the same frame within tol points on every edge.
func (r Rect) near(o Rect, tol float64) bool {
	return math.Abs(r.X-o.X) <= tol && math.Abs(r.Y-o.Y) <= tol && math.Abs(r.W-o.W) <= tol && math.Abs(r.H-o.H) <= tol
}

func (r Rect) String() string {
	return fmt.Sprintf("(%s,%s %s×%s)", num(r.X), num(r.Y), num(r.W), num(r.H))
}

// num writes a coordinate without a needless fraction.
func num(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// App is the app in front; zero on the home screen or the lock screen when the bridge can't tell.
type App struct {
	BundleID string `json:"bundle_id,omitempty"`
	Name     string `json:"name,omitempty"`
}

func (a App) String() string {
	switch {
	case a.Name != "" && a.BundleID != "":
		return a.Name + " (" + a.BundleID + ")"
	case a.Name != "":
		return a.Name
	case a.BundleID != "":
		return a.BundleID
	}
	return "home screen or lock screen"
}

// Roles the rules and the results treat specially (the bridge's names, lower camel case).
const (
	RoleSecureTextField = "secureTextField"
	RoleKeyboardKey     = "keyboardKey"
	RoleCell            = "cell"
	RoleButton          = "button"
)

// Element is one thing on the screen, a node of the bridge's accessibility tree.
type Element struct {
	Role string `json:"role"`
	// Identifier is the app's accessibilityIdentifier, when it sets one: it makes an element's identity survive a
	// changed label.
	Identifier string `json:"identifier,omitempty"`
	Label      string `json:"label,omitempty"`
	Value      string `json:"value,omitempty"`
	Hint       string `json:"hint,omitempty"`
	Frame      Rect   `json:"frame"`
	Disabled   bool   `json:"disabled,omitempty"` // still listed, since it says what the screen shows
	Selected   bool   `json:"selected,omitempty"`
	Focused    bool   `json:"focused,omitempty"` // has the keyboard
}

// secure says whether what is typed into e is hidden: its value is never shown.
func (e Element) secure() bool { return e.Role == RoleSecureTextField }

// Image is a screenshot: its bytes, or the file the bridge wrote it to (Wire with a TempDir).
type Image struct {
	Mime   string `json:"mime"`
	Data   []byte `json:"data,omitempty"`
	File   string `json:"file,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// Size is the screen in points, in its current orientation.
type Size struct {
	W float64 `json:"w"`
	H float64 `json:"h"`
}

func (s Size) known() bool { return s.W > 0 && s.H > 0 }

func (s Size) center() Point { return Point{s.W / 2, s.H / 2} }

// DeviceInfo is the bridge's device_info: what the phone is and what can be done with it now. Locked is nil when it
// can't be read.
type DeviceInfo struct {
	Name         string            `json:"name,omitempty"`
	ProductType  string            `json:"product_type,omitempty"`
	OSVersion    string            `json:"os_version,omitempty"`
	Size         Size              `json:"size"`
	Orientation  string            `json:"orientation,omitempty"`
	Locked       *bool             `json:"locked,omitempty"`
	Capabilities map[string]bool   `json:"capabilities,omitempty"` // screenshot, ax_tree, input, apps
	Notes        map[string]string `json:"notes,omitempty"`        // why a capability is false (an error code)
}

func (d DeviceInfo) locked() bool { return d.Locked != nil && *d.Locked }

// can says whether a capability is not known to be off.
func (d DeviceInfo) can(capability string) bool {
	on, ok := d.Capabilities[capability]
	return !ok || on
}

// Node is a node of the bridge's ax_tree. Its id means nothing beyond the one read and is not kept.
type Node struct {
	Role       string  `json:"role"`
	Label      string  `json:"label,omitempty"`
	Value      string  `json:"value,omitempty"`
	Identifier string  `json:"identifier,omitempty"`
	Hint       string  `json:"hint,omitempty"`
	Frame      Rect    `json:"frame"`
	Enabled    *bool   `json:"enabled,omitempty"`
	Selected   bool    `json:"selected,omitempty"`
	Focused    bool    `json:"focused,omitempty"`
	Children   []*Node `json:"children,omitempty"`
}

// Tree is the bridge's ax_tree answer.
type Tree struct {
	App       App   `json:"app"`
	Size      Size  `json:"size"`
	Truncated bool  `json:"truncated,omitempty"`
	Root      *Node `json:"root"`
}

// hiddenValue stands for a secure field's value.
const hiddenValue = "(hidden)"

// containers are roles that only group others: left out unless they say something themselves.
var containers = map[string]bool{"application": true, "window": true, "other": true, "group": true,
	"scrollView": true, "table": true, "collectionView": true, "layoutArea": true}

// Elements flattens the tree into the things on screen, depth first: containers without a label, value or
// identifier are left out, their children kept.
func (t Tree) Elements() []Element {
	var out []Element
	var walk func(n *Node)
	walk = func(n *Node) {
		if n == nil {
			return
		}
		label, value := strings.TrimSpace(n.Label), strings.TrimSpace(n.Value)
		if n.Role == RoleSecureTextField && value != "" {
			// What was typed into a password field goes nowhere, the model's data included.
			value = hiddenValue
		}
		if !(containers[n.Role] && label == "" && value == "" && n.Identifier == "") && n.Role != "application" {
			out = append(out, Element{Role: n.Role, Identifier: n.Identifier, Label: label, Value: value, Hint: n.Hint,
				Frame: n.Frame, Disabled: n.Enabled != nil && !*n.Enabled, Selected: n.Selected, Focused: n.Focused})
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(t.Root)
	return out
}

// words writes a lower camel case role as words: staticText → static text.
func words(role string) string {
	var b strings.Builder
	for i, r := range role {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
