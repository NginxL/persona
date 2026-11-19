package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// A Claude Code tier alias stored as an API purpose's model is sent as the model its launcher's tier field names, and
// refused when the connection names none (#9165): a gateway has no model called "sonnet".
func TestAPIConfigResolvesTierAlias(t *testing.T) {
	s, _, _ := modelService(t)
	key, err := s.sealKey("synthetic-key")
	if err != nil {
		t.Fatal(err)
	}
	tiers := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "tiers", Kind: "launcher", API: "messages",
		BaseURL: "https://gateway.example.com", Model: "gw/claude-default",
		OpusModel: "gw/claude-opus-5-5", SonnetModel: "gw/claude-sonnet-5-5", HaikuModel: "gw/claude-haiku-5-5",
		FableModel: "gw/claude-fable-5"}, KeySealed: key}
	bare := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "bare", Kind: "api", API: "messages",
		BaseURL: "https://other.example.com", Model: "sonnet"}, KeySealed: key}
	loop := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "loop", Kind: "launcher", API: "messages",
		BaseURL: "https://loop.example.com", Model: "m", SonnetModel: "opus"}, KeySealed: key}
	small := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "small", Kind: "launcher", API: "messages",
		BaseURL: "https://small.example.com", Model: "m", SmallModel: "gw/fast"}, KeySealed: key}
	aliasDefault := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "aliasdefault", Kind: "launcher", API: "messages",
		BaseURL: "https://alias.example.com", Model: "sonnet", SonnetModel: "gw/claude-sonnet-5-5"}, KeySealed: key}
	putLaunchers(t, s, tiers, bare, loop, small, aliasDefault)

	for _, tc := range []struct {
		name    string
		b       ModelBinding
		want    string
		refused bool
	}{
		{name: "sonnet", b: ModelBinding{Provider: "launcher.tiers", Model: "sonnet"}, want: "gw/claude-sonnet-5-5"},
		{name: "case and 1m suffix", b: ModelBinding{Provider: "launcher.tiers", Model: "Opus[1m]"}, want: "gw/claude-opus-5-5"},
		{name: "haiku", b: ModelBinding{Provider: "launcher.tiers", Model: "haiku"}, want: "gw/claude-haiku-5-5"},
		{name: "fable", b: ModelBinding{Provider: "launcher.tiers", Model: "fable"}, want: "gw/claude-fable-5"},
		{name: "best runs opus", b: ModelBinding{Provider: "launcher.tiers", Model: "best"}, want: "gw/claude-opus-5-5"},
		{name: "opusplan runs opus", b: ModelBinding{Provider: "launcher.tiers", Model: "opusplan"}, want: "gw/claude-opus-5-5"},
		{name: "haiku falls back to the small model", b: ModelBinding{Provider: "launcher.small", Model: "haiku"}, want: "gw/fast"},
		{name: "launcher default is a resolvable alias", b: ModelBinding{Provider: "launcher.aliasdefault"}, want: "gw/claude-sonnet-5-5"},
		{name: "a real ID is left alone", b: ModelBinding{Provider: "launcher.tiers", Model: "claude-sonnet-5-5"}, want: "claude-sonnet-5-5"},
		{name: "the launcher's own model", b: ModelBinding{Provider: "launcher.tiers"}, want: "gw/claude-default"},
		{name: "launcher default is an alias with no tier field", b: ModelBinding{Provider: "launcher.bare"}, refused: true},
		{name: "no tier field", b: ModelBinding{Provider: "launcher.bare", Model: "haiku"}, refused: true},
		{name: "tier field is itself an alias", b: ModelBinding{Provider: "launcher.loop", Model: "sonnet"}, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := s.apiConfig(context.Background(), tc.b)
			if tc.refused {
				var m *i18n.Message
				if !errors.As(err, &m) || m.Key != "assistant-models.err_tier_alias_unresolved" {
					t.Fatalf("model %q: err = %v, want the tier alias refusal", c.Model, err)
				}
				if s.modelUsable(context.Background(), tc.b) {
					t.Fatal("an unresolvable tier alias counts as usable")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Model != tc.want {
				t.Fatalf("model = %q, want %q", c.Model, tc.want)
			}
			if !s.modelUsable(context.Background(), tc.b) {
				t.Fatal("a resolved binding is not usable")
			}
		})
	}
}

func TestSameTierAliasTargetsChecksSmallBehindHaiku(t *testing.T) {
	a := storedLauncher{ManagedLauncher: proto.ManagedLauncher{HaikuModel: "gw/haiku", SmallModel: "gw/fast"}}
	b := a
	b.SmallModel = "gw/other"
	if sameTierAliasTargets(a, b) {
		t.Fatal("small_model change hidden by a nonempty haiku_model")
	}
}

func TestChatSaveMigratesTierAliasesWithProvider(t *testing.T) {
	s, mux := launcherTestService(t)
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{
		Name: "chat", Kind: "api", API: "chat", BaseURL: "https://chat.example.com",
		Model: "Sonnet[1m]", SonnetModel: "gw/sonnet", SmallModel: "gw/fast",
	}})
	for id, model := range map[string]string{
		"kb": "Sonnet[1m]", purposeMeetingMinutes: "sonnet", "inbox.triage": "haiku", "agent.delegate": "custom-model",
	} {
		setModel(t, s, id, ModelBinding{Provider: "launcher.chat", Model: model})
	}

	clear := `{"kind":"api","api":"chat","base_url":"https://chat.example.com","model":"Sonnet[1m]","small_model":"","sonnet_model":""}`
	if code, body := launcherCall(t, mux, "PUT", "/api/v1/launchers/chat", clear); code != 200 {
		t.Fatalf("clear legacy tier fields: %d %s", code, body)
	}
	stored := s.storedLaunchers(context.Background())
	if len(stored) != 1 || stored[0].Model != "gw/sonnet" || stored[0].SonnetModel != "" || stored[0].SmallModel != "" {
		t.Fatalf("stored provider = %+v", stored)
	}
	for id, want := range map[string]string{"kb": "gw/sonnet", purposeMeetingMinutes: "gw/sonnet", "inbox.triage": "gw/fast"} {
		p := purposeOf(t, s, id)
		if p.Override.Model != want || p.Revision != 2 {
			t.Fatalf("%s override = %+v, revision %d", id, p.Override, p.Revision)
		}
		config, err := s.apiConfig(context.Background(), p.Effective)
		if err != nil || config.Model != want {
			t.Fatalf("%s resolved model = %q, err = %v", id, config.Model, err)
		}
	}
	if p := purposeOf(t, s, "agent.delegate"); p.Override.Model != "custom-model" || p.Revision != 1 {
		t.Fatalf("non-alias purpose changed: %+v, revision %d", p.Override, p.Revision)
	}
}

func chatAliasFixture(t *testing.T) (*Service, *fakeHub, *settingsFaultStore) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.ownModels = true
	st := newSettingsFaultStore()
	s := newServiceOn(t, f, st)
	key, err := s.sealKey("chat-alias-test-key")
	if err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{
		Name: "chat", Kind: "api", API: "chat", BaseURL: "https://chat.example.com",
		Model: "gw/default", SonnetModel: "gw/sonnet", SmallModel: "gw/fast",
	}, KeySealed: key})
	return s, f, st
}

func clearChatAliasTiers(t *testing.T, s *Service) (int, string) {
	t.Helper()
	body := `{"kind":"api","api":"chat","base_url":"https://chat.example.com","model":"gw/default","small_model":"","sonnet_model":"","haiku_model":""}`
	return launcherCall(t, s, "PUT", "/api/v1/launchers/chat", body)
}

func putAliasRecord(t *testing.T, st *settingsFaultStore, key string, revision int64, binding ModelBinding) {
	t.Helper()
	if err := st.PutSetting(context.Background(), key, modelRecord{Revision: revision, Binding: binding}); err != nil {
		t.Fatal(err)
	}
}

func aliasRecord(t *testing.T, st *settingsFaultStore, key string) modelRecord {
	t.Helper()
	var record modelRecord
	if _, err := st.Setting(context.Background(), key, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestChatSaveMigratesEveryPersistedRecordWithoutExtensionCatalog(t *testing.T) {
	s, f, st := chatAliasFixture(t)
	putAliasRecord(t, st, modelRecordKey("kb"), 1, ModelBinding{Provider: "launcher.chat", Model: "gw/default"})
	putAliasRecord(t, st, modelRecordKey("agent.new"), 1, ModelBinding{Provider: "launcher.chat", Model: "gw/default"})
	for key, binding := range map[string]ModelBinding{
		modelKey("extension.disabled"):         {Provider: "launcher.chat", Model: "sonnet"},
		modelKey("extension.ungranted"):        {Model: "haiku"},
		modelKey("unknown.retained"):           {Provider: "launcher.chat", Model: "Sonnet[1m]"},
		modelKey("inbox.agent"):                {Model: "sonnet"},
		modelOverrideKeyPrefix + "inbox.agent": {Model: "haiku"},
		modelKey("extension.unrelated"):        {Provider: "launcher.other", Model: "sonnet"},
	} {
		putAliasRecord(t, st, key, 4, binding)
	}
	if code, body := clearChatAliasTiers(t, s); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	for key, want := range map[string]string{
		modelKey("extension.disabled"):         "gw/sonnet",
		modelKey("extension.ungranted"):        "gw/fast",
		modelKey("unknown.retained"):           "gw/sonnet",
		modelKey("inbox.agent"):                "gw/sonnet",
		modelOverrideKeyPrefix + "inbox.agent": "gw/fast",
	} {
		if got := aliasRecord(t, st, key); got.Binding.Model != want || got.Revision != 5 {
			t.Fatalf("%s = %+v, want model %s at revision 5", key, got, want)
		}
	}
	if got := aliasRecord(t, st, modelKey("extension.unrelated")); got.Binding.Model != "sonnet" || got.Revision != 4 {
		t.Fatalf("another provider's record changed: %+v", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.appCalls {
		if call.Method == assistantwire.PluginsModels {
			t.Fatal("migration consulted the enabled extension catalog")
		}
	}
}

func TestChatSaveMigratesProfilesAndRollbackWithTheirOwnInheritance(t *testing.T) {
	s, f, st := chatAliasFixture(t)
	ctx := context.Background()
	setSettings(t, s, func(settings *Settings) { settings.Launcher = "chat" })
	putAliasRecord(t, st, modelRecordKey("kb"), 2, ModelBinding{Provider: "launcher.other", Model: "current-other"})
	putAliasRecord(t, st, modelRecordKey("assistant"), 2, ModelBinding{Provider: "launcher.other", Model: "current-other"})
	profiles := modelProfiles{Revision: 7, Profiles: []ModelProfile{
		{ID: "one", Name: "One", Purposes: map[string]ModelBinding{
			"kb": {Provider: "launcher.chat", Model: "sonnet"}, purposeMeetingMinutes: {Model: "haiku"},
			"task.t1": {Model: "sonnet"},
		}, Launchers: map[string]LauncherModels{"chat": {Model: profileValue("sonnet"), Effort: profileValue("high")}}},
		{ID: "two", Name: "Two", Purposes: map[string]ModelBinding{
			"kb": {}, "extension.disabled": {Model: "sonnet"}, "task.t1": {Model: "sonnet"},
		}, Launchers: map[string]LauncherModels{"chat": {Model: profileValue("haiku")}}},
		{ID: "applicable", Name: "Applicable", Purposes: map[string]ModelBinding{
			"kb": {Provider: "launcher.chat", Model: "sonnet"},
		}, Launchers: map[string]LauncherModels{"chat": {Model: profileValue("sonnet")}}},
	}}
	state := modelProfileState{Active: "one", Previous: &modelProfileImage{Profile: "before", Purposes: map[string]ModelBinding{
		"kb": {Provider: "launcher.chat", Model: "sonnet"}, purposeMeetingMinutes: {Model: "haiku"},
	}, Launchers: map[string]LauncherModels{"chat": {Model: profileValue("haiku")}}}}
	if err := st.PutSettings(ctx, map[string]any{modelProfilesKey: profiles, modelProfileStateKey: state}); err != nil {
		t.Fatal(err)
	}
	if code, body := clearChatAliasTiers(t, s); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	var gotProfiles modelProfiles
	if _, err := st.Setting(ctx, modelProfilesKey, &gotProfiles); err != nil {
		t.Fatal(err)
	}
	if gotProfiles.Revision != 8 {
		t.Fatalf("profile revision = %d, want one increment", gotProfiles.Revision)
	}
	one, two := gotProfiles.Profiles[0], gotProfiles.Profiles[1]
	if one.Purposes["kb"].Model != "gw/sonnet" || one.Purposes[purposeMeetingMinutes].Model != "gw/fast" ||
		one.Purposes["task.t1"].Model != "sonnet" || *one.Launchers["chat"].Model != "gw/sonnet" || *one.Launchers["chat"].Effort != "high" {
		t.Fatalf("first profile = %+v", one)
	}
	if two.Purposes["extension.disabled"].Model != "gw/sonnet" || two.Purposes["extension.disabled"].Provider != "" ||
		two.Purposes["task.t1"].Model != "sonnet" || *two.Launchers["chat"].Model != "gw/fast" {
		t.Fatalf("profile overlay = %+v", two)
	}
	applicable := gotProfiles.Profiles[2]
	if applicable.Purposes["kb"].Model != "gw/sonnet" || *applicable.Launchers["chat"].Model != "gw/sonnet" {
		t.Fatalf("applicable profile = %+v", applicable)
	}
	gotState, err := s.readModelProfileState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotState.Previous.Purposes["kb"].Model != "gw/sonnet" || gotState.Previous.Purposes[purposeMeetingMinutes].Model != "gw/fast" ||
		*gotState.Previous.Launchers["chat"].Model != "gw/fast" {
		t.Fatalf("rollback image = %+v", gotState.Previous)
	}
	if f.eventTypes() != "model_profile" {
		t.Fatalf("profile event = %q", f.eventTypes())
	}

	// The migrated rollback and profile can both be applied; neither sends an alias to the Chat gateway.
	if _, err := s.rollbackModelProfile(ctx, ProfileActor{Kind: "hub"}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := modelRecordOf(t, s, "kb").Binding.Model; got != "gw/sonnet" {
		t.Fatalf("rollback model = %q", got)
	}
	if config, err := s.apiConfig(ctx, ModelBinding{Provider: "launcher.chat"}); err != nil || config.Model != "gw/fast" {
		t.Fatalf("rollback provider default = %q, %v", config.Model, err)
	}
	if _, err := s.applyModelProfile(ctx, "applicable", ProfileActor{Kind: "hub"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if config, err := s.apiConfig(ctx, ModelBinding{Provider: "launcher.chat"}); err != nil || config.Model != "gw/sonnet" {
		t.Fatalf("applied provider default = %q, %v", config.Model, err)
	}
}

func TestChatSaveTierAliasUsesProfileLauncherOverlay(t *testing.T) {
	for _, tc := range []struct{ name, sonnet, haiku string }{
		{name: "provider and overlay mappings", sonnet: "gw/sonnet", haiku: "gw/haiku"},
		{name: "only overlay mapping", haiku: "haiku"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, st := chatAliasFixture(t)
			ctx := context.Background()
			launchers := s.storedLaunchers(ctx)
			launchers[0].SonnetModel = tc.sonnet
			launchers[0].HaikuModel = tc.haiku
			putLaunchers(t, s, launchers...)
			overlay := LauncherModels{Model: profileValue("haiku"), HaikuModel: profileValue("")}
			profiles := modelProfiles{Revision: 3, Profiles: []ModelProfile{{
				ID: "overlay", Name: "Overlay",
				Purposes:  map[string]ModelBinding{"kb": {Provider: "launcher.chat", Model: "haiku"}},
				Launchers: map[string]LauncherModels{"chat": overlay},
			}}}
			state := modelProfileState{Previous: &modelProfileImage{
				Profile:   "before",
				Purposes:  map[string]ModelBinding{"kb": {Provider: "launcher.chat", Model: "haiku"}},
				Launchers: map[string]LauncherModels{"chat": overlay},
			}}
			if err := st.PutSettings(ctx, map[string]any{modelProfilesKey: profiles, modelProfileStateKey: state}); err != nil {
				t.Fatal(err)
			}
			if code, body := clearChatAliasTiers(t, s); code != 200 {
				t.Fatalf("save: %d %s", code, body)
			}
			var gotProfiles modelProfiles
			if _, err := st.Setting(ctx, modelProfilesKey, &gotProfiles); err != nil {
				t.Fatal(err)
			}
			profile := gotProfiles.Profiles[0]
			if got := *profile.Launchers["chat"].Model; got != "gw/fast" {
				t.Fatalf("profile provider default = %q, want the overlaid small model", got)
			}
			if got := profile.Purposes["kb"].Model; got != "gw/fast" {
				t.Fatalf("profile purpose = %q, want the overlaid small model", got)
			}
			gotState, err := s.readModelProfileState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			previous := gotState.Previous
			if got := *previous.Launchers["chat"].Model; got != "gw/fast" {
				t.Fatalf("rollback provider default = %q, want the overlaid small model", got)
			}
			if got := previous.Purposes["kb"].Model; got != "gw/fast" {
				t.Fatalf("rollback purpose = %q, want the overlaid small model", got)
			}
			if _, err := s.rollbackModelProfile(ctx, ProfileActor{Kind: "hub"}); err != nil {
				t.Fatalf("rollback: %v", err)
			}
			if got := modelRecordOf(t, s, "kb").Binding.Model; got != "gw/fast" {
				t.Fatalf("rolled back purpose = %q", got)
			}
			if config, err := s.apiConfig(ctx, ModelBinding{Provider: "launcher.chat"}); err != nil || config.Model != "gw/fast" {
				t.Fatalf("rolled back provider default = %q, %v", config.Model, err)
			}
			if _, err := s.rollbackModelProfile(ctx, ProfileActor{Kind: "hub"}); err != nil {
				t.Fatalf("redo: %v", err)
			}
			if config, err := s.apiConfig(ctx, ModelBinding{Provider: "launcher.chat"}); err != nil || config.Model != "gw/default" {
				t.Fatalf("redone provider default = %q, %v", config.Model, err)
			}
			if _, err := s.applyModelProfile(ctx, "overlay", ProfileActor{Kind: "hub"}); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if config, err := s.apiConfig(ctx, ModelBinding{Provider: "launcher.chat"}); err != nil || config.Model != "gw/fast" {
				t.Fatalf("applied provider default = %q, %v", config.Model, err)
			}
		})
	}
}

func TestChatSaveTierAliasRollbackOnlyKeepsProfileRevision(t *testing.T) {
	s, f, st := chatAliasFixture(t)
	ctx := context.Background()
	profiles := modelProfiles{Revision: 9, Profiles: []ModelProfile{{ID: "one", Name: "One"}}}
	launchers := LauncherModels{
		Model: profileValue("sonnet"), SmallModel: profileValue("haiku"), SubagentModel: profileValue("sonnet"),
		OpusModel: profileValue("haiku"), SonnetModel: profileValue("sonnet"), HaikuModel: profileValue("haiku"),
		FableModel: profileValue("sonnet"), Effort: profileValue("high"),
	}
	state := modelProfileState{Active: "one", Previous: &modelProfileImage{
		Purposes:  map[string]ModelBinding{"kb": {Provider: "launcher.chat", Model: "sonnet"}},
		Launchers: map[string]LauncherModels{"chat": launchers},
	}}
	if err := st.PutSettings(ctx, map[string]any{modelProfilesKey: profiles, modelProfileStateKey: state}); err != nil {
		t.Fatal(err)
	}
	if code, body := clearChatAliasTiers(t, s); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	var gotProfiles modelProfiles
	if _, err := st.Setting(ctx, modelProfilesKey, &gotProfiles); err != nil {
		t.Fatal(err)
	}
	if gotProfiles.Revision != 9 {
		t.Fatalf("rollback-only save changed profile revision to %d", gotProfiles.Revision)
	}
	gotState, err := s.readModelProfileState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotState.Previous.Purposes["kb"].Model != "gw/sonnet" {
		t.Fatalf("rollback purpose = %+v", gotState.Previous.Purposes["kb"])
	}
	models := gotState.Previous.Launchers["chat"]
	for _, field := range models.fields(nil) {
		if *field.set == nil {
			t.Fatalf("%s was cleared", field.name)
		}
		want := "gw/sonnet"
		if field.name == "small_model" || field.name == "opus_model" || field.name == "haiku_model" {
			want = "gw/fast"
		} else if field.name == "effort" {
			want = "high"
		}
		if **field.set != want {
			t.Fatalf("%s = %q, want %q", field.name, **field.set, want)
		}
	}
	if f.eventTypes() != "model_profile" {
		t.Fatalf("rollback-only event = %q", f.eventTypes())
	}
}

func TestChatSaveMigratesBlankProviderRootsWithoutFixingTheirProvider(t *testing.T) {
	s, _, st := chatAliasFixture(t)
	setSettings(t, s, func(settings *Settings) { settings.Launcher = "chat" })
	putAliasRecord(t, st, modelRecordKey("kb"), 1, ModelBinding{Model: "sonnet"})
	putAliasRecord(t, st, modelRecordKey("task.t1"), 1, ModelBinding{Model: "haiku"})
	putAliasRecord(t, st, modelRecordKey("extension.disabled"), 1, ModelBinding{Model: "sonnet"})
	putAliasRecord(t, st, modelRecordKey("plugin.voice"), 1, ModelBinding{Model: "sonnet"})
	putAliasRecord(t, st, modelRecordKey("agent.new"), 1, ModelBinding{Model: "sonnet"})
	if code, body := clearChatAliasTiers(t, s); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	for key, want := range map[string]string{
		modelRecordKey("kb"): "gw/sonnet", modelRecordKey("task.t1"): "gw/fast", modelRecordKey("extension.disabled"): "gw/sonnet",
	} {
		if got := aliasRecord(t, st, key); got.Binding.Provider != "" || got.Binding.Model != want || got.Revision != 2 {
			t.Fatalf("%s = %+v, want inherited model %s at revision 2", key, got, want)
		}
	}
	if got := aliasRecord(t, st, modelRecordKey("agent.new")); got.Binding.Model != "sonnet" || got.Revision != 1 {
		t.Fatalf("Claude's independent default migrated to Chat: %+v", got)
	}
	if got := aliasRecord(t, st, modelRecordKey("plugin.voice")); got.Binding != (ModelBinding{Model: "sonnet"}) || got.Revision != 1 {
		t.Fatalf("plugin binding changed: %+v", got)
	}
}

func TestChatSaveMigratesAliasesInheritedFromCandidateFallback(t *testing.T) {
	s, _, st := chatAliasFixture(t)
	ctx := context.Background()
	launchers := s.storedLaunchers(ctx)
	launchers[0].KeySealed = ""
	putLaunchers(t, s, launchers...)
	if got := fallbackAPIProviderFrom(launchers, nil); got != "" {
		t.Fatalf("pre-save fallback = %q, want none", got)
	}
	putAliasRecord(t, st, modelRecordKey("kb"), 4, ModelBinding{Model: "sonnet"})
	profiles := modelProfiles{Revision: 7, Profiles: []ModelProfile{{ID: "one", Name: "One", Purposes: map[string]ModelBinding{
		"kb": {Model: "sonnet"}, purposeMeetingMinutes: {Model: "haiku"},
	}}}}
	state := modelProfileState{Active: "one", Previous: &modelProfileImage{Profile: "before", Purposes: map[string]ModelBinding{
		"kb": {Model: "haiku"},
	}}}
	if err := st.PutSettings(ctx, map[string]any{modelProfilesKey: profiles, modelProfileStateKey: state}); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.reads[launchersManagedKey] = 0
	st.onRead = func(st *settingsFaultStore, key string, n int) {
		if key == launchersManagedKey && n > 2 {
			st.failRead = key
		}
	}
	st.mu.Unlock()
	body := `{"kind":"api","api":"chat","base_url":"https://chat.example.com","model":"gw/default","small_model":"","sonnet_model":"","api_key":"new-chat-key"}`
	if code, response := launcherCall(t, s, "PUT", "/api/v1/launchers/chat", body); code != 200 {
		t.Fatalf("save: %d %s", code, response)
	}
	if got := st.readCount(launchersManagedKey); got != 2 {
		t.Fatalf("managed provider reads = %d, want the route and save reads only", got)
	}
	st.mu.Lock()
	prefixReads := st.prefixReads
	st.onRead = nil
	st.failRead = ""
	st.mu.Unlock()
	if prefixReads != 1 {
		t.Fatalf("prefix reads = %d, want 1", prefixReads)
	}
	if got := s.fallbackAPIProvider(ctx); got != "chat" {
		t.Fatalf("post-save fallback = %q, want chat", got)
	}
	if got := aliasRecord(t, st, modelRecordKey("kb")); got.Binding != (ModelBinding{Model: "gw/sonnet"}) || got.Revision != 5 {
		t.Fatalf("current inherited binding = %+v", got)
	}
	var gotProfiles modelProfiles
	if _, err := st.Setting(ctx, modelProfilesKey, &gotProfiles); err != nil {
		t.Fatal(err)
	}
	if gotProfiles.Revision != 8 {
		t.Fatalf("profile revision = %d, want one increment", gotProfiles.Revision)
	}
	profile := gotProfiles.Profiles[0]
	if profile.Purposes["kb"] != (ModelBinding{Model: "gw/sonnet"}) ||
		profile.Purposes[purposeMeetingMinutes] != (ModelBinding{Model: "gw/fast"}) {
		t.Fatalf("saved profile = %+v", profile.Purposes)
	}
	gotState, err := s.readModelProfileState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotState.Previous.Purposes["kb"] != (ModelBinding{Model: "gw/fast"}) {
		t.Fatalf("rollback binding = %+v", gotState.Previous.Purposes["kb"])
	}
}

func TestChatSaveTierAliasProfileEventOutsideModelLock(t *testing.T) {
	s, f, st := chatAliasFixture(t)
	profiles := modelProfiles{Profiles: []ModelProfile{{
		ID: "one", Launchers: map[string]LauncherModels{"chat": {Model: profileValue("sonnet")}},
	}}}
	if err := st.PutSetting(context.Background(), modelProfilesKey, profiles); err != nil {
		t.Fatal(err)
	}
	f.onBroadcast = func() {
		if !s.models.mu.TryLock() {
			t.Error("model_profile broadcast holds models.mu")
			return
		}
		s.models.mu.Unlock()
	}
	if code, body := clearChatAliasTiers(t, s); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	if got := f.eventTypes(); got != "model_profile" {
		t.Fatalf("profile events = %q, want model_profile", got)
	}
}

func TestChatSaveTierAliasFailuresWriteNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		bind  ModelBinding
		setup func(*Service, *settingsFaultStore)
	}{
		{name: "prefix", key: modelRecordKey("kb"), bind: ModelBinding{Provider: "launcher.chat", Model: "sonnet"}, setup: func(_ *Service, st *settingsFaultStore) {
			st.failPrefix = errors.New("prefix unavailable")
		}},
		{name: "hidden launchers", key: modelRecordKey("kb"), bind: ModelBinding{Model: "sonnet"}, setup: func(_ *Service, st *settingsFaultStore) {
			st.failKey(launchersHiddenKey, errors.New("hidden unavailable"))
		}},
		{name: "assistant settings", key: modelRecordKey("task.t1"), bind: ModelBinding{Model: "sonnet"}, setup: func(s *Service, st *settingsFaultStore) {
			st.failKey(settingsKey, errors.New("settings unavailable"))
			s.prefs.mu.Lock()
			s.prefs.cached = nil
			s.prefs.mu.Unlock()
		}},
		{name: "profiles", key: modelRecordKey("kb"), bind: ModelBinding{Provider: "launcher.chat", Model: "sonnet"}, setup: func(_ *Service, st *settingsFaultStore) {
			st.failKey(modelProfilesKey, errors.New("profiles unavailable"))
		}},
		{name: "rollback", key: modelRecordKey("kb"), bind: ModelBinding{Provider: "launcher.chat", Model: "sonnet"}, setup: func(_ *Service, st *settingsFaultStore) {
			st.failKey(modelProfileStateKey, errors.New("state unavailable"))
		}},
		{name: "raw JSON", key: modelRecordKey("kb"), bind: ModelBinding{Provider: "launcher.chat", Model: "sonnet"}, setup: func(_ *Service, st *settingsFaultStore) {
			st.memStore.mu.Lock()
			st.memStore.vals[modelKey("broken")] = []byte("{")
			st.memStore.mu.Unlock()
		}},
		{name: "unknown inherited purpose", key: modelKey("unknown.inherited"), bind: ModelBinding{Model: "sonnet"}},
		{name: "batch", key: modelRecordKey("kb"), bind: ModelBinding{Provider: "launcher.chat", Model: "sonnet"}, setup: func(_ *Service, st *settingsFaultStore) {
			st.failSecondPut = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, st := chatAliasFixture(t)
			putAliasRecord(t, st, tc.key, 3, tc.bind)
			if tc.setup != nil {
				tc.setup(s, st)
			}
			before, puts := profileStoreSnapshot(st.memStore)
			if code, body := clearChatAliasTiers(t, s); code == 200 {
				t.Fatalf("save succeeded: %s", body)
			}
			requireProfileStoreUnchanged(t, st.memStore, before, puts)
			if f.eventTypes() != "" {
				t.Fatalf("failed save emitted %q", f.eventTypes())
			}
		})
	}
}

func TestChatSaveTierAliasUsesOneManagedSnapshot(t *testing.T) {
	s, _, st := chatAliasFixture(t)
	putAliasRecord(t, st, modelRecordKey("kb"), 1, ModelBinding{Model: "sonnet"})
	st.onRead = func(st *settingsFaultStore, key string, n int) {
		if key == launchersManagedKey && n > 2 {
			st.failRead = key
		}
	}
	if code, body := clearChatAliasTiers(t, s); code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	if got := st.readCount(launchersManagedKey); got != 2 {
		t.Fatalf("managed provider reads = %d, want the route and save reads only", got)
	}
	if got := aliasRecord(t, st, modelRecordKey("kb")); got.Binding != (ModelBinding{Model: "gw/sonnet"}) || got.Revision != 2 {
		t.Fatalf("inherited binding = %+v", got)
	}
	st.mu.Lock()
	prefixReads := st.prefixReads
	st.mu.Unlock()
	if prefixReads != 1 {
		t.Fatalf("prefix reads = %d, want 1", prefixReads)
	}
}

type batchWithoutPrefix struct{ Store }

func (st batchWithoutPrefix) PutSettings(ctx context.Context, values map[string]any) error {
	return st.Store.(interface {
		PutSettings(context.Context, map[string]any) error
	}).PutSettings(ctx, values)
}

func TestChatSaveTierAliasRequiresCompleteRecordEnumeration(t *testing.T) {
	f := newFakeHub().withMachine()
	f.ownModels = true
	mem := newSettingsFaultStore()
	s := newServiceOn(t, f, batchWithoutPrefix{Store: mem})
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "chat", Kind: "api", API: "chat",
		BaseURL: "https://chat.example.com", Model: "gw/default", SonnetModel: "gw/sonnet"}})
	putAliasRecord(t, mem, modelRecordKey("kb"), 1, ModelBinding{Provider: "launcher.chat", Model: "sonnet"})
	before, puts := profileStoreSnapshot(mem.memStore)
	if code, body := clearChatAliasTiers(t, s); code == 200 {
		t.Fatalf("save succeeded: %s", body)
	}
	requireProfileStoreUnchanged(t, mem.memStore, before, puts)
}

func TestAPIConfigLeavesTierAliasForCLIRun(t *testing.T) {
	s, _, _ := modelService(t)
	c, err := s.apiConfig(context.Background(), ModelBinding{Provider: "launcher.claude", Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	if c.RunLauncher != "claude" || c.Model != "sonnet" {
		t.Fatalf("run=%q model=%q, want the alias passed to the CLI as is", c.RunLauncher, c.Model)
	}
}
