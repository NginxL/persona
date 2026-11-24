package extmgr

import (
	"context"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/botport"

	"github.com/nwverse/persona/server/internal/i18n"
)

// botConfigure retains only what the localized pairing hint needs, never the platform's raw error text.
// It belongs to the saved credentials and survives pairing-code renewal and manager restarts.
type botConfigure struct {
	Issue      botcard.ConfigureIssue `json:"issue,omitempty"`
	Configured bool                   `json:"configured,omitempty"`
}

var errBotSettingsSave = i18n.Err("hub-bots.settings_save_failed")

// botConfigureReceipt is private to the request that committed credentials. It is never sent or persisted.
type botConfigureReceipt struct {
	appID, secretEnc, domain string
	pairLifecycle            uint64
}

// botConfigureReceiptLocked captures a receipt while the successful settings commit still holds b.mu.
func (h *Manager) botConfigureReceiptLocked() botConfigureReceipt {
	b := &h.bots
	return botConfigureReceipt{b.set.AppID, b.set.SecretEnc, b.set.Domain, b.pairLifecycle}
}

func (r botConfigureReceipt) currentLocked(b *botHub) bool {
	return r.appID != "" && r.secretEnc != "" && b.set.AppID == r.appID && b.set.SecretEnc == r.secretEnc && b.set.Domain == r.domain && b.pairLifecycle == r.pairLifecycle && b.set.Owner == nil
}

// configureBotForPairing runs only for the exact saved receipt, before and after the remote call.
func (h *Manager) configureBotForPairing(ctx context.Context, receipt botConfigureReceipt) error {
	b := &h.bots
	b.mu.Lock()
	current := receipt.currentLocked(b)
	b.mu.Unlock()
	if !current {
		return nil
	}
	secret, err := h.openKey(receipt.secretEnc)
	if err != nil {
		return errBotSettingsSave
	}
	if !botport.Available() {
		return botport.ErrUnavailable
	}
	rep := botport.New(receipt.appID, secret, receipt.domain).AutoConfigure(ctx)
	if rep.Detail != "" {
		h.logf("bots: auto-configure: %s", rep.Detail)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !receipt.currentLocked(b) {
		return nil
	}
	previous := b.set.Configure
	b.set.Configure = nil
	if !rep.Skipped {
		b.set.Configure = &botConfigure{Issue: rep.Issue, Configured: rep.Configured}
	}
	if err := h.saveBotSettingsLocked(); err != nil {
		b.set.Configure = previous
		return errBotSettingsSave
	}
	return nil
}

// clearBotConfigureOnChangeLocked compares the actual commit-time credentials, not the snapshot taken before
// validation: another save may have committed while that network request was pending. b.mu is held.
func (h *Manager) clearBotConfigureOnChangeLocked(next FeishuBotSettings) {
	cur := &h.bots.set
	if cur.Configure == nil {
		return
	}
	if next.AppID == cur.AppID && next.Domain == cur.Domain {
		previous, previousErr := h.openKey(cur.SecretEnc)
		incoming, incomingErr := h.openKey(next.SecretEnc)
		if previousErr == nil && incomingErr == nil && previous == incoming {
			return
		}
	}
	cur.Configure = nil
}
