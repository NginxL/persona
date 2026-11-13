package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// One app per kind of device: the real iPhone before a newer simulator, the newest of two Macs, an iPad apart from
// the iPhone; the line names each with its version and build.
func TestSnapshotApps(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	apps := []ClientApp{
		{Platform: "ios", Version: "1.5.0", Build: "60", OS: "iOS 26.1", Simulator: true, SeenAt: now},
		{Platform: "ios", Version: "1.4.0", Build: "52", OS: "iOS 26.1", SeenAt: now.Add(-3 * time.Hour)},
		{Platform: "ios", Version: "1.3.0", Build: "40", OS: "iPadOS 26.0", SeenAt: now.Add(-48 * time.Hour)},
		{Platform: "macos", Version: "1.2.0", Build: "30", SeenAt: now.Add(-time.Hour)},
		{Platform: "macos", Version: "1.4.0", Build: "52", OS: "macOS 26.1", SeenAt: now.Add(-time.Minute)},
		{Platform: "watchos", Version: "1.4.0", Build: "52", OS: "watchOS 11.2", SeenAt: now},
		{Platform: "android", Version: "0.9.1", Build: "91", OS: "Android 16", SeenAt: now},
	}
	got := snapshotApps(apps)
	var line []string
	for _, a := range got {
		line = append(line, a.Platform+" "+a.Version+"/"+a.Build)
	}
	if want := "ios 1.4.0/52,ipados 1.3.0/40,watchos 1.4.0/52,macos 1.4.0/52,android 0.9.1/91"; strings.Join(line, ",") != want {
		t.Fatalf("got %v, want %s", line, want)
	}
	// Only a simulator: it is shown, and says so.
	sim := snapshotApps([]ClientApp{{Platform: "ios", Version: "1.5.0", Build: "60", Simulator: true, SeenAt: now}})
	text := prompts.RenderSnapshot(prompts.Snapshot{Now: now, Zone: "UTC", Apps: sim})
	if !strings.Contains(text, "iPhone App 1.5.0 (build 60)〔模拟器〕") {
		t.Fatalf("simulator line:\n%s", text)
	}
	if text := prompts.RenderSnapshot(prompts.Snapshot{Now: now, Zone: "UTC"}); strings.Contains(text, "Persona App 版本") {
		t.Fatalf("no app said a version: no line\n%s", text)
	}
	golden(t, "assistant_snapshot.apps", prompts.RenderSnapshot(prompts.Snapshot{Now: now, Zone: "UTC", Apps: got}))
}

func TestHarmonyAppVersionInSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	apps := snapshotApps([]ClientApp{
		{Platform: "harmony", Version: "1.0.2", Build: "1000002", OS: "HarmonyOS 7.0", Simulator: true, SeenAt: now},
		{Platform: "harmony", Version: "1.0.0", Build: "1000000", OS: "HarmonyOS 7.0", SeenAt: now.Add(-2 * time.Hour)},
		{Platform: "harmony", Version: "1.0.1", Build: "1000001", OS: "HarmonyOS 7.0", SeenAt: now.Add(-time.Hour)},
	})
	if len(apps) != 1 || apps[0].Platform != "harmony" || apps[0].Version != "1.0.1" || apps[0].Simulator {
		t.Fatalf("want the newest real HarmonyOS app: %+v", apps)
	}
	text := prompts.RenderSnapshot(prompts.Snapshot{Now: now, Zone: "UTC", Apps: apps})
	if !strings.Contains(text, "HarmonyOS App 1.0.1 (build 1000001)（HarmonyOS 7.0）") {
		t.Fatalf("HarmonyOS app missing from assistant context:\n%s", text)
	}
}
