package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The primary device for phone calls (docs/apple/phone-tools.md "Which device takes a call"): the owner picks one of
// the paired devices that offer phone tools in Settings › Devices, and phone_* calls go to it first instead of to
// whichever connected device was active last. It is a choice about the hub's own phone transport (where a tool call
// is sent, which device a silent push wakes), so it lives in the hub's settings, one value for all clients. With none
// chosen (Automatic) every call goes where it went before.

// phonePrimaryKey is the hub's setting that holds the chosen primary device.
const phonePrimaryKey = "phone.primary"

// phonePrimary is the chosen primary device: its credential, and the name that credential had when it was chosen (for
// what the model is told). The zero value is Automatic.
type phonePrimary struct {
	CredentialID string `json:"credential_id"`
	Name         string `json:"name,omitempty"`
}

// phonePrimaryView is the phone object of GET /api/v1/auth/tokens and the answer of PUT /api/v1/phone/primary.
type phonePrimaryView struct {
	// Primary is the chosen device's credential; "" is Automatic, also when the chosen credential was revoked.
	Primary string `json:"primary"`
	// Candidates are the active credentials whose device offers phone tools: the ones that can be chosen.
	Candidates []string `json:"candidates"`
}

// loadPhonePrimary reads the chosen primary device at start.
func (h *Hub) loadPhonePrimary(ctx context.Context) {
	if h.store == nil {
		return
	}
	var p phonePrimary
	if _, err := h.store.Setting(ctx, phonePrimaryKey, &p); err != nil {
		h.logf("load the primary device for phone calls: %v", err)
		return
	}
	h.mu.Lock()
	h.primaryPhone = p
	h.mu.Unlock()
}

// primaryPhoneLocked is the chosen primary device's credential while the hub knows the device (connected, or with a
// registration; revoking a credential drops both, revoked()), and whether it offers tool ("" any tool). Connected, that
// is what its app offers now: a registration is written after the connection learns the tools, and a failed write
// leaves it behind. Away, it is its latest registration (an older app's, kept under the credential itself, may be left
// beside it). A primary device that does not offer the tool (that data source is off there, or the tool is another
// platform's) leaves the call to the automatic order. The caller holds h.mu.
func (h *Hub) primaryPhoneLocked(tool string) (credential string, offers bool) {
	id := h.primaryPhone.CredentialID
	if id == "" {
		return "", false
	}
	if c := h.phoneClientLocked("", id); c != nil {
		return id, tool == "" || c.phoneToolNames[tool]
	}
	var latest *db.PhoneToolRegistration
	for _, r := range h.phoneTools {
		if r.CredentialID == id && (latest == nil || r.UpdatedAt.After(latest.UpdatedAt)) {
			latest = &r
		}
	}
	if latest == nil {
		return "", false
	}
	for _, t := range latest.Tools {
		if tool == "" || t.Name == tool {
			return id, true
		}
	}
	return id, false
}

// primaryPhoneFor is primaryPhoneLocked under h.mu, with the primary device's name for what the model is told.
func (h *Hub) primaryPhoneFor(tool string) (credential, name string, offers bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	credential, offers = h.primaryPhoneLocked(tool)
	return credential, h.primaryPhone.Name, offers
}

// phoneClientLocked is the connected phone that takes tool ("" any): only credential's connections when it is not
// empty; real devices before simulators, then the one that sent a message last. The caller holds h.mu.
func (h *Hub) phoneClientLocked(tool, credential string) *client {
	var best *client
	for c := range h.clients {
		select {
		case <-c.done:
			continue
		default:
		}
		if !c.phone || (tool != "" && !c.phoneToolNames[tool]) || (credential != "" && c.device != credential) {
			continue
		}
		if best == nil || (best.sim && !c.sim) || (best.sim == c.sim &&
			(c.activeAt.After(best.activeAt) || (c.activeAt.Equal(best.activeAt) && c.since.After(best.since)))) {
			best = c
		}
	}
	return best
}

// phoneClientOf is phoneClientLocked under h.mu: credential's connection, or with credential "" the automatic order.
func (h *Hub) phoneClientOf(tool, credential string) *client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.phoneClientLocked(tool, credential)
}

// phoneClientPreferring is the connected phone that takes tool: primary's connection while it has one, else the
// automatic order. Unlike phoneClient it does not read the setting again, so one call keeps the primary it started with.
func (h *Hub) phoneClientPreferring(tool, primary string) *client {
	h.mu.Lock()
	defer h.mu.Unlock()
	if primary != "" {
		if c := h.phoneClientLocked(tool, primary); c != nil {
			return c
		}
	}
	return h.phoneClientLocked(tool, "")
}

// queuedWriteClient is where a queued write is delivered: the primary device when one is chosen and offers the tool
// (queued writes wait for it, even while another device is connected), otherwise the automatic order.
func (h *Hub) queuedWriteClient(tool string) *client {
	h.mu.Lock()
	defer h.mu.Unlock()
	if primary, offers := h.primaryPhoneLocked(tool); offers {
		return h.phoneClientLocked(tool, primary)
	}
	return h.phoneClientLocked(tool, "")
}

// phonePrimaryView is what Settings › Devices reads: tokens are the hub's credentials, revoked ones included, and a
// primary device whose credential is gone shows as Automatic.
func (h *Hub) phonePrimaryView(tokens []db.DeviceToken) phonePrimaryView {
	active := map[string]bool{}
	for _, t := range tokens {
		if t.RevokedAt == nil {
			active[t.ID] = true
		}
	}
	view := phonePrimaryView{Candidates: []string{}}
	h.mu.Lock()
	for _, r := range h.phoneTools {
		if r.CredentialID != "" && len(r.Tools) > 0 && active[r.CredentialID] && !slices.Contains(view.Candidates, r.CredentialID) {
			view.Candidates = append(view.Candidates, r.CredentialID)
		}
	}
	if id := h.primaryPhone.CredentialID; active[id] {
		view.Primary = id
	}
	h.mu.Unlock()
	slices.Sort(view.Candidates)
	return view
}

// phonePrimaryRoutes: choosing the primary device (full credentials only: an answer-only device puts nothing but its
// widgets, allowed()).
func (h *Hub) phonePrimaryRoutes(api *http.ServeMux) {
	// The device that takes phone calls first, or Automatic with credential_id "": Settings › Devices on iPhone and iPad.
	api.HandleFunc("PUT /api/v1/phone/primary", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			CredentialID *string `json:"credential_id"`
		}
		if err := decodeJSONBody(w, r, &body, 4<<10); err != nil || body.CredentialID == nil {
			writeBodyErr(w, err, errors.New("credential_id is required"))
			return
		}
		if h.store == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
			return
		}
		// One choice written at a time, so the setting and what calls read agree on the last one. Not under h.mu:
		// the hub's global lock never waits on the database (#5358).
		h.primaryPhoneMu.Lock()
		defer h.primaryPhoneMu.Unlock()
		tokens, err := h.store.DeviceTokens(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		var next phonePrimary
		if id := strings.TrimSpace(*body.CredentialID); id != "" {
			i := slices.IndexFunc(tokens, func(t db.DeviceToken) bool { return t.ID == id && t.RevokedAt == nil })
			if i < 0 || !slices.Contains(h.phonePrimaryView(tokens).Candidates, id) {
				writeErrCode(w, http.StatusConflict, "not_a_phone", i18n.Err("hub-phone_primary.not_candidate"))
				return
			}
			next = phonePrimary{CredentialID: id, Name: tokens[i].Name}
		}
		if err := h.store.PutSetting(r.Context(), phonePrimaryKey, next); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.mu.Lock()
		h.primaryPhone = next
		h.mu.Unlock()
		h.wakePhoneWrites() // writes queued for the previous choice may go now
		writeJSON(w, h.phonePrimaryView(tokens))
	})
}

// withStandInNote tells the model which device answered when a primary device is chosen and another one took the
// call. A result that is one JSON object gets the note as its device_note field, so what reads that object (the map
// and quick-result cards, which see the result's text blocks joined) still parses it; any other result gets the note
// as a text block of its own after the result.
func withStandInNote(r proto.ToolResult, device, primary string) proto.ToolResult {
	note := prompts.RenderPhoneStandIn(prompts.PhoneStandIn{Device: device, Primary: primary})
	r.Content = slices.Clone(r.Content)
	text := -1
	for i, c := range r.Content {
		if c.Type == "text" {
			if text >= 0 { // two text blocks never parse as one object
				text = -1
				break
			}
			text = i
		}
	}
	if text >= 0 {
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(r.Content[text].Text), &object) == nil && object != nil {
			object["device_note"], _ = json.Marshal(note)
			if b, err := json.Marshal(object); err == nil {
				r.Content[text].Text = string(b)
				return r
			}
		}
	}
	// TextResult builds the block in the proto package (see annotateHealthSnapshot).
	r.Content = append(r.Content, proto.TextResult(note, false).Content...)
	return r
}

// standInName is how the model is told about a device that answered: the name it gave itself, else its platform
// ("" leaves the template's own words).
func (h *Hub) standInName(c *client) string {
	h.mu.Lock()
	name := strings.TrimSpace(c.name)
	h.mu.Unlock()
	if name != "" {
		return name
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(c.meta.Platform)
}
