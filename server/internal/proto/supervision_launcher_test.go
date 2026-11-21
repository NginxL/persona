package proto

import (
	"reflect"
	"testing"
)

func TestSupervisionLaunchersRequireAnInstructionChannel(t *testing.T) {
	list := []Launcher{
		{Name: "cc", Source: "zshrc"}, {Name: "cx", Agent: "codex", Source: "path"},
		{Name: "api", Source: "persona"},
		{Name: "managed-codex", Source: "persona", Agent: "codex"},
		{Name: "agy", Agent: "agy", Source: "antigravity", Assistant: true, SupervisionPolicy: true},
		{Name: "oc", Source: "opencode", Assistant: true, SupervisionPolicy: true, DisplayName: "OpenCode"},
		{Name: "other-oc", Source: "opencode", Assistant: true, SupervisionPolicy: true, DisplayName: "OpenCode"},
	}
	for _, l := range list {
		if !LauncherSupervisionPolicy(l) {
			t.Errorf("verified channel rejected: %+v", l)
		}
	}
	for _, l := range []Launcher{
		{Name: "dsh", Source: "dsh", Assistant: true},
		{Name: "grok", Source: "grok", Assistant: true},
		{Name: "oc-plugin", Source: "opencode", Assistant: true},
		{Name: "cursor", Source: "path", Agent: "cursor", SupervisionPolicy: true},
		{Name: "traex", Source: "path", Agent: "traex"},
		{Name: "ordinary", Source: "unknown", SupervisionPolicy: true},
		{Name: "old-runtime", Source: "unknown", Assistant: true},
		{},
	} {
		if LauncherSupervisionPolicy(l) {
			t.Errorf("unverified channel admitted: %+v", l)
		}
	}
	if got, want := SupervisionLauncherNames(list), []string{"OpenCode", "agy", "api", "cc", "cx", "managed-codex"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %v; want %v", got, want)
	}
	if got := SupervisionLauncherNames(nil); len(got) != 0 {
		t.Fatalf("invented launcher: %v", got)
	}
}
