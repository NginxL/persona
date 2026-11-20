package devices

import (
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Quota to show in the usage sidebar can be much older than quota that may
// postpone work. Two ordinary collection intervals bound both observations.
const gatewayAdmissionFresh = 2 * usagePollDefault * time.Minute

// GatewayRetryAt returns the earliest plausible recovery of a precisely named
// source and provider, only when its complete, fresh inventory proves every
// enabled account is waiting on a known quota clock. It never polls a gateway.
func (d *Devices) GatewayRetryAt(source, provider string, now time.Time) time.Time {
	return gatewayRetryAt(d.usageGW.gatewayAccounts(), source, provider, now)
}

func gatewayRetryAt(inventory proto.GatewayAccounts, source, provider string, now time.Time) time.Time {
	var observation *proto.GatewaySource
	for i := range inventory.Sources {
		pool := &inventory.Sources[i]
		if pool.ID == source {
			if observation != nil {
				return time.Time{} // an ambiguous source cannot prove capacity
			}
			observation = pool
		}
	}
	if observation == nil || !observation.Complete || observation.Error != "" ||
		!recentGatewayObservation(observation.CheckedAt, now) {
		return time.Time{}
	}
	var soonest time.Time
	total := 0
	for _, account := range inventory.Accounts {
		if account.Source != source {
			continue
		}
		total++
		if account.Provider == "" && account.Status != "disabled" {
			return time.Time{} // an unclassified account could belong to this provider
		}
		if account.Provider != provider || account.Status == "disabled" {
			continue
		}
		if account.Schedulable || account.Stale || !recentGatewayObservation(account.CheckedAt, now) {
			return time.Time{}
		}
		recovery := quotaAccountRecovery(account, now)
		if recovery.IsZero() {
			return time.Time{}
		}
		if soonest.IsZero() || recovery.Before(soonest) {
			soonest = recovery
		}
	}
	if total != observation.Listed {
		return time.Time{}
	}
	return soonest
}

func recentGatewayObservation(at *time.Time, now time.Time) bool {
	return at != nil && !at.After(now) && now.Sub(*at) <= gatewayAdmissionFresh
}

// An account becomes available no earlier than its last active restriction.
// A model's own window never proves this whole account is unavailable.
func quotaAccountRecovery(account proto.GatewayAccount, now time.Time) time.Time {
	if account.Status != "cooldown" && account.Status != "exhausted" {
		return time.Time{}
	}
	var cooldown, quota time.Time
	if until := account.CooldownUntil; until != nil && until.After(now) {
		cooldown = *until
	}
	for _, window := range account.Windows {
		if window.Scope != "account" {
			continue
		}
		exhausted := window.UsedRatio != nil && *window.UsedRatio >= 1 ||
			window.Remaining != nil && *window.Remaining <= 0
		if !exhausted {
			continue
		}
		if window.ResetsAt == nil || !window.ResetsAt.After(now) {
			return time.Time{} // a stale or open-ended exhausted window is not a recovery clock
		}
		if window.ResetsAt.After(quota) {
			quota = *window.ResetsAt
		}
	}
	if account.Status == "cooldown" && cooldown.IsZero() || account.Status == "exhausted" && quota.IsZero() {
		return time.Time{}
	}
	if cooldown.After(quota) {
		return cooldown
	}
	return quota
}
