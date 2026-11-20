//go:build !windows

package node

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestGoldenAgentLanguagePeerDelivery(t *testing.T) {
	for _, tc := range []struct{ language, suffix string }{{"", "zh-Hans"}, {"English", "en"}, {"日本語", "ja"}} {
		t.Run(tc.suffix, func(t *testing.T) {
			n, f, home := herdrNode(t)
			noPaneEnv(t)
			shortPeerWait(t, 3*time.Second)
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			p := modeTranscript(t, home, home, "default")
			inbox := newFakeInbox(t, p)
			inbox.setMode(peerModePrompting)
			writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, nil)
			const source = "Source 原文\n`file.id`"
			_, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": source, "prompt_language": tc.language})
			if err != nil {
				t.Fatal(err)
			}
			_, content := inboxPriority(t, inbox, 0)
			_, body, ok := transcript.CrossSession(content)
			if !ok {
				t.Fatal(content)
			}
			original, ok := prompts.PeerMessageBody(strings.TrimSpace(body))
			if !ok || original != source {
				t.Fatalf("lost source: %q %v", original, ok)
			}
			goldenNode(t, "agent_language.peer."+tc.suffix, strings.TrimSpace(body))
			if got := envelopeHead(t, inbox, 0); got != `<cross-session-message from-name="Persona">` {
				t.Fatalf("language changed permission envelope: %s", got)
			}
		})
	}
}

func TestGoldenAgentLanguageOutsideAnswer(t *testing.T) {
	for _, tc := range []struct{ language, suffix string }{{"", "zh-Hans"}, {"English", "en"}, {"日本語", "ja"}} {
		t.Run(tc.suffix, func(t *testing.T) {
			n, f, home := herdrNode(t)
			noPaneEnv(t)
			shortPeerWait(t, 3*time.Second)
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			p := modeTranscript(t, home, home, "bypassPermissions")
			appendLines(t, p, askCall(sidA, "toolu_language", multiAsk))
			inbox := newFakeInbox(t, p)
			inbox.setMode(peerModeBypass)
			writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
			cards := n.outsideAskCards()
			if len(cards) != 1 {
				t.Fatalf("cards: %+v", cards)
			}
			params := map[string]any{"session_id": sidA, "id": cards[0].ID, "prompt_language": tc.language, "resolution": proto.Resolution{Answers: map[string]string{"Which setting wins?": "Account"}}}
			// The language alone cannot make an untrusted answer bypass the peer gate.
			if _, err := request(t, n, "codex.answer", params); refusalCode(err) != peerHeld {
				t.Fatalf("language granted authority: %v", err)
			}
			if _, err := request(t, n, nodeproto.OwnerCardAnswer, params); err != nil {
				t.Fatal(err)
			}
			_, content := inboxPriority(t, inbox, 1)
			_, body, ok := transcript.CrossSession(content)
			if !ok {
				t.Fatal(content)
			}
			answer, ok := prompts.PeerMessageBody(strings.TrimSpace(body))
			if !ok || !strings.Contains(answer, "Which setting wins? → Account") {
				t.Fatalf("answer: %q", answer)
			}
			goldenNode(t, "agent_language.outside_answer."+tc.suffix, answer)
			if h := envelopeHead(t, inbox, 1); h != `<cross-session-message from-name="Persona" from-mode="bypass">` {
				t.Fatalf("owner authority changed: %s", h)
			}
		})
	}
}
