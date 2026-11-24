package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Looks (docs/extensions/extensions.md "Avatars", docs/apple/persona.md): one list of every look a user can choose — the ones the apps
// draw themselves (builtin), the persona packages on the hub (hub) and the ones extensions declare (extension) — and
// which one is chosen, kept on the hub so every device of the user shows the same one.
//
// An extension's look is served as a persona package: the hub makes its persona.json (format 1) from the manifest's
// item and gives out only the files it names, SVGs cleaned. So GET /api/v1/personas lists it too, and apps that know
// only persona packages show it as they show the others.

const lookKey = "look" // the setting: the chosen look

// builtinLookNames are the names of the looks the apps draw (extension.BuiltinLooks), shown on the picker's cards.
var builtinLookNames = map[string]struct {
	name  string
	names map[string]string
}{
	"persona": {"团子", map[string]string{"en": "Mochi", "ja": "だんご"}},
}

// lookView is one look as the apps list it.
type lookView struct {
	ID string `json:"id"`
	// Source: builtin (drawn by the apps, no files), hub (a persona package), extension.
	Source    string            `json:"source"`
	Name      string            `json:"name"`
	Names     map[string]string `json:"names,omitempty"`
	Subtitle  string            `json:"subtitle,omitempty"`
	Subtitles map[string]string `json:"subtitles,omitempty"`
	// AppIcon is the built-in look whose icon stands for this one where the platform shows only icons the app ships.
	AppIcon       string          `json:"app_icon"`
	Extension     string          `json:"extension,omitempty"`
	ExtensionName string          `json:"extension_name,omitempty"`
	Rev           string          `json:"rev,omitempty"`
	Bytes         int64           `json:"bytes,omitempty"`
	Manifest      json.RawMessage `json:"manifest,omitempty"`
	// Uploaded: the hub made it from an image the user uploaded (look_uploads.go); only these can be deleted.
	Uploaded bool `json:"uploaded,omitempty"`
}

// lookChoice is the setting: the look the user chose and when.
type lookChoice struct {
	ID        string    `json:"id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// lookCurrent is GET /api/v1/looks's current: the chosen one (nil: never chosen) and the one to show (the default when
// the chosen one is not there: its extension off, its package gone).
type lookCurrent struct {
	ID        *string    `json:"id"`
	Effective string     `json:"effective"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// lookSource says which extension a persona package comes from (nil: the hub's own).
type lookSource struct {
	extension, name string
	appIcon         string
}

// extensionLookCache keeps the packages made from extensions' looks, by what they are made of.
type extensionLookCache struct {
	mu   sync.Mutex
	pkgs map[string]*personaPackage
}

var extLooks = &extensionLookCache{pkgs: map[string]*personaPackage{}}

// extensionLooks are the packages of the looks of the extensions that are on, as the manager reports them (its
// extensions: extwire.Report.Looks), kept while it is away. A look with the id of one of the hub's own packages is
// left out (the hub's wins).
func (h *Hub) extensionLooks() []*personaPackage {
	var out []*personaPackage
	own := h.builtinPersonaIDs()
	for _, lp := range h.extReport().Looks {
		var l extension.LookItem
		if err := json.Unmarshal(lp.Item, &l); err != nil || slices.Contains(own, l.ID) {
			continue
		}
		pkg, err := extensionLookPackage(lp, l)
		if err != nil {
			h.logf("look %s of %s left out: %v", l.ID, lp.Extension, err)
			continue
		}
		out = append(out, pkg)
	}
	return out
}

// extensionLookPackage is the persona package of an extension's look: its made persona.json and the files it names,
// checked as any package is.
func extensionLookPackage(lp extwire.LookPackage, l extension.LookItem) (*personaPackage, error) {
	var key strings.Builder
	fmt.Fprintf(&key, "%s\x00%s\x00%s", lp.Extension, lp.Version, l.ID)
	key.Write(lp.Item)
	for _, f := range l.Files() {
		b, ok := lp.Files[f]
		if !ok {
			return nil, fmt.Errorf("%s: missing", f)
		}
		fmt.Fprintf(&key, "\x00%s\x00%d\x00%x", f, len(b), fnv64(b))
	}
	extLooks.mu.Lock()
	if pkg := extLooks.pkgs[key.String()]; pkg != nil {
		extLooks.mu.Unlock()
		return pkg, nil
	}
	extLooks.mu.Unlock()

	manifest, err := json.Marshal(lookManifest(lp, l))
	if err != nil {
		return nil, err
	}
	fsys := fstest.MapFS{"persona.json": &fstest.MapFile{Data: manifest, Mode: 0o644}}
	for _, f := range l.Files() {
		fsys[f] = &fstest.MapFile{Data: lp.Files[f], Mode: 0o644}
	}
	pkg, err := openPersona(fsys, l.ID)
	if err != nil {
		return nil, err
	}
	pkg.source = &lookSource{extension: lp.Extension, name: lp.ExtensionName, appIcon: extension.DefaultLook}
	extLooks.mu.Lock()
	if len(extLooks.pkgs) > 256 { // versions come and go; a hub has a handful of looks
		clear(extLooks.pkgs)
	}
	extLooks.pkgs[key.String()] = pkg
	extLooks.mu.Unlock()
	return pkg, nil
}

func fnv64(b []byte) uint64 {
	h := uint64(14695981039346656037)
	for _, c := range b {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return h
}

// lookManifest is the persona.json (format 1) of an extension's look, with what format 1 does not have beside it
// (names, the SVGs, the dark avatar, app_icon_fallback, source), which the apps that do not know it leave alone. A look
// without a dot gets one: its largest avatar PNG as idle.
func lookManifest(lp extwire.LookPackage, l extension.LookItem) map[string]any {
	avatar := map[string]any{"sizes": l.Avatar.Sizes}
	if l.Avatar.Safe > 0 {
		avatar["safe"] = l.Avatar.Safe
	}
	if l.Avatar.SVG != "" {
		avatar["svg"] = l.Avatar.SVG
	}
	if l.Avatar.Dark != nil {
		avatar["dark"] = l.Avatar.Dark
	}
	var dot any = l.Dot
	if l.Dot == nil {
		largest, file := 0, ""
		for side, f := range l.Avatar.Sizes {
			if n, _ := strconv.Atoi(side); n > largest {
				largest, file = n, f
			}
		}
		dot = map[string]any{"size": largest, "states": map[string]any{"idle": map[string]any{"file": file}}}
	}
	m := map[string]any{
		"format": personaFormat, "id": l.ID, "name": l.Name, "avatar": avatar, "dot": dot,
		"app_icon_fallback": extension.DefaultLook, // old manager reports may still name a retired icon
		"source":            map[string]any{"kind": "extension", "extension": lp.Extension, "extension_name": lp.ExtensionName},
	}
	if l.Subtitle != "" {
		m["subtitle"] = l.Subtitle
	}
	if len(l.Names) > 0 {
		m["names"] = l.Names
	}
	if len(l.Subtitles) > 0 {
		m["subtitles"] = l.Subtitles
	}
	if l.Colors != nil {
		m["colors"] = l.Colors
	}
	return m
}

// looks is every look, in the picker's order: the built-in ones, the hub's packages, the extensions'.
func (h *Hub) looks() []lookView {
	out := []lookView{}
	for _, id := range extension.BuiltinLooks {
		b := builtinLookNames[id]
		out = append(out, lookView{ID: id, Source: "builtin", Name: b.name, Names: b.names, AppIcon: id})
	}
	for _, p := range h.personaPackages() {
		info, err := h.personaInfo(p)
		if err != nil {
			h.logf("persona %s left out: %v", p.id, err)
			continue
		}
		var m struct {
			Name      string            `json:"name"`
			Names     map[string]string `json:"names"`
			Subtitle  string            `json:"subtitle"`
			Subtitles map[string]string `json:"subtitles"`
		}
		_ = json.Unmarshal(info.Manifest, &m)
		v := lookView{ID: p.id, Source: "hub", Name: m.Name, Names: m.Names, Subtitle: m.Subtitle, Subtitles: m.Subtitles,
			AppIcon: extension.DefaultLook, Rev: info.Rev, Bytes: info.Bytes, Manifest: info.Manifest, Uploaded: isUploadedManifest(p.raw)}
		if s := p.source; s != nil {
			v.Source, v.Extension, v.ExtensionName, v.AppIcon = "extension", s.extension, s.name, s.appIcon
		}
		out = append(out, v)
	}
	return out
}

// lookChoice is the chosen look as stored (nil: never chosen, or no store).
func (h *Hub) lookChoice(ctx context.Context) (*lookChoice, error) {
	if h.store == nil {
		return nil, nil
	}
	var c lookChoice
	if ok, err := h.store.Setting(ctx, lookKey, &c); err != nil {
		return nil, err
	} else if !ok || c.ID == "" {
		return nil, nil
	}
	return &c, nil
}

// lookCurrentOf is what the apps are told is chosen and shown, given the looks there are.
func lookCurrentOf(c *lookChoice, looks []lookView) lookCurrent {
	cur := lookCurrent{Effective: extension.DefaultLook}
	if c == nil {
		return cur
	}
	id, at := c.ID, c.UpdatedAt
	cur.ID, cur.UpdatedAt = &id, &at
	if slices.ContainsFunc(looks, func(v lookView) bool { return v.ID == c.ID }) {
		cur.Effective = c.ID
	}
	return cur
}

// chooseLook stores the choice and tells the apps.
func (h *Hub) chooseLook(ctx context.Context, id string, looks []lookView) (lookCurrent, error) {
	id = compat.Looks(id)
	c := &lookChoice{ID: id, UpdatedAt: time.Now().UTC().Truncate(time.Second)}
	if err := h.store.PutSetting(ctx, lookKey, c); err != nil {
		return lookCurrent{}, err
	}
	cur := lookCurrentOf(c, looks)
	h.broadcast(map[string]any{"type": "look", "id": id, "effective": cur.Effective})
	return cur, nil
}

// looksReported is the manager's news for the extensions' looks (a new report): the apps are told the list changed.
func (h *Hub) looksReported(old, now []extwire.LookPackage) {
	key := func(list []extwire.LookPackage) []string {
		var out []string
		for _, lp := range list {
			out = append(out, lp.Extension+"\x00"+lp.Version+"\x00"+string(lp.Item))
		}
		slices.Sort(out)
		return out
	}
	if !slices.Equal(key(old), key(now)) {
		h.broadcast(map[string]any{"type": "personas"})
	}
}

// looksGone: an extension with looks was removed. When the chosen look was one of its own and no one gives it now,
// the choice goes back to the default (switched off, the choice stays: the default only shows meanwhile).
func (h *Hub) looksGone(ctx context.Context, ids []string) {
	if h.store == nil {
		return
	}
	if !h.quiesce.beginOrDefer(func() {
		h.looksGoneMu.Lock()
		h.looksGonePending = append(h.looksGonePending, ids...)
		h.looksGoneMu.Unlock()
	}) {
		return
	}
	defer h.quiesce.end()
	chosen, err := h.lookChoice(ctx)
	if err != nil {
		h.logf("look: read choice after removal: %v", err)
		return
	}
	if chosen == nil || !slices.Contains(ids, chosen.ID) {
		return
	}
	looks := h.looks()
	if slices.ContainsFunc(looks, func(v lookView) bool { return v.ID == chosen.ID }) {
		return
	}
	if _, err := h.chooseLook(ctx, extension.DefaultLook, looks); err != nil {
		h.logf("look: back to the default after %s went: %v", chosen.ID, err)
	}
}

func (h *Hub) resumeLooksGone() {
	if h.store == nil {
		return
	}
	h.looksGoneMu.Lock()
	ids := h.looksGonePending
	h.looksGonePending = nil
	h.looksGoneMu.Unlock()
	if len(ids) == 0 {
		return
	}
	h.nodeWorkers.Add(1)
	go func() {
		defer h.nodeWorkers.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.looksGone(ctx, ids)
	}()
}

func (h *Hub) lookRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/looks", func(w http.ResponseWriter, r *http.Request) {
		chosen, err := h.lookChoice(r.Context())
		if err != nil {
			h.logf("look: read choice: %v", err)
			writeErr(w, http.StatusInternalServerError, i18n.Err("http-status.internal"))
			return
		}
		looks := h.looks()
		writeJSON(w, map[string]any{"looks": looks, "current": lookCurrentOf(chosen, looks)})
	})
	api.HandleFunc("PUT /api/v1/looks/current", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID      string `json:"id"`
			IfUnset bool   `json:"if_unset"`
		}
		if err := decodeJSONBody(w, r, &body, 4<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if h.store == nil {
			writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("this hub has no database"))
			return
		}
		looks := h.looks()
		if body.IfUnset {
			c, err := h.lookChoice(r.Context())
			if err != nil {
				h.logf("look: read choice before migration: %v", err)
				writeErr(w, http.StatusInternalServerError, i18n.Err("http-status.internal"))
				return
			}
			if c != nil {
				writeJSON(w, lookCurrentOf(c, looks))
				return
			}
		}
		body.ID = compat.Looks(body.ID)
		if !slices.ContainsFunc(looks, func(v lookView) bool { return v.ID == body.ID }) {
			writeErr(w, http.StatusNotFound, fmt.Errorf("no look %q", body.ID))
			return
		}
		cur, err := h.chooseLook(r.Context(), body.ID, looks)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, cur)
	})
}

// personaFileHeaders are what a package's file is served with: an SVG may not run anything even opened by itself, and
// a file asked for at the package's current rev never changes.
func personaFileHeaders(w http.ResponseWriter, name string, immutable bool) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasSuffix(name, ".svg") {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	if immutable {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	}
}
