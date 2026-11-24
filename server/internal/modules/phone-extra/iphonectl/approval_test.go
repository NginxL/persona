package iphonectl

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func checkTap(e Element, app App) Verdict {
	scr := number(nil, snapshot{App: app, Elements: []Element{e}})
	return testRules.Check(Step{Action: ActTap, Target: &Target{N: 1}}, Touch{Target: &scr.Elements[0]}, scr)
}

func TestSensitiveTaps(t *testing.T) {
	for _, c := range []struct {
		e    Element
		app  App
		want []Sensitivity
	}{
		{el("button", "Pay", 0, 0, 10, 10), exampleApp, []Sensitivity{Payment}},
		{el("button", "Buy for $4.99", 0, 0, 10, 10), exampleApp, []Sensitivity{Payment}},
		{el("button", "$4.99", 0, 0, 10, 10), exampleApp, []Sensitivity{Payment}},
		{el("button", "12.00 €", 0, 0, 10, 10), exampleApp, []Sensitivity{Payment}},
		{el("button", "确认付款", 0, 0, 10, 10), exampleApp, []Sensitivity{Payment}},
		{el("button", "Delete Message", 0, 0, 10, 10), exampleApp, []Sensitivity{Deletion}},
		{el("button", "删除", 0, 0, 10, 10), exampleApp, []Sensitivity{Deletion}},
		{el("button", "削除", 0, 0, 10, 10), exampleApp, []Sensitivity{Deletion}},
		{el("button", "Trash", 0, 0, 10, 10), exampleApp, []Sensitivity{Deletion}},
		{el("button", "Send", 0, 0, 10, 10), smsApp, []Sensitivity{SendMessage}},
		{el(RoleKeyboardKey, "发送", 0, 0, 10, 10), exampleApp, []Sensitivity{SendMessage}},
		{el("cell", "Card", 0, 0, 10, 10), App{BundleID: "com.apple.Passbook"}, []Sensitivity{Payment}},
		// Not sensitive.
		{el("button", "Payment Methods", 0, 0, 10, 10), exampleApp, nil},
		{el("button", "Payee", 0, 0, 10, 10), exampleApp, nil},
		{el("button", "Recently Deleted", 0, 0, 10, 10), exampleApp, nil},
		{el("button", "最近删除", 0, 0, 10, 10), exampleApp, nil},
		{el(RoleKeyboardKey, "delete", 0, 0, 10, 10), exampleApp, nil},
		{el("staticText", "$4.99", 0, 0, 10, 10), exampleApp, nil},
		{el("button", "Sender", 0, 0, 10, 10), exampleApp, nil},
		{el("button", "Back", 0, 0, 10, 10), smsApp, nil},
	} {
		v := checkTap(c.e, c.app)
		if !slices.Equal(v.Needs, c.want) || v.Refused != "" {
			t.Errorf("%s %q in %s: needs %v, want %v (%+v)", c.e.Role, c.e.Label, c.app, v.Needs, c.want, v)
		}
	}
}

func TestSensitiveGestures(t *testing.T) {
	scr := number(nil, snapshot{App: exampleApp, Size: screenSize, Elements: []Element{el("cell", "Mail from Sam", 0, 100, 390, 60),
		el("button", "Delete", 300, 700, 80, 40)}})
	row, del := &scr.Elements[0], &scr.Elements[1]
	swipe := func(st Step, target *Numbered, to *Target) Verdict {
		st.Action, st.To = ActSwipe, to
		tg := resolved{target: target}
		if p, ok := to.point(); ok {
			tg.toAt = &p
		}
		return testRules.Check(st, touchOf(st, tg, scr), scr)
	}
	if v := swipe(Step{Direction: "left"}, row, nil); !slices.Equal(v.Needs, []Sensitivity{Deletion}) {
		t.Errorf("swipe left on a row: %+v", v)
	}
	x, y := 5.0, 130.0
	if v := swipe(Step{}, row, &Target{X: &x, Y: &y}); !slices.Equal(v.Needs, []Sensitivity{Deletion}) {
		t.Errorf("swipe to a point on the left: %+v", v)
	}
	if v := swipe(Step{Direction: "up"}, row, nil); len(v.Needs) != 0 {
		t.Errorf("scrolling: %+v", v)
	}
	if v := testRules.Check(Step{Action: ActDrag}, Touch{Target: row, To: del}, scr); !slices.Equal(v.Needs, []Sensitivity{Deletion}) {
		t.Errorf("drag onto Delete: %+v", v)
	}
	sms := number(nil, snapshot{App: smsApp})
	if v := testRules.Check(Step{Action: ActKey, Key: "return"}, Touch{}, sms); !slices.Equal(v.Needs, []Sensitivity{SendMessage}) {
		t.Errorf("return in Messages: %+v", v)
	}
	if v := testRules.Check(Step{Action: ActKey, Key: "backspace"}, Touch{}, sms); len(v.Needs) != 0 {
		t.Errorf("backspace: %+v", v)
	}
}

func TestExtraWordsFromConfig(t *testing.T) {
	r := Rules{Words: map[Sensitivity][]string{Deletion: {"archive forever"}}}
	scr := number(nil, snapshot{App: exampleApp, Elements: []Element{el("button", "Archive forever", 0, 0, 1, 1)}})
	if v := r.Check(Step{Action: ActTap, Target: &Target{N: 1}}, Touch{Target: &scr.Elements[0]}, scr); !slices.Equal(v.Needs, []Sensitivity{Deletion}) {
		t.Fatalf("%+v", v)
	}
}

func TestSensitiveTyping(t *testing.T) {
	secure := el(RoleSecureTextField, "Password", 0, 0, 100, 30)
	plain := el("textField", "Name", 0, 40, 100, 30)
	pin := el("textField", "PIN", 0, 80, 100, 30)
	scr := number(nil, snapshot{App: exampleApp, Elements: []Element{secure, plain, pin}})
	typeInto := func(n int, text string, s *Screen) Verdict {
		var target *Numbered
		if n > 0 {
			e, _ := s.Find(n)
			target = &e
		}
		return testRules.Check(Step{Action: ActType, Text: text}, Touch{Target: target}, s)
	}
	if v := typeInto(1, "x", scr); !slices.Equal(v.Needs, []Sensitivity{Password}) {
		t.Errorf("secure field: %+v", v)
	}
	if v := typeInto(2, "x", scr); len(v.Needs) != 0 {
		t.Errorf("plain field: %+v", v)
	}
	if v := typeInto(3, "1234", scr); !slices.Equal(v.Needs, []Sensitivity{Password}) {
		t.Errorf("PIN field: %+v", v)
	}
	// No target and nothing known to have the keyboard, with a password field on screen: it may be the one.
	if v := typeInto(0, "x", scr); !slices.Equal(v.Needs, []Sensitivity{Password}) {
		t.Errorf("unknown focus: %+v", v)
	}
	focusedPlain := plain
	focusedPlain.Focused = true
	if v := typeInto(0, "x", number(nil, snapshot{App: exampleApp, Elements: []Element{secure, focusedPlain}})); len(v.Needs) != 0 {
		t.Errorf("focused plain field: %+v", v)
	}
	sms := number(nil, snapshot{App: smsApp, Elements: []Element{focusedPlain}})
	if v := typeInto(0, "see you\n", sms); !slices.Equal(v.Needs, []Sensitivity{SendMessage}) {
		t.Errorf("line break in Messages: %+v", v)
	}
	if v := typeInto(0, "see you", sms); len(v.Needs) != 0 {
		t.Errorf("no line break: %+v", v)
	}
}

func TestPersonasOwnAppIsNeverOperated(t *testing.T) {
	scr := number(nil, snapshot{App: selfApp, Elements: []Element{el("button", "Approve", 0, 0, 100, 40)}})
	for _, st := range []Step{
		{Action: ActTap}, {Action: ActLongPress}, {Action: ActSwipe, Direction: "up"}, {Action: ActDrag},
		{Action: ActType, Text: "y"}, {Action: ActKey, Key: "return"},
	} {
		if v := testRules.Check(st, Touch{Target: &scr.Elements[0]}, scr); v.Refused == "" {
			t.Errorf("%s in Persona: %+v", st.Action, v)
		}
	}
	for _, id := range []string{selfApp.BundleID, " " + strings.ToUpper(selfApp.BundleID) + " "} {
		if v := testRules.Check(Step{Action: ActLaunch, App: id}, Touch{}, nil); v.Refused == "" {
			t.Errorf("launching Persona as %q is allowed", id)
		}
	}
	named := Rules{SelfNames: []string{"Persona"}}
	if v := named.Check(Step{Action: ActTap}, Touch{}, number(nil, snapshot{App: App{Name: "Persona"}})); v.Refused == "" {
		t.Error("Persona named without a bundle id is operated")
	}
	// No bundle id known at all: the name alone keeps every touch out, whatever the bundle id in front.
	for _, front := range []App{{BundleID: "com.example.unknown", Name: "Persona"}, {BundleID: "com.example.unknown", Name: " persona "}} {
		scr := number(nil, snapshot{App: front, Elements: []Element{el("button", "Approve", 0, 0, 100, 40)}})
		for _, st := range []Step{{Action: ActTap}, {Action: ActType, Text: "y"}, {Action: ActKey, Key: "return"}, {Action: ActSwipe, Direction: "up"}} {
			if v := named.Check(st, Touch{Target: &scr.Elements[0]}, scr); v.Refused == "" {
				t.Errorf("%s in %+v with SelfApps empty: %+v", st.Action, front, v)
			}
		}
	}
	// The whole name only: an app whose name merely starts with it is not Persona's.
	if v := named.Check(Step{Action: ActTap}, Touch{}, number(nil, snapshot{App: App{BundleID: "com.example.other", Name: "Persona Notes"}})); v.Refused != "" {
		t.Errorf("a longer name: %+v", v)
	}
	// Leaving it is fine.
	if v := testRules.Check(Step{Action: ActButton, Button: "home"}, Touch{}, scr); v.Refused != "" || len(v.Needs) != 0 {
		t.Errorf("home: %+v", v)
	}
}

func TestStepsCannotCarryAnApproval(t *testing.T) {
	for _, raw := range []string{
		`[{"action":"tap","target":{"n":1},"approved":true}]`,
		`[{"action":"tap","target":{"n":1,"approval":"yes"}}]`,
		`[{"action":"tap","target":{"n":1}}] {"approved":true}`,
		`[{"action":"tap","target":{"n":1}}] []`,
	} {
		if _, err := ParseSteps(json.RawMessage(raw)); err == nil {
			t.Errorf("%s parsed", raw)
		}
	}
	if steps, err := ParseSteps(json.RawMessage(`[{"action":"tap","target":{"n":1}}]`)); err != nil || len(steps) != 1 {
		t.Fatalf("%v %v", steps, err)
	}
}

func TestStepValidation(t *testing.T) {
	for _, c := range []struct{ raw, want string }{
		{`[{"action":"swipe","target":{"n":3},"to":{"n":8},"ms":600000}]`, "ms is 0 to 5000"},
		{`[{"action":"swipe","to":{"n":8},"ms":-1}]`, "ms is 0 to 5000"},
		{`[{"action":"drag","target":{"n":1}}]`, "needs to"},
		{`[{"action":"tap","target":{"n":1},"count":4}]`, "count"},
		{`[{"action":"key","key":"enter"}]`, "key is"},
		{`[{"action":"button","button":"power"}]`, "button is"},
		{`[{"action":"tap","target":{"n":1,"label":"x"}}]`, "one of"},
		{`[{"action":"fly"}]`, "unknown action"},
	} {
		if _, err := ParseSteps(json.RawMessage(c.raw)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.raw, err, c.want)
		}
	}
}

func TestATapOnAButtonsTextIsATapOnTheButton(t *testing.T) {
	scr := number(nil, snapshot{App: exampleApp, Elements: []Element{el("button", "Pay", 20, 760, 350, 50),
		el("staticText", "Pay", 170, 770, 50, 30)}})
	text, _ := scr.Find(2)
	if text.Role != "staticText" {
		t.Fatalf("%+v", scr.Elements)
	}
	tg := resolved{target: &text}
	if v := testRules.Check(Step{Action: ActTap}, touchOf(Step{Action: ActTap}, tg, scr), scr); !slices.Equal(v.Needs, []Sensitivity{Payment}) {
		t.Fatalf("%+v", v)
	}
}

func TestNamesFromIdentifiersAndPlaceholders(t *testing.T) {
	del := el("other", "", 0, 0, 40, 40)
	del.Identifier = "deleteButton"
	if v := checkTap(del, exampleApp); !slices.Equal(v.Needs, []Sensitivity{Deletion}) {
		t.Errorf("identifier: %+v", v)
	}
	pin := el("textField", "", 0, 0, 200, 40)
	pin.Value = "Password"
	scr := number(nil, snapshot{App: exampleApp, Elements: []Element{pin}})
	if v := testRules.Check(Step{Action: ActType, Text: "x"}, Touch{Target: &scr.Elements[0]}, scr); !slices.Equal(v.Needs, []Sensitivity{Password}) {
		t.Errorf("placeholder: %+v", v)
	}
}

func TestTypingAtABarePointIsAnUnknownField(t *testing.T) {
	focused := el("textField", "Username", 0, 100, 390, 40)
	focused.Focused = true
	scr := number(nil, snapshot{App: exampleApp, Elements: []Element{focused, el(RoleSecureTextField, "", 0, 0, 0, 0)}})
	x, y := 100.0, 300.0
	st := Step{Action: ActType, Text: "x", Target: &Target{X: &x, Y: &y}}
	if v := testRules.Check(st, Touch{}, scr); !slices.Equal(v.Needs, []Sensitivity{Password}) {
		t.Fatalf("%+v", v)
	}
}
