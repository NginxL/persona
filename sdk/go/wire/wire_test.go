package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Every example in schema/extension/examples decodes into its definition's type and encodes back to the same JSON:
// the generated types hold everything the schema says, under the same names.
func TestExamplesRoundTrip(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "schema", "extension", "examples", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples: %v", err)
	}
	for _, f := range files {
		def := strings.SplitN(filepath.Base(f), ".", 2)[0]
		t.Run(filepath.Base(f), func(t *testing.T) {
			mk, ok := newDef[def]
			if !ok {
				t.Fatalf("no type %s", def)
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			v := mk()
			if err := json.Unmarshal(raw, v); err != nil {
				t.Fatalf("decode: %v", err)
			}
			out, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			var want, got any
			_ = json.Unmarshal(raw, &want)
			_ = json.Unmarshal(out, &got)
			if !reflect.DeepEqual(want, got) {
				t.Errorf("round trip differs\nwant %s\n got %s", raw, out)
			}
		})
	}
}

// The component types and the UI version that brought each.
func TestComponentTypes(t *testing.T) {
	want := map[string]int{"text": 1, "metric": 1, "progress": 1, "row": 1, "button": 1, "link": 1, "field": 1,
		"divider": 1, "chart": 2, "stat_grid": 2, "segmented": 2, "markdown": 3, "table": 3, "empty": 3, "loading": 3, "html": 4,
		"header": 4, "breadcrumb": 4, "file_tree": 4, "code": 4, "diff": 4, "comment_thread": 4, "composer": 4,
		"image": 6, "grid": 6, "carousel": 6, "graph": 10, "scene": 11}
	if !reflect.DeepEqual(ComponentSince, want) {
		t.Errorf("ComponentSince = %v", ComponentSince)
	}
	if len(ComponentTypes) != len(want) {
		t.Errorf("ComponentTypes = %v", ComponentTypes)
	}
}

// Validate agrees with the schema's examples: each holds to its definition, each under invalid/ does not.
func TestValidateExamples(t *testing.T) {
	for _, dir := range []string{"", "invalid"} {
		files, _ := filepath.Glob(filepath.Join("..", "..", "..", "schema", "extension", "examples", dir, "*.json"))
		if len(files) == 0 {
			t.Fatalf("no examples in %q", dir)
		}
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateJSON(strings.SplitN(filepath.Base(f), ".", 2)[0], raw)
			if dir == "" && err != nil {
				t.Errorf("%s: %v", filepath.Base(f), err)
			}
			if dir == "invalid" && err == nil {
				t.Errorf("%s: holds, but should not", filepath.Base(f))
			}
		}
	}
}

// maxProperties and uniqueItems hold (#4012): the invalid examples fail on them, one value or entry fewer holds.
func TestValidateMaxPropertiesAndUniqueItems(t *testing.T) {
	values := map[string]string{}
	for _, k := range strings.Split("abcdefghi", "") {
		values[k] = "x"
	}
	v := View{Sections: []Section{{Items: []Component{{Type: "button", Title: "Go", Action: &Action{ID: "go", Values: values}}}}}}
	if err := CheckView(&v); err == nil || !strings.Contains(err.Error(), "more than 8 properties") {
		t.Fatalf("nine values: %v", err)
	}
	delete(values, "i")
	if err := CheckView(&v); err != nil {
		t.Fatalf("eight values: %v", err)
	}
	m := []byte(`{"manifest_version":1,"id":"x","name":"X","version":"1.0.0","off_by_default":["voice/v","voice/v"],"contributes":{"voice":[{"id":"v"}]}}`)
	if err := ValidateJSON("Manifest", m); err == nil || !strings.Contains(err.Error(), `$.off_by_default[1]: "voice/v" twice`) {
		t.Fatalf("an entry twice: %v", err)
	}
	if err := ValidateJSON("Manifest", []byte(strings.Replace(string(m), `"voice/v","voice/v"`, `"voice/v"`, 1))); err != nil {
		t.Fatalf("once: %v", err)
	}
}

func TestValidateConfiguredNetworkEntryPaths(t *testing.T) {
	for _, value := range []string{
		"config:base_url", "config:base_url:http", "config:accounts[].address", "config:accounts[].address:http", "notes.example.com",
	} {
		if err := Validate("NetworkEntry", value); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
	for _, value := range []string{
		"config:accounts[0].address", "config:accounts[].address.extra", "config:accounts[].address:ftp",
		"config:accounts[].address:http:extra", "config:[].address", "config:accounts[]", "config:accounts[].",
	} {
		if err := Validate("NetworkEntry", value); err == nil {
			t.Errorf("%q: malformed configured network path accepted", value)
		}
	}
}

func TestKBUpdateStatus(t *testing.T) {
	for _, status := range []string{"", "active", "ignored", "self"} {
		t.Run(status, func(t *testing.T) {
			params := KBUpdateParams{Kind: "people", ID: "person", Status: status}
			if err := Validate("KBUpdateParams", params); err != nil {
				t.Fatalf("status %q: %v", status, err)
			}
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			var decoded KBUpdateParams
			if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Status != status {
				t.Fatalf("status round trip: %+v, %v", decoded, err)
			}
		})
	}
}

func TestValidateSays(t *testing.T) {
	err := ValidateJSON("View", []byte(`{"sections":[{"items":[{"type":"metric","label":"a"},{"type":"nope"}]}]}`))
	ve, ok := err.(*ValidationError)
	if !ok || len(ve.Errors) != 2 || ve.Errors[0] != "$.sections[0].items[0]: no value" || ve.Errors[1] != `$.sections[0].items[1]: no variant with type "nope"` {
		t.Fatalf("got %#v", err)
	}
	if err := Validate("View", View{Sections: []Section{{Items: []Component{{Type: "text", Text: "hi"}}}}}); err != nil {
		t.Fatal(err)
	}
}
