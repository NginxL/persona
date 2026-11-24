package channel

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestReadTurnHeldToolImageContinuation(t *testing.T) {
	src := &pagedSource{page: 1, items: numbered(
		proto.Item{Role: "user", Kind: proto.KindNotice, Wake: true, Text: "Background command finished"},
		proto.Item{Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "read-1", ToolName: "Read"},
		proto.Item{Role: "user", Kind: proto.KindImage, ToolUseID: "read-1"},
		agentSays("The screenshot confirms the result."),
	)}
	rd, err := ReadTurn(context.Background(), src, &Run{Held: true})
	if err != nil || rd.Unrelated || !rd.Found || rd.Answer != "The screenshot confirms the result." {
		t.Fatalf("tool image continuation = %+v, err = %v", rd, err)
	}
}

// A tool image does not hide a later owner turn, even across transcript pages.
func TestReadTurnHeldToolImageThenOwnerInput(t *testing.T) {
	for _, kind := range []string{proto.KindText, proto.KindImage} {
		t.Run(kind, func(t *testing.T) {
			src := &pagedSource{page: 1, items: numbered(
				proto.Item{Role: "user", Kind: proto.KindImage, ToolUseID: "read-1"},
				agentSays("The background result."),
				proto.Item{Role: "user", Kind: kind, Text: "An unrelated question"},
				agentSays("A private answer."),
			)}
			rd, err := ReadTurn(context.Background(), src, &Run{Held: true})
			if err != nil || !rd.Unrelated || rd.Answer != "" {
				t.Fatalf("owner input after tool image = %+v, err = %v", rd, err)
			}
		})
	}
}
