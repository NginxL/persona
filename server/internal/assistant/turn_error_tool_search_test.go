package assistant

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

// toolSearchRefusal is a gateway edge's words for Claude Code's tool search, as Claude Code writes the failed turn
// (#10365).
const toolSearchRefusal = "API Error: 400 request is not representable on this edge: ToolSearch is not supported on this endpoint (defer_loading:true, tool_search_*, tool_reference); resend with all tools loaded"

// A turn a gateway refused for tool search that the node's one retry did not get past has its own code: not a model
// route (the model is fine) and not the user's to fix (the node starts that launcher and model with every tool from
// now on, so a retry works); not a launcher health class either.
func TestToolSearchRefusalIsItsOwnTurnError(t *testing.T) {
	for text, want := range map[string]string{
		toolSearchRefusal: proto.TurnErrorToolSearchUnsupported,
		"API Error: 400 tools.3.defer_loading: Extra inputs are not permitted": proto.TurnErrorToolSearchUnsupported,
		// A gateway that puts "model" and "not supported" in the same words: still tool search.
		"API Error: 400 model x: tool_reference blocks are not supported": proto.TurnErrorToolSearchUnsupported,
		"API Error: 400 invalid request":                                  "",
	} {
		if got := turnErrorCode(text); got != want {
			t.Errorf("turnErrorCode(%q) = %q, want %q", text, got, want)
		}
	}
	if userTurnError(proto.TurnErrorToolSearchUnsupported) {
		t.Error("a tool-search refusal is not the user's to fix")
	}
	if got := classifyLauncherError(toolSearchRefusal); got == errKindModelRoute || got == errKindAuth {
		t.Errorf("launcher health class %q", got)
	}

	ft := newFleetTestNoStore(t)
	ft.d.setSessions("mac", []proto.Session{stopped("s1", toolSearchRefusal)})
	sess := listed(t, ft.s, "mac", "s1")
	if sess.TurnError != toolSearchRefusal || sess.TurnErrorCode != proto.TurnErrorToolSearchUnsupported || sess.TurnErrorKind == proto.TurnErrorUser {
		t.Fatalf("listed: %q %q %q", sess.TurnError, sess.TurnErrorKind, sess.TurnErrorCode)
	}
}

// Readiness words it in every language, offers trying again rather than setting up a model, and does not hold the
// main conversation in setup. Only a node that announces assistant_tool_search_fallback sends every tool there from
// now on, so only for one the words say so; an older node's (or one the manager cannot ask) are plain retry advice.
func TestToolSearchRefusalReadinessStep(t *testing.T) {
	d := Device{ID: "mac", Name: "mac"}
	for _, tc := range []struct {
		name     string
		s        *Service
		fallback bool
	}{
		{"a node that falls back", &Service{devs: toolSearchDevs{announced: true}}, true},
		{"an older node", &Service{devs: toolSearchDevs{}}, false},
		{"no node to ask", &Service{}, false},
	} {
		details := map[string]bool{}
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			st := tc.s.toolSearchWords(lang, d, tc.s.readinessStep(lang, StepModel, StepProblem, proto.TurnErrorToolSearchUnsupported, "mac", "gw", "test_assistant"))
			if st.Title == "" || strings.Contains(st.Title, "tool_search_unsupported") || st.Detail == "" || strings.Contains(st.Detail, "tool_search_unsupported") {
				t.Errorf("%s, %s: %q / %q", tc.name, lang, st.Title, st.Detail)
			}
			details[st.Detail] = true
			if lang == i18n.En && strings.Contains(st.Detail, "every tool") != tc.fallback {
				t.Errorf("%s: %q", tc.name, st.Detail)
			}
		}
		if len(details) != 3 {
			t.Errorf("%s: not worded in each language: %v", tc.name, details)
		}
	}
	other := (&Service{devs: toolSearchDevs{announced: true}}).toolSearchWords(i18n.En, d,
		(&Service{}).readinessStep(i18n.En, StepModel, StepProblem, proto.TurnErrorModelRoute, "mac", "gw", "connect_model"))
	if strings.Contains(other.Detail, "every tool") {
		t.Errorf("another code took the tool-search words: %q", other.Detail)
	}
	if readinessBlocks(ReadinessStep{State: StepProblem, Code: proto.TurnErrorToolSearchUnsupported}) {
		t.Error("a tool-search refusal holds the main conversation in setup")
	}
}

// toolSearchDevs are the manager's computers, whose nodes announced assistant_tool_search_fallback when announced.
type toolSearchDevs struct {
	Devices
	announced bool
}

func (d toolSearchDevs) NodeAnnounced(_, capability string) bool {
	return d.announced && capability == nodeproto.AssistantToolSearchFallback
}
