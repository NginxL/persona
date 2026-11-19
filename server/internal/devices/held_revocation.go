package devices

import (
	"context"

	"github.com/nwverse/persona/server/internal/db"
)

// heldCredentialRevoked records the event before doing store work so a concurrent delivery can see it.
func (d *Devices) heldCredentialRevoked(id string) {
	h := &d.held
	h.mu.Lock()
	if h.revoked == nil {
		h.revoked = make(map[string]bool)
	}
	h.revoked[id] = true
	h.mu.Unlock()
	ctx := context.Background()
	if d.heldLoad(ctx) {
		d.heldFailRevoked(ctx)
	}
}

// heldFailRevoked fails the rows that are safe to change now. The tick retries rows busy with a delivery,
// a user action or a store failure. A claimed delivery checks the hub itself before sending.
func (d *Devices) heldFailRevoked(ctx context.Context) {
	h := &d.held
	h.mu.Lock()
	if len(h.revoked) == 0 {
		h.mu.Unlock()
		return
	}
	var rows []db.HeldOperation
	for _, machineRows := range h.rows {
		for _, r := range machineRows {
			if h.revoked[r.CredentialID] && !r.busy && !r.stale && r.State != db.HeldSending && r.Failure != db.HeldSignedOut {
				r.busy = true
				rows = append(rows, r.HeldOperation)
			}
		}
	}
	h.mu.Unlock()
	if len(rows) == 0 {
		return
	}

	sctx, cancel := heldStoreCtx(ctx)
	defer cancel()
	st := d.heldStore()
	changed := make(map[string]bool)
	for _, row := range rows {
		ok, err := st.SettleHeldOperation(sctx, row.Seq, row.State, db.HeldFailed, db.HeldSignedOut, "")
		if err != nil {
			d.logf("fail held message from a signed-out device: %v", err)
		} else if !ok {
			d.heldSync(ctx, row.MachineID, row.Seq)
		}
		h.mu.Lock()
		if r := h.rowLocked(row.MachineID, row.Seq); r != nil {
			r.busy = false
			if ok {
				r.State, r.Failure, r.Error = db.HeldFailed, db.HeldSignedOut, ""
			}
		}
		h.mu.Unlock()
		if err == nil {
			changed[row.MachineID] = true
		}
	}
	for machine := range changed {
		d.heldChanged(machine)
		d.heldWake(machine)
	}
}
