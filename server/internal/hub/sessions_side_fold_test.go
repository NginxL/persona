package hub

import (
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// sideFoldHub is a hub whose machine "demo" lists a parent with two side sessions folded under it (the second a side
// of the first) and a side's Codex child, as the manager folds them (#7944), and a row of its own.
func sideFoldHub(t *testing.T) *Hub {
	t.Helper()
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo", Name: "Studio"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	row := func(id, state string, n int, under, as, side string, kids *proto.SessionChildren) proto.Session {
		s := proto.Session{MachineID: "demo", SessionID: id, Title: id, State: state, Cwd: "/demo/app", UpdatedAt: time.Unix(int64(1000+n), 0)}
		s.SetFold(under, as, side, kids)
		return s
	}
	side := row("side", proto.StateUnread, 2, "parent", proto.FoldedAsSide, "", nil)
	side.SetParentAgentID("parent")
	side.LastText = "The tokenizer is twice as fast with the lookup table."
	side2 := row("side2", proto.StateWorking, 3, "parent", proto.FoldedAsSide, "", nil)
	side2.SetParentAgentID("side")
	side2.LastText = "Measuring the lookup table again."
	kid := row("kid", proto.StateWorking, 4, "parent", "", "side", nil)
	kid.Agent, kid.IsSubagent, kid.ParentSessionID = "codex", true, "side"
	kid.LastText = "lookup table benchmark"
	m.sessions = []proto.Session{
		row("parent", proto.StateIdle, 1, "", "", "", &proto.SessionChildren{Total: 3, Working: 2, Sides: 2, SidesWorking: 1, SidesUnread: 1}),
		side, side2, kid,
		row("solo", proto.StateUnread, 5, "", "", "", nil),
	}
	return h
}

// roots=1 lists the parent once, by its folded state, with its sides and their children left out of the rows and
// every count; parent_session_id=<parent> lists them with the keys the clients route by (#7944).
func TestRuntimeListSideFold(t *testing.T) {
	h := sideFoldHub(t)
	roots := runtimePage(t, h, "limit=50&roots=1")
	if got := pageIDs(roots); !slices.Equal(got, []string{"demo/solo", "demo/parent"}) || roots.Total != 2 {
		t.Fatalf("roots: %v (total %d)", got, roots.Total)
	}
	if p := roots.Sessions[1]; p.Children() != (proto.SessionChildren{Total: 3, Working: 2, Sides: 2, SidesWorking: 1, SidesUnread: 1}) {
		t.Fatalf("the parent's row: %+v", p.Children())
	}
	if c := hierarchyCounts(t, h, "roots=1"); c.Total != 2 || c.State[proto.StateWorking] != 1 || c.State[proto.StateUnread] != 1 {
		t.Fatalf("roots counts: %+v", c)
	}
	under := runtimePage(t, h, "summary=1&limit=50&machine_id=demo&parent_session_id=parent")
	if got := pageIDs(under); !slices.Equal(got, []string{"demo/kid", "demo/side2", "demo/side"}) {
		t.Fatalf("under the parent: %v", got)
	}
	for _, s := range under.Sessions {
		as, side := s.FoldedAs(), s.FoldedSide()
		switch s.SessionID {
		case "side", "side2":
			if as != proto.FoldedAsSide || side != "" || s.ParentAgentID() == "" {
				t.Fatalf("%s: as %q side %q", s.SessionID, as, side)
			}
		case "kid":
			if as != "" || side != "side" {
				t.Fatalf("kid: as %q side %q", as, side)
			}
		}
	}
}

// With roots=1 a search hit in a folded side comes back as its parent's row, naming the side in hit_session_id with the
// side's own evidence; the parent's own hit wins a tie, a side's child is not searched as the parent, and without roots
// each row answers for itself (#7944).
func TestRuntimeSearchSideFold(t *testing.T) {
	h := sideFoldHub(t)
	page := searchRuntime(t, h, "search="+url.QueryEscape("twice as fast")+"&summary=1&roots=1")
	if got := pageIDs(page.listTestPage); !slices.Equal(got, []string{"demo/parent"}) || page.Total != 1 {
		t.Fatalf("a side's hit: %v (total %d)", got, page.Total)
	}
	hit := page.Hits[runtimeListID("demo", "parent")]
	if hit.HitSessionID != "side" || hit.Evidence == nil || hit.Evidence.Match != "twice" {
		t.Fatalf("the hit does not name the side: %+v", hit)
	}
	// Two sides match: one row, by the side whose id comes first; the child's text is not the parent's.
	page = searchRuntime(t, h, "search="+url.QueryEscape("lookup table")+"&summary=1&roots=1")
	if got := pageIDs(page.listTestPage); !slices.Equal(got, []string{"demo/parent"}) {
		t.Fatalf("two sides' hits: %v", got)
	}
	if hit := page.Hits[runtimeListID("demo", "parent")]; hit.HitSessionID != "side" {
		t.Fatalf("the sides' tie: %+v", hit)
	}
	// The parent's own title wins over its sides.
	page = searchRuntime(t, h, "search=parent&summary=1&roots=1")
	if hit := page.Hits[runtimeListID("demo", "parent")]; len(page.Sessions) != 1 || hit.HitSessionID != "" || hit.Score != 105 {
		t.Fatalf("the parent's own hit: %v %+v", pageIDs(page.listTestPage), hit)
	}
	// Filters apply to the row returned: the parent shows working (its side works), not unread.
	if page := searchRuntime(t, h, "search="+url.QueryEscape("twice as fast is:working")+"&summary=1&roots=1"); page.Total != 1 {
		t.Fatalf("the parent's folded state: %v", pageIDs(page.listTestPage))
	}
	if page := searchRuntime(t, h, "search="+url.QueryEscape("twice as fast is:unread")+"&summary=1&roots=1"); page.Total != 0 {
		t.Fatalf("the side's own state filtered the parent: %v", pageIDs(page.listTestPage))
	}
	// Without roots every row answers for itself.
	page = searchRuntime(t, h, "search="+url.QueryEscape("lookup table")+"&summary=1")
	if got := pageIDs(page.listTestPage); len(got) != 3 || slices.Contains(got, "demo/parent") {
		t.Fatalf("without roots: %v", got)
	}
	for id, hit := range page.Hits {
		if hit.HitSessionID != "" {
			t.Fatalf("%s names a hit elsewhere without roots: %+v", id, hit)
		}
	}
}

// sessions_side_fold is listed only while every manager that carries Agents names it, as agent_session_archives is.
func TestSessionsSideFoldNeedsEveryManager(t *testing.T) {
	h, _, a, b := archiveRelayHub(t)
	hello := func(m *guardManager, side bool) {
		t.Helper()
		caps := []string{assistantwire.CapabilityConfigOwn, assistantwire.CapabilityAgentSessionArchives}
		if side {
			caps = append(caps, assistantwire.CapabilitySessionsSideFold)
		}
		if err := m.call(t, busHello, map[string]any{"id": m.id, "capabilities": caps}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if slices.Contains(h.features(), featureSessionsSideFold) {
		t.Fatal("advertised before any manager named it")
	}
	hello(a, true)
	hello(b, false)
	if slices.Contains(h.features(), featureSessionsSideFold) {
		t.Fatal("mixed manager versions must hide the feature")
	}
	hello(b, true)
	if !slices.Contains(h.features(), featureSessionsSideFold) {
		t.Fatalf("every manager folds sides: %v", h.features())
	}
	if err := h.busConnOf(b.id).ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "manager offline", func() bool { return h.busConnOf(b.id) == nil })
	if slices.Contains(h.features(), featureSessionsSideFold) {
		t.Fatal("an offline manager must hide the feature")
	}
}
