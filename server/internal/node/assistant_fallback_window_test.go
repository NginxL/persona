package node

import (
	"context"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A Codex launcher's list in the order of a recorded failure (#10277): the configured 1M model failed on a rate limit,
// and the next entries hold 131k (Codex counts 95% of it). The conversation's last request was 193,884 tokens.
func recordedFallbackList() (proto.LauncherModels, map[string]int) {
	list := proto.LauncherModels{Agent: "codex", Models: []proto.AgentModel{
		{ID: "native-default", Default: true},
		{ID: "earlier-1m"},
		{ID: "earlier-272k"},
		{ID: "big-1m"},
		{ID: "small-131k"},
		{ID: "small-131k-fast"},
		{ID: "wide-2m"},
		{ID: "wide-2m-reasoning"},
	}}
	windows := map[string]int{
		"native-default": 1000000, "earlier-1m": 1000000, "earlier-272k": 272000, "big-1m": 1000000,
		"small-131k": 124518, "small-131k-fast": 124518, "wide-2m": 2000000, "wide-2m-reasoning": 2000000,
	}
	return list, windows
}

func TestNextAssistantModelSkipsAWindowTooSmallForTheConversation(t *testing.T) {
	list, windows := recordedFallbackList()
	fit := assistantFit{need: assistantWindowNeed(193884), prefer: 1000000, windows: windows}
	if got, ok := nextAssistantModel(list, "big-1m", map[string]bool{"big-1m": true}, fit); !ok || got != "wide-2m" {
		t.Fatalf("selected %q %v, want wide-2m: the 131k entries cannot hold a 194k-token conversation", got, ok)
	}
	// Without the windows the order is the list's, as before: the next entry.
	if got, ok := nextAssistantModel(list, "big-1m", map[string]bool{"big-1m": true}, assistantFit{}); !ok || got != "small-131k" {
		t.Fatalf("with no windows known selected %q %v, want the next entry small-131k", got, ok)
	}
}

// The models before the failed one are tried too, after those following it (#10277).
func TestNextAssistantModelWrapsToTheModelsBeforeTheFailedOne(t *testing.T) {
	list, windows := recordedFallbackList()
	fit := assistantFit{need: assistantWindowNeed(193884), prefer: 1000000, windows: windows}
	used := map[string]bool{"big-1m": true, "wide-2m": true, "wide-2m-reasoning": true}
	if got, ok := nextAssistantModel(list, "big-1m", used, fit); !ok || got != "earlier-1m" {
		t.Fatalf("selected %q %v, want earlier-1m from before the failed model", got, ok)
	}
	used["earlier-1m"] = true
	// No 1M model left: one that still holds the conversation, though smaller than the configured model's window.
	if got, ok := nextAssistantModel(list, "big-1m", used, fit); !ok || got != "earlier-272k" {
		t.Fatalf("selected %q %v, want earlier-272k", got, ok)
	}
	used["earlier-272k"] = true
	if got, ok := nextAssistantModel(list, "big-1m", used, fit); ok {
		t.Fatalf("selected %q, whose window cannot hold the conversation", got)
	}
}

// A model with at least the configured model's window comes before a smaller one that only holds the conversation.
func TestNextAssistantModelPrefersTheConfiguredWindow(t *testing.T) {
	list := proto.LauncherModels{Agent: "codex", Models: []proto.AgentModel{
		{ID: "configured"}, {ID: "mid-400k"}, {ID: "unknown"}, {ID: "same-1m"},
	}}
	fit := assistantFit{need: assistantWindowNeed(100000), prefer: 1000000,
		windows: map[string]int{"configured": 1000000, "mid-400k": 400000, "same-1m": 1000000}}
	if got, ok := nextAssistantModel(list, "configured", map[string]bool{"configured": true}, fit); !ok || got != "same-1m" {
		t.Fatalf("selected %q %v, want same-1m", got, ok)
	}
	// None as large: the list's order among those that hold it, a model whose window is not known included.
	fit.prefer = 2000000
	if got, ok := nextAssistantModel(list, "configured", map[string]bool{"configured": true}, fit); !ok || got != "mid-400k" {
		t.Fatalf("selected %q %v, want mid-400k", got, ok)
	}
	// Nothing known of the conversation: no window is too small.
	fit.need, fit.prefer = 0, 0
	fit.windows["mid-400k"] = 1000
	if got, ok := nextAssistantModel(list, "configured", map[string]bool{"configured": true}, fit); !ok || got != "mid-400k" {
		t.Fatalf("selected %q %v, want mid-400k", got, ok)
	}
}

func TestAssistantFallbackFitReadsTheConversationAndTheCatalog(t *testing.T) {
	list, windows := recordedFallbackList()
	wins, ctxOf := assistantRecoveryWindows, assistantRecoveryContext
	t.Cleanup(func() { assistantRecoveryWindows, assistantRecoveryContext = wins, ctxOf })
	assistantRecoveryWindows = func(context.Context, *Node, string, proto.LauncherModels) map[string]int { return windows }
	assistantRecoveryContext = func(*Node, string) (int, int) { return 193884, 1000000 }
	n := assistantNode(t)
	for _, c := range []struct {
		configured, current string
		prefer              int
	}{
		{"earlier-272k", "big-1m", 272000},   // the configured model's window, not the failed fallback's
		{"", "small-131k", 1000000},          // the launcher default it stands for
		{"custom-id", "big-1m", 1000000},     // a model the catalog does not know: the one that failed
		{"custom-id", "custom-too", 1000000}, // neither known: the window the transcript recorded
	} {
		fit := n.assistantFallbackFit(context.Background(), "id", "codex", list, c.configured, c.current)
		if fit.need != 242355 || fit.prefer != c.prefer {
			t.Errorf("%s/%s: need %d prefer %d, want 242355 and %d", c.configured, c.current, fit.need, fit.prefer, c.prefer)
		}
	}
}

// The recorded failure end to end: the chain goes to a model that holds the conversation.
func TestAssistantRecoveryChoosesAFallbackThatHoldsTheConversation(t *testing.T) {
	list, windows := recordedFallbackList()
	models, send, wins, ctxOf := assistantRecoveryModels, assistantRecoverySend, assistantRecoveryWindows, assistantRecoveryContext
	t.Cleanup(func() {
		assistantRecoveryModels, assistantRecoverySend, assistantRecoveryWindows, assistantRecoveryContext = models, send, wins, ctxOf
	})
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) { return list, nil }
	assistantRecoveryWindows = func(context.Context, *Node, string, proto.LauncherModels) map[string]int { return windows }
	assistantRecoveryContext = func(*Node, string) (int, int) { return 193884, 1000000 }
	selected := ""
	assistantRecoverySend = func(_ context.Context, _ *Node, _ *assistantProc, _, _ string, execution modelExecution) error {
		selected = execution.Model
		return nil
	}
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	p := &assistantProc{id: id, agent: "codex"}
	n.procs[id] = p
	r := &assistantTurnRecovery{retried: map[string]bool{"big-1m": true}, used: map[string]bool{"big-1m": true}, running: true,
		from: modelExecution{Model: "big-1m"}}
	n.recoveries = map[string]*assistantTurnRecovery{id: r}
	n.runAssistantRecovery(p, r, "codex", "", modelExecution{Model: "big-1m"}, "big-1m", false)
	if selected != "wide-2m" {
		t.Fatalf("selected %q, want wide-2m", selected)
	}
}

// `codex debug models` renders the catalog on one line; a banner before it is read past. Each window is the part
// Codex lets a conversation fill: 131,072 at 95% is the 124,518 a turn on it reports.
func TestCodexDebugModelWindows(t *testing.T) {
	out := []byte("welcome to the shell\n" +
		`{"models":[{"slug":"small","context_window":131072,"effective_context_window_percent":95},` +
		`{"slug":"big","context_window":1000000,"effective_context_window_percent":100},` +
		`{"slug":"plain","context_window":272000},{"slug":"none","context_window":null}]}` + "\n")
	got, err := codexDebugModelWindows(out)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"small": 124518, "big": 1000000, "plain": 272000}
	if len(got) != len(want) {
		t.Fatalf("windows %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("windows %v, want %v", got, want)
		}
	}
	if _, err := codexDebugModelWindows([]byte("error: unrecognized subcommand 'debug'\n")); err == nil {
		t.Fatal("a Codex without the subcommand gave windows")
	}
}

// The launcher's codex runs `debug models` as it runs app-server: a shell launcher through the wrapper that keeps a
// user's codex() function, a managed one through personad launch with the subcommand after its words.
func TestCodexDebugModelsScript(t *testing.T) {
	n, _ := testNode(t)
	if sh := n.codexDebugModelsScript("codex"); !strings.Contains(sh, `codex "$@" debug models "${__persona_args[@]}"`) ||
		!strings.HasSuffix(sh, "\ncodex") {
		t.Errorf("shell launcher: %s", sh)
	}
	n.setManaged([]proto.ManagedLauncher{{Name: "gw", Agent: "codex", BaseURL: "http://127.0.0.1:9/v1", APIKey: "k"}})
	if sh := n.codexDebugModelsScript("gw"); !strings.HasSuffix(sh, " launch gw debug models") {
		t.Errorf("managed launcher: %s", sh)
	}
}
