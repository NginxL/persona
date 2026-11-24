package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/update"
)

type phoneCallSeen struct {
	Method string
	Call   proto.IPhoneCall
	Raw    json.RawMessage // the params of a job's call (iphone.install)
}

// iphoneMac is a Mac whose node reaches the paired iPhones devices, answering iphone.status and iphone.call as the
// #3452 contract says; answer, when set, answers phone.call instead of the default (look: a screen, do: done). It
// says which calls it was asked.
func iphoneMac(t *testing.T, h *Manager, id string, devices []proto.IPhoneDevice, answer func(proto.IPhoneCall) (proto.IPhoneResult, error)) (*Machine, func() []phoneCallSeen) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	var mu sync.Mutex
	var seen []phoneCallSeen
	// The node's iphoneStatus includes the current job, so a status answered after a pair cannot erase it.
	var job *proto.ComputerJob
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "plugin.node.call" {
			t.Errorf("%s by its name: the iphone program is asked over plugin.node.call", method)
			return nil, &rpc.Error{Message: "unknown method " + method}
		}
		var c proto.PluginNodeCall
		_ = json.Unmarshal(params, &c)
		if c.ID != iphoneExtensionID {
			t.Errorf("plugin.node.call %s.%s", c.ID, c.Method)
		}
		method, params = "iphone."+c.Method, c.Params
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "iphone.status":
			seen = append(seen, phoneCallSeen{Method: method})
			return proto.IPhone{Devices: devices, Checked: time.Now(), Enabled: true, Job: job}, nil // Enabled is never the node's
		case "iphone.call":
			var p proto.IPhoneCall
			_ = json.Unmarshal(params, &p)
			seen = append(seen, phoneCallSeen{Method: method, Call: p})
			if answer != nil {
				r, err := answer(p)
				if err != nil {
					return nil, err
				}
				return r, nil
			}
			if p.Op == "look" {
				return proto.IPhoneResult{Output: json.RawMessage(`{"elements":"[1] button \"Settings\""}`), Image: &proto.Image{Mime: "image/jpeg", Data: "AAAA"}}, nil
			}
			return proto.IPhoneResult{Output: json.RawMessage(`{"done":true}`)}, nil
		case "iphone.pair", "iphone.forget", "iphone.cancel", "iphone.install", "iphone.prepare":
			seen = append(seen, phoneCallSeen{Method: method, Raw: params})
			kind := "pair"
			switch method {
			case "iphone.install":
				kind = "install"
			case "iphone.prepare":
				kind = "prepare"
			}
			job = &proto.ComputerJob{Kind: kind, State: "running"}
			return proto.IPhone{Devices: devices, Checked: time.Now(), Job: job}, nil
		}
		return nil, &rpc.Error{Message: "unknown method " + method}
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(h, MachineConfig{ID: id, Name: id})
	m.peer, m.online, m.info = hubSide, true, proto.NodeInfo{OS: "darwin", BuiltinPrograms: []string{"cua", "ego", "imessage", iphoneExtensionID}}
	h.fleetMu.Lock()
	h.machines, h.byID[id] = append(h.machines, m), m
	h.fleetMu.Unlock()
	return m, func() []phoneCallSeen {
		mu.Lock()
		defer mu.Unlock()
		return append([]phoneCallSeen(nil), seen...)
	}
}

// lastIPhoneCall is the last iphone.call the node got (a refresh's iphone.status may land after it).
func lastIPhoneCall(t *testing.T, calls []phoneCallSeen) phoneCallSeen {
	t.Helper()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Method == "iphone.call" {
			return calls[i]
		}
	}
	t.Fatal("no iphone.call")
	return phoneCallSeen{}
}

func shownIPhone(t *testing.T, h *Manager, m *Machine) *proto.IPhone {
	t.Helper()
	x, ok := h.report().Machines[m.cfg.ID]
	if !ok || len(x.IPhone) == 0 {
		return nil
	}
	var p proto.IPhone
	if err := json.Unmarshal(x.IPhone, &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

var testIPhone = proto.IPhoneDevice{ID: "dev-a1", Name: "Test iPhone", ProductType: "iPhone17,1", IOS: "26.1", Connected: true, Ready: true}

// Off until turned on, then Mac by Mac and iPhone by iPhone: the tools come only once a Mac is on and a paired
// iPhone consented from its own app, and only that iPhone is ever operated (fail-closed).
func TestIPhoneExtension(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	mux := h.routes()
	other := proto.IPhoneDevice{ID: "dev-b2", Name: "Other iPhone", ProductType: "iPhone16,2", IOS: "26.0", Connected: true, Ready: true}
	mac, seen := iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone, other}, nil)
	phone := deviceToken("dev_phone", roleFull)

	if p := h.extensions.reg.Get(iphoneExtensionID); p == nil || p.Record.Enabled {
		t.Fatalf("built in and off: %+v", p)
	}
	if shownIPhone(t, h, mac) != nil || strings.Contains(toolNames(toolsForAgents(t, h, true)), "iphone_") {
		t.Fatal("off: nothing of it")
	}
	h.refreshIPhone(ctx, mac)
	if len(seen()) != 0 {
		t.Fatal("the node is not asked while it is off")
	}
	if code, out := call(t, mux, "GET", "/api/v1/iphone", phone, ""); code != 200 || out["on"] != false {
		t.Fatalf("off: %d %v", code, out)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/iphone/consent", phone, `{"device": "dev-a1", "allowed": true}`); code != 404 {
		t.Fatalf("no consent while it is off: %d", code)
	}

	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	// Every Mac is asked while it is on: the iPhone's own app finds itself among them before its Mac is on.
	code, out := call(t, mux, "GET", "/api/v1/iphone", phone, "")
	devs, _ := out["devices"].([]any)
	if code != 200 || out["on"] != true || len(devs) != 2 {
		t.Fatalf("%d %v", code, out)
	}
	if d := devs[0].(map[string]any); d["id"] != "dev-a1" || d["product_type"] != "iPhone17,1" || d["consented"] != false || d["allowed"] != false {
		t.Fatalf("%v", d)
	}
	if p := shownIPhone(t, h, mac); p == nil || p.Enabled || len(p.Devices) != 2 || p.Devices[0].Allowed {
		t.Fatalf("not on for this Mac, and the node's word is not taken for it: %+v", p)
	}

	// The Mac on, nobody consented: still nothing to operate.
	code, out = call(t, mux, "PUT", "/api/v1/machines/mac/iphone", h.cfg.Token, `{"enabled": true}`)
	if code != 200 || out["enabled"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if h.iphoneTools() != nil {
		t.Fatal("no iPhone consented: no tools")
	}
	if res := h.callIPhoneTool(ctx, "conv1234x", "iphone_look", json.RawMessage(`{}`)); !res.IsError || !strings.Contains(res.Content[0].Text, "allowed: none") {
		t.Fatalf("fail-closed: %+v", res)
	}

	// Only the user's own signed-in app gives an iPhone's consent: never the master token.
	if code, _ := call(t, mux, "PUT", "/api/v1/iphone/consent", h.cfg.Token, `{"device": "dev-a1", "allowed": true}`); code != 403 {
		t.Fatalf("master token: %d", code)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/iphone/consent", phone, `{"device": " ", "allowed": true}`); code != 400 {
		t.Fatalf("no device: %d", code)
	}
	code, out = call(t, mux, "PUT", "/api/v1/iphone/consent", phone, `{"device": "dev-a1", "allowed": true}`)
	if code != 200 || out["consented"] != true || out["allowed"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if got := toolNames(toolsForAgents(t, h, true)); !strings.Contains(got, "iphone_status iphone_look iphone_do") {
		t.Fatal(got)
	}
	if strings.Contains(toolNames(toolsForAgents(t, h, false)), "iphone_") {
		t.Fatal("the assistant's only")
	}
	if p := shownIPhone(t, h, mac); !p.Devices[0].Allowed || p.Devices[1].Allowed {
		t.Fatalf("only the one that consented: %+v", p.Devices)
	}

	// The one allowed is the default; the other one is never operated, by id or name.
	res := h.callIPhoneTool(ctx, "conv1234x", "iphone_look", json.RawMessage(`{}`))
	if res.IsError || len(res.Content) != 2 || res.Content[1].Type != "image" || !strings.Contains(res.Content[0].Text, "Settings") {
		t.Fatalf("%+v", res)
	}
	last := lastIPhoneCall(t, seen())
	var args map[string]any
	_ = json.Unmarshal(last.Call.Args, &args)
	if last.Call.Device != "dev-a1" || last.Call.Op != "look" || last.Call.Session != "persona-conv1234" || len(args) != 1 || args["screenshot"] != true {
		t.Fatalf("%+v %v", last, args)
	}
	for _, name := range []string{"dev-b2", "Other iPhone"} {
		before := len(seen())
		res := h.callIPhoneTool(ctx, "c", "iphone_do", json.RawMessage(`{"phone": "`+name+`", "steps": [{"action": "tap", "target": {"n": 1}}]}`))
		if !res.IsError || !strings.Contains(res.Content[0].Text, "Test iPhone (dev-a1)") {
			t.Fatalf("%s: %+v", name, res)
		}
		for _, c := range seen()[before:] {
			if c.Method == "iphone.call" {
				t.Fatalf("%s was operated: %+v", name, c)
			}
		}
	}
	if res := h.callIPhoneTool(ctx, "c", "iphone_do", json.RawMessage(`{"steps": []}`)); !res.IsError {
		t.Fatal("no steps")
	}

	// iphone_status: the allowed one with what it says of itself, and how many are not allowed.
	res = h.callIPhoneTool(ctx, "c", "iphone_status", nil)
	var st struct {
		Phones []struct {
			ID     string          `json:"id"`
			Mac    string          `json:"mac"`
			Detail json.RawMessage `json:"detail"`
		} `json:"phones"`
		NotAllowed int `json:"not_allowed"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &st); err != nil || res.IsError || len(st.Phones) != 1 || st.Phones[0].ID != "dev-a1" ||
		st.Phones[0].Mac != "mac" || len(st.Phones[0].Detail) == 0 || st.NotAllowed != 1 {
		t.Fatalf("%v %+v %s", err, st, res.Content[0].Text)
	}

	// Taking the consent back, or turning the Mac off, takes the iPhone away again.
	if code, _ := call(t, mux, "PUT", "/api/v1/iphone/consent", phone, `{"device": "dev-a1", "allowed": false}`); code != 200 {
		t.Fatal(code)
	}
	if h.iphoneTools() != nil {
		t.Fatal("consent taken back")
	}
	call(t, mux, "PUT", "/api/v1/iphone/consent", phone, `{"device": "dev-a1", "allowed": true}`)
	call(t, mux, "PUT", "/api/v1/machines/mac/iphone", h.cfg.Token, `{"enabled": false}`)
	if h.iphoneTools() != nil || shownIPhone(t, h, mac).Devices[0].Allowed {
		t.Fatal("the Mac turned off")
	}
}

// A step the node says needs approval is asked on the phone in the manager's words, from the node's data; once
// allowed, the do is resumed with the approval alone. The card's input is that data without the evidence, never the
// model's arguments (a typed password). A refusal ends it.
func TestIPhoneApprovals(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	pay := &proto.IPhoneApproval{Step: 1, Action: "tap", Needs: []string{"payment"}, App: proto.IPhoneApp{Name: "Wallet", BundleID: "com.example.wallet"},
		Target: &proto.IPhoneElementRef{N: 4, Role: "button", Label: "Pay ¥36.00"}, Evidence: []string{"label matches pay"}}
	answer := func(p proto.IPhoneCall) (proto.IPhoneResult, error) {
		if p.Op == "do" && p.Approved != "ap1" {
			return proto.IPhoneResult{NeedsApproval: true, ApprovalID: "ap1", Approval: pay}, nil
		}
		return proto.IPhoneResult{Text: "paid"}, nil
	}
	_, seen := iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone}, answer)
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(`{"machines": ["mac"], "phones": ["dev-a1"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.refreshIPhoneMacs(ctx)
	steps := json.RawMessage(`{"steps": [{"action": "tap", "target": {"n": 4}}, {"action": "type", "text": "secret words"}], "numbering": 7}`)

	done := make(chan proto.ToolResult, 1)
	go func() { done <- h.callIPhoneTool(ctx, "c", "iphone_do", steps) }()
	card := answerCard(t, h, true, false)
	l := h.userLang()
	want := i18n.T(l, "hub-iphone.card_on_in", i18n.Args{"phone": "Test iPhone", "app": "Wallet",
		"action": i18n.T(l, "hub-iphone.card_act_tap_on", i18n.Args{"target": i18n.T(l, "hub-iphone.card_target_label", i18n.Args{"label": "Pay ¥36.00"})})})
	if card.ToolName != "iphone_do" || !strings.HasPrefix(card.Summary, want+"\n") || !strings.Contains(card.Summary, i18n.T(l, "hub-iphone.card_need_payment")) {
		t.Fatalf("%q", card.Summary)
	}
	if in := string(card.ToolInput); strings.Contains(in, "secret") || strings.Contains(in, "evidence") || !strings.Contains(in, "Pay ¥36.00") {
		t.Fatalf("card input: %s", in)
	}
	if res := <-done; res.IsError || res.Content[0].Text != "paid" {
		t.Fatalf("%+v", res)
	}
	var calls []proto.IPhoneCall
	for _, c := range seen() {
		if c.Method == "iphone.call" {
			calls = append(calls, c.Call)
		}
	}
	if len(calls) != 2 || calls[0].Approved != "" || !strings.Contains(string(calls[0].Args), `"numbering":7`) ||
		calls[1].Approved != "ap1" || len(calls[1].Args) != 0 || calls[1].Op != "do" {
		t.Fatalf("resumed with the approval alone: %+v", calls)
	}

	go func() { done <- h.callIPhoneTool(ctx, "c", "iphone_do", steps) }()
	answerCard(t, h, false, false)
	if res := <-done; !res.IsError || !strings.Contains(res.Content[0].Text, "declined") {
		t.Fatalf("%+v", res)
	}
	// A card nobody answered (it expired, or was withdrawn) ends it too, but it is no decline: the model is told so.
	for _, reason := range []string{proto.DismissedExpired, proto.DismissedWithdrawn} {
		go func() { done <- h.callIPhoneTool(ctx, "c", "iphone_do", steps) }()
		select {
		case c := <-deskOf(h).ch:
			c.answer <- proto.Resolution{Dismiss: true, Reason: reason}
		case <-time.After(5 * time.Second):
			t.Fatal("no card")
		}
		if res := <-done; !res.IsError || strings.Contains(res.Content[0].Text, "declined") || !strings.Contains(res.Content[0].Text, "did NOT decline") {
			t.Fatalf("%s: %+v", reason, res)
		}
	}

	// Ask before every action: it goes down with the call; the manager asks nothing before the node does.
	if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(`{"machines": ["mac"], "phones": ["dev-a1"], "confirm": true}`), "test"); err != nil {
		t.Fatal(err)
	}
	before := len(seen())
	go func() { done <- h.callIPhoneTool(ctx, "c", "iphone_do", steps) }()
	answerCard(t, h, true, false)
	<-done
	first := true
	for _, c := range seen()[before:] {
		if c.Method == "iphone.call" && first {
			first = false
			if !c.Call.Confirm || c.Call.Approved != "" {
				t.Fatalf("the first call carries confirm, before any card: %+v", c.Call)
			}
		}
	}
	// Looking is never asked.
	if res := h.callIPhoneTool(ctx, "c", "iphone_look", json.RawMessage(`{"screenshot": false}`)); res.IsError {
		t.Fatalf("%+v", res)
	}
}

// The card's words: a password never shown (only its length), a point when the target can't be identified, and the
// steps one "ask before every action" answer covers.
func TestIPhoneCard(t *testing.T) {
	l := i18n.En
	text, input := iphoneCard(l, "My iPhone", &proto.IPhoneApproval{Step: 2, Action: "type", Needs: []string{"password"}, TextLen: 8,
		Target: &proto.IPhoneElementRef{N: 3, Role: "secure_text_field"}})
	if !strings.Contains(text, "8 characters") || !strings.Contains(text, "item 3") || strings.Contains(string(input), `"text"`) {
		t.Fatalf("%q %s", text, input)
	}
	text, _ = iphoneCard(l, "My iPhone", &proto.IPhoneApproval{Step: 1, Action: "tap", Needs: []string{"unchecked"}, At: &proto.IPhonePoint{X: 119.6, Y: 640}})
	if !strings.Contains(text, "(120, 640)") || !strings.Contains(text, i18n.T(l, "hub-iphone.card_need_unchecked")) || strings.Contains(text, " in ") {
		t.Fatalf("%q", text)
	}
	text, _ = iphoneCard(l, "My iPhone", &proto.IPhoneApproval{Step: 1, Action: "tap", Needs: []string{"confirm"}, App: proto.IPhoneApp{BundleID: "com.example.notes"},
		Target: &proto.IPhoneElementRef{N: 2, Role: "button", Label: "New"},
		Steps: []proto.IPhoneStepSummary{{Action: "tap", Target: &proto.IPhoneElementRef{N: 2, Label: "New"}}, {Action: "type", TextLen: 5},
			{Action: "button", Button: "home"}, {Action: "launch", App: "com.example.mail"}}})
	if !strings.Contains(text, "com.example.notes") || !strings.Contains(text, "Type 5 characters → Go to the Home Screen → Open com.example.mail") {
		t.Fatalf("%q", text)
	}
	long := strings.Repeat("字", 300)
	text, _ = iphoneCard(i18n.ZhHans, "我的 iPhone", &proto.IPhoneApproval{Action: "type", Needs: []string{"send_message"}, Text: long, TextLen: 300})
	if strings.Contains(text, long) || !strings.Contains(text, "…") {
		t.Fatal("a long text is cut")
	}
}

// A node that asks again and again ends the call instead of asking forever; a resume the node no longer takes (the
// approval used, replaced, too old, the node restarted) is said as it is, and not tried again.
func TestIPhoneApprovalsEnd(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	stale := false
	iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone}, func(p proto.IPhoneCall) (proto.IPhoneResult, error) {
		if stale && p.Approved != "" {
			data, _ := json.Marshal(proto.IPhoneErrorData{Sent: proto.IPhoneNotSent})
			return proto.IPhoneResult{}, &rpc.Error{Code: "approval_unknown", Message: "nothing was sent: that approval is no longer pending", Data: data}
		}
		return proto.IPhoneResult{NeedsApproval: true, ApprovalID: "again", Approval: &proto.IPhoneApproval{Step: 1, Action: "tap", Needs: []string{"payment"}}}, nil
	})
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(`{"machines": ["mac"], "phones": ["dev-a1"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.refreshIPhoneMacs(ctx) // the default desk allows every card
	do := json.RawMessage(`{"steps": [{"action": "tap", "target": {"x": 1, "y": 2}}]}`)
	if res := h.callIPhoneTool(ctx, "c", "iphone_do", do); !res.IsError || !strings.Contains(res.Content[0].Text, "again and again") {
		t.Fatalf("%+v", res)
	}
	stale = true
	res := h.callIPhoneTool(ctx, "c", "iphone_do", do)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "nothing was sent: that approval is no longer pending") ||
		!strings.Contains(res.Content[0].Text, "do not try it again without asking") {
		t.Fatalf("%+v", res)
	}
}

// A node from before phone control (no built-in program iphone) is never asked: its computer's page says so, and a
// call is refused as not sent.
func TestIPhoneOldNode(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	m, seen := iphoneMac(t, h, "old", []proto.IPhoneDevice{testIPhone}, nil)
	m.info.BuiltinPrograms = []string{"cua", "ego"}
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if p := h.refreshIPhone(ctx, m); p == nil || p.Note != i18n.T(i18n.Default, "hub-iphone.note_outdated") {
		t.Fatalf("%+v", p)
	}
	if got := i18n.IPhoneStatus(i18n.En, h.iphoneOf(m)).Note; got != i18n.T(i18n.En, "hub-iphone.note_outdated") {
		t.Fatalf("translated for the app: %q", got)
	}
	if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(`{"machines": ["old"], "phones": ["dev-a1"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.iphoneCall(ctx, m, proto.IPhoneCall{Device: "dev-a1", Op: "look"}); iphoneSentOf(err) != proto.IPhoneNotSent {
		t.Fatal(err)
	}
	if len(seen()) != 0 {
		t.Fatalf("an old node is never asked: %+v", seen())
	}
}

// A linux computer reaches iPhones too; a Windows one does not list them.
func TestIPhoneSystems(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	lin, _ := iphoneMac(t, h, "box", []proto.IPhoneDevice{testIPhone}, nil)
	win, _ := iphoneMac(t, h, "pc", []proto.IPhoneDevice{testIPhone}, nil)
	lin.info.OS, win.info.OS = "linux", "windows"
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if h.iphoneOf(lin) == nil || h.iphoneOf(win) != nil {
		t.Fatal("darwin and linux only")
	}
}

// The manifest is English with its own translations, and its settings are the form the plugin page draws.
func TestIPhoneManifest(t *testing.T) {
	h, _ := extensionTestHub(t)
	p := h.extensions.reg.Snapshot(iphoneExtensionID)
	if p == nil || p.Manifest.Name != "Phone control" || p.L10n.Name(i18n.ZhHans, p.Manifest.Name) != i18n.T(i18n.ZhHans, "hub-iphone.manifest_name") {
		t.Fatalf("%+v", p)
	}
	var cfg struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(p.Manifest.Configuration, &cfg); err != nil || len(cfg.Properties) != 5 {
		t.Fatalf("%v %s", err, p.Manifest.Configuration)
	}
	if order := string(p.Manifest.Configuration); strings.Index(order, `"machines"`) > strings.Index(order, `"phones"`) ||
		strings.Index(order, `"phones"`) > strings.Index(order, `"confirm"`) {
		t.Fatal(order)
	}
}

func TestIPhoneToolsGolden(t *testing.T) {
	var tools []proto.Tool
	for _, n := range []string{"iphone_status", "iphone_look", "iphone_do"} {
		tools = append(tools, mcpTool(n))
	}
	b, _ := json.MarshalIndent(tools, "", "  ")
	golden(t, "iphone_tools", string(b)+"\n")
}

// A failure says whether anything reached the iPhone: "no" may be tried again, anything else means look first. The
// call itself checks again that the Mac is on and the iPhone allowed, and the node gets its time with 5 s more to wait.
func TestIPhoneCallFailures(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	sent := map[string]string{"look": "no", "do": "unknown"}
	mac, seen := iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone}, func(p proto.IPhoneCall) (proto.IPhoneResult, error) {
		data, _ := json.Marshal(proto.IPhoneErrorData{Sent: sent[p.Op]})
		return proto.IPhoneResult{}, &rpc.Error{Code: "bridge_failed", Message: "the bridge failed", Data: data}
	})
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(`{"machines": ["mac"], "phones": ["dev-a1"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.refreshIPhoneMacs(ctx)
	if res := h.callIPhoneTool(ctx, "c", "iphone_look", nil); !res.IsError || !strings.Contains(res.Content[0].Text, "nothing was sent") {
		t.Fatalf("%+v", res)
	}
	res := h.callIPhoneTool(ctx, "c", "iphone_do", json.RawMessage(`{"steps": [{"action": "home"}], "timeout_ms": 500000}`))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "look at its screen with iphone_look") {
		t.Fatalf("%+v", res)
	}
	if got := lastIPhoneCall(t, seen()).Call.TimeoutMS; got != 120000 {
		t.Fatalf("the node's time is clamped to 120 s: %d", got)
	}

	// Checked again in the call: the Mac turned off, or the consent taken back, after the target was picked.
	before := len(seen())
	for _, cfg := range []string{`{"machines": [], "phones": ["dev-a1"]}`, `{"machines": ["mac"], "phones": []}`} {
		if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(cfg), "test"); err != nil {
			t.Fatal(err)
		}
		_, err := h.iphoneCall(ctx, mac, proto.IPhoneCall{Device: "dev-a1", Op: "look"})
		if err == nil || iphoneSentOf(err) != proto.IPhoneNotSent {
			t.Fatalf("%s: %v", cfg, err)
		}
	}
	for _, c := range seen()[before:] {
		if c.Method == "iphone.call" {
			t.Fatal("nothing went to the node")
		}
	}
	if iphoneSentOf(errors.New("connection lost")) != proto.IPhoneMaybeSent {
		t.Fatal("a plain error may have sent something")
	}
}

// Pairing, forgetting and cancelling go to the node's built-in program from the Mac's page, once the Mac is on.
func TestIPhoneJobs(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	mux := h.routes()
	m, seen := iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone}, nil)
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/iphone/pair", h.cfg.Token, `{"device": "dev-a1"}`); code != 404 {
		t.Fatalf("pairing needs the computer on: %d", code)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/iphone/cancel", h.cfg.Token, ""); code != 200 {
		t.Fatalf("cancelling needs only the extension on: %d", code)
	}
	call(t, mux, "PUT", "/api/v1/machines/mac/iphone", h.cfg.Token, `{"enabled": true}`)
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/iphone/pair", h.cfg.Token, ""); code != 400 {
		t.Fatalf("pair names a device: %d", code)
	}
	code, out := call(t, mux, "POST", "/api/v1/machines/mac/iphone/pair", h.cfg.Token, `{"device": "dev-a1"}`)
	if job, _ := out["job"].(map[string]any); code != 200 || job["kind"] != "pair" || out["enabled"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if p := h.refreshIPhone(ctx, m); p == nil || p.Job == nil || p.Job.Kind != "pair" {
		t.Fatalf("a status answered after pairing keeps its job: %+v", p)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/iphone/forget", h.cfg.Token, `{}`); code != 400 {
		t.Fatalf("forget names a device: %d", code)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/iphone/forget", h.cfg.Token, `{"device": "dev-a1"}`); code != 200 {
		t.Fatal(code)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/iphone/reboot", h.cfg.Token, ""); code != 404 {
		t.Fatal(code)
	}
	var methods []string
	for _, c := range seen() {
		if c.Method != "iphone.status" {
			methods = append(methods, c.Method)
		}
	}
	if strings.Join(methods, " ") != "iphone.cancel iphone.pair iphone.forget" {
		t.Fatal(methods)
	}
}

// helperDevices is the manager's devices with a release site behind them (devices.Devices.HelperDownload).
type helperDevices struct {
	Devices
	urls  []string
	err   error
	asked []string
}

func (d *helperDevices) HelperDownload(_ context.Context, goos, arch, name string) ([]string, update.File, error) {
	d.asked = append(d.asked, goos+"/"+arch+" "+name)
	return d.urls, update.File{Name: "persona-iphone-bridge-darwin-arm64.gz", Size: 7, SHA256: "s", BinarySHA256: "b"}, d.err
}

// Install (#8199): the node gets the release's build of the bridge for its platform, with what it must be; no build,
// or no release site, says so without asking the node; it needs the computer on, as pairing does.
func TestIPhoneInstall(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	mux := h.routes()
	m, seen := iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone}, nil)
	m.info.Arch = "arm64"
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	src := &helperDevices{urls: []string{"https://example.com/dl/v1/persona-iphone-bridge-darwin-arm64.gz"}}
	h.opts.Devices = src
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/iphone/install", h.cfg.Token, ""); code != 404 {
		t.Fatalf("installing needs the computer on: %d", code)
	}
	call(t, mux, "PUT", "/api/v1/machines/mac/iphone", h.cfg.Token, `{"enabled": true}`)
	code, out := call(t, mux, "POST", "/api/v1/machines/mac/iphone/install", h.cfg.Token, "")
	if job, _ := out["job"].(map[string]any); code != 200 || job["kind"] != "install" {
		t.Fatalf("%d %v", code, out)
	}
	if strings.Join(src.asked, ",") != "darwin/arm64 persona-iphone-bridge" {
		t.Fatalf("asked %v", src.asked)
	}
	var got map[string]any
	for _, c := range seen() {
		if c.Method == "iphone.install" {
			_ = json.Unmarshal(c.Raw, &got)
		}
	}
	if urls, _ := got["urls"].([]any); len(urls) != 1 || urls[0] != src.urls[0] || got["size"] != float64(7) || got["sha256"] != "s" || got["binary_sha256"] != "b" {
		t.Fatalf("the node got %v", got)
	}
	installs := func() int {
		n := 0
		for _, c := range seen() {
			if c.Method == "iphone.install" {
				n++
			}
		}
		return n
	}
	for _, tc := range []struct {
		err  error
		code int
	}{{devices.ErrNoHelper, 404}, {devices.ErrNoHelperSource, 404}, {errors.New("site away"), 502}} {
		src.err = tc.err
		if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/iphone/install", h.cfg.Token, ""); code != tc.code || installs() != 1 {
			t.Fatalf("%v: %d (node asked %d times)", tc.err, code, installs())
		}
	}
}

// Prepare (#8199) names its iPhone and needs the computer on, as pairing does; the node gets the device.
func TestIPhonePrepare(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	mux := h.routes()
	_, seen := iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone}, nil)
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/iphone/prepare", h.cfg.Token, `{"device": "dev-a1"}`); code != 404 {
		t.Fatalf("preparing needs the computer on: %d", code)
	}
	call(t, mux, "PUT", "/api/v1/machines/mac/iphone", h.cfg.Token, `{"enabled": true}`)
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/iphone/prepare", h.cfg.Token, ""); code != 400 {
		t.Fatalf("prepare names a device: %d", code)
	}
	code, out := call(t, mux, "POST", "/api/v1/machines/mac/iphone/prepare", h.cfg.Token, `{"device": "dev-a1"}`)
	if job, _ := out["job"].(map[string]any); code != 200 || job["kind"] != "prepare" {
		t.Fatalf("%d %v", code, out)
	}
	var got map[string]any
	for _, c := range seen() {
		if c.Method == "iphone.prepare" {
			_ = json.Unmarshal(c.Raw, &got)
		}
	}
	if got["device"] != "dev-a1" {
		t.Fatalf("the node got %v", got)
	}
}

// Two allowed iPhones with the same name are never guessed between; a consent can be taken back while the extension is
// off; a switch body that says nothing changes nothing.
func TestIPhoneEdges(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	mux := h.routes()
	twin := testIPhone
	twin.ID = "dev-c3"
	_, seen := iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone, twin}, nil)
	phone := deviceToken("dev_phone", roleFull)
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(`{"machines": ["mac"], "phones": ["dev-a1", "dev-c3"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.refreshIPhoneMacs(ctx)
	for _, args := range []string{`{"phone": "test iphone"}`, `{}`} {
		if res := h.callIPhoneTool(ctx, "c", "iphone_look", json.RawMessage(args)); !res.IsError || !strings.Contains(res.Content[0].Text, "more than one iPhone") {
			t.Fatalf("%s: %+v", args, res)
		}
	}
	if res := h.callIPhoneTool(ctx, "c", "iphone_look", json.RawMessage(`{"phone": "dev-c3"}`)); res.IsError {
		t.Fatalf("by id: %+v", res)
	}
	if last := lastIPhoneCall(t, seen()); last.Call.Device != "dev-c3" {
		t.Fatalf("%+v", last)
	}

	if code, _ := call(t, mux, "PUT", "/api/v1/machines/mac/iphone", h.cfg.Token, `{}`); code != 400 {
		t.Fatalf("no enabled: %d", code)
	}
	if p := h.iphoneOf(h.machineByID("mac")); !p.Enabled {
		t.Fatal("the switch stays on")
	}

	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, false, "test"); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/iphone/consent", phone, `{"device": "dev-a1", "allowed": true}`); code != 404 {
		t.Fatalf("no new consent while off: %d", code)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/iphone/consent", phone, `{"device": "dev-a1", "allowed": false}`); code != 200 {
		t.Fatalf("taken back while off: %d", code)
	}
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if phones := h.iphone().Phones; slices.Contains(phones, "dev-a1") || !slices.Contains(phones, "dev-c3") {
		t.Fatalf("on again, without the one taken back: %v", phones)
	}
}

// The iPhones that consented change only through PUT /api/v1/iphone/consent: the general settings route (full token,
// as the CLI's personad extension config uses) and the assistant's settings card keep what is stored, and the
// settings form does not show the field.
func TestIPhonePhonesOnlyFromTheConsent(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	mux := h.routes()
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(`{"machines": ["mac"], "phones": ["dev-a1"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	phones := func() []string { return h.iphone().Phones }
	for _, body := range []string{
		`{"config": {"machines": ["mac"], "phones": ["dev-a1", "dev-evil"], "confirm": true}}`, // adding one
		`{"config": {"machines": ["mac"], "phones": []}}`,                                      // taking one away
		`{"config": {"machines": ["mac"]}}`,                                                    // leaving it out (a form without it)
	} {
		code, out := call(t, mux, "PUT", "/api/v1/extensions/iphone/-/config", h.cfg.Token, body)
		if code != 200 || strings.Join(phones(), ",") != "dev-a1" {
			t.Fatalf("%s: %d %v → %v", body, code, out, phones())
		}
		if strings.Contains(body, "confirm") && !h.iphone().Confirm {
			t.Fatal("the other fields are saved as sent")
		}
	}

	// The assistant's settings card refuses the field outright.
	res, err := h.toolPersonaSettings(ctx, "server", "s1", json.RawMessage(`{"action":"set","key":"extension.iphone","value":{"phones":["dev-evil"]}}`))
	if err == nil || !strings.Contains(err.Error(), "own consent") || strings.Join(phones(), ",") != "dev-a1" {
		t.Fatalf("%q %v → %v", res, err, phones())
	}

	// Not in the form any client draws.
	raw, _ := json.Marshal(h.extensions.detail(ctx, h.extensions.reg.Get(iphoneExtensionID)).Form)
	if strings.Contains(string(raw), `"phones"`) || strings.Contains(string(raw), `"self_apps"`) || !strings.Contains(string(raw), `"machines"`) {
		t.Fatalf("form: %s", raw)
	}
}

// One iphone_do at a time per iPhone: while a do waits for its approval (the card may show on that very iPhone), a
// second do, from another conversation too, does not reach the node until the first is done.
func TestIPhoneDoWaitsItsTurn(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	_, seen := iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone}, func(p proto.IPhoneCall) (proto.IPhoneResult, error) {
		if p.Op == "do" && p.Approved == "" && strings.Contains(string(p.Args), "pay") {
			return proto.IPhoneResult{NeedsApproval: true, ApprovalID: "ap1", Approval: &proto.IPhoneApproval{Step: 1, Action: "tap", Needs: []string{"payment"}}}, nil
		}
		return proto.IPhoneResult{Text: "done"}, nil
	})
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(`{"machines": ["mac"], "phones": ["dev-a1"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.refreshIPhoneMacs(ctx)
	calls := func() int {
		n := 0
		for _, c := range seen() {
			if c.Method == "iphone.call" {
				n++
			}
		}
		return n
	}
	deskOf(h)
	th := hubOf(h)
	th.mu.Lock()
	answer := th.answer
	asked := make(chan struct{}, 1)
	th.answer = func(ix proto.Interaction) proto.Resolution {
		select {
		case asked <- struct{}{}:
		default:
		}
		return answer(ix)
	}
	th.mu.Unlock()
	first, second := make(chan proto.ToolResult, 1), make(chan proto.ToolResult, 1)
	go func() {
		first <- h.callIPhoneTool(ctx, "conv-a", "iphone_do", json.RawMessage(`{"steps": [{"action": "tap", "target": {"label": "pay"}}]}`))
	}()
	var card deskCard
	select {
	case card = <-deskOf(h).ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no card")
	}
	go func() {
		second <- h.callIPhoneTool(ctx, "conv-b", "iphone_do", json.RawMessage(`{"steps": [{"action": "tap", "target": {"n": 2}}]}`))
	}()
	select {
	case <-asked: // the second do is in, waiting its turn
	case <-time.After(5 * time.Second):
		t.Fatal("the second do never asked")
	}
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if n := calls(); n != 1 {
		t.Fatalf("while the card waits only the first do reached the node: %d calls", n)
	}
	yes := true
	card.answer <- proto.Resolution{Allow: &yes}
	if res := <-first; res.IsError {
		t.Fatalf("%+v", res)
	}
	if res := <-second; res.IsError {
		t.Fatalf("%+v", res)
	}
	if n := calls(); n != 3 {
		t.Fatalf("the first, its resume, then the second: %d", n)
	}
	// A look is not input: it never waits.
	if res := h.callIPhoneTool(ctx, "conv-c", "iphone_look", nil); res.IsError {
		t.Fatalf("%+v", res)
	}
}

// Persona's own app on the phone, as the app says it when consenting, is kept (only the consent route sets it) and goes
// down with every call so the core never operates it.
func TestIPhoneSelfApp(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	mux := h.routes()
	_, seen := iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone}, nil)
	phone := deviceToken("dev_phone", roleFull)
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(`{"machines": ["mac"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.refreshIPhoneMacs(ctx)
	body := `{"device": "dev-a1", "allowed": true, "app": {"bundle_id": "com.example.persona", "name": "Persona"}}`
	for range 2 { // said again: kept once
		if code, out := call(t, mux, "PUT", "/api/v1/iphone/consent", phone, body); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
	}
	if s := h.iphone(); strings.Join(s.SelfApps, ",") != "com.example.persona" || strings.Join(s.SelfNames, ",") != "Persona" {
		t.Fatalf("%+v", s)
	}
	code, _ := call(t, mux, "PUT", "/api/v1/extensions/iphone/-/config", h.cfg.Token, `{"config": {"machines": ["mac"], "self_apps": [], "self_names": ["x"]}}`)
	if s := h.iphone(); code != 200 || strings.Join(s.SelfApps, ",") != "com.example.persona" || strings.Join(s.SelfNames, ",") != "Persona" {
		t.Fatalf("the settings route keeps them: %d %+v", code, s)
	}
	h.refreshIPhoneMacs(ctx)
	if res := h.callIPhoneTool(ctx, "c", "iphone_do", json.RawMessage(`{"steps": [{"action": "wait", "ms": 10}]}`)); res.IsError {
		t.Fatalf("%+v", res)
	}
	last := lastIPhoneCall(t, seen())
	if strings.Join(last.Call.SelfApps, ",") != "com.example.persona" || strings.Join(last.Call.SelfNames, ",") != "Persona" {
		t.Fatalf("%+v", last.Call)
	}
}

// What the model types never leaves through the manager: a password the model put in its arguments is in neither the
// card (its text and its input, which the hub stores and pushes) nor the manager's log; other typed text is never in
// the log either (only the actions are).
func TestIPhoneTypedTextStaysOut(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	var logged bytes.Buffer
	logTo(h, &logged)
	const password, message = "hunter2-secret", "see you at nine"
	iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone}, func(p proto.IPhoneCall) (proto.IPhoneResult, error) {
		if p.Approved == "" {
			return proto.IPhoneResult{NeedsApproval: true, ApprovalID: "ap1", Approval: &proto.IPhoneApproval{Step: 2, Action: "type",
				Needs: []string{"password"}, TextLen: len(password), Target: &proto.IPhoneElementRef{N: 5, Role: "secure_text_field"},
				Evidence: []string{"secure field"}}}, nil
		}
		return proto.IPhoneResult{Text: "done"}, nil
	})
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(`{"machines": ["mac"], "phones": ["dev-a1"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.refreshIPhoneMacs(ctx)
	done := make(chan proto.ToolResult, 1)
	go func() {
		done <- h.callIPhoneTool(ctx, "c", "iphone_do", json.RawMessage(`{"steps": [{"action": "type", "text": "`+message+`", "target": {"n": 4}},
			{"action": "type", "text": "`+password+`", "target": {"n": 5}}]}`))
	}()
	card := answerCard(t, h, true, false)
	if res := <-done; res.IsError {
		t.Fatalf("%+v", res)
	}
	shown, _ := json.Marshal(card)
	if strings.Contains(string(shown), password) || strings.Contains(string(shown), "secure field") {
		t.Fatalf("the card holds the password or the evidence: %s", shown)
	}
	if !strings.Contains(card.Summary, strconv.Itoa(len(password))) {
		t.Fatalf("its length only: %q", card.Summary)
	}
	if l := logged.String(); strings.Contains(l, password) || strings.Contains(l, message) || !strings.Contains(l, "do [type type]") {
		t.Fatalf("log: %s", l)
	}
}

// The core's refusals: busy on a resume keeps the approval, so the same id is tried again without asking the owner
// again; not_pending is said as it is and not tried again; bad_args tells the model to fix its call.
func TestIPhoneRefusals(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	old := iphoneBusyWaits
	iphoneBusyWaits = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { iphoneBusyWaits = old })
	refuse := func(code string) error {
		data, _ := json.Marshal(proto.IPhoneErrorData{Sent: proto.IPhoneNotSent})
		return &rpc.Error{Code: code, Message: code + ": refused", Data: data}
	}
	var mu sync.Mutex
	busy, mode := 0, "busy"
	_, seen := iphoneMac(t, h, "mac", []proto.IPhoneDevice{testIPhone}, func(p proto.IPhoneCall) (proto.IPhoneResult, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case mode == "bad_args":
			return proto.IPhoneResult{}, refuse("bad_args")
		case p.Approved == "":
			return proto.IPhoneResult{NeedsApproval: true, ApprovalID: "ap1", Approval: &proto.IPhoneApproval{Step: 1, Action: "tap", Needs: []string{"payment"}}}, nil
		case mode == "not_pending":
			return proto.IPhoneResult{}, refuse("not_pending")
		case busy < 2:
			busy++
			return proto.IPhoneResult{}, refuse("busy")
		}
		return proto.IPhoneResult{Text: "paid"}, nil
	})
	if err := h.extensions.reg.SetEnabled(ctx, iphoneExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, iphoneExtensionID, json.RawMessage(`{"machines": ["mac"], "phones": ["dev-a1"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.refreshIPhoneMacs(ctx)
	do := json.RawMessage(`{"steps": [{"action": "tap", "target": {"n": 1}}]}`)
	cards := func() int { th := hubOf(h); th.mu.Lock(); defer th.mu.Unlock(); return len(th.pending) }
	before := cards()
	if res := h.callIPhoneTool(ctx, "c", "iphone_do", do); res.IsError || res.Content[0].Text != "paid" {
		t.Fatalf("busy twice, then done: %+v", res)
	}
	var approved []string
	for _, c := range seen() {
		if c.Method == "iphone.call" {
			approved = append(approved, c.Call.Approved)
		}
	}
	if strings.Join(approved, ",") != ",ap1,ap1,ap1" || cards()-before != 1 {
		t.Fatalf("the same id again, one card: %v, %d cards", approved, cards()-before)
	}
	mu.Lock()
	mode = "not_pending"
	mu.Unlock()
	if res := h.callIPhoneTool(ctx, "c", "iphone_do", do); !res.IsError || !strings.Contains(res.Content[0].Text, "no longer holds") ||
		!strings.Contains(res.Content[0].Text, "do not try it again without asking") {
		t.Fatalf("%+v", res)
	}
	mu.Lock()
	mode = "bad_args"
	mu.Unlock()
	if res := h.callIPhoneTool(ctx, "c", "iphone_do", do); !res.IsError || !strings.Contains(res.Content[0].Text, "fix them") {
		t.Fatalf("%+v", res)
	}
}
