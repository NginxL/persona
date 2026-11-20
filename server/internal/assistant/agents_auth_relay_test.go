package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The link an agent stops at in these tests, as the triage gives it and its command printed it (placeholders).
const relayLink = "https://accounts.example.com/oauth/device/verify?flow_id=F&user_code=ABCD-EFGH"

// A turn that stops at an authorization link: the triage's answer.
const linkTriage = `{"phase":"blocked","on":"user","line":"等你授权",
	"link":{"url":"` + relayLink + `","minutes":10}}`

// relayExt is the extensions with one that completes relayLink's kind, answering res.
type relayExt struct {
	guestExt
	mu    sync.Mutex
	res   wire.AuthRelayResult
	asked []string
}

func (e *relayExt) AuthRelays(link string) bool {
	return strings.HasPrefix(link, "https://accounts.example.com/")
}

func (e *relayExt) CompleteAuth(_ context.Context, link string) (wire.AuthRelayResult, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.asked = append(e.asked, link)
	return e.res, e.res.Status != ""
}

func (e *relayExt) tries() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.asked...)
}

// printedBy is a last turn whose command printed the link.
func printedBy(tool, text string) []proto.Item {
	return []proto.Item{
		{ID: "u1", Kind: proto.KindToolUse, ToolUseID: "t1", ToolName: tool},
		{ID: "r1", Kind: proto.KindToolResult, ToolUseID: "t1", Text: text},
	}
}

func relayService(t *testing.T, res wire.AuthRelayResult, items []proto.Item) (*Service, *fakeHub, *relayExt) {
	t.Helper()
	s, f := workService(t)
	e := &relayExt{res: res}
	s.SetExtensions(e)
	f.triage = linkTriage
	f.setSessions("box", stopped("s1", "请打开链接授权"))
	f.setItems(items...)
	return s, f, e
}

// The extension authorized it: the agent is told, no card goes up.
func TestAuthRelayDone(t *testing.T) {
	s, f, e := relayService(t, wire.AuthRelayResult{Status: "done", App: "示例 CLI"},
		printedBy("Bash", `{"verification_url":"`+strings.ReplaceAll(relayLink, "&", `\u0026`)+`"}`))
	triage(t, s, "s1")
	if cards := s.workCards(); len(cards) != 0 {
		t.Fatalf("cards %+v", cards)
	}
	got := f.sentToAgents()
	if len(got) != 1 || !strings.Contains(got[0], "Persona 已经用用户本人已登录的账号确认了授权") || !strings.Contains(got[0], "「示例 CLI」") {
		t.Fatalf("sent %v", got)
	}
	if tries := e.tries(); len(tries) != 1 || tries[0] != relayLink {
		t.Fatalf("tries %v", tries)
	}
	if en := entryOf(t, s, "s1"); en.InteractionID != "" || en.Work.On == proto.WaitUser || en.Work.Link != nil || !strings.Contains(en.Work.Line, "示例 CLI") {
		t.Fatalf("entry %+v", en)
	}
}

// Not one the extension completes: the card as before, saying why.
func TestAuthRelayManual(t *testing.T) {
	s, f, _ := relayService(t, wire.AuthRelayResult{Status: "manual", Reason: "new_permissions"}, printedBy("Bash", "Open "+relayLink+" to sign in"))
	triage(t, s, "s1")
	cards := s.workCards()
	if len(cards) != 1 || !strings.Contains(cards[0].Questions[0].Question, "device/verify") ||
		!strings.Contains(cards[0].Questions[0].Question, "新的权限") || cards[0].Questions[0].Options[0].Label != answerLabel("zh-Hans", answerAuthed) {
		t.Fatalf("cards %+v", cards)
	}
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// Only a link a command of its own printed is handed on: not one the agent wrote, a page it fetched had, a failed
// command printed, or the start of a longer one.
func TestAuthRelayOnlyItsOwnCommand(t *testing.T) {
	for name, items := range map[string][]proto.Item{
		"written":  {{ID: "a1", Kind: proto.KindText, Role: "assistant", Text: "open " + relayLink}},
		"fetched":  printedBy("WebFetch", relayLink),
		"failed":   {{ID: "u1", Kind: proto.KindToolUse, ToolUseID: "t1", ToolName: "Bash"}, {ID: "r1", Kind: proto.KindToolResult, ToolUseID: "t1", IsError: true, Text: relayLink}},
		"longer":   printedBy("Bash", relayLink+"&redirect_uri=https://evil.example"),
		"no items": nil,
	} {
		s, f, e := relayService(t, wire.AuthRelayResult{Status: "done"}, items)
		triage(t, s, "s1")
		cards := s.workCards()
		if len(e.tries()) != 0 || len(f.sentToAgents()) != 0 || len(cards) != 1 || !strings.Contains(cards[0].Questions[0].Question, "不是它自己的命令") {
			t.Errorf("%s: tries %v cards %+v", name, e.tries(), cards)
		}
	}
}

// No extension completes it (none installed, or none takes it): the card exactly as before.
func TestAuthRelayOff(t *testing.T) {
	for _, ext := range []bool{false, true} {
		s, f := workService(t)
		if ext {
			s.SetExtensions(&relayExt{}) // names it, then says not_handled
		}
		f.triage = linkTriage
		f.setSessions("box", stopped("s1", "请打开链接授权"))
		f.setItems(printedBy("Bash", relayLink)...)
		triage(t, s, "s1")
		cards := s.workCards()
		if len(cards) != 1 || strings.Contains(cards[0].Questions[0].Question, "\n") {
			t.Fatalf("ext %v: cards %+v", ext, cards)
		}
	}
}

func TestCommandPrinted(t *testing.T) {
	cut := []proto.Item{
		{ID: "u1", Kind: proto.KindToolUse, ToolUseID: "t1", ToolName: "BashOutput"},
		{ID: "r1", Kind: proto.KindToolResult, ToolUseID: "t1", Text: "…", FullSize: 9000},
	}
	full := func(id string) (string, bool) { return "Open " + relayLink + ". It expires in 10 minutes", id == "r1" }
	if !commandPrinted(cut, relayLink, full) {
		t.Error("a long output a page cut: its whole text is read")
	}
	for text, want := range map[string]bool{
		relayLink:                           true,
		"(" + relayLink + ")":               true,
		relayLink + ", then":                true,
		relayLink + ".":                     true,
		relayLink + "X":                     false,
		relayLink + ".evil":                 false,
		strings.TrimSuffix(relayLink, "GH"): false,
	} {
		if got := commandPrinted(printedBy("Bash", text), relayLink, nil); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
	// Go's encoding/json writes & as \u0026: the whole link is printed, its start before an & is not.
	b, _ := json.Marshal(map[string]string{"url": relayLink})
	if !commandPrinted(printedBy("Bash", string(b)), relayLink, nil) {
		t.Errorf("%s: the link a JSON printer escaped is not found", b)
	}
	if head := strings.Split(relayLink, "&")[0]; commandPrinted(printedBy("Bash", string(b)), head, nil) {
		t.Errorf("%s: %q, the start of a longer link, is taken as printed", b, head)
	}
}

func TestGoldenAuthRelayed(t *testing.T) {
	golden(t, "agent_auth_relayed", renderAuthRelayed("示例 CLI", ""))
	golden(t, "agent_auth_relayed.no_app", renderAuthRelayed("", ""))
}

// A computer a member hub brought is not the user's own here: its links are not handed on.
func TestAuthRelayNotAMembersComputer(t *testing.T) {
	s, f, e := relayService(t, wire.AuthRelayResult{Status: "done"}, printedBy("Bash", relayLink))
	f.mu.Lock()
	f.devices = []Device{{ID: "box", Name: "box", Kind: memberKind, Online: true, Reachable: true}}
	f.mu.Unlock()
	triage(t, s, "s1")
	cards := s.workCards()
	if len(e.tries()) != 0 || len(f.sentToAgents()) != 0 || len(cards) != 1 || strings.Contains(cards[0].Questions[0].Question, "\n") {
		t.Fatalf("tries %v cards %+v", e.tries(), cards)
	}
}
