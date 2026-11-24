package assistant

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

func drawIntakePage(p intakePageState, c healthIntakeSettings, v healthIntakeView, reminder waterReminderState, now time.Time, loc *time.Location, l i18n.Lang, version int, quiet bool) wire.View {
	t := func(key string, args ...i18n.Args) string { return intakeText(l, key, args...) }
	text := func(s string) wire.Component { return wire.Component{Type: "text", Text: s} }
	button := func(title, action string, submit bool) wire.Component {
		return wire.Component{Type: "button", Title: title, Action: &wire.Action{ID: action, Submit: submit}}
	}
	field := func(name, label, value, kind string) wire.Component {
		return wire.Component{Type: "field", Name: name, Label: t(label), Value: value, Kind: kind}
	}
	page := wire.View{Title: t("title." + p.Kind), RefreshAfter: 30}
	if p.Edit {
		items := []wire.Component{}
		if p.Kind == "protein" {
			items = append(items, field("goal_g", "setting.protein_goal", strconv.Itoa(c.Protein.Goal), "number"))
		} else {
			items = append(items,
				field("goal_ml", "setting.water_goal", strconv.Itoa(c.Water.Goal), "number"),
				field("cup_ml", "setting.cup", strconv.Itoa(c.Water.Cup), "number"),
				field("start", "setting.start", c.Water.Start, "time"),
				field("end", "setting.end", c.Water.End, "time"),
				field("interval_minutes", "setting.interval", strconv.Itoa(c.Water.Interval), "number"))
			for _, flag := range []struct {
				name string
				on   bool
			}{{"remind", c.Water.Remind}, {"weekends", c.Water.Weekends}, {"stop_when_done", c.Water.Stop}} {
				f := field(flag.name, "setting."+flag.name, strconv.FormatBool(flag.on), "choice")
				f.Choices = []wire.Choice{{Value: "true", Title: t("on")}, {Value: "false", Title: t("off")}}
				items = append(items, f)
			}
		}
		items = append(items, button(t("save"), "save", true), button(t("back"), "back", false))
		page.Sections = []wire.Section{{Title: t("settings"), Items: items}}
	} else {
		kind, unit, color := v.Water, t("unit.water"), "blue"
		if p.Kind == "protein" {
			kind, unit, color = v.Protein, t("unit.protein"), "amber"
		}
		ratio := min(1.0, kind.Today/float64(kind.Goal))
		detail := t("progress", i18n.Args{"today": intakeNumber(kind.Today), "goal": kind.Goal, "unit": unit})
		if ratio >= 1 {
			detail = t("toast.goal")
		}
		// Progress has no category color; the existing gauge keeps the intake kind's hue.
		today := []wire.Component{{Type: "grid", MaxColumns: 1, Tiles: []wire.Tile{{
			Title: t("today"), Value: intakeNumber(kind.Today), Subtitle: detail, Color: color,
			Gauge: &wire.Gauge{Ratio: &ratio, Unit: unit, Color: color},
		}}},
			text(t("zone."+p.Kind, i18n.Args{"zone": v.Zone}))}
		if p.Kind == "water" {
			today = append(today, text(intakeReminderStatus(l, c.Water, reminder, kind.Entries, now, loc)))
			if quiet {
				today = append(today, wire.Component{Type: "metric", Label: t("reminders"), Value: t("status.quiet"), Tone: "caution"})
			}
			if reminder.Reason != "" {
				today = append(today, wire.Component{Type: "metric", Label: t("reminders"), Value: intakeDeliveryNote(l, reminder.Reason), Tone: "caution"})
			}
		}
		add := []wire.Component{}
		if p.Kind == "water" {
			add = append(add, button(t("drink", i18n.Args{"cup": c.Water.Cup}), "drink", false))
		}
		add = append(add, field("amount", "amount."+p.Kind, "", "number"))
		if p.Kind == "protein" {
			add = append(add, field("note", "food", "", "text"))
		}
		add = append(add, button(t("log"), "log", true))
		rows := []wire.Component{}
		if p.Offset >= len(kind.Entries) {
			p.Offset = 0
		}
		for _, e := range kind.Entries[p.Offset:min(p.Offset+40, len(kind.Entries))] {
			action := &wire.Action{ID: "undo", Confirm: t("undo.confirm"), Values: map[string]string{"id": e.ID}}
			if version < 4 {
				action.ID, action.Values = "undo:"+e.ID, nil
			}
			rows = append(rows, wire.Component{Type: "row", Title: e.At.In(loc).Format("15:04") + " · " + intakeNumber(e.Amount) + " " + unit,
				Subtitle: clip(e.Note, 200), Value: t("undo"), Action: action})
		}
		if len(rows) == 0 {
			rows = append(rows, text(t("empty."+p.Kind)))
		}
		if p.Offset+40 < len(kind.Entries) {
			rows = append(rows, button(t("older"), "older", false))
		}
		if p.Offset > 0 {
			rows = append(rows, button(t("latest"), "latest", false))
		}
		points, daysDone := []wire.ChartPoint{}, 0
		for _, d := range v.Days {
			n := d.Water
			if p.Kind == "protein" {
				n = d.Protein
			}
			if n >= float64(kind.Goal) {
				daysDone++
			}
			points = append(points, wire.ChartPoint{Label: d.Date[5:], Value: n})
		}
		recent := []wire.Component{
			{Type: "segmented", Name: "days", Value: strconv.Itoa(p.Days), Choices: []wire.Choice{
				{Value: "7", Title: t("days", i18n.Args{"days": 7})}, {Value: "30", Title: t("days", i18n.Args{"days": 30})}}, Action: &wire.Action{ID: "range"}},
			{Type: "chart", Kind: "bar", Title: t("chart." + p.Kind), Unit: unit, Series: []wire.ChartSeries{{Name: t("title." + p.Kind), Color: color, Points: points}}},
			text(t("days_done", i18n.Args{"goal": kind.Goal, "unit": unit, "days": daysDone})),
		}
		page.Sections = []wire.Section{
			{Title: t("today"), Items: today}, {Title: t("record"), Items: add}, {Title: t("entries"), Items: rows},
			{Title: t("recent"), Items: recent}, {Items: []wire.Component{button(t("settings"), "settings", false)}},
		}
	}
	state, _ := json.Marshal(p)
	page.State = string(state)
	return page
}
