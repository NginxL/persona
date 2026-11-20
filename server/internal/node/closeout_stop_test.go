package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func closeoutTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const (
	coUserAsk    = `{"type":"user","message":{"role":"user","content":"fix the retry"}}`
	coUserBlocks = `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"and now explain it"}]}}`
	coToolResult = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	coEditCall   = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"editing"},{"type":"tool_use","id":"t1","name":"Edit","input":{}}]}}`
	coReadCall   = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]}}`
	coSideEdit   = `{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"tool_use","id":"t9","name":"Edit","input":{}}]}}`
	coAnswer     = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`
)

func TestTurnWorked(t *testing.T) {
	for name, tc := range map[string]struct {
		lines []string
		want  bool
	}{
		"edited":                   {[]string{coUserAsk, coEditCall, coToolResult, coAnswer}, true},
		"only read":                {[]string{coUserAsk, coReadCall, coToolResult, coAnswer}, false},
		"plain answer":             {[]string{coUserAsk, coAnswer}, false},
		"edit was the turn before": {[]string{coUserAsk, coEditCall, coToolResult, coAnswer, coUserBlocks, coReadCall, coToolResult, coAnswer}, false},
		"a subagent's edit":        {[]string{coUserAsk, coSideEdit, coAnswer}, false},
		"cut first line":           {[]string{`"content":"half a line"}}`, coUserAsk, coEditCall, coToolResult, coAnswer}, true},
	} {
		if got := turnWorked(closeoutTranscript(t, tc.lines...)); got != tc.want {
			t.Errorf("%s: turnWorked = %v, want %v", name, got, tc.want)
		}
	}
	if turnWorked("") || turnWorked(filepath.Join(t.TempDir(), "missing.jsonl")) {
		t.Error("an unreadable transcript counts as a turn that worked")
	}
}

func TestStopRecognizesOnlyTheCompleteTerminalReport(t *testing.T) {
	n, asked := closeoutHookNode(t, func(method string, raw json.RawMessage) (any, error) {
		var request assistantwire.CloseoutCheckRequest
		if method != assistantwire.MethodCloseoutCheck || json.Unmarshal(raw, &request) != nil || request.SessionID != "s" {
			t.Errorf("unexpected request %s %s", method, raw)
		}
		return assistantwire.CloseoutCheckResponse{Reason: "Please report the work already done."}, nil
	})
	p := closeoutTranscript(t, coUserAsk, coEditCall, coToolResult, coAnswer)
	report := "### Persona work report\n\n- Status: done\n- Merged: n/a\n- Deployed: n/a\n\n---"
	for _, tc := range []struct {
		text string
		hold bool
	}{{report, false}, {strings.Replace(report, "Status: done", "Status: blocked", 1), false},
		{"<closeout status=\"done\">verified: four entries</closeout>", false},
		{strings.TrimSuffix(report, "---"), true}, {"    " + report, true}, {"> " + strings.ReplaceAll(report, "\n", "\n> "), true}} {
		in := hookInput{HookEventName: "Stop", SessionID: "s", TranscriptPath: p, LastAssistantMessage: tc.text}
		before := asked.Load()
		got := n.closeoutStop(t.Context(), sockRequest{}, in)
		if (got != nil) != tc.hold || (asked.Load() > before) != tc.hold {
			t.Errorf("held = %v for %q", got, tc.text)
		}
	}
}

func TestCloseoutStopLetsGo(t *testing.T) {
	p := closeoutTranscript(t, coUserAsk, coEditCall, coToolResult, coAnswer)
	n := &Node{}
	for name, tc := range map[string]struct {
		req sockRequest
		in  hookInput
	}{
		"no hub":       {sockRequest{}, hookInput{HookEventName: "Stop", SessionID: "s", TranscriptPath: p, LastAssistantMessage: "done"}},
		"report only":  {sockRequest{ReportOnly: true}, hookInput{HookEventName: "Stop", SessionID: "s", TranscriptPath: p, LastAssistantMessage: "done"}},
		"second stop":  {sockRequest{}, hookInput{HookEventName: "Stop", SessionID: "s", TranscriptPath: p, LastAssistantMessage: "done", StopHookActive: true}},
		"a subagent":   {sockRequest{}, hookInput{HookEventName: "Stop", SessionID: "s", TranscriptPath: p, LastAssistantMessage: "done", AgentID: "a1"}},
		"not a stop":   {sockRequest{}, hookInput{HookEventName: "Notification", SessionID: "s", TranscriptPath: p, LastAssistantMessage: "done"}},
		"empty answer": {sockRequest{}, hookInput{HookEventName: "Stop", SessionID: "s", TranscriptPath: p}},
	} {
		if out := n.closeoutStop(t.Context(), tc.req, tc.in); out != nil {
			t.Errorf("%s: the stop was held: %v", name, out)
		}
	}
}
