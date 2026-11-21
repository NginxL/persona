package extension

import (
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
)

// UI version 11 (docs/extensions/extensions.md "UI version 11", #12544): a scene, a small 3D model the person turns,
// zooms and taps. downgrade11 draws it with version 10's for an older app, and for a native stream (whose components
// never include a scene).

// downgrade11 draws every scene with version 10's components, in place. With a poster it becomes an image of it, with
// the scene's key, alt and caption; a native stream's pictures are not signed, so there only a poster with an https url
// counts. Without one it becomes its caption as a text, when it has one, and a row per pin in the pins' order: the
// pin's label, its state as the value (or as a badge in its tone, when the tone says something), its icon, color and
// action. A row's key is the scene's key and the pin's id when that is free on the page; a native stream needs a key
// on every row, so a taken or overlong one becomes a free digest of the id (freeGraphRowKey's). The scene's camera,
// shapes, labels and lights are left out.
//
// A card has at most CompactItems items and the scene is one of them (Fit's compact count): its caption and rows have
// to fit in what the card's other items leave, so they are cut to the room left, in the pins' order, keeping at least
// the first.
func downgrade11(v *View, native, compact bool) {
	used := make(map[string]bool)
	room := CompactItems
	// every key on the page: a section's and an item's, and every item's tile keys share the page's one key space
	for i := range v.Sections {
		if k := v.Sections[i].Key; k != "" {
			used[k] = true
		}
		for j := range v.Sections[i].Items {
			e := &v.Sections[i].Items[j]
			if k := e.Key; k != "" {
				used[k] = true
			}
			for k := range e.Tiles {
				if t := e.Tiles[k].Key; t != "" {
					used[t] = true
				}
			}
			if e.Type != wire.ComponentScene {
				room -= max(1, len(e.Tiles))
			}
		}
	}
	for i := range v.Sections {
		s := &v.Sections[i]
		items := make([]UIElem, 0, len(s.Items))
		for _, e := range s.Items {
			if e.Type != wire.ComponentScene {
				items = append(items, e)
				continue
			}
			if p := e.Poster; p != nil && (!native || p.URL != "") {
				items = append(items, UIElem{Type: wire.ComponentImage, Key: e.Key, Src: p, Alt: e.Alt, Caption: e.Caption})
				continue
			}
			var rows []UIElem
			if e.Caption != "" {
				rows = append(rows, UIElem{Type: wire.ComponentText, Text: e.Caption})
			}
			for _, pin := range e.Pins {
				row := UIElem{Type: wire.ComponentRow, Title: pin.Label, Icon: pin.Icon, IconColor: pin.Color, Action: pin.Action}
				if pin.Tone != "" && pin.Tone != wire.ToneNeutral && pin.State != "" {
					row.Badge = &wire.Badge{Text: pin.State, Tone: pin.Tone}
				} else {
					row.Value = pin.State
				}
				if e.Key != "" {
					if k := e.Key + "/" + pin.ID; !used[k] && utf8.RuneCountInString(k) <= graphRowKeyMost {
						row.Key = k
						used[k] = true
					} else if native {
						row.Key = freeGraphRowKey(e.Key, pin.ID, used)
					}
				} else if native {
					row.Key = freeGraphRowKey("", pin.ID, used)
				}
				rows = append(rows, row)
			}
			if len(rows) == 0 {
				// nothing to say but what the model shows
				rows = append(rows, UIElem{Type: wire.ComponentText, Text: e.Alt})
			}
			if compact {
				rows = rows[:min(len(rows), max(room, 1))]
			}
			items = append(items, rows...)
		}
		s.Items = items
	}
}
