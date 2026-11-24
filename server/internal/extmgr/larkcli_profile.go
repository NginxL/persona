package extmgr

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/cliwire"

	"github.com/nwverse/persona/server/internal/connector"
)

const managedLarkCLIProfile = "persona"

// Scheduling never waits on the registry/connector locks: a CLI callback may
// hold its own Inbox lock. Only one recovery runs, with a cooldown after failure.
type larkCLIRecovery struct {
	needed            atomic.Bool
	running           atomic.Bool
	after             atomic.Int64
	preferencesUnread atomic.Bool
	publishing        atomic.Bool
}

func (h *Manager) deferLarkCLIRecovery() {
	h.larkCLIRecovery.after.Store(time.Now().Add(botSettingsRetry).UnixNano())
	h.larkCLIRecovery.needed.Store(true)
}

func (h *Manager) requestLarkCLIRecovery(ctx context.Context) {
	if h == nil || h.cfg == nil || h.cfg.Lark.CLI == nil || !h.larkCLIRecovery.needed.Load() {
		return
	}
	if h.ctx != nil {
		ctx = h.ctx // recovery belongs to the manager, not a completed HTTP request
	}
	if ctx == nil || ctx.Err() != nil || time.Now().UnixNano() < h.larkCLIRecovery.after.Load() ||
		!h.larkCLIRecovery.running.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer h.larkCLIRecovery.running.Store(false)
		_ = h.refreshLarkCLIConnector(ctx)
	}()
}

// CLI invocation needs both its account and the independent connector choices.
// An explicit manager profile identifies the account, but cannot enable a
// connector whose durable enabled/feed choices are still unread.
func (h *Manager) larkCLIExecutionProfile(ctx context.Context) (string, error) {
	if h.larkCLIRecovery.preferencesUnread.Load() || h.larkCLIRecovery.publishing.Load() {
		h.requestLarkCLIRecovery(ctx)
		return "", errBotSettingsUnread
	}
	profile, err := h.resolveLarkCLIProfile(ctx)
	if err != nil {
		return "", err
	}
	if profile == managedLarkCLIProfile && h.configuredLarkCLIProfile() == "" && h.larkCLIRecovery.needed.Load() {
		h.requestLarkCLIRecovery(ctx)
		return "", errBotSettingsUnread
	}
	return profile, nil
}

type larkCLIBotView struct {
	Profile  string               `json:"profile"`
	AppID    string               `json:"app_id,omitempty"`
	SameApp  bool                 `json:"same_app"`
	Mode     string               `json:"mode"`
	Choice   string               `json:"choice"`
	Profiles []larkCLIProfileView `json:"profiles"`
}

type larkCLIProfileView struct {
	Name        string `json:"name"`
	AppID       string `json:"app_id"`
	User        string `json:"user"`
	TokenStatus string `json:"token_status"`
}

// effectiveLarkCLIProfile is the one profile the manager gives to both the
// connector and assistant.lark. An explicit profile remains an escape hatch;
// otherwise a bot app gets the managed profile and no bot falls back to the
// CLI's default profile.
func (h *Manager) effectiveLarkCLIProfile(ctx context.Context) string {
	profile, _ := h.resolveLarkCLIProfile(ctx)
	return profile
}

// resolveLarkCLIProfile distinguishes a known default profile from unread settings.
// The manager.json override already identifies the account and remains available during a store outage.
func (h *Manager) resolveLarkCLIProfile(ctx context.Context) (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Lark.CLI == nil {
		return "", nil
	}
	if profile := strings.TrimSpace(h.cfg.Lark.CLI.Profile); profile != "" {
		return profile, nil
	}
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := h.loadBotsLocked(ctx); err != nil {
		return "", err
	}
	if profile := strings.TrimSpace(b.set.LarkCLIProfile); profile != "" {
		return profile, nil
	}
	if b.set.AppID != "" && b.set.SecretEnc != "" || h.cfg.Bots.Feishu.AppID != "" && h.cfg.Bots.Feishu.AppSecret != "" {
		return managedLarkCLIProfile, nil
	}
	return "", nil
}

// larkCLIProfileChoice is the app's setting. It lives with the bot settings
// in the manager store, not in manager.json, and is intentionally separate
// from the effective profile selected by the precedence rules.
func (h *Manager) larkCLIProfileChoice(ctx context.Context) string {
	if h == nil {
		return ""
	}
	b := &h.bots
	b.mu.Lock()
	h.loadBotsLocked(ctx)
	choice := strings.TrimSpace(b.set.LarkCLIProfile)
	b.mu.Unlock()
	return choice
}

func (h *Manager) configuredLarkCLIProfile() string {
	if h == nil || h.cfg == nil || h.cfg.Lark.CLI == nil {
		return ""
	}
	return strings.TrimSpace(h.cfg.Lark.CLI.Profile)
}

func (h *Manager) larkCLIMode(ctx context.Context) string {
	switch {
	case h.configuredLarkCLIProfile() != "":
		return "config"
	case h.larkCLIProfileChoice(ctx) != "":
		return "chosen"
	case h.botAppCredentialsID(ctx) != "":
		return "bot"
	default:
		return "default"
	}
}

func (h *Manager) botAppCredentials(ctx context.Context) (appID, secret, domain string) {
	if h == nil || h.cfg == nil {
		return "", "", ""
	}
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := h.loadBotsLocked(ctx); err != nil {
		return "", "", ""
	}
	appID, secret, domain, _, err := h.botCredentialsLocked()
	if err != nil {
		// The app id is still safe and authoritative for profile selection even
		// when the sealed secret cannot be opened; sync will report the failure
		// without exposing it.
		return appID, "", domain
	}
	return appID, secret, domain
}

func (h *Manager) botAppCredentialsID(ctx context.Context) string {
	id, _, _ := h.botAppCredentials(ctx)
	return id
}

// syncLarkCLIProfile binds the managed profile to the bot app without ever
// putting the app secret in argv or a returned error. It is intentionally a
// no-op for an explicit profile and for a manager without bot credentials.
func (h *Manager) syncLarkCLIProfile(ctx context.Context) error {
	if _, err := h.resolveLarkCLIProfile(ctx); err != nil {
		return err
	}
	if h == nil || h.cfg == nil || h.cfg.Lark.CLI == nil || h.configuredLarkCLIProfile() != "" || h.larkCLIProfileChoice(ctx) != "" {
		return nil
	}
	appID, secret, domain := h.botAppCredentials(ctx)
	if appID == "" || secret == "" {
		return nil
	}
	cli := h.cfg.Lark.CLI
	show := cliport.NewRunner(cliwire.Config{Bin: cli.Bin, Profile: managedLarkCLIProfile, Timeout: 15 * time.Second})
	showCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	current, err := show.ConfigShow(showCtx)
	cancel()
	if err == nil && current.AppID == appID {
		return nil
	}
	brand := larkCLIBrand(domain)
	initRunner := cliport.NewRunner(cliwire.Config{Bin: cli.Bin, Timeout: 15 * time.Second})
	initCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = initRunner.InitProfile(initCtx, managedLarkCLIProfile, appID, secret, brand)
	cancel()
	if err != nil {
		return err
	}
	return nil
}

// validateLarkCLIProfile checks a user-selected profile against the CLI's
// read-only profile list. Empty means automatic selection.
func (h *Manager) validateLarkCLIProfile(ctx context.Context, choice string) error {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return nil
	}
	if h == nil || h.cfg == nil || h.cfg.Lark.CLI == nil {
		return fmt.Errorf("lark-cli is not configured")
	}
	cli := h.cfg.Lark.CLI
	profile, err := h.larkCLIExecutionProfile(ctx)
	if err != nil {
		return err
	}
	r := cliport.NewRunner(cliwire.Config{Bin: cli.Bin, Profile: profile, Timeout: 5 * time.Second, ResolveProfile: h.larkCLIExecutionProfile})
	profiles, err := r.Profiles(ctx)
	if err != nil {
		return err
	}
	for _, profile := range profiles {
		if profile.Name == choice && choice != managedLarkCLIProfile {
			return nil
		}
	}
	return fmt.Errorf("lark-cli profile %q does not exist", choice)
}

// setLarkCLIProfile persists the app choice and rebuilds the connector. The
// registry keeps enabled/feed switches by connector id, so Inbox wiring stays
// attached to the replacement.
func (h *Manager) setLarkCLIProfile(ctx context.Context, choice string) error {
	choice = strings.TrimSpace(choice)
	b := &h.bots
	b.mu.Lock()
	if err := h.loadBotsLocked(ctx); err != nil {
		b.mu.Unlock()
		return err
	}
	if b.set.LarkCLIProfile == choice {
		b.mu.Unlock()
		return nil
	}
	b.set.LarkCLIProfile = choice
	h.saveBotSettingsLocked()
	b.mu.Unlock()
	if err := h.syncLarkCLIProfile(ctx); err != nil {
		h.logf("larkcli profile sync: %v", err)
	}
	h.refreshLarkCLIConnector(ctx)
	h.kickInbox()
	return nil
}

func larkCLIBrand(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "lark" || d == "larksuite" || strings.Contains(d, "larksuite.com") {
		return "lark"
	}
	return "feishu"
}

// newLarkCLIConnector builds the connector for an effective profile with its
// Inbox wiring (state store, gateway preference), so a rebuild after a profile
// change keeps the chat Inbox working.
func (h *Manager) newLarkCLIConnector(profile string) cliport.Connector {
	cli := h.cfg.Lark.CLI
	var lc cliport.Connector
	lc = cliport.NewConnector(cliwire.Config{Bin: cli.Bin, Profile: profile,
		InboxPoll: cli.InboxPoll(), InboxPageLimit: cli.InboxPageLimit,
		ResolveProfile: func(ctx context.Context) (string, error) {
			if h.connectors.Get(cliwire.ConnectorID) != lc {
				return "", errBotSettingsUnread // a retired instance never owns current-account calls
			}
			current, err := h.larkCLIExecutionProfile(ctx)
			if err != nil {
				return "", err
			}
			if current != profile {
				// Retained callers must not use the old connector's Inbox cursor
				// and caches for the recovered account. The replacement owns them.
				if live, ok := h.connectors.Get(cliwire.ConnectorID).(cliport.Connector); ok && live.Profile() != current {
					h.larkCLIRecovery.needed.Store(true)
					h.requestLarkCLIRecovery(ctx)
				}
				return "", errBotSettingsUnread
			}
			return current, nil
		}})
	lc.SetStateStore(h.store)
	lc.SetInboxSource(h.cfg.Lark.Prefer, func(ctx context.Context) bool {
		gateway := h.connectors.Get(connector.Lark)
		if gateway == nil {
			return false
		}
		return connector.StatusOf(ctx, gateway).State == connector.StateOK
	})
	return lc
}

// refreshLarkCLIConnector applies a profile change to the live registry. The
// registry's Remove/Add pair preserves the user's enabled switch and cancels a
// pending login through the connector lifecycle.
func (h *Manager) refreshLarkCLIConnector(ctx context.Context) error {
	return h.refreshLarkCLIConnectorAtPublication(ctx, nil)
}

// beforeActivation observes the published replacement before its instance switch is applied.
// Production passes nil; integration tests use this boundary for concurrent Registry.Call.
func (h *Manager) refreshLarkCLIConnectorAtPublication(ctx context.Context, beforeActivation func()) (err error) {
	if h == nil || h.cfg == nil || h.cfg.Lark.CLI == nil || h.connectors == nil {
		return nil
	}
	h.larkCLIRefreshMu.Lock()
	defer h.larkCLIRefreshMu.Unlock()
	defer func() {
		if err != nil {
			h.deferLarkCLIRecovery()
		} else {
			h.larkCLIRecovery.after.Store(0)
			h.larkCLIRecovery.needed.Store(false)
		}
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	profile, err := h.resolveLarkCLIProfile(ctx)
	if err != nil {
		return err
	}
	existing := h.connectors.Get(cliwire.ConnectorID)
	if existing == nil {
		return errBotSettingsUnread
	}
	unread := h.larkCLIRecovery.preferencesUnread.Load()
	enabled := h.connectors.Enabled(cliwire.ConnectorID)
	var cs connectorSettings
	if unread {
		cs, err = h.readConnectorSettings(ctx)
		if err != nil {
			return err
		}
		enabled = h.connectorOn(cs, cliwire.ConnectorID)
	}
	if err = h.syncLarkCLIProfile(ctx); err != nil {
		h.logf("larkcli profile sync: %v", err)
		return err
	}
	same := false
	if c, ok := existing.(cliport.Connector); ok {
		same = c.Profile() == profile
	}
	if same && !unread {
		return nil
	}
	next := h.newLarkCLIConnector(profile)
	if unread {
		if raw, ok := cs.Options[cliwire.ConnectorID]; ok {
			if err = next.SetOptions(raw); err != nil {
				return err
			}
		}
	} else if options, ok := connector.As[connector.Options](existing); ok {
		if err = next.SetOptions(options.Options()); err != nil {
			return err
		}
	}
	// Unknown preferences were quarantined before Start. Keep that fence until
	// the replacement has its durable options and feed choices, then restore on/off.
	h.larkCLIRecovery.publishing.Store(true)
	defer h.larkCLIRecovery.publishing.Store(false)
	// Keep Add from starting a watcher before the new instance has its switch.
	if err = h.connectors.SetEnabled(cliwire.ConnectorID, false); err != nil {
		return err
	}
	if unread {
		on, chosen := cs.Inbox[cliwire.ConnectorID]
		if err = h.connectors.SetFeed(cliwire.ConnectorID, connector.FeedInbox, !chosen || on); err != nil {
			return err
		}
	}
	h.connectors.Remove(cliwire.ConnectorID)
	h.larkCLIRecovery.needed.Store(false) // synchronized before Add can start the new watcher
	if err = h.addConnector(next); err != nil {
		return err
	}
	if beforeActivation != nil {
		beforeActivation()
	}
	// Reapply off to the new pointer as well as preserving the registry's ID
	// switch: StatusOf uses the connector instance's disabled mark.
	if err = h.connectors.SetEnabled(cliwire.ConnectorID, false); err != nil {
		return err
	}
	if unread {
		h.larkCLIRecovery.preferencesUnread.Store(false)
	}
	h.larkCLIRecovery.publishing.Store(false)
	if enabled {
		return h.connectors.SetEnabled(cliwire.ConnectorID, true)
	}
	return nil
}

// larkCLIView is the small binding summary consumed by the bot settings page.
// The app id is read from the selected profile's non-secret auth status, then
// compared with the bot app id held by the manager.
func (h *Manager) larkCLIView(ctx context.Context, botAppID string) *larkCLIBotView {
	if h == nil || h.cfg == nil || h.cfg.Lark.CLI == nil {
		return nil
	}
	profile, err := h.larkCLIExecutionProfile(ctx)
	if err != nil {
		return nil
	}
	choice := h.larkCLIProfileChoice(ctx)
	appID := ""
	cli := h.cfg.Lark.CLI
	r := cliport.NewRunner(cliwire.Config{Bin: cli.Bin, Profile: profile, Timeout: 5 * time.Second, ResolveProfile: h.larkCLIExecutionProfile})
	if auth, err := r.AuthStatus(ctx); err == nil {
		appID = auth.AppID
	}
	profiles, err := r.Profiles(ctx)
	if err != nil {
		profiles = nil
	}
	viewProfiles := make([]larkCLIProfileView, 0, len(profiles))
	for _, p := range profiles {
		// The managed profile is what "follow the bot's app" (choice "") uses: listing it again would offer the
		// same thing twice under an internal name.
		if p.Name == managedLarkCLIProfile {
			continue
		}
		viewProfiles = append(viewProfiles, larkCLIProfileView{Name: p.Name, AppID: p.AppID, User: p.User, TokenStatus: p.TokenStatus})
	}
	return &larkCLIBotView{
		Profile:  profile,
		AppID:    appID,
		SameApp:  botAppID != "" && appID != "" && botAppID == appID,
		Mode:     h.larkCLIMode(ctx),
		Choice:   choice,
		Profiles: viewProfiles,
	}
}
