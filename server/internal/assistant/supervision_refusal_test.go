package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestSupervisionRefusalUsesOnlyThisMachinesVerifiedChannels(t *testing.T) {
	s, f := mainChatFixture(t)
	f.mu.Lock()
	f.launcherList = []proto.Launcher{
		{Name: "oc", DisplayName: "OpenCode", Source: "opencode", Assistant: true, SupervisionPolicy: true},
		{Name: "oc-plugin", Source: "opencode", Assistant: true},
		{Name: "grok", Source: "grok", Assistant: true},
	}
	f.mu.Unlock()
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		err := s.supervisionRuntimeRefusal(i18n.With(context.Background(), lang), "server")
		re, ok := err.(*rpc.Error)
		if !ok || !strings.Contains(re.Message, "OpenCode") || strings.Contains(re.Message, "oc-plugin") || strings.Contains(re.Message, "grok") || strings.Contains(re.Message, "{launchers}") {
			t.Fatalf("%s: %v", lang, err)
		}
		var data map[string]any
		if json.Unmarshal(re.Data, &data) != nil || data["delivered"] != "none" {
			t.Fatalf("delivery status: %s", re.Data)
		}
		account := i18n.T(lang, "rpc-errors.assistant_supervision_account")
		if strings.Contains(account, "rpc-errors.") || account == "" {
			t.Fatalf("missing account translation: %s", lang)
		}
		if text := i18n.T(lang, "rpc-errors.assistant_supervision_runtime", supervisionNamesArgs(lang, nil)); strings.Contains(text, "{") {
			t.Fatalf("unknown inventory: %s", text)
		}
	}
}

func TestSupervisionPickerShowsFailedProbeAsUnavailable(t *testing.T) {
	s, _ := mainChatFixture(t)
	list := []ConfiguredLauncher{
		{Launcher: proto.Launcher{Name: "claude", Source: "builtin"}},
		{Launcher: proto.Launcher{Name: "verified", DisplayName: "OpenCode", Source: "opencode", Agent: "opencode", Assistant: true, SupervisionPolicy: true}},
		{Launcher: proto.Launcher{Name: "failed-probe", Source: "opencode", Agent: "opencode", Assistant: true}},
		{Launcher: proto.Launcher{Name: "plugin", Source: "opencode", Agent: "opencode", Assistant: true}},
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		fits, off := s.supervisionPickerLaunchers(list, nil, lang)
		if len(fits) != 2 || len(off) != 2 {
			t.Fatalf("%s: fits=%+v off=%+v", lang, fits, off)
		}
		for _, l := range off {
			if l.Code != "assistant_supervision_runtime" || !strings.Contains(l.Reason, "OpenCode") || strings.Contains(l.Reason, "{launchers}") {
				t.Fatalf("%s: %+v", lang, l)
			}
		}
	}
	// The ordinary Assistant catalog remains unaffected by picker-only filtering.
	if len(list) != 4 || !list[2].Assistant {
		t.Fatal("ordinary catalog mutated")
	}
}
