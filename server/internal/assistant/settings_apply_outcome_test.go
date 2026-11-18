package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestSettingRebuildValidationFailureIsDefinitelyNotApplied(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	var builds, applies atomic.Int32
	setting := ownSetting{Setting: Setting{Key: "assistant.notes", Title: "Notes"}, set: func(context.Context, json.RawMessage) (*ownChange, error) {
		if builds.Add(1) == 1 {
			return &ownChange{summary: "Notes: before → after", apply: func(context.Context) (string, error) {
				applies.Add(1)
				return "unexpected", nil
			}}, nil
		}
		return nil, errors.New("the current value no longer validates")
	}}
	done := make(chan struct {
		text string
		err  error
	}, 1)
	go func() {
		text, err := ft.s.ownSettingAction(context.Background(), ToolCall{MachineID: "mac", SessionID: "s1"}, setting, "set", json.RawMessage(`"after"`))
		done <- struct {
			text string
			err  error
		}{text, err}
	}()
	cards := ft.waitPending(1)
	allow := true
	if err := ft.s.resolve(cards[0].ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !strings.Contains(got.text, "Nothing was applied") || strings.Contains(got.text, "uncertain") {
			t.Fatalf("pre-apply validation outcome = %q, %v", got.text, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the validated setting approval did not finish")
	}
	if builds.Load() != 2 || applies.Load() != 0 {
		t.Fatalf("builds=%d applies=%d, want a rebuilt validation and no execution", builds.Load(), applies.Load())
	}
}

func TestPreparedApplyFailureRemainsUncertain(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ft.f.mu.Lock()
	ft.f.known["s1"] = true
	ft.f.mu.Unlock()
	done := startPreparedTest(t, ft, "ix_apply_uncertain", "", func(context.Context, proto.Resolution) (string, error) {
		return "", errors.New("the write may have reached the disk")
	})
	if err := ft.s.clarify(context.Background(), "ix_apply_uncertain", "Why?", "owner-apply-uncertain", nil); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	allow := true
	if err := ft.s.resolve("ix_apply_uncertain", proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	result := waitApprovalResult(t, ft, "ix_apply_uncertain")
	if !strings.Contains(result, "uncertain") || !strings.Contains(result, "do not assume it applied or that nothing changed") {
		t.Fatalf("genuine apply failure was misreported: %q", result)
	}
}
