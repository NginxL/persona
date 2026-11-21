package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

func writeEntryPackage(t *testing.T, version string) string {
	t.Helper()
	return writeEntryPackageNetwork(t, version, []any{"config:accounts[].address"})
}

func writeEntryPackageNetwork(t *testing.T, version string, network []any) string {
	t.Helper()
	return writeEntryPackageContract(t, version, network, true)
}

func writeEntryPackageContract(t *testing.T, version string, network []any, settingsCheck bool) string {
	t.Helper()
	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["version"] = version
		m["configuration"] = json.RawMessage(`{"type":"object","properties":{
		  "accounts":{"type":"array","title":"Accounts","maxItems":4,"x-secret":{"name":"account_key","title":"Account key","hint":"last4"},"items":{"type":"object","properties":{"name":{"type":"string","title":"Name"},"address":{"type":"string","title":"Address","format":"uri"}},"required":["name","address"]}},
		  "include_shared":{"type":"boolean","default":false}
		}}`)
		m["secrets"] = []any{}
		m["settings_check"] = settingsCheck
		m["permissions"] = map[string]any{"network": network}
	})
	return pkg
}

func installEntryPackage(t *testing.T, r *Registry) {
	t.Helper()
	pv, err := r.Preview(context.Background(), "dir:"+writeEntryPackage(t, "1.2.0"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(context.Background(), pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
}

func savedEntries(t *testing.T, r *Registry) []Entry {
	t.Helper()
	p := r.Get("example-notes")
	var config map[string]json.RawMessage
	if err := json.Unmarshal(r.SavedConfig(p), &config); err != nil {
		t.Fatal(err)
	}
	entries, err := decodeEntries(p.Schema.Fields[0], config["accounts"])
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func saveEntryConfig(t *testing.T, r *Registry, raw string) []string {
	t.Helper()
	cleared, err := r.SetConfigClearing(context.Background(), "example-notes", json.RawMessage(raw), "test")
	if err != nil {
		t.Fatal(err)
	}
	return cleared
}

func TestEntryConfigLifecycle(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	installEntryPackage(t, r)
	saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"https://one.example.com/v1"},{"name":"Two","address":"https://two.example.com"}],"include_shared":true}`)
	entries := savedEntries(t, r)
	if len(entries) != 2 {
		t.Fatalf("entries: %+v", entries)
	}
	for _, entry := range entries {
		if len(entry.ID) != 32 || !strings.HasPrefix(entry.ID, entryIDGenerated) || !secretPattern.MatchString(entry.ID) {
			t.Fatalf("host id: %q", entry.ID)
		}
	}
	if entries[0].ID == entries[1].ID {
		t.Fatal("entry ids collide")
	}
	first, second := entries[0].ID, entries[1].ID
	for _, id := range []string{first, second} {
		if err := r.SetSecret(ctx, "example-notes", id, "placeholder-secret-1234", "test"); err != nil {
			t.Fatal(err)
		}
	}
	secrets, err := r.Secrets(ctx, "example-notes")
	if err != nil || len(secrets) != 2 || secrets[first] != "placeholder-secret-1234" {
		t.Fatalf("active secrets: %+v, %v", secrets, err)
	}
	store.secrets["example-notes"]["orphan"] = "sealed:orphan"
	secrets, err = r.Secrets(ctx, "example-notes")
	if err != nil || len(secrets) != 2 {
		t.Fatalf("orphan handed over: %+v, %v", secrets, err)
	}
	if set := r.SecretsSet(ctx, r.Get("example-notes")); !set[first] || !set[second] || set["orphan"] {
		t.Fatalf("set projection: %+v", set)
	}

	bad := `{"accounts":[{"_id":"client_chosen","name":"One","address":"https://one.example.com"}]}`
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(bad), "test"); err == nil {
		t.Fatal("a client resurrected an unknown id")
	}
	if got := savedEntries(t, r); len(got) != 2 {
		t.Fatal("refused write changed entries")
	}

	// An older client can omit a list, or submit a stale array: neither may delete newer entries.
	if _, err := r.SetConfigPreservingEntries(ctx, "example-notes", json.RawMessage(`{"include_shared":false}`), "native"); err != nil {
		t.Fatal(err)
	}
	stale := `{"accounts":[{"_id":"` + first + `","name":"One","address":"https://one.example.com/v1"}],"include_shared":true}`
	if _, err := r.SetConfigPreservingEntries(ctx, "example-notes", json.RawMessage(stale), "native"); err != nil {
		t.Fatal(err)
	}
	if got := savedEntries(t, r); len(got) != 2 || got[0].ID != first || got[1].ID != second {
		t.Fatalf("legacy draft changed entries: %+v", got)
	}

	// A spelling change with the same normalized target keeps the credential.
	cleared := saveEntryConfig(t, r, `{"accounts":[{"_id":"`+first+`","name":"One","address":"https://ONE.example.com:443/other"},{"_id":"`+second+`","name":"Two","address":"https://two.example.com"}]}`)
	if len(cleared) != 0 {
		t.Fatalf("equivalent target cleared secrets: %+v", cleared)
	}
	secrets, _ = r.Secrets(ctx, "example-notes")
	if len(secrets) != 2 {
		t.Fatalf("equivalent target lost a secret: %+v", secrets)
	}

	// Rebinding and removal delete exactly the affected sealed rows with the config write.
	cleared = saveEntryConfig(t, r, `{"accounts":[{"_id":"`+first+`","name":"One","address":"https://new.example.com"}]}`)
	if !slices.Contains(cleared, first) || !slices.Contains(cleared, second) || len(cleared) != 2 {
		t.Fatalf("cleared: %+v, want %s and %s", cleared, first, second)
	}
	if _, ok := store.secrets["example-notes"][first]; ok {
		t.Fatal("rebound secret survived")
	}
	if _, ok := store.secrets["example-notes"][second]; ok {
		t.Fatal("removed entry secret survived")
	}
	secrets, err = r.Secrets(ctx, "example-notes")
	if err != nil || len(secrets) != 0 {
		t.Fatalf("deleted secrets handed over: %+v, %v", secrets, err)
	}
}

func TestEntryConfigDevelopmentAndLocalizedReadiness(t *testing.T) {
	ctx := context.Background()
	t.Run("development config", func(t *testing.T) {
		r, _, _ := newTestRegistry(t)
		p, err := r.AddDev(writeEntryPackage(t, "1.2.0"), json.RawMessage(`{"accounts":[{"_id":"entry_developer","name":"Saved","address":"https://saved.example.com"},{"name":"New","address":"https://new.example.com"}]}`), map[string]string{"entry_developer": "placeholder-secret-dev"})
		if err != nil || p.Record.Source != SourceDev {
			t.Fatalf("add development entry list: %v", err)
		}
		entries := savedEntries(t, r)
		if len(entries) != 2 || entries[0].ID != "entry_developer" || !strings.HasPrefix(entries[1].ID, entryIDGenerated) {
			t.Fatalf("development identities: %+v", entries)
		}
		secrets, err := r.Secrets(ctx, "example-notes")
		if err != nil || len(secrets) != 1 || secrets["entry_developer"] != "placeholder-secret-dev" {
			t.Fatalf("development secrets: %+v, %v", secrets, err)
		}
		if _, err := r.AddDev(writeEntryPackage(t, "1.2.0"), json.RawMessage(`{}`), map[string]string{"unknown_entry": "placeholder-secret-unknown"}); err == nil {
			t.Fatal("development config accepted a secret with no fixed or live entry declaration")
		}
	})

	t.Run("localized required entry secret", func(t *testing.T) {
		r, _, _ := newTestRegistry(t)
		installEntryPackage(t, r)
		saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"https://one.example.com"}]}`)
		id := savedEntries(t, r)[0].ID
		p := r.Get("example-notes")
		p.L10n = Translations{i18n.En: &Translation{
			Configuration: map[string]*FieldText{"accounts": {Title: "Translated accounts", Properties: map[string]*FieldText{"name": {Title: "Translated name"}}}},
			Secrets:       map[string]*SecretText{"account_key": {Title: "Translated key"}},
		}}
		form := p.L10n.Schema(i18n.En, p.Schema)
		if form.Fields[0].Title != "Translated accounts" || form.Fields[0].Items.Fields[0].Title != "Translated name" || form.Fields[0].Secret.Title != "Translated key" {
			t.Fatalf("localized entry form: %+v", form.Fields[0])
		}
		if p.Schema.Fields[0].Secret.Title != "Account key" || p.Schema.Fields[0].Items.Fields[0].Title != "Name" {
			t.Fatal("localization mutated the shared schema")
		}
		candidate := snapshotExtension(p)
		candidate.Schema = form
		candidate.Schema.Fields[0].Secret.Required = true
		missing := r.dependencyGraph(ctx).localMissing(candidate)
		if !strings.Contains(strings.Join(missing, " "), "Translated key (One)") {
			t.Fatalf("required entry secret is not entry-labelled: %v", missing)
		}
		if err := r.SetSecret(ctx, "example-notes", id, "placeholder-secret-ready", "test"); err != nil {
			t.Fatal(err)
		}
		if missing := r.dependencyGraph(ctx).localMissing(candidate); len(missing) != 0 {
			t.Fatalf("set entry secret is still missing: %v", missing)
		}
	})
}

func TestEntryConfigSettingsCheckSeesPostCleanupSecrets(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	installEntryPackage(t, r)
	var checkedConfig json.RawMessage
	var checked map[string]string
	r.SettingsCheck = func(_ context.Context, _ *Extension, config json.RawMessage, secrets map[string]string) error {
		checkedConfig = slices.Clone(config)
		checked = map[string]string{}
		for name, value := range secrets {
			checked[name] = value
		}
		return nil
	}
	saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"https://one.example.com"},{"name":"Two","address":"https://two.example.com"}]}`)
	entries := savedEntries(t, r)
	first, second := entries[0].ID, entries[1].ID
	for _, id := range []string{first, second} {
		if err := r.SetSecret(context.Background(), "example-notes", id, "placeholder-secret-1234", "test"); err != nil {
			t.Fatal(err)
		}
	}
	saveEntryConfig(t, r, `{"accounts":[{"_id":"`+first+`","name":"One","address":"https://new.example.com"}]}`)
	if len(checked) != 0 {
		t.Fatalf("scheduled deletions reached settings.check: %+v", checked)
	}
	if !strings.Contains(string(checkedConfig), "new.example.com") {
		t.Fatalf("wrong proposed config: %s", checkedConfig)
	}
	if err := r.SetSecret(context.Background(), "example-notes", second, "placeholder-secret-1234", "test"); err == nil {
		t.Fatal("a removed entry accepted a secret")
	}
}

func TestEntrySecretNeedsValidSavedAddress(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	installEntryPackage(t, r)
	saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"https://one.example.com"}]}`)
	id := savedEntries(t, r)[0].ID
	if err := r.SetSecret(context.Background(), "example-notes", id, "placeholder-secret-1234", "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSecret(context.Background(), "example-notes", "not_an_entry", "placeholder-secret-1234", "test"); err == nil {
		t.Fatal("unknown dynamic secret accepted")
	}
	saveEntryConfig(t, r, `{"accounts":[]}`)
	if err := r.DeleteSecret(context.Background(), "example-notes", id, "test"); err == nil {
		t.Fatal("removed entry remained secret-addressable")
	}
}

func TestEntryConfigRefusesStoreWithoutAtomicDeletion(t *testing.T) {
	r, store, _ := newTestRegistry(t)
	installEntryPackage(t, r)
	saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"https://one.example.com"}]}`)
	id := savedEntries(t, r)[0].ID
	if err := r.SetSecret(context.Background(), "example-notes", id, "placeholder-secret-1234", "test"); err != nil {
		t.Fatal(err)
	}
	original := r.store
	r.store = storeWithoutPlan{Store: store}
	cleared, err := r.SetConfigClearing(context.Background(), "example-notes", json.RawMessage(`{"accounts":[]}`), "test")
	r.store = original
	if !errors.Is(err, ErrSecretNotCleared) || len(cleared) != 0 {
		t.Fatalf("non-atomic store: cleared=%v err=%v", cleared, err)
	}
	if len(savedEntries(t, r)) != 1 {
		t.Fatal("config changed without atomic deletion")
	}
	if _, ok := store.secrets["example-notes"][id]; !ok {
		t.Fatal("secret deleted without config commit")
	}
}

type storeWithoutPlan struct{ Store }

func TestEntryConfigRejectsNullEntries(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	installEntryPackage(t, r)
	if err := r.SetConfig(context.Background(), "example-notes", json.RawMessage(`{"accounts":[null]}`), "test"); err == nil {
		t.Fatal("a null entry was accepted")
	} else if !strings.Contains(err.Error(), "accounts[0]") {
		t.Fatalf("null entry error has no indexed path: %v", err)
	}
}

func TestEntryConfigLegacyWriteAllowsNewRequiredEntryFields(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	installEntryPackage(t, r)
	saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"https://one.example.com"}]}`)
	id := savedEntries(t, r)[0].ID

	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["version"] = "1.3.0"
		m["configuration"] = json.RawMessage(`{"type":"object","properties":{
		  "accounts":{"type":"array","title":"Accounts","maxItems":4,"x-secret":{"name":"account_key","title":"Account key"},"items":{"type":"object","properties":{"name":{"type":"string"},"address":{"type":"string","format":"uri"},"region":{"type":"string"}},"required":["name","address","region"]}},
		  "include_shared":{"type":"boolean","default":false}
		}}`)
		m["secrets"] = []any{}
		m["permissions"] = map[string]any{"network": []any{"config:accounts[].address"}}
	})
	pv, err := r.Preview(ctx, "dir:"+pkg, "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upgrade(ctx, "example-notes", pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetConfigPreservingEntries(ctx, "example-notes", json.RawMessage(`{"include_shared":true}`), "native"); err != nil {
		t.Fatalf("an untouched saved entry blocked a legacy write: %v", err)
	}
	strict := `{"accounts":[{"_id":"` + id + `","name":"One","address":"https://one.example.com"}],"include_shared":false}`
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(strict), "test"); err == nil || !strings.Contains(err.Error(), "accounts[0].region") {
		t.Fatalf("a capable edit did not require the new field: %v", err)
	}
}

func TestEntryConfigConcurrentLegacyWrites(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	installEntryPackage(t, r)
	saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"https://one.example.com"}]}`)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for edit := range 20 {
				raw := json.RawMessage(fmt.Sprintf(`{"include_shared":%t}`, (worker+edit)%2 == 0))
				if _, err := r.SetConfigPreservingEntries(context.Background(), "example-notes", raw, "native"); err != nil {
					if errors.Is(err, ErrSettingsMoved) {
						continue // another prepared write won the configuration compare-and-swap
					}
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := savedEntries(t, r); len(got) != 1 {
		t.Fatalf("concurrent legacy writes changed entries: %+v", got)
	}
}

func TestEntryConfigManifestMovedDuringSettingsCheck(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	pv, err := r.Preview(ctx, "dir:"+writeEntryPackageNetwork(t, "1.2.0", []any{}), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"https://one.example.com"}]}`)
	id := savedEntries(t, r)[0].ID
	if err := r.SetSecret(ctx, "example-notes", id, "placeholder-secret-1234", "test"); err != nil {
		t.Fatal(err)
	}

	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.SettingsCheck = func(_ context.Context, _ *Extension, config json.RawMessage, _ map[string]string) error {
		if strings.Contains(string(config), "new.example.com") {
			once.Do(func() { close(started) })
			<-release
		}
		return nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := r.SetConfigClearing(ctx, "example-notes", json.RawMessage(`{"accounts":[{"_id":"`+id+`","name":"One","address":"https://new.example.com"}]}`), "test")
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("settings check did not start")
	}
	upgraded, err := r.Preview(ctx, "dir:"+writeEntryPackageNetwork(t, "1.3.0", []any{"config:accounts[].address"}), "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upgrade(ctx, "example-notes", upgraded.ID, upgraded.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, ErrSettingsMoved) {
			t.Fatalf("write prepared under the replaced manifest: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("settings write did not finish")
	}
	if got := savedEntries(t, r); string(got[0].Values["address"]) != `"https://one.example.com"` {
		t.Fatalf("refused write changed the entry: %+v", got[0])
	}
	secrets, err := r.Secrets(ctx, "example-notes")
	if err != nil || secrets[id] == "" {
		t.Fatalf("refused write cleared the credential: %+v, %v", secrets, err)
	}

	// A retry is prepared under the new egress contract and removes the old credential with the address change.
	r.SettingsCheck = nil
	cleared, err := r.SetConfigClearing(ctx, "example-notes", json.RawMessage(`{"accounts":[{"_id":"`+id+`","name":"One","address":"https://new.example.com"}]}`), "test")
	if err != nil || !slices.Equal(cleared, []string{id}) {
		t.Fatalf("retry under the new manifest: cleared=%v err=%v", cleared, err)
	}
	secrets, err = r.Secrets(ctx, "example-notes")
	if err != nil || secrets[id] != "" {
		t.Fatalf("rebound credential survived the retry: %+v, %v", secrets, err)
	}
}

func TestEntrySecretCheckStateSurvivesManifestUpgrade(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	pv, err := r.Preview(ctx, "dir:"+writeEntryPackageContract(t, "1.2.0", []any{"config:accounts[].address"}, false), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"https://one.example.com"}]}`)
	id := savedEntries(t, r)[0].ID
	checked, err := r.checkSettings(ctx, "example-notes", nil, id, "placeholder-secret-1234")
	if err != nil || checked == nil {
		t.Fatalf("an unchecked old manifest left no compare-and-swap state: checked=%v err=%v", checked, err)
	}
	upgraded, err := r.Preview(ctx, "dir:"+writeEntryPackageContract(t, "1.3.0", []any{"config:accounts[].address"}, true), "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upgrade(ctx, "example-notes", upgraded.ID, upgraded.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.stillChecked(r.Get("example-notes"), checked); !errors.Is(err, ErrSettingsMoved) {
		t.Fatalf("old unchecked state survived the new settings-check contract: %v", err)
	}
	called := 0
	r.SettingsCheck = func(_ context.Context, _ *Extension, _ json.RawMessage, _ map[string]string) error {
		called++
		return nil
	}
	if err := r.SetSecret(ctx, "example-notes", id, "placeholder-secret-1234", "test"); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("the new manifest's settings.check did not run before the retry: %d", called)
	}
}

func TestEntrySecretsSetConcurrentConfigWrites(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	installEntryPackage(t, r)
	saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"https://one.example.com"}]}`)
	id := savedEntries(t, r)[0].ID
	if err := r.SetSecret(ctx, "example-notes", id, "placeholder-secret-1234", "test"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				p := r.Get("example-notes")
				if set := r.SecretsSet(ctx, p); !set[id] {
					errs <- errors.New("concurrent secret projection lost the live entry")
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for edit := range 100 {
			raw := json.RawMessage(fmt.Sprintf(`{"include_shared":%t}`, edit%2 == 0))
			if _, err := r.SetConfigPreservingEntries(ctx, "example-notes", raw, "native"); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestEntrySecretAddressValidatedBeforeSettingsCheck(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	installEntryPackage(t, r)
	saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"ftp://one.example.com"}]}`)
	id := savedEntries(t, r)[0].ID
	called := 0
	r.SettingsCheck = func(_ context.Context, _ *Extension, _ json.RawMessage, _ map[string]string) error {
		called++
		return nil
	}
	err := r.SetSecret(ctx, "example-notes", id, "placeholder-secret-1234", "test")
	var message *i18n.Message
	if !errors.As(err, &message) || message.Key != "extension-status.entry_address_required" {
		t.Fatalf("invalid entry address: %v", err)
	}
	if called != 0 {
		t.Fatal("settings.check received a credential whose entry address was already invalid")
	}
	saveEntryConfig(t, r, `{"accounts":[{"_id":"`+id+`","name":"One","address":"https://one.example.com"}]}`)
	called = 0 // the valid configuration's own settings.check is not the secret write under test
	if err := r.SetSecret(ctx, "example-notes", id, "placeholder-secret-1234", "test"); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("valid entry secret checks: %d", called)
	}
}

type failingSecretDeleteStore struct {
	*memStore
	fail string
}

func (s *failingSecretDeleteStore) DeleteExtensionSecret(ctx context.Context, id, name string) error {
	if name == s.fail {
		return errors.New("injected deletion failure")
	}
	return s.memStore.DeleteExtensionSecret(ctx, id, name)
}

func TestConfigCleanupFailureStillAuditsAndRestarts(t *testing.T) {
	ctx := context.Background()
	r, store, changes := newTestRegistry(t)
	pv, err := r.Preview(ctx, "dir:"+writeFixedSlotPackage(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSecret(ctx, "example-notes", "api_key", "placeholder-secret-1234", "test"); err != nil {
		t.Fatal(err)
	}
	r.BoundSecrets = func(*Extension, json.RawMessage, json.RawMessage) []string { return []string{"api_key", "other_key"} }
	r.store = &failingSecretDeleteStore{memStore: store, fail: "other_key"}
	changesBefore := len(*changes)
	cleared, err := r.SetConfigClearing(ctx, "example-notes", json.RawMessage(`{"base_url":"https://new.example.com"}`), "test")
	if !errors.Is(err, ErrSecretNotCleared) || !slices.Equal(cleared, []string{"api_key"}) {
		t.Fatalf("partial cleanup: cleared=%v err=%v", cleared, err)
	}
	if len(*changes) <= changesBefore || (*changes)[len(*changes)-1].Kind != "secret" {
		t.Fatalf("partial cleanup did not notify a new secret change: %+v", *changes)
	}
	joined := strings.Join(store.audit, "\n")
	if !strings.Contains(joined, "secret_delete") || !strings.Contains(joined, "api_key") {
		t.Fatalf("partial cleanup was not audited: %s", joined)
	}
}
