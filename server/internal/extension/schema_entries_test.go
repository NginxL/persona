package extension

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const entryListSchema = `{"type":"object","properties":{
  "accounts":{"type":"array","title":"Accounts","maxItems":4,
    "x-secret":{"name":"account_key","title":"Account key","hint":"last4"},
    "items":{"type":"object","properties":{
      "name":{"type":"string","title":"Name","maxLength":40},
      "address":{"type":"string","title":"Address","format":"uri"}
    },"required":["name","address"]}}
}}`

func TestSchemaEntryList(t *testing.T) {
	s, err := ParseSchema(json.RawMessage(entryListSchema))
	if err != nil {
		t.Fatal(err)
	}
	f := s.Fields[0]
	if !f.EntryList() || f.Secret == nil || f.Secret.Name != "account_key" || len(f.Items.Fields) != 2 {
		t.Fatalf("entry-list projection: %+v", f)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"secret":{"name":"account_key","title":"Account key","hint":"last4"}`) ||
		!strings.Contains(string(raw), `"items":{"name":"items","type":"object","fields":[`) ||
		strings.Contains(string(raw), `"entry":`) || strings.Contains(string(raw), `"from":`) {
		t.Fatalf("wire form: %s", raw)
	}
	good := `{"accounts":[{"_id":"entry_one","name":"One","address":"https://one.example.com"},{"_id":"entry_two","name":"Two","address":"https://two.example.com"}]}`
	if err := s.Check(json.RawMessage(good)); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckSaved(json.RawMessage(`{"accounts":[{"_id":"entry_one"}]}`)); err != nil {
		t.Fatalf("saved configuration may await required fields after an upgrade: %v", err)
	}
}

func TestSchemaEntryListErrorsHaveIndexedPaths(t *testing.T) {
	s, err := ParseSchema(json.RawMessage(entryListSchema))
	if err != nil {
		t.Fatal(err)
	}
	for raw, path := range map[string]string{
		`{"accounts":[{"name":"One","address":"https://one.example.com"}]}`:                                                                              "accounts[0]._id",
		`{"accounts":[{"_id":"Entry_One","name":"One","address":"https://one.example.com"}]}`:                                                            "accounts[0]._id",
		`{"accounts":[{"_id":"entry_one","name":"One","address":"https://one.example.com","x":1}]}`:                                                      "accounts[0].x",
		`{"accounts":[{"_id":"entry_one","address":"https://one.example.com"}]}`:                                                                         "accounts[0].name",
		`{"accounts":[{"_id":"entry_one","name":"One","address":"not a URL"}]}`:                                                                          "accounts[0].address",
		`{"accounts":[{"_id":"same","name":"One","address":"https://one.example.com"},{"_id":"same","name":"Two","address":"https://two.example.com"}]}`: "accounts[1]._id",
	} {
		t.Run(path, func(t *testing.T) {
			err := s.Check(json.RawMessage(raw))
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Path != path {
				t.Fatalf("%s: %v; want path %s", raw, err, path)
			}
		})
	}
}

func TestSchemaEntryListNamespaceSpansLists(t *testing.T) {
	raw := `{"type":"object","properties":{
	  "accounts":{"type":"array","maxItems":2,"x-secret":{"name":"account_key","title":"Account key"},"items":{"type":"object","properties":{"name":{"type":"string"},"address":{"type":"string","format":"uri"}}}},
	  "other":{"type":"array","maxItems":2,"x-secret":{"name":"other_key","title":"Other key"},"items":{"type":"object","properties":{"name":{"type":"string"}}}}
	}}`
	s, err := ParseSchema(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	config := `{"accounts":[{"_id":"same","name":"One","address":"https://one.example.com"}],"other":[{"_id":"same","name":"Other"}]}`
	err = s.Check(json.RawMessage(config))
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Path != "other[0]._id" {
		t.Fatalf("cross-list collision: %v", err)
	}
}

func TestSchemaEntryListRefusesUnsafeShapes(t *testing.T) {
	valid := `"accounts":{"type":"array","maxItems":4,"x-secret":{"name":"account_key","title":"Account key"},"items":{"type":"object","properties":{"name":{"type":"string"}}}}`
	for name, field := range map[string]string{
		"missing secret":    `"accounts":{"type":"array","maxItems":4,"items":{"type":"object","properties":{"name":{"type":"string"}}}}`,
		"missing bound":     `"accounts":{"type":"array","x-secret":{"name":"account_key","title":"Account key"},"items":{"type":"object","properties":{"name":{"type":"string"}}}}`,
		"large bound":       strings.Replace(valid, `"maxItems":4`, `"maxItems":33`, 1),
		"zero bound":        strings.Replace(valid, `"maxItems":4`, `"maxItems":0`, 1),
		"bad minimum":       strings.Replace(valid, `"maxItems":4`, `"minItems":5,"maxItems":4`, 1),
		"unique objects":    strings.Replace(valid, `"maxItems":4`, `"maxItems":4,"uniqueItems":true`, 1),
		"list default":      strings.Replace(valid, `"maxItems":4`, `"maxItems":4,"default":[]`, 1),
		"entry default":     strings.Replace(valid, `"properties":{`, `"default":{},"properties":{`, 1),
		"field default":     strings.Replace(valid, `"name":{"type":"string"}`, `"name":{"type":"string","default":"One"}`, 1),
		"number field":      strings.Replace(valid, `"name":{"type":"string"}`, `"name":{"type":"integer"}`, 1),
		"reserved id":       strings.Replace(valid, `"name":{"type":"string"}`, `"_id":{"type":"string"}`, 1),
		"bad secret":        strings.Replace(valid, `"name":"account_key"`, `"name":"Account Key"`, 1),
		"missing title":     strings.Replace(valid, `"title":"Account key"`, `"title":""`, 1),
		"bad hint":          strings.Replace(valid, `"title":"Account key"`, `"title":"Account key","hint":"value"`, 1),
		"secret on strings": `"accounts":{"type":"array","maxItems":4,"x-secret":{"name":"account_key","title":"Account key"},"items":{"type":"string"}}`,
		"from on strings":   `"accounts":{"type":"array","maxItems":4,"x-from":{"name":"old"},"items":{"type":"string"}}`,
		"self migration":    strings.Replace(valid, `"maxItems":4`, `"maxItems":4,"x-from":{"name":"accounts"}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			raw := `{"type":"object","properties":{` + field + `}}`
			if _, err := ParseSchema(json.RawMessage(raw)); err == nil {
				t.Fatalf("unsafe shape parsed: %s", raw)
			}
		})
	}
}

func TestSchemaEntryListAllowsFixedSlotMigration(t *testing.T) {
	raw := `{"type":"object","properties":{
	  "accounts":{"type":"array","maxItems":4,"x-secret":{"name":"account_key","title":"Account key"},"x-from":{"name":"old_name","address":"old_address"},"items":{"type":"object","properties":{"name":{"type":"string"},"address":{"type":"string","format":"uri"}}}}
	}}`
	if _, err := ParseSchema(json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaEntryMigrationSourcesAreOwnedOnce(t *testing.T) {
	for name, raw := range map[string]string{
		"retained source": `{"type":"object","properties":{
		  "accounts":{"type":"array","maxItems":4,"x-secret":{"name":"account_key","title":"Account key"},"x-from":{"address":"old_address"},"items":{"type":"object","properties":{"address":{"type":"string","format":"uri"}}}},
		  "old_address":{"type":"string","format":"uri"}
		}}`,
		"shared by two lists": `{"type":"object","properties":{
		  "accounts":{"type":"array","maxItems":4,"x-secret":{"name":"account_key","title":"Account key"},"x-from":{"address":"old_address"},"items":{"type":"object","properties":{"address":{"type":"string","format":"uri"}}}},
		  "other":{"type":"array","maxItems":4,"x-secret":{"name":"other_key","title":"Other key"},"x-from":{"address":"old_address"},"items":{"type":"object","properties":{"address":{"type":"string","format":"uri"}}}}
		}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSchema(json.RawMessage(raw)); err == nil {
				t.Fatal("an ambiguous fixed-slot migration parsed")
			}
		})
	}
}
