package assistantwire

import "testing"

func TestSealedCompactionIsAFailedTurnNotAnAnswer(t *testing.T) {
	text := "This conversation was compacted natively by <account>: the compaction is sealed (encrypted) to that account"
	if !CutOff(text) || !ErrorOnlyText(text) {
		t.Fatal("raw account-sealed compaction was treated as an answer")
	}
	if CutOff("I investigated the error: " + text) {
		t.Fatal("a reply discussing a sealed compaction was treated as an error")
	}
}

// A failed turn's words that say an endpoint refused Claude Code's tool search (#10365), and nothing else: the node
// resends such a turn once with every tool loaded, and the hub gives what is left its own code.
func TestToolSearchRefusedText(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		// The gateway edge's words, as the transcript and the stream-json result carry them.
		{"API Error: 400 request is not representable on this edge: ToolSearch is not supported on this endpoint (defer_loading:true, tool_search_*, tool_reference); resend with all tools loaded", true},
		{"API Error: 400 tools.3.defer_loading: Extra inputs are not permitted", true},
		{`API Error: 400 {"type":"error","error":{"type":"invalid_request_error","message":"messages.2.content.0: unknown block type tool_reference"}}`, true},
		{"request is not representable on this edge: tool search is not supported", true},
		{"API Error: 400 invalid request", false},
		{"API Error: 400 request is not representable on this edge: Claude Code Artifacts are enabled", false},
		{"API Error: 401 tool_search key invalid", false},
		{"API Error: 429 Too Many Requests (defer_loading)", false},
		{"API Error: 503 no eligible upstream (tool_reference)", false},
		{"API Error: 4000 tokens of defer_loading", false},
		{"I turned defer_loading off and tool search works now.", false},
		{"", false},
	} {
		if got := ToolSearchRefused(tc.text); got != tc.want {
			t.Errorf("ToolSearchRefused(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}
