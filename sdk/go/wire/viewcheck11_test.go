package wire

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

const minSceneBoxJSON = `{"kind":"box","center":[0,0],"size":[1,1],"bottom":0,"top":1}`

func decodeSceneView(t *testing.T, raw string) *View {
	t.Helper()
	var v View
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("decode view JSON: %v\n%s", err, raw)
	}
	return &v
}

func checkSceneJSON(t *testing.T, sceneJSON string) error {
	t.Helper()
	return CheckView(decodeSceneView(t, `{"sections":[{"items":[`+sceneJSON+`]}]}`))
}

func scenePolygonPointsJSON(n int) string {
	pts := make([]string, n)
	for k := range n {
		ang := 2 * math.Pi * float64(k) / float64(n)
		pts[k] = fmt.Sprintf("[%.6f,%.6f]", 10*math.Cos(ang), 10*math.Sin(ang))
	}
	return "[" + strings.Join(pts, ",") + "]"
}

func scenePrismSeriesJSON(counts ...int) string {
	shapes := make([]string, len(counts))
	for i, n := range counts {
		shapes[i] = `{"kind":"prism","points":` + scenePolygonPointsJSON(n) + `,"bottom":0,"top":1}`
	}
	return "[" + strings.Join(shapes, ",") + "]"
}

func sceneLitPinsJSON(lit int, extra ...string) string {
	pins := make([]string, 0, lit+len(extra))
	for i := range lit {
		pins = append(pins, fmt.Sprintf(
			`{"id":"l%d","at":[0,0],"label":"Lamp %d","state":"On","light":{"on":true,"warmth":"warm"}}`, i, i))
	}
	pins = append(pins, extra...)
	return "[" + strings.Join(pins, ",") + "]"
}

// What a scene may be: the full home model, tight thickness and corner radius limits, simple non-convex prisms,
// 4000 prism points across shapes, pins with neutral tone or light off without state, and 16 lights on.
func TestCheckViewSceneAccepts(t *testing.T) {
	sixtyTwo64s := make([]int, 63)
	for i := range 62 {
		sixtyTwo64s[i] = 64
	}
	sixtyTwo64s[62] = 32 // 62*64 + 32 = 4000

	for name, raw := range map[string]string{
		"full home scene": `{
			"type": "scene",
			"key": "home",
			"alt": "The home from above: living room, bedroom, bathroom, kitchen and balcony",
			"caption": "3 lights on",
			"poster": {"ref": "home.png", "width": 2000, "height": 1378},
			"camera": {"yaw": 225, "pitch": 35, "zoom": 1},
			"shapes": [
				{"kind": "box", "center": [-1.6, 2.4], "size": [2.4, 0.9], "yaw": 0, "bottom": 0, "top": 0.42, "round": 0.07, "color": "#C8C8C6"},
				{"kind": "cylinder", "center": [-3.2, 1.6], "radius": 0.3, "bottom": 0.42, "top": 0.47, "color": "#F6F4EE"},
				{"kind": "prism", "points": [[0, 0], [4, 0], [4, 3], [0, 3]], "bottom": -0.02, "top": 0, "material": "tile"}
			],
			"labels": [
				{"text": "Living room", "at": [-2.6, 2.8]},
				{"text": "Kitchen", "at": [1.5, 1.2], "height": 0}
			],
			"pins": [
				{"id": "light.living_1", "at": [-3.8, 2.75], "height": 2.2, "label": "Living room light 1", "state": "On",
				 "icon": "light", "tone": "positive", "light": {"on": true, "warmth": "warm"},
				 "action": {"id": "open:light.living_1"}}
			]
		}`,
		"thinnest height and half-size round": `{
			"type": "scene",
			"alt": "Tabletop",
			"shapes": [
				{"kind": "box", "center": [0, 0], "size": [2, 0.8], "round": 0.4, "bottom": 0, "top": 0.005},
				{"kind": "box", "center": [3, 0], "size": [0.8, 2], "round": 0, "bottom": -0.5, "top": 0.5}
			]
		}`,
		"non-convex L prism and small valid triangle": `{
			"type": "scene",
			"alt": "Counter",
			"shapes": [
				{"kind": "prism", "points": [[0, 0], [3, 0], [3, 1], [1, 1], [1, 3], [0, 3]], "bottom": 0, "top": 0.9, "material": "wood"},
				{"kind": "prism", "points": [[0, 0], [0.015625, 0], [0, 0.015625]], "bottom": 0, "top": 0.01}
			]
		}`,
		"4000 prism points across 63 prisms": `{"type":"scene","alt":"Many prisms","shapes":` +
			scenePrismSeriesJSON(sixtyTwo64s...) + `}`,
		"neutral tone and light off without state, plus 16 lights on": `{"type":"scene","alt":"Devices","shapes":[` +
			minSceneBoxJSON + `],"pins":` + sceneLitPinsJSON(16,
			`{"id":"plain","at":[1,0],"label":"Sensor"}`,
			`{"id":"neutral","at":[1,1],"label":"Door","tone":"neutral"}`,
			`{"id":"off","at":[1,2],"label":"Desk lamp","light":{"on":false,"warmth":"cool"}}`,
			`{"id":"colored","at":[1,3],"label":"Strip","state":"Warm","color":"amber"}`,
		) + `}`,
	} {
		if err := checkSceneJSON(t, raw); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Each shape kind refuses the other kinds' fields, a height under 0.005, and a box's round above half its smaller size.
func TestCheckViewSceneShapes(t *testing.T) {
	for name, c := range map[string]struct {
		shape string
		want  string
	}{
		"box with points": {
			`{"kind":"box","center":[0,0],"size":[1,1],"bottom":0,"top":1,"points":[[0,0],[1,0],[0,1]]}`,
			"a box has no points or radius",
		},
		"box with radius": {
			`{"kind":"box","center":[0,0],"size":[1,1],"bottom":0,"top":1,"radius":0.5}`,
			"a box has no points or radius",
		},
		"cylinder with points": {
			`{"kind":"cylinder","center":[0,0],"radius":0.5,"bottom":0,"top":1,"points":[[0,0],[1,0],[0,1]]}`,
			"a cylinder has no points, size, yaw, round or wall",
		},
		"cylinder with size": {
			`{"kind":"cylinder","center":[0,0],"radius":0.5,"bottom":0,"top":1,"size":[1,1]}`,
			"a cylinder has no points, size, yaw, round or wall",
		},
		"cylinder with yaw": {
			`{"kind":"cylinder","center":[0,0],"radius":0.5,"bottom":0,"top":1,"yaw":0}`,
			"a cylinder has no points, size, yaw, round or wall",
		},
		"cylinder with round": {
			`{"kind":"cylinder","center":[0,0],"radius":0.5,"bottom":0,"top":1,"round":0}`,
			"a cylinder has no points, size, yaw, round or wall",
		},
		"cylinder marked a wall": {
			`{"kind":"cylinder","center":[0,0],"radius":0.5,"bottom":0,"top":1,"wall":true}`,
			"a cylinder has no points, size, yaw, round or wall",
		},
		"prism marked a wall": {
			`{"kind":"prism","points":[[0,0],[1,0],[0,1]],"bottom":0,"top":1,"wall":true}`,
			"a prism has no center, size, yaw, round, radius or wall",
		},
		"prism with center": {
			`{"kind":"prism","points":[[0,0],[1,0],[0,1]],"bottom":0,"top":1,"center":[0,0]}`,
			"a prism has no center, size, yaw, round, radius or wall",
		},
		"prism with size": {
			`{"kind":"prism","points":[[0,0],[1,0],[0,1]],"bottom":0,"top":1,"size":[1,1]}`,
			"a prism has no center, size, yaw, round, radius or wall",
		},
		"prism with yaw": {
			`{"kind":"prism","points":[[0,0],[1,0],[0,1]],"bottom":0,"top":1,"yaw":0}`,
			"a prism has no center, size, yaw, round, radius or wall",
		},
		"prism with round": {
			`{"kind":"prism","points":[[0,0],[1,0],[0,1]],"bottom":0,"top":1,"round":0}`,
			"a prism has no center, size, yaw, round, radius or wall",
		},
		"prism with radius": {
			`{"kind":"prism","points":[[0,0],[1,0],[0,1]],"bottom":0,"top":1,"radius":0.5}`,
			"a prism has no center, size, yaw, round, radius or wall",
		},
		"top equal to bottom": {
			`{"kind":"box","center":[0,0],"size":[1,1],"bottom":0.4,"top":0.4}`,
			"top is at least 0.005 above bottom",
		},
		"top below bottom": {
			`{"kind":"cylinder","center":[0,0],"radius":0.3,"bottom":1,"top":0.5}`,
			"top is at least 0.005 above bottom",
		},
		"top minus bottom under 0.005": {
			`{"kind":"box","center":[0,0],"size":[1,1],"bottom":0,"top":0.004}`,
			"top is at least 0.005 above bottom",
		},
		"round over half smaller y size": {
			`{"kind":"box","center":[0,0],"size":[2.4,0.9],"round":0.46,"bottom":0,"top":0.5}`,
			"round is at most half the smaller size",
		},
		"round over half smaller x size": {
			`{"kind":"box","center":[0,0],"size":[0.6,2.0],"round":0.31,"bottom":0,"top":0.5}`,
			"round is at most half the smaller size",
		},
	} {
		err := checkSceneJSON(t, `{"type":"scene","alt":"Home","shapes":[`+c.shape+`]}`)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
}

// NaN and Inf have no JSON spelling: CheckScene refuses non-finite numbers across camera, shapes, labels and pins.
func TestCheckSceneNonFinite(t *testing.T) {
	goodBox := SceneShape{
		Kind: SceneShapeBox, Center: []float64{0, 0}, Size: []float64{1, 1}, Bottom: fptr(0), Top: fptr(1),
	}
	nan, posInf, negInf := math.NaN(), math.Inf(1), math.Inf(-1)

	for name, c := range map[string]struct {
		scene Component
		want  string
	}{
		"camera yaw NaN": {
			Component{Camera: &SceneCamera{Yaw: fptr(nan)}, Shapes: []SceneShape{goodBox}},
			"camera: numbers are finite",
		},
		"camera pitch +Inf": {
			Component{Camera: &SceneCamera{Pitch: fptr(posInf)}, Shapes: []SceneShape{goodBox}},
			"camera: numbers are finite",
		},
		"camera zoom -Inf": {
			Component{Camera: &SceneCamera{Zoom: fptr(negInf)}, Shapes: []SceneShape{goodBox}},
			"camera: numbers are finite",
		},
		"shape missing bottom": {
			Component{Shapes: []SceneShape{{Kind: SceneShapeBox, Center: []float64{0, 0}, Size: []float64{1, 1}, Top: fptr(1)}}},
			"bottom and top are required",
		},
		"shape missing top": {
			Component{Shapes: []SceneShape{{Kind: SceneShapeBox, Center: []float64{0, 0}, Size: []float64{1, 1}, Bottom: fptr(0)}}},
			"bottom and top are required",
		},
		"shape bottom NaN": {
			Component{Shapes: []SceneShape{{Kind: SceneShapeBox, Center: []float64{0, 0}, Size: []float64{1, 1}, Bottom: fptr(nan), Top: fptr(1)}}},
			"numbers are finite",
		},
		"shape top +Inf": {
			Component{Shapes: []SceneShape{{Kind: SceneShapeBox, Center: []float64{0, 0}, Size: []float64{1, 1}, Bottom: fptr(0), Top: fptr(posInf)}}},
			"numbers are finite",
		},
		"shape yaw -Inf": {
			Component{Shapes: []SceneShape{{Kind: SceneShapeBox, Center: []float64{0, 0}, Size: []float64{1, 1}, Yaw: fptr(negInf), Bottom: fptr(0), Top: fptr(1)}}},
			"numbers are finite",
		},
		"shape round NaN": {
			Component{Shapes: []SceneShape{{Kind: SceneShapeBox, Center: []float64{0, 0}, Size: []float64{1, 1}, Round: fptr(nan), Bottom: fptr(0), Top: fptr(1)}}},
			"numbers are finite",
		},
		"cylinder radius +Inf": {
			Component{Shapes: []SceneShape{{Kind: SceneShapeCylinder, Center: []float64{0, 0}, Radius: posInf, Bottom: fptr(0), Top: fptr(1)}}},
			"numbers are finite",
		},
		"box center NaN": {
			Component{Shapes: []SceneShape{{Kind: SceneShapeBox, Center: []float64{nan, 0}, Size: []float64{1, 1}, Bottom: fptr(0), Top: fptr(1)}}},
			"center and size are finite",
		},
		"box size +Inf": {
			Component{Shapes: []SceneShape{{Kind: SceneShapeBox, Center: []float64{0, 0}, Size: []float64{1, posInf}, Bottom: fptr(0), Top: fptr(1)}}},
			"center and size are finite",
		},
		"box center wrong arity": {
			Component{Shapes: []SceneShape{{Kind: SceneShapeBox, Center: []float64{0}, Size: []float64{1, 1}, Bottom: fptr(0), Top: fptr(1)}}},
			"center and size are finite",
		},
		"cylinder center -Inf": {
			Component{Shapes: []SceneShape{{Kind: SceneShapeCylinder, Center: []float64{0, negInf}, Radius: 0.5, Bottom: fptr(0), Top: fptr(1)}}},
			"center is finite",
		},
		"prism point NaN": {
			Component{Shapes: []SceneShape{{Kind: SceneShapePrism, Points: [][]float64{{0, 0}, {1, nan}, {0, 1}}, Bottom: fptr(0), Top: fptr(1)}}},
			"point 1 is finite",
		},
		"prism point +Inf": {
			Component{Shapes: []SceneShape{{Kind: SceneShapePrism, Points: [][]float64{{0, 0}, {1, 0}, {posInf, 1}}, Bottom: fptr(0), Top: fptr(1)}}},
			"point 2 is finite",
		},
		"prism fewer than 3 points": {
			Component{Shapes: []SceneShape{{Kind: SceneShapePrism, Points: [][]float64{{0, 0}, {1, 0}}, Bottom: fptr(0), Top: fptr(1)}}},
			"at least 3 points",
		},
		"label at NaN": {
			Component{Shapes: []SceneShape{goodBox}, Labels: []SceneLabel{{Text: "Room", At: []float64{nan, 0}}}},
			"label 0: numbers are finite",
		},
		"label height +Inf": {
			Component{Shapes: []SceneShape{goodBox}, Labels: []SceneLabel{{Text: "Room", At: []float64{0, 0}, Height: fptr(posInf)}}},
			"label 0: numbers are finite",
		},
		"pin at -Inf": {
			Component{Shapes: []SceneShape{goodBox}, Pins: []ScenePin{{ID: "p", Label: "Lamp", At: []float64{negInf, 0}}}},
			`pin "p": numbers are finite`,
		},
		"pin height NaN": {
			Component{Shapes: []SceneShape{goodBox}, Pins: []ScenePin{{ID: "p", Label: "Lamp", At: []float64{0, 0}, Height: fptr(nan)}}},
			`pin "p": numbers are finite`,
		},
	} {
		c.scene.Type = ComponentScene
		err := CheckScene(&c.scene)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
}

// A prism's polygon must be simple, with edges at least 0.005 m, area at least 1e-4 m², and at most 4000 prism points
// across the scene.
func TestCheckViewScenePrisms(t *testing.T) {
	for name, c := range map[string]struct {
		points string
		want   string
	}{
		"repeated adjacent point": {
			`[[0,0],[2,0],[2,0],[0,2]]`,
			"edge 1 is shorter than 0.005",
		},
		"repeated non-adjacent point (figure-eight)": {
			`[[0,0],[2,0],[2,2],[0,0],[-2,2],[-2,0]]`,
			"edges 0 and 2 touch",
		},
		"short edge": {
			`[[0,0],[0.004,0],[0,1]]`,
			"edge 0 is shorter than 0.005",
		},
		"short closing edge": {
			`[[0,0],[1,0],[0,0.004]]`,
			"edge 2 is shorter than 0.005",
		},
		"fold-back": {
			`[[0,0],[3,0],[1,0],[2,2],[0,2]]`,
			"edge 1 folds back",
		},
		"fold-back on closing edge": {
			`[[1,0],[3,0],[2,2],[0,2],[0,0],[2,0]]`,
			"edge 5 folds back",
		},
		"crossing edges (bow tie)": {
			`[[0,0],[2,2],[2,0],[0,2]]`,
			"edges 0 and 2 touch",
		},
		"vertex touching non-adjacent edge": {
			`[[0,0],[4,0],[4,3],[2,0],[0,3]]`,
			"edges 0 and 2 touch",
		},
		"collinear overlapping non-adjacent edges": {
			`[[0,0],[4,0],[4,2],[3,2],[3,0],[1,0],[1,2],[0,2]]`,
			"edges 0 and 3 touch",
		},
		"tiny area": {
			`[[0,0],[0.02,0],[0,0.008]]`,
			"the polygon's area is under 0.0001",
		},
	} {
		raw := `{"type":"scene","alt":"Floor","shapes":[{"kind":"prism","points":` + c.points + `,"bottom":0,"top":0.1}]}`
		err := checkSceneJSON(t, raw)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}

	over := make([]int, 63)
	for i := range 62 {
		over[i] = 64
	}
	over[62] = 33 // 62*64 + 33 = 4001
	err := checkSceneJSON(t, `{"type":"scene","alt":"Too many points","shapes":`+scenePrismSeriesJSON(over...)+`}`)
	if err == nil || !strings.Contains(err.Error(), "at most 4000 points together") {
		t.Fatalf("4001 prism points: %v", err)
	}
}

// Pin rules: unique ids, never both tone and color, state required whenever a non-neutral tone, a color or light on
// conveys state (while neutral tone and light off may omit state), and at most 16 lights on.
func TestCheckViewScenePins(t *testing.T) {
	for name, c := range map[string]struct {
		pins string
		want string
	}{
		"duplicate id": {
			`[{"id":"lamp","at":[0,0],"label":"Lamp 1"},{"id":"lamp","at":[1,0],"label":"Lamp 2"}]`,
			`id "lamp" twice`,
		},
		"tone and color both": {
			`[{"id":"lamp","at":[0,0],"label":"Lamp","state":"On","tone":"positive","color":"amber"}]`,
			"a tone or a color, not both",
		},
		"positive tone without state": {
			`[{"id":"lamp","at":[0,0],"label":"Lamp","tone":"positive"}]`,
			"a state in words",
		},
		"caution tone without state": {
			`[{"id":"lamp","at":[0,0],"label":"Lamp","tone":"caution"}]`,
			"a state in words",
		},
		"danger tone without state": {
			`[{"id":"lamp","at":[0,0],"label":"Lamp","tone":"danger"}]`,
			"a state in words",
		},
		"color without state": {
			`[{"id":"lamp","at":[0,0],"label":"Lamp","color":"blue"}]`,
			"a state in words",
		},
		"light on without state": {
			`[{"id":"lamp","at":[0,0],"label":"Lamp","light":{"on":true}}]`,
			"a state in words",
		},
		"17 lights on": {
			sceneLitPinsJSON(17),
			"at most 16 pins with a light on",
		},
	} {
		err := checkSceneJSON(t, `{"type":"scene","alt":"Home","shapes":[`+minSceneBoxJSON+`],"pins":`+c.pins+`}`)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}

	for name, pins := range map[string]string{
		"neutral tone without state": `[{"id":"p","at":[0,0],"label":"Sensor","tone":"neutral"}]`,
		"light off without state":    `[{"id":"p","at":[0,0],"label":"Lamp","light":{"on":false}}]`,
	} {
		if err := checkSceneJSON(t, `{"type":"scene","alt":"Home","shapes":[`+minSceneBoxJSON+`],"pins":`+pins+`}`); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A view has at most one scene, whether in the same section or across sections.
func TestCheckViewTwoScenes(t *testing.T) {
	scene := `{"type":"scene","alt":"Room","shapes":[` + minSceneBoxJSON + `]}`
	for name, raw := range map[string]string{
		"same section": `{"sections":[{"items":[` + scene + `,` + scene + `]}]}`,
		"two sections": `{"sections":[{"items":[` + scene + `]},{"items":[` + scene + `]}]}`,
	} {
		err := CheckView(decodeSceneView(t, raw))
		if err == nil || !strings.Contains(err.Error(), "at most one scene") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Each pin of a scene counts toward the page's 300-component limit: 200 items + 100 pins fit, 201 + 100 do not.
func TestCheckViewSceneCountsPins(t *testing.T) {
	pins := make([]string, 100)
	for k := range pins {
		pins[k] = fmt.Sprintf(`{"id":"p%d","at":[0,0],"label":"Pin %d"}`, k, k)
	}
	scene := `{"type":"scene","alt":"Home","shapes":[` + minSceneBoxJSON + `],"pins":[` + strings.Join(pins, ",") + `]}`

	makeView := func(texts int) *View {
		items := make([]string, 0, texts+1)
		for k := range texts {
			items = append(items, fmt.Sprintf(`{"type":"text","text":"t%d"}`, k))
		}
		items = append(items, scene)
		return decodeSceneView(t, `{"sections":[{"items":[`+strings.Join(items, ",")+`]}]}`)
	}

	if err := CheckView(makeView(200)); err != nil {
		t.Fatalf("300 components (200 texts + 100 pins): %v", err)
	}
	err := CheckView(makeView(201))
	if err == nil || !strings.Contains(err.Error(), "300 items") {
		t.Fatalf("301 components (201 texts + 100 pins): %v", err)
	}
}

// A scene's poster obeys ImageSource rules: path is written by the hub only, and exactly one of https url and ref is given.
func TestCheckViewScenePoster(t *testing.T) {
	for name, c := range map[string]struct {
		poster string
		want   string
	}{
		"path with ref": {
			`{"ref":"home.png","path":"/api/v1/extensions/home/-/image/home.png"}`,
			"path is written by the hub only",
		},
		"path alone": {
			`{"path":"/api/v1/extensions/home/-/image/home.png"}`,
			"path is written by the hub only",
		},
		"neither url nor ref": {
			`{}`,
			"exactly one of url and ref",
		},
		"both url and ref": {
			`{"ref":"home.png","url":"https://example.com/home.png"}`,
			"exactly one of url and ref",
		},
		"url with credentials": {
			`{"url":"https://user:pass@example.com/home.png"}`,
			"needs an https url",
		},
	} {
		raw := `{"type":"scene","alt":"Home","poster":` + c.poster + `,"shapes":[` + minSceneBoxJSON + `]}`
		err := checkSceneJSON(t, raw)
		if err == nil || !strings.Contains(err.Error(), "poster:") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want poster error with %q, got %v", name, c.want, err)
		}
	}
}

// A scene's poster counts toward ImagesMost (50), even when the scene is the last item on the page.
func TestCheckViewScenePosterCountsImages(t *testing.T) {
	makeView := func(imgs int) *View {
		items := make([]string, 0, imgs+1)
		for k := range imgs {
			items = append(items, fmt.Sprintf(`{"type":"image","src":{"ref":"p%d.png"},"alt":"Picture %d"}`, k, k))
		}
		items = append(items, `{"type":"scene","alt":"Home","poster":{"ref":"home.png"},"shapes":[`+minSceneBoxJSON+`]}`)
		return decodeSceneView(t, `{"sections":[{"items":[`+strings.Join(items, ",")+`]}]}`)
	}

	if err := CheckView(makeView(ImagesMost - 1)); err != nil {
		t.Fatalf("%d pictures: %v", ImagesMost, err)
	}
	err := CheckView(makeView(ImagesMost))
	if err == nil || !strings.Contains(err.Error(), "at most 50 pictures") {
		t.Fatalf("%d pictures (scene last): want at most 50 pictures, got %v", ImagesMost+1, err)
	}
}

// Pin actions are returned by Actions(), checked for push state size, and stripped when assistant-only while keeping
// the pins themselves and any plain extension action.
func TestScenePinActions(t *testing.T) {
	v := decodeSceneView(t, `{
		"sections": [{
			"items": [{
				"type": "scene",
				"alt": "Home",
				"shapes": [`+minSceneBoxJSON+`],
				"pins": [
					{"id": "push", "at": [0, 0], "label": "Push", "action": {"id": "a1", "push": {"page": "room"}}},
					{"id": "reply", "at": [1, 0], "label": "Reply", "action": {"id": "a2", "reply": "Turn off"}},
					{"id": "open", "at": [2, 0], "label": "Open", "action": {"id": "a3", "open": "https://example.com"}},
					{"id": "pop", "at": [3, 0], "label": "Pop", "action": {"id": "a4", "pop": true}},
					{"id": "plain", "at": [4, 0], "label": "Plain", "action": {"id": "a5"}},
					{"id": "idle", "at": [5, 0], "label": "Idle"}
				]
			}]
		}]
	}`)

	acts := Actions(v)
	if len(acts) != 5 {
		t.Fatalf("got %d actions, want 5", len(acts))
	}
	for i, want := range []string{"a1", "a2", "a3", "a4", "a5"} {
		if acts[i].ID != want {
			t.Fatalf("action %d: got %q, want %q", i, acts[i].ID, want)
		}
	}
	if err := CheckView(v); err != nil {
		t.Fatal(err)
	}

	dropped := StripAssistantFields(v)
	if !reflect.DeepEqual(dropped, []string{"assistant_action"}) {
		t.Fatalf("dropped %v, want [assistant_action]", dropped)
	}
	pins := v.Sections[0].Items[0].Pins
	if len(pins) != 6 {
		t.Fatalf("got %d pins after strip, want all 6 kept", len(pins))
	}
	for i := range 4 {
		if pins[i].Action != nil {
			t.Errorf("pin %d (%s): assistant-only action survived: %+v", i, pins[i].ID, pins[i].Action)
		}
	}
	if pins[4].Action == nil || pins[4].Action.ID != "a5" {
		t.Errorf("plain pin action lost: %+v", pins[4].Action)
	}

	bigPush := decodeSceneView(t, fmt.Sprintf(`{
		"sections": [{
			"items": [{
				"type": "scene",
				"alt": "Home",
				"shapes": [%s],
				"pins": [{
					"id": "p", "at": [0, 0], "label": "Room",
					"action": {"id": "open", "push": {"ui": "room", "state": %q}}
				}]
			}]
		}]
	}`, minSceneBoxJSON, strings.Repeat("x", StateMost+1)))
	if err := CheckView(bigPush); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("oversized pin push state: %v", err)
	}
}

// ImageRefs and HasImageRef see a scene's poster ref (and ignore an https url poster), and CheckAssistantView refuses
// a ref poster accordingly.
func TestScenePosterImageRefs(t *testing.T) {
	v := decodeSceneView(t, `{
		"sections": [{
			"items": [
				{"type": "image", "src": {"ref": "first.png"}, "alt": "First"},
				{"type": "scene", "alt": "Home", "poster": {"ref": "home.png"}, "shapes": [`+minSceneBoxJSON+`]},
				{"type": "image", "src": {"ref": "home.png"}, "alt": "Duplicate"}
			]
		}]
	}`)
	if !HasImageRef(&v.Sections[0].Items[1]) {
		t.Fatal("HasImageRef missed poster ref")
	}
	if got := ImageRefs(v); !reflect.DeepEqual(got, []string{"first.png", "home.png"}) {
		t.Fatalf("ImageRefs = %v, want [first.png home.png]", got)
	}

	urlView := decodeSceneView(t, `{
		"fallback": "Home model",
		"sections": [{
			"items": [{
				"type": "scene",
				"alt": "Home",
				"poster": {"url": "https://example.com/home.png"},
				"shapes": [`+minSceneBoxJSON+`]
			}]
		}]
	}`)
	if HasImageRef(&urlView.Sections[0].Items[0]) {
		t.Fatal("HasImageRef reported true for https url poster")
	}
	if got := ImageRefs(urlView); len(got) != 0 {
		t.Fatalf("ImageRefs for url poster = %v, want none", got)
	}
	if err := CheckAssistantView(urlView); err != nil {
		t.Fatalf("assistant card with https poster: %v", err)
	}

	urlView.Sections[0].Items[0].Poster = &ImageSource{Ref: "home.png"}
	if err := CheckAssistantView(urlView); err == nil || !strings.Contains(err.Error(), "https url, not ref") {
		t.Fatalf("assistant card with ref poster: %v", err)
	}
}

// Pointer numbers where 0 is meaningful (camera yaw, box yaw/round/bottom, prism top, label/pin height) survive JSON
// round-tripping without disappearing under omitempty.
func TestSceneZeroPointersRoundTrip(t *testing.T) {
	v := decodeSceneView(t, `{
		"sections": [{
			"items": [{
				"type": "scene",
				"alt": "Zeros",
				"camera": {"yaw": 0},
				"shapes": [
					{"kind": "box", "center": [0, 0], "size": [1, 1], "yaw": 0, "round": 0, "bottom": 0, "top": 1},
					{"kind": "prism", "points": [[0, 0], [1, 0], [0, 1]], "bottom": -0.1, "top": 0}
				],
				"labels": [{"text": "Floor", "at": [0, 0], "height": 0}],
				"pins": [{"id": "p", "at": [0, 0], "height": 0, "label": "Sensor"}]
			}]
		}]
	}`)
	if err := CheckView(v); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var back View
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	sc := back.Sections[0].Items[0]
	for name, p := range map[string]*float64{
		"camera.yaw":   sc.Camera.Yaw,
		"box.yaw":      sc.Shapes[0].Yaw,
		"box.round":    sc.Shapes[0].Round,
		"box.bottom":   sc.Shapes[0].Bottom,
		"prism.top":    sc.Shapes[1].Top,
		"label.height": sc.Labels[0].Height,
		"pin.height":   sc.Pins[0].Height,
	} {
		if p == nil || *p != 0 {
			t.Errorf("%s came back %v, want 0", name, p)
		}
	}
}
