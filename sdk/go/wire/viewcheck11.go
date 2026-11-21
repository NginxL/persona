package wire

import (
	"errors"
	"fmt"
	"math"
)

// UI version 11's checks (docs/extensions/extensions.md "UI version 11"): a scene's shapes, labels and pins. What the
// schema cannot say: every number finite, a shape at least SceneThinnest tall, a box's round at most half its smaller
// size, a prism's polygon simple, pin ids once, a pin's tone or color (not both), a pin's state whenever its tone, color
// or light says something, at most SceneLightsMost lights on, at most ScenePrismPointsMost prism points, and one scene
// a view. The page's count (each pin one component) is the page check's.

const (
	// SceneThinnest is the least a shape stands, top - bottom, and the shortest edge of a prism, in metres.
	SceneThinnest = 0.005
	// SceneAreaLeast is the least area of a prism's polygon, in square metres.
	SceneAreaLeast = 1e-4
	// SceneLightsMost is how many pins of a scene may have their light on: a phone lights so many at once.
	SceneLightsMost = 16
	// ScenePrismPointsMost is how many points a scene's prisms have together.
	ScenePrismPointsMost = 4000
)

// checkV11 holds a scene to UI version 11's rules: its shapes, labels and pins, its poster as a picture of version 6's,
// and one scene a view.
func (p *pageCheck) checkV11(e *Component) error {
	if e.Type != ComponentScene {
		return nil
	}
	if p.scenes++; p.scenes > 1 {
		return errors.New("a view has at most one scene")
	}
	if e.Poster != nil {
		if err := checkSource(e.Poster); err != nil {
			return fmt.Errorf("poster: %v", err)
		}
		// counted after checkV6 ran on this item, so held to the limit here
		if p.images++; p.images > ImagesMost {
			return fmt.Errorf("a page has at most %d pictures", ImagesMost)
		}
	}
	return CheckScene(e)
}

// CheckScene holds a scene to UI version 11's checks the schema cannot say, so a conformance test can check a view it
// did not decode through CheckView.
func CheckScene(e *Component) error {
	if c := e.Camera; c != nil {
		if !finitePtr(c.Yaw) || !finitePtr(c.Pitch) || !finitePtr(c.Zoom) {
			return errors.New("camera: numbers are finite")
		}
	}
	points := 0
	for k := range e.Shapes {
		s := &e.Shapes[k]
		if err := checkShape(s); err != nil {
			return fmt.Errorf("shape %d (%s): %v", k, s.Kind, err)
		}
		if points += len(s.Points); points > ScenePrismPointsMost {
			return fmt.Errorf("a scene's prisms have at most %d points together", ScenePrismPointsMost)
		}
	}
	for k, l := range e.Labels {
		if !finitePoint(l.At) || !finitePtr(l.Height) {
			return fmt.Errorf("label %d: numbers are finite", k)
		}
	}
	ids := make(map[string]bool, len(e.Pins))
	lights := 0
	for k := range e.Pins {
		pin := &e.Pins[k]
		if ids[pin.ID] {
			return fmt.Errorf("pin %d: id %q twice", k, pin.ID)
		}
		ids[pin.ID] = true
		if !finitePoint(pin.At) || !finitePtr(pin.Height) {
			return fmt.Errorf("pin %q: numbers are finite", pin.ID)
		}
		if pin.Tone != "" && pin.Color != "" {
			return fmt.Errorf("pin %q: a tone or a color, not both", pin.ID)
		}
		lit := pin.Light != nil && pin.Light.On
		if lit {
			lights++
		}
		if pin.State == "" && (pin.Tone != "" && pin.Tone != ToneNeutral || pin.Color != "" || lit) {
			return fmt.Errorf("pin %q: a state in words, since its tone, color or light says something", pin.ID)
		}
	}
	if lights > SceneLightsMost {
		return fmt.Errorf("a scene has at most %d pins with a light on", SceneLightsMost)
	}
	return nil
}

// checkShape holds one shape to its kind's checks.
func checkShape(s *SceneShape) error {
	if s.Bottom == nil || s.Top == nil {
		return errors.New("bottom and top are required")
	}
	if !finitePtr(s.Bottom) || !finitePtr(s.Top) || !finitePtr(s.Yaw) || !finitePtr(s.Round) || !finite(s.Radius) {
		return errors.New("numbers are finite")
	}
	if *s.Top-*s.Bottom < SceneThinnest {
		return fmt.Errorf("top is at least %g above bottom", SceneThinnest)
	}
	switch s.Kind {
	case SceneShapeBox:
		if len(s.Points) > 0 || s.Radius != 0 {
			return errors.New("a box has no points or radius")
		}
		if !finitePoint(s.Center) || !finitePoint(s.Size) {
			return errors.New("center and size are finite")
		}
		if s.Round != nil && *s.Round > min(s.Size[0], s.Size[1])/2 {
			return errors.New("round is at most half the smaller size")
		}
	case SceneShapeCylinder:
		if len(s.Points) > 0 || len(s.Size) > 0 || s.Yaw != nil || s.Round != nil {
			return errors.New("a cylinder has no points, size, yaw or round")
		}
		if !finitePoint(s.Center) {
			return errors.New("center is finite")
		}
	case SceneShapePrism:
		if len(s.Center) > 0 || len(s.Size) > 0 || s.Yaw != nil || s.Round != nil || s.Radius != 0 {
			return errors.New("a prism has no center, size, yaw, round or radius")
		}
		return checkPolygon(s.Points)
	}
	return nil
}

// checkPolygon holds a prism's points to a simple polygon: finite, no point twice, no edge shorter than SceneThinnest,
// no edge folding straight back on the one before it, no two edges that are not neighbours touching, and an area of
// at least SceneAreaLeast.
func checkPolygon(pts [][]float64) error {
	n := len(pts)
	if n < 3 {
		return errors.New("a prism has at least 3 points")
	}
	for k, p := range pts {
		if !finitePoint(p) {
			return fmt.Errorf("point %d is finite", k)
		}
	}
	at := func(k int) [2]float64 { p := pts[((k%n)+n)%n]; return [2]float64{p[0], p[1]} }
	area := 0.0
	for k := range n {
		a, b, c := at(k), at(k+1), at(k+2)
		if math.Hypot(b[0]-a[0], b[1]-a[1]) < SceneThinnest {
			return fmt.Errorf("edge %d is shorter than %g", k, SceneThinnest)
		}
		// b to c folding straight back along a to b
		ux, uy, vx, vy := b[0]-a[0], b[1]-a[1], c[0]-b[0], c[1]-b[1]
		if cross := ux*vy - uy*vx; math.Abs(cross) <= 1e-12*math.Hypot(ux, uy)*math.Hypot(vx, vy) && ux*vx+uy*vy < 0 {
			return fmt.Errorf("edge %d folds back", (k+1)%n)
		}
		area += a[0]*b[1] - b[0]*a[1]
	}
	for i := range n {
		for j := i + 2; j < n; j++ {
			if i == 0 && j == n-1 {
				continue // neighbours through the closing edge
			}
			if segmentsTouch(at(i), at(i+1), at(j), at(j+1)) {
				return fmt.Errorf("edges %d and %d touch", i, j)
			}
		}
	}
	if math.Abs(area)/2 < SceneAreaLeast {
		return fmt.Errorf("the polygon's area is under %g", SceneAreaLeast)
	}
	return nil
}

// segmentsTouch says whether the closed segments ab and cd share a point.
func segmentsTouch(a, b, c, d [2]float64) bool {
	orient := func(p, q, r [2]float64) float64 { return (q[0]-p[0])*(r[1]-p[1]) - (q[1]-p[1])*(r[0]-p[0]) }
	on := func(p, q, r [2]float64) bool { // r on the segment pq, given the three are in line
		return min(p[0], q[0]) <= r[0] && r[0] <= max(p[0], q[0]) && min(p[1], q[1]) <= r[1] && r[1] <= max(p[1], q[1])
	}
	sign := func(v float64) int {
		switch {
		case v > 0:
			return 1
		case v < 0:
			return -1
		}
		return 0
	}
	o1, o2, o3, o4 := sign(orient(a, b, c)), sign(orient(a, b, d)), sign(orient(c, d, a)), sign(orient(c, d, b))
	if o1 != o2 && o3 != o4 {
		return true
	}
	return o1 == 0 && on(a, b, c) || o2 == 0 && on(a, b, d) || o3 == 0 && on(c, d, a) || o4 == 0 && on(c, d, b)
}

// finitePoint says whether p is two finite numbers (the schema says two; a hand-built Component may not).
func finitePoint(p []float64) bool {
	return len(p) == 2 && finite(p[0]) && finite(p[1])
}
