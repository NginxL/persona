package db

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestAcceptIdleConversationSwitchFencesRealInput(t *testing.T) {
	t.Run("idle accepts", func(t *testing.T) {
		d, _ := historyDB(t)
		op := prepareLogicalSwitch(t, d)
		ctx := t.Context()
		if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
			t.Fatal(err)
		}
		accepted, err := d.AcceptIdleConversationSwitch(ctx, "root-machine", "root-chat", op.OperationID, 1)
		if err != nil || !accepted {
			t.Fatalf("idle acceptance: %v %v", accepted, err)
		}
		got, err := d.ConversationSwitch(ctx, "root-machine", "root-chat", op.OperationID)
		if err != nil || got == nil || got.Phase != "accepted" {
			t.Fatalf("outcome: %+v %v", got, err)
		}
		// Replaying the same stored batch item after a manager restart is harmless.
		accepted, err = d.AcceptIdleConversationSwitch(ctx, "root-machine", "root-chat", op.OperationID, 1)
		if err != nil || !accepted {
			t.Fatalf("accepted replay: %v %v", accepted, err)
		}
	})

	t.Run("pending input retains first-turn protocol", func(t *testing.T) {
		d, _ := historyDB(t)
		op := prepareLogicalSwitch(t, d)
		ctx := t.Context()
		if err := d.EnqueueConversationInput(ctx, "root-machine", "root-chat", "real-user-input", 1, json.RawMessage(`{"text":"continue"}`)); err != nil {
			t.Fatal(err)
		}
		if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
			t.Fatal(err)
		}
		accepted, err := d.AcceptIdleConversationSwitch(ctx, "root-machine", "root-chat", op.OperationID, 1)
		if err != nil || accepted {
			t.Fatalf("pending input was bypassed: %v %v", accepted, err)
		}
		got, err := d.ConversationSwitch(ctx, "root-machine", "root-chat", op.OperationID)
		if err != nil || got == nil || got.Phase != "active" {
			t.Fatalf("pending outcome: %+v %v", got, err)
		}
	})

	t.Run("epoch is fenced", func(t *testing.T) {
		d, _ := historyDB(t)
		op := prepareLogicalSwitch(t, d)
		ctx := t.Context()
		if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
			t.Fatal(err)
		}
		accepted, err := d.AcceptIdleConversationSwitch(ctx, "root-machine", "root-chat", op.OperationID, 2)
		if accepted || !errors.Is(err, ErrConversationEpoch) {
			t.Fatalf("stale epoch: %v %v", accepted, err)
		}
	})

	t.Run("activated operation cannot return to in doubt", func(t *testing.T) {
		d, _ := historyDB(t)
		op := prepareLogicalSwitch(t, d)
		ctx := t.Context()
		if err := d.ActivateConversationSegment(ctx, "root-machine", "root-chat", op.OperationID, 1); err != nil {
			t.Fatal(err)
		}
		err := d.ResolveConversationSwitch(ctx, "root-machine", "root-chat", op.OperationID, "in_doubt", "late preparation failure")
		if !errors.Is(err, ErrConversationEpoch) {
			t.Fatalf("late in-doubt resolution was not fenced: %v", err)
		}
		got, err := d.ConversationSwitch(ctx, "root-machine", "root-chat", op.OperationID)
		if err != nil || got == nil || got.Phase != "active" {
			t.Fatalf("activated operation regressed: %+v %v", got, err)
		}
	})
}
