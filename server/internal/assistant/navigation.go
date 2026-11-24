package assistant

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// The user's navigation choices (#9071, docs/protocol/protocol.md "Navigation"): the page the apps open on, how the
// sidebar's pages are arranged (their order, the ones put away, the groups) and how Overview is (its extension cards'
// order, the ones hidden, their widths: #9745; its own sections among them: #10905). The default
// manager keeps them for the hub, and a hub has one owner, so they are the user's: every device of theirs follows. Kept
// in the settings table under navigationKey (a key of its own: no migration). A field never stored is left out of the
// answer, so an app tells "never chosen" from "the default chosen" (an empty start) and may hand over a choice it kept
// while there was no record (docs/web/web.md "Start page").
const navigationKey = "navigation"

// navigation is the stored record. A PUT changes only the fields it carries.
type navigation struct {
	// Start is the page the apps open on: "" for the default (the main conversation), else a page's key as the web's
	// sidebar names it (`home`, `inbox`, `todos`, …, or `p:<extension>/<page>` for an extension's nav_page).
	Start *string `json:"start,omitempty"`
	// Pages is the sidebar's arrangement, as the web keeps it (web/src/lib/sidebar-pages.ts): a page's key as for Start.
	Pages *sidebarPages `json:"pages,omitempty"`
	// Overview is Overview's extension cards and its own sections as the user arranged them, as the web keeps it
	// (web/src/lib/overview-cards.ts). An empty list is the default chosen (the sections in their order, then every card
	// in the hub's order, half a row: the cards last, #11147), not "never chosen".
	Overview *[]overviewPlace `json:"overview,omitempty"`
}

// overviewPlace is one of Overview's cards (an extension's overview_card, docs/extensions/extensions.md "UI slots") as
// the user placed it: the extension's id and the card's, whether it is hidden, and its width: half a row ("" or "half",
// the default) or the whole row ("full"). Or one of Overview's own sections (#10905): its id in Section, whether it
// is hidden, and nothing else. Which cards and sections there are is the apps' business: one the list names that is
// not there now is passed over, and one it does not name keeps its place by default (web/src/lib/overview-cards.ts).
type overviewPlace struct {
	Extension string `json:"extension,omitempty"`
	ID        string `json:"id,omitempty"`
	Section   string `json:"section,omitempty"`
	Hidden    bool   `json:"hidden,omitempty"`
	Span      string `json:"span,omitempty"`
}

// overviewCardsMost: cards and sections in the list, as many as the sidebar's pages.
const overviewCardsMost = sidebarPagesMost

// overviewSection is a section's id: the web's (needs_you, in_progress, …) and, so a newer web's is kept, any of that shape.
var overviewSection = lazyre.New(`^[a-z][a-z0-9_]{0,31}$`)

// checkOverview says what in cards is not of the shape the apps write: a card's ids as an extension's and a UI
// contribution's (extension.ValidID) and a width of "half" or "full"; a section's id of its shape and no card's fields;
// each card and section once.
func checkOverview(cards []overviewPlace) error {
	if len(cards) > overviewCardsMost {
		return fmt.Errorf("overview: at most %d cards and sections", overviewCardsMost)
	}
	seen := make(map[string]bool, len(cards))
	for _, c := range cards {
		k := c.Extension + "/" + c.ID
		switch {
		case c.Section != "":
			if !overviewSection.MatchString(c.Section) || c.Extension != "" || c.ID != "" || c.Span != "" {
				return fmt.Errorf("overview: %q is not one of Overview's sections", c.Section)
			}
			k = c.Section // a card's key has a "/"
		case !extension.ValidID(c.Extension) || !extension.ValidID(c.ID):
			return fmt.Errorf("overview: %q / %q is not an extension's card", c.Extension, c.ID)
		case c.Span != "" && c.Span != "half" && c.Span != "full":
			return fmt.Errorf("overview: a card's span is half or full, not %q", c.Span)
		}
		if seen[k] {
			return fmt.Errorf("overview: %s more than once", k)
		}
		seen[k] = true
	}
	return nil
}

// keepSections is what a change to list leaves of Overview: list, unless it names cards and no section while the stored
// one names sections. Such a list comes from an app that does not know the sections (#9745's web): the sections stay
// where they are, the cards take the stored cards' places in list's order, and one the stored list did not have goes
// after the last of them (none: where the cards are by default, last, under the sections, #11147). An empty list
// (Reset) is taken as it is, as is one that would hold more than the most there may be.
func keepSections(stored, list []overviewPlace) []overviewPlace {
	if len(list) == 0 || slices.ContainsFunc(list, func(c overviewPlace) bool { return c.Section != "" }) ||
		!slices.ContainsFunc(stored, func(c overviewPlace) bool { return c.Section != "" }) {
		return list
	}
	out := make([]overviewPlace, 0, len(stored)+len(list))
	next, after := 0, -1
	for _, c := range stored {
		if c.Section == "" {
			if next == len(list) {
				continue
			}
			c, next = list[next], next+1
			after = len(out) + 1
		}
		out = append(out, c)
	}
	if after < 0 {
		after = len(out)
	}
	out = slices.Insert(out, after, list[next:]...)
	if len(out) > overviewCardsMost {
		return list
	}
	return out
}

// sidebarPages: the pages in the user's order; those switched into the list or put under More; the groups in their
// order ("" is the built-in Pages group, a user's own is "u:<id>", any other an extension's nav_page group) with the
// title the user gave (none: the group's own); the group a page was moved to; and the extensions' groups removed.
type sidebarPages struct {
	Order  []string `json:"order"`
	Shown  []string `json:"shown"`
	Hidden []string `json:"hidden"`
	// More is an explicit visibility choice for the More entry. Nil and true leave it shown.
	More    *bool             `json:"more,omitempty"`
	Groups  []sidebarGroup    `json:"groups,omitempty"`
	Assign  map[string]string `json:"assign,omitempty"`
	Removed []string          `json:"removed,omitempty"`
}

type sidebarGroup struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

const (
	sidebarPagesMost  = 256 // keys in each list, pages moved to a group
	sidebarGroupsMost = 32
)

var sidebarGroupID = lazyre.New(`^(?:u:[a-z0-9]{1,16}|[a-z][a-z0-9-]{0,31})$`)

func validGroupID(id string) bool { return id == "" || sidebarGroupID.MatchString(id) }

// check says what in p is not of the shape the apps write.
func (p *sidebarPages) check() error {
	for _, list := range [][]string{p.Order, p.Shown, p.Hidden} {
		if len(list) > sidebarPagesMost {
			return fmt.Errorf("pages: at most %d keys in a list", sidebarPagesMost)
		}
		for _, k := range list {
			if k == "" || !validStartPage(k) {
				return fmt.Errorf("pages: %q is not a page's key", k)
			}
		}
	}
	if len(p.Groups) > sidebarGroupsMost || len(p.Removed) > sidebarGroupsMost {
		return fmt.Errorf("pages: at most %d groups", sidebarGroupsMost)
	}
	seen := map[string]bool{}
	for _, g := range p.Groups {
		n := utf8.RuneCountInString(strings.TrimSpace(g.Title))
		if !validGroupID(g.ID) || seen[g.ID] || n > 40 || (strings.HasPrefix(g.ID, "u:") && n == 0) {
			return fmt.Errorf("pages: group %q: a group's id once, and its title at most 40 characters (a user's group has one)", g.ID)
		}
		seen[g.ID] = true
	}
	for _, id := range p.Removed {
		if id == "" || strings.HasPrefix(id, "u:") || !validGroupID(id) {
			return fmt.Errorf("pages: %q is not an extension's group", id)
		}
	}
	if len(p.Assign) > sidebarPagesMost {
		return fmt.Errorf("pages: at most %d pages moved to a group", sidebarPagesMost)
	}
	for k, id := range p.Assign {
		if k == "" || !validStartPage(k) || !validGroupID(id) {
			return fmt.Errorf("pages: %q in group %q", k, id)
		}
	}
	return nil
}

// startPagePattern is a start page's shape: a built-in page's key, or an extension page's `p:<extension>/<page>`. Which
// pages there are is the apps' business (an extension's page comes and goes): an app opens on the default for a key it
// does not know or a page that is not there.
var startPagePattern = lazyre.New(`^(?:[a-z][a-z0-9-]{0,63}|p:[^/\x00-\x20\x7f]{1,128}/[^/\x00-\x20\x7f]{1,128})$`)

func validStartPage(s string) bool { return s == "" || startPagePattern.MatchString(s) }

func (s *Service) navigationRoutes() {
	s.handle("GET /api/v1/navigation", s.getNavigation)
	s.handle("PUT /api/v1/navigation", s.putNavigation)
}

func (s *Service) getNavigation(w http.ResponseWriter, r *http.Request) {
	var n navigation
	if st := s.store(); st != nil {
		if _, err := st.Setting(r.Context(), navigationKey, &n); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
	}
	apierr.WriteJSON(w, n)
}

func (s *Service) putNavigation(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, errors.New("only a device with full access may change the start page"))
		return
	}
	var body navigation
	if err := decodeBody(w, r, &body, 64<<10); err != nil || (body.Start == nil && body.Pages == nil && body.Overview == nil) {
		writeBodyErr(w, err, errors.New("start, pages or overview is required"))
		return
	}
	if body.Start != nil && !validStartPage(*body.Start) {
		apierr.Write(w, http.StatusBadRequest, errors.New("start must be empty, a page's key or p:<extension>/<page>"))
		return
	}
	if body.Pages != nil {
		if err := body.Pages.check(); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	if body.Overview != nil {
		if err := checkOverview(*body.Overview); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	st := s.store()
	if st == nil {
		apierr.Write(w, http.StatusServiceUnavailable, errors.New("the start page needs a database"))
		return
	}
	s.navMu.Lock()
	defer s.navMu.Unlock()
	var n navigation
	if _, err := st.Setting(r.Context(), navigationKey, &n); err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, err)
		return
	}
	ev := map[string]any{"type": "navigation"}
	if body.Start != nil {
		n.Start = body.Start
		ev["start"] = *n.Start
	}
	if body.Pages != nil {
		n.Pages = body.Pages
		ev["pages"] = n.Pages
	}
	if body.Overview != nil {
		var stored []overviewPlace
		if n.Overview != nil {
			stored = *n.Overview
		}
		kept := keepSections(stored, *body.Overview)
		n.Overview = &kept
		ev["overview"] = kept
	}
	if err := st.PutSetting(r.Context(), navigationKey, n); err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, err)
		return
	}
	// every app follows at once: its copy decides the next opening and draws the sidebar and Overview before the hub
	// answers. Only what changed goes out, so an app holding a change of another field it has not sent yet keeps it.
	s.event(r.Context(), ev)
	apierr.WriteJSON(w, n)
}
