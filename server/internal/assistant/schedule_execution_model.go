package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
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
	launchers, err := s.assistantLaunchers(ctx, machine)
	if err != nil {
		return local, nil // unknown discovery is not proof that an exact launcher is absent
	}
	if slices.ContainsFunc(launchers, func(l ConfiguredLauncher) bool { return l.Name == name }) {
		return local, nil
	}
	d, _ := s.device(ctx, machine)
	kind := agentKind(s.launcherAgent(ctx, s.settings(ctx).MachineID, name))
	if replacement := s.mainChatInstead(ctx, d, name, kind, launchers); replacement != "" {
		local.Provider = launcherProvider(replacement)
		return local, nil
	}
	// Without an explicit backend choice, a computer's other headless-compatible default is valid too.
	for _, l := range launchers {
		if _, bad := s.launcherUnusable(ctx, machine, l.Name); !bad {
			return ModelBinding{Provider: launcherProvider(l.Name)}, nil
		}
	}
	return local, nil // normal validation reports an authoritative unavailable configuration
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
