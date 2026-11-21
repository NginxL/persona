package extension

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// UI version 6 (docs/extensions/extensions.md "UI version 6"): pictures, controls in rows, new charts and sparks, a row's extra
// actions, grids and carousels of tiles, date, time, multiple choice and multi-line fields, title styles, named colors
// and more icons. downgrade6 draws them with version 5's components for an older app; CollectValues puts back what
// such an app sends for a multichoice drawn as toggles.

// UI version 6's parts.
type (
	ImageSource = wire.ImageSource
	RowControl  = wire.RowControl
	RowAction   = wire.RowAction
	Spark       = wire.Spark
	Tile        = wire.Tile
)

// newIcons6 are the icons of UI version 6: an older app gets none (its default).
var newIcons6 = func() []string {
	i := slices.Index(wire.NavIcons, "file")
	return slices.Clone(wire.NavIcons[i+1:])
}()

// colorTone is the tone an older app draws a named color as: the nearest meaning, or none (neutral).
var colorTone = map[string]string{
	"red": "danger", "orange": "caution", "amber": "caution", "yellow": "caution", "green": "positive", "teal": "positive",
}

// Names the hub gives the fields it draws an older app for a multichoice (wire.ReservedName first): one toggle per
// choice, ~<name>.<base64url of the choice's value>, or when there are too many one text field, ~csv:<name>, the
// values with commas between.
const (
	multiCSVPrefix   = wire.ReservedName + "csv:"
	multiTogglesMost = 20
	fieldNameMost    = 128
)

// multiToggleName is the name of the toggle standing for choice value of multichoice name.
func multiToggleName(name, value string) string {
	return wire.ReservedName + name + "." + base64.RawURLEncoding.EncodeToString([]byte(value))
}

// CollectValues puts back the values of the multichoices an older app was drawn as toggles or a text field
// (downgrade6): every ~<name>.<value> that is "true" goes into <name> as a JSON array, ~csv:<name> is split at its
// commas, and the ~ names are taken out. The extension only ever sees a multichoice as UI version 6 writes it. A map
// with none of them is returned as it is.
func CollectValues(values map[string]string) map[string]string {
	if !slices.ContainsFunc(mapKeys(values), func(k string) bool { return strings.HasPrefix(k, wire.ReservedName) }) {
		return values
	}
	out := map[string]string{}
	chosen := map[string][]string{}
	for k, v := range values {
		if !strings.HasPrefix(k, wire.ReservedName) {
			out[k] = v
			continue
		}
		if name, ok := strings.CutPrefix(k, multiCSVPrefix); ok && name != "" {
			list := []string{}
			for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '，' || r == '、' }) {
				if part = strings.TrimSpace(part); part != "" && !slices.Contains(list, part) {
					list = append(list, part)
				}
			}
			chosen[name] = append(chosen[name], list...)
			continue
		}
		rest := strings.TrimPrefix(k, wire.ReservedName)
		dot := strings.LastIndexByte(rest, '.')
		if dot <= 0 {
			continue // not one of the hub's: dropped, as no extension name starts with ~
		}
		name := rest[:dot]
		value, err := base64.RawURLEncoding.DecodeString(rest[dot+1:])
		if err != nil {
			continue
		}
		if _, ok := chosen[name]; !ok {
			chosen[name] = []string{}
		}
		if v == "true" {
			chosen[name] = append(chosen[name], string(value))
		}
	}
	for name, list := range chosen {
		slices.Sort(list)
		raw, _ := json.Marshal(slices.Compact(list))
		out[name] = string(raw)
	}
	return out
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// number writes a control's number as an older app shows it.
func number(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }

// controlShown is how a control's value reads in a row: its display, else the number and unit, else on or off.
func controlShown(c *RowControl, l i18n.Lang) string {
	if c.Display != "" {
		return c.Display
	}
	if c.Kind == wire.RowControlKindSwitch {
		if c.On {
			return i18n.T(l, "extension-ui-fallback.switch_on")
		}
		return i18n.T(l, "extension-ui-fallback.switch_off")
	}
	if c.Value == nil {
		return ""
	}
	return strings.TrimSpace(number(*c.Value) + " " + c.Unit)
}

// withValue is a control's action carrying name = value, or submitting the page's fields (where an older app fills a
// field for it).
func withValue(c *RowControl, value string, submit bool) *UIAction {
	a := c.Action
	if submit {
		a.Submit = true
		return &a
	}
	a.Values = maps.Clone(c.Action.Values)
	if a.Values == nil {
		a.Values = map[string]string{}
	}
	a.Values[c.Name] = value
	return &a
}

// fitBadge draws a badge's color as its nearest tone.
func fitBadge(b *Badge) {
	if b != nil && b.Color != "" {
		b.Tone, b.Color = colorTone[b.Color], ""
	}
}

// oldThumbnail and oldAvatar leave out a picture the hub would fetch (a ref): an older app cannot.
func oldThumbnail(t *wire.Thumbnail) *wire.Thumbnail {
	if t == nil || t.URL == "" {
		return nil
	}
	return &wire.Thumbnail{URL: t.URL, Aspect: t.Aspect}
}

func oldAvatar(a *wire.Avatar) *wire.Avatar {
	if a == nil {
		return nil
	}
	out := &wire.Avatar{URL: a.URL, Text: a.Text}
	if out.URL == "" && out.Text == "" {
		return nil
	}
	return out
}

func oldIcon6(icon string) string {
	if slices.Contains(newIcons6, icon) {
		return ""
	}
	return icon
}

// controlItems draws a row's control for an app of uiVersion (below 6), after the row it is in: version 4 and 5 tap a
// row or a button with the value (action.values), older ones fill a field and submit it. A card has no fields: there
// the row only shows the value. A disabled control is only shown.
func controlItems(row UIElem, c *RowControl, uiVersion int, compact bool, l i18n.Lang) []UIElem {
	row.Control = nil
	shown := controlShown(c, l)
	if c.Disabled {
		row.Value = cmp.Or(row.Value, shown)
		return []UIElem{row}
	}
	values := uiVersion >= 4
	switch c.Kind {
	case wire.RowControlKindSwitch:
		next := strconv.FormatBool(!c.On)
		if values {
			if row.Action == nil {
				row.Value, row.Action = cmp.Or(row.Value, shown), withValue(c, next, false)
				return []UIElem{row}
			}
			// the row's own action stays the row's: the switch is a row of its own under it
			return []UIElem{row, {Type: "row", Title: i18n.T(l, "extension-ui-fallback.switch"), Value: shown, Action: withValue(c, next, false)}}
		}
		row.Value = cmp.Or(row.Value, shown)
		if compact {
			return []UIElem{row}
		}
		return []UIElem{row,
			{Type: "field", Name: c.Name, Label: row.Title, Kind: "toggle", Value: strconv.FormatBool(c.On)},
			{Type: "button", Title: i18n.T(l, "extension-ui-fallback.apply"), Action: withValue(c, "", true)}}
	case wire.RowControlKindStepper:
		row.Value = cmp.Or(row.Value, shown)
		if values {
			v, step := *c.Value, *c.Step
			down, up := v-step, v+step
			if c.Min != nil {
				down = max(down, *c.Min)
			}
			if c.Max != nil {
				up = min(up, *c.Max)
			}
			out := []UIElem{row}
			if down != v {
				out = append(out, UIElem{Type: "button", Title: "−", Action: withValue(c, number(down), false)})
			}
			if up != v {
				out = append(out, UIElem{Type: "button", Title: "+", Action: withValue(c, number(up), false)})
			}
			return out
		}
	default: // a slider
		row.Value = cmp.Or(row.Value, shown)
	}
	if compact {
		return []UIElem{row}
	}
	label := row.Title
	if c.Min != nil && c.Max != nil {
		label = i18n.T(l, "extension-ui-fallback.control_range", i18n.Args{"title": row.Title, "min": number(*c.Min),
			"max": number(*c.Max), "unit": c.Unit})
	}
	return []UIElem{row,
		{Type: "field", Name: c.Name, Label: clip(label, StringMost), Kind: "number", Value: number(*c.Value)},
		{Type: "button", Title: i18n.T(l, "extension-ui-fallback.set_value"), Action: withValue(c, "", true)}}
}

// tileRow is a tile as an older app's row.
func tileRow(t Tile, l i18n.Lang) UIElem {
	row := UIElem{Type: "row", Key: t.Key, Title: t.Title, Subtitle: t.Subtitle, Value: t.Value, Badge: t.Badge,
		Icon: t.Icon, Action: t.Action, Control: t.Control}
	if row.Badge == nil && t.Active {
		row.Badge = &Badge{Text: i18n.T(l, "extension-ui-fallback.tile_on"), Tone: "positive"}
	}
	if t.Image != nil && t.Image.URL != "" {
		row.Icon, row.Thumbnail = "", &wire.Thumbnail{URL: t.Image.URL, Aspect: "square"}
	}
	return row
}

// fitChart draws a chart of UI version 6 as a bar or a line chart: a candlestick as the line of its closes (its
// latest PointsMost), an area as a line, a donut or a pie as bars; colors as tones, each once.
func fitChart(e *UIElem, l i18n.Lang) {
	switch e.Kind {
	case wire.ChartKindCandlestick:
		e.Kind, e.Detail = wire.ChartKindLine, strings.TrimSpace(i18n.T(l, "extension-ui-fallback.closing_price")+" "+e.Detail)
		if s := &e.Series[0]; len(s.Points) > PointsMost {
			s.Points = s.Points[len(s.Points)-PointsMost:]
		}
	case wire.ChartKindArea:
		e.Kind, e.Stacked = wire.ChartKindLine, false
	case wire.ChartKindDonut, wire.ChartKindPie:
		e.Kind = wire.ChartKindBar
		if e.Center != "" {
			e.Detail = strings.TrimSpace(e.Center + " " + e.Detail)
		}
	}
	e.Center, e.Rising = "", ""
	e.Detail = clip(e.Detail, StringMost)
	used := map[string]bool{}
	for k := range e.Series {
		s := &e.Series[k]
		if s.Color != "" {
			s.Tone, s.Color = colorTone[s.Color], ""
		}
		if s.Tone != "" && used[s.Tone] {
			s.Tone = "" // the app's own color, not one another series has
		}
		used[s.Tone] = s.Tone != ""
		points := make([]ChartPoint, len(s.Points))
		for n, pt := range s.Points {
			points[n] = ChartPoint{Label: pt.Label, Value: pt.Value, Display: pt.Display}
		}
		s.Points = points
	}
}

// fieldItems draws a field of UI version 6 for an older app: a date or a time as a text in its format, multiple lines
// as a text, a multichoice as a toggle per choice (or one text of values with commas, when there are many).
func fieldItems(e UIElem, l i18n.Lang) []UIElem {
	label := e.Label
	if e.Detail != "" {
		label = clip(i18n.T(l, "extension-ui-fallback.field_detail", i18n.Args{"label": e.Label, "detail": e.Detail}), StringMost)
	}
	e.Earliest, e.Latest, e.Detail = "", "", ""
	switch e.Kind {
	case wire.FieldKindDate, wire.FieldKindTime, wire.FieldKindDatetime:
		e.Placeholder = map[string]string{wire.FieldKindDate: "YYYY-MM-DD", wire.FieldKindTime: "HH:MM",
			wire.FieldKindDatetime: "YYYY-MM-DDTHH:MM"}[e.Kind]
		e.Kind, e.Label = "text", label
		return []UIElem{e}
	case wire.FieldKindMultiline:
		e.Kind, e.Label = "text", label
		return []UIElem{e}
	case wire.FieldKindMultichoice:
		chosen, _ := wire.MultiValue(e.Value, e.Choices)
		tooLong := slices.ContainsFunc(e.Choices, func(c UIChoice) bool { return len(multiToggleName(e.Name, c.Value)) > fieldNameMost })
		if len(e.Choices) > multiTogglesMost || tooLong {
			return []UIElem{{Type: "field", Name: multiCSVPrefix + e.Name, Label: clip(i18n.T(l, "extension-ui-fallback.comma_separated", i18n.Args{"label": label}), StringMost),
				Kind: "text", Value: clip(strings.Join(chosen, ", "), StringMost), Placeholder: e.Placeholder}}
		}
		out := []UIElem{{Type: "text", Text: label, Style: "secondary"}}
		for _, c := range e.Choices {
			out = append(out, UIElem{Type: "field", Name: multiToggleName(e.Name, c.Value), Label: cmp.Or(c.Title, c.Value),
				Kind: "toggle", Value: strconv.FormatBool(slices.Contains(chosen, c.Value))})
		}
		return out
	}
	e.Label = label
	return []UIElem{e}
}

// downgrade6 draws UI version 6's components and fields with version 5's (or, for what differs by version, those of
// uiVersion), in place; then the older downgrades go on. compact says it is a card's (no fields there).
func downgrade6(v *View, uiVersion int, compact bool, l i18n.Lang) {
	total := 0
	for i := range v.Sections {
		s := &v.Sections[i]
		items := make([]UIElem, 0, len(s.Items))
		for _, e := range s.Items {
			e.IconColor, e.Spark, e.Actions = "", nil, nil
			e.Icon = oldIcon6(e.Icon)
			e.Thumbnail, e.Avatar = oldThumbnail(e.Thumbnail), oldAvatar(e.Avatar)
			fitBadge(e.Badge)
			for k := range e.Badges {
				fitBadge(&e.Badges[k])
			}
			for k := range e.Entries {
				fitBadge(e.Entries[k].Badge)
			}
			for k := range e.Facts {
				e.Facts[k].Icon = oldIcon6(e.Facts[k].Icon)
			}
			for k := range e.Comments {
				c := &e.Comments[k]
				c.Avatar = oldAvatar(c.Avatar)
				fitBadge(c.Badge)
			}
			for k := range e.Threads {
				for n := range e.Threads[k].Comments {
					c := &e.Threads[k].Comments[n]
					c.Avatar = oldAvatar(c.Avatar)
					fitBadge(c.Badge)
				}
			}
			for k := range e.Stats {
				e.Stats[k].Spark = nil
			}
			switch e.Type {
			case "text":
				if e.Style == wire.TextStyleTitle || e.Style == wire.TextStyleHeadline {
					e.Style = "body"
				}
				items = append(items, e)
			case "metric":
				e.Icon = ""
				items = append(items, e)
			case "chart":
				fitChart(&e, l)
				items = append(items, e)
			case "field":
				items = append(items, fieldItems(e, l)...)
			case "row":
				if c := e.Control; c != nil {
					items = append(items, controlItems(e, c, uiVersion, compact, l)...)
				} else {
					items = append(items, e)
				}
			case "image":
				src := e.Src
				label := clip(cmp.Or(e.Caption, e.Alt), StringMost)
				switch {
				case src == nil || src.URL == "":
					items = append(items, UIElem{Type: "text", Text: clip(i18n.T(l, "extension-ui-fallback.image_newer", i18n.Args{"alt": e.Alt}), StringMost), Style: "caption"})
				case uiVersion < 3:
					items = append(items, UIElem{Type: "text", Text: label, Style: "secondary"})
				default:
					aspect := wire.ThumbnailAspectWide
					if e.Aspect == wire.ImageAspectSquare || e.Aspect == wire.ImageAspectPortrait {
						aspect = wire.ThumbnailAspectSquare
					}
					items = append(items, UIElem{Type: "row", Key: e.Key, Title: label, Thumbnail: &wire.Thumbnail{URL: src.URL, Aspect: aspect}})
				}
			case "grid", "carousel":
				if e.Type == "carousel" && e.Title != "" {
					items = append(items, UIElem{Type: "text", Text: e.Title, Style: "secondary"})
				}
				for _, t := range e.Tiles {
					row := tileRow(t, l)
					row.Icon = oldIcon6(row.Icon)
					fitBadge(row.Badge)
					if c := row.Control; c != nil {
						items = append(items, controlItems(row, c, uiVersion, compact, l)...)
					} else {
						items = append(items, row)
					}
				}
			default:
				items = append(items, e)
			}
		}
		if total+len(items) > ItemsMost-1 {
			items = append(items[:max(ItemsMost-1-total, 0)], UIElem{Type: "text", Text: i18n.T(l, "extension-ui-fallback.too_long"), Style: "caption"})
			s.Items = items
			v.Sections = v.Sections[:i+1]
			return
		}
		total += len(items)
		s.Items = items
	}
}

// Limits of a card (compact) in UI version 6: pictures, a grid's and a carousel's tiles.
const (
	CompactImages        = 4
	CompactGridTiles     = 4
	CompactCarouselTiles = 6
)

// checkCompact6 holds a card to what fits in it: no slider (too narrow to drag), a few pictures and tiles.
func checkCompact6(v *View) error {
	images := 0
	for _, s := range v.Sections {
		for _, e := range s.Items {
			if c := e.Control; c != nil && c.Kind == wire.RowControlKindSlider {
				return errors.New("a card has no slider: open a page for it")
			}
			switch e.Type {
			case "image":
				images++
			case "grid":
				if len(e.Tiles) > CompactGridTiles {
					return fmt.Errorf("a card's grid has at most %d tiles", CompactGridTiles)
				}
			case "carousel":
				if len(e.Tiles) > CompactCarouselTiles {
					return fmt.Errorf("a card's carousel has at most %d tiles", CompactCarouselTiles)
				}
			}
			if e.Thumbnail != nil || e.Poster != nil || e.Avatar != nil && (e.Avatar.URL != "" || e.Avatar.Ref != "") {
				images++
			}
			for _, t := range e.Tiles {
				if t.Image != nil {
					images++
				}
			}
		}
	}
	if images > CompactImages {
		return fmt.Errorf("a card has at most %d pictures", CompactImages)
	}
	return nil
}
