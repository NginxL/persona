package iphonectl

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func payTree() Tree {
	return tree(exampleApp, el("heading", "Checkout", 0, 50, 390, 40), el("textField", "Note", 0, 400, 390, 40),
		el("button", "Pay", 20, 760, 350, 50))
}

const payThenHome = `{"steps":[{"action":"tap","target":{"label":"Note"}},{"action":"tap","target":{"n":3}},{"action":"button","button":"home"}]}`

func TestASensitiveStepStopsTheDoUntilTheOwnerApproves(t *testing.T) {
	h := newHarness(t, Options{}, payTree())
	h.look()
	resp, r := h.do(payThenHome)
	if !resp.NeedsApproval || resp.ApprovalID == "" || resp.Approval == nil {
		t.Fatalf("%+v", resp)
	}
	// The safe step before it went; the sensitive one and the rest did not.
	if r.Steps[0].Delivery != DeliverySent || r.Steps[1].Delivery != DeliveryNotSent || !r.Steps[1].Waiting ||
		r.Steps[2].Delivery != DeliveryNotSent || h.f.count("tap") != 1 || h.f.count("button") != 0 {
		t.Fatalf("%s\n%v", resp.Text, h.f.calls())
	}
	a := resp.Approval
	if a.Step != 2 || a.Action != ActTap || !slices.Equal(a.Needs, []Sensitivity{Payment}) || a.App != exampleApp ||
		a.Target == nil || a.Target.Label != "Pay" || a.Target.N != 3 {
		t.Fatalf("%+v", a)
	}
	// The id is for the hub only.
	if strings.Contains(resp.Text, resp.ApprovalID) || strings.Contains(string(resp.Output), resp.ApprovalID) {
		t.Fatal("the approval id reaches the model")
	}
	if !strings.Contains(resp.Text, "2. tap [3] → button \"Pay\": not_sent") || !strings.Contains(resp.Text, "waits for the owner's approval (payment)") {
		t.Fatalf("%s", resp.Text)
	}

	resp, r = h.do(`{}`, approved(resp.ApprovalID))
	if resp.NeedsApproval || len(r.Steps) != 2 || r.Steps[0].Step != 2 || r.Steps[0].Delivery != DeliverySent ||
		!r.Steps[0].Approved || r.Steps[1].Step != 3 || r.Steps[1].Delivery != DeliverySent {
		t.Fatalf("%s", resp.Text)
	}
	if !contains(h.f.calls(), "tap 195,785 ×1") || h.f.count("button") != 1 {
		t.Fatalf("%v", h.f.calls())
	}
	if !strings.Contains(resp.Text, "approved by the owner") {
		t.Fatalf("%s", resp.Text)
	}
}

func TestAnApprovalIsUsedOnceAndOnlyAsIssued(t *testing.T) {
	t.Run("a made-up id", func(t *testing.T) {
		h := newHarness(t, Options{}, payTree())
		h.look()
		h.do(payThenHome)
		resp, r := h.do(`{}`, approved("0123456789abcdef0123456789abcdef"))
		if len(r.Steps) != 0 || !strings.Contains(resp.Text, "not pending") || h.f.count("tap") != 1 {
			t.Fatalf("%s", resp.Text)
		}
	})
	t.Run("used twice", func(t *testing.T) {
		h := newHarness(t, Options{}, payTree())
		h.look()
		resp, _ := h.do(payThenHome)
		id := resp.ApprovalID
		h.do(`{}`, approved(id))
		resp, _ = h.do(`{}`, approved(id))
		if !strings.Contains(resp.Text, "not pending") || h.f.count("tap") != 2 {
			t.Fatalf("%s %v", resp.Text, h.f.calls())
		}
	})
	t.Run("replaced by a newer do", func(t *testing.T) {
		h := newHarness(t, Options{}, payTree())
		h.look()
		resp, _ := h.do(payThenHome)
		h.do(`{"steps":[{"action":"wait","ms":1}]}`)
		if resp, _ = h.do(`{}`, approved(resp.ApprovalID)); !strings.Contains(resp.Text, "not pending") {
			t.Fatalf("%s", resp.Text)
		}
	})
	t.Run("another conversation", func(t *testing.T) {
		h := newHarness(t, Options{}, payTree())
		h.look()
		resp, _ := h.do(payThenHome)
		if resp, _ = h.do(`{}`, approved(resp.ApprovalID), func(r *Request) { r.Session = "chat-2" }); !strings.Contains(resp.Text, "not pending") {
			t.Fatalf("%s", resp.Text)
		}
	})
	t.Run("expired", func(t *testing.T) {
		l := fastLimits
		l.Pending = 20 * time.Millisecond
		h := newHarness(t, Options{Limits: l}, payTree())
		h.look()
		resp, _ := h.do(payThenHome)
		time.Sleep(40 * time.Millisecond) // sleep-ok: past the pending limit
		if resp, _ = h.do(`{}`, approved(resp.ApprovalID)); !strings.Contains(resp.Text, "expired") || h.f.count("tap") != 1 {
			t.Fatalf("%s", resp.Text)
		}
	})
	t.Run("the screen changed meanwhile", func(t *testing.T) {
		h := newHarness(t, Options{}, payTree())
		h.look()
		resp, _ := h.do(payThenHome)
		changed := payTree()
		changed.Root.Children[0].Children[2].Label = "Pay $999"
		h.f.set(changed)
		resp, r := h.do(`{}`, approved(resp.ApprovalID))
		if st := r.Steps[0]; st.Delivery != DeliveryNotSent || h.f.count("tap") != 1 {
			t.Fatalf("%s", resp.Text)
		}
	})
	t.Run("the amount changed meanwhile", func(t *testing.T) {
		pay := el("button", "Pay $1.00", 20, 760, 350, 50)
		pay.Identifier = "buyButton"
		h := newHarness(t, Options{}, tree(exampleApp, pay))
		h.look()
		resp, _ := h.do(`{"steps":[{"action":"tap","target":{"n":1}}]}`)
		if !resp.NeedsApproval {
			t.Fatalf("%s", resp.Text)
		}
		pay.Label = "Pay $500.00"
		h.f.set(tree(exampleApp, pay))
		if _, r := h.do(`{}`, approved(resp.ApprovalID)); r.Steps[0].Delivery != DeliveryNotSent || h.f.count("tap") != 0 {
			t.Fatalf("%+v", r.Steps[0])
		}
	})
	t.Run("the keyboard moved meanwhile", func(t *testing.T) {
		secure := el(RoleSecureTextField, "Password", 0, 100, 390, 40)
		secure.Focused = true
		plain := el("textField", "Message", 0, 200, 390, 40)
		h := newHarness(t, Options{}, tree(exampleApp, secure, plain))
		resp, _ := h.do(`{"steps":[{"action":"type","text":"hunter22"}]}`)
		if !resp.NeedsApproval || !slices.Equal(resp.Approval.Needs, []Sensitivity{Password}) {
			t.Fatalf("%+v", resp)
		}
		secure.Focused, plain.Focused = false, true
		h.f.set(tree(exampleApp, secure, plain))
		resp, r := h.do(`{}`, approved(resp.ApprovalID))
		if r.Steps[0].Delivery != DeliveryNotSent || h.f.count("keys") != 0 {
			t.Fatalf("an approved password went to another field:\n%s", resp.Text)
		}
	})
}

func TestWhatTheOwnerIsShown(t *testing.T) {
	t.Run("a password only by its length", func(t *testing.T) {
		secure := el(RoleSecureTextField, "Password", 0, 100, 390, 40)
		secure.Focused = true
		h := newHarness(t, Options{}, tree(exampleApp, secure))
		resp, _ := h.do(`{"steps":[{"action":"type","text":"hunter22"}]}`)
		if a := resp.Approval; a == nil || a.Text != "" || a.TextLen != 8 {
			t.Fatalf("%+v", a)
		}
		if strings.Contains(resp.Text, "hunter22") || strings.Contains(string(resp.Output), "hunter22") {
			t.Fatal("the result repeats the password")
		}
		resp, _ = h.do(`{}`, approved(resp.ApprovalID))
		if !contains(h.f.calls(), "keys hunter22") {
			t.Fatalf("%s", resp.Text)
		}
	})
	t.Run("a message with its text", func(t *testing.T) {
		field := el("textField", "Message", 0, 700, 390, 40)
		field.Focused = true
		h := newHarness(t, Options{}, tree(smsApp, field))
		resp, _ := h.do(`{"steps":[{"action":"type","text":"on my way\n"}]}`)
		if a := resp.Approval; a == nil || a.Text != "on my way\n" || a.App != smsApp || h.f.count("keys") != 0 {
			t.Fatalf("%+v", resp.Approval)
		}
	})
	t.Run("Persona's own app is refused, not asked about", func(t *testing.T) {
		h := newHarness(t, Options{}, tree(selfApp, el("button", "Approve", 0, 0, 100, 40)))
		resp, r := h.do(`{"steps":[{"action":"tap","target":{"label":"Approve"}}]}`, withConfirm)
		if resp.NeedsApproval || !strings.HasPrefix(r.Steps[0].Detail, "refused:") || h.f.count("tap") != 0 {
			t.Fatalf("%s", resp.Text)
		}
	})
}

func TestConfirmAsksOnceForTheWholeDo(t *testing.T) {
	h := newHarness(t, Options{}, payTree())
	const steps = `{"steps":[{"action":"wait","ms":1},{"action":"tap","target":{"label":"Note"}},{"action":"tap","target":{"label":"Checkout"}},{"action":"tap","target":{"label":"Pay"}}]}`
	resp, r := h.do(steps, withConfirm)
	if !resp.NeedsApproval || r.Steps[0].Delivery != DeliveryDone || r.Steps[1].Delivery != DeliveryNotSent || h.f.count("tap") != 0 {
		t.Fatalf("%s", resp.Text)
	}
	if a := resp.Approval; !slices.Equal(a.Needs, []Sensitivity{Confirm}) || a.Step != 2 || len(a.Steps) != 3 || a.Steps[2].Target.Label != "Pay" {
		t.Fatalf("%+v", a)
	}
	// Confirmed: the plain taps go, the payment still stops on its own.
	resp, r = h.do(`{}`, approved(resp.ApprovalID), withConfirm)
	if !resp.NeedsApproval || r.Steps[0].Delivery != DeliverySent || r.Steps[1].Delivery != DeliverySent || h.f.count("tap") != 2 {
		t.Fatalf("%s", resp.Text)
	}
	if a := resp.Approval; a.Step != 4 || !slices.Equal(a.Needs, []Sensitivity{Payment}) {
		t.Fatalf("%+v", a)
	}
	resp, _ = h.do(`{}`, approved(resp.ApprovalID), withConfirm)
	if resp.NeedsApproval || h.f.count("tap") != 3 {
		t.Fatalf("%s", resp.Text)
	}
}

func TestABusyPhoneLeavesTheApprovalPending(t *testing.T) {
	h := newHarness(t, Options{}, payTree())
	h.look()
	resp, _ := h.do(payThenHome)
	id := resp.ApprovalID
	h.f.mu.Lock()
	h.f.delay["tap"] = 50 * time.Millisecond
	h.f.started = make(chan string, 64)
	h.f.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.do(`{"steps":[{"action":"tap","target":{"label":"Checkout"}}]}`, func(r *Request) { r.Session = "chat-2" })
	}()
	for op := range h.f.started {
		if op == "tap" {
			break
		}
	}
	if resp, _ := h.do(`{}`, approved(id)); !strings.Contains(resp.Text, "the approval is still pending") || resp.Refused != RefusedBusy {
		t.Fatalf("%s", resp.Text)
	}
	<-done
	h.f.mu.Lock()
	h.f.started, h.f.delay["tap"] = nil, 0
	h.f.mu.Unlock()
	if resp, r := h.do(`{}`, approved(id)); len(r.Steps) != 2 || r.Steps[0].Delivery != DeliverySent {
		t.Fatalf("%s", resp.Text)
	}
}

func TestTheOwnersConfirmSettingIsTheOneTheDoStoppedUnder(t *testing.T) {
	h := newHarness(t, Options{}, payTree())
	resp, _ := h.do(`{"steps":[{"action":"tap","target":{"label":"Note"}}]}`, withConfirm)
	if !resp.NeedsApproval {
		t.Fatalf("%s", resp.Text)
	}
	// Turned off before approving: the approval still resumes the step.
	if resp, r := h.do(`{}`, approved(resp.ApprovalID)); r.Steps[0].Delivery != DeliverySent {
		t.Fatalf("%s", resp.Text)
	}
}

func TestSelfAppsFromRequestsStay(t *testing.T) {
	h := newHarness(t, Options{Rules: Rules{SelfApps: []string{}}}, tree(selfApp, el("button", "Approve", 0, 0, 100, 40)))
	h.look()
	tap := `{"steps":[{"action":"tap","target":{"label":"Approve"}}]}`
	// The hub names Persona's app: no step touches it.
	resp, r := h.do(tap, func(r *Request) { r.SelfApps = []string{" " + selfApp.BundleID + " "} })
	if len(r.Steps) != 1 || r.Steps[0].Delivery != DeliveryNotSent || !strings.Contains(r.Steps[0].Detail, "Persona's own app") ||
		h.f.count("tap") != 0 || resp.Refused != "" {
		t.Fatalf("%s", resp.Text)
	}
	// A later call that doesn't name it (a resume) keeps the rule.
	if _, r = h.do(tap); r.Steps[0].Delivery != DeliveryNotSent || h.f.count("tap") != 0 {
		t.Fatalf("%+v", r.Steps)
	}
	if got := h.c.ruleSet().SelfApps; len(got) != 1 {
		t.Fatalf("listed once: %v", got)
	}
}

func TestACallTurnedDownAsAWholeSaysSo(t *testing.T) {
	h := newHarness(t, Options{}, payTree())
	h.look()
	if resp, _ := h.do(`{}`, approved("0123456789abcdef0123456789abcdef")); resp.Refused != RefusedNotPending || !strings.Contains(resp.Text, "othing was sent") {
		t.Fatalf("an approval not pending: %+v", resp)
	}
	if resp := h.handle(Request{Op: OpDo, Args: json.RawMessage(`{"steps":[{"action":"fly"}]}`)}); resp.Refused != RefusedArgs {
		t.Fatalf("bad steps: %+v", resp)
	}
	if resp, _ := h.do(`{"steps":[{"action":"tap","target":{"label":"Pay"}}]}`); resp.Refused != "" || !resp.NeedsApproval {
		t.Fatalf("a do that ran: %+v", resp)
	}
	if resp, _ := h.look(); resp.Refused != "" {
		t.Fatal("a look")
	}
}

func TestConfirmNamesTheStepsTargetsAsOnScreen(t *testing.T) {
	h := newHarness(t, Options{}, payTree())
	h.look()
	// By number, by a label only one element has, by a label several have, and a point.
	resp, _ := h.do(`{"steps":[{"action":"tap","target":{"n":2}},{"action":"tap","target":{"label":"pay"}},`+
		`{"action":"tap","target":{"label":"o"}},{"action":"tap","target":{"x":10,"y":20}}]}`, withConfirm)
	a := resp.Approval
	if a == nil || len(a.Steps) != 4 {
		t.Fatalf("%+v", resp)
	}
	if got := *a.Steps[0].Target; got != (ElementRef{N: 2, Role: "textField", Label: "Note"}) {
		t.Errorf("by number: %+v", got)
	}
	if got := *a.Steps[1].Target; got != (ElementRef{N: 3, Role: "button", Label: "Pay"}) {
		t.Errorf("by label: %+v", got)
	}
	if got := *a.Steps[2].Target; got != (ElementRef{Label: "o"}) {
		t.Errorf("an ambiguous label stays as written: %+v", got)
	}
	if a.Steps[3].Target != nil || a.Steps[3].At == nil || *a.Steps[3].At != (Point{10, 20}) {
		t.Errorf("a point: %+v", a.Steps[3])
	}
}
