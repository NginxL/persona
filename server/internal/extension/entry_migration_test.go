package extension

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension/egress"
	"github.com/nwverse/persona/server/internal/i18n"
)

func writeFixedSlotPackage(t *testing.T) string {
	t.Helper()
	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["configuration"] = json.RawMessage(`{"type":"object","properties":{
		  "base_url":{"type":"string","title":"Service address","format":"uri","default":"https://default.example.com"},
		  "account_name":{"type":"string","title":"Account name"},
		  "include_shared":{"type":"boolean","default":false}
		},"required":["base_url"]}`)
		m["permissions"] = map[string]any{"network": []any{"notes.example.com"}}
	})
	return pkg
}

func writeMigratedEntryPackage(t *testing.T, version string) string {
	t.Helper()
	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["version"] = version
		m["configuration"] = json.RawMessage(`{"type":"object","properties":{
		  "accounts":{"type":"array","title":"Accounts","maxItems":4,"x-secret":{"name":"api_key","title":"API Key","required":true},"x-from":{"name":"account_name","address":"base_url"},"items":{"type":"object","properties":{"name":{"type":"string","title":"Name"},"address":{"type":"string","title":"Address","format":"uri"},"note":{"type":"string","title":"Note"}},"required":["name","address"]}},
		  "include_shared":{"type":"boolean","default":false}
		}}`)
		m["secrets"] = []any{}
		m["permissions"] = map[string]any{"network": []any{"config:accounts[].address"}}
	})
	return pkg
}

func installFixedSlot(t *testing.T, r *Registry) {
	t.Helper()
	pv, err := r.Preview(context.Background(), "dir:"+writeFixedSlotPackage(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(context.Background(), pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestFixedSlotMigrationPreservesSealedSecretAndRollback(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	installFixedSlot(t, r)
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"base_url":"https://one.example.com:8443/v1","account_name":"One","include_shared":true}`), "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSecret(ctx, "example-notes", "api_key", "placeholder-secret-1234", "test"); err != nil {
		t.Fatal(err)
	}
	sealedBefore := store.secrets["example-notes"]["api_key"]

	pv, err := r.Preview(ctx, "dir:"+writeMigratedEntryPackage(t, "1.3.0"), "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	var previewConfig map[string]json.RawMessage
	_ = json.Unmarshal(pv.config, &previewConfig)
	if _, ok := previewConfig["base_url"]; ok {
		t.Fatalf("legacy field remained in preview: %s", pv.config)
	}
	var entries []map[string]string
	if err := json.Unmarshal(previewConfig["accounts"], &entries); err != nil || len(entries) != 1 {
		t.Fatalf("preview entries: %s, %v", previewConfig["accounts"], err)
	}
	if entries[0][entryIDField] != "api_key" || entries[0]["name"] != "One" || entries[0]["address"] != "https://one.example.com:8443/v1" {
		t.Fatalf("migrated preview entry: %+v", entries[0])
	}
	var permissionText string
	for _, line := range pv.In(i18n.En).Permissions {
		permissionText += line.Title + " " + line.Detail + "\n"
	}
	if !strings.Contains(permissionText, "one.example.com:8443") || !strings.Contains(permissionText, "future entry") {
		t.Fatalf("migration consent: %s", permissionText)
	}
	var addedSecrets int
	for _, line := range pv.In(i18n.En).AddedPermissions {
		if line.Key == "secrets" && strings.Contains(line.Detail, "current and future entry") {
			addedSecrets++
		}
	}
	if addedSecrets != 1 {
		t.Fatalf("fixed-to-entry migration did not explain its wider per-entry secret scope: %+v", pv.In(i18n.En).AddedPermissions)
	}

	upgraded, err := r.Upgrade(ctx, "example-notes", pv.ID, pv.Digest, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := store.secrets["example-notes"]["api_key"]; got != sealedBefore {
		t.Fatal("migration rewrote the sealed secret")
	}
	secrets, err := r.Secrets(ctx, "example-notes")
	if err != nil || secrets["api_key"] != "placeholder-secret-1234" || len(secrets) != 1 {
		t.Fatalf("migrated handoff: %+v, %v", secrets, err)
	}
	if !upgraded.IsLiveEntrySecret("api_key") || !r.SecretsSet(ctx, upgraded)["api_key"] {
		t.Fatalf("the migrated secret is not live: config=%s", upgraded.Record.Config)
	}
	if missing := r.Missing(ctx, upgraded); len(missing) != 0 {
		t.Fatalf("migrated required entry secret is still missing: %v", missing)
	}
	reloaded := r.fromRow(upgraded.Record)
	if !reloaded.IsLiveEntrySecret("api_key") {
		t.Fatalf("the migrated secret is not live after reload: config=%s", reloaded.Record.Config)
	}
	targets := ConfiguredTargets(upgraded.Manifest, r.Config(upgraded))
	if len(targets) != 1 || targets[0] != (egress.Target{Host: "one.example.com", Port: "8443"}) {
		t.Fatalf("migrated targets: %+v", targets)
	}
	again, declared, err := migrateFixedSlots(writeManifest(t, writeFixedSlotPackage(t)), upgraded.Full, upgraded.Record.Config, store.secrets["example-notes"])
	if err != nil || !declared || !sameJSON(again, upgraded.Record.Config) {
		t.Fatalf("repeated migration is not idempotent: %s, %v, %v", again, declared, err)
	}

	rolledBack, err := r.Rollback(ctx, "example-notes", "test")
	if err != nil {
		t.Fatal(err)
	}
	var oldConfig map[string]any
	_ = json.Unmarshal(r.SavedConfig(rolledBack), &oldConfig)
	if oldConfig["base_url"] != "https://one.example.com:8443/v1" || oldConfig["account_name"] != "One" || oldConfig["include_shared"] != true {
		t.Fatalf("rollback config: %+v", oldConfig)
	}
	if _, exists := oldConfig["accounts"]; exists {
		t.Fatal("rollback kept the list")
	}
	if got := store.secrets["example-notes"]["api_key"]; got != sealedBefore {
		t.Fatal("rollback rewrote the sealed secret")
	}
}

func TestEntryIDCannotCollideWithNewFixedSecret(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	installFixedSlot(t, r)
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"base_url":"https://one.example.com","account_name":"One"}`), "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSecret(ctx, "example-notes", "api_key", "placeholder-secret-1234", "test"); err != nil {
		t.Fatal(err)
	}
	pv, err := r.Preview(ctx, "dir:"+writeMigratedEntryPackage(t, "1.3.0"), "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := r.Upgrade(ctx, "example-notes", pv.ID, pv.Digest, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !upgraded.IsLiveEntrySecret("api_key") {
		t.Fatal("the migrated fixed secret did not become a live entry secret")
	}

	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["version"] = "1.4.0"
		m["configuration"] = json.RawMessage(`{"type":"object","properties":{
		  "accounts":{"type":"array","title":"Accounts","maxItems":4,"x-secret":{"name":"account_key","title":"Account key"},"items":{"type":"object","properties":{"name":{"type":"string"},"address":{"type":"string","format":"uri"}},"required":["name","address"]}}
		}}`)
		m["secrets"] = []any{map[string]any{"name": "api_key", "title": "Fixed API key"}}
		m["permissions"] = map[string]any{"network": []any{"config:accounts[].address"}}
	})
	if _, err := r.Preview(ctx, "dir:"+pkg, "example-notes"); err == nil || !strings.Contains(err.Error(), "accounts[0]._id") {
		t.Fatalf("an entry/fixed storage collision was not refused at preview: %v", err)
	}

	// A previously persisted ambiguous row is marked broken and never treated as a live entry credential.
	manifest := *upgraded.Full
	manifest.Secrets = []SecretSpec{{Name: "api_key", Title: "Fixed API key"}}
	rawManifest, _ := json.Marshal(&manifest)
	row := upgraded.Record
	row.Manifest = rawManifest
	loaded := r.fromRow(row)
	if loaded.Problem == "" || loaded.IsLiveEntrySecret("api_key") {
		t.Fatalf("ambiguous saved identity became live after loading: %+v", loaded)
	}
}

func TestEntryListRollbackRefusesConfigurationPreviousSchemaCannotRepresent(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	installFixedSlot(t, r)
	pv, err := r.Preview(ctx, "dir:"+writeEntryPackage(t, "1.3.0"), "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upgrade(ctx, "example-notes", pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	saveEntryConfig(t, r, `{"accounts":[{"name":"One","address":"https://one.example.com"}],"include_shared":true}`)
	id := savedEntries(t, r)[0].ID
	if err := r.SetSecret(ctx, "example-notes", id, "placeholder-secret-1234", "test"); err != nil {
		t.Fatal(err)
	}
	before := slicesClone(r.Get("example-notes").Record.Config)
	if _, err := r.Rollback(ctx, "example-notes", "test"); err == nil || !strings.Contains(err.Error(), "previous version cannot represent") {
		t.Fatalf("rollback stranded entry configuration and its secret: %v", err)
	}
	if !sameJSON(before, r.Get("example-notes").Record.Config) || !r.Get("example-notes").IsLiveEntrySecret(id) {
		t.Fatal("refused rollback changed the entry configuration or secret activation")
	}
}

func TestFixedSlotMigrationDoesNotUseDefaults(t *testing.T) {
	r, _, _ := newTestRegistry(t)
	installFixedSlot(t, r)
	pv, err := r.Preview(context.Background(), "dir:"+writeMigratedEntryPackage(t, "1.3.0"), "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pv.config), "default.example.com") || strings.Contains(string(pv.config), "accounts") {
		t.Fatalf("a default became a user entry: %s", pv.config)
	}
}

func TestFixedSlotMigrationRejectsConfigDrift(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	installFixedSlot(t, r)
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"base_url":"https://one.example.com","account_name":"One"}`), "test"); err != nil {
		t.Fatal(err)
	}
	pv, err := r.Preview(ctx, "dir:"+writeMigratedEntryPackage(t, "1.3.0"), "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"base_url":"https://changed.example.com","account_name":"One"}`), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upgrade(ctx, "example-notes", pv.ID, pv.Digest, "test"); err == nil || !strings.Contains(err.Error(), "重新预览") {
		t.Fatalf("migration drift was not refused: %v", err)
	}
	if !strings.Contains(string(r.Get("example-notes").Record.Config), "changed.example.com") {
		t.Fatal("refused upgrade changed the saved config")
	}
}

func TestFixedSlotRollbackRefusesLaterData(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	installFixedSlot(t, r)
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"base_url":"https://one.example.com","account_name":"One"}`), "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSecret(ctx, "example-notes", "api_key", "placeholder-secret-1234", "test"); err != nil {
		t.Fatal(err)
	}
	pv, err := r.Preview(ctx, "dir:"+writeMigratedEntryPackage(t, "1.3.0"), "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upgrade(ctx, "example-notes", pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	saveEntryConfig(t, r, `{"accounts":[{"_id":"api_key","name":"One","address":"https://one.example.com"},{"name":"Two","address":"https://two.example.com"}]}`)
	before := slicesClone(r.Get("example-notes").Record.Config)
	if _, err := r.Rollback(ctx, "example-notes", "test"); err == nil {
		t.Fatal("rollback discarded a later entry")
	}
	if !sameJSON(before, r.Get("example-notes").Record.Config) {
		t.Fatal("refused rollback changed configuration")
	}

	// Even one entry is not invertible when a new, unmapped field holds data.
	saveEntryConfig(t, r, `{"accounts":[{"_id":"api_key","name":"One","address":"https://one.example.com","note":"keep me"}]}`)
	if _, err := r.Rollback(ctx, "example-notes", "test"); err == nil || !strings.Contains(err.Error(), "no fixed-slot destination") {
		t.Fatalf("rollback discarded an unmapped field: %v", err)
	}
}

func slicesClone(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func writeManifest(t *testing.T, dir string) *Manifest {
	t.Helper()
	raw, err := os.ReadFile(ManifestPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
