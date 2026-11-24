package iphonectl

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLookNumbersTheScreen(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	resp, r := h.look()
	if r.Screen == nil || len(r.Screen.Elements) != 5 || resp.Image == nil {
		t.Fatalf("%s", resp.Text)
	}
	for _, want := range []string{"App: Notes (com.example.notes)", "Numbering 1 · 5 elements · screen 390×844 points",
		`[1] button "Back" (8,52 44×44)`, `[5] button "Delete"`, "Screenshot attached (4×8 pixels)."} {
		if !strings.Contains(resp.Text, want) {
			t.Errorf("look text lacks %q:\n%s", want, resp.Text)
		}
	}
	if strings.Contains(resp.Text, "other") || strings.Contains(resp.Text, "application") {
		t.Errorf("containers that say nothing are listed:\n%s", resp.Text)
	}
}

func TestLookAnswersWithWhateverCouldBeRead(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	h.f.fail("ax_tree", &Error{Code: CodeServiceUnavailable, Message: "accessibility service"})
	resp, r := h.look()
	if r.Screen != nil || resp.Image == nil || !strings.Contains(resp.Text, "Elements not read: service_unavailable: accessibility service.") {
		t.Fatalf("%s", resp.Text)
	}
	// A dropped link on a read is retried once.
	h.f.fail("ax_tree", &Error{Code: CodeDisconnected})
	if _, r := h.look(); r.Screen == nil {
		t.Fatal("a read after one dropped link was not retried")
	}
	h.f.info.Locked = new(bool)
	*h.f.info.Locked = true
	if resp, _ := h.look(); !strings.Contains(resp.Text, "The phone is locked.") {
		t.Fatalf("%s", resp.Text)
	}
}

func TestDoTapsTheNumberedElementAndListsOnlyWhatChanged(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	h.look()
	after := notesElements()
	after = append(after[:3], el("staticText", "Bread is checked", 0, 164, 390, 44), after[4])
	after[2].Frame.Y = 130
	// The read before the tap still shows Bread; the one after shows the tap's result.
	h.f.set(notes())
	h.f.show(tree(exampleApp, after...))

	resp, r := h.do(`{"steps":[{"action":"tap","target":{"n":4}}],"numbering":1}`)
	if len(r.Steps) != 1 || r.Steps[0].Delivery != DeliverySent {
		t.Fatalf("%s", resp.Text)
	}
	if !contains(h.f.calls(), "tap 195,186 ×1") {
		t.Fatalf("taps the center of Bread (0,164 390×44): %v", h.f.calls())
	}
	for _, want := range []string{`1. tap [4] → cell "Bread" at (195,186): sent`, `+[6] static text "Bread is checked"`,
		`~[3] cell "Milk" (0,130 390×44) from (0,120 390×44)`, "Gone: [4]", "Took "} {
		if !strings.Contains(resp.Text, want) {
			t.Errorf("do text lacks %q:\n%s", want, resp.Text)
		}
	}
	if strings.Contains(resp.Text, `[1] button "Back"`) {
		t.Errorf("unchanged elements are listed again:\n%s", resp.Text)
	}
}

func TestStaleNumbersAreNeverActedOn(t *testing.T) {
	t.Run("no look yet", func(t *testing.T) {
		h := newHarness(t, Options{}, notes())
		_, r := h.do(`{"steps":[{"action":"tap","target":{"n":2}}]}`)
		if r.Steps[0].Delivery != DeliveryNotSent || !strings.Contains(r.Steps[0].Detail, "no element numbers yet") || h.f.count("tap") != 0 {
			t.Fatalf("%+v", r.Steps[0])
		}
	})
	t.Run("an older numbering", func(t *testing.T) {
		h := newHarness(t, Options{}, notes())
		h.look()
		_, r := h.do(`{"steps":[{"action":"tap","target":{"n":2}}],"numbering":7}`)
		if r.Steps[0].Delivery != DeliveryNotSent || !strings.Contains(r.Steps[0].Detail, "numbering 7") || h.f.count("tap") != 0 {
			t.Fatalf("%+v", r.Steps[0])
		}
	})
	t.Run("the screen was replaced since the look", func(t *testing.T) {
		h := newHarness(t, Options{}, notes())
		h.look()
		h.f.set(tree(otherApp, notesElements()...))
		resp, r := h.do(`{"steps":[{"action":"tap","target":{"n":2}}]}`)
		if r.Steps[0].Delivery != DeliveryNotSent || !strings.Contains(r.Steps[0].Detail, "replaced") || h.f.count("tap") != 0 {
			t.Fatalf("%+v", r.Steps[0])
		}
		if !r.Changes.Replaced || !strings.Contains(resp.Text, "The screen was replaced; every element is listed") {
			t.Fatalf("%s", resp.Text)
		}
	})
	t.Run("a step replaces the screen", func(t *testing.T) {
		h := newHarness(t, Options{}, notes())
		h.look()
		h.f.set(notes())
		h.f.show(tree(otherApp, notesElements()...))
		_, r := h.do(`{"steps":[{"action":"tap","target":{"n":1}},{"action":"tap","target":{"n":3}}]}`)
		if r.Steps[0].Delivery != DeliverySent || r.Steps[1].Delivery != DeliveryNotSent || h.f.count("tap") != 1 {
			t.Fatalf("%+v %v", r.Steps, h.f.calls())
		}
	})
	t.Run("the element left", func(t *testing.T) {
		h := newHarness(t, Options{}, notes())
		h.look()
		_, r := h.do(`{"steps":[{"action":"tap","target":{"n":99}}]}`)
		if r.Steps[0].Delivery != DeliveryNotSent || !strings.Contains(r.Steps[0].Detail, "no longer on screen") {
			t.Fatalf("%+v", r.Steps[0])
		}
	})
}

func TestTargetsByLabelAndPoint(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	_, r := h.do(`{"steps":[{"action":"tap","target":{"label":"milk"}},{"action":"tap","target":{"x":20,"y":140}},
		{"action":"tap","target":{"x":5000,"y":10}}]}`)
	if r.Steps[0].Delivery != DeliverySent || r.Steps[0].Target.Label != "Milk" {
		t.Fatalf("%+v", r.Steps[0])
	}
	if r.Steps[1].Delivery != DeliverySent || r.Steps[1].Target == nil || r.Steps[1].Target.Label != "Milk" {
		t.Fatalf("a point is checked against the element under it: %+v", r.Steps[1])
	}
	if !contains(h.f.calls(), "tap 20,140 ×1") {
		t.Fatalf("calls %v", h.f.calls())
	}
	if r.Steps[2].Delivery != DeliveryNotSent || !strings.Contains(r.Steps[2].Detail, "off the screen") {
		t.Fatalf("%+v", r.Steps[2])
	}
	h.f.set(tree(exampleApp, el("button", "Reply", 0, 0, 10, 10), el("button", "Reply", 0, 50, 10, 10)))
	_, r = h.do(`{"steps":[{"action":"tap","target":{"label":"Reply"}}]}`)
	if r.Steps[0].Delivery != DeliveryNotSent || !strings.Contains(r.Steps[0].Detail, `"Reply" matches [`) {
		t.Fatalf("%+v", r.Steps[0])
	}
}

func TestEveryInputGoesToTheBridgeAsWritten(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	resp, r := h.do(`{"steps":[
		{"action":"tap","target":{"label":"Back"},"count":2},
		{"action":"long_press","target":{"label":"Milk"}},
		{"action":"drag","target":{"label":"Milk"},"to":{"label":"Bread"},"hold_ms":300},
		{"action":"swipe","direction":"up"},
		{"action":"swipe","target":{"label":"Milk"},"direction":"right","distance":100,"ms":200},
		{"action":"key","key":"backspace","repeat":3},
		{"action":"button","button":"volume_up"},
		{"action":"launch","app":"com.example.other"},
		{"action":"terminate","app":"com.example.other"},
		{"action":"wait","ms":5}]}`)
	for _, st := range r.Steps {
		if st.Delivery != DeliverySent && st.Delivery != DeliveryDone {
			t.Fatalf("%s", resp.Text)
		}
	}
	var inputs []string
	for _, c := range h.f.calls() {
		if c != "ax_tree" && c != "device_info" && c != "screenshot" {
			inputs = append(inputs, c)
		}
	}
	want := []string{"tap 30,74 ×2", "long_press 195,142 800ms", "drag 195,142 → 195,186 500ms 300ms",
		"swipe 195,422 → 195,0 300ms", "swipe 195,142 → 295,142 200ms", "key backspace ×3", "button volume_up",
		"launch_app com.example.other", "terminate_app com.example.other"}
	if strings.Join(inputs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("inputs\n%s\nwant\n%s", strings.Join(inputs, "\n"), strings.Join(want, "\n"))
	}
}

func TestTypingIntoAField(t *testing.T) {
	field := el("textField", "Title", 0, 100, 390, 40)
	h := newHarness(t, Options{}, tree(exampleApp, field))
	resp, r := h.do(`{"steps":[{"action":"type","text":"short","target":{"label":"Title"}}]}`)
	if st := r.Steps[0]; st.Delivery != DeliverySent || st.At == nil {
		t.Fatalf("%s", resp.Text)
	}
	if got := h.f.calls(); !contains(got, "tap 195,120 ×1") || !contains(got, "keys short") {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(resp.Text, `1. type 5 characters into "Title" → text field "Title" at (195,120): sent`) {
		t.Fatalf("%s", resp.Text)
	}
	// Text the keyboard input can't type is refused before anything, the focusing tap included.
	taps := h.f.count("tap")
	_, r = h.do(`{"steps":[{"action":"type","text":"héllo","target":{"label":"Title"}}]}`)
	if st := r.Steps[0]; st.Delivery != DeliveryNotSent || !strings.Contains(st.Detail, `printable ASCII, tab and newline, and character 2 of the text is none of those; nothing was typed`) ||
		strings.Contains(st.Detail, "é") ||
		h.f.count("tap") != taps {
		t.Fatalf("%+v", st)
	}
	h.f.fail("keys", &Error{Code: CodeUnsupported, Message: "keyboard service"})
	_, r = h.do(`{"steps":[{"action":"type","text":"hello","target":{"label":"Title"}}]}`)
	if st := r.Steps[0]; st.Delivery != DeliveryUnknown || !strings.Contains(st.Detail, "the field was tapped, the text was not sent: the phone did not take it: unsupported") {
		t.Fatalf("%+v", st)
	}
}

func TestAHungInputIsUnknownAndNothingIsSentOnTopOfIt(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	h.look()
	h.f.hang["tap"] = true
	start := time.Now()
	resp, r := h.do(`{"steps":[{"action":"tap","target":{"n":1}},{"action":"button","button":"home"}]}`)
	if took := time.Since(start); took > fastLimits.Call+300*time.Millisecond {
		t.Fatalf("took %s, the call's limit is %s", took, fastLimits.Call)
	}
	if st := r.Steps[0]; st.Delivery != DeliveryUnknown || !strings.Contains(st.Detail, "it may have been sent, so it is not sent again") {
		t.Fatalf("%+v", st)
	}
	if st := r.Steps[1]; st.Delivery != DeliveryNotSent || !strings.Contains(st.Detail, "step 1 did not complete") {
		t.Fatalf("%+v", st)
	}
	if r.Screen == nil {
		t.Fatalf("the read after the steps still happens:\n%s", resp.Text)
	}
	// The tap is still out: the next call sends nothing, but reading still works.
	_, r = h.do(`{"steps":[{"action":"button","button":"home"}]}`)
	if st := r.Steps[0]; st.Delivery != DeliveryNotSent || !strings.Contains(st.Detail, "has not answered yet, and nothing is sent on top of it") {
		t.Fatalf("%+v", st)
	}
	if h.f.count("button") != 0 {
		t.Fatal("pressed on top of an unanswered tap")
	}
	if _, l := h.look(); l.Screen == nil {
		t.Fatal("reading is refused while an input is out")
	}
}

func TestBridgeErrorsSaySentOrNot(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	h.f.fail("button", &Error{Code: CodeBadRequest, Message: "no such button"})
	_, r := h.do(`{"steps":[{"action":"button","button":"home"}]}`)
	if st := r.Steps[0]; st.Delivery != DeliveryNotSent || !strings.Contains(st.Detail, "the phone did not take it: bad_request: no such button") {
		t.Fatalf("%+v", st)
	}
	h.f.fail("launch_app", &Error{Code: CodeDisconnected})
	_, r = h.do(`{"steps":[{"action":"launch","app":"com.example.other"}]}`)
	if st := r.Steps[0]; st.Delivery != DeliveryUnknown || !strings.Contains(st.Detail, "the link to the phone dropped") {
		t.Fatalf("%+v", st)
	}
	if h.f.count("launch_app") != 1 {
		t.Fatal("an input was sent again after a dropped link")
	}
	h.f.fail("device_info", &Error{Code: CodePairingRejected, Message: "pair again"})
	_, r = h.do(`{"steps":[{"action":"button","button":"home"}]}`)
	if st := r.Steps[0]; st.Delivery != DeliveryNotSent || !strings.Contains(st.Detail, "could not be reached: pairing_rejected") {
		t.Fatalf("%+v", st)
	}
}

func TestALockedPhoneOnlyTakesButtons(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	locked := true
	h.f.info.Locked = &locked
	_, r := h.do(`{"steps":[{"action":"button","button":"home"},{"action":"tap","target":{"label":"Back"}}]}`)
	if r.Steps[0].Delivery != DeliverySent || r.Steps[1].Delivery != DeliveryNotSent || !strings.Contains(r.Steps[1].Detail, "locked") {
		t.Fatalf("%+v", r.Steps)
	}
	if h.f.count("tap") != 0 {
		t.Fatal("tapped a locked phone")
	}
	// The phone is read again after a button: unlocked, the next input goes.
	locked = false
	_, r = h.do(`{"steps":[{"action":"button","button":"home"},{"action":"tap","target":{"label":"Back"}}]}`)
	if r.Steps[1].Delivery != DeliverySent {
		t.Fatalf("%+v", r.Steps[1])
	}
	h.f.info = DeviceInfo{Size: screenSize, Capabilities: map[string]bool{"input": false}, Notes: map[string]string{"input": "developer_mode_off"}}
	_, r = h.do(`{"steps":[{"action":"button","button":"home"}]}`)
	if st := r.Steps[0]; st.Delivery != DeliveryNotSent || !strings.Contains(st.Detail, "can't take input now (developer_mode_off)") {
		t.Fatalf("%+v", st)
	}
}

func TestEveryLimitHolds(t *testing.T) {
	slack := 300 * time.Millisecond
	t.Run("the call", func(t *testing.T) {
		h := newHarness(t, Options{}, notes())
		steps := strings.Repeat(`{"action":"wait","ms":100},`, 20)
		start := time.Now()
		_, r := h.do(`{"steps":[` + strings.TrimSuffix(steps, ",") + `]}`)
		if took := time.Since(start); took > fastLimits.Call+slack {
			t.Fatalf("took %s", took)
		}
		if len(r.Steps) != 20 {
			t.Fatalf("every step is reported: %d", len(r.Steps))
		}
		if last := r.Steps[19]; last.Delivery != DeliveryNotSent || !strings.Contains(last.Detail, "time limit") {
			t.Fatalf("%+v", last)
		}
	})
	t.Run("the read before a step", func(t *testing.T) {
		h := newHarness(t, Options{}, notes())
		h.f.hang["ax_tree"] = true
		start := time.Now()
		resp, r := h.do(`{"steps":[{"action":"tap","target":{"label":"Back"}}]}`)
		if took := time.Since(start); took > fastLimits.Call+slack {
			t.Fatalf("took %s", took)
		}
		if r.Steps[0].Delivery != DeliveryNotSent || !strings.Contains(r.Steps[0].Detail, "could not be read") {
			t.Fatalf("%+v", r.Steps[0])
		}
		if r.Screen != nil || !strings.Contains(resp.Text, "Screen not read after the steps") {
			t.Fatalf("%s", resp.Text)
		}
	})
	t.Run("a look", func(t *testing.T) {
		h := newHarness(t, Options{}, notes())
		h.f.hang["ax_tree"], h.f.hang["screenshot"] = true, true
		start := time.Now()
		resp, _ := h.look()
		if took := time.Since(start); took > 3*fastLimits.Primitive+slack {
			t.Fatalf("took %s", took)
		}
		if !strings.Contains(resp.Text, "Elements not read: no answer from the phone within") || resp.Image != nil {
			t.Fatalf("%s", resp.Text)
		}
	})
	t.Run("status", func(t *testing.T) {
		h := newHarness(t, Options{})
		h.f.hang["device_info"] = true
		if resp := h.handle(Request{Op: OpStatus}); !strings.Contains(resp.Text, "did not answer") {
			t.Fatalf("%s", resp.Text)
		}
	})
	t.Run("the caller's context", func(t *testing.T) {
		h := newHarness(t, Options{Limits: Limits{Primitive: time.Minute, Action: time.Minute, Call: time.Hour}}, notes())
		h.f.hang["tap"] = true
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()
		start := time.Now()
		resp := h.c.Handle(ctx, h.f, Request{Device: "phone-1", Session: "chat-1", Op: OpDo,
			Args: json.RawMessage(`{"steps":[{"action":"tap","target":{"label":"Back"}}]}`)})
		if took := time.Since(start); took > slack+80*time.Millisecond {
			t.Fatalf("took %s after the caller gave up", took)
		}
		if !strings.Contains(resp.Text, "unknown") {
			t.Fatalf("%s", resp.Text)
		}
	})
}

func TestOneCallAtATimePerPhone(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	h.f.delay["tap"] = 100 * time.Millisecond
	h.f.started = make(chan string, 64)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.do(`{"steps":[{"action":"tap","target":{"label":"Back"}}]}`)
	}()
	for op := range h.f.started {
		if op == "tap" {
			break
		}
	}
	// Another conversation on the same phone waits, then says the phone is busy.
	_, r := h.do(`{"steps":[{"action":"button","button":"home"}]}`, func(r *Request) { r.Session = "chat-2" })
	if st := r.Steps[0]; st.Delivery != DeliveryNotSent || !strings.Contains(st.Detail, busyNote) {
		t.Fatalf("%+v", st)
	}
	if resp := h.handle(Request{Op: OpStatus}); !strings.Contains(resp.Text, "A look or do is running.") {
		t.Fatalf("%s", resp.Text)
	}
	wg.Wait()
	if _, l := h.look(); l.Screen == nil {
		t.Fatalf("after the call: %v", l.Notes)
	}
}

func TestSessionsKeepTheirOwnNumbers(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	h.look()
	_, r := h.do(`{"steps":[{"action":"tap","target":{"n":1}}]}`, func(r *Request) { r.Session = "chat-2" })
	if st := r.Steps[0]; st.Delivery != DeliveryNotSent || !strings.Contains(st.Detail, "no element numbers yet") {
		t.Fatalf("another conversation used this one's numbers: %+v", st)
	}
}

func TestStatus(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	h.f.info = DeviceInfo{Name: "Test iPhone", ProductType: "iPhone16,2", OSVersion: "18.1", Size: screenSize}
	resp := h.handle(Request{Op: OpStatus})
	for _, want := range []string{"Phone: Test iPhone (iPhone16,2, iOS 18.1)", "Latest read: none yet."} {
		if !strings.Contains(resp.Text, want) {
			t.Fatalf("%s", resp.Text)
		}
	}
	h.look()
	resp = h.handle(Request{Op: OpStatus})
	if !strings.Contains(resp.Text, "Latest read: numbering 1 · 5 elements in Notes (com.example.notes)") ||
		!strings.Contains(resp.Text, "Last call: look: 5 elements") {
		t.Fatalf("%s", resp.Text)
	}
}

func TestBadRequests(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	for _, c := range []struct {
		req  Request
		want string
	}{
		{Request{Op: "fly"}, "unknown op"},
		{Request{Op: OpDo, Args: json.RawMessage(`{"steps":[{"action":"tap"}]}`)}, "nothing was sent: step 1 (tap): needs a target"},
		{Request{Op: OpDo, Args: json.RawMessage(`{"steps":[{"action":"tap","target":{"n":1},"approved":true}]}`)}, "unknown field"},
		{Request{Op: OpLook, Args: json.RawMessage(`{"screenshot":"yes"}`)}, "bad arguments"},
	} {
		if resp := h.handle(c.req); !strings.Contains(resp.Text, c.want) {
			t.Errorf("%+v: %s", c.req, resp.Text)
		}
	}
	if h.f.count("tap") != 0 {
		t.Fatal("a bad request sent input")
	}
}

func TestNothingIsSentAfterTheCallerGaveUp(t *testing.T) {
	h := newHarness(t, Options{Limits: Limits{Primitive: time.Minute, Action: time.Minute, Call: time.Hour}}, notes())
	h.f.delay["launch_app"] = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	resp := h.c.Handle(ctx, h.f, Request{Device: "phone-1", Session: "chat-1", Op: OpDo,
		Args: json.RawMessage(`{"steps":[{"action":"launch","app":"com.example.other"},{"action":"button","button":"home"}]}`)})
	var r DoResult
	if err := json.Unmarshal(resp.Output, &r); err != nil {
		t.Fatal(err)
	}
	if r.Steps[0].Delivery != DeliveryUnknown || r.Steps[1].Delivery != DeliveryNotSent || h.f.count("button") != 0 {
		t.Fatalf("%s", resp.Text)
	}
}

func TestADeadInputStopsHoldingThePhoneBack(t *testing.T) {
	l := fastLimits
	l.Stuck = 100 * time.Millisecond
	h := newHarness(t, Options{Limits: l}, notes())
	h.f.hang["launch_app"] = true
	h.do(`{"steps":[{"action":"launch","app":"com.example.other"}]}`)
	if _, r := h.do(`{"steps":[{"action":"button","button":"home"}]}`); r.Steps[0].Delivery != DeliveryNotSent {
		t.Fatalf("%+v", r.Steps[0])
	}
	if resp := h.handle(Request{Op: OpStatus}); !strings.Contains(resp.Text, "has not answered yet") {
		t.Fatalf("%s", resp.Text)
	}
	time.Sleep(120 * time.Millisecond) // sleep-ok: past Limits.Stuck
	if _, r := h.do(`{"steps":[{"action":"button","button":"home"}]}`); r.Steps[0].Delivery != DeliverySent {
		t.Fatalf("%+v", r.Steps[0])
	}
}

func TestElementsWithoutAFrameAreNotTouched(t *testing.T) {
	h := newHarness(t, Options{}, tree(exampleApp, el("button", "Ghost", 0, 0, 0, 0), el("button", "Back", 8, 52, 44, 44)))
	h.look()
	_, r := h.do(`{"steps":[{"action":"tap","target":{"label":"Ghost"}}]}`)
	if st := r.Steps[0]; st.Delivery != DeliveryNotSent || !strings.Contains(st.Detail, "has no frame") || h.f.count("tap") != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestASecureFieldsValueGoesNowhere(t *testing.T) {
	secure := el(RoleSecureTextField, "Password", 0, 100, 390, 40)
	secure.Value = "hunter22"
	h := newHarness(t, Options{}, tree(exampleApp, secure))
	resp, _ := h.look()
	if strings.Contains(resp.Text, "hunter22") || strings.Contains(string(resp.Output), "hunter22") || !strings.Contains(resp.Text, "= (hidden)") {
		t.Fatalf("%s\n%s", resp.Text, resp.Output)
	}
}

func TestEachInputSaysWhetherTheScreenChanged(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	resp, r := h.do(`{"steps":[{"action":"button","button":"home"}]}`)
	if st := r.Steps[0]; st.Delivery != DeliverySent || st.Effect != EffectChanged {
		t.Fatalf("%s", resp.Text)
	}
	if !strings.Contains(resp.Text, "1. button home: sent, screen changed") || strings.Contains(strings.ToLower(resp.Text), "confirmed") {
		t.Fatalf("%s", resp.Text)
	}
	h.f.inert = true
	resp, r = h.do(`{"steps":[{"action":"tap","target":{"label":"Back"}}]}`)
	if st := r.Steps[0]; st.Effect != EffectUnchanged || !strings.Contains(resp.Text, "sent, no visible change") {
		t.Fatalf("%s", resp.Text)
	}
	// A screen that only differs by noise (the clock in the status bar) is no change.
	a, b := signature(Image{Data: screenPNG(0)}), signature(Image{Data: screenPNG(0)})
	b[0], b[1], b[2], b[3] = 255, 255, 255, 255
	if differs(a, b) {
		t.Fatal("the status bar counts as a change")
	}
	if !differs(a, signature(Image{Data: screenPNG(1)})) {
		t.Fatal("another screen counts as no change")
	}
	h.f.info.Capabilities = map[string]bool{"screenshot": false}
	_, r = h.do(`{"steps":[{"action":"button","button":"home"}]}`)
	if st := r.Steps[0]; st.Delivery != DeliverySent || st.Effect != EffectUnknown {
		t.Fatalf("%+v", st)
	}
}
