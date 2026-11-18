package hub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestPreparedSettingHandleIsOwnerBoundAndAppliedOnce(t *testing.T) {
	h := &Hub{}
	var applies atomic.Int32
	if err := h.retainPreparedSetting("manager-a", proto.Interaction{ID: "ix_hub_once"}, func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "applied", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.applyPreparedSetting(context.Background(), "manager-b", "ix_hub_once", proto.Resolution{}); err == nil {
		t.Fatal("another manager consumed the retained handle")
	}
	if text, err := h.applyPreparedSetting(context.Background(), "manager-a", "ix_hub_once", proto.Resolution{}); err != nil || text != "applied" {
		t.Fatalf("owner apply = %q, %v", text, err)
	}
	if _, err := h.applyPreparedSetting(context.Background(), "manager-a", "ix_hub_once", proto.Resolution{}); err == nil {
		t.Fatal("a duplicate apply consumed the handle twice")
	}
	if applies.Load() != 1 {
		t.Fatalf("apply count = %d, want one", applies.Load())
	}
}

func TestPreparedSettingDiscardIsOwnerBound(t *testing.T) {
	h := &Hub{}
	if err := h.retainPreparedSetting("manager-a", proto.Interaction{ID: "ix_hub_discard"}, func(context.Context, proto.Resolution) (string, error) {
		return "unexpected", nil
	}); err != nil {
		t.Fatal(err)
	}
	h.dropPreparedSetting("manager-b", "ix_hub_discard")
	if _, err := h.applyPreparedSetting(context.Background(), "manager-a", "ix_hub_discard", proto.Resolution{}); err != nil {
		t.Fatalf("another manager discarded the handle: %v", err)
	}
	if err := h.retainPreparedSetting("manager-a", proto.Interaction{ID: "ix_hub_discard"}, func(context.Context, proto.Resolution) (string, error) {
		return "unexpected", nil
	}); err != nil {
		t.Fatal(err)
	}
	h.dropPreparedSetting("manager-a", "ix_hub_discard")
	if _, err := h.applyPreparedSetting(context.Background(), "manager-a", "ix_hub_discard", proto.Resolution{}); err == nil {
		t.Fatal("a discarded handle still applied")
	}
}

func TestPreparedSettingBusReturnsStructuredStaleAndConsumesHandle(t *testing.T) {
	h := &Hub{}
	var applies atomic.Int32
	if err := h.retainPreparedSetting("manager-a", proto.Interaction{ID: "ix_hub_stale"}, func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "", approval.ErrStale
	}); err != nil {
		t.Fatal(err)
	}
	allow := true
	params, _ := json.Marshal(assistantwire.FleetApprovalApply{InteractionID: "ix_hub_stale", Resolution: proto.Resolution{Allow: &allow}})
	raw, err := h.busPreparedSettingApply(context.Background(), &busConn{ident: db.BusIdentity{ID: "manager-a"}}, params)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := raw.(assistantwire.FleetApprovalApplyResult)
	if !ok || !out.Stale || out.Text != "" {
		t.Fatalf("stale bus result = %#v", raw)
	}
	if applies.Load() != 1 {
		t.Fatalf("stale apply count = %d", applies.Load())
	}
	again, err := h.busPreparedSettingApply(context.Background(), &busConn{ident: db.BusIdentity{ID: "manager-a"}}, params)
	if err != nil {
		t.Fatalf("duplicate apply did not get a structured definite-not-applied result: %v", err)
	}
	repeated, ok := again.(assistantwire.FleetApprovalApplyResult)
	if !ok || !repeated.Stale || repeated.Text != "" {
		t.Fatalf("duplicate apply result = %#v, want stale with no execution", again)
	}
	if applies.Load() != 1 {
		t.Fatalf("duplicate bus apply executed the setting again: apply count = %d, want one", applies.Load())
	}
}

func TestPreparedSettingOneShotAdapterStillAppliesAndCleansUp(t *testing.T) {
	h := &Hub{}
	allow := true
	h.askVia = func(context.Context, proto.Interaction) (proto.Resolution, error) {
		return proto.Resolution{Allow: &allow}, nil
	}
	var applies atomic.Int32
	out, err := h.waitPreparedSetting(context.Background(), "manager-a", proto.Interaction{ID: "ix_hub_legacy"}, "", func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "applied", nil
	}, nil)
	if err != nil || out.Text != "applied" || applies.Load() != 1 {
		t.Fatalf("one-shot prepared setting = %+v, %v, applies=%d", out, err, applies.Load())
	}
	h.prepared.mu.Lock()
	left := len(h.prepared.byID)
	h.prepared.mu.Unlock()
	if left != 0 {
		t.Fatalf("one-shot approval retained %d handles", left)
	}
}

func TestPreparedSettingBusDoesNotExecuteForAnotherManager(t *testing.T) {
	h := &Hub{}
	if err := h.retainPreparedSetting("manager-a", proto.Interaction{ID: "ix_hub_wrong_caller"}, func(context.Context, proto.Resolution) (string, error) {
		return "", errors.New("must not run")
	}); err != nil {
		t.Fatal(err)
	}
	allow := true
	params, _ := json.Marshal(assistantwire.FleetApprovalApply{InteractionID: "ix_hub_wrong_caller", Resolution: proto.Resolution{Allow: &allow}})
	if _, err := h.busPreparedSettingApply(context.Background(), &busConn{ident: db.BusIdentity{ID: "manager-b"}}, params); err == nil {
		t.Fatal("the bus accepted another manager's apply")
	}
	h.prepared.mu.Lock()
	_, kept := h.prepared.byID["ix_hub_wrong_caller"]
	h.prepared.mu.Unlock()
	if !kept {
		t.Fatal("another manager's failed apply consumed the handle")
	}
}

func TestPreparedSettingBusRequiresFreshOwnerAllow(t *testing.T) {
	h := &Hub{}
	var applies atomic.Int32
	if err := h.retainPreparedSetting("manager-a", proto.Interaction{ID: "ix_hub_owner"}, func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "applied", nil
	}); err != nil {
		t.Fatal(err)
	}
	allow := true
	for name, resolution := range map[string]proto.Resolution{
		"attributed": {Allow: &allow, AnsweredBy: proto.AnsweredByAssistant},
		"remembered": {Allow: &allow, Remembered: true},
	} {
		t.Run(name, func(t *testing.T) {
			params, _ := json.Marshal(assistantwire.FleetApprovalApply{InteractionID: "ix_hub_owner", Resolution: resolution})
			if _, err := h.busPreparedSettingApply(context.Background(), &busConn{ident: db.BusIdentity{ID: "manager-a"}}, params); err == nil {
				t.Fatal("a non-owner answer consumed the prepared setting")
			}
			if applies.Load() != 0 {
				t.Fatal("a non-owner answer executed the prepared setting")
			}
		})
	}
	params, _ := json.Marshal(assistantwire.FleetApprovalApply{InteractionID: "ix_hub_owner", Resolution: proto.Resolution{Allow: &allow}})
	raw, err := h.busPreparedSettingApply(context.Background(), &busConn{ident: db.BusIdentity{ID: "manager-a"}}, params)
	if err != nil || raw.(assistantwire.FleetApprovalApplyResult).Text != "applied" || applies.Load() != 1 {
		t.Fatalf("fresh owner Allow = %#v, %v, applies=%d", raw, err, applies.Load())
	}
}

func TestPreparedSettingHandleOutlivesPauseBudgetButNotItsGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &Hub{}
		start := time.Now()
		var applies atomic.Int32
		ix := proto.Interaction{ID: "ix_hub_deadline", ExpiresAt: start.Add(15 * time.Minute)}
		if err := h.retainPreparedSetting("manager-a", ix, func(context.Context, proto.Resolution) (string, error) {
			applies.Add(1)
			return "unexpected", nil
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(15*time.Minute + preparedSettingPauseBudget) // sleep-ok: virtual synctest time advances the bounded handle lifetime
		h.prepared.mu.Lock()
		_, kept := h.prepared.byID[ix.ID]
		h.prepared.mu.Unlock()
		if !kept {
			t.Fatal("the handle expired before the cumulative clarification budget plus decision time")
		}
		time.Sleep(cardAnswerGrace) // sleep-ok: virtual synctest time crosses the answer grace
		synctest.Wait()
		h.prepared.mu.Lock()
		_, kept = h.prepared.byID[ix.ID]
		h.prepared.mu.Unlock()
		if kept {
			t.Fatal("the handle survived its bounded clarification lifetime")
		}
		allow := true
		params, _ := json.Marshal(assistantwire.FleetApprovalApply{InteractionID: ix.ID, Resolution: proto.Resolution{Allow: &allow}})
		raw, err := h.busPreparedSettingApply(context.Background(), &busConn{ident: db.BusIdentity{ID: "manager-a"}}, params)
		if err != nil || !raw.(assistantwire.FleetApprovalApplyResult).Stale || applies.Load() != 0 {
			t.Fatalf("expired handle apply = %#v, %v, applies=%d; want definite not applied", raw, err, applies.Load())
		}
	})
}

func TestPreparedSettingWaitingCardOutlivesWaitingAndPauseBudgets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		h.askVia = nil
		mgr := playManager(t, h, func(context.Context, string, json.RawMessage) (any, error) { return map[string]any{}, nil })
		mgr.peer.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
			time.Sleep(15 * time.Minute) // sleep-ok: virtual synctest time covers the coordinator's first decision window
			return approval.Outcome{Clarifying: true, Text: approval.WaitingText}, nil
		}
		start := time.Now()
		var applies atomic.Int32
		ix := proto.Interaction{ID: "ix_hub_waiting_deadline", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission,
			ExpiresAt: start.Add(15 * time.Minute)}
		out, err := h.waitPreparedSetting(context.Background(), assistantwire.AssistantID, ix, "",
			func(context.Context, proto.Resolution) (string, error) {
				applies.Add(1)
				return "unexpected", nil
			}, nil)
		if err != nil || !out.Clarifying || out.Text != approval.WaitingText || time.Since(start) != 15*time.Minute {
			t.Fatalf("waiting prepared setting = %+v, %v after %s", out, err, time.Since(start))
		}

		// The original pause-only deadline ends here. A waiting card must still
		// retain its handle for the clarification budget that can follow.
		time.Sleep(preparedSettingPauseBudget + cardAnswerGrace) // sleep-ok: virtual synctest time advances the bounded handle lifetime
		synctest.Wait()
		h.prepared.mu.Lock()
		_, kept := h.prepared.byID[ix.ID]
		h.prepared.mu.Unlock()
		if !kept {
			t.Fatal("the handle expired between the unanswered window and its clarification budget")
		}
		time.Sleep(preparedSettingPauseBudget) // sleep-ok: virtual synctest time spends the clarification budget
		synctest.Wait()
		h.prepared.mu.Lock()
		_, kept = h.prepared.byID[ix.ID]
		h.prepared.mu.Unlock()
		if kept {
			t.Fatal("the waiting plus clarification handle survived its bounded lifetime")
		}
		allow := true
		params, _ := json.Marshal(assistantwire.FleetApprovalApply{InteractionID: ix.ID, Resolution: proto.Resolution{Allow: &allow}})
		raw, err := h.busPreparedSettingApply(context.Background(), &busConn{ident: db.BusIdentity{ID: assistantwire.AssistantID}}, params)
		if err != nil || !raw.(assistantwire.FleetApprovalApplyResult).Stale || applies.Load() != 0 {
			t.Fatalf("expired waiting handle apply = %#v, %v, applies=%d; want definite not applied", raw, err, applies.Load())
		}
	})
}

func TestPreparedSettingRestartLosesExecutableHandle(t *testing.T) {
	oldHub := &Hub{}
	var applies atomic.Int32
	ix := proto.Interaction{ID: "ix_hub_restart", ExpiresAt: time.Now().Add(time.Hour)}
	if err := oldHub.retainPreparedSetting("manager-a", ix, func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "unexpected", nil
	}); err != nil {
		t.Fatal(err)
	}

	// Executable closures are process-local. The new hub has no handle to apply,
	// and its structured result must not turn that into an uncertain execution.
	restartedHub := &Hub{}
	allow := true
	params, _ := json.Marshal(assistantwire.FleetApprovalApply{InteractionID: ix.ID, Resolution: proto.Resolution{Allow: &allow}})
	raw, err := restartedHub.busPreparedSettingApply(context.Background(), &busConn{ident: db.BusIdentity{ID: "manager-a"}}, params)
	if err != nil || !raw.(assistantwire.FleetApprovalApplyResult).Stale || applies.Load() != 0 {
		t.Fatalf("restart-lost handle apply = %#v, %v, applies=%d; want definite not applied", raw, err, applies.Load())
	}
}

func TestPreparedSettingWithoutExpiryGetsBoundedAfterClarification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newTestHub(t)
		h.askVia = nil
		mgr := playManager(t, h, func(context.Context, string, json.RawMessage) (any, error) { return map[string]any{}, nil })
		mgr.peer.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
			return approval.Outcome{Clarifying: true, Text: approval.ClarifyingText}, nil
		}
		out, err := h.waitPreparedSetting(context.Background(), assistantwire.AssistantID,
			proto.Interaction{ID: "ix_hub_no_expiry", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission}, "",
			func(context.Context, proto.Resolution) (string, error) { return "unexpected", nil }, nil)
		if err != nil || !out.Clarifying {
			t.Fatalf("clarifying no-expiry card = %+v, %v", out, err)
		}
		time.Sleep(preparedSettingPauseBudget + cardAnswerGrace) // sleep-ok: virtual synctest time advances the bounded handle lifetime
		synctest.Wait()
		h.prepared.mu.Lock()
		_, kept := h.prepared.byID["ix_hub_no_expiry"]
		h.prepared.mu.Unlock()
		if kept {
			t.Fatal("a clarified card without its own expiry retained the handle forever")
		}
	})
}

func TestHubSelfSettingRechecksCurrentProposalAfterClarification(t *testing.T) {
	for _, externalChange := range []bool{false, true} {
		name := "fresh proposal applies"
		if externalChange {
			name = "stale proposal is refused"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newTestHub(t)
				h.push.store = newLnStore()
				h.askVia = nil
				mgr := playManager(t, h, func(context.Context, string, json.RawMessage) (any, error) { return map[string]any{}, nil })
				shown := make(chan proto.Interaction, 1)
				mgr.peer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
					if method != assistantwire.MethodFleetAskPrepared {
						return map[string]any{}, nil
					}
					var in struct {
						Interaction proto.Interaction `json:"interaction"`
					}
					if err := json.Unmarshal(params, &in); err != nil {
						return nil, err
					}
					shown <- in.Interaction
					return approval.Outcome{Clarifying: true, Text: approval.ClarifyingText}, nil
				}
				m := &Machine{cfg: MachineConfig{ID: "m1"}, hub: h}
				toolDone := make(chan struct {
					text string
					err  error
				}, 1)
				go func() {
					text, err := h.toolPersonaSettings(context.Background(), assistantwire.AssistantID, m, "s1",
						json.RawMessage(`{"action":"set","key":"notifications.low_distraction","value":true}`), nil)
					toolDone <- struct {
						text string
						err  error
					}{text, err}
				}()
				var ix proto.Interaction
				select {
				case ix = <-shown:
				case <-time.After(5 * time.Second):
					t.Fatal("the prepared setting card was not shown")
				}
				select {
				case got := <-toolDone:
					if got.err != nil || got.text != approval.ClarifyingText {
						t.Fatalf("clarified Hub tool = %q, %v", got.text, got.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("the Hub tool did not return for clarification")
				}
				if externalChange {
					// Another path turns the switch on after the off→on card was prepared.
					if err := h.push.setLnMode(context.Background(), true); err != nil {
						t.Fatal(err)
					}
				}
				allow := true
				params, _ := json.Marshal(assistantwire.FleetApprovalApply{InteractionID: ix.ID, Resolution: proto.Resolution{Allow: &allow}})
				raw, err := h.busPreparedSettingApply(context.Background(), &busConn{ident: db.BusIdentity{ID: assistantwire.AssistantID}}, params)
				if err != nil {
					t.Fatal(err)
				}
				out, ok := raw.(assistantwire.FleetApprovalApplyResult)
				if !ok {
					t.Fatalf("apply bus result = %#v", raw)
				}
				if externalChange {
					if !out.Stale || out.Text != "" || !h.push.lnModeOn() {
						t.Fatalf("stale Hub setting result = %+v, switch=%v", out, h.push.lnModeOn())
					}
				} else if out.Stale || !strings.Contains(out.Text, "true") || !h.push.lnModeOn() {
					t.Fatalf("fresh Hub setting result = %+v, switch=%v", out, h.push.lnModeOn())
				}
			})
		})
	}
}
