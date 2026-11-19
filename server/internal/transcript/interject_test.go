package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The model sees the note in its input, but it must never become a separate
// user item or part of the caption, including when Claude absorbs input mid-turn.
func TestInterjectIsNotUserContext(t *testing.T) {
	note := prompts.RenderInterject(prompts.Interject{})
	for _, queued := range []bool{false, true} {
		for _, photo := range []bool{false, true} {
			for _, coalesced := range []bool{false, true} {
				t.Run(fmt.Sprintf("queued=%v/image=%v/coalesced=%v", queued, photo, coalesced), func(t *testing.T) {
					blocks := []map[string]any{{"type": "text", "text": "<persona-context>snapshot</persona-context>"}, {"type": "text", "text": note}}
					if photo {
						blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}})
					}
					blocks = append(blocks, map[string]any{"type": "text", "text": "Sketch"})
					if coalesced {
						var text []string
						var pictures []map[string]any
						for _, block := range blocks {
							if block["type"] == "text" {
								text = append(text, block["text"].(string))
							} else {
								pictures = append(pictures, block)
							}
						}
						blocks = append([]map[string]any{{"type": "text", "text": strings.Join(text, "\n\n")}}, pictures...)
					}
					entry := map[string]any{"type": "user", "uuid": "interjection", "message": map[string]any{"role": "user", "content": blocks}}
					if queued {
						entry = map[string]any{"type": "attachment", "uuid": "interjection", "attachment": map[string]any{"type": "queued_command", "commandMode": "prompt", "origin": map[string]string{"kind": "human"}, "prompt": blocks}}
					}
					raw, _ := json.Marshal(entry)
					var meta Meta
					items := ParseLine(1, raw, &meta)
					texts, images := 0, 0
					ids := map[string]bool{}
					for _, it := range items {
						if ids[it.ID] {
							t.Fatalf("duplicate item ID %s", it.ID)
						}
						ids[it.ID] = true
						if it.Queued != queued {
							t.Fatalf("queued = %v, want %v", it.Queued, queued)
						}
						switch it.Kind {
						case proto.KindText:
							texts++
							if it.Text != "Sketch" {
								t.Errorf("model context leaked into user text: %q", it.Text)
							}
						case proto.KindImage:
							images++
						}
					}
					wantImages := 0
					if photo {
						wantImages = 1
					}
					if texts != 1 || images != wantImages {
						t.Fatalf("texts=%d images=%d, want one caption and photo=%v", texts, images, photo)
					}
				})
			}
		}
	}
	if got := codexUserText(note + "\nSketch"); got != "Sketch" {
		t.Errorf("Codex user text: %q", got)
	}
	// Normal owner-picked context, even with a similar title, stays inspectable.
	for _, keep := range []string{
		`<persona-context kind="context" title="Notes">source</persona-context>`,
		`<persona-context kind="context" title="插话记录">source</persona-context>`,
		`<persona-context kind="forward" title="插话">source</persona-context>`,
	} {
		var meta Meta
		if got := userText(func(int, string, string) proto.Item { return proto.Item{} }, keep, &meta); len(got) != 1 || !strings.Contains(got[0].Text, "source") {
			t.Errorf("user-picked context removed: %q", keep)
		}
	}
}

func TestStripInterjectAttributes(t *testing.T) {
	for _, head := range []string{
		`kind="context" title="插话" url=""`,
		`url="" title="插话" kind="context"`,
		`title="插话"`,
		`kind="context" title="&#25554;&#35805;"`,
	} {
		input := "before<persona-context " + head + ">model-only note</persona-context>after"
		if got := StripInterject(input); got != "beforeafter" {
			t.Errorf("attributes %s: %q", head, got)
		}
	}
}
