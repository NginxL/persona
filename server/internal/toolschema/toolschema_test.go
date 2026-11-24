package toolschema

import (
	"encoding/json"
	"testing"
)

// The schema that took every Agy session down on 2026-11-23 (enkeep_seed_import's items[].expiring).
const seedImport = `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{` +
	`"expiring":{"type":"string","enum":["","recent","role"]},"kind":{"type":"string","enum":["rule","fact"]}}}}}}`

func TestProblemsNamesAnEmptyStringInAnEnum(t *testing.T) {
	got := Problems(json.RawMessage(seedImport))
	if len(got) != 1 || got[0] != ".properties.items.items.properties.expiring.enum[0] is an empty string" {
		t.Fatalf("Problems = %q", got)
	}
}

func TestPortableDropsTheEmptyStringAndKeepsTheRest(t *testing.T) {
	out := Portable(json.RawMessage(seedImport))
	if p := Problems(out); len(p) != 0 {
		t.Fatalf("still has problems: %q", p)
	}
	var v struct {
		Properties struct {
			Items struct {
				Items struct {
					Properties map[string]struct {
						Enum []string `json:"enum"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	props := v.Properties.Items.Items.Properties
	if e := props["expiring"].Enum; len(e) != 2 || e[0] != "recent" || e[1] != "role" {
		t.Fatalf("expiring enum = %q", e)
	}
	if e := props["kind"].Enum; len(e) != 2 {
		t.Fatalf("kind enum changed: %q", e)
	}
}

func TestPortableDropsAnEnumLeftEmpty(t *testing.T) {
	out := Portable(json.RawMessage(`{"type":"object","properties":{"a":{"type":"string","enum":[""]},"b":{"type":"boolean","enum":[true]}}}`))
	if string(out) != `{"properties":{"a":{"type":"string"},"b":{"enum":[true],"type":"boolean"}},"type":"object"}` {
		t.Fatalf("Portable = %s", out)
	}
}

func TestPortableLeavesACleanSchemaByteForByte(t *testing.T) {
	in := json.RawMessage(`{"type":"object", "properties":{"k":{"type":"string","enum":["a","b"]}}}`)
	if out := Portable(in); string(out) != string(in) {
		t.Fatalf("Portable rewrote a clean schema: %s", out)
	}
}
