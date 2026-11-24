package iphonectl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Actions a step can take.
const (
	ActTap       = "tap"
	ActLongPress = "long_press"
	ActDrag      = "drag"
	ActSwipe     = "swipe"
	ActType      = "type"
	ActKey       = "key"
	ActButton    = "button"
	ActLaunch    = "launch"
	ActTerminate = "terminate"
	ActWait      = "wait"
)

// keys are the keyboard keys the bridge can press.
var keys = []string{"return", "backspace", "delete", "tab", "escape", "space", "up", "down", "left", "right"}

var actions = []string{ActTap, ActLongPress, ActDrag, ActSwipe, ActType, ActKey, ActButton, ActLaunch, ActTerminate, ActWait}

// Step is one thing for do to do. It has no field that approves anything: the owner's approval comes only with the
// call that resumes a pending step (Request.Approved, which the hub sets after asking the owner), never from the steps
// the model writes; ParseSteps rejects unknown fields.
type Step struct {
	Action string `json:"action"`
	// Target is what tap, long_press and drag act on; type taps it first; swipe starts on it.
	Target *Target `json:"target,omitempty"`
	To     *Target `json:"to,omitempty"` // drag, swipe: where it ends
	// Direction is a swipe without To: up, down, left or right (the way the finger moves), by Distance points
	// (default half the screen along that axis).
	Direction string  `json:"direction,omitempty"`
	Distance  float64 `json:"distance,omitempty"`
	Count     int     `json:"count,omitempty"`   // tap: 2 for a double tap
	Text      string  `json:"text,omitempty"`    // type
	Key       string  `json:"key,omitempty"`     // key: return, backspace, tab, escape, space
	Repeat    int     `json:"repeat,omitempty"`  // key
	Button    string  `json:"button,omitempty"`  // button: home, lock, volume_up, volume_down
	App       string  `json:"app,omitempty"`     // launch, terminate: bundle id
	Ms        int     `json:"ms,omitempty"`      // wait; long_press, drag, swipe, button: how long
	HoldMs    int     `json:"hold_ms,omitempty"` // drag: hold at the start before moving
}

// Target is what a step acts on: an element by its number from the latest read, an element by label (and role), or
// a point on the screen.
type Target struct {
	N     int      `json:"n,omitempty"`
	Label string   `json:"label,omitempty"`
	Role  string   `json:"role,omitempty"`
	X     *float64 `json:"x,omitempty"`
	Y     *float64 `json:"y,omitempty"`
}

func (t *Target) point() (Point, bool) {
	if t == nil || t.X == nil || t.Y == nil {
		return Point{}, false
	}
	return Point{*t.X, *t.Y}, true
}

// byNumber says the target is an element number, which only means something under the numbering it was read in.
func (t *Target) byNumber() bool { return t != nil && t.N > 0 }

// named says the target is an element (by number or label), which needs the element tree.
func (t *Target) named() bool { return t != nil && (t.N > 0 || t.Label != "") }

// Bounds on a do, and the fixed timings the input is sent with: the same steps always send the same input.
const (
	MaxSteps    = 50
	MaxTextLen  = 2000 // runes per type step
	MaxRepeat   = 100
	MaxWait     = 10 * time.Second
	MaxGesture  = 5 * time.Second
	LongPress   = 800 * time.Millisecond
	SwipeTime   = 300 * time.Millisecond
	DragTime    = 500 * time.Millisecond
	FocusSettle = 400 * time.Millisecond // after the tap that focuses a field, before typing
)

// ParseSteps reads a do's steps: exactly one JSON array, no unknown fields, so nothing the model writes can pass
// for an approval.
func ParseSteps(raw json.RawMessage) ([]Step, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var steps []Step
	if err := dec.Decode(&steps); err != nil {
		return nil, fmt.Errorf("steps: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("steps: one JSON array and nothing after it")
	}
	if err := ValidateSteps(steps); err != nil {
		return nil, err
	}
	return steps, nil
}

// ValidateSteps checks steps before any is run, so a bad step later on doesn't leave the earlier ones half done.
func ValidateSteps(steps []Step) error {
	if len(steps) == 0 {
		return errors.New("no steps")
	}
	if len(steps) > MaxSteps {
		return fmt.Errorf("%d steps, at most %d per call", len(steps), MaxSteps)
	}
	for i, s := range steps {
		if err := s.validate(); err != nil {
			return fmt.Errorf("step %d (%s): %w", i+1, s.Action, err)
		}
	}
	return nil
}

func msWithin(ms int, max time.Duration, name string) error {
	if ms < 0 || time.Duration(ms)*time.Millisecond > max {
		return fmt.Errorf("%s is 0 to %d", name, max.Milliseconds())
	}
	return nil
}

func (s Step) validate() error {
	target := func(t *Target, what string) error {
		if t == nil {
			return errors.New("needs " + what)
		}
		return t.validate()
	}
	switch s.Action {
	case ActTap:
		if s.Count < 0 || s.Count > 3 {
			return errors.New("count is 1 to 3 (2 for a double tap)")
		}
		return target(s.Target, "a target")
	case ActLongPress:
		if err := msWithin(s.Ms, MaxGesture, "ms"); err != nil {
			return err
		}
		return target(s.Target, "a target")
	case ActDrag:
		if err := errors.Join(msWithin(s.Ms, MaxGesture, "ms"), msWithin(s.HoldMs, MaxGesture, "hold_ms")); err != nil {
			return err
		}
		if err := target(s.Target, "a target"); err != nil {
			return err
		}
		return target(s.To, "to")
	case ActSwipe:
		if err := msWithin(s.Ms, MaxGesture, "ms"); err != nil {
			return err
		}
		if s.Distance < 0 {
			return errors.New("distance is in points, not negative")
		}
		if s.Target != nil {
			if err := s.Target.validate(); err != nil {
				return err
			}
		}
		switch {
		case s.To != nil && s.Direction != "":
			return errors.New("give to or direction, not both")
		case s.To != nil:
			return s.To.validate()
		case !validDirection(s.Direction):
			return errors.New("needs to, or direction up, down, left or right")
		}
	case ActType:
		if s.Text == "" {
			return errors.New("needs text")
		}
		if n := utf8.RuneCountInString(s.Text); n > MaxTextLen {
			return fmt.Errorf("%d characters, at most %d per step", n, MaxTextLen)
		}
		if s.Target != nil {
			return s.Target.validate()
		}
	case ActKey:
		if !slices.Contains(keys, s.Key) {
			return errors.New("key is " + strings.Join(keys, ", "))
		}
		if s.Repeat < 0 || s.Repeat > MaxRepeat {
			return fmt.Errorf("repeat is 1 to %d", MaxRepeat)
		}
	case ActButton:
		switch s.Button {
		case "home", "lock", "volume_up", "volume_down":
		default:
			return errors.New("button is home, lock, volume_up or volume_down")
		}
		return msWithin(s.Ms, MaxGesture, "ms")
	case ActLaunch, ActTerminate:
		if strings.TrimSpace(s.App) == "" {
			return errors.New("needs an app's bundle id")
		}
	case ActWait:
		if s.Ms <= 0 || time.Duration(s.Ms)*time.Millisecond > MaxWait {
			return fmt.Errorf("ms is 1 to %d", MaxWait.Milliseconds())
		}
	case "":
		return errors.New("no action")
	default:
		return fmt.Errorf("unknown action (%s)", strings.Join(actions, ", "))
	}
	return nil
}

func (t *Target) validate() error {
	kinds := 0
	if t.N != 0 {
		kinds++
		if t.N < 0 {
			return errors.New("element numbers start at 1")
		}
	}
	if t.Label != "" {
		kinds++
	}
	if t.X != nil || t.Y != nil {
		kinds++
		if t.X == nil || t.Y == nil {
			return errors.New("a point needs x and y")
		}
	}
	switch {
	case kinds == 0:
		return errors.New("the target needs n, label, or x and y")
	case kinds > 1:
		return errors.New("the target is one of n, label, or x and y")
	case t.Role != "" && t.Label == "":
		return errors.New("role goes with label")
	}
	return nil
}

func validDirection(d string) bool { return d == "up" || d == "down" || d == "left" || d == "right" }

// usesNumbers says whether any step names an element by number.
func usesNumbers(steps []Step) bool {
	for _, s := range steps {
		if s.Target.byNumber() || s.To.byNumber() {
			return true
		}
	}
	return false
}

// input says whether the action sends anything to the phone.
func input(action string) bool { return action != ActWait }

// touches says whether the action puts a finger or the keyboard on the app in front.
func touches(action string) bool {
	switch action {
	case ActTap, ActLongPress, ActDrag, ActSwipe, ActType, ActKey:
		return true
	}
	return false
}

// gestureTime is how long a step's input itself takes, on top of its bridge call.
func gestureTime(st Step) time.Duration {
	ms := time.Duration(st.Ms) * time.Millisecond
	switch st.Action {
	case ActLongPress:
		return orDur(ms, LongPress)
	case ActSwipe:
		return orDur(ms, SwipeTime)
	case ActDrag:
		return orDur(ms, DragTime) + time.Duration(st.HoldMs)*time.Millisecond
	case ActButton:
		return ms
	case ActType:
		if st.Target != nil {
			return FocusSettle
		}
	}
	return 0
}

func orDur(v, d time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return d
}
