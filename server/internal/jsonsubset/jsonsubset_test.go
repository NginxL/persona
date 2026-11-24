package jsonsubset

import (
	"encoding/json"
	"testing"
)

func TestDecodeRejectsTrailingTokens(t *testing.T) {
	bad := []string{
		`{"a":1}}`,
		`[1]]`,
		`{"a":1}} trailing`,
		`{"a":1}{"b":2}`,
		`{"a":1} 2`,
	}
	for _, raw := range bad {
		if v, err := Decode([]byte(raw)); err == nil {
			t.Errorf("Decode(%q) = (%#v, nil), want error", raw, v)
		}
	}

	if _, err := Decode([]byte(" \n\t{\"a\": 1}\n\t ")); err != nil {
		t.Fatalf("Decode with surrounding whitespace failed: %v", err)
	}
}

func TestCompileRejectsTrailingTokens(t *testing.T) {
	bad := []string{
		`{"type":"object"} extra`,
		`{"type":"object"}{"type":"string"}`,
		`{"type":"object"}}`,
	}
	for _, raw := range bad {
		if root, err := Compile(json.RawMessage(raw)); err == nil {
			t.Errorf("Compile(%q) = (%#v, nil), want error", raw, root)
		}
	}

	if _, err := Compile(json.RawMessage(" {\"type\":\"object\"} \n")); err != nil {
		t.Fatalf("Compile with surrounding whitespace failed: %v", err)
	}
}

func TestValidateConstAndEnumNumericEquality(t *testing.T) {
	constSchema, err := Compile(json.RawMessage(`{"const": 9007199254740992}`))
	if err != nil {
		t.Fatalf("Compile const schema: %v", err)
	}
	exactVal, err := Decode([]byte(`9007199254740992`))
	if err != nil {
		t.Fatalf("Decode exact value: %v", err)
	}
	if p := Validate(constSchema, exactVal); p != "" {
		t.Fatalf("Validate exact 64-bit const = %q, want empty", p)
	}
	distinctVal, err := Decode([]byte(`9007199254740993`))
	if err != nil {
		t.Fatalf("Decode distinct value: %v", err)
	}
	if p := Validate(constSchema, distinctVal); p == "" {
		t.Fatalf("Validate(9007199254740993 against const 9007199254740992) = empty, want mismatch")
	}

	enumSchema, err := Compile(json.RawMessage(`{"enum": [9007199254740992]}`))
	if err != nil {
		t.Fatalf("Compile enum schema: %v", err)
	}
	if p := Validate(enumSchema, distinctVal); p == "" {
		t.Fatalf("Validate(9007199254740993 against enum [9007199254740992]) = empty, want mismatch")
	}

	nestedConst, err := Compile(json.RawMessage(`{"const": [1.0, {"n": 2e0, "id": 9007199254740992}]}`))
	if err != nil {
		t.Fatalf("Compile nested const: %v", err)
	}
	nestedMatch, err := Decode([]byte(`[1, {"id": 9007199254740992, "n": 2}]`))
	if err != nil {
		t.Fatalf("Decode nested match: %v", err)
	}
	if p := Validate(nestedConst, nestedMatch); p != "" {
		t.Fatalf("Validate nested numerically equal value = %q, want empty", p)
	}
	nestedMismatch, err := Decode([]byte(`[1, {"id": 9007199254740993, "n": 2}]`))
	if err != nil {
		t.Fatalf("Decode nested mismatch: %v", err)
	}
	if p := Validate(nestedConst, nestedMismatch); p == "" {
		t.Fatalf("Validate nested distinct 64-bit integer = empty, want mismatch")
	}
}

func TestValidateRejectsOverflowingJSONNumbers(t *testing.T) {
	typedSchema, err := Compile(json.RawMessage(`{"type": "number", "minimum": 0}`))
	if err != nil {
		t.Fatalf("Compile typed schema: %v", err)
	}
	huge, err := Decode([]byte(`1e999`))
	if err != nil {
		t.Fatalf("Decode 1e999: %v", err)
	}
	if p := Validate(typedSchema, huge); p == "" {
		t.Fatalf("Validate(1e999 against {type:number, minimum:0}) = empty, want error")
	}

	untypedBound, err := Compile(json.RawMessage(`{"minimum": 0}`))
	if err != nil {
		t.Fatalf("Compile untyped schema: %v", err)
	}
	if p := Validate(untypedBound, huge); p == "" {
		t.Fatalf("Validate(1e999 against {minimum:0}) = empty, want error")
	}
	if p := Validate(typedSchema, json.Number("Inf")); p == "" {
		t.Fatalf("Validate(Inf against {type:number, minimum:0}) = empty, want error")
	}
}
