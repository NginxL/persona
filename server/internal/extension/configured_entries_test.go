package extension

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension/egress"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestConfiguredEntryListTargetsAndConsent(t *testing.T) {
	m := writeManifest(t, writeEntryPackage(t, "1.2.0"))
	networks := m.Permissions.ConfiguredNetworks()
	if len(networks) != 1 || networks[0] != (ConfiguredNetwork{List: "accounts", Field: "address"}) {
		t.Fatalf("configured networks: %+v", networks)
	}
	config := json.RawMessage(`{"accounts":[
	  {"_id":"entry_one","name":"One","address":"https://ONE.example.com/v1"},
	  {"_id":"entry_two","name":"Two","address":"http://127.0.0.1:8080"},
	  {"_id":"entry_three","name":"Three","address":"http://external.example.com"},
	  {"_id":"entry_four","name":"Four","address":"https://user:placeholder@private.example.com"},
	  {"_id":"entry_five","name":"Five","address":"https://two.example.com:9443"}
	]}`)
	targets := ConfiguredTargets(m, config)
	want := []egress.Target{{Host: "one.example.com", Port: "443"}, {Host: "127.0.0.1", Port: "8080", HTTP: true}, {Host: "two.example.com", Port: "9443"}}
	if len(targets) != len(want) {
		t.Fatalf("targets: %+v, want %+v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Errorf("target %d: %+v, want %+v", i, targets[i], want[i])
		}
	}
	var network, secret string
	for _, line := range ExplainWith(i18n.En, m, config) {
		if line.Key == "network_config" {
			network = line.Detail
		}
		if line.Key == "secrets" && strings.Contains(line.Title, "each entry") {
			secret = line.Detail
		}
	}
	for _, text := range []string{"one.example.com:443", "127.0.0.1:8080", "two.example.com:9443", "future entry"} {
		if !strings.Contains(network, text) {
			t.Errorf("network consent %q does not contain %q", network, text)
		}
	}
	if strings.Contains(network, "external.example.com") || strings.Contains(network, "private.example.com") {
		t.Fatalf("invalid addresses became consent targets: %q", network)
	}
	if !strings.Contains(secret, "current and future entry") || !strings.Contains(secret, "write-only") || !strings.Contains(secret, "sealed") {
		t.Fatalf("entry secret consent: %q", secret)
	}
	before := Digest(m)
	withAnother := json.RawMessage(`{"accounts":[{"_id":"entry_one","address":"https://one.example.com"},{"_id":"entry_new","address":"https://new.example.com"}]}`)
	if Digest(m) != before {
		t.Fatal("runtime entries changed the manifest digest")
	}
	if added := AddedWith(i18n.En, m, m, withAnother); len(added) != 0 {
		t.Fatalf("adding an entry asked for new manifest consent: %+v", added)
	}
}

func TestEntryListUpgradeFromFixedSecretAsksForPerEntryConsent(t *testing.T) {
	next := writeManifest(t, writeEntryPackage(t, "1.2.0"))
	old := *next
	old.Configuration = json.RawMessage(`{"type":"object","properties":{"account_name":{"type":"string"},"account_address":{"type":"string","format":"uri"}}}`)
	old.Secrets = []SecretSpec{{Name: "account_key", Title: "Account key", Hint: HintLast4}}
	added := AddedWith(i18n.En, &old, next, nil)
	if len(added) != 1 || added[0].Key != "secrets" || !strings.Contains(added[0].Title, "entry lists") || !strings.Contains(added[0].Detail, "current and future entry") {
		t.Fatalf("fixed-to-entry secret upgrade consent: %+v", added)
	}
	if addedAgain := AddedWith(i18n.En, next, next, nil); len(addedAgain) != 0 {
		t.Fatalf("an unchanged entry declaration asked again: %+v", addedAgain)
	}
}

func TestEntryListUpgradeToFixedSecretAsksForFixedConsent(t *testing.T) {
	old := writeManifest(t, writeEntryPackage(t, "1.2.0"))
	old.Permissions.Network = nil
	next := *old
	next.Configuration = json.RawMessage(`{"type":"object","properties":{"account_name":{"type":"string"},"account_address":{"type":"string","format":"uri"}}}`)
	next.Secrets = []SecretSpec{{Name: "account_key", Title: "Account key", Hint: HintLast4}}
	added := AddedWith(i18n.En, old, &next, nil)
	if len(added) != 1 || added[0].Key != "secrets" || strings.Contains(added[0].Title, "entry lists") {
		t.Fatalf("entry-to-fixed secret upgrade consent: %+v", added)
	}
	current := &Extension{Full: old, Record: Record{Granted: old.Permissions}}
	if !MorePermissions(current, &next) {
		t.Fatal("a new fixed secret inherited entry-list consent")
	}
}

func TestConfiguredEntryListHTTP(t *testing.T) {
	m := writeManifest(t, writeEntryPackage(t, "1.2.0"))
	m.Permissions.Network = []string{"config:accounts[].address:http"}
	config := json.RawMessage(`{"accounts":[{"_id":"entry_one","address":"http://one.example.com"}]}`)
	targets := ConfiguredTargets(m, config)
	if len(targets) != 1 || targets[0] != (egress.Target{Host: "one.example.com", Port: "80", HTTP: true}) {
		t.Fatalf("http entry target: %+v", targets)
	}
}

func TestConfiguredEntryListRefusesMalformedPaths(t *testing.T) {
	for _, path := range []string{
		"config:accounts[0].address",
		"config:accounts[].address.extra",
		"config:accounts[].address:ftp",
		"config:accounts[].address:http:extra",
		"config:accounts[].name",
		"config:accounts[].missing",
		"config:include_shared[].address",
		"config:[].address",
		"config:accounts[]",
	} {
		t.Run(path, func(t *testing.T) {
			pkg := t.TempDir()
			writePackage(t, pkg, func(m map[string]any) {
				m["configuration"] = json.RawMessage(`{"type":"object","properties":{"accounts":{"type":"array","maxItems":2,"x-secret":{"name":"account_key","title":"Account key"},"items":{"type":"object","properties":{"name":{"type":"string"},"address":{"type":"string","format":"uri"}}}},"include_shared":{"type":"boolean"}}}`)
				m["secrets"] = []any{}
				m["permissions"] = map[string]any{"network": []any{path}}
			})
			if _, err := Parse(manifestBytes(t, pkg)); err == nil {
				t.Fatalf("malformed configured path parsed: %s", path)
			}
		})
	}
}

func manifestBytes(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(ManifestPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
