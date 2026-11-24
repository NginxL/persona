package extmgr

// Operating the user's iPhones for the assistant: a computer's node reaches the iPhones paired with it over
// RemotePairing (#3452, docs/extensions/iphone.md). The built-in extension iphone is off until the user turns it on,
// then computer by computer, and an iPhone is operated only once it consented from its own app (fail-closed: an iPhone that never said yes
// is never operated). While it is off no node is asked about iPhones, the assistant has no iphone_* tools and the
// machines carry no IPhone. Like the extensions cua and ego (computer.go, browser.go), whose switch it shares.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/update"
)

const iphoneExtensionID = "iphone"

// iphoneSystems are the computers the device bridge runs on.
var iphoneSystems = []string{"darwin", "linux"}

// iphoneConfig is the extension's settings in l: the computers whose paired iPhones may be operated (each machine page's
// switch), the iPhones that consented (each from its own app) and whether every action is asked first.
func iphoneConfig(l i18n.Lang) json.RawMessage {
	t := func(key string) string { return i18n.T(l, "hub-iphone.manifest_"+key) }
	type field = map[string]any
	machines, _ := json.Marshal(field{"type": "array", "title": t("machines_title"), "description": t("machines_description"),
		"items": field{"type": "string", "format": "machine", "x-os": iphoneSystems}, "uniqueItems": true})
	phones, _ := json.Marshal(field{"type": "array", "title": t("phones_title"), "description": t("phones_description"),
		"items": field{"type": "string"}, "uniqueItems": true})
	confirm, _ := json.Marshal(field{"type": "boolean", "title": t("confirm_title"), "description": t("confirm_description"), "default": false})
	selfApps, _ := json.Marshal(field{"type": "array", "title": t("self_apps_title"), "items": field{"type": "string", "maxLength": 200}, "uniqueItems": true, "maxItems": 16})
	selfNames, _ := json.Marshal(field{"type": "array", "title": t("self_names_title"), "items": field{"type": "string", "maxLength": 200}, "uniqueItems": true, "maxItems": 16})
	// Joined by hand to keep the properties in this order: the form follows it.
	return json.RawMessage(`{"type":"object","properties":{"machines":` + string(machines) + `,"phones":` + string(phones) +
		`,"confirm":` + string(confirm) + `,"self_apps":` + string(selfApps) + `,"self_names":` + string(selfNames) + `}}`)
}

// iphoneManifest is the extension's manifest in English, with iphoneTranslations for the other languages.
func iphoneManifest(h *Manager) *extension.Manifest {
	items, _ := json.Marshal([]extension.ComputerItem{{ID: iphoneExtensionID, Name: "iPhone"}})
	return &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: iphoneExtensionID,
		Name: i18n.T(i18n.En, "hub-iphone.manifest_name"), Version: h.version, Description: i18n.T(i18n.En, "hub-iphone.manifest_description"),
		Contributes: map[string]json.RawMessage{"iphone": items}, Configuration: iphoneConfig(i18n.En), Node: builtinNode("machines")}
}

// iphoneTranslations are the manifest's name, description and settings in every language but English.
func iphoneTranslations() extension.Translations {
	out := extension.Translations{}
	for _, l := range []i18n.Lang{i18n.ZhHans, i18n.Ja} {
		t := func(key string) string { return i18n.T(l, "hub-iphone.manifest_"+key) }
		out[l] = &extension.Translation{Name: t("name"), Description: t("description"), Configuration: map[string]*extension.FieldText{
			"machines":   {Title: t("machines_title"), Description: t("machines_description")},
			"phones":     {Title: t("phones_title"), Description: t("phones_description")},
			"self_apps":  {Title: t("self_apps_title")},
			"self_names": {Title: t("self_names_title")},
			"confirm":    {Title: t("confirm_title"), Description: t("confirm_description")},
		}}
	}
	return out
}

// iphoneSettings is the extension's state: On when it is on, its computers, the iPhones that consented, whether to ask first.
type iphoneSettings struct {
	On       bool     `json:"-"`
	Machines []string `json:"machines"`
	Phones   []string `json:"phones"`
	// SelfApps, SelfNames: Persona's own app on the phones (bundle ids, display names, as each app said when it
	// consented), which the core never operates.
	SelfApps  []string `json:"self_apps"`
	SelfNames []string `json:"self_names"`
	Confirm   bool     `json:"confirm"`
}

func (h *Manager) iphone() iphoneSettings {
	if h.extensions == nil || h.extensions.reg == nil {
		return iphoneSettings{}
	}
	p := h.extensions.reg.Snapshot(iphoneExtensionID) // a copy, as machineExtension reads it
	if p == nil || !p.Record.Enabled {
		return iphoneSettings{}
	}
	s := iphoneSettings{On: true}
	_ = json.Unmarshal(h.extensions.reg.Config(p), &s)
	return s
}

// iphonePhonesField is the settings field of the iPhones that consented; iphoneOwnFields are it and Persona's own app's
// identity on the phones. Only PUT /api/v1/iphone/consent changes them (from the user's own signed-in app); the general
// settings routes keep what is stored (keepIPhonePhones) and the settings form does not show them, so nothing else can
// consent for an iPhone, or make the core's "never operate Persona's own app" miss it.
const iphonePhonesField = "phones"

var iphoneOwnFields = []string{iphonePhonesField, "self_apps", "self_names"}

// keepIPhonePhones is raw, a whole new configuration of extension p, with the iPhone extension's phones put back to
// what is stored; any other extension's raw as it is.
func (h *Manager) keepIPhonePhones(p *extension.Extension, raw json.RawMessage) (json.RawMessage, error) {
	if p == nil || p.Record.ID != iphoneExtensionID {
		return raw, nil
	}
	next := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &next); err != nil || next == nil {
			return raw, nil // not an object: the schema check refuses it as it is
		}
	}
	stored := map[string]json.RawMessage{}
	_ = json.Unmarshal(h.extensions.reg.Config(p), &stored)
	for _, f := range iphoneOwnFields {
		if v, ok := stored[f]; ok {
			next[f] = v
		} else {
			delete(next, f)
		}
	}
	return json.Marshal(next)
}

// withoutIPhonePhones is the iPhone extension's settings form without phones and Persona's own app's identity (only
// each iPhone's own app changes them).
func withoutIPhonePhones(p *extension.Extension, form *extension.Schema) *extension.Schema {
	if p == nil || p.Record.ID != iphoneExtensionID || form == nil {
		return form
	}
	out := *form
	out.Fields = slices.DeleteFunc(slices.Clone(form.Fields), func(f extension.Field) bool { return slices.Contains(iphoneOwnFields, f.Name) })
	return &out
}

// iphoneOf is m's proto.Machine.IPhone: nil unless the extension is on and m is a system the bridge runs on; else the
// last devices its node gave (every such computer's while it is on, so the iPhone's own app can find itself before
// the computer is turned on), with Enabled when it is on for m and each device's Allowed when also that iPhone consented.
func (h *Manager) iphoneOf(m *Machine) *proto.IPhone {
	s := h.iphone()
	m.mu.Lock()
	system, last := m.info.OS, m.iphone
	m.mu.Unlock()
	if !s.On || !slices.Contains(iphoneSystems, system) {
		return nil
	}
	p := proto.IPhone{}
	if last != nil {
		p = *last
		p.Devices = slices.Clone(last.Devices)
	}
	p.Enabled = slices.Contains(s.Machines, m.cfg.ID)
	for i := range p.Devices {
		p.Devices[i].Allowed = p.Enabled && p.Devices[i].ID != "" && slices.Contains(s.Phones, p.Devices[i].ID)
	}
	return &p
}

// refreshIPhone asks m's node (iphone.status) which iPhones it reaches now, while the extension is on and m is online; the
// machine list goes out again when that changed.
func (h *Manager) refreshIPhone(ctx context.Context, m *Machine) *proto.IPhone {
	if p := h.iphoneOf(m); p == nil || !m.isOnline() {
		return p
	}
	asked := time.Now()
	var p proto.IPhone
	if !m.hasIPhoneProgram() {
		p = proto.IPhone{Note: i18n.T(i18n.Default, "hub-iphone.note_outdated")}
	} else {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := m.callBuiltin(ctx, "iphone.status", map[string]any{}, &p); err != nil {
			return h.iphoneOf(m)
		}
	}
	p.Enabled = false // the manager's, never the node's word
	for i := range p.Devices {
		p.Devices[i].Allowed = false
	}
	if h.keepIPhone(m, p, asked) {
		h.broadcastMachines()
	}
	return h.iphoneOf(m)
}

// keepIPhone stores what m's node answered unless what is kept is newer (as keepComputer); true when it changed.
func (h *Manager) keepIPhone(m *Machine, p proto.IPhone, asked time.Time) bool {
	when := p.Checked
	if when.IsZero() {
		when = asked
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if when.Before(m.iphoneAt) {
		return false
	}
	old := m.iphone
	m.iphone, m.iphoneAt = &p, when
	if old == nil {
		return true
	}
	a, b := *old, p
	a.Checked, b.Checked = time.Time{}, time.Time{}
	return !reflect.DeepEqual(a, b)
}

// iphoneChanged: the extension was turned on or off, or its computers or iPhones changed: ask them again.
func (h *Manager) iphoneChanged(ctx context.Context) {
	for _, m := range h.machinesNow() {
		if p := h.iphoneOf(m); p != nil && m.isOnline() {
			go h.refreshIPhone(ctx, m)
		}
	}
	h.broadcastMachines()
}

// ---- the app's routes ----

// getIPhone: GET /api/v1/machines/{mid}/iphone, the iPhones this computer reaches now (its page asks when it opens).
// 404 while the extension is off or the bridge does not run there.
func (h *Manager) getIPhone(w http.ResponseWriter, r *http.Request, m *Machine) {
	p := h.refreshIPhone(r.Context(), m)
	if p == nil {
		writeErr(w, http.StatusNotFound, i18n.Err("hub-iphone.err_off"))
		return
	}
	writeJSON(w, i18n.IPhoneStatus(h.requestLang(r), p))
}

// putIPhone: PUT /api/v1/machines/{mid}/iphone {"enabled": bool}, the machine page's switch (the extension's machines).
func (h *Manager) putIPhone(w http.ResponseWriter, r *http.Request, m *Machine) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSONBody(w, r, &in, 4<<10); err != nil {
		h.writeComputerBodyErr(w, r, err)
		return
	}
	if in.Enabled == nil { // a body without it never turns the switch off
		writeErr(w, http.StatusBadRequest, i18n.Err("hub-iphone.err_no_enabled"))
		return
	}
	if h.iphoneOf(m) == nil {
		writeErr(w, http.StatusNotFound, i18n.Err("hub-iphone.err_off"))
		return
	}
	if err := h.setExtensionMachine(r.Context(), iphoneExtensionID, m.cfg.ID, *in.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	h.logf("[%s] iphone control %v", m.cfg.ID, *in.Enabled)
	writeJSON(w, i18n.IPhoneStatus(h.requestLang(r), h.refreshIPhone(r.Context(), m)))
}

// helperSource finds a release's build of a helper program for a computer (devices.Devices.HelperDownload).
type helperSource interface {
	HelperDownload(ctx context.Context, goos, arch, name string) ([]string, update.File, error)
}

// postIPhoneJob: POST /api/v1/machines/{mid}/iphone/{pair|forget|cancel|install|prepare} {"device"}: the machine page's
// Pair (an iPhone plugged into this computer, trusted on the phone), Forget (its pairing record), Cancel (no device),
// and the guided setup's Install (no device: the device bridge itself, the release's build for this computer) and
// Prepare (an iPhone's developer disk image, which then also allows the computer to mount it again by itself, #8199).
// The node runs them in the background; the state comes back as IPhone.job (the page asks again while it runs).
// Pairing, installing and preparing need the computer turned on; forgetting and cancelling only the extension on (a
// pairing started before the switch went off can still be stopped, a paired iPhone still forgotten).
func (h *Manager) postIPhoneJob(w http.ResponseWriter, r *http.Request, m *Machine) {
	action := r.PathValue("action")
	if action != "pair" && action != "forget" && action != "cancel" && action != "install" && action != "prepare" {
		writeErr(w, http.StatusNotFound, i18n.ErrEnglish("hub-computer.err_unknown_action"))
		return
	}
	var in struct {
		Device string `json:"device"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSONBody(w, r, &in, 4<<10); err != nil {
			h.writeComputerBodyErr(w, r, err)
			return
		}
	}
	in.Device = strings.TrimSpace(in.Device)
	if action != "cancel" && action != "install" && in.Device == "" {
		writeErr(w, http.StatusBadRequest, i18n.Err("hub-iphone.err_no_device"))
		return
	}
	p := h.iphoneOf(m)
	if p == nil {
		writeErr(w, http.StatusNotFound, i18n.Err("hub-iphone.err_off"))
		return
	}
	if (action == "pair" || action == "install" || action == "prepare") && !p.Enabled {
		writeErr(w, http.StatusNotFound, i18n.Err("hub-iphone.err_not_enabled"))
		return
	}
	if !m.isOnline() {
		writeErr(w, http.StatusConflict, i18n.Err("hub-computer.err_offline"))
		return
	}
	if !m.hasIPhoneProgram() {
		writeErr(w, http.StatusConflict, i18n.Err("hub-iphone.note_outdated"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	asked := time.Now()
	params := map[string]any{}
	if in.Device != "" {
		params["device"] = in.Device
	}
	if action == "install" {
		src, ok := h.opts.Devices.(helperSource)
		if !ok {
			writeErr(w, http.StatusNotFound, i18n.Err("hub-iphone.err_no_helper"))
			return
		}
		m.mu.Lock()
		goos, arch := m.info.OS, m.info.Arch
		m.mu.Unlock()
		urls, f, err := src.HelperDownload(ctx, goos, arch, "persona-iphone-bridge")
		switch {
		case errors.Is(err, devices.ErrNoHelper), errors.Is(err, devices.ErrNoHelperSource):
			writeErr(w, http.StatusNotFound, i18n.Err("hub-iphone.err_no_helper"))
			return
		case err != nil:
			h.logf("[%s] iphone bridge: %v", m.cfg.ID, err)
			writeErr(w, http.StatusBadGateway, i18n.Err("hub-iphone.err_helper_unreachable"))
			return
		}
		params = map[string]any{"urls": urls, "size": f.Size, "sha256": f.SHA256, "binary_sha256": f.BinarySHA256}
	}
	var got proto.IPhone
	if err := m.callBuiltin(ctx, "iphone."+action, params, &got); err != nil {
		h.writeComputerErr(w, r, http.StatusConflict, err)
		return
	}
	h.logf("[%s] iphone %s", m.cfg.ID, action) // never which iPhone
	got.Enabled = false
	for i := range got.Devices {
		got.Devices[i].Allowed = false
	}
	if h.keepIPhone(m, got, asked) {
		h.broadcastMachines()
	}
	writeJSON(w, i18n.IPhoneStatus(h.requestLang(r), h.iphoneOf(m)))
}

// hasIPhoneProgram: m's node has the built-in program iphone (node.info's builtin_programs); one from before has no
// way to an iPhone.
func (m *Machine) hasIPhoneProgram() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.info.BuiltinPrograms, iphoneExtensionID)
}

// iphoneSeen is one iPhone as the app on an iPhone lists them to find itself: the device, the computers that reach it, and
// whether it consented.
type iphoneSeen struct {
	proto.IPhoneDevice
	Machines  []iphoneSeenMac `json:"machines"`
	Consented bool            `json:"consented"`
}

type iphoneSeenMac struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// iphoneList is GET /api/v1/iphone: whether the extension is on and every iPhone the Macs reach (each once, with the
// Macs that reach it), for Settings › Connectors › This iPhone.
type iphoneList struct {
	On      bool         `json:"on"`
	Devices []iphoneSeen `json:"devices"`
}

// iphoneDevices is every iPhone the computers reported, each once, with the computers that reach it: as the computer
// that best reaches it reports it (allowed there first, then connected).
func (h *Manager) iphoneDevices() []iphoneSeen {
	better := func(a, b proto.IPhoneDevice) bool {
		if a.Allowed != b.Allowed {
			return a.Allowed
		}
		return a.Connected && !b.Connected
	}
	s := h.iphone()
	var out []iphoneSeen
	at := map[string]int{}
	for _, m := range h.machinesNow() {
		p := h.iphoneOf(m)
		if p == nil {
			continue
		}
		for _, d := range p.Devices {
			if d.ID == "" {
				continue
			}
			mac := iphoneSeenMac{ID: m.cfg.ID, Name: m.name(), Enabled: p.Enabled}
			if i, ok := at[d.ID]; ok {
				out[i].Machines = append(out[i].Machines, mac)
				if better(d, out[i].IPhoneDevice) {
					out[i].IPhoneDevice = d
				}
				continue
			}
			at[d.ID] = len(out)
			out = append(out, iphoneSeen{IPhoneDevice: d, Machines: []iphoneSeenMac{mac}, Consented: slices.Contains(s.Phones, d.ID)})
		}
	}
	return out
}

func (h *Manager) getIPhoneList(w http.ResponseWriter, r *http.Request) {
	s := h.iphone()
	out := iphoneList{On: s.On, Devices: []iphoneSeen{}}
	if s.On {
		var wait []chan struct{}
		for _, m := range h.machinesNow() {
			if h.iphoneOf(m) == nil || !m.isOnline() {
				continue
			}
			done := make(chan struct{})
			wait = append(wait, done)
			go func() { defer close(done); h.refreshIPhone(r.Context(), m) }()
		}
		for _, c := range wait {
			<-c
		}
		l := h.requestLang(r)
		for _, d := range h.iphoneDevices() {
			d.Note = i18n.ComputerText(l, d.Note)
			out.Devices = append(out.Devices, d)
		}
	}
	writeJSON(w, out)
}

// iphoneOwn is field (self_apps, self_names) as stored, whether the extension is on or not.
func (h *Manager) iphoneOwn(field string) []string {
	p := h.extensions.reg.Snapshot(iphoneExtensionID)
	if p == nil {
		return nil
	}
	cfg := map[string][]string{}
	_ = json.Unmarshal(h.extensions.reg.Config(p), &cfg)
	return cfg[field]
}

// putIPhoneConsent: PUT /api/v1/iphone/consent {"device": id, "allowed": bool, "app"?: {"bundle_id", "name"}}, an
// iPhone's own "Allow the assistant to operate this iPhone". Only from the user's own signed-in app (never a master
// token or an extension): the consent is that iPhone's. app is that Persona app itself, kept so the core never operates
// it (self_apps, self_names).
func (h *Manager) putIPhoneConsent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Device  string `json:"device"`
		Allowed bool   `json:"allowed"`
		App     struct {
			BundleID string `json:"bundle_id"`
			Name     string `json:"name"`
		} `json:"app"`
	}
	if err := decodeJSONBody(w, r, &in, 4<<10); err != nil {
		h.writeComputerBodyErr(w, r, err)
		return
	}
	in.Device = strings.TrimSpace(in.Device)
	if !byOwnDevice(r.Context()) || principalOf(r.Context()).Extension != "" {
		writeErr(w, http.StatusForbidden, i18n.Err("hub-iphone.err_consent_device"))
		return
	}
	if !h.iphone().On && in.Allowed { // taking it back always works: the consent must not wait for the extension
		writeErr(w, http.StatusNotFound, i18n.Err("hub-iphone.err_off"))
		return
	}
	if in.Device == "" || len(in.Device) > 200 {
		writeErr(w, http.StatusBadRequest, i18n.Err("hub-iphone.err_no_device"))
		return
	}
	for field, v := range map[string]string{"self_apps": strings.TrimSpace(in.App.BundleID), "self_names": strings.TrimSpace(in.App.Name)} {
		if v == "" || len(v) > 200 || slices.Contains(h.iphoneOwn(field), v) {
			continue
		}
		if err := h.setExtensionList(r.Context(), iphoneExtensionID, field, v, true); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := h.setExtensionList(r.Context(), iphoneExtensionID, "phones", in.Device, in.Allowed); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	h.logf("iphone consent %v from device %s", in.Allowed, principalOf(r.Context()).ID)
	for _, d := range h.iphoneDevices() {
		if d.ID == in.Device {
			d.Note = i18n.ComputerText(h.requestLang(r), d.Note)
			writeJSON(w, d)
			return
		}
	}
	writeJSON(w, iphoneSeen{IPhoneDevice: proto.IPhoneDevice{ID: in.Device}, Machines: []iphoneSeenMac{}, Consented: in.Allowed})
}

// ---- the assistant's tools ----
// Tool diagnostics below are model instructions and raw node output, not client interface copy.

// iphoneTools are the assistant's tools while some iPhone is allowed: the extension on, a Mac on, that iPhone consented.
func (h *Manager) iphoneTools() []proto.Tool {
	if s := h.iphone(); !s.On || len(s.Machines) == 0 || len(s.Phones) == 0 {
		return nil
	}
	for _, d := range h.iphoneDevices() {
		if d.Allowed {
			return []proto.Tool{mcpTool("iphone_status"), mcpTool("iphone_look"), mcpTool("iphone_do")}
		}
	}
	return nil
}

func isIPhoneTool(name string) bool {
	return name == "iphone_status" || name == "iphone_look" || name == "iphone_do"
}

// iphoneApprovals: the most approvals one iphone_do may ask for (a step after another that needs one).
const iphoneApprovals = 8

// callIPhoneTool runs iphone_status, iphone_look or iphone_do for the assistant's conversation sessionID.
func (h *Manager) callIPhoneTool(ctx context.Context, sessionID, name string, args json.RawMessage) proto.ToolResult {
	var a struct {
		Phone      string          `json:"phone"`
		Screenshot *bool           `json:"screenshot"`
		Steps      json.RawMessage `json:"steps"`
		Numbering  int             `json:"numbering"`
		TimeoutMS  int             `json:"timeout_ms"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return proto.TextResult("bad arguments: "+err.Error(), true)
		}
	}
	session := "persona-" + convID(sessionID)
	if name == "iphone_status" {
		return h.iphoneStatus(ctx, session)
	}
	d, m, err := h.iphoneTarget(ctx, a.Phone)
	if err != nil {
		if !h.iphoneSomeAllowed() { // none allowed at all: the setup isn't done, not a wrong name
			return proto.TextResult(err.Error()+" "+iphoneSetupHint, true)
		}
		return proto.TextResult(err.Error(), true)
	}
	in := map[string]any{}
	op := "look"
	if name == "iphone_look" {
		in["screenshot"] = a.Screenshot == nil || *a.Screenshot
	} else {
		var steps []map[string]any
		if json.Unmarshal(a.Steps, &steps) != nil || len(steps) == 0 {
			return proto.TextResult(`give steps: [{"action": "tap", "target": {"n": 3}}, {"action": "type", "text": "…"}], in order`, true)
		}
		op, in["steps"] = "do", a.Steps
		if a.Numbering > 0 {
			in["numbering"] = a.Numbering
		}
		if a.Screenshot != nil {
			in["screenshot"] = *a.Screenshot
		}
	}
	raw, _ := json.Marshal(in)
	by := h.assistantAsker(sessionID, m.cfg.ID)
	what := iphoneLogWhat(op, a.Steps)
	if op == "do" {
		// One do at a time on this iPhone, held until its approvals are answered and it is done: while a card waits
		// (it may show on that very iPhone), no other input reaches it, so the automation can never tap its own approval.
		release, err := h.iphoneTurn(ctx, d.ID)
		if err != nil {
			return proto.TextResult("waited for another action on "+d.Name+" to finish and gave up: nothing was sent", true)
		}
		defer release()
	}
	// "Ask before every action" goes down with the call: the node asks step by step, with the screen in view; the
	// manager only shows its approvals and waits for the answer.
	call := proto.IPhoneCall{Device: d.ID, Session: session, Op: op, Args: raw, TimeoutMS: a.TimeoutMS, Confirm: h.iphone().Confirm,
		SelfApps: h.iphone().SelfApps, SelfNames: h.iphone().SelfNames}
	start := time.Now()
	res, err := h.iphoneCall(ctx, m, call)
	resumed := false
	for n := 0; err == nil && res.NeedsApproval; n++ {
		if n == iphoneApprovals || res.ApprovalID == "" || res.Approval == nil {
			err = errors.New("the iPhone asked for approval again and again: stop and tell the user what is left to do")
			break
		}
		card, input := iphoneCard(h.userLang(), d.Name, res.Approval)
		if ok, outcome := h.approveOutcome(ctx, by, name, card, input); !ok {
			h.logf("[%s] %s %s (%s, step %d %s)", m.cfg.ID, name, refusalWord(outcome), what, res.Approval.Step, res.Approval.Action)
			return notApproved(outcome)
		}
		// Resumed with the approval alone: the node holds the pending do. A phone busy with another call keeps the
		// approval pending: the same id is tried again, without asking the owner again.
		call.Args, call.Approved, resumed = nil, res.ApprovalID, true
		res, err = h.iphoneCall(ctx, m, call)
		for try := 1; err != nil && iphoneCode(err) == "busy" && try <= len(iphoneBusyWaits); try++ {
			select {
			case <-time.After(iphoneBusyWaits[try-1]):
			case <-ctx.Done():
				return proto.TextResult("gave up waiting for "+d.Name+" to be free: the approved step was not done", true)
			}
			res, err = h.iphoneCall(ctx, m, call)
		}
	}
	h.logf("[%s] %s for %s: %s, %v, %s", m.cfg.ID, name, convID(sessionID), what, time.Since(start).Round(time.Millisecond),
		map[bool]string{true: "failed (sent " + iphoneSentOf(err) + ")", false: "ok"}[err != nil])
	if err != nil {
		switch iphoneCode(err) {
		case "bad_args":
			return proto.TextResult(err.Error()+" (the iPhone could not read the arguments or steps; nothing was sent: fix them and call again)", true)
		case "not_pending":
			return proto.TextResult(err.Error()+" (the approval no longer holds: used, replaced by a newer do, expired, or the node restarted. The approved step was not done: tell the user, and do not try it again without asking them)", true)
		case "busy":
			return proto.TextResult(err.Error()+" (the iPhone stayed busy; nothing was sent: try again in a moment)", true)
		}
		if resumed && iphoneSentOf(err) == proto.IPhoneNotSent {
			// The approval was not used (used before, replaced by a newer do, too old, the node restarted).
			return proto.TextResult(err.Error()+" (the approved step was not done: tell the user, and do not try it again without asking them)", true)
		}
		return iphoneFailure(err)
	}
	return computerResult(&proto.ComputerResult{Output: res.Output, Text: res.Text, Image: res.Image})
}

// iphoneBusyWaits are the pauses before trying a resume again while the phone is busy with another call (the
// approval stays pending meanwhile).
var iphoneBusyWaits = []time.Duration{time.Second, 2 * time.Second, 3 * time.Second}

// iphoneCode is a failed call's code (the node core's refusals: bad_args, not_pending, busy), "" for another error.
func iphoneCode(err error) string {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// iphoneTurn waits for device's turn: the do before it on that iPhone done, or ctx over. release ends the turn.
func (h *Manager) iphoneTurn(ctx context.Context, device string) (release func(), err error) {
	v, _ := h.iphoneTurns.LoadOrStore(device, make(chan struct{}, 1))
	turn := v.(chan struct{})
	select {
	case turn <- struct{}{}:
		return func() { <-turn }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// iphoneStatus is iphone_status: each allowed iPhone as its Macs see it now, with what its node says of it (op status).
func (h *Manager) iphoneStatus(ctx context.Context, session string) proto.ToolResult {
	h.refreshIPhoneMacs(ctx)
	type phone struct {
		ID          string          `json:"id"`
		Name        string          `json:"name"`
		Model       string          `json:"model,omitempty"`
		IOS         string          `json:"ios,omitempty"`
		Mac         string          `json:"mac"`
		Connected   bool            `json:"connected"`
		Transport   string          `json:"transport,omitempty"`
		Locked      bool            `json:"locked"`
		Ready       bool            `json:"ready"`
		Note        string          `json:"note,omitempty"`
		Detail      json.RawMessage `json:"detail,omitempty"`
		DetailError string          `json:"detail_error,omitempty"`
	}
	out := struct {
		Phones     []phone `json:"phones"`
		NotAllowed int     `json:"not_allowed,omitempty"`
		Setup      string  `json:"setup,omitempty"`
	}{Phones: []phone{}}
	for _, d := range h.iphoneDevices() {
		if iphoneSetupUnfinished(d) {
			out.Setup = iphoneSetupHint
		}
		if !d.Allowed {
			out.NotAllowed++
			continue
		}
		m := h.iphoneMac(d)
		p := phone{ID: d.ID, Name: d.Name, Model: d.ProductType, IOS: d.IOS, Connected: d.Connected, Transport: d.Transport,
			Locked: d.Locked, Ready: d.Ready, Note: d.Note}
		if m != nil {
			p.Mac = m.cfg.ID
			if d.Connected {
				res, err := h.iphoneCall(ctx, m, proto.IPhoneCall{Device: d.ID, Session: session, Op: "status"})
				switch {
				case err != nil:
					p.DetailError = err.Error()
				case len(res.Output) > 0:
					p.Detail = res.Output
				case res.Text != "":
					p.Detail, _ = json.Marshal(res.Text)
				}
			}
		}
		out.Phones = append(out.Phones, p)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return proto.TextResult(string(b), false)
}

// refreshIPhoneMacs asks every Mac the extension is on for what it reaches now, all at once.
func (h *Manager) refreshIPhoneMacs(ctx context.Context) {
	var wait []chan struct{}
	for _, m := range h.machinesNow() {
		if p := h.iphoneOf(m); p == nil || !p.Enabled || !m.isOnline() {
			continue
		}
		done := make(chan struct{})
		wait = append(wait, done)
		go func() { defer close(done); h.refreshIPhone(ctx, m) }()
	}
	for _, c := range wait {
		<-c
	}
}

// iphoneTarget is the allowed iPhone the assistant named (by id or name; the only one when it named none) and the
// computer to reach it through: from what the computers said last, and only when that does not settle it, from what
// they say now (each look and step would otherwise wait for every computer's status first).
func (h *Manager) iphoneTarget(ctx context.Context, name string) (iphoneSeen, *Machine, error) {
	d, m, err := h.iphonePick(name)
	if err == nil && m != nil {
		return d, m, nil
	}
	h.refreshIPhoneMacs(ctx)
	d, m, err = h.iphonePick(name)
	if err != nil {
		return iphoneSeen{}, nil, err
	}
	if m == nil {
		return iphoneSeen{}, nil, fmt.Errorf("no computer that reaches %s is online", d.Name)
	}
	return d, m, nil
}

// iphonePick is iphoneTarget on what the computers said last: the iPhone (an error when none or several match) and
// the computer to reach it through (nil when none is online).
func (h *Manager) iphonePick(name string) (iphoneSeen, *Machine, error) {
	var allowed []iphoneSeen
	for _, d := range h.iphoneDevices() {
		if d.Allowed {
			allowed = append(allowed, d)
		}
	}
	names := make([]string, len(allowed))
	for i, d := range allowed {
		names[i] = fmt.Sprintf("%s (%s)", d.Name, d.ID)
	}
	list := strings.Join(names, ", ")
	if list == "" {
		list = "none"
	}
	name = strings.TrimSpace(name)
	var match []iphoneSeen
	for _, d := range allowed {
		if name == "" || d.ID == name {
			match = append(match, d)
		}
	}
	if name != "" && len(match) == 0 {
		for _, d := range allowed {
			if strings.EqualFold(d.Name, name) {
				match = append(match, d)
			}
		}
	}
	switch {
	case len(match) == 1:
		return match[0], h.iphoneMac(match[0]), nil
	case len(match) > 1:
		return iphoneSeen{}, nil, fmt.Errorf("more than one iPhone matches: give phone as an id (one of: %s)", list)
	default:
		return iphoneSeen{}, nil, fmt.Errorf("no iPhone %q the user allowed (allowed: %s); the user allows one in that iPhone's Persona app and turns its computer on on that computer's page", name, list)
	}
}

// iphoneMac is the online Mac, turned on, to reach d through: one where it is connected first.
func (h *Manager) iphoneMac(d iphoneSeen) *Machine {
	var found *Machine
	for _, mac := range d.Machines {
		m := h.machineByID(mac.ID)
		if m == nil || !mac.Enabled || !m.isOnline() {
			continue
		}
		p := h.iphoneOf(m)
		if p == nil {
			continue
		}
		for _, dev := range p.Devices {
			if dev.ID == d.ID && dev.Allowed && dev.Connected {
				return m
			}
		}
		if found == nil {
			found = m
		}
	}
	return found
}

// The node's time for an iphone.call (TimeoutMS), and how much longer the manager waits for its answer.
const (
	iphoneTimeoutDefault = 30 * time.Second
	iphoneTimeoutMost    = 120 * time.Second
	iphoneTimeoutSlack   = 5 * time.Second
)

// iphoneNotSent is a call that never left the manager: the extension off, the computer not on or offline, the iPhone
// not allowed there, the node too old. Safe to try again once that changed.
func iphoneNotSent(code, format string, args ...any) error {
	return &rpc.Error{Code: code, Message: fmt.Sprintf(format, args...), Data: iphoneSentData(proto.IPhoneNotSent)}
}

func iphoneSentData(sent string) json.RawMessage {
	raw, _ := json.Marshal(proto.IPhoneErrorData{Sent: sent})
	return raw
}

// iphoneCall is one iphone.call on m's node. It checks again, here, that the extension is on, m is on, online and has
// the program, and the iPhone is allowed on m (a caller's earlier look may be stale); any of them failing is an error
// that says nothing was sent. The node gets call.TimeoutMS (1–120 s, under a second rounded up, 30 s when 0); the
// manager waits 5 s more. Every error is an *rpc.Error whose data says whether anything may have reached the iPhone:
// the node's own as it is, no for a computer that could not be reached, unknown for a call that left with no answer.
func (h *Manager) iphoneCall(ctx context.Context, m *Machine, call proto.IPhoneCall) (proto.IPhoneResult, error) {
	var res proto.IPhoneResult
	p := h.iphoneOf(m)
	switch {
	case p == nil:
		return res, iphoneNotSent("iphone_off", "the phone control extension is off, or %s cannot reach iPhones", m.cfg.ID)
	case !p.Enabled:
		return res, iphoneNotSent("not_enabled", "phone control is not turned on for %s", m.cfg.ID)
	case !m.isOnline():
		return res, iphoneNotSent("offline", "%s is offline", m.cfg.ID)
	case !m.hasIPhoneProgram():
		return res, iphoneNotSent("outdated", "the personad on %s is too old to operate iPhones: the user redeploys it", m.cfg.ID)
	}
	allowed := false
	for _, d := range p.Devices {
		allowed = allowed || (d.ID == call.Device && d.Allowed)
	}
	if !allowed {
		return res, iphoneNotSent("not_allowed", "the iPhone %s is not allowed on %s: the user allows it in that iPhone's Persona app", call.Device, m.cfg.ID)
	}
	wait := time.Duration(call.TimeoutMS) * time.Millisecond
	switch {
	case call.TimeoutMS <= 0:
		wait, call.TimeoutMS = iphoneTimeoutDefault, 0
	case wait < time.Second:
		wait, call.TimeoutMS = time.Second, 1000
	case wait > iphoneTimeoutMost:
		wait, call.TimeoutMS = iphoneTimeoutMost, int(iphoneTimeoutMost/time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(ctx, wait+iphoneTimeoutSlack)
	defer cancel()
	if err := m.callBuiltin(ctx, "iphone.call", call, &res); err != nil {
		var coded *rpc.Error
		switch {
		case errors.As(err, &coded) && len(coded.Data) > 0:
			return res, coded
		case errors.Is(err, errOffline):
			return res, iphoneNotSent("offline", "%s", err.Error())
		}
		// The call left for the node and no answer came back: it may have been done.
		return res, &rpc.Error{Code: "unknown", Message: err.Error(), Data: iphoneSentData(proto.IPhoneMaybeSent)}
	}
	return res, nil
}

// iphoneSentOf is what a failed call says was sent: no, or unknown (also when it says neither).
func iphoneSentOf(err error) string {
	var e *rpc.Error
	var d proto.IPhoneErrorData
	if errors.As(err, &e) && json.Unmarshal(e.Data, &d) == nil && d.Sent == proto.IPhoneNotSent {
		return proto.IPhoneNotSent
	}
	return proto.IPhoneMaybeSent
}

// iphoneFailure is a failed call for the model: what went wrong, and whether to try again or look first; when the
// reason is a setup the user hasn't finished, where to finish it (iphoneSetupHint).
func iphoneFailure(err error) proto.ToolResult {
	hint := ""
	if iphoneSetupCodes[iphoneCode(err)] {
		hint = " " + iphoneSetupHint
	}
	if iphoneSentOf(err) == proto.IPhoneNotSent {
		return proto.TextResult(err.Error()+" (nothing was sent to the iPhone)"+hint, true)
	}
	return proto.TextResult(err.Error()+" (some of it may have happened on the iPhone: look at its screen with iphone_look before anything else, and do not repeat the steps blindly)"+hint, true)
}

// iphoneSetupHint goes with an iPhone tool's result when the iPhone isn't set up yet (#8199): the user finishes that on
// the iPhone, in the app's guided setup, which this link opens. Only in results, never in the tools' descriptions or the
// assistant's prompt.
const iphoneSetupHint = "iPhone control isn't fully set up: give the user the link [Set up iPhone control](persona://iphone/setup), its words in their language, to finish it on their iPhone (a few minutes)."

// iphoneSetupCodes are the failures a finished setup ends: the extension or the computer off, the iPhone not allowed,
// not paired (any more), Developer Mode off, not prepared, no bridge on the computer.
var iphoneSetupCodes = map[string]bool{
	"iphone_off": true, "not_enabled": true, "not_allowed": true, "not_paired": true, "pairing_missing": true,
	"pairing_rejected": true, "developer_mode_off": true, "ddi_not_mounted": true, "bridge_missing": true,
}

// iphoneSetupUnfinished says whether a device's state is one the guided setup finishes (iphone_status).
func iphoneSetupUnfinished(d iphoneSeen) bool { return !d.Allowed || iphoneSetupCodes[d.Code] }

// iphoneSomeAllowed says whether some iPhone is allowed now: the assistant can operate it.
func (h *Manager) iphoneSomeAllowed() bool {
	for _, d := range h.iphoneDevices() {
		if d.Allowed {
			return true
		}
	}
	return false
}

// iphoneLogWhat is the log's account of a call: look, or the steps' actions, never what is typed or tapped.
func iphoneLogWhat(op string, steps json.RawMessage) string {
	if op != "do" {
		return op
	}
	return "do [" + strings.Join(iphoneActions(steps), " ") + "]"
}

// iphoneActions are the actions of iphone_do's steps ({"action": "tap", …}); one without is "step".
func iphoneActions(steps json.RawMessage) []string {
	var list []struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(steps, &list)
	out := make([]string, 0, len(list))
	for _, s := range list {
		a := strings.TrimSpace(s.Action)
		if a == "" || len(a) > 32 {
			a = "step"
		}
		out = append(out, a)
	}
	return out
}
