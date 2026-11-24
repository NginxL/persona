package assistant

import (
	"fmt"
	"testing"
)

// The scene matrix (#13246, docs/assistant/health.md "Using information by scene"): seven scenes × general, each
// sensitive category and untagged information. Inside its own track (scene 1) everything is used; outside it general
// information is used as usual (by relevance in the main chat or another track) and anything else, untagged included,
// takes the strict side of its scene.
func TestUsageMatrix(t *testing.T) {
	type kind struct {
		name string
		info infoUse
	}
	kinds := []kind{{"general", infoUse{Known: true, SourceTrack: "qa-plain"}}, {"untagged", infoUse{SourceTrack: "qa-plain"}}}
	for _, c := range infoCategories {
		kinds = append(kinds, kind{c, infoUse{Categories: []string{c}, Known: true, SourceTrack: "qa-finance"}})
	}
	general := map[scene]usageRule{sceneSourceTrack: usageAllow, sceneElsewhere: usageRelevant, sceneHandOff: usageRelevant,
		sceneLockScreen: usageAllow, sceneOthers: usageAllow, sceneExtension: usageAllow, sceneKBWrite: usageAllow}
	sensitive := map[scene]usageRule{sceneSourceTrack: usageAllow, sceneElsewhere: usageMinimal, sceneHandOff: usageConfirm,
		sceneLockScreen: usageRedact, sceneOthers: usageConfirm, sceneExtension: usageDeny, sceneKBWrite: usageDeny}
	for _, k := range kinds {
		want := sensitive
		if k.name == "general" {
			want = general
		}
		for _, sc := range scenes {
			if got := usage(k.info, sc, extensionGrant{}); got != want[sc] {
				t.Errorf("%s in scene %d = %d, want %d", k.name, sc, got, want[sc])
			}
		}
	}
	if got := usage(infoUse{Known: true}, scene(99), extensionGrant{}); got != usageDeny {
		t.Errorf("an unknown scene = %d, want deny", got)
	}
}

// Extensions (scene 6): one the user owns reads everything; a third-party one reads sensitive information only with a
// grant for each of its categories, and never information whose categories are not known.
func TestUsageExtensionGrants(t *testing.T) {
	both := infoUse{Categories: []string{categoryFinance, categoryHealth}, Known: true}
	for _, c := range []struct {
		name  string
		info  infoUse
		grant extensionGrant
		want  usageRule
	}{
		{"owner, sensitive", both, extensionGrant{Owner: true}, usageAllow},
		{"owner, untagged", infoUse{}, extensionGrant{Owner: true}, usageAllow},
		{"third party, no grant", both, extensionGrant{}, usageDeny},
		{"third party, one of two granted", both, extensionGrant{Categories: []string{categoryFinance}}, usageDeny},
		{"third party, both granted", both, extensionGrant{Categories: []string{categoryHealth, categoryFinance}}, usageAllow},
		{"third party, untagged", infoUse{}, extensionGrant{Categories: infoCategories}, usageDeny},
		{"third party, general", infoUse{Known: true}, extensionGrant{}, usageAllow},
	} {
		if got := usage(c.info, sceneExtension, c.grant); got != c.want {
			t.Errorf("%s = %d, want %d", c.name, got, c.want)
		}
	}
}

// The switches the call sites read follow usage: none for general information, all for anything else; so the health
// space (its default category health) keeps every switch on, the main space none, and an unknown track every one.
func TestPolicyOf(t *testing.T) {
	if got := policyOf(infoUse{Known: true}); got != (TrackPolicy{}) {
		t.Errorf("general = %+v", got)
	}
	for _, info := range []infoUse{{}, {Categories: []string{categoryHealth}, Known: true}, {Categories: []string{categoryLegalID}, Known: true}} {
		if got := policyOf(info); got != strictPolicy {
			t.Errorf("%s = %+v, want strict", fmt.Sprint(info.Categories), got)
		}
	}
}

// Every row of the health space's outbound table (docs/assistant/health.md "Outbound") is a scene whose rule for the
// health space's information is what the row says: the health space behaves as before.
func TestUsageHealthOutbound(t *testing.T) {
	health := infoUse{Categories: []string{categoryHealth}, Known: true, SourceTrack: SpaceHealth}
	for _, row := range []struct {
		exit string
		sc   scene
		want usageRule
	}{
		{"push: turn ended, notify_user, plan waiting, scheduled task failed", sceneLockScreen, usageRedact},
		{"phone local notification: phone_notify", sceneLockScreen, usageRedact},
		{"push for question and authorization cards", sceneLockScreen, usageRedact},
		{"Lark bot and plugin channels: forwarded notices, all cards, all questions", sceneLockScreen, usageRedact},
		{"agent_send, agent_start", sceneHandOff, usageConfirm},
		{"task_plan", sceneHandOff, usageConfirm},
		{"mail_send and connector writes", sceneOthers, usageConfirm},
		{"Lark writes", sceneOthers, usageConfirm},
		{"kb_update, kb_history_*", sceneKBWrite, usageDeny},
		{"third-party extensions", sceneExtension, usageDeny},
		{"the main chat's Read and Edit of its directory", sceneElsewhere, usageMinimal},
		{"memory and conversations within the space", sceneSourceTrack, usageAllow},
	} {
		if got := usage(health, row.sc, extensionGrant{}); got != row.want {
			t.Errorf("%s: %d, want %d", row.exit, got, row.want)
		}
	}
}
