package assistant

import (
	"strings"

	"github.com/nwverse/persona/server/internal/prompts"
)

// snapshotAppOrder is the order of the snapshot's app line (docs/assistant/assistant.md "Live snapshots and prefix caching").
var snapshotAppOrder = []string{"ios", "ipados", "watchos", "macos", "visionos", "android", "harmony"}

// snapshotApps is the snapshot's app line: one app per kind of device, the one most recently heard from, a real
// device before a simulator (a developer's simulator must not stand in for the phone). An iPad reports platform ios
// with an iPadOS system and is its own kind.
func snapshotApps(apps []ClientApp) []prompts.SnapshotApp {
	best := map[string]ClientApp{}
	for _, a := range apps {
		kind := a.Platform
		if kind == "ios" && strings.HasPrefix(a.OS, "iPadOS") {
			kind = "ipados"
		}
		cur, ok := best[kind]
		if !ok || (cur.Simulator && !a.Simulator) || (cur.Simulator == a.Simulator && a.SeenAt.After(cur.SeenAt)) {
			best[kind] = a
		}
	}
	var out []prompts.SnapshotApp
	for _, kind := range snapshotAppOrder {
		if a, ok := best[kind]; ok {
			out = append(out, prompts.SnapshotApp{Platform: kind, Version: a.Version, Build: a.Build, OS: a.OS,
				Simulator: a.Simulator})
		}
	}
	return out
}
