package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Prepaid balances (#11825, docs/server/usage-windows.md "Prepaid balances"): an official DeepSeek account has no
// 5-hour or weekly rate-limit window; GET https://api.deepseek.com/user/balance reports how much money is left.
// The manager reads it for a launcher added in Persona, whose key it already holds sealed, and only at the provider's
// own origin (officialDeepSeekURL). A ~/.zshrc launcher's key stays on its computer, so it is never read here.

const (
	// deepseekBalancePath is the provider's read-only balance route, at the API origin rather than under /anthropic.
	deepseekBalancePath = "/user/balance"
	// balanceBodyMost bounds one answer; the documented shape is a few hundred bytes.
	balanceBodyMost = 64 << 10
	// balanceTimeout bounds one launcher's balance read.
	balanceTimeout = 10 * time.Second
)

// deepseekBalanceBody is the documented answer: the amount is a decimal string ("12.34"), never a float, so it is
// handed on exactly as the provider wrote it.
type deepseekBalanceBody struct {
	Available    bool `json:"is_available"`
	BalanceInfos []struct {
		Currency     string `json:"currency"`
		TotalBalance string `json:"total_balance"`
	} `json:"balance_infos"`
}

// LauncherBalances reads the balance of every official DeepSeek launcher added in Persona, one sample each. It never
// returns a key: the account shape has no field for one, and an error is only a code. known is false when the
// launcher list itself could not be read, so that the caller keeps what it had rather than treating every launcher as
// removed.
//
// A launcher keeps its own key, or one key per computer (machine_keys). The manager reads the first of those it can
// open, in machine-name order; the launcher's own key wins when it has one.
func (s *Service) LauncherBalances(ctx context.Context) (accounts []proto.UsageAccount, known bool) {
	stored, known := s.storedLaunchersKnown(ctx)
	if !known {
		return nil, false
	}
	machineKeys := s.launcherMachineKeys(ctx)
	seen := map[[sha256.Size]byte]bool{}
	for _, l := range stored {
		base, ok := officialDeepSeekURL(l.BaseURL)
		if !ok {
			continue
		}
		sealed, machine := l.KeySealed, ""
		if sealed == "" {
			keys := machineKeys[l.Name]
			sort.Slice(keys, func(i, j int) bool { return keys[i].Machine < keys[j].Machine })
			if len(keys) > 0 {
				sealed, machine = keys[0].KeySealed, keys[0].Machine
			}
		}
		if sealed == "" {
			continue // no key the manager may read: nothing to ask the provider with
		}
		name := strings.TrimSpace(l.DisplayName)
		if name == "" {
			name = l.Name
		}
		sample := proto.UsageAccount{Provider: "deepseek", Name: name, Ref: "balance:" + l.Name}
		key, err := s.openLauncherKey(ctx, sealed, l.Name, machine)
		if err != nil {
			sample.Problem = "rejected" // the key cannot be used: a reference that does not resolve, or one that does not open
			accounts = append(accounts, sample)
			continue
		}
		// Two launchers with one key are one account: one balance row, under the first launcher's name.
		digest := sha256.Sum256([]byte(key))
		if seen[digest] {
			continue
		}
		seen[digest] = true
		sample.Balance, sample.Problem = readDeepSeekBalance(ctx, base, key)
		accounts = append(accounts, sample)
	}
	return accounts, true
}

// launcherMachineKeys is the computers' own keys by launcher, for a launcher that has no key of its own. An unreadable
// list is no keys: the launcher's own key, when it has one, is still read.
func (s *Service) launcherMachineKeys(ctx context.Context) map[string][]machineKey {
	st := s.store()
	if st == nil {
		return nil
	}
	var keys []machineKey
	if _, err := memoSetting(ctx, st, launchersMachineKeysKey, &keys); err != nil {
		return nil
	}
	out := map[string][]machineKey{}
	for _, k := range keys {
		if k.KeySealed != "" {
			out[k.Launcher] = append(out[k.Launcher], k)
		}
	}
	return out
}

// readDeepSeekBalance asks the provider once; balance nil with a problem code says why (see proto.UsageAccount.Problem).
func readDeepSeekBalance(ctx context.Context, base *url.URL, key string) (*proto.UsageBalance, string) {
	u := *base
	u.Path, u.RawPath = deepseekBalancePath, ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "failed"
	}
	// The documented balance route takes the key as a Bearer token. The launcher added from the preset also launches
	// with x-api-key, so both are sent: only the official origin ever sees them.
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("x-api-key", key)
	req.Header.Set("Accept", "application/json")
	// Redirects are not followed: a redirect would take the launcher's key to another address.
	res, err := (&http.Client{Timeout: balanceTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(req)
	if err != nil {
		return nil, "unreachable"
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, "rejected"
	case http.StatusOK:
	default:
		return nil, "failed"
	}
	var body deepseekBalanceBody
	if err := json.NewDecoder(io.LimitReader(res.Body, balanceBodyMost)).Decode(&body); err != nil {
		return nil, "failed"
	}
	for _, info := range body.BalanceInfos {
		amount := strings.TrimSpace(info.TotalBalance)
		currency := strings.ToUpper(strings.TrimSpace(info.Currency))
		if validBalanceAmount(amount) && validCurrencyCode(currency) {
			return &proto.UsageBalance{Amount: amount, Currency: currency}, ""
		}
	}
	return nil, "failed" // an answer without a usable amount is not a zero
}

// validBalanceAmount keeps the provider's decimal string as it is: digits, at most one point, at most six places,
// and a finite non-negative value. It is not parsed into a float for display or comparison.
func validBalanceAmount(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" || len(frac) > 6 || (hasFrac && frac == "") || strings.Contains(frac, ".") {
		return false
	}
	for _, part := range []string{whole, frac} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	return err == nil && v >= 0
}

// validCurrencyCode is an ISO-style code as the provider writes it: three ASCII letters, upper-cased before display.
func validCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
