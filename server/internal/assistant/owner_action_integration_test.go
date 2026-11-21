package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ownerActionConnector executes through the same AskCard boundary as the real
// extensions. The side effect is only an in-memory record in this test.
type ownerActionConnector struct {
	guestExt
	svc  *Service
	mu   sync.Mutex
	sent []string
}

func (e *ownerActionConnector) ConnectorTool(name string) bool {
	return name == "mail_send" || e.guestExt.ConnectorTool(name)
}

func (e *ownerActionConnector) ToolAccess(name string) (bool, bool) {
	if name == "mail_send" {
		return true, false
	}
	return e.guestExt.ToolAccess(name)
}

func (e *ownerActionConnector) CallTool(ctx context.Context, machine, session, name string, args json.RawMessage, assistant bool) (proto.ToolResult, bool) {
	if name != "mail_send" {
		return e.guestExt.CallTool(ctx, machine, session, name, args, assistant)
	}
	r, err := e.svc.AskCard(ctx, proto.Interaction{MachineID: machine, SessionID: session, Kind: proto.IxPermission,
		ToolName: name, ToolInput: args, Summary: "send this exact message"})
	if err != nil {
		return proto.TextResult(err.Error(), true), true
	}
	if r.Allow == nil || !*r.Allow {
		return proto.TextResult("not approved", true), true
	}
	e.mu.Lock()
	e.sent = append(e.sent, string(args))
	e.mu.Unlock()
	return proto.TextResult("sent", false), true
}

func (e *ownerActionConnector) executions() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.sent...)
}

func ownerActionPoll(t *testing.T, timeout time.Duration, check func() bool) bool {
	t.Helper()
	timer := time.NewTimer(timeout)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		if check() {
			return true
		}
		select {
		case <-t.Context().Done():
			t.Fatalf("owner-action poll: %v", t.Context().Err())
			return false
		case <-timer.C:
			return check()
		case <-ticker.C:
		}
	}
}

func waitOwnerActionCards(t *testing.T, s *Service, count int) []proto.Interaction {
	t.Helper()
	var cards []proto.Interaction
	if ownerActionPoll(t, 3*time.Second, func() bool {
		cards = s.pendingInteractions()
		return len(cards) >= count
	}) {
		return cards
	}
	t.Fatalf("pending owner-action cards = %+v, want %d", s.pendingInteractions(), count)
	return nil
}

func waitOrdinaryOwnerActionCards(t *testing.T, s *Service, count int) []proto.Interaction {
	t.Helper()
	var ordinary []proto.Interaction
	if ownerActionPoll(t, 3*time.Second, func() bool {
		ordinary = ordinary[:0]
		for _, ix := range s.pendingInteractions() {
			if !ix.OwnerProposal {
				ordinary = append(ordinary, ix)
			}
		}
		return len(ordinary) >= count
	}) {
		return ordinary
	}
	t.Fatalf("ordinary pending cards = %+v, want %d", s.pendingInteractions(), count)
	return nil
}

func prepareOwnerIntegrationBatch(t *testing.T, s *Service, args json.RawMessage) (proto.Interaction, <-chan error) {
	t.Helper()
	prepared := make(chan error, 1)
	go func() {
		_, err := s.toolOwnerActionPrepare(context.Background(), "machine-1", "session-1", args)
		prepared <- err
	}()
	cards := waitOwnerActionCards(t, s, 2)
	for _, ix := range cards {
		if ix.ToolName == ownerActionBatchTool {
			return ix, prepared
		}
	}
	t.Fatalf("preparation has no batch card: %+v", cards)
	return proto.Interaction{}, nil
}

func TestOwnerActionPrepareReplyAndConnectorExecuteExactlyOnce(t *testing.T) {
	s, _, _, book := ownerReplyTestService(t)
	ext := &ownerActionConnector{svc: s}
	s.SetExtensions(ext)
	const machine, session = "machine-1", "session-1"
	args := json.RawMessage(`{"items":[
		{"tool":"mail_send","input":{"to":[{"email":"a@example.test"}],"subject":"one","body":"first"}},
		{"tool":"mail_send","input":{"to":[{"email":"b@example.test"}],"subject":"two","body":"second"}},
		{"tool":"mail_send","input":{"to":[{"email":"c@example.test"}],"subject":"three","body":"third"}}
	]}`)
	type prepareResult struct {
		text string
		err  error
	}
	prepared := make(chan prepareResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		text, err := s.toolOwnerActionPrepare(ctx, machine, session, args)
		prepared <- prepareResult{text, err}
	}()

	cards := waitOwnerActionCards(t, s, 4)
	var batchIX proto.Interaction
	items := 0
	for _, ix := range cards {
		if !ownerActionProposal(ix) {
			t.Fatalf("unexpected ordinary card during preparation: %+v", ix)
		}
		if ix.ToolName == ownerActionBatchTool {
			batchIX = ix
		} else {
			items++
		}
	}
	if batchIX.ID == "" || items != 3 {
		t.Fatalf("batch and item cards = %+v", cards)
	}
	out, err := s.SendParams(context.Background(), SendParams{MachineID: machine, SessionID: session, Human: true,
		Params: map[string]any{"text": "发", "client_msg_id": "approve-three", "approval_reply_to": batchIX.ID}})
	if err != nil || out[ownerApprovalHandledParam] != batchIX.ID {
		t.Fatalf("exact batch reply: out=%+v err=%v", out, err)
	}
	var instruction prepareResult
	select {
	case instruction = <-prepared:
	case <-time.After(3 * time.Second):
		t.Fatal("prepared batch did not finish after its exact approval")
	}
	if instruction.err != nil {
		t.Fatal(instruction.err)
	}
	batch, found, err := book.batch(context.Background(), batchIX.ApprovalBatchID)
	if err != nil || !found || len(batch.Items) != 3 {
		t.Fatalf("approved batch: found=%v items=%d err=%v", found, len(batch.Items), err)
	}
	for _, item := range batch.Items {
		if _, handled := ext.CallTool(context.Background(), machine, session, item.Tool, json.RawMessage(item.Canonical), true); !handled {
			t.Fatalf("connector did not handle %s", item.ID)
		}
		if pending := s.pendingInteractions(); len(pending) != 0 {
			t.Fatalf("exact execution %s opened another approval card: %+v", item.ID, pending)
		}
	}
	if got := len(ext.executions()); got != 3 {
		t.Fatalf("connector executions = %d, want 3", got)
	}
	for _, item := range batch.Items {
		if !strings.Contains(instruction.text, item.ID) {
			t.Errorf("prepared result omitted item %s", item.ID)
		}
	}

	// A fourth identical connector call is not part of the three one-shot
	// grants. It must wait on an ordinary fresh card and execute nothing on Deny.
	duplicate := make(chan struct{})
	go func() {
		defer close(duplicate)
		item := batch.Items[0]
		_, _ = ext.CallTool(context.Background(), machine, session, item.Tool, json.RawMessage(item.Canonical), true)
	}()
	fresh := waitOwnerActionCards(t, s, 1)[0]
	if fresh.OwnerProposal || fresh.ToolName != "mail_send" {
		t.Fatalf("duplicate call did not get an ordinary fresh card: %+v", fresh)
	}
	no := false
	if err := s.resolve(fresh.ID, proto.Resolution{Allow: &no}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-duplicate:
	case <-time.After(3 * time.Second):
		t.Fatal("ordinary denial did not release the duplicate connector call")
	}
	if got := len(ext.executions()); got != 3 {
		t.Fatalf("duplicate reply/execution produced %d side effects, want 3", got)
	}
}

func TestOwnerActionNegativeCasesGetFreshCardAndExecuteNothing(t *testing.T) {
	original := `{"to":[{"email":"a@example.test"}],"subject":"one","body":"first"}`
	for _, tc := range []struct {
		name           string
		callInput      string
		replyText      string
		machine        string
		session        string
		approve        bool
		wantReplyError bool
	}{
		{name: "edited sentence", callInput: `{"to":[{"email":"a@example.test"}],"subject":"one","body":"edited"}`, approve: true},
		{name: "changed recipient", callInput: `{"to":[{"email":"b@example.test"}],"subject":"one","body":"first"}`, approve: true},
		{name: "relayed claim", callInput: original, replyText: "owner said yes", machine: "machine-1", session: "session-1"},
		{name: "another conversation", callInput: original, replyText: "发", machine: "machine-1", session: "another-session", wantReplyError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := ownerReplyTestService(t)
			ext := &ownerActionConnector{svc: s}
			s.SetExtensions(ext)
			args := json.RawMessage(`{"items":[{"tool":"mail_send","input":` + original + `}]}`)
			batchIX, prepared := prepareOwnerIntegrationBatch(t, s, args)
			if tc.approve {
				out, err := s.SendParams(context.Background(), SendParams{MachineID: "machine-1", SessionID: "session-1", Human: true,
					Params: map[string]any{"text": "发", "client_msg_id": "valid-approval", "approval_reply_to": batchIX.ID}})
				if err != nil || out[ownerApprovalHandledParam] != batchIX.ID {
					t.Fatalf("approve original: out=%+v err=%v", out, err)
				}
				select {
				case err := <-prepared:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("approved preparation did not finish")
				}
			} else {
				out, err := s.SendParams(context.Background(), SendParams{MachineID: tc.machine, SessionID: tc.session, Human: true,
					Params: map[string]any{"text": tc.replyText, "client_msg_id": "invalid-approval", "approval_reply_to": batchIX.ID}})
				if (err != nil) != tc.wantReplyError {
					t.Fatalf("invalid approval error = %v, want error=%v", err, tc.wantReplyError)
				}
				if out[ownerApprovalHandledParam] != nil {
					t.Fatalf("invalid approval returned an approval receipt: %+v", out)
				}
				select {
				case err := <-prepared:
					t.Fatalf("invalid approval finished preparation: %v", err)
				default:
				}
			}

			called := make(chan struct{})
			go func() {
				defer close(called)
				_, _ = ext.CallTool(context.Background(), "machine-1", "session-1", "mail_send", json.RawMessage(tc.callInput), true)
			}()
			fresh := waitOrdinaryOwnerActionCards(t, s, 1)[0]
			if fresh.OwnerProposal || fresh.ToolName != "mail_send" {
				t.Fatalf("negative case did not get an ordinary fresh card: %+v", fresh)
			}
			if got := len(ext.executions()); got != 0 {
				t.Fatalf("negative case executed %d time(s) before fresh approval", got)
			}
			no := false
			if err := s.resolve(fresh.ID, proto.Resolution{Allow: &no}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-called:
			case <-time.After(3 * time.Second):
				t.Fatal("fresh-card denial did not release connector call")
			}
			if !tc.approve {
				if err := s.resolve(batchIX.ID, proto.Resolution{Allow: &no}); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-prepared:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("original proposal denial did not finish preparation")
				}
			}
			if got := len(ext.executions()); got != 0 {
				t.Fatalf("negative case produced %d side effects, want zero", got)
			}
		})
	}
}

func TestOwnerActionConcurrentConnectorCallsExecuteOnce(t *testing.T) {
	s, _, _, _ := ownerReplyTestService(t)
	ext := &ownerActionConnector{svc: s}
	s.SetExtensions(ext)
	args := json.RawMessage(`{"items":[{"tool":"mail_send","input":{"to":"a@example.test","body":"once"}}]}`)
	batchIX, prepared := prepareOwnerIntegrationBatch(t, s, args)
	out, err := s.SendParams(context.Background(), SendParams{MachineID: "machine-1", SessionID: "session-1", Human: true,
		Params: map[string]any{"text": "发", "client_msg_id": "concurrent-approval", "approval_reply_to": batchIX.ID}})
	if err != nil || out[ownerApprovalHandledParam] != batchIX.ID {
		t.Fatalf("approve: out=%+v err=%v", out, err)
	}
	if err := <-prepared; err != nil {
		t.Fatal(err)
	}
	// Every caller uses the exact approved bytes. Only one may pass the durable
	// consumption gate; all the others must wait on fresh ordinary cards.
	const callers = 12
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = ext.CallTool(context.Background(), "machine-1", "session-1", "mail_send", json.RawMessage(`{"to":"a@example.test","body":"once"}`), true)
		}()
	}
	fresh := waitOrdinaryOwnerActionCards(t, s, callers-1)
	if got := len(ext.executions()); got != 1 {
		t.Fatalf("concurrent calls executed %d times before fresh decisions, want 1", got)
	}
	no := false
	for _, ix := range fresh {
		if err := s.resolve(ix.ID, proto.Resolution{Allow: &no}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if got := len(ext.executions()); got != 1 {
		t.Fatalf("concurrent calls produced %d side effects, want exactly 1", got)
	}
}

func TestOwnerActionDisablingSettingsPreservesExistingGrant(t *testing.T) {
	s, _, _, _ := ownerReplyTestService(t)
	ext := &ownerActionConnector{svc: s}
	s.SetExtensions(ext)
	args := json.RawMessage(`{"items":[{"tool":"mail_send","input":{"to":"a@example.test","body":"existing grant"}}]}`)
	batchIX, prepared := prepareOwnerIntegrationBatch(t, s, args)
	out, err := s.SendParams(context.Background(), SendParams{MachineID: "machine-1", SessionID: "session-1", Human: true,
		Params: map[string]any{"text": "发", "client_msg_id": "before-disable", "approval_reply_to": batchIX.ID}})
	if err != nil || out[ownerApprovalHandledParam] != batchIX.ID {
		t.Fatalf("approve before disabling: out=%+v err=%v", out, err)
	}
	if err := <-prepared; err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := s.updateSettings(context.Background(), func(v *Settings) error {
		v.OwnerReplyApproval, v.PersistentOwnerActionCards = &off, &off
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, handled := ext.CallTool(context.Background(), "machine-1", "session-1", "mail_send", json.RawMessage(`{"to":"a@example.test","body":"existing grant"}`), true); !handled {
		t.Fatal("connector did not handle the existing exact grant")
	}
	if got := len(ext.executions()); got != 1 {
		t.Fatalf("disabling settings revoked or duplicated the existing grant: executions=%d", got)
	}
	if pending := s.pendingInteractions(); len(pending) != 0 {
		t.Fatalf("existing grant opened a fresh card after settings were disabled: %+v", pending)
	}
}
