package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// navigationHub keeps what the service broadcasts, whole.
type navigationHub struct {
	*fakeHub
	mu   sync.Mutex
	sent []map[string]any
}

func (h *navigationHub) Broadcast(ctx context.Context, b Broadcast) {
	var m map[string]any
	_ = json.Unmarshal(b.Message, &m)
	h.mu.Lock()
	h.sent = append(h.sent, m)
	h.mu.Unlock()
	h.fakeHub.Broadcast(ctx, b)
}

func (h *navigationHub) take() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.sent
	h.sent = nil
	return out
}

func navigationService(t *testing.T, st Store) (*Service, *navigationHub) {
	t.Helper()
	h := &navigationHub{fakeHub: &fakeHub{}}
	o := Options{Hub: h, Logf: testLogf(t)}
	if st != nil {
		o.Store = func() Store { return st }
	}
	return New(o), h
}

func navigationCall(t *testing.T, s *Service, method, body, role string) (int, map[string]any) {
	t.Helper()
	w := boardCall(s, method, "/api/v1/navigation", body, role)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// The start page (#9071): never chosen, the answer has no start, so an app may hand over the one it kept; a choice is
// stored and every app is told; the default chosen is an empty start, kept as such (not "never chosen", which would let
// another browser's older copy come back); a key of the wrong shape, a body without start, or a device that may only
// answer changes nothing and tells no one.
func TestNavigationStartPage(t *testing.T) {
	s, hub := navigationService(t, newSettingsBoardStore())
	if code, got := navigationCall(t, s, "GET", "", roleFull); code != http.StatusOK || len(got) != 0 {
		t.Fatalf("never chosen: %d %v, want 200 {}", code, got)
	}
	for _, start := range []string{"p:example-today/today", "inbox", "workspace-board", ""} {
		code, got := navigationCall(t, s, "PUT", `{"start":"`+start+`"}`, roleFull)
		if code != http.StatusOK || got["start"] != start {
			t.Fatalf("PUT %q: %d %v", start, code, got)
		}
		if sent := hub.take(); len(sent) != 1 || sent[0]["type"] != "navigation" || sent[0]["start"] != start {
			t.Fatalf("PUT %q told the apps %v", start, sent)
		}
		if code, got := navigationCall(t, s, "GET", "", roleAnswer); code != http.StatusOK || got["start"] != start {
			t.Fatalf("GET after PUT %q: %d %v", start, code, got)
		}
	}
	if _, got := navigationCall(t, s, "GET", "", roleFull); got["start"] != "" || len(got) != 1 {
		t.Fatalf("the default chosen reads %v, want an empty start that is there", got)
	}

	navigationCall(t, s, "PUT", `{"start":"todos"}`, roleFull)
	hub.take()
	for _, bad := range []string{`{}`, `{"start":null}`, `{"start":"Inbox"}`, `{"start":"../settings"}`, `{"start":"p:only-extension"}`,
		`{"start":"p:a/b/c"}`, `{"start":"p:a b/c"}`, `{"start":"` + strings.Repeat("x", 65) + `"}`, `{"start":1}`, `nope`} {
		if code, _ := navigationCall(t, s, "PUT", bad, roleFull); code != http.StatusBadRequest {
			t.Fatalf("PUT %s: %d, want 400", bad, code)
		}
	}
	if code, _ := navigationCall(t, s, "PUT", `{"start":"kb"}`, roleAnswer); code != http.StatusForbidden {
		t.Fatalf("an answer-only device: %d, want 403", code)
	}
	if _, got := navigationCall(t, s, "GET", "", roleFull); got["start"] != "todos" {
		t.Fatalf("refused changes moved the start page: %v", got)
	}
	if sent := hub.take(); len(sent) != 0 {
		t.Fatalf("refused changes told the apps %v", sent)
	}
}

// Without a database nothing is kept: the answer is the default, and a change says why it is refused.
func TestNavigationWithoutStore(t *testing.T) {
	s, _ := navigationService(t, nil)
	if code, got := navigationCall(t, s, "GET", "", roleFull); code != http.StatusOK || len(got) != 0 {
		t.Fatalf("GET: %d %v", code, got)
	}
	if code, _ := navigationCall(t, s, "PUT", `{"start":"inbox"}`, roleFull); code != http.StatusServiceUnavailable {
		t.Fatalf("PUT: %d, want 503", code)
	}
}

// On the manager's database the record is a key of its settings like any other, and another service on it reads it.
func TestNavigationOnDatabase(t *testing.T) {
	dsn := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	var before json.RawMessage
	had, err := store.Setting(ctx, navigationKey, &before)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { // the lane's database is shared: put back what was there
		if had {
			_ = store.PutSetting(context.Background(), navigationKey, before)
		} else {
			_ = store.PutSetting(context.Background(), navigationKey, map[string]any{})
		}
	})
	s, _ := navigationService(t, store)
	if code, got := navigationCall(t, s, "PUT", `{"start":"p:example-today/today"}`, roleFull); code != http.StatusOK || got["start"] != "p:example-today/today" {
		t.Fatalf("PUT: %d %v", code, got)
	}
	again, _ := navigationService(t, store)
	if code, got := navigationCall(t, again, "GET", "", roleFull); code != http.StatusOK || got["start"] != "p:example-today/today" {
		t.Fatalf("another service on the database read %d %v", code, got)
	}
}

// The sidebar's arrangement (#9071 groups) is kept beside the start page: a PUT of one leaves the other, the apps are
// told only what changed, and an arrangement of another shape changes nothing.
func TestNavigationSidebarPages(t *testing.T) {
	s, hub := navigationService(t, newSettingsBoardStore())
	pages := `{"order":["p:example-today/today","home"],"shown":[],"hidden":["kb"],"more":false,` +
		`"groups":[{"id":"workbench","title":"Work"},{"id":""},{"id":"u:a1b2","title":"生活"}],` +
		`"assign":{"p:example-today/today":"workbench","routines":"u:a1b2","inbox":""},"removed":["assets"]}`
	navigationCall(t, s, "PUT", `{"start":"inbox"}`, roleFull)
	hub.take()
	code, got := navigationCall(t, s, "PUT", `{"pages":`+pages+`}`, roleFull)
	if code != http.StatusOK || got["start"] != "inbox" || got["pages"] == nil {
		t.Fatalf("PUT pages: %d %v", code, got)
	}
	sent := hub.take()
	if len(sent) != 1 || sent[0]["type"] != "navigation" || sent[0]["pages"] == nil {
		t.Fatalf("told %v", sent)
	}
	if _, has := sent[0]["start"]; has {
		t.Fatalf("a change of the pages told the start page again: %v", sent[0])
	}
	_, got = navigationCall(t, s, "GET", "", roleFull)
	p, _ := got["pages"].(map[string]any)
	if got["start"] != "inbox" || p == nil || p["more"] != false || len(p["groups"].([]any)) != 3 || p["assign"].(map[string]any)["routines"] != "u:a1b2" {
		t.Fatalf("GET: %v", got)
	}
	if sentPages, _ := sent[0]["pages"].(map[string]any); sentPages["more"] != false {
		t.Fatalf("the More choice was not told to the apps: %v", sent)
	}
	navigationCall(t, s, "PUT", `{"start":"kb"}`, roleFull)
	if _, got = navigationCall(t, s, "GET", "", roleFull); got["pages"] == nil || got["start"] != "kb" {
		t.Fatalf("a change of the start page kept the pages: %v", got)
	}
	hub.take()
	long := `"` + strings.Repeat("长", 41) + `"`
	for _, bad := range []string{
		`{"pages":{"order":[""],"shown":[],"hidden":[]}}`,
		`{"pages":{"order":["Not A Key"],"shown":[],"hidden":[]}}`,
		`{"pages":{"order":[],"shown":[],"hidden":[],"groups":[{"id":"work"},{"id":"work"}]}}`,
		`{"pages":{"order":[],"shown":[],"hidden":[],"groups":[{"id":"u:x1"}]}}`,
		`{"pages":{"order":[],"shown":[],"hidden":[],"groups":[{"id":"Work Bench","title":"x"}]}}`,
		`{"pages":{"order":[],"shown":[],"hidden":[],"groups":[{"id":"u:x1","title":` + long + `}]}}`,
		`{"pages":{"order":[],"shown":[],"hidden":[],"removed":["u:x1"]}}`,
		`{"pages":{"order":[],"shown":[],"hidden":[],"assign":{"inbox":"Not An Id"}}}`,
		`{"pages":{"order":[` + strings.Repeat(`"home",`, sidebarPagesMost) + `"home"],"shown":[],"hidden":[]}}`,
		`{"pages":1}`,
	} {
		if code, _ := navigationCall(t, s, "PUT", bad, roleFull); code != http.StatusBadRequest {
			t.Fatalf("PUT %.80s: %d, want 400", bad, code)
		}
	}
	if sent := hub.take(); len(sent) != 0 {
		t.Fatalf("refused arrangements told the apps %v", sent)
	}
}

// Overview's extension cards (#9745) are kept beside the start page and the sidebar's pages: a PUT of one leaves the
// others, the apps are told only what changed, an empty list is kept as the default chosen (not left out as never
// chosen), and a list of another shape changes nothing and tells no one.
func TestNavigationOverview(t *testing.T) {
	s, hub := navigationService(t, newSettingsBoardStore())
	navigationCall(t, s, "PUT", `{"start":"home","pages":{"order":["home"],"shown":[],"hidden":[]}}`, roleFull)
	hub.take()
	overview := `[{"extension":"example-quota","id":"left","span":"full"},{"extension":"example-gateway","id":"today"},` +
		`{"extension":"example-today","id":"agenda","hidden":true,"span":"half"}]`
	code, got := navigationCall(t, s, "PUT", `{"overview":`+overview+`}`, roleFull)
	cards, _ := got["overview"].([]any)
	if code != http.StatusOK || got["start"] != "home" || got["pages"] == nil || len(cards) != 3 {
		t.Fatalf("PUT overview: %d %v", code, got)
	}
	sent := hub.take()
	if len(sent) != 1 || sent[0]["type"] != "navigation" || len(sent[0]["overview"].([]any)) != 3 {
		t.Fatalf("told %v", sent)
	}
	for _, other := range []string{"start", "pages"} {
		if _, has := sent[0][other]; has {
			t.Fatalf("a change of the cards told %s again: %v", other, sent[0])
		}
	}
	first, _ := cards[0].(map[string]any)
	third, _ := cards[2].(map[string]any)
	if first["extension"] != "example-quota" || first["id"] != "left" || first["span"] != "full" || first["hidden"] != nil ||
		third["hidden"] != true || third["span"] != "half" {
		t.Fatalf("the cards came back as %v", cards)
	}

	// a PUT of another field leaves the cards alone, and tells nothing of them
	navigationCall(t, s, "PUT", `{"start":"kb"}`, roleFull)
	navigationCall(t, s, "PUT", `{"pages":{"order":[],"shown":[],"hidden":["kb"]}}`, roleFull)
	if _, got = navigationCall(t, s, "GET", "", roleAnswer); len(got["overview"].([]any)) != 3 || got["start"] != "kb" {
		t.Fatalf("other changes moved the cards: %v", got)
	}
	for _, m := range hub.take() {
		if _, has := m["overview"]; has {
			t.Fatalf("a change of another field told the cards: %v", m)
		}
	}

	// the default chosen (Reset) is an empty list that is there, so another browser's older copy is not handed over again
	if code, got = navigationCall(t, s, "PUT", `{"overview":[]}`, roleFull); code != http.StatusOK {
		t.Fatalf("PUT []: %d %v", code, got)
	}
	if sent := hub.take(); len(sent) != 1 || sent[0]["overview"] == nil || len(sent[0]["overview"].([]any)) != 0 {
		t.Fatalf("PUT [] told %v", sent)
	}
	if _, got = navigationCall(t, s, "GET", "", roleFull); got["overview"] == nil || len(got["overview"].([]any)) != 0 {
		t.Fatalf("the default chosen reads %v, want an empty list that is there", got)
	}

	navigationCall(t, s, "PUT", `{"overview":`+overview+`}`, roleFull)
	hub.take()
	var many []string
	for i := 0; i <= overviewCardsMost; i++ {
		many = append(many, fmt.Sprintf(`{"extension":"example-%d","id":"card"}`, i))
	}
	for _, bad := range []string{
		`{"overview":null}`,
		`{"overview":{}}`,
		`{"overview":[1]}`,
		`{"overview":"example-quota/left"}`,
		`{"overview":[{"extension":"","id":"left"}]}`,
		`{"overview":[{"extension":"example-quota"}]}`,
		`{"overview":[{"extension":"Example","id":"left"}]}`,
		`{"overview":[{"extension":"example/quota","id":"left"}]}`,
		`{"overview":[{"extension":"example-quota","id":"` + strings.Repeat("x", 33) + `"}]}`,
		`{"overview":[{"extension":"example-quota","id":"left","span":"wide"}]}`,
		`{"overview":[{"extension":"example-quota","id":"left","hidden":"yes"}]}`,
		`{"overview":[{"extension":"example-quota","id":"left"},{"extension":"example-quota","id":"left","span":"full"}]}`,
		`{"overview":[` + strings.Join(many, ",") + `]}`,
	} {
		if code, _ := navigationCall(t, s, "PUT", bad, roleFull); code != http.StatusBadRequest {
			t.Fatalf("PUT %.80s: %d, want 400", bad, code)
		}
	}
	if code, _ := navigationCall(t, s, "PUT", `{"overview":[]}`, roleAnswer); code != http.StatusForbidden {
		t.Fatalf("an answer-only device: %d, want 403", code)
	}
	if _, got = navigationCall(t, s, "GET", "", roleFull); len(got["overview"].([]any)) != 3 {
		t.Fatalf("refused changes moved the cards: %v", got)
	}
	if sent := hub.take(); len(sent) != 0 {
		t.Fatalf("refused changes told the apps %v", sent)
	}
	// the most cards there may be is taken
	if code, _ := navigationCall(t, s, "PUT", `{"overview":[`+strings.Join(many[:overviewCardsMost], ",")+`]}`, roleFull); code != http.StatusOK {
		t.Fatalf("PUT %d cards: %d, want 200", overviewCardsMost, code)
	}
}

// Overview's own sections (#10905) are entries of the same list, {"section", "hidden"?}: kept, answered and pushed as
// the apps write them, of their own shape; a list of only cards from an app that does not know them (#9745's web)
// leaves them where they are, and Reset ([]) takes them back to the default with the cards.
func TestNavigationOverviewSections(t *testing.T) {
	s, hub := navigationService(t, newSettingsBoardStore())
	keys := func(v any) string {
		var out []string
		for _, e := range v.([]any) {
			m := e.(map[string]any)
			k := fmt.Sprint(m["section"])
			if m["section"] == nil {
				k = fmt.Sprintf("%v/%v", m["extension"], m["id"])
			}
			if m["hidden"] == true {
				k += "!"
			}
			out = append(out, k)
		}
		return strings.Join(out, " ")
	}
	arranged := `[{"section":"in_progress"},{"extension":"example-quota","id":"left"},{"section":"needs_you"},` +
		`{"extension":"example-today","id":"agenda","span":"full"},{"section":"working","hidden":true},{"section":"computers"}]`
	code, got := navigationCall(t, s, "PUT", `{"overview":`+arranged+`}`, roleFull)
	const want = "in_progress example-quota/left needs_you example-today/agenda working! computers"
	if code != http.StatusOK || keys(got["overview"]) != want {
		t.Fatalf("PUT sections: %d %v", code, got)
	}
	if sent := hub.take(); len(sent) != 1 || keys(sent[0]["overview"]) != want {
		t.Fatalf("told %v", sent)
	}
	// a section is only its id (and hidden): no card's fields come back with it
	first := got["overview"].([]any)[0].(map[string]any)
	if len(first) != 1 || first["section"] != "in_progress" {
		t.Fatalf("a section came back as %v", first)
	}
	if _, got = navigationCall(t, s, "GET", "", roleAnswer); keys(got["overview"]) != want {
		t.Fatalf("GET: %v", got)
	}

	// #9745's web moves its cards (Agenda first, Needs me installed since) and knows no section: they stay where they are
	navigationCall(t, s, "PUT", `{"overview":[{"extension":"example-today","id":"agenda"},{"extension":"example-today","id":"needs"},`+
		`{"extension":"example-quota","id":"left","hidden":true}]}`, roleFull)
	const merged = "in_progress example-today/agenda needs_you example-today/needs example-quota/left! working! computers"
	if _, got = navigationCall(t, s, "GET", "", roleFull); keys(got["overview"]) != merged {
		t.Fatalf("a list of only cards moved the sections: %s, want %s", keys(got["overview"]), merged)
	}
	if sent := hub.take(); len(sent) != 1 || keys(sent[0]["overview"]) != merged {
		t.Fatalf("told %v, want what was kept", sent)
	}
	// fewer cards than before: the others' places go, the sections stay
	navigationCall(t, s, "PUT", `{"overview":[{"extension":"example-quota","id":"left"}]}`, roleFull)
	if _, got = navigationCall(t, s, "GET", "", roleFull); keys(got["overview"]) != "in_progress example-quota/left needs_you working! computers" {
		t.Fatalf("one card: %s", keys(got["overview"]))
	}
	// a list that names a section is taken as it is
	navigationCall(t, s, "PUT", `{"overview":[{"extension":"example-quota","id":"left"},{"section":"computers"}]}`, roleFull)
	if _, got = navigationCall(t, s, "GET", "", roleFull); keys(got["overview"]) != "example-quota/left computers" {
		t.Fatalf("a list with a section: %s", keys(got["overview"]))
	}
	// Reset takes the sections back with the cards
	navigationCall(t, s, "PUT", `{"overview":`+arranged+`}`, roleFull)
	navigationCall(t, s, "PUT", `{"overview":[]}`, roleFull)
	if _, got = navigationCall(t, s, "GET", "", roleFull); got["overview"] == nil || len(got["overview"].([]any)) != 0 {
		t.Fatalf("Reset: %v", got)
	}
	// a section a newer web has (an id of the shape) is kept
	navigationCall(t, s, "PUT", `{"overview":[{"section":"later_section"},{"section":"in_progress"}]}`, roleFull)
	if _, got = navigationCall(t, s, "GET", "", roleFull); keys(got["overview"]) != "later_section in_progress" {
		t.Fatalf("a newer web's section: %s", keys(got["overview"]))
	}
	hub.take()

	for _, bad := range []string{
		`{"overview":[{"section":"In-progress"}]}`,
		`{"overview":[{"section":"in progress"}]}`,
		`{"overview":[{"section":"` + strings.Repeat("x", 33) + `"}]}`,
		`{"overview":[{"section":"in_progress","extension":"example-quota","id":"left"}]}`,
		`{"overview":[{"section":"in_progress","id":"left"}]}`,
		`{"overview":[{"section":"in_progress","span":"full"}]}`,
		`{"overview":[{"section":"in_progress"},{"section":"in_progress","hidden":true}]}`,
		`{"overview":[{"section":""}]}`,
		`{"overview":[{"section":1}]}`,
	} {
		if code, _ := navigationCall(t, s, "PUT", bad, roleFull); code != http.StatusBadRequest {
			t.Fatalf("PUT %s: %d, want 400", bad, code)
		}
	}
	if sent := hub.take(); len(sent) != 0 {
		t.Fatalf("refused changes told the apps %v", sent)
	}

	// cards and sections count together toward the most there may be; a list of only cards that would go past it
	// with the stored sections is taken as it is
	var many []string
	for i := 0; i < overviewCardsMost; i++ {
		many = append(many, fmt.Sprintf(`{"extension":"example-%d","id":"card"}`, i))
	}
	if code, _ := navigationCall(t, s, "PUT", `{"overview":[{"section":"in_progress"},`+strings.Join(many, ",")+`]}`, roleFull); code != http.StatusBadRequest {
		t.Fatalf("PUT %d cards and a section: %d, want 400", overviewCardsMost, code)
	}
	if code, got := navigationCall(t, s, "PUT", `{"overview":[`+strings.Join(many, ",")+`]}`, roleFull); code != http.StatusOK || len(got["overview"].([]any)) != overviewCardsMost {
		t.Fatalf("PUT %d cards over stored sections: %d", overviewCardsMost, code)
	}
}

// keepSections on its own: where the cards of a list that names no section go among the stored sections.
func TestKeepSections(t *testing.T) {
	card := func(id string) overviewPlace { return overviewPlace{Extension: "example", ID: id} }
	section := func(id string) overviewPlace { return overviewPlace{Section: id} }
	show := func(l []overviewPlace) string {
		var out []string
		for _, c := range l {
			out = append(out, c.Section+c.ID)
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		stored, list []overviewPlace
		want         string
	}{
		{nil, []overviewPlace{card("a")}, "a"},
		{[]overviewPlace{card("a"), card("b")}, []overviewPlace{card("b"), card("a")}, "b a"},
		{[]overviewPlace{section("s"), card("a"), section("t"), card("b")}, []overviewPlace{card("b"), card("a"), card("c")}, "s b t a c"},
		{[]overviewPlace{section("s"), section("t")}, []overviewPlace{card("a")}, "s t a"},
		{[]overviewPlace{card("a"), section("s"), card("b")}, []overviewPlace{card("c")}, "c s"},
		{[]overviewPlace{section("s")}, nil, ""},
		{[]overviewPlace{section("s"), card("a")}, []overviewPlace{section("t"), card("a")}, "t a"},
	} {
		if got := show(keepSections(c.stored, c.list)); got != c.want {
			t.Errorf("keepSections(%s, %s) = %q, want %q", show(c.stored), show(c.list), got, c.want)
		}
	}
}
