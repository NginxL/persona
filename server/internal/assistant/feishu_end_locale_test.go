package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise the held-run entry point and real sink, with only the Feishu transport replaced. An unrelated turn
// closes the existing card in the owner's language without sending that turn's text or a new answer.
func TestFeishuUnrelatedEndUpdatesLocalizedProgress(t *testing.T) {
	needsLarkLayer(t)
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			s, f, r, posts := heldRunWithoutCard(t)
			setAgentLanguage(f, string(lang))
			f.setSessions("server", proto.Session{Status: proto.StatusWorking, State: proto.StateWorking,
				MachineID: "server", SessionID: "c1",
				Background: []proto.BackgroundTask{{ID: "background-work", Kind: "shell"}}})
			s.chans.mu.Lock()
			r.Ext["card"] = "om_existing_progress"
			s.chans.mu.Unlock()
			s.SessionItems(context.Background(), SessionItems{MachineID: "server", SessionID: "c1", Items: []proto.Item{
				{Seq: 5, Role: "user", Kind: proto.KindText, Text: "unrelated private question"},
				{Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "unrelated private answer"},
			}})
			s.channelProgressTick(r.ID)
			if run, _ := s.channelRunByID(r.ID); run != nil {
				t.Fatal("the unrelated turn did not end the held run")
			}
			if len(*posts) != 1 {
				t.Fatalf("want one existing-card update, got %v", *posts)
			}
			post := (*posts)[0]
			if post["update"] != true || post["message_id"] != "om_existing_progress" {
				t.Fatalf("ending sent a new answer instead of updating the existing card: %v", post)
			}
			card, _ := post["card"].(string)
			var got botcard.Card
			if err := json.Unmarshal([]byte(card), &got); err != nil {
				t.Fatal(err)
			}
			want := i18n.T(lang, "assistant-feishu.end_unrelated")
			if got.Header == nil || got.Header.Title.Content != want || !strings.Contains(card, want+" · ") ||
				strings.Contains(card, "assistant-feishu.") || strings.Contains(card, "unrelated private") ||
				strings.Contains(card, i18n.T(lang, "assistant-feishu.state_working")) {
				t.Fatalf("incorrect localized terminal card: %s", card)
			}
		})
	}
}
