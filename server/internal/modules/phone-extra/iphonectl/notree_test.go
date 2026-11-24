package iphonectl

import (
	"slices"
	"strings"
	"testing"
)

// The bridge's v1 gives no element tree: the assistant acts on points read off screenshots, and since the rules
// can't see what anything hits, every input waits for the owner, each on its own (fail-closed).
func noTreeHarness(t *testing.T) *harness {
	h := newHarness(t, Options{}, notes())
	h.f.info.Capabilities = map[string]bool{"screenshot": true, "ax_tree": false, "input": true, "apps": true}
	h.f.info.Notes = map[string]string{"ax_tree": CodeUnsupported}
	return h
}

func TestLookWithoutAnElementTree(t *testing.T) {
	h := noTreeHarness(t)
	resp, r := h.look()
	if r.Screen != nil || resp.Image == nil || h.f.count("ax_tree") != 0 {
		t.Fatalf("%s %v", resp.Text, h.f.calls())
	}
	if !strings.Contains(resp.Text, "this phone can't give its elements (unsupported), so act on points (x, y) read off the screenshot, in points of a 390×844 screen") {
		t.Fatalf("%s", resp.Text)
	}
	if !strings.Contains(resp.Text, "Points = screenshot pixels × 97.5000 (x: 390 pt over 4 px, y: 844 pt over 8 px).") {
		t.Fatalf("%s", resp.Text)
	}
}

func TestEveryInputWaitsForTheOwnerWithoutAnElementTree(t *testing.T) {
	h := noTreeHarness(t)
	_, r := h.do(`{"steps":[{"action":"tap","target":{"n":1}}]}`)
	if st := r.Steps[0]; st.Delivery != DeliveryNotSent || !strings.Contains(st.Detail, "can't name one") {
		t.Fatalf("%+v", st)
	}
	// A wait goes; each input after it stops for its own approval, launching and buttons included.
	resp, r := h.do(`{"steps":[{"action":"wait","ms":1},{"action":"launch","app":"com.example.other"},{"action":"tap","target":{"x":100,"y":200}},{"action":"type","text":"hi"}]}`)
	if r.Steps[0].Delivery != DeliveryDone || !resp.NeedsApproval || r.Steps[1].Delivery != DeliveryNotSent || h.f.count("launch_app") != 0 {
		t.Fatalf("%s", resp.Text)
	}
	if a := resp.Approval; !slices.Equal(a.Needs, []Sensitivity{Unchecked}) || a.Step != 2 || a.LaunchApp != "com.example.other" || len(a.Steps) != 0 {
		t.Fatalf("%+v", a)
	}
	if resp.Image == nil {
		t.Fatal("the owner is asked without the screen they approve against")
	}
	want := []struct {
		step int
		at   *Point
		text string
	}{{3, &Point{100, 200}, ""}, {4, nil, "hi"}}
	for _, w := range want {
		resp, r = h.do(`{}`, approved(resp.ApprovalID))
		if r.Steps[0].Delivery != DeliverySent || r.Steps[0].Effect != EffectChanged || !resp.NeedsApproval {
			t.Fatalf("%s", resp.Text)
		}
		a := resp.Approval
		if a.Step != w.step || !slices.Equal(a.Needs, []Sensitivity{Unchecked}) || (w.at != nil && (a.At == nil || *a.At != *w.at)) || a.Text != w.text {
			t.Fatalf("%+v", a)
		}
	}
	resp, r = h.do(`{}`, approved(resp.ApprovalID))
	if resp.NeedsApproval || r.Steps[0].Delivery != DeliverySent || resp.Image == nil {
		t.Fatalf("%s", resp.Text)
	}
	if got := h.f.calls(); !contains(got, "launch_app com.example.other") || !contains(got, "tap 100,200 ×1") || !contains(got, "keys hi") ||
		h.f.count("ax_tree") != 0 {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(resp.Text, "Elements not read: this phone can't give its elements") || !strings.Contains(resp.Text, "Screenshot attached") {
		t.Fatalf("%s", resp.Text)
	}
}

func TestAnApprovalWithoutATreeIsForTheScreenTheOwnerSaw(t *testing.T) {
	h := noTreeHarness(t)
	resp, _ := h.do(`{"steps":[{"action":"tap","target":{"x":100,"y":200}}]}`)
	h.f.mu.Lock()
	h.f.screen += 5 // something else changed the screen while the owner looked
	h.f.mu.Unlock()
	if resp, r := h.do(`{}`, approved(resp.ApprovalID)); r.Steps[0].Delivery != DeliveryNotSent || h.f.count("tap") != 0 {
		t.Fatalf("%s", resp.Text)
	}
}

func TestATreeTheBridgeTurnsOutNotToHave(t *testing.T) {
	h := newHarness(t, Options{}, notes())
	h.f.fail("ax_tree", &Error{Code: CodeUnsupported}, &Error{Code: CodeUnsupported})
	resp, _ := h.do(`{"steps":[{"action":"tap","target":{"x":10,"y":10}}]}`)
	if !resp.NeedsApproval || !slices.Equal(resp.Approval.Needs, []Sensitivity{Unchecked}) {
		t.Fatalf("%s", resp.Text)
	}
}
