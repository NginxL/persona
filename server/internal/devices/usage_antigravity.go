package devices

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const antigravityUsageProvider = "antigravity"

func canonicalUsageProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "agy" {
		return antigravityUsageProvider
	}
	return provider
}

// The companion sends model groups in one account. An external source can send the same groups as separate
// accounts whose plan windows have no Model field.
func antigravityQuotaScope(a proto.UsageAccount, w proto.UsageWindow) string {
	if w.Model != "" {
		return antigravityModelScope(w.Model)
	}
	_, suffix, _ := strings.Cut(a.Name, "·")
	// The merged list numbers same-name accounts before the display response is assembled.
	suffix = strings.TrimSpace(suffix)
	if i := strings.LastIndexByte(suffix, ' '); i >= 0 {
		if n, err := strconv.Atoi(suffix[i+1:]); err == nil && n > 1 {
			suffix = suffix[:i]
		}
	}
	for _, name := range []string{w.Group, suffix} {
		if scope := antigravityModelScope(name); scope == "gemini" || scope == "claude/gpt" {
			return scope
		}
	}
	return ""
}

func antigravityModelScope(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "gemini models":
		return "gemini"
	case "claude and gpt models", "claude gpt", "claude_gpt":
		return "claude/gpt"
	}
	return name
}

type antigravityQuotaKey struct {
	scope   string
	minutes int
	reset   int64
}

func antigravityWindowKey(a proto.UsageAccount, w proto.UsageWindow, now time.Time) (antigravityQuotaKey, bool) {
	if w.ResetsAt == nil || !w.ResetsAt.After(now) {
		return antigravityQuotaKey{}, false
	}
	return antigravityQuotaKey{antigravityQuotaScope(a, w), w.WindowMinutes, w.ResetsAt.Unix()}, true
}

type usageReading struct {
	at    time.Time
	stale bool
}

func usageAccountReading(a proto.UsageAccount, now time.Time) usageReading {
	return usageReading{a.SampledAt, a.Stale || a.StaleAt != nil && !a.StaleAt.After(now)}
}

func (a usageReading) preferredTo(b usageReading) bool {
	if a.stale != b.stale {
		return !a.stale
	}
	return a.at.After(b.at)
}

// reconcileAntigravityUsage folds matching local windows into external display rows. The external row keeps its
// ID, window labels and extension details. The newest fresh reading wins, with the external reading winning ties.
// Keep the poll's accounts intact for the scheduler, which has its own gateway inventory.
func reconcileAntigravityUsage(accounts []proto.UsageAccount, now time.Time) []proto.UsageAccount {
	type match struct {
		account, window int
		reading         usageReading
	}
	byWindow := map[antigravityQuotaKey]*match{}
	for i, a := range accounts {
		if a.Provider != antigravityUsageProvider || a.Local || len(a.Sources) == 0 {
			continue
		}
		for j, w := range a.Windows {
			if key, ok := antigravityWindowKey(a, w, now); ok {
				if _, ambiguous := byWindow[key]; ambiguous {
					byWindow[key] = nil // matching resets on multiple seats cannot identify the local account
				} else {
					byWindow[key] = &match{i, j, usageAccountReading(a, now)}
				}
			}
		}
	}
	if len(byWindow) == 0 {
		return accounts
	}
	var out []proto.UsageAccount
	for i, a := range accounts {
		if a.Provider != antigravityUsageProvider || !a.Local || len(a.Sources) != 0 {
			continue
		}
		reading := usageAccountReading(a, now)
		kept := make([]proto.UsageWindow, 0, len(a.Windows))
		for _, w := range a.Windows {
			key, ok := antigravityWindowKey(a, w, now)
			m := byWindow[key]
			if !ok || m == nil {
				kept = append(kept, w)
				continue
			}
			if out == nil {
				out = slices.Clone(accounts)
			}
			remote := &out[m.account]
			if !remote.Local {
				remote.Local = true
				remote.Windows = slices.Clone(remote.Windows)
			}
			remote.Machines = uniqueSorted(slices.Concat(remote.Machines, a.Machines))
			if reading.preferredTo(m.reading) {
				remote.Windows[m.window].UsedPercent = w.UsedPercent
				m.reading = reading
				if reading.preferredTo(usageAccountReading(*remote, now)) {
					remote.SampledAt, remote.StaleAt, remote.Stale, remote.Problem = a.SampledAt, a.StaleAt, reading.stale, a.Problem
				}
			}
		}
		if len(kept) != len(a.Windows) {
			out[i].Windows = kept
		}
	}
	if out == nil {
		return accounts
	}
	out = slices.DeleteFunc(out, func(a proto.UsageAccount) bool {
		return a.Provider == antigravityUsageProvider && a.Local && len(a.Sources) == 0 && len(a.Windows) == 0 && !isBalanceSample(a)
	})
	sortUsageAccounts(out)
	return out
}
