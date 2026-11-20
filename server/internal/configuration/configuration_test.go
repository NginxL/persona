package configuration

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func example(t *testing.T) Document {
	t.Helper()
	raw, err := os.ReadFile("../../../schema/configuration/all-types.json")
	if err != nil {
		t.Fatal(err)
	}
	var d Document
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSharedUpdateCases(t *testing.T) {
	d := example(t)
	if err := CheckDocument(d); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../schema/configuration/validation-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name        string       `json:"name"`
			Update      Update       `json:"update"`
			Code        string       `json:"code"`
			FieldErrors []FieldError `json:"field_errors"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || len(fixture.Cases) == 0 {
		t.Fatalf("empty or invalid shared fixture: %v", err)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			err := CheckUpdate(d, c.Update)
			if c.Code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var got *Error
			if !errors.As(err, &got) || got.Code != c.Code || !reflect.DeepEqual(got.FieldErrors, c.FieldErrors) {
				t.Fatalf("got %#v / %v; want %s, %#v", got, err, c.Code, c.FieldErrors)
			}
		})
	}
}

func TestUpdateIsAtomicPartialAndDetached(t *testing.T) {
	d := example(t)
	d.Values["label"] = json.RawMessage(`"Saved label"`)
	u := Update{SchemaVersion: Version, ID: d.ID, Revision: d.Revision, Changes: map[string]json.RawMessage{"compact": []byte("true")}, Reset: []string{"label"}}
	values, err := ApplyUpdate(d, u)
	if err != nil {
		t.Fatal(err)
	}
	if string(values["label"]) != `"Preview"` || string(values["compact"]) != "true" || string(values["interval"]) != "30" {
		t.Fatalf("reset, patch or preservation failed: %v", values)
	}
	if string(d.Values["label"]) != `"Saved label"` || string(d.Values["compact"]) != "false" || d.Revision != "revision-1" {
		t.Fatal("helper mutated the authoritative snapshot")
	}
	values["interval"][0] = '9'
	values["compact"][0] = 'f'
	if string(d.Values["interval"]) != "30" || string(u.Changes["compact"]) != "true" {
		t.Fatal("returned values alias a caller-owned byte slice")
	}
	u.Changes["layout"] = json.RawMessage(`"unavailable"`)
	values, err = ApplyUpdate(d, u)
	if err == nil || values != nil || string(d.Values["compact"]) != "false" {
		t.Fatal("invalid mixed update partially applied")
	}
}

func TestProducerRefusesInvalidDefinitionsAndValues(t *testing.T) {
	for name, mutate := range map[string]func(*Document){
		"unknown kind":                        func(d *Document) { d.Groups[0].Fields[0].Kind = "future" },
		"duplicate id":                        func(d *Document) { d.Groups[0].Fields[1].ID = "compact" },
		"two option types":                    func(d *Document) { d.Groups[0].Fields[1].Number = &NumberOptions{} },
		"wrong option type":                   func(d *Document) { d.Groups[0].Fields[1].Text = nil; d.Groups[0].Fields[1].Number = &NumberOptions{} },
		"bad default":                         func(d *Document) { d.Groups[0].Fields[3].Default = []byte(`"missing"`) },
		"null default":                        func(d *Document) { d.Groups[0].Fields[0].Default = []byte(`null`) },
		"missing effective value":             func(d *Document) { delete(d.Values, "compact") },
		"unknown effective value":             func(d *Document) { d.Values["other"] = []byte(`false`) },
		"wrong effective value":               func(d *Document) { d.Values["compact"] = []byte(`"false"`) },
		"duplicate option":                    func(d *Document) { f := &d.Groups[0].Fields[3]; f.Select.Options[1].Value = f.Select.Options[0].Value },
		"unavailable selection count":         func(d *Document) { n := 4; d.Groups[0].Fields[6].Multiselect.MinSelected = &n },
		"reversed number bounds":              func(d *Document) { n := 200.0; d.Groups[0].Fields[7].Number.Minimum = &n },
		"zero step":                           func(d *Document) { n := 0.0; d.Groups[0].Fields[7].Number.Step = &n },
		"negative length":                     func(d *Document) { n := -1; d.Groups[0].Fields[1].Text.MinLength = &n },
		"disabled reason without restriction": func(d *Document) { d.Groups[0].Fields[0].DisabledReason = "Unavailable" },
		"notice for unknown field":            func(d *Document) { d.Notices["missing"] = Notice{Kind: "warning", Message: "Warning"} },
		"oversize":                            func(d *Document) { d.Description = strings.Repeat("a", MaxBytes) },
	} {
		t.Run(name, func(t *testing.T) {
			d := example(t)
			mutate(&d)
			if err := CheckDocument(d); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
}

func TestEmptyCollectionAndPortableNumber(t *testing.T) {
	if err := CheckDocument(Document{SchemaVersion: 1, ID: "empty", Revision: "1", Title: "No settings", Groups: []Group{}, Values: map[string]json.RawMessage{}}); err != nil {
		t.Fatal(err)
	}
	f := Field{Kind: Number, Number: &NumberOptions{}}
	for _, raw := range []string{"9007199254740991", "-9007199254740991", "0", "0.25"} {
		if code := ValueError(f, []byte(raw)); code != "" {
			t.Fatalf("portable number %s: %s", raw, code)
		}
	}
	for _, raw := range []string{"9007199254740992", "-9007199254740992", "1e100", "null", `"1"`, "NaN"} {
		if code := ValueError(f, []byte(raw)); code == "" {
			t.Fatalf("nonportable number %s accepted", raw)
		}
	}
}

func TestNumberChecksWireRangeAndIntegralityBeforeRounding(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		integer bool
		want    string
	}{
		{"9007199254740991.01", false, "range"},
		{"-9007199254740991.01", false, "range"},
		{"9.00719925474099101e15", false, "range"},
		{"-900719925474099100e-2", true, ""},
		{"0.90071992547409910e16", true, ""},
		{"9007199254740990.99", false, ""},
		{"9007199254740990.99", true, "integer"},
		{"1.00000000000000001", true, "integer"},
		{"30e-1", true, ""},
		{"1e-99999999999999999", true, "integer"},
		{"1e99999999999999999", false, "range"},
		{"-0.0e99999999999999999", true, ""},
	} {
		f := Field{Kind: Number, Number: &NumberOptions{Integer: tc.integer}}
		if code := ValueError(f, json.RawMessage(tc.raw)); code != tc.want {
			t.Errorf("ValueError(%s, integer=%v) = %q; want %q", tc.raw, tc.integer, code, tc.want)
		}
	}
}
