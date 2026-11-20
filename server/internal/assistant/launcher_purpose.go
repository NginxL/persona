package assistant

import "context"

type launcherPurposeKey struct{}
type launcherModelHostKey struct{}

// Keep assistant model discovery separate from an engine's provider of the same
// name: an assistant conversation runs the shell launcher or an assistant runtime.
func withLauncherPurpose(ctx context.Context, purpose string) context.Context {
	if purpose == "assistant" {
		return context.WithValue(ctx, launcherPurposeKey{}, purpose)
	}
	return ctx
}

func launcherPurpose(ctx context.Context) string {
	purpose, _ := ctx.Value(launcherPurposeKey{}).(string)
	return purpose
}

// IDs, aliases and defaults describe the same execution catalog and must use the same cache partition.
func launcherModelCacheKey(ctx context.Context, machine, launcher string) string {
	key := machine + "\x00" + launcher
	if purpose := launcherPurpose(ctx); purpose != "" {
		key += "\x00" + purpose
	}
	return key
}
