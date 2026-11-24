// Package jsonsubset checks JSON values against a subset of JSON Schema: model.complete's json_schema (#6102, the
// assistant's model_rpc.go) and a subscription's tool arguments against the tool's input_schema
// (internal/subscriptions) use the same rules.
package jsonsubset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strings"
	"unicode/utf8"
)

// The common keywords of structured output are checked: type, enum, const, properties, required,
// additionalProperties, items, prefixItems, min/maxItems, min/maxLength, minimum/maximum (and the exclusive ones),
// anyOf / oneOf / allOf, and local $ref (#/$defs/…, #/definitions/…). Others (format, pattern…) are left to the
// model and the caller. The schema itself is never rewritten.

// Compile reads a schema: a JSON object, whose local $refs resolve.
func Compile(raw json.RawMessage) (map[string]any, error) {
	v, err := Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("json_schema is not JSON: %w", err)
	}
	root, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("json_schema must be an object")
	}
	if err := checkRefs(root, root, 0); err != nil {
		return nil, err
	}
	return root, nil
}

func checkRefs(root map[string]any, node any, depth int) error {
	if depth > 64 {
		return fmt.Errorf("json_schema is nested too deep")
	}
	switch n := node.(type) {
	case map[string]any:
		if ref, ok := n["$ref"].(string); ok {
			if _, err := resolveRef(root, ref); err != nil {
				return err
			}
		}
		for _, v := range n {
			if err := checkRefs(root, v, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, v := range n {
			if err := checkRefs(root, v, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveRef(root map[string]any, ref string) (map[string]any, error) {
	if ref == "#" {
		return root, nil
	}
	path, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil, fmt.Errorf("json_schema: only local $ref is supported, not %q", ref)
	}
	var cur any = root
	for _, part := range strings.Split(path, "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("json_schema: $ref %q does not resolve", ref)
		}
		if cur, ok = m[part]; !ok {
			return nil, fmt.Errorf("json_schema: $ref %q does not resolve", ref)
		}
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("json_schema: $ref %q is not a schema", ref)
	}
	return m, nil
}

// Decode reads one JSON value with its numbers as json.Number, as Validate expects them.
func Decode(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("more than one JSON value")
	}
	return v, nil
}

// Validate says what in v does not match the compiled schema root, "" when it does. v holds numbers as json.Number
// (Decode).
func Validate(root map[string]any, v any) string { return ValidateAt(root, root, v, "$", 0) }

// ValidateAt says what in v, found at at, does not match schema, a part of root; "" when it does.
func ValidateAt(root, schema map[string]any, v any, at string, depth int) string {
	if depth > 64 {
		return at + ": nested too deep"
	}
	if ref, ok := schema["$ref"].(string); ok {
		target, err := resolveRef(root, ref)
		if err != nil {
			return err.Error()
		}
		if p := ValidateAt(root, target, v, at, depth+1); p != "" {
			return p
		}
	}
	if t, ok := schema["type"]; ok {
		var types []string
		switch t := t.(type) {
		case string:
			types = []string{t}
		case []any:
			for _, x := range t {
				if s, ok := x.(string); ok {
					types = append(types, s)
				}
			}
		}
		if len(types) > 0 && !anyType(v, types) {
			return fmt.Sprintf("%s: should be %s, is %s", at, strings.Join(types, " or "), jsonType(v))
		}
	}
	if e, ok := schema["enum"].([]any); ok {
		found := false
		for _, x := range e {
			if schemaEqual(x, v) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("%s: should be one of %s", at, compact(e))
		}
	}
	if c, ok := schema["const"]; ok && !schemaEqual(c, v) {
		return fmt.Sprintf("%s: should be %s", at, compact(c))
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		list, ok := schema[key].([]any)
		if !ok || len(list) == 0 {
			continue
		}
		var problems []string
		for _, sub := range list {
			m, _ := sub.(map[string]any)
			if m == nil {
				continue
			}
			if p := ValidateAt(root, m, v, at, depth+1); p != "" {
				problems = append(problems, p)
				if key == "allOf" {
					return p
				}
			}
		}
		if key != "allOf" && len(problems) == len(list) { // oneOf is taken as anyOf: a model's answer matching two is fine
			return fmt.Sprintf("%s: matches none of %s (%s)", at, key, problems[0])
		}
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if name, ok := r.(string); ok {
					if _, has := x[name]; !has {
						return fmt.Sprintf("%s: missing required %q", at, name)
					}
				}
			}
		}
		for name, val := range x {
			if sub, ok := props[name].(map[string]any); ok {
				if p := ValidateAt(root, sub, val, at+"."+name, depth+1); p != "" {
					return p
				}
				continue
			}
			switch extra := schema["additionalProperties"].(type) {
			case bool:
				if !extra {
					return fmt.Sprintf("%s: unexpected property %q", at, name)
				}
			case map[string]any:
				if p := ValidateAt(root, extra, val, at+"."+name, depth+1); p != "" {
					return p
				}
			}
		}
	case []any:
		if n, ok := number(schema["minItems"]); ok && float64(len(x)) < n {
			return fmt.Sprintf("%s: should have at least %v items, has %d", at, n, len(x))
		}
		if n, ok := number(schema["maxItems"]); ok && float64(len(x)) > n {
			return fmt.Sprintf("%s: should have at most %v items, has %d", at, n, len(x))
		}
		prefix, _ := schema["prefixItems"].([]any)
		for i, item := range x {
			sub, _ := schema["items"].(map[string]any)
			if i < len(prefix) {
				sub, _ = prefix[i].(map[string]any)
			}
			if sub == nil {
				continue
			}
			if p := ValidateAt(root, sub, item, fmt.Sprintf("%s[%d]", at, i), depth+1); p != "" {
				return p
			}
		}
	case string:
		n := float64(utf8.RuneCountInString(x))
		if m, ok := number(schema["minLength"]); ok && n < m {
			return fmt.Sprintf("%s: should be at least %v characters", at, m)
		}
		if m, ok := number(schema["maxLength"]); ok && n > m {
			return fmt.Sprintf("%s: should be at most %v characters", at, m)
		}
	case json.Number:
		f, ok := number(x)
		if !ok {
			return at + ": number is out of range"
		}
		if m, ok := number(schema["minimum"]); ok && f < m {
			return fmt.Sprintf("%s: should be at least %v", at, m)
		}
		if m, ok := number(schema["maximum"]); ok && f > m {
			return fmt.Sprintf("%s: should be at most %v", at, m)
		}
		if m, ok := number(schema["exclusiveMinimum"]); ok && f <= m {
			return fmt.Sprintf("%s: should be more than %v", at, m)
		}
		if m, ok := number(schema["exclusiveMaximum"]); ok && f >= m {
			return fmt.Sprintf("%s: should be less than %v", at, m)
		}
	}
	return ""
}

func anyType(v any, types []string) bool {
	for _, t := range types {
		switch t {
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "null":
			if v == nil {
				return true
			}
		case "number":
			if _, ok := number(v); ok {
				return true
			}
		case "integer":
			if f, ok := number(v); ok && f == math.Trunc(f) {
				return true
			}
		}
	}
	return false
}

func jsonType(v any) string {
	switch v.(type) {
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case json.Number:
		if _, ok := number(v); !ok {
			return "an out-of-range number"
		}
		return "a number"
	case nil:
		return "null"
	}
	return "unknown"
}

func number(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func numberEqual(x, y json.Number) bool {
	fx, ok1 := number(x)
	fy, ok2 := number(y)
	if !ok1 || !ok2 || fx != fy {
		return false
	}
	if fx == 0 {
		return true
	}
	if len(x) <= 512 && len(y) <= 512 {
		rx, ok1 := new(big.Rat).SetString(string(x))
		ry, ok2 := new(big.Rat).SetString(string(y))
		if ok1 && ok2 {
			return rx.Cmp(ry) == 0
		}
	}
	return string(x) == string(y)
}

func schemaEqual(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case json.Number:
		y, ok := b.(json.Number)
		return ok && numberEqual(x, y)
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !schemaEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, vx := range x {
			vy, has := y[k]
			if !has || !schemaEqual(vx, vy) {
				return false
			}
		}
		return true
	}
	return compact(a) == compact(b)
}

func compact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
