package deeplink

import (
	"encoding/json"
	"os"
	"testing"
)

// fixture is spec/fixtures/deep-links/cases.json's sections this package writes (its README says what each holds).
type fixture struct {
	Version int `json:"version"`
	Build   []struct {
		Name string            `json:"name"`
		Page map[string]string `json:"page"`
		Path string            `json:"path"`
	} `json:"build"`
	Links []struct {
		Name string            `json:"name"`
		Base string            `json:"base"`
		Page map[string]string `json:"page"`
		Web  string            `json:"web"`
		App  string            `json:"app"`
	} `json:"links"`
}

func readFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile("../../../spec/fixtures/deep-links/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 || len(f.Build) == 0 || len(f.Links) == 0 {
		t.Fatalf("fixture version %d with %d build and %d links cases", f.Version, len(f.Build), len(f.Links))
	}
	return f
}

// build is the builder a fixture page names; "" for a page this package does not know.
func build(p map[string]string) string {
	path := ""
	switch p["page"] {
	case "persona":
		path = Persona()
	case "persona-new":
		path = NewConversation()
	case "conversation":
		path = ConversationOf(p["id"], p["machine"], p["manager"])
	case "session":
		path = Session(p["machine"], p["session"])
	case "agents":
		path = Agents()
	case "inbox":
		path = Inbox()
	case "inbox-item":
		path = InboxItem(p["id"], p["manager"])
	case "todos":
		path = Todos()
	case "todo":
		path = Todo(p["id"])
	case "knowledge":
		path = Knowledge(p["kind"])
	case "knowledge-entry":
		path = KnowledgeEntry(p["kind"], p["id"])
	case "machines":
		path = Machines()
	case "machine":
		path = Machine(p["id"])
	case "settings":
		path = Settings(p["tab"], p["item"])
	case "extension":
		path = Extension(p["extension"], p["ui"], p["state"])
	default:
		return ""
	}
	return At(path, p["at"])
}

func TestBuildMatchesSharedCases(t *testing.T) {
	for _, c := range readFixture(t).Build {
		if got := build(c.Page); got != c.Path {
			t.Errorf("%s: %v wrote %q, want %q", c.Name, c.Page, got, c.Path)
		}
	}
}

func TestWebAndAppLinksMatchSharedCases(t *testing.T) {
	for _, c := range readFixture(t).Links {
		path := build(c.Page)
		if got := Web(c.Base, path); got != c.Web {
			t.Errorf("%s: web link %q, want %q", c.Name, got, c.Web)
		}
		if got := App(path); got != c.App {
			t.Errorf("%s: app link %q, want %q", c.Name, got, c.App)
		}
	}
}

// The helpers callers use beyond the fixture's pages say the same paths.
func TestNamedPages(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{Recording("rec 5"), "/kb/recordings/rec%205"},
		{Meeting("m-31"), "/kb/meetings/m-31"},
		{ExtensionSettings("github"), "/settings?tab=extensions&extension=github"},
		{Settings("hub", "ignored"), "/settings?tab=hub"},
		{At(Conversation("c-7f3a"), ""), "/persona/c-7f3a"},
		{At(InboxItem("i-1", "m-2"), "msg-1"), "/inbox/i-1?manager=m-2&at=msg-1"},
		{Web("", Todo("t-19")), ""},
		{Web("https://hub.example.com", ""), ""},
		{App(""), ""},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
