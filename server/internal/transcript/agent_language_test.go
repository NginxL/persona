package transcript

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
)

func TestAgentLanguagePeerProvenanceAndStop(t *testing.T) {
	for _, language := range []string{"", "English", "日本語"} {
		for _, source := range []string{"Original 原文\n`file.id`", prompts.PeerStop()} {
			body := prompts.RenderPeerMessage(prompts.PeerMessage{Language: language, Text: source})
			for _, sender := range []string{PersonaPeer, "Other session"} {
				line, _ := json.Marshal(map[string]any{"type": "user", "isMeta": true, "uuid": "00000000-0000-4000-8000-000000000004", "timestamp": "2026-11-12T16:41:28Z", "message": map[string]any{"role": "user", "content": "x"}, "origin": map[string]any{"kind": "peer", "name": sender, "body": body}, "sessionId": "s"})
				meta := &Meta{}
				items := ParseLine(1, line, meta)
				want := body
				if sender == PersonaPeer {
					want = source
					if source == prompts.PeerStop() {
						want = StoppedFromPhone
					}
				}
				if len(items) != 1 || items[0].Text != want || items[0].From != sender || !meta.LastPromptAt.IsZero() {
					t.Fatalf("%s %s: %+v meta %+v", language, sender, items, meta)
				}
			}
		}
	}
}
