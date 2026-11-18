package assistant

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTranscriptTextByteLimit(t *testing.T) {
	const marker = "…(earlier part cut)\n"
	for _, size := range []int{39999, 40000, 40001} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			head := strings.Repeat("a", size-2)
			want := head + "\n\n"
			if size > 40000 {
				want = marker + strings.Repeat("a", 39998) + "\n\n"
			}
			if got := transcriptText(head, "", nil); got != want {
				t.Fatalf("transcript at %d bytes: got %d bytes, want %d", size, len(got), len(want))
			}
		})
	}
}

func TestTranscriptTextTruncatesAtRuneBoundary(t *testing.T) {
	const marker = "…(earlier part cut)\n"
	recentText := strings.Repeat("a", 3990)
	// Ten complete messages occupy 39990 bytes, leaving the cut in the older message.
	recent := strings.Repeat("Agent: "+recentText+"\n\n", 10)
	for _, char := range []string{"a", "é", "界", "あ", "😀"} {
		for keepBytes := 0; keepBytes <= len(char); keepBytes++ {
			t.Run(fmt.Sprintf("%s/keep_%d_bytes", char, keepBytes), func(t *testing.T) {
				padding := strings.Repeat("x", 8-keepBytes)
				items := []proto.Item{{Kind: proto.KindText, Role: "user", Text: "earlier" + char + padding}}
				for range 10 {
					items = append(items, proto.Item{Kind: proto.KindText, Role: "assistant", Text: recentText})
				}
				wantTail := padding + "\n\n" + recent
				if keepBytes == len(char) {
					wantTail = char + wantTail
				}
				got := transcriptText("head", "", items)
				if !utf8.ValidString(got) {
					t.Fatal("truncation produced invalid UTF-8")
				}
				if got != marker+wantTail {
					t.Fatalf("transcript did not preserve the expected complete suffix: got %d bytes, want %d", len(got), len(marker+wantTail))
				}
				if len(strings.TrimPrefix(got, marker)) > 40000 {
					t.Fatal("transcript exceeds the byte limit")
				}
			})
		}
	}
}

func TestTranscriptTextLabelsThinkingAsProgressSummary(t *testing.T) {
	got := transcriptText("head", "", []proto.Item{{Role: "assistant", Kind: proto.KindThinking, Text: "Summary, not verbatim"}})
	want := "head\n\n(agent, progress summary) Summary, not verbatim\n"
	if got != want {
		t.Fatalf("thinking label: got %q, want %q", got, want)
	}
}

// With the conversation's page known, each message carries its own link (?at=<item id>, #2194); without it the text
// is as before.
func TestTranscriptTextLinksEachMessage(t *testing.T) {
	items := []proto.Item{
		{ID: "u1#0", Role: "user", Kind: proto.KindText, Text: "修一下 CI"},
		{ID: "a2#1", Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash"},
		{ID: "a3#0", Role: "assistant", Kind: proto.KindText, Text: "修好了"},
	}
	got := transcriptText("head", "https://hub.example.com/s/mac-a/b005", items)
	for _, want := range []string{
		"User (https://hub.example.com/s/mac-a/b005?at=u1%230): 修一下 CI",
		"Agent (https://hub.example.com/s/mac-a/b005?at=a3%230): 修好了",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "a2%231") {
		t.Fatalf("a tool call got a link:\n%s", got)
	}
	if got := transcriptText("head", "", items); strings.Contains(got, "?at=") || !strings.Contains(got, "User: 修一下 CI") {
		t.Fatalf("links without a page:\n%s", got)
	}
}
