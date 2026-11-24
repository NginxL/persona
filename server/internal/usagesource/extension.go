package usagesource

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// TypeExtension is a source an extension reads (contributes.usage, docs/extensions/extensions.md "Usage"): the manager asks the
// extension (usage.query), which sends its requests to a launcher's gateway through usage.http and answers with the
// accounts and windows to draw. It has no address and no key of its own: the launcher has both, and the key stays with
// whoever holds it.
const TypeExtension = "extension"

// The most text an extension's lines may have, in characters.
const (
	extNameMost  = 80
	extShortMost = 40
	extLineMost  = 200
)

var extProvider = lazyre.New(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ExtensionAnswer is what an extension answers usage.query with.
type ExtensionAnswer struct {
	Accounts []ExtensionAccount `json:"accounts"`
}

// ExtensionAccount is one row of the usage bars as an extension describes it. An account with no window may still have a
// Notice (why there is nothing to draw).
type ExtensionAccount struct {
	ID        string            `json:"id"`
	Provider  string            `json:"provider"`
	Name      string            `json:"name"`
	Plan      string            `json:"plan"`
	Headline  string            `json:"headline"`
	Notice    string            `json:"notice"`
	SampledAt *time.Time        `json:"sampled_at"`
	Windows   []ExtensionWindow `json:"windows"`
}

// ExtensionWindow is one window. Group, Title and Note are the extension's own words; the windows of a group are
// kept in the order the extension gave them.
type ExtensionWindow struct {
	Label         string     `json:"label"`
	WindowMinutes int        `json:"window_minutes"`
	UsedPercent   *float64   `json:"used_percent"`
	ResetsAt      *time.Time `json:"resets_at"`
	Model         string     `json:"model"`
	Group         string     `json:"group"`
	Title         string     `json:"title"`
	Note          string     `json:"note"`
	// ProtectPercent is the gateway's protection line for this window, in percent (0-100); omitted when unknown.
	ProtectPercent *float64 `json:"protect_percent"`
}

// FromExtension turns an extension's answer into accounts the sidebar draws as they are (Pass: they are not merged with
// what the computers read). What does not fit is left out, never repaired into something else: an account without a
// provider name, one with neither a window nor a line to say, a window without a length or a number, and whatever is
// beyond AccountsMost and WindowsMost. Text is plain and cut to length; a percentage is clamped to 0-100.
func FromExtension(raw json.RawMessage, now time.Time) (Result, error) {
	var in ExtensionAnswer
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{}, &Error{Code: CodeBadResponse, Detail: "not the expected JSON"}
	}
	res := Result{Seen: Seen{Providers: map[string]int{}, Listed: len(in.Accounts)}}
	for i, a := range in.Accounts {
		if i >= AccountsMost {
			break
		}
		provider := strings.ToLower(strings.TrimSpace(a.Provider))
		if !extProvider.MatchString(provider) {
			continue
		}
		var wins []proto.UsageWindow
		for j, w := range a.Windows {
			if j >= WindowsMost || w.UsedPercent == nil {
				continue
			}
			minutes := w.WindowMinutes
			if m, ok := KindMinutes(w.Label); ok {
				minutes = m
			}
			used, ok := clampPercent(*w.UsedPercent)
			if !ok || minutes <= 0 {
				continue
			}
			protect := protectLine(w.ProtectPercent)
			wins = append(wins, proto.UsageWindow{Label: proto.WindowLabel(minutes), WindowMinutes: minutes, UsedPercent: used,
				ResetsAt: utc(w.ResetsAt), Model: clip(plain(w.Model), extShortMost),
				Group: clip(plain(w.Group), extShortMost), Title: clip(plain(w.Title), extShortMost), Note: clip(plain(w.Note), extLineMost), ProtectPercent: protect})
		}
		headline, notice := clip(plain(a.Headline), extLineMost), clip(plain(a.Notice), extLineMost)
		if len(wins) == 0 && notice == "" && headline == "" {
			continue
		}
		sampled := now
		if a.SampledAt != nil && !a.SampledAt.IsZero() {
			sampled = notAfter(a.SampledAt.UTC(), now)
		}
		name := clip(plain(a.Name), extNameMost)
		if name == "" {
			name = provider
		}
		res.Accounts = append(res.Accounts, proto.UsageAccount{Provider: provider, Name: name, Plan: clip(plain(a.Plan), extShortMost),
			Headline: headline, Notice: notice, Windows: wins, SampledAt: sampled, Ref: strings.TrimSpace(a.ID), Pass: true})
		res.Seen.Accounts++
		res.Seen.Windows += len(wins)
		res.Seen.Providers[provider]++
	}
	return res, nil
}

// protectLine is an extension's protection line when it is a number from 0 to 100; anything else (absent, NaN, out of
// range) is "not said", never repaired into a value, so a client draws no marker rather than a wrong one.
func protectLine(p *float64) *float64 {
	if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0 || *p > 100 {
		return nil
	}
	v := *p
	return &v
}

// plain is s as one line of text: trimmed, with line breaks and other control characters turned into spaces.
func plain(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)), " ")
}
