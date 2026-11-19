package extmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

type legacyUpdateStore10515 struct {
	*subscriptions.MemStore
	calls atomic.Int32
	err   error
}

func (s *legacyUpdateStore10515) UpdateSubscription(ctx context.Context, sub subscriptions.Subscription, prev int64) (bool, error) {
	s.calls.Add(1)
	if s.err != nil {
		return false, s.err
	}
	return s.MemStore.UpdateSubscription(ctx, sub, prev)
}

func legacySaveResult10515(e *toolEnv, id string, rev int64, label string) proto.ToolResult {
	e.t.Helper()
	args := fmt.Sprintf(`{"action":"update","id":%q,"revision":%d,"label":%q}`, id, rev, label)
	return e.h.callSubscriptionTool(context.Background(), asker{"mac", "conv1"}, "subscription_save", []byte(args))
}

func requireDefiniteLegacyNonApply10515(t *testing.T, res proto.ToolResult) {
	t.Helper()
	text := strings.ToLower(res.Content[0].Text)
	if !res.IsError || (!strings.Contains(text, "not applied") && !strings.Contains(text, "nothing was applied")) || strings.Contains(text, "uncertain") {
		t.Fatalf("pre-Apply legacy wait result = %+v; want a definite not-applied error, not an uncertain one", res)
	}
}

func TestSubscriptionSaveLegacyWaitFailureIsDefinitelyNotApplied10515(t *testing.T) {
	e := newToolEnv(t)
	before := e.made()
	store := &legacyUpdateStore10515{MemStore: e.store}
	e.h.subs = toolRunService(e, store, nil)
	e.h.opts.Ask = func(context.Context, proto.Interaction) (proto.Resolution, error) {
		return proto.Resolution{}, errors.New("the approval wait ended before an answer")
	}
	res := legacySaveResult10515(e, before.ID, before.Revision, "Must not be saved")
	requireDefiniteLegacyNonApply10515(t, res)
	current, _, err := e.h.subs.Get(context.Background(), before.ID)
	if err != nil || current.Label != before.Label || current.Revision != before.Revision || store.calls.Load() != 0 {
		t.Fatalf("wait failure mutated state or entered Apply: current=%+v err=%v update calls=%d", current, err, store.calls.Load())
	}
}

func TestSubscriptionSaveLegacyStaleApplyIsDefinitelyNotApplied10515(t *testing.T) {
	e := newToolEnv(t)
	before := e.made()
	store := &legacyUpdateStore10515{MemStore: e.store}
	e.h.subs = toolRunService(e, store, nil)
	var externalErr error
	e.h.opts.Ask = func(_ context.Context, ix proto.Interaction) (proto.Resolution, error) {
		_, externalErr = e.h.subs.Update(context.Background(), before.ID, subscriptions.Patch{Revision: before.Revision, Label: ptrTo("External change")}, subscriptionActor)
		return allowCard(ix), nil
	}
	res := legacySaveResult10515(e, before.ID, before.Revision, "Stale approved change")
	if externalErr != nil {
		t.Fatal(externalErr)
	}
	requireDefiniteLegacyNonApply10515(t, res)
	current, _, err := e.h.subs.Get(context.Background(), before.ID)
	if err != nil || current.Label != "External change" || current.Revision != before.Revision+1 || store.calls.Load() != 1 {
		t.Fatalf("stale Apply changed current state or wrote twice: current=%+v err=%v update calls=%d", current, err, store.calls.Load())
	}
}

func TestSubscriptionSaveWithoutApprovalServiceIsDefinitelyNotApplied10515(t *testing.T) {
	e := newToolEnv(t)
	before := e.made()
	store := &legacyUpdateStore10515{MemStore: e.store}
	e.h.subs = toolRunService(e, store, nil)
	e.h.opts.AskPrepared = nil
	e.h.opts.Ask = nil
	res := legacySaveResult10515(e, before.ID, before.Revision, "Must not be saved")
	requireDefiniteLegacyNonApply10515(t, res)
	current, _, err := e.h.subs.Get(context.Background(), before.ID)
	if err != nil || current.Label != before.Label || current.Revision != before.Revision || store.calls.Load() != 0 {
		t.Fatalf("offline wait mutated state or entered Apply: current=%+v err=%v update calls=%d", current, err, store.calls.Load())
	}
}

func TestSubscriptionSaveLegacyWriteFailureRemainsUncertain10515(t *testing.T) {
	e := newToolEnv(t)
	before := e.made()
	store := &legacyUpdateStore10515{MemStore: e.store, err: errors.New("the write may have reached the store")}
	e.h.subs = toolRunService(e, store, nil)
	res := legacySaveResult10515(e, before.ID, before.Revision, "Approved change")
	text := strings.ToLower(res.Content[0].Text)
	if !res.IsError || !strings.Contains(text, "uncertain") || !strings.Contains(text, "do not assume") ||
		strings.Contains(text, "not applied") || strings.Contains(text, "nothing was applied") {
		t.Fatalf("genuine Apply failure = %+v; want an uncertain execution outcome", res)
	}
	current, _, err := e.h.subs.Get(context.Background(), before.ID)
	if err != nil || current.Label != before.Label || current.Revision != before.Revision || store.calls.Load() != 1 {
		t.Fatalf("Apply did not make exactly one controlled store attempt: current=%+v err=%v update calls=%d", current, err, store.calls.Load())
	}
}
