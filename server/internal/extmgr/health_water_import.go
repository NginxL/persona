package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// WaterExtension reads only water's saved configuration and, on import, its state.
// A missing file is the extension's untouched empty state. Never run its loader:
// that loader prunes history and renames corrupt files instead of reporting them.
func (h *Manager) WaterExtension(ctx context.Context, readState bool) (*assistantwire.WaterExtension, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h.extensions == nil {
		return nil, nil
	}
	p := h.extensions.reg.Snapshot("water")
	if p == nil {
		return nil, nil
	}
	v := &assistantwire.WaterExtension{StorageKey: p.Record.StorageKey(), Version: p.Record.Version,
		Enabled: p.Record.Enabled, Config: append(json.RawMessage(nil), p.Record.Config...)}
	if readState {
		b, err := os.ReadFile(filepath.Join(h.extensions.reg.DataDir(p.Record.ID), "water.json"))
		if errors.Is(err, os.ErrNotExist) {
			b, err = []byte(`{}`), nil
		}
		if err != nil {
			return nil, errors.New("could not read the water extension's data file")
		}
		v.State = b
	}
	return v, nil
}

// DisableWaterExtension uses the same dependent checks and setByUser as Settings.
// The registry's change callback stops and joins the old process before returning.
func (h *Manager) DisableWaterExtension(w http.ResponseWriter, r *http.Request, storageKey string) bool {
	if h.extensions == nil {
		writeErrCode(w, http.StatusNotFound, "not_found", errors.New("water extension is not installed"))
		return false
	}
	p := h.extensions.reg.Snapshot("water")
	if p == nil || p.Record.StorageKey() != storageKey {
		writeErrCode(w, http.StatusConflict, "extension_changed", errors.New("water extension changed during import"))
		return false
	}
	_, ok := h.switchExtension(w, r, p, false, false, actorOr(r, "Health water import"))
	return ok
}
