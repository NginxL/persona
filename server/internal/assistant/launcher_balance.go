package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"

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
)

var (
	// balanceTimeout bounds one launcher's balance read (a var for the tests).
	balanceTimeout = 10 * time.Second
	// balanceReadWorkers bounds concurrent balance HTTP reads in one poll (a var for the tests).
	balanceReadWorkers = 4
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
// launcher list or the computers' key store could not be read, so that the caller keeps what it had rather than
// treating every launcher as removed.
//
// A launcher keeps a global key, per-computer keys (machine_keys), or both. Per-computer keys take precedence over
// the global key, matching keySealedFor: when ViaMachine names the routed machine, its effective key from
// keySealedFor is used; otherwise in-scope per-computer keys are tried first in machine-name order, falling back to
// the global key.
func (s *Service) LauncherBalances(ctx context.Context) (accounts []proto.UsageAccount, known bool) {
	stored, known := s.storedLaunchersKnown(ctx)
	if !known {
		return nil, false
	}
	machineKeys, err := s.launcherMachineKeys(ctx)
	if err != nil {
		// A store that could not be read is not "every computer's key is gone": keep what the caller had. Without
		// this, launchers that rely on a per-computer key would be omitted and the caller would drop their samples.
		return nil, false
	}
	if ctx.Err() != nil {
		return nil, false
	}
	type balanceReadJob struct {
		index  int
		base   *url.URL
		key    string
		client *http.Client
	}
	seen := map[[sha256.Size]byte]bool{}   // openable keys, by the key itself
	broken := map[[sha256.Size]byte]bool{} // sealed material that could not be opened, already reported
	var jobs []balanceReadJob
	for _, l := range stored {
		base, ok := officialDeepSeekURL(l.BaseURL)
		if !ok {
			continue
		}
		candidates := s.launcherKeyCandidates(l, machineKeys[l.Name])
		if len(candidates) == 0 {
			continue // no key the manager may read: nothing to ask the provider with
		}
		name := strings.TrimSpace(l.DisplayName)
		if name == "" {
			name = l.Name
		}
		sealed := make([]string, 0, len(candidates))
		for _, c := range candidates {
			sealed = append(sealed, c.sealed)
		}
		sample := proto.UsageAccount{
			Provider:                "deepseek",
			Name:                    name,
			Ref:                     "balance:" + l.Name,
			BalanceSealedCredential: sealedCredential(sealed...),
		}
		// The first key that opens wins: a corrupt or unresolvable key on the first computer must not hide a usable
		// one on the next.
		key := ""
		for _, c := range candidates {
			k, err := s.openLauncherKey(ctx, c.sealed, l.Name, c.machine)
			if err == nil && k != "" {
				key = k
				break
			}
			if ctx.Err() != nil {
				return nil, false
			}
		}
		if key == "" {
			// Every key this launcher may use failed before any request: a local credential problem (a sealed value
			// that does not open, an extension that is gone, a reference that no longer resolves), not the provider
			// turning a key away. One row per broken key, so two launchers sharing one emit it once.
			fresh := false
			digests := make([][sha256.Size]byte, 0, len(candidates))
			for _, c := range candidates {
				d := sha256.Sum256([]byte(c.sealed))
				digests = append(digests, d)
				if !broken[d] {
					fresh = true
				}
			}
			for _, d := range digests {
				broken[d] = true
			}
			if !fresh {
				continue
			}
			sample.Problem = "failed"
			// Leave BalanceCredential empty so usageBalances compares BalanceSealedCredential instead: unchanged
			// sealed material keeps the last known amount marked stale, while genuinely replaced sealed material
			// clears it.
			accounts = append(accounts, sample)
			continue
		}
		// Two launchers with one key are one account: one balance row, under the first launcher's name.
		// Derive BalanceCredential from the resolved key so rotating an op:// (or other) secret reference changes the
		// credential identity even though the sealed reference bytes stay constant.
		digest := sha256.Sum256([]byte(key))
		if seen[digest] {
			continue
		}
		seen[digest] = true
		sample.BalanceCredential = hex.EncodeToString(digest[:])
		client, err := s.balanceClient(l)
		if err != nil {
			sample.Problem = "failed"
			accounts = append(accounts, sample)
			continue
		}
		accounts = append(accounts, sample)
		jobs = append(jobs, balanceReadJob{index: len(accounts) - 1, base: base, key: key, client: client})
	}
	if len(jobs) > 0 {
		workers := min(max(balanceReadWorkers, 1), len(jobs))
		work := make(chan balanceReadJob, len(jobs))
		for _, job := range jobs {
			work <- job
		}
		close(work)
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				for job := range work {
					if ctx.Err() != nil {
						accounts[job.index].BalanceSkipped = true
						continue
					}
					bal, problem, skipped := readDeepSeekBalance(ctx, job.base, job.key, job.client)
					if skipped {
						accounts[job.index].BalanceSkipped = true
						continue
					}
					accounts[job.index].Balance, accounts[job.index].Problem = bal, problem
				}
			}()
		}
		wg.Wait()
	}
	return accounts, true
}

// sealedCredential is the non-secret digest of a launcher's sealed key material, tracked separately from the
// resolved-key digest (BalanceCredential) so a temporary open/resolution failure of unchanged sealed material retains
// the last known balance while genuinely replaced sealed material clears it.
func sealedCredential(sealed ...string) string {
	h := sha256.New()
	for _, s := range sealed {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return "sealed:" + hex.EncodeToString(h.Sum(nil))
}

// launcherKeyCandidate is one sealed key the manager may read for a launcher: its own, or one a computer holds.
type launcherKeyCandidate struct {
	sealed  string
	machine string // "" for the launcher's own key
}

// launcherKeyCandidates returns the sealed key(s) to try for l, honoring keySealedFor's precedence (a computer's own
// key overrides the launcher's global key):
//   - When ViaMachine names a routed machine, keySealedFor selects that machine's effective key (its per-computer key
//     when present, falling back to l.KeySealed only when that machine has none); if empty, returns nil so the
//     launcher is skipped instead of borrowing another computer's key.
//   - When the launcher is not shared to any computer (shareNone), only the launcher's own global key applies.
//   - When the launcher is scoped to selected computers (shareSelected), only in-scope computers' keys are considered
//     via keySealedFor, with per-computer overrides ordered before the global fallback.
//   - Otherwise, per-computer keys are tried first in sorted machine-name order, with l.KeySealed as fallback.
func (s *Service) launcherKeyCandidates(l storedLauncher, machineKeys []machineKey) []launcherKeyCandidate {
	if via := strings.TrimSpace(l.ViaMachine); via != "" {
		sealed := s.keySealedFor(l, machineKeys, via)
		if sealed == "" {
			return nil
		}
		return []launcherKeyCandidate{{sealed: sealed, machine: via}}
	}
	if l.Share == shareNone {
		if l.KeySealed == "" {
			return nil
		}
		return []launcherKeyCandidate{{sealed: l.KeySealed}}
	}
	if l.Share == shareSelected {
		machines := slices.Clone(l.ScopeMachines)
		sort.Strings(machines)
		var overrides, fallbacks []launcherKeyCandidate
		seen := map[string]bool{}
		for _, m := range machines {
			sealed := s.keySealedFor(l, machineKeys, m)
			if sealed == "" || seen[sealed] {
				continue
			}
			seen[sealed] = true
			if l.KeySealed != "" && sealed == l.KeySealed {
				fallbacks = append(fallbacks, launcherKeyCandidate{sealed: sealed, machine: m})
			} else {
				overrides = append(overrides, launcherKeyCandidate{sealed: sealed, machine: m})
			}
		}
		return append(overrides, fallbacks...)
	}
	keys := slices.Clone(machineKeys)
	sort.Slice(keys, func(i, j int) bool { return keys[i].Machine < keys[j].Machine })
	out := make([]launcherKeyCandidate, 0, len(keys)+1)
	seen := map[string]bool{}
	for _, k := range keys {
		if k.KeySealed == "" || seen[k.KeySealed] {
			continue
		}
		seen[k.KeySealed] = true
		out = append(out, launcherKeyCandidate{sealed: k.KeySealed, machine: k.Machine})
	}
	if l.KeySealed != "" && !seen[l.KeySealed] {
		out = append(out, launcherKeyCandidate{sealed: l.KeySealed})
	}
	return out
}

// launcherMachineKeys is the computers' own keys by launcher, for a launcher that has no key of its own. An error means
// the store could not be read, not that there are no keys.
func (s *Service) launcherMachineKeys(ctx context.Context) (map[string][]machineKey, error) {
	st := s.store()
	if st == nil {
		return nil, nil
	}
	var keys []machineKey
	if _, err := memoSetting(ctx, st, launchersMachineKeysKey, &keys); err != nil {
		return nil, err
	}
	out := map[string][]machineKey{}
	for _, k := range keys {
		if k.KeySealed != "" {
			out[k.Launcher] = append(out[k.Launcher], k)
		}
	}
	return out, nil
}

// balanceClient is the client for one launcher's balance read. It goes where the launcher's own calls go: through the
// computer it names, through its proxy (honoring no_proxy), or directly, as probeTarget.do sends a probe. A redirect
// is never followed, so the key stays with the address asked, and one read is bounded like a probe. An invalid or
// hostless configured proxy returns an error instead of falling back to a direct client, so a required proxy is never
// silently bypassed.
func (s *Service) balanceClient(l storedLauncher) (*http.Client, error) {
	client := &http.Client{Timeout: balanceTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	if via := strings.TrimSpace(l.ViaMachine); via != "" {
		client.Transport = viaNode{s: s, machine: via}
		return client, nil
	}
	proxy := strings.TrimSpace(l.Proxy)
	noProxy := strings.TrimSpace(l.NoProxy)
	if proxy == "" && noProxy == "" {
		return client, nil
	}
	var proxyURL *url.URL
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Host == "" || !slices.Contains([]string{"http", "https", "socks5", "socks5h"}, u.Scheme) {
			return nil, errors.New("invalid launcher proxy")
		}
		proxyURL = u
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		if proxyURL != nil {
			return nil, errors.New("cannot configure launcher proxy on non-default transport")
		}
		return client, nil
	}
	t := transport.Clone()
	t.DisableKeepAlives = true
	if proxyURL != nil {
		t.Proxy = balanceProxy(proxyURL, l.NoProxy)
	}
	client.Transport = t
	return client, nil
}

// balanceProxy is the launcher's proxy for a request, except for an address its bypass list (no_proxy) exempts.
func balanceProxy(proxy *url.URL, noProxy string) func(*http.Request) (*url.URL, error) {
	if strings.TrimSpace(noProxy) == "" {
		return http.ProxyURL(proxy)
	}
	cfg := httpproxy.Config{HTTPProxy: proxy.String(), HTTPSProxy: proxy.String(), NoProxy: noProxy}
	use := cfg.ProxyFunc()
	return func(r *http.Request) (*url.URL, error) { return use(r.URL) }
}

// readDeepSeekBalance asks the provider once; balance nil with a problem code says why (see proto.UsageAccount.Problem).
// skipped is true when the caller's context ended before the read finished, so context exhaustion is not misreported as
// a per-launcher unreachable result.
func readDeepSeekBalance(ctx context.Context, base *url.URL, key string, client *http.Client) (*proto.UsageBalance, string, bool) {
	u := *base
	u.Path, u.RawPath = deepseekBalancePath, ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "failed", false
	}
	// The documented balance route takes the key as a Bearer token. The launcher added from the preset also launches
	// with x-api-key, so both are sent: only the official origin ever sees them.
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("x-api-key", key)
	req.Header.Set("Accept", "application/json")
	// Redirects are not followed: a redirect would take the launcher's key to another address.
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", true
		}
		return nil, "unreachable", false
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, "rejected", false
	case http.StatusOK:
	default:
		return nil, "failed", false
	}
	var body deepseekBalanceBody
	if err := json.NewDecoder(io.LimitReader(res.Body, balanceBodyMost)).Decode(&body); err != nil {
		if ctx.Err() != nil {
			return nil, "", true
		}
		return nil, "failed", false
	}
	for _, info := range body.BalanceInfos {
		amount := strings.TrimSpace(info.TotalBalance)
		currency := strings.ToUpper(strings.TrimSpace(info.Currency))
		if validBalanceAmount(amount) && validCurrencyCode(currency) {
			return &proto.UsageBalance{Amount: amount, Currency: currency}, "", false
		}
	}
	return nil, "failed", false // an answer without a usable amount is not a zero
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
