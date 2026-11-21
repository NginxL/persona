package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestRuntimeLauncherReportsVerifiedSupervisionChannel(t *testing.T) {
	h := &runtimeConn{}
	h.hello.Runtime, h.hello.Launcher = "opencode", "oc"
	h.hello.Caps.Assistant = true
	h.hello.Caps.SupervisionPolicy = false
	l := runtimeLauncher{Name: "oc"}
	if got := h.formLauncherLocked(l); !got.Assistant || got.SupervisionPolicy {
		t.Fatalf("failed probe: %+v", got)
	}
	h.hello.Caps.SupervisionPolicy = true
	if got := h.formLauncherLocked(l); !got.SupervisionPolicy {
		t.Fatalf("verified channel absent: %+v", got)
	}
	if got := h.formLauncherLocked(runtimeLauncher{Name: "ordinary"}); got.Assistant || got.SupervisionPolicy {
		t.Fatalf("secondary launcher inherited role: %+v", got)
	}
	h.hello.Caps.Assistant = false
	if got := h.formLauncherLocked(l); got.SupervisionPolicy {
		t.Fatalf("non-assistant inherited channel: %+v", got)
	}
}

func TestSupervisionRefusalListsSnapshotWithoutStartingDiscovery(t *testing.T) {
	launcherCache.Lock()
	oldAt, oldList := launcherCache.at, launcherCache.list
	launcherCache.at, launcherCache.list = time.Time{}, []proto.Launcher{{Name: "local-cc", Source: "zshrc"}, {Name: "grok", Source: "grok", Assistant: true}}
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list = oldAt, oldList
		launcherCache.Unlock()
	})
	n := &Node{}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		err := n.supervisionRuntimeRefusal(i18n.With(context.Background(), lang)).(*rpc.Error)
		var data struct {
			Launchers []string `json:"launchers"`
			Delivered string   `json:"delivered"`
		}
		if json.Unmarshal(err.Data, &data) != nil || data.Delivered != "none" || len(data.Launchers) != 1 || data.Launchers[0] != "local-cc" || !strings.Contains(err.Message, "local-cc") || strings.Contains(err.Message, "{") {
			t.Fatalf("%s: %v %s", lang, err, err.Data)
		}
	}
}

func TestRuntimeSupervisionRefusalPreservesDeliveryEvidence(t *testing.T) {
	n := &Node{}
	for _, code := range []string{"assistant_supervision_runtime", "assistant_supervision_account"} {
		for _, evidence := range []string{`{}`, `{"delivered":"unknown","retryable":false}`, `{"delivered":"none"}`} {
			original := &rpc.Error{Code: code, Message: "runtime diagnostic", Data: json.RawMessage(evidence)}
			got := n.localizeSupervisionRefusal(i18n.With(context.Background(), i18n.Ja), original).(*rpc.Error)
			var before, after map[string]any
			_ = json.Unmarshal(original.Data, &before)
			_ = json.Unmarshal(got.Data, &after)
			if got.Code != code || after["delivered"] != before["delivered"] || after["retryable"] != before["retryable"] {
				t.Fatalf("changed delivery evidence: %s -> %s", original.Data, got.Data)
			}
			if got.Message == original.Message || strings.Contains(got.Message, "{") {
				t.Fatalf("not localized: %v", got)
			}
		}
	}
}
