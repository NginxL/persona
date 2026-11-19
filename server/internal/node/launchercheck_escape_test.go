package node

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/textfmt"
)

// checkOutput feeds the launcher health record and the Test result: what the shell printed through its prompt hooks must
// not reach either, and an address the escapes wrapped must still be redacted.
func TestCheckOutputStripsTerminalEscapesAndNoise(t *testing.T) {
	cases := map[string]string{
		"startup noise and error": "Restart Zsh to retry gitstatus initialization: \x1b[32mexec zsh\x1b[39m\n" +
			"\x1b]1337;RemoteHost=dev@workstation\x07\x1b]1337;CurrentDir=/Users/dev\x07\n" +
			"Error: No Cursor IDE installation found.",
		"flattened noise": "Restart Zsh to retry gitstatus initialization: \x1b[32mexec zsh\x1b[39m " +
			"\x1b]1337;RemoteHost=dev@workstation\x07\x1b]1337;CurrentDir=/Users/dev\x07... " +
			"Error: No Cursor IDE installation found.",
	}
	for name, in := range cases {
		if got, want := checkOutput(in), "Error: No Cursor IDE installation found."; got != want {
			t.Errorf("%s: checkOutput = %q, want %q", name, got, want)
		}
	}
}

func TestCheckOutputRedactsInsideEscapes(t *testing.T) {
	got := checkOutput("Error: failed to reach \x1b[32mhttps://gateway.test:8080/v1\x1b[0m from \x1b[33m203.0.113.7:443\x1b[0m")
	if strings.Contains(got, "gateway.test") || strings.Contains(got, "203.0.113.7") {
		t.Fatalf("address left in %q", got)
	}
	if !strings.Contains(got, "<url>") || !strings.Contains(got, "<ip>") {
		t.Fatalf("address not redacted in %q", got)
	}
}

// A check's JSON result line can carry shell-integration escapes around it (claudeResult reads the last JSON object).
func TestClaudeResultIgnoresEscapes(t *testing.T) {
	out := "some banner\n\x1b]1337;RemoteHost=dev@workstation\x07" +
		"{\"result\":\"hello\",\"is_error\":false,\"duration_ms\":12}\x1b[0m\n"
	r, ok := claudeResult(out)
	if !ok || r.result != "hello" || r.isError || r.millis != 12 {
		t.Fatalf("claudeResult = %+v, ok=%v", r, ok)
	}
}

// The error the record keeps is one clean line, never the terminal's bytes.
func TestCheckOutputNeverKeepsEscapes(t *testing.T) {
	raw := "\x1b[1;31mfatal:\x1b[0m upstream refused\n" +
		"\x1b]1337;CurrentDir=/Users/dev\x07"
	got := checkOutput(raw)
	if strings.ContainsRune(got, 0x1b) || strings.Contains(got, "\x07") {
		t.Fatalf("escapes left in %q", got)
	}
	if got != "fatal: upstream refused" {
		t.Fatalf("got %q", got)
	}
	if again := textfmt.StripTerminalEscapes(got); again != got {
		t.Fatalf("the output is not clean text: %q", again)
	}
}
