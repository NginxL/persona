package extension

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension/uistream"
	"github.com/nwverse/persona/server/internal/i18n"
)

func v11Example(t *testing.T) *View {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", "extension", "examples", "View.v11-scene-flat.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseView(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// sceneOf is v's scene, for changing it before a fit.
func sceneOf(t *testing.T, v *View) *UIElem {
	t.Helper()
	for i := range v.Sections {
		for j := range v.Sections[i].Items {
			if e := &v.Sections[i].Items[j]; e.Type == wire.ComponentScene {
				return e
			}
		}
	}
	t.Fatal("no scene")
	return nil
}

// An app of 11 gets the scene as the extension drew it.
func TestFitV11Example(t *testing.T) {
	page, _ := Slot("nav_page")
	v := v11Example(t)
	want, _ := json.Marshal(v)
	if dropped, err := Fit(v, page, 11); err != nil || len(dropped) > 0 {
		t.Fatalf("dropped %v, %v", dropped, err)
	}
	if got, _ := json.Marshal(v); string(got) != string(want) {
		t.Errorf("an app of 11 gets it changed\nwant %s\n got %s", want, got)
	}
}

// An app of 10 gets the poster as an image in the scene's place, with its key, alt and caption, and nothing else of
// the scene.
func TestFitV11PosterBecomesImage(t *testing.T) {
	page, _ := Slot("nav_page")
	v := v11Example(t)
	if _, err := Fit(v, page, 10); err != nil {
		t.Fatal(err)
	}
	got := items(v)
	if len(got) != 1 {
		t.Fatalf("items %+v", got)
	}
	e := got[0]
	if e.Type != wire.ComponentImage || e.Key != "home" || e.Src == nil || e.Src.Ref != "home.png" ||
		e.Alt != "A one-bedroom flat from above: living room and bedroom" || e.Caption != "1 light on" {
		t.Errorf("image %+v", e)
	}
	if len(e.Shapes) > 0 || len(e.Pins) > 0 || e.Camera != nil || e.Poster != nil {
		t.Errorf("the scene's fields came along: %+v", e)
	}
}

// Without a poster an app of 10 gets the caption and a row per pin in order: the label, the state as the value, or
// as a badge in the pin's tone when the tone says something, the icon and the action; keys are the scene's and the
// pin's.
func TestFitV11RowsWithoutPoster(t *testing.T) {
	page, _ := Slot("nav_page")
	v := v11Example(t)
	sceneOf(t, v).Poster = nil
	if _, err := Fit(v, page, 10); err != nil {
		t.Fatal(err)
	}
	got := items(v)
	if len(got) != 4 || got[0].Type != wire.ComponentText || got[0].Text != "1 light on" {
		t.Fatalf("items %+v", got)
	}
	lamp, bedroom, ac := got[1], got[2], got[3]
	if lamp.Type != wire.ComponentRow || lamp.Title != "Living room light" || lamp.Badge == nil ||
		lamp.Badge.Text != "On" || lamp.Badge.Tone != wire.TonePositive || lamp.Value != "" || lamp.Icon != "light" ||
		lamp.Key != "home/light.living" || lamp.Action == nil || lamp.Action.Values["entity"] != "light.living" {
		t.Errorf("lamp row %+v", lamp)
	}
	if bedroom.Title != "Bedroom light" || bedroom.Value != "Off" || bedroom.Badge != nil || bedroom.Key != "home/light.bedroom" {
		t.Errorf("bedroom row %+v", bedroom)
	}
	if ac.Title != "Living room AC" || ac.Value != "Off" || ac.Action != nil || ac.Key != "home/climate.living" {
		t.Errorf("AC row %+v", ac)
	}
}

// A scene with neither a poster nor pins still says what it shows.
func TestFitV11NothingButAlt(t *testing.T) {
	page, _ := Slot("nav_page")
	v := v11Example(t)
	s := sceneOf(t, v)
	s.Poster, s.Pins, s.Caption = nil, nil, ""
	if _, err := Fit(v, page, 10); err != nil {
		t.Fatal(err)
	}
	if got := items(v); len(got) != 1 || got[0].Type != wire.ComponentText || got[0].Text != s.Alt {
		t.Errorf("items %+v", got)
	}
}

// A row's key is the scene's and the pin's only when nothing else on the page has it.
func TestFitV11RowKeysOnlyWhenFree(t *testing.T) {
	page, _ := Slot("nav_page")
	v := v11Example(t)
	sceneOf(t, v).Poster = nil
	v.Sections = append(v.Sections, Section{Items: []UIElem{{Type: wire.ComponentText, Key: "home/light.bedroom", Text: "taken"}}})
	if _, err := Fit(v, page, 10); err != nil {
		t.Fatal(err)
	}
	for _, e := range items(v) {
		if e.Title == "Bedroom light" && e.Key != "" {
			t.Errorf("a taken key was given again: %q", e.Key)
		}
	}
}

// A native stream never gets a scene, even at 11. Its pictures are not signed, so a poster by ref gives rows and a
// poster by url an image; every row has a key.
func TestFitNativeV11(t *testing.T) {
	page, _ := Slot("nav_page")
	native := func(poster *ImageSource) []UIElem {
		t.Helper()
		v := v11Example(t)
		sceneOf(t, v).Poster = poster
		v.Sections[0].Key = "s"
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		fitted, err := FitNative(raw, page, 11, i18n.En)
		if err != nil {
			t.Fatal(err)
		}
		var got View
		if err := json.Unmarshal(fitted, &got); err != nil {
			t.Fatal(err)
		}
		return got.Sections[0].Items
	}
	rows := native(&ImageSource{Ref: "home.png"})
	if len(rows) != 4 || rows[1].Type != wire.ComponentRow || rows[1].Key != "home/light.living" {
		t.Errorf("native with a ref poster: %+v", rows)
	}
	for _, e := range rows[1:] {
		if e.Key == "" {
			t.Errorf("a native row without a key: %+v", e)
		}
	}
	img := native(&ImageSource{URL: "https://example.com/home.png"})
	if len(img) != 1 || img[0].Type != wire.ComponentImage || img[0].Src.URL != "https://example.com/home.png" {
		t.Errorf("native with a url poster: %+v", img)
	}
}

// A native stream's rows always have a key the stream can hold: a section's key or an overlong `key/id` gives a free
// digest instead, and the snapshot decodes.
func TestFitNativeV11RowKeys(t *testing.T) {
	page, _ := Slot("nav_page")
	box := []wire.SceneShape{{Kind: wire.SceneShapeBox, Center: []float64{0, 0}, Size: []float64{1, 1}, Bottom: num(0), Top: num(1)}}
	pin := func(id string) wire.ScenePin { return wire.ScenePin{ID: id, At: []float64{0, 0}, Label: id} }
	section := &View{Sections: []Section{
		{Key: "home/a", Items: []UIElem{{Type: "text", Text: "x", Key: "t"}}},
		{Key: "s", Items: []UIElem{{Type: wire.ComponentScene, Key: "home", Alt: "A flat", Shapes: box,
			Pins: []wire.ScenePin{pin("a"), pin("b")}}}},
	}}
	tooLong := &View{Sections: []Section{{Key: "s", Items: []UIElem{{Type: wire.ComponentScene, Key: strings.Repeat("k", 100),
		Alt: "A flat", Shapes: box, Pins: []wire.ScenePin{pin(strings.Repeat("n", 40))}}}}}}
	for name, v := range map[string]*View{"a section takes the row key": section, "the row key is too long": tooLong} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		fitted, err := FitNative(raw, page, 11, i18n.En)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got wire.View
		if err := json.Unmarshal(fitted, &got); err != nil {
			t.Fatalf("%s: %s", name, fitted)
		}
		rows := 0
		for _, s := range got.Sections {
			for _, e := range s.Items {
				if e.Type != wire.ComponentRow {
					continue
				}
				rows++
				if e.Key == "" || e.Key == "home/a" || utf8.RuneCountInString(e.Key) > graphRowKeyMost {
					t.Fatalf("%s: row key %q", name, e.Key)
				}
			}
		}
		if rows == 0 {
			t.Fatalf("%s: no rows in %s", name, fitted)
		}
		snapshot, err := json.Marshal(wire.UIStreamSnapshot{Protocol: 1, Session: "s", Epoch: "e", RootKey: "root", Version: 1, View: got})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := uistream.DecodeSnapshot(snapshot); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// In a card an app of 11 gets the scene; below, its rows are cut to the card's room, the first kept. The poster is
// one of the card's pictures.
func TestFitV11Card(t *testing.T) {
	card, _ := Slot("overview_card")
	many := func() *View {
		v := v11Example(t)
		s := sceneOf(t, v)
		s.Poster, s.Caption = nil, ""
		s.Pins = nil
		for k := range 40 {
			s.Pins = append(s.Pins, wire.ScenePin{ID: "p" + strconv.Itoa(k), At: []float64{1, 1}, Label: "Pin " + strconv.Itoa(k)})
		}
		return v
	}
	v := many()
	if _, err := Fit(v, card, 11); err != nil || len(items(v)) != 1 || items(v)[0].Type != wire.ComponentScene {
		t.Fatalf("a card at 11: %v %+v", err, items(v))
	}
	v = many()
	if _, err := Fit(v, card, 10); err != nil {
		t.Fatal(err)
	}
	if n := len(items(v)); n < 1 || n > CompactItems {
		t.Errorf("a card at 10 has %d items, want 1 to %d", n, CompactItems)
	}

	v = v11Example(t)
	tiles := make([]wire.Tile, CompactImages)
	for k := range tiles {
		tiles[k] = wire.Tile{Key: "t" + strconv.Itoa(k), Title: "t", Image: &ImageSource{URL: "https://example.com/t.png"}}
	}
	v.Sections[0].Items = append(v.Sections[0].Items, UIElem{Type: "grid", Tiles: tiles})
	if _, err := Fit(v, card, 11); err == nil {
		t.Error("a card with a poster and as many pictures again is taken")
	}
}

// An app older than 6 still gets something in the scene's place.
func TestFitV11OldApps(t *testing.T) {
	page, _ := Slot("nav_page")
	for _, version := range []int{5, 3, 1} {
		for _, poster := range []bool{true, false} {
			v := v11Example(t)
			if !poster {
				sceneOf(t, v).Poster = nil
			}
			if _, err := Fit(v, page, version); err != nil {
				t.Fatalf("v%d poster %v: %v", version, poster, err)
			}
			if len(items(v)) == 0 {
				t.Errorf("v%d poster %v: nothing left", version, poster)
			}
			for _, e := range items(v) {
				if e.Type == wire.ComponentScene {
					t.Errorf("v%d: a scene reached an old app", version)
				}
			}
		}
	}
}

// An action's answer that replaces a scene in place keeps it for an app of 11; below, the items go and the app draws
// the whole page again, since a scene becomes more or fewer items.
func TestFitResultSceneBelow11(t *testing.T) {
	page, _ := Slot("nav_page")
	scene := func() ActionResult {
		v := v11Example(t)
		return ActionResult{Lines: &FileLines{Path: "a.go", Side: "new", Start: 1, Lines: []string{"a"}}, Items: []UIElem{*sceneOf(t, v)}}
	}
	res := scene()
	if err := FitResult(&res, page, 11); err != nil {
		t.Fatalf("v11: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Type != wire.ComponentScene || res.Lines == nil {
		t.Fatalf("v11 does not keep the scene and the lines: %+v", res)
	}
	for _, version := range []int{10, 6, 5} {
		res := scene()
		if err := FitResult(&res, page, version); err != nil {
			t.Fatalf("v%d: %v", version, err)
		}
		if res.Items != nil || res.Lines == nil {
			t.Errorf("v%d: items %+v, lines %v; want no items and the lines", version, res.Items, res.Lines)
		}
	}
	res = scene()
	res.Items[0].Pins = append(res.Items[0].Pins, res.Items[0].Pins[0])
	if err := FitResult(&res, page, 10); err == nil {
		t.Error("a scene with a pin twice is taken below 11")
	}
}
