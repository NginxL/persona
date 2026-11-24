package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

type botCallSeen struct {
	machine string
	method  string
	params  map[string]any
}

type fakeBotCaller struct {
	mu       sync.Mutex
	pages    map[string]BotPage
	calls    []botCallSeen
	sendErr  error
	sendHook func(map[string]any) error
}

func (f *fakeBotCaller) CallBot(_ context.Context, machine, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	f.mu.Lock()
	f.calls = append(f.calls, botCallSeen{machine: machine, method: method, params: p})
	hook := f.sendHook
	f.mu.Unlock()
	if method == "agentbot.send" {
		if hook != nil {
			if err := hook(p); err != nil {
				return err
			}
			return botTestDecode(out, map[string]any{"accepted": true})
		}
		f.mu.Lock()
		err := f.sendErr
		f.mu.Unlock()
		if err != nil {
			return err
		}
		return botTestDecode(out, map[string]any{"accepted": true})
	}
	session, _ := p["session_id"].(string)
	f.mu.Lock()
	page, ok := f.pages[session]
	f.mu.Unlock()
	if !ok {
		return &rpc.Error{Code: "not_found", Message: "raw socket path and token must not escape"}
	}
	return botTestDecode(out, page)
}

func botTestDecode(out, page any) error {
	raw, _ := json.Marshal(page)
	return json.Unmarshal(raw, out)
}

func (f *fakeBotCaller) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

func (f *fakeBotCaller) setPage(session string, page BotPage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pages == nil {
		f.pages = map[string]BotPage{}
	}
	f.pages[session] = page
}

var testBotRegistration = BotRegistration{
	ID: "research", Provider: "orca", Title: "Research bot", MachineID: "machine-b", SessionID: "claude_exact_registered",
}

func testBotPage() BotPage {
	return BotPage{CanSend: true, RankTimeMs: 100, Messages: []Message{{
		ExternalID: "assistant-1", Role: "assistant", CreateTimeMs: 100, Position: 2,
		Blocks: []Block{{Type: "text", Text: "A real history reply"}},
	}}, NextCursor: "older-cursor", HasMore: true}
}

func newTestBot(t *testing.T, kv KV, caller BotCaller) *BotSource {
	t.Helper()
	src, err := NewBotRegistry(kv, caller).Source(testBotRegistration)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestBotDirectoryAndHistoryUseOnlyExactRegistration(t *testing.T) {
	caller := &fakeBotCaller{pages: map[string]BotPage{testBotRegistration.SessionID: testBotPage()}}
	src := newTestBot(t, &memKV{m: map[string][]byte{}}, caller)
	ctx := context.Background()
	if src.Name() != "bot_research" {
		t.Fatalf("source name %q", src.Name())
	}
	list, err := src.Sessions(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("directory %+v %v", list, err)
	}
	got := list[0]
	if got.ID != "bot_research:endpoint" || got.SessionKey != BotSessionKey || got.Provider != "bot" || got.Title != "Research bot" ||
		!got.CanSend || got.RankTimeMs != 100 || strings.Contains(got.ID, testBotRegistration.SessionID) {
		t.Fatalf("public directory row %+v", got)
	}
	page, err := src.Messages(ctx, BotSessionKey, "", 40)
	if err != nil {
		t.Fatal(err)
	}
	if page.Session == nil || page.Session.ID != got.ID || len(page.Messages) != 1 || page.Messages[0].SessionKey != BotSessionKey ||
		page.Messages[0].Role != "assistant" || page.Messages[0].Blocks[0].Text != "A real history reply" ||
		!page.HasMore || page.NextCursor != "older-cursor" {
		t.Fatalf("history page %+v", page)
	}
	for _, c := range caller.calls {
		if c.machine != "machine-b" || c.params["session_id"] != "claude_exact_registered" {
			t.Fatalf("call escaped exact binding: %+v", c)
		}
	}
	before := len(caller.calls)
	if _, err := src.Messages(ctx, "another-session", "", 40); HTTPStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown endpoint key: %v", err)
	}
	if _, err := src.Send(ctx, "another-session", NewClientID(), "must not send"); HTTPStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown send key: %v", err)
	}
	if len(caller.calls) != before {
		t.Fatalf("unknown key reached node: %+v", caller.calls[before:])
	}
	if hits, err := src.Search(ctx, "anything"); err != nil || len(hits) != 0 {
		t.Fatalf("unregistered search results %+v %v", hits, err)
	}
}

func TestBotSendUnknownRetryRestartAndPayloadConflict(t *testing.T) {
	FollowUps = nil
	kv := &memKV{m: map[string][]byte{}}
	const secret = "secret /tmp/orca.sock token raw-payload"
	var mu sync.Mutex
	var operations []string
	accepted := map[string]string{}
	writes := 0
	caller := &fakeBotCaller{pages: map[string]BotPage{testBotRegistration.SessionID: testBotPage()}}
	caller.sendHook = func(p map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		op, _ := p["operation_id"].(string)
		text, _ := p["text"].(string)
		operations = append(operations, op)
		if prior, ok := accepted[op]; ok {
			if prior != text {
				return &rpc.Error{Code: "conflict", Message: secret}
			}
			return botTestDecode(&struct{}{}, map[string]any{})
		}
		accepted[op] = text
		writes++
		if writes == 1 {
			return &rpc.Error{Code: "unknown", Message: secret}
		}
		return nil
	}
	ctx := context.Background()
	src := newTestBot(t, kv, caller)
	svc := NewService(kv, nil, nil, src)
	clientID := NewClientID()
	if _, err := svc.Send(ctx, "bot_research:endpoint", clientID, "first assignment", "test"); err == nil {
		t.Fatal("unknown outcome was hidden")
	} else if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "same clientId") {
		t.Fatalf("unsanitized or unhelpful unknown outcome %q", err)
	}
	if writes != 1 || len(operations) != 1 {
		t.Fatalf("unknown outcome auto-reposted: writes=%d calls=%d", writes, len(operations))
	}
	if rows := svc.Outbox(ctx, "bot_research:endpoint", 0); len(rows) != 1 || rows[0].Status != OutFailed {
		t.Fatalf("outbox %+v", rows)
	}

	// A new registry and service are a manager restart. The same client ID must carry the same operation ID.
	restarted := newTestBot(t, kv, caller)
	svc2 := NewService(kv, nil, nil, restarted)
	res, err := svc2.Send(ctx, "bot_research:endpoint", clientID, "first assignment", "test")
	if err != nil || res.Entry.Status != OutSent {
		t.Fatalf("restart retry %+v %v", res, err)
	}
	if writes != 1 || len(operations) != 2 || operations[0] != operations[1] {
		t.Fatalf("retry was not deduplicated: writes=%d operations=%v", writes, operations)
	}
	callsBefore := len(operations)
	if _, err := svc2.Send(ctx, "bot_research:endpoint", clientID, "different payload", "test"); HTTPStatus(err) != http.StatusConflict {
		t.Fatalf("same ID / different payload: %v", err)
	}
	if len(operations) != callsBefore || writes != 1 {
		t.Fatalf("payload conflict reached write path: calls=%d writes=%d", len(operations), writes)
	}
	res, err = svc2.Send(ctx, "bot_research:endpoint", clientID, "first assignment", "test")
	if err != nil || !res.Duplicate || len(operations) != callsBefore {
		t.Fatalf("successful retry was not answered from outbox: %+v %v calls=%d", res, err, len(operations))
	}
}

func TestBotSendConflictAfterOutboxEvictionPreservesOriginal(t *testing.T) {
	FollowUps = nil
	kv := &memKV{m: map[string][]byte{}}
	caller := &fakeBotCaller{pages: map[string]BotPage{testBotRegistration.SessionID: testBotPage()}}
	registry := NewBotRegistry(kv, caller)
	original, err := registry.Source(testBotRegistration)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(kv, nil, nil, original)
	ctx := context.Background()
	sessionID := "bot_research:endpoint"
	clientID := NewClientID()
	const originalText = "original assignment"
	sent, err := svc.Send(ctx, sessionID, clientID, originalText, "test")
	if err != nil || sent.Entry.Status != OutSent {
		t.Fatalf("original send %+v %v", sent, err)
	}

	// Fill the real bounded outbox with newer rows to evict only the audit row; the bot operation book must retain the ID.
	for i := 0; i < outboxMax; i++ {
		entry := OutboxEntry{ClientID: fmt.Sprintf("eviction-%03d", i), SessionID: sessionID, Status: OutSent,
			CreatedAt: sent.Entry.CreatedAt.Add(time.Duration(i+1) * time.Nanosecond)}
		if err := svc.out.put(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := svc.out.get(ctx, clientID); ok {
		t.Fatal("original outbox row was not evicted")
	}
	var book map[string]botOperation
	if ok, err := kv.Setting(ctx, botOperationsKey, &book); !ok || err != nil {
		t.Fatalf("operation book missing: %v %v", ok, err)
	}
	originalOperation, ok := book[clientID]
	if !ok || originalOperation.EndpointID != testBotRegistration.ID || originalOperation.MachineID != testBotRegistration.MachineID ||
		originalOperation.SessionID != testBotRegistration.SessionID || originalOperation.TextSHA256 != textSHA256(originalText) {
		t.Fatalf("original client-ID binding was not retained: %+v", originalOperation)
	}
	before := map[string]string{outboxKey: string(kv.m[outboxKey]), botOperationsKey: string(kv.m[botOperationsKey])}
	sendsBefore := caller.count("agentbot.send")
	assertRejectedWithoutMutation := func(label string, err error) {
		t.Helper()
		if HTTPStatus(err) != http.StatusConflict {
			t.Errorf("%s: got status %d, want 409 (%v)", label, HTTPStatus(err), err)
		}
		if got := caller.count("agentbot.send"); got != sendsBefore {
			t.Errorf("%s reached transport: sends=%d, want %d", label, got, sendsBefore)
		}
		for _, key := range []string{outboxKey, botOperationsKey} {
			if got := string(kv.m[key]); got != before[key] {
				t.Errorf("%s mutated %s", label, key)
			}
		}
	}

	_, err = svc.Send(ctx, sessionID, clientID, "changed assignment", "test")
	assertRejectedWithoutMutation("changed payload", err)

	reboundRegistration := testBotRegistration
	reboundRegistration.MachineID = "machine-rebound"
	reboundRegistration.SessionID = "claude_rebound"
	caller.setPage(reboundRegistration.SessionID, testBotPage())
	rebound, err := registry.Source(reboundRegistration)
	if err != nil {
		t.Fatal(err)
	}
	reboundSvc := NewService(kv, nil, nil, rebound)
	_, err = reboundSvc.Send(ctx, sessionID, clientID, originalText, "test")
	assertRejectedWithoutMutation("changed binding", err)

	retried, err := svc.Send(ctx, sessionID, clientID, originalText, "test")
	if err != nil || retried.Entry.Status != OutSent || (!retried.Duplicate && string(retried.Upstream) != `{"accepted":true}`) {
		t.Errorf("original retry did not return its duplicate/accepted outcome: %+v %v", retried, err)
	}
	wantSends := sendsBefore + 1
	if retried != nil && retried.Duplicate {
		wantSends = sendsBefore
	}
	var transportCalls []botCallSeen
	for _, call := range caller.calls {
		if call.method == "agentbot.send" {
			transportCalls = append(transportCalls, call)
		}
	}
	if len(transportCalls) != wantSends {
		t.Errorf("original retry transport sends=%d, want %d", len(transportCalls), wantSends)
	} else if retried != nil && !retried.Duplicate {
		last := transportCalls[len(transportCalls)-1]
		if last.machine != originalOperation.MachineID || last.params["operation_id"] != originalOperation.OperationID || last.params["text"] != originalText {
			t.Errorf("original retry escaped its operation binding: %+v", last)
		}
	}
	if got := string(kv.m[botOperationsKey]); got != before[botOperationsKey] {
		t.Error("original retry replaced the persistent operation")
	}
}

func TestUnhealthyBotDoesNotDisableHealthyBot(t *testing.T) {
	caller := &fakeBotCaller{pages: map[string]BotPage{testBotRegistration.SessionID: testBotPage()}}
	good := newTestBot(t, nil, caller)
	other := testBotRegistration
	other.ID, other.SessionID = "broken", "claude_unhealthy"
	bad, err := NewBotRegistry(nil, caller).Source(other)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(nil, nil, nil, good, bad)
	list, err := svc.Sessions(context.Background(), "", "")
	if err != nil || len(list) != 1 || list[0].ID != "bot_research:endpoint" {
		t.Fatalf("healthy source was hidden: %+v %v", list, err)
	}
	statuses := svc.Statuses(context.Background())
	if len(statuses) != 2 || statuses[1].Error == "" || strings.Contains(statuses[1].Error, "socket") || strings.Contains(statuses[1].Error, "token") {
		t.Fatalf("unsanitized health status %+v", statuses)
	}
}

func TestBotReplyRankInvalidatesOnceAndMergesHistory(t *testing.T) {
	caller := &fakeBotCaller{pages: map[string]BotPage{testBotRegistration.SessionID: testBotPage()}}
	src := newTestBot(t, nil, caller)
	var changes [][]string
	svc := NewService(nil, func(ids []string) { changes = append(changes, ids) }, nil, src)
	ctx := context.Background()
	svc.PollOnce(ctx) // baseline does not notify
	if len(changes) != 0 {
		t.Fatalf("baseline notified %v", changes)
	}
	page := testBotPage()
	page.RankTimeMs = 200
	page.Messages = append([]Message{{
		ExternalID: "assistant-2", Role: "assistant", CreateTimeMs: 200, Position: 3,
		Blocks: []Block{{Type: "text", Text: "Follow-up answer"}},
	}}, page.Messages...)
	caller.setPage(testBotRegistration.SessionID, page)
	svc.PollOnce(ctx)
	if len(changes) != 1 || len(changes[0]) != 1 || changes[0][0] != "bot_research:endpoint" {
		t.Fatalf("reply invalidation %v", changes)
	}
	messages, err := svc.Messages(ctx, "bot_research:endpoint", "", 40)
	if err != nil || len(messages.Messages) != 2 || messages.Messages[0].ExternalID != "assistant-2" {
		t.Fatalf("invalidated history %+v %v", messages, err)
	}
	svc.PollOnce(ctx)
	if len(changes) != 1 {
		t.Fatalf("stable rank notified again %v", changes)
	}
}

type botFailKV struct{ memKV }

func (k *botFailKV) PutSetting(context.Context, string, any) error {
	return errors.New("settings unavailable")
}

func TestBotOperationRequiresPersistentRecordBeforeWrite(t *testing.T) {
	caller := &fakeBotCaller{pages: map[string]BotPage{testBotRegistration.SessionID: testBotPage()}}
	kv := &botFailKV{memKV{m: map[string][]byte{}}}
	src := newTestBot(t, kv, caller)
	if _, err := src.Send(context.Background(), BotSessionKey, NewClientID(), "no durable operation"); err == nil {
		t.Fatal("send proceeded without a durable operation ID")
	}
	if n := caller.count("agentbot.send"); n != 0 {
		t.Fatalf("%d writes without a durable operation ID", n)
	}
}

func TestBotOperationIDHasOrcaShape(t *testing.T) {
	caller := &fakeBotCaller{pages: map[string]BotPage{testBotRegistration.SessionID: testBotPage()}}
	src := newTestBot(t, &memKV{m: map[string][]byte{}}, caller)
	if _, err := src.Send(context.Background(), BotSessionKey, NewClientID(), "assignment"); err != nil {
		t.Fatal(err)
	}
	calls := caller.calls
	if len(calls) != 1 {
		t.Fatalf("calls %+v", calls)
	}
	op, _ := calls[0].params["operation_id"].(string)
	if len(op) != 46 || op[13] != '-' {
		t.Fatalf("operation ID %q", op)
	}
}

func TestBotOperationBookIsBoundedAndKeepsNewest(t *testing.T) {
	defer func(was int) { botOperationsMax = was }(botOperationsMax)
	botOperationsMax = 3
	caller := &fakeBotCaller{pages: map[string]BotPage{testBotRegistration.SessionID: testBotPage()}}
	kv := &memKV{m: map[string][]byte{}}
	src := newTestBot(t, kv, caller)
	clock := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	src.ops.now = func() time.Time { clock = clock.Add(time.Millisecond); return clock } // distinct CreatedAt, no sleep
	ids := make([]string, 5)
	for i := range ids {
		ids[i] = NewClientID()
		if _, err := src.Send(context.Background(), BotSessionKey, ids[i], fmt.Sprintf("message %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var saved map[string]botOperation
	if ok, err := kv.Setting(context.Background(), botOperationsKey, &saved); !ok || err != nil {
		t.Fatalf("book not persisted: %v %v", ok, err)
	}
	if len(saved) != 3 {
		t.Fatalf("book kept %d rows, want 3", len(saved))
	}
	for _, id := range ids[2:] {
		if _, ok := saved[id]; !ok {
			t.Fatalf("newest operation %s was pruned", id)
		}
	}
	// A kept row still refuses a changed payload after a restart (a fresh book on the same store).
	restarted := newTestBot(t, kv, caller)
	if _, err := restarted.Send(context.Background(), BotSessionKey, ids[4], "changed"); err == nil {
		t.Fatal("kept operation accepted a changed payload")
	}
}
