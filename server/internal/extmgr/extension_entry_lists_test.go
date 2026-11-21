package extmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

func entryListAPIPackage(t *testing.T) string {
	t.Helper()
	dir := notesPackage(t, "1.0.0")
	raw, err := os.ReadFile(extension.ManifestPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["configuration"] = json.RawMessage(`{"type":"object","properties":{
	  "accounts":{"type":"array","title":"Accounts","maxItems":4,"x-secret":{"name":"account_key","title":"Account key","hint":"last4"},"items":{"type":"object","properties":{"name":{"type":"string","title":"Name"},"address":{"type":"string","title":"Address","format":"uri"}},"required":["name","address"]}},
	  "include_shared":{"type":"boolean","default":false}
	}}`)
	manifest["secrets"] = []any{}
	manifest["settings_check"] = false
	manifest["permissions"] = map[string]any{"network": []any{"config:accounts[].address"}}
	out, _ := json.Marshal(manifest)
	if err := os.WriteFile(extension.ManifestPath(dir), out, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func untitledEntryListAPIPackage(t *testing.T) string {
	t.Helper()
	dir := entryListAPIPackage(t)
	raw, err := os.ReadFile(extension.ManifestPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["id"] = "example-untitled"
	manifest["name"] = "Example untitled"
	manifest["configuration"] = json.RawMessage(`{"type":"object","properties":{
	  "accounts":{"type":"array","title":"Accounts","maxItems":4,"x-secret":{"name":"account_key","title":"Account key"},"items":{"type":"object","properties":{"name":{"type":"string","title":"Name"},"address":{"type":"string","title":"Address","format":"uri"}}}}
	}}`)
	manifest["permissions"] = map[string]any{"network": []any{}}
	out, _ := json.Marshal(manifest)
	if err := os.WriteFile(extension.ManifestPath(dir), out, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func objectAt(t *testing.T, value any, keys ...string) map[string]any {
	t.Helper()
	current, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %#v", value)
	}
	for _, key := range keys {
		next, ok := current[key]
		if !ok {
			t.Fatalf("missing %s in %#v", key, current)
		}
		current, ok = next.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object: %#v", key, next)
		}
	}
	return current
}

func arrayAt(t *testing.T, value map[string]any, key string) []any {
	t.Helper()
	raw, exists := value[key]
	if !exists || raw == nil {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s is not a list: %#v", key, value[key])
	}
	return items
}

func TestExtensionEntryListAPICompatibility(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	pkg := entryListAPIPackage(t)
	code, preview := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, fmt.Sprintf(`{"source":%q}`, "dir:"+pkg))
	if code != 200 {
		t.Fatalf("preview: %d %v", code, preview)
	}
	code, detail := call(t, mux, "POST", "/api/v1/extensions/-/install", master, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"]))
	if code != 200 {
		t.Fatalf("install: %d %v", code, detail)
	}

	code, detail = call(t, mux, "PUT", "/api/v1/extensions/example-notes/-/config?entry_lists=1", master,
		`{"config":{"accounts":[{"name":"One","address":"https://one.example.com"}],"include_shared":true}}`)
	if code != 200 {
		t.Fatalf("entry save: %d %v", code, detail)
	}
	entries := arrayAt(t, objectAt(t, detail, "config"), "accounts")
	if len(entries) != 1 {
		t.Fatalf("entries: %v", entries)
	}
	first := objectAt(t, entries[0])["_id"].(string)
	if first == "" {
		t.Fatal("the hub did not mint an entry id")
	}
	fields := arrayAt(t, objectAt(t, detail, "form"), "fields")
	if len(fields) != 2 {
		t.Fatalf("opted-in form lost fields: %v", detail["form"])
	}
	entryFields := arrayAt(t, objectAt(t, fields[0], "items"), "fields")
	if !objectAt(t, entryFields[1])["clears_secret"].(bool) {
		t.Fatalf("network-bound address lacks its secret-clearing hint: %v", fields[0])
	}

	code, legacy := call(t, mux, "GET", "/api/v1/extensions/example-notes/-/", master, "")
	if code != 200 {
		t.Fatalf("legacy detail: %d %v", code, legacy)
	}
	if len(arrayAt(t, objectAt(t, legacy, "form"), "fields")) != 1 {
		t.Fatalf("legacy form exposes entry editor: %v", legacy["form"])
	}
	if len(arrayAt(t, objectAt(t, legacy, "config"), "accounts")) != 1 {
		t.Fatal("legacy projection dropped raw entries")
	}

	code, legacy = call(t, mux, "PUT", "/api/v1/extensions/example-notes/-/secrets/"+first, master, `{"value":"placeholder-secret-1234"}`)
	if code != 200 {
		t.Fatalf("entry secret: %d %v", code, legacy)
	}
	if len(arrayAt(t, legacy, "secrets")) != 0 {
		t.Fatalf("legacy response exposes dynamic secret rows: %v", legacy["secrets"])
	}
	if raw, _ := json.Marshal(legacy); strings.Contains(string(raw), "placeholder-secret-1234") {
		t.Fatal("secret value entered a detail response")
	}
	code, detail = call(t, mux, "GET", "/api/v1/extensions/example-notes/-/?entry_lists=1", master, "")
	if code != 200 || len(arrayAt(t, detail, "secrets")) != 1 {
		t.Fatalf("opted-in secret view: %d %v", code, detail)
	}
	secret := objectAt(t, arrayAt(t, detail, "secrets")[0])
	if secret["name"] != first || secret["set"] != true || secret["list"] != "accounts" || secret["hint"] != "1234" {
		t.Fatalf("dynamic secret view: %+v", secret)
	}

	nodeExtension := *h.extensions.reg.Get("example-notes")
	nodeManifest := *nodeExtension.Manifest
	nodeManifest.Node = &extension.NodeRuntime{Secrets: []string{first}}
	nodeExtension.Manifest = &nodeManifest
	initRaw, err := h.extensions.nodeInit(t.Context(), &nodeExtension)
	if err != nil {
		t.Fatal(err)
	}
	var init extension.InitializeParams
	if err := json.Unmarshal(initRaw, &init); err != nil {
		t.Fatal(err)
	}
	if _, sent := init.Secrets[first]; sent {
		t.Fatal("a live entry secret was delivered to a node program")
	}
	if h.extensions.reg.Get("example-notes").Schema.Fields[0].Items.Fields[1].ClearsSecret {
		t.Fatal("detail rendering annotated the shared registry schema")
	}

	// An unresolved hub-only entry reference must not block a node that cannot receive it.
	providerPkg := secretProviderPackage(t, "example-vault", "exv", filepath.Join(t.TempDir(), "calls.jsonl"))
	code, providerPreview := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, fmt.Sprintf(`{"source":%q}`, "dir:"+providerPkg))
	if code != 200 {
		t.Fatalf("provider preview: %d %v", code, providerPreview)
	}
	code, providerDetail := call(t, mux, "POST", "/api/v1/extensions/-/install", master, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, providerPreview["preview_id"], providerPreview["permissions_digest"]))
	if code != 200 {
		t.Fatalf("provider install: %d %v", code, providerDetail)
	}
	if err := h.extensions.reg.SetSecretReference(t.Context(), "example-notes", first, "exv://Example/item/ok", "test"); err != nil {
		t.Fatal(err)
	}
	code, providerDetail = call(t, mux, "GET", "/api/v1/extensions/example-vault/-/", master, "")
	if code != 200 {
		t.Fatalf("provider detail: %d %v", code, providerDetail)
	}
	users := arrayAt(t, providerDetail, "secret_users")
	if len(users) != 1 {
		t.Fatalf("provider users: %+v", users)
	}
	user := objectAt(t, users[0])
	if user["secret"] != first || user["entry_title"] != "One" || user["secret_title"] != "Account key" {
		t.Fatalf("provider user does not name the live entry: %+v", user)
	}
	code, providerDetail = call(t, mux, "PUT", "/api/v1/extensions/example-vault/-/enabled", master, `{"enabled":false}`)
	if code != 200 {
		t.Fatalf("provider disable: %d %v", code, providerDetail)
	}
	nodeExtension = *h.extensions.reg.Get("example-notes")
	nodeManifest = *nodeExtension.Manifest
	nodeManifest.Node = &extension.NodeRuntime{}
	nodeExtension.Manifest = &nodeManifest
	if _, err := h.extensions.nodeInit(t.Context(), &nodeExtension); err != nil {
		t.Fatalf("a hub-only entry reference blocked node initialization: %v", err)
	}

	code, detail = call(t, mux, "PUT", "/api/v1/extensions/example-notes/-/enabled?entry_lists=1", master, `{"enabled":true}`)
	if code != 200 || len(arrayAt(t, objectAt(t, detail, "form"), "fields")) != 2 {
		t.Fatalf("lifecycle response lost the entry editor: %d %v", code, detail)
	}

	// Web adds a second entry; a stale native draft containing only the first must not remove it.
	code, detail = call(t, mux, "PUT", "/api/v1/extensions/example-notes/-/config?entry_lists=1", master,
		fmt.Sprintf(`{"config":{"accounts":[{"_id":%q,"name":"One","address":"https://one.example.com"},{"name":"Two","address":"https://two.example.com"}],"include_shared":true}}`, first))
	if code != 200 {
		t.Fatalf("second entry: %d %v", code, detail)
	}
	entries = arrayAt(t, objectAt(t, detail, "config"), "accounts")
	second := objectAt(t, entries[1])["_id"].(string)
	if code, detail = call(t, mux, "PUT", "/api/v1/extensions/example-notes/-/secrets/"+second+"?entry_lists=1", master, `{"value":"placeholder-secret-5678"}`); code != 200 {
		t.Fatalf("second secret: %d %v", code, detail)
	}
	stale := fmt.Sprintf(`{"config":{"accounts":[{"_id":%q,"name":"One","address":"https://one.example.com"}],"include_shared":false}}`, first)
	code, legacy = call(t, mux, "PUT", "/api/v1/extensions/example-notes/-/config", master, stale)
	if code != 200 {
		t.Fatalf("stale native save: %d %v", code, legacy)
	}
	entries = arrayAt(t, objectAt(t, legacy, "config"), "accounts")
	if len(entries) != 2 {
		t.Fatalf("stale native draft deleted an entry: %v", entries)
	}
	secrets, err := h.extensions.reg.Secrets(t.Context(), "example-notes")
	if err != nil || len(secrets) != 2 || secrets[first] == "" || secrets[second] == "" {
		t.Fatalf("stale native draft detached secrets: %+v, %v", secrets, err)
	}

	// An immediate machine-style save may preserve entries explicitly without giving up the opted-in response.
	code, detail = call(t, mux, "PUT", "/api/v1/extensions/example-notes/-/config?entry_lists=1&preserve_entries=1", master, stale)
	if code != 200 || len(arrayAt(t, objectAt(t, detail, "config"), "accounts")) != 2 || len(arrayAt(t, objectAt(t, detail, "form"), "fields")) != 2 {
		t.Fatalf("entry-preserving immediate save: %d %v", code, detail)
	}
	secrets, err = h.extensions.reg.Secrets(t.Context(), "example-notes")
	if err != nil || len(secrets) != 2 || secrets[first] == "" || secrets[second] == "" {
		t.Fatalf("entry-preserving immediate save detached secrets: %+v, %v", secrets, err)
	}

	code, detail = call(t, mux, "PUT", "/api/v1/extensions/example-notes/-/config?entry_lists=1", master,
		fmt.Sprintf(`{"config":{"accounts":[{"_id":%q,"name":"One","address":"https://one.example.com"}],"include_shared":false}}`, first))
	if code != 200 {
		t.Fatalf("remove second: %d %v", code, detail)
	}
	secrets, err = h.extensions.reg.Secrets(t.Context(), "example-notes")
	if err != nil || len(secrets) != 1 || secrets[second] != "" {
		t.Fatalf("removed entry secret survived: %+v, %v", secrets, err)
	}
}

func TestProviderUsersNumberUntitledEntries(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	providerPkg := secretProviderPackage(t, "example-vault", "exv", filepath.Join(t.TempDir(), "calls.jsonl"))
	code, preview := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, fmt.Sprintf(`{"source":%q}`, "dir:"+providerPkg))
	if code != 200 {
		t.Fatalf("provider preview: %d %v", code, preview)
	}
	code, detail := call(t, mux, "POST", "/api/v1/extensions/-/install", master, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"]))
	if code != 200 {
		t.Fatalf("provider install: %d %v", code, detail)
	}
	code, preview = call(t, mux, "POST", "/api/v1/extensions/-/preview", master, fmt.Sprintf(`{"source":%q}`, "dir:"+untitledEntryListAPIPackage(t)))
	if code != 200 {
		t.Fatalf("entry preview: %d %v", code, preview)
	}
	code, detail = call(t, mux, "POST", "/api/v1/extensions/-/install", master, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"]))
	if code != 200 {
		t.Fatalf("entry install: %d %v", code, detail)
	}
	code, detail = call(t, mux, "PUT", "/api/v1/extensions/example-untitled/-/config?entry_lists=1", master, `{"config":{"accounts":[{},{}]}}`)
	if code != 200 {
		t.Fatalf("untitled entries: %d %v", code, detail)
	}
	entries := arrayAt(t, objectAt(t, detail, "config"), "accounts")
	if len(entries) != 2 {
		t.Fatalf("entries: %+v", entries)
	}
	for _, raw := range entries {
		id := objectAt(t, raw)["_id"].(string)
		if err := h.extensions.reg.SetSecretReference(t.Context(), "example-untitled", id, "exv://Example/item/shared", "test"); err != nil {
			t.Fatal(err)
		}
	}
	code, detail = call(t, mux, "GET", "/api/v1/extensions/example-vault/-/", master, "")
	if code != 200 {
		t.Fatalf("provider detail: %d %v", code, detail)
	}
	var titles []string
	for _, raw := range arrayAt(t, detail, "secret_users") {
		user := objectAt(t, raw)
		if user["extension"] == "example-untitled" {
			title, _ := user["entry_title"].(string)
			titles = append(titles, title)
		}
	}
	joined := strings.Join(titles, "|")
	if len(titles) != 2 || titles[0] == "" || titles[1] == "" || titles[0] == titles[1] ||
		!strings.Contains(joined, "1") || !strings.Contains(joined, "2") {
		t.Fatalf("untitled provider labels are not numbered and distinct: %+v", titles)
	}
}

func TestEntryNetworkFormUsesResponseOnlyCopies(t *testing.T) {
	m := &extension.Manifest{Configuration: json.RawMessage(`{"type":"object","properties":{"accounts":{"type":"array","maxItems":2,"x-secret":{"name":"account_key","title":"Account key"},"items":{"type":"object","properties":{"address":{"type":"string","format":"uri"}}}}}}`), Permissions: extension.Permissions{Network: []string{"config:accounts[].address"}}}
	form, err := extension.ParseSchema(m.Configuration)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				projected := entryNetworkForm(form, m)
				if !projected.Fields[0].Items.Fields[0].ClearsSecret {
					t.Error("response projection lost its binding metadata")
					return
				}
			}
		}()
	}
	wg.Wait()
	if form.Fields[0].Items.Fields[0].ClearsSecret {
		t.Fatal("response projection mutated the shared schema")
	}
}
