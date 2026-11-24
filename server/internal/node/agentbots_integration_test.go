package node

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/rpc"
)

type integrationBotKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (k *integrationBotKV) Setting(_ context.Context, key string, out any) (bool, error) {
	k.mu.Lock()
	raw, ok := k.m[key]
	k.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (k *integrationBotKV) PutSetting(_ context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	k.mu.Lock()
	k.m[key] = raw
	k.mu.Unlock()
	return nil
}

type integrationBotConversation struct {
	*agentBotFake
	items       []orcart.JournalItem
	failUnknown bool
}

func newIntegrationBotConversation(t *testing.T) *integrationBotConversation {
	t.Helper()
	b := &integrationBotConversation{agentBotFake: newAgentBotFake(t)}
	b.f.Handle("agentSession.history", func(p map[string]any) any {
		b.mu.Lock()
		defer b.mu.Unlock()
		session, _ := p["sessionId"].(string)
		if session != botSessionB {
			return orcarttest.Fail{Code: "agent_session_identity_required"}
		}
		items := make([]any, len(b.items))
		for i, item := range b.items {
			items[i] = item
		}
		return map[string]any{"ok": true, "page": map[string]any{
			"sessionId": session, "epoch": "epoch-1", "fence": 7, "working": false, "direction": p["direction"],
			"items": items, "hasOlder": false, "hasNewer": false, "window": map[string]any{},
		}}
	})
	b.f.Handle("agentSession.send", func(p map[string]any) any {
		b.mu.Lock()
		defer b.mu.Unlock()
		envelope, _ := p["envelope"].(map[string]any)
		op, _ := envelope["clientOperationId"].(string)
		fingerprint, _ := envelope["payloadFingerprint"].(string)
		if prior, ok := b.accepted[op]; ok {
			if prior != fingerprint {
				return map[string]any{"ok": false, "refusal": map[string]any{"code": "agent_session_payload_mismatch"}}
			}
			return map[string]any{"ok": true, "replayed": true, "value": map[string]any{}}
		}
		b.accepted[op] = fingerprint
		b.writes++
		body, _ := p["body"].(map[string]any)
		blocks, _ := body["blocks"].([]any)
		text := ""
		if len(blocks) > 0 {
			block, _ := blocks[0].(map[string]any)
			text, _ = block["text"].(string)
		}
		b.items = append(b.items, orcart.JournalItem{ItemID: fmt.Sprintf("user-%d", len(b.items)+1), Revision: 1,
			Sequence: int64(len(b.items) + 1), ObservedAt: int64(1000 * (len(b.items) + 1)),
			Body: orcart.JournalBody{Kind: "message", Role: "user", Blocks: []orcart.JournalBlock{{Type: "text", Text: text}}}})
		if b.failUnknown {
			b.failUnknown = false
			return orcarttest.Fail{Code: "agent_session_operation_unknown"}
		}
		return map[string]any{"ok": true, "replayed": false, "value": map[string]any{}}
	})
	return b
}

func (b *integrationBotConversation) reply(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items = append(b.items, orcart.JournalItem{ItemID: fmt.Sprintf("assistant-%d", len(b.items)+1), Revision: 1,
		Sequence: int64(len(b.items) + 1), ObservedAt: int64(1000 * (len(b.items) + 1)),
		Body: orcart.JournalBody{Kind: "message", Role: "assistant", Blocks: []orcart.JournalBlock{{Type: "text", Text: text}}}})
}

func (b *integrationBotConversation) service(kv agents.KV, changes *[][]string) *agents.Service {
	caller := agents.BotCallFunc(func(ctx context.Context, machine, method string, params, out any) error {
		if machine != "machine-b" {
			return &rpc.Error{Code: "not_found", Message: "wrong machine"}
		}
		raw, _ := json.Marshal(params)
		res, err := b.n.handleMethod(ctx, method, raw)
		if err != nil {
			return err
		}
		if out == nil {
			return nil
		}
		body, _ := json.Marshal(res)
		return json.Unmarshal(body, out)
	})
	src, err := agents.NewBotRegistry(kv, caller).Source(agents.BotRegistration{
		ID: "research", Provider: "orca", Title: "Research bot", MachineID: "machine-b", SessionID: botSessionB,
	})
	if err != nil {
		panic(err)
	}
	var changeMu sync.Mutex
	return agents.NewService(kv, func(ids []string) {
		changeMu.Lock()
		defer changeMu.Unlock()
		*changes = append(*changes, append([]string(nil), ids...))
	}, nil, src)
}

func TestAgentBotIntegrationAssignmentReplyFollowupAndRestart(t *testing.T) {
	oldFollowUps := agents.FollowUps
	agents.FollowUps = nil
	t.Cleanup(func() { agents.FollowUps = oldFollowUps })
	b := newIntegrationBotConversation(t)
	kv := &integrationBotKV{m: map[string][]byte{}}
	var changes [][]string
	svc := b.service(kv, &changes)
	ctx := context.Background()
	svc.PollOnce(ctx) // establish the directory baseline
	changes = nil

	firstID := agents.NewClientID()
	first, err := svc.Send(ctx, "bot_research:endpoint", firstID, "First assignment", "test")
	if err != nil || first.Entry.Status != agents.OutSent {
		t.Fatalf("first assignment %+v %v", first, err)
	}
	if strings.Contains(string(first.Upstream), "assistant") {
		t.Fatalf("send receipt synthesized a reply: %s", first.Upstream)
	}
	page, err := svc.Messages(ctx, "bot_research:endpoint", "", 40)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Role != "user" {
		t.Fatalf("accepted send is not completion: %+v %v", page, err)
	}
	b.reply("First bot reply")
	changes = nil
	svc.PollOnce(ctx)
	if len(changes) != 1 || len(changes[0]) != 1 || changes[0][0] != "bot_research:endpoint" {
		t.Fatalf("reply invalidation %v", changes)
	}
	page, err = svc.Messages(ctx, "bot_research:endpoint", "", 40)
	if err != nil || len(page.Messages) != 2 || page.Messages[1].Blocks[0].Text != "First bot reply" {
		t.Fatalf("first reply %+v %v", page, err)
	}

	secondID := agents.NewClientID()
	if _, err := svc.Send(ctx, "bot_research:endpoint", secondID, "Follow-up assignment", "test"); err != nil {
		t.Fatal(err)
	}
	page, _ = svc.Messages(ctx, "bot_research:endpoint", "", 40)
	if len(page.Messages) != 3 || page.Messages[2].Role != "user" {
		t.Fatalf("follow-up receipt became a reply: %+v", page.Messages)
	}
	b.reply("Follow-up bot reply")
	svc.PollOnce(ctx)
	page, err = svc.Messages(ctx, "bot_research:endpoint", "", 40)
	if err != nil || len(page.Messages) != 4 || page.Messages[3].Blocks[0].Text != "Follow-up bot reply" {
		t.Fatalf("follow-up reply %+v %v", page, err)
	}
	seen := map[string]bool{}
	for _, message := range page.Messages {
		if seen[message.ExternalID] {
			t.Fatalf("duplicate external ID %s", message.ExternalID)
		}
		seen[message.ExternalID] = true
	}

	// Force an unknown outcome after Orca accepted the third operation, then restart manager/source and retry explicitly.
	b.mu.Lock()
	b.failUnknown = true
	b.mu.Unlock()
	thirdID := agents.NewClientID()
	if _, err := svc.Send(ctx, "bot_research:endpoint", thirdID, "Retry after restart", "test"); err == nil {
		t.Fatal("unknown outcome was hidden")
	}
	writes := b.writes
	restarted := b.service(kv, &changes)
	if _, err := restarted.Send(ctx, "bot_research:endpoint", thirdID, "Retry after restart", "test"); err != nil {
		t.Fatal(err)
	}
	if b.writes != writes {
		t.Fatalf("restart retry wrote twice: before=%d after=%d", writes, b.writes)
	}
	if _, err := restarted.Send(ctx, "bot_research:endpoint", thirdID, "Changed after restart", "test"); agents.HTTPStatus(err) != 409 {
		t.Fatalf("same ID / changed payload: %v", err)
	}
	if b.writes != writes {
		t.Fatal("payload conflict reached another write")
	}
	if _, err := restarted.Send(ctx, "bot_research:missing", agents.NewClientID(), "wrong session", "test"); agents.HTTPStatus(err) != 404 {
		t.Fatalf("wrong public session: %v", err)
	}
	if b.writes != writes {
		t.Fatal("wrong public session reached a write")
	}

	// Concurrent retries of one operation may cross in the outbox, but Orca accepts only one write.
	b.mu.Lock()
	b.failUnknown = true
	b.mu.Unlock()
	concurrentID := agents.NewClientID()
	writes = b.writes
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := restarted.Send(ctx, "bot_research:endpoint", concurrentID, "Concurrent same-ID retry", "test")
			if err != nil && !strings.Contains(err.Error(), "same clientId") {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent retry: %v", err)
	}
	if b.writes != writes+1 {
		t.Fatalf("concurrent retries accepted %d writes, want one", b.writes-writes)
	}
	if got := b.f.Invalid(); len(got) != 0 {
		t.Fatalf("schema rejected integration calls: %v", got)
	}
}
