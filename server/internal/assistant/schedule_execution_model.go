package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// A local alias on another computer is a different provider, even when its name matches Main chat's.
// Resolve inheritance before applying task overrides; an explicit task choice is never substituted.
func (s *Service) taskModelBaseline(ctx context.Context, machine string) (ModelBinding, error) {
	b, err := s.resolveModel(ctx, "assistant")
	if err != nil || machine == "" || machine == s.settings(ctx).MachineID {
		return b, err
	}
	name := strings.TrimPrefix(b.Provider, "launcher.")
	if managed, ok := s.managedNamed(ctx, name); ok && s.usableOn(ctx, managed, machine) {
		return b, nil // a managed provider retains its identity across computers in its scope
	}
	local := ModelBinding{Provider: b.Provider}
	r := s.mainChatFitState(ctx, machine, name, false)
	wait, fallbacks := s.launcherRecovery(ctx, machine, name)
	recovered := r.fit == mainChatGone && r.transient && wait > 0
	if recovered {
		r = s.waitMainChatLauncher(ctx, machine, name, wait)
	}
	if ctx.Err() != nil {
		return local, ctx.Err()
	}
	if r.fit == mainChatFits {
		return local, nil
	}
	if !recovered && r.launchers == nil && r.discoveryErr == nil {
		// A managed provider outside this machine's scope has no picker snapshot and is not a transient shell miss.
		r.launchers, r.discoveryErr = s.assistantLaunchers(ctx, machine)
	}
	if r.discoveryErr != nil {
		return local, nil // unknown discovery is not proof that an exact launcher is absent
	}
	d, _ := s.device(ctx, machine)
	kind := s.mainChatLauncherKind(ctx, s.settings(ctx).MachineID, name)
	if replacement := s.mainChatInstead(ctx, d, name, kind, r.launchers, fallbacks); replacement != "" {
		local.Provider = launcherProvider(replacement)
		return local, nil
	}
	// An inherited launcher is still the user's resolved backend. Never replace it with an arbitrary local default;
	// normal validation reports the authoritative unavailable configuration when no explicit substitute fits.
	return local, nil
}

// Compare the binding even when the selected computer cannot report a launcher identity.
func (s *Service) taskExecutionFingerprints(ctx context.Context, machine string, b ModelBinding) (binding, observed string) {
	type launcherIdentity struct{ Agent, Source, BaseURL, Model string }
	prefix := s.modelBindingFingerprint(ctx, b) + "\x00" + machine + "\x00"
	fingerprint := func(identity launcherIdentity) string {
		raw, _ := json.Marshal(identity)
		sum := sha256.Sum256(append([]byte(prefix), raw...))
		return hex.EncodeToString(sum[:])
	}
	binding = fingerprint(launcherIdentity{}) // also the digest older tasks stored when the catalog was unknown
	name := strings.TrimPrefix(b.Provider, "launcher.")
	if list, known := s.launchersHere(withUnhidden(ctx, name), machine); known {
		for _, l := range list {
			if l.Name == name {
				return binding, fingerprint(launcherIdentity{Agent: l.Agent, Source: proto.CanonicalLauncherSource(l.Source), BaseURL: l.BaseURL, Model: l.Model})
			}
		}
	}
	return binding, ""
}
