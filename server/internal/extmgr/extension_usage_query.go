package extmgr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/quota"
)

// The usage extension point (docs/extensions/extensions.md "Usage"): an extension contributing usage answers usage.query with the
// accounts and windows the usage bars draw (usagesource.ExtensionAnswer), reading the launcher's gateway through
// usage.http. Like quota.http, those requests are taken only while the query runs, from the extension it was asked of,
// and go out through whoever holds the launcher's key (launcherSender): the extension never sees the key.

// UsageQuery is how a usage source of type extension is read (devices.Options.UsageExtension): it asks extension id
// about the gateway of launcher and returns its answer as JSON, for usagesource.FromExtension to check.
func (h *Manager) UsageQuery(ctx context.Context, id, launcher string) (json.RawMessage, error) {
	if h == nil || h.extensions == nil {
		return nil, errors.New("no extensions")
	}
	p := h.extensions.reg.Get(id)
	if p == nil || !p.Record.Enabled || p.Problem != "" || !p.Record.Granted.LauncherKeys {
		return nil, errors.New("extension " + id + " cannot read usage")
	}
	hp := h.extensions.get(id)
	if hp == nil || hp.proc == nil {
		return nil, errors.New("extension " + id + " is not running")
	}
	gw, err := h.launcherGateway(ctx, launcher, "")
	if err != nil {
		return nil, err
	}
	root, err := quota.Root(gw.base)
	if err != nil {
		return nil, err
	}
	var item *extension.UsageItem
	for _, u := range p.Manifest.Usages() {
		if len(u.Hosts) == 0 || hostMatches(root.Hostname(), u.Hosts) {
			item = &u
			break
		}
	}
	if item == nil {
		return nil, errors.New("extension " + id + " does not read this gateway")
	}
	var b [12]byte
	_, _ = rand.Read(b[:])
	qid := hex.EncodeToString(b[:])
	quotaQueries.Store(qid, &quotaQuery{extension: id, send: gw.send})
	defer quotaQueries.Delete(qid)
	params, _ := extension.Targeted(map[string]string{"query_id": qid, "base_url": root.String(), "tz": h.userTZName(), "lang": string(h.contextLang(ctx))}, item.ID)
	var res json.RawMessage
	if err := hp.proc.Call(ctx, "usage.query", params, &res); err != nil {
		return nil, err
	}
	return res, nil
}
