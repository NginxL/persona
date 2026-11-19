package transcript

import (
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestBackgroundPlaceholderFoldsOnPageAndIncrementalWatch(t *testing.T) {
	for _, tc := range []struct {
		name, source, answer, wantKind string
	}{
		{"supervision placeholder", proto.SourceSupervision, "No response requested.", proto.KindThinking},
		{"scheduled placeholder", proto.SourcePatrol, " [no content]。 ", proto.KindThinking},
		{"relay placeholder", proto.SourceConversation, "no content", proto.KindThinking},
		{"substantive supervision reply", proto.SourceSupervision, "The backup passed verification.", proto.KindText},
		{"phrase in a substantive reply", proto.SourceSupervision, "No response requested. That is what the log said.", proto.KindText},
		{"direct input", "", "No response requested.", proto.KindText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ask := "Check the result."
			if tc.source != "" {
				ask = prompts.MessageSourceText(ask, &proto.MessageSource{Kind: tc.source, ConversationID: "peer"})
			}
			lines := &tline{}
			lines.user("u1", "", "An earlier question.", 1)
			lines.reply("a1", "u1", "An earlier answer.", 2)
			lines.user("u2", "a1", ask, 3)
			lines.toolUse("t1", "u2", "call", 4)
			lines.toolResult("r1", "t1", "call", "OK", 5)
			lines.reply("a2", "r1", tc.answer, 6)
			f := NewFile(writeLines(t, lines))
			items, err := f.Refresh()
			if err != nil {
				t.Fatal(err)
			}
			tail := items[len(items)-1:]
			got := f.FoldBackgroundPlaceholders(tail)[0] // the watch sends only the new reply
			if got.Kind != tc.wantKind || got.ID != tail[0].ID || tail[0].Kind != proto.KindText {
				t.Fatalf("projected kind %q, want %q with stable ID and original item", got.Kind, tc.wantKind)
			}
			page := f.FoldBackgroundPlaceholders(items)
			if page[len(page)-1].Kind != tc.wantKind || BackgroundReplyMissing(page) != (tc.wantKind == proto.KindThinking) {
				t.Fatalf("page and completion disagree with the incremental projection")
			}
		})
	}
}

func TestBackgroundPlaceholderStopsAtTheNextUserInput(t *testing.T) {
	source := &proto.MessageSource{Kind: proto.SourceSupervision}
	lines := &tline{}
	lines.user("u1", "", prompts.MessageSourceText("Check the result.", source), 1)
	lines.reply("a1", "u1", "No response requested.", 2)
	lines.user("u2", "a1", "Quote the exact result.", 3)
	lines.reply("a2", "u2", "No response requested.", 4)
	f := NewFile(writeLines(t, lines))
	items, err := f.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	page := f.FoldBackgroundPlaceholders(items)
	if page[1].Kind != proto.KindThinking || page[3].Kind != proto.KindText || BackgroundReplyMissing(page) {
		t.Fatal("the background source reached past a new direct input")
	}
}
