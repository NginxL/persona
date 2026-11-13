package wire

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"
)

// The schemas (schema/extension, copied here by the generator) are the one truth of what a view, a manifest or a
// handshake may hold: Validate holds a value to one of their definitions. It knows the subset the generator does
// (schema/extension/README.md "The subset"), the same as scripts/test/test_extension_schema.py's validator, and nothing more.

//go:embed schema/*.json
var schemaFS embed.FS

var (
	loadOnce sync.Once
	files    map[string]map[string]any // file name → its JSON
	loadErr  error
	patterns sync.Map // pattern → *regexp.Regexp
	// refs holds what resolve answers for every $ref the schemas carry, keyed by
	// the ref and the file it stands in. Filled once with files, read-only after,
	// so no lock: a view visits the same handful of refs once per component.
	refs map[refSite]refTarget
	// siblings holds, for each schema node with a $ref, its other keywords
	// but description (refSiblings). Filled with refs, read-only after.
	siblings map[unsafe.Pointer]map[string]any
	// present holds, for each schema node of the loaded schemas (and each
	// refSiblings rest), which keywords check reads it has. Filled with refs,
	// read-only after.
	present map[unsafe.Pointer]keywords
)

// keywords is a set of the schema keywords check reads, one bit each, so a
// node is asked only for the keywords it has: most carry two or three of them.
type keywords uint32

const (
	kwRef keywords = 1 << iota
	kwAllOf
	kwAnyOf
	kwNot
	kwOneOf
	kwType
	kwConst
	kwEnum
	kwMinLength
	kwMaxLength
	kwPattern
	kwMinimum
	kwMaximum
	kwMinItems
	kwMaxItems
	kwUniqueItems
	kwItems
	kwProperties
	kwRequired
	kwMinProperties
	kwMaxProperties
	kwAdditionalProperties
	kwPropertyNames
)

var keywordBits = map[string]keywords{
	"$ref": kwRef, "allOf": kwAllOf, "anyOf": kwAnyOf, "not": kwNot, "oneOf": kwOneOf, "type": kwType,
	"const": kwConst, "enum": kwEnum, "minLength": kwMinLength, "maxLength": kwMaxLength, "pattern": kwPattern,
	"minimum": kwMinimum, "maximum": kwMaximum, "minItems": kwMinItems, "maxItems": kwMaxItems,
	"uniqueItems": kwUniqueItems, "items": kwItems, "properties": kwProperties, "required": kwRequired,
	"minProperties": kwMinProperties, "maxProperties": kwMaxProperties,
	"additionalProperties": kwAdditionalProperties, "propertyNames": kwPropertyNames,
}

// keywordsOf is the keywords s has, from present for the loaded schemas.
func keywordsOf(s map[string]any) keywords {
	if k, ok := present[mapKey(s)]; ok {
		return k
	}
	return keywordsIn(s)
}

// member is s[name] when s has the keyword bit (have is keywordsOf(s)), else
// nil without looking: check's keywords are all absent from most nodes.
func member(s map[string]any, have, bit keywords, name string) any {
	if have&bit == 0 {
		return nil
	}
	return s[name]
}

func keywordsIn(s map[string]any) keywords {
	var k keywords
	for name := range s {
		k |= keywordBits[name]
	}
	return k
}

type refSite struct{ ref, here string }

type refTarget struct {
	schema map[string]any
	file   string
}

func loadSchemas() {
	files = map[string]map[string]any{}
	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		loadErr = err
		return
	}
	for _, e := range entries {
		raw, err := schemaFS.ReadFile(path.Join("schema", e.Name()))
		if err != nil {
			loadErr = err
			return
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			loadErr = fmt.Errorf("%s: %v", e.Name(), err)
			return
		}
		files[e.Name()] = doc
	}
	refs = map[refSite]refTarget{}
	siblings = map[unsafe.Pointer]map[string]any{}
	present = map[unsafe.Pointer]keywords{}
	for name, doc := range files {
		collectRefs(doc, name)
	}
}

// collectRefs resolves every $ref of node, which stands in the file here, so
// that resolve walks a JSON pointer once per (ref, file) instead of once per
// visit. A $ref that is not a schema reference costs one unused entry.
func collectRefs(node any, here string) {
	switch value := node.(type) {
	case map[string]any:
		present[mapKey(value)] = keywordsIn(value)
		if _, ok := value["$ref"].(string); ok {
			rest := refSiblingsOf(value)
			siblings[mapKey(value)] = rest
			present[mapKey(rest)] = keywordsIn(rest)
		}
		for key, member := range value {
			if key == "$ref" {
				if ref, ok := member.(string); ok {
					site := refSite{ref: ref, here: here}
					if _, seen := refs[site]; !seen {
						schema, file := walkRef(ref, here)
						refs[site] = refTarget{schema: schema, file: file}
					}
				}
				continue
			}
			collectRefs(member, here)
		}
	case []any:
		for _, member := range value {
			collectRefs(member, here)
		}
	}
}

// refSiblings is the keywords of s other than $ref and description, which the
// value must hold to as well as to what $ref names. Those of the loaded
// schemas were found once by collectRefs; a node they do not carry is read
// here.
func refSiblings(s map[string]any) map[string]any {
	if rest, ok := siblings[mapKey(s)]; ok {
		return rest
	}
	return refSiblingsOf(s)
}

func refSiblingsOf(s map[string]any) map[string]any {
	rest := map[string]any{}
	for k, x := range s {
		if k != "$ref" && k != "description" {
			rest[k] = x
		}
	}
	return rest
}

// mapKey is the identity of a map, which the loaded schemas keep for the life
// of the process.
func mapKey(m map[string]any) unsafe.Pointer { return reflect.ValueOf(m).UnsafePointer() }

// ValidationError is what a value does not hold to, each "<where>: <what>" ($ is the value itself).
type ValidationError struct {
	Def    string
	Errors []string
}

func (e *ValidationError) Error() string {
	if len(e.Errors) == 1 {
		return e.Errors[0]
	}
	return fmt.Sprintf("%s (and %d more)", e.Errors[0], len(e.Errors)-1)
}

// errorsMost is how many errors a validation collects before it stops looking.
const errorsMost = 20

// Validate holds v to the definition def (View, Manifest, InitializeResult…): v is decoded JSON (any of
// map[string]any, []any, string, float64, bool, nil) or anything encoding/json writes, which is written and read back
// first. It returns a *ValidationError, or nil when v holds.
func Validate(def string, v any) error {
	switch v.(type) {
	case map[string]any, []any, string, float64, json.Number, bool, nil:
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return &ValidationError{Def: def, Errors: []string{"$: " + err.Error()}}
		}
		return ValidateJSON(def, raw)
	}
	v = compatibilityValue(def, v)
	loadOnce.Do(loadSchemas)
	if loadErr != nil {
		return loadErr
	}
	for name, doc := range files {
		if s, ok := defs(doc)[def].(map[string]any); ok {
			c := checker{}
			c.check(s, v, name)
			if len(c.errs) > 0 {
				return &ValidationError{Def: def, Errors: c.errs}
			}
			return nil
		}
	}
	return fmt.Errorf("no definition %s in schema/extension", def)
}

// ValidateJSON is Validate for raw JSON.
func ValidateJSON(def string, raw []byte) error {
	var v any
	if strings.HasPrefix(def, "UIStream") {
		// Stream bounds and versions must be checked before float64 rounding. Keep legacy decoding unchanged.
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&v); err != nil {
			return &ValidationError{Def: def, Errors: []string{"$: " + err.Error()}}
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return &ValidationError{Def: def, Errors: []string{"$: trailing JSON data"}}
		}
	} else if err := json.Unmarshal(raw, &v); err != nil {
		return &ValidationError{Def: def, Errors: []string{"$: " + err.Error()}}
	}
	return Validate(def, v)
}

func defs(doc map[string]any) map[string]any {
	d, _ := doc["$defs"].(map[string]any)
	return d
}

// checker collects what fails in a value. The location of what it checks is a
// stack of steps written out only when an error is added: a view that holds
// builds no location strings. A trial checker (anyOf, oneOf, not) only reports
// whether its branch fails, so it stops at the first failure and formats none.
type checker struct {
	errs  []string
	path  []step
	trial bool
	fails bool
	// keys holds the sorted member names of the objects being checked, one
	// run per object on the way down, so an object's names take no slice of
	// their own.
	keys []string
}

// step is one move from a value to a part of it: a member, an item, or a
// property name checked against propertyNames.
type step struct {
	member string
	item   int
	kind   uint8
}

const (
	memberStep = iota
	itemStep
	keyStep
)

// at writes the location as the messages give it: $, then .member, [item] or .<key>.
func (c *checker) at() string {
	var b strings.Builder
	b.WriteByte('$')
	for _, s := range c.path {
		switch s.kind {
		case memberStep:
			b.WriteByte('.')
			b.WriteString(s.member)
		case itemStep:
			b.WriteByte('[')
			b.WriteString(strconv.Itoa(s.item))
			b.WriteByte(']')
		default:
			b.WriteString(".<key>")
		}
	}
	return b.String()
}

// add records "<location>: <message>".
func (c *checker) add(format string, a ...any) {
	if c.trial {
		c.fails = true
		return
	}
	c.errs = append(c.errs, c.at()+": "+fmt.Sprintf(format, a...))
}

func (c *checker) full() bool { return c.fails || len(c.errs) >= errorsMost }

// failed reports whether anything was added.
func (c *checker) failed() bool { return c.fails || len(c.errs) > 0 }

// trialOf is a checker for one branch, at c's location.
func (c *checker) trialOf() checker { return checker{path: c.path, trial: true, keys: c.keys} }

func (c *checker) checkAt(s map[string]any, v any, here string, next step) {
	c.path = append(c.path, next)
	c.check(s, v, here)
	c.path = c.path[:len(c.path)-1]
}

// resolve finds a $ref (#/$defs/X or <file>.json#/$defs/X) from the file here,
// from what collectRefs already walked; a ref the schemas do not carry is
// walked here, as before.
func resolve(ref, here string) (map[string]any, string) {
	if target, ok := refs[refSite{ref: ref, here: here}]; ok {
		return target.schema, target.file
	}
	return walkRef(ref, here)
}

// walkRef is resolve's own pointer walk: the file the ref names, else here,
// then one map step per segment of its JSON pointer.
func walkRef(ref, here string) (map[string]any, string) {
	name, ptr, _ := strings.Cut(ref, "#")
	if name == "" {
		name = here
	}
	var node any = files[name]
	for _, part := range strings.Split(strings.Trim(ptr, "/"), "/") {
		if part == "" {
			continue
		}
		m, _ := node.(map[string]any)
		node = m[part]
	}
	s, _ := node.(map[string]any)
	return s, name
}

func (c *checker) check(s map[string]any, v any, here string) {
	if c.full() || s == nil {
		return
	}
	kw := keywordsOf(s)
	if ref, ok := member(s, kw, kwRef, "$ref").(string); ok {
		target, f := resolve(ref, here)
		if target == nil {
			c.add("the schema has no %s", ref)
			return
		}
		c.check(target, v, f)
		if rest := refSiblings(s); len(rest) > 0 {
			c.check(rest, v, here)
		}
		return
	}
	if all, ok := member(s, kw, kwAllOf, "allOf").([]any); ok {
		for _, option := range all {
			c.check(option.(map[string]any), v, here)
		}
	}
	if anyOf, ok := member(s, kw, kwAnyOf, "anyOf").([]any); ok {
		passing := false
		for _, option := range anyOf {
			sub := c.trialOf()
			sub.check(option.(map[string]any), v, here)
			if !sub.failed() {
				passing = true
				break
			}
		}
		if !passing {
			c.add("no matching anyOf branch")
		}
	}
	if forbidden, ok := member(s, kw, kwNot, "not").(map[string]any); ok {
		sub := c.trialOf()
		sub.check(forbidden, v, here)
		if !sub.failed() {
			c.add("matches forbidden schema")
		}
	}
	if one, ok := member(s, kw, kwOneOf, "oneOf").([]any); ok {
		if disc, ok := s["x-discriminator"].(string); ok {
			obj, _ := v.(map[string]any)
			tag := obj[disc]
			matched := false
			for _, o := range one {
				option := o.(map[string]any)
				ref, _ := option["$ref"].(string)
				target, _ := resolve(ref, here)
				props, _ := target["properties"].(map[string]any)
				d, _ := props[disc].(map[string]any)
				if tag != nil && d["const"] == tag {
					// Check the complete branch, including overlays next to its $ref.
					c.check(option, v, here)
					matched = true
					break
				}
			}
			if !matched {
				c.add("no variant with %s %s", disc, show(tag))
				return
			}
		} else {
			passing := 0
			for _, o := range one {
				sub := c.trialOf()
				sub.check(o.(map[string]any), v, here)
				if !sub.failed() {
					passing++
				}
			}
			if passing != 1 {
				c.add("matches %d of oneOf, not one", passing)
			}
		}
		// All oneOf forms also retain their sibling type/properties constraints.
	}
	if t, ok := member(s, kw, kwType, "type").(string); ok && !isType(t, v) {
		c.add("not of type %s", t)
		return
	}
	if want := member(s, kw, kwConst, "const"); kw&kwConst != 0 && !equal(v, want) {
		c.add("not %s", show(want))
	}
	if enum, ok := member(s, kw, kwEnum, "enum").([]any); ok {
		in := false
		for _, e := range enum {
			if equal(v, e) {
				in = true
			}
		}
		if !in {
			c.add("%s is not one of %s", show(v), show(enum))
		}
	}
	switch x := v.(type) {
	case string:
		n := utf8.RuneCountInString(x)
		if m, ok := num(member(s, kw, kwMinLength, "minLength")); ok && float64(n) < m {
			c.add("shorter than %v", m)
		}
		if m, ok := num(member(s, kw, kwMaxLength, "maxLength")); ok && float64(n) > m {
			c.add("longer than %v", m)
		}
		if p, ok := member(s, kw, kwPattern, "pattern").(string); ok {
			if re := pattern(p); re == nil || !re.MatchString(x) {
				c.add("%s does not match %s", show(x), p)
			}
		}
	case float64:
		if m, ok := num(member(s, kw, kwMinimum, "minimum")); ok && x < m {
			c.add("below %v", m)
		}
		if m, ok := num(member(s, kw, kwMaximum, "maximum")); ok && x > m {
			c.add("above %v", m)
		}
	case json.Number:
		if cmp, ok := compareNumber(x, member(s, kw, kwMinimum, "minimum")); ok && cmp < 0 {
			c.add("below %s", show(member(s, kw, kwMinimum, "minimum")))
		}
		if cmp, ok := compareNumber(x, member(s, kw, kwMaximum, "maximum")); ok && cmp > 0 {
			c.add("above %s", show(member(s, kw, kwMaximum, "maximum")))
		}
	case []any:
		if m, ok := num(member(s, kw, kwMinItems, "minItems")); ok && float64(len(x)) < m {
			c.add("fewer than %v items", m)
		}
		if m, ok := num(member(s, kw, kwMaxItems, "maxItems")); ok && float64(len(x)) > m {
			c.add("more than %v items", m)
		}
		if member(s, kw, kwUniqueItems, "uniqueItems") == true {
			seen := map[string]bool{}
			for i, e := range x {
				if k := show(e); seen[k] {
					c.path = append(c.path, step{item: i, kind: itemStep})
					c.add("%s twice", k)
					c.path = c.path[:len(c.path)-1]
				} else {
					seen[k] = true
				}
			}
		}
		items, _ := member(s, kw, kwItems, "items").(map[string]any)
		for i, e := range x {
			c.checkAt(items, e, here, step{item: i, kind: itemStep})
		}
	case map[string]any:
		props, _ := member(s, kw, kwProperties, "properties").(map[string]any)
		if req, ok := member(s, kw, kwRequired, "required").([]any); ok {
			for _, k := range req {
				if _, has := x[k.(string)]; !has {
					c.add("no %s", k)
				}
			}
		}
		if m, ok := num(member(s, kw, kwMinProperties, "minProperties")); ok && float64(len(x)) < m {
			c.add("fewer than %v properties", m)
		}
		if m, ok := num(member(s, kw, kwMaxProperties, "maxProperties")); ok && float64(len(x)) > m {
			c.add("more than %v properties", m)
		}
		extra := member(s, kw, kwAdditionalProperties, "additionalProperties")
		start := len(c.keys)
		for k := range x {
			c.keys = append(c.keys, k)
		}
		sort.Strings(c.keys[start:])
		for i := range len(x) {
			// Checking a member appends its own object's names after these, and
			// may move the buffer, so each name is read from c.keys as it is.
			k := c.keys[start+i]
			if names, ok := member(s, kw, kwPropertyNames, "propertyNames").(map[string]any); ok {
				c.checkAt(names, k, here, step{kind: keyStep})
			}
			if p, ok := props[k].(map[string]any); ok {
				c.checkAt(p, x[k], here, step{member: k})
			} else if extra == false {
				c.add("unknown field %s", k)
			} else if e, ok := extra.(map[string]any); ok {
				c.checkAt(e, x[k], here, step{member: k})
			}
		}
		c.keys = c.keys[:start]
	}
}

func isType(t string, v any) bool {
	switch t {
	case "null":
		return v == nil
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "number", "integer":
		if n, ok := v.(json.Number); ok {
			if f, ok := fastNumber(n); ok {
				// fastNumber's float64 being finite means preciseNumber's is too. A literal without a
				// fraction or exponent is an integer; a value whose rounding keeps a fraction is not one.
				if t == "number" || !strings.ContainsAny(string(n), ".eE") {
					return true
				}
				if f != math.Trunc(f) {
					return false
				}
			}
			f, valid := preciseNumber(n)
			return valid && (t == "number" || f.IsInt())
		}
		f, ok := v.(float64)
		return ok && !math.IsNaN(f) && !math.IsInf(f, 0) && (t == "number" || f == math.Trunc(f))
	}
	return true
}

func num(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

// preciseNumber retains enough mantissa bits to distinguish the input's decimal digits from integer bounds.
// big.Float stores an exponent separately: hostile exponents never allocate an enormous integer/denominator.
func preciseNumber(v any) (*big.Float, bool) {
	switch n := v.(type) {
	case json.Number:
		f, _, err := big.ParseFloat(string(n), 10, uint(len(n))*4+64, big.ToNearestEven)
		return f, err == nil && !f.IsInf()
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) {
			return big.NewFloat(n), true
		}
	}
	return nil, false
}

// compareNumber orders the JSON number n against bound as preciseNumber's big.Float Cmp does; ok is false where
// preciseNumber rejects either side.
func compareNumber(n json.Number, bound any) (int, bool) {
	if bound == nil {
		return 0, false
	}
	if m, isFloat := bound.(float64); isFloat && !math.IsNaN(m) && !math.IsInf(m, 0) {
		// Correct rounding is monotonic and m is a float64: a parsed value below (above) m means the
		// exact value, and big.Float's closer rounding of it, are below (above) m too. Equal needs big.Float.
		if f, ok := fastNumber(n); ok && f != m {
			if f < m {
				return -1, true
			}
			return 1, true
		}
	}
	x, ok := preciseNumber(n)
	if !ok {
		return 0, false
	}
	y, ok := preciseNumber(bound)
	if !ok {
		return 0, false
	}
	return x.Cmp(y), true
}

// fastNumber parses n as the nearest float64 when n is JSON number text with an exponent of at most four
// digits and a finite float64 value. Other text is left to preciseNumber: strconv and big.ParseFloat read
// Inf or 0x10 differently, and big.ParseFloat rejects exponents that strconv rounds to zero.
func fastNumber(n json.Number) (float64, bool) {
	if !isShortJSONNumber(string(n)) {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(n), 64)
	return f, err == nil
}

// isShortJSONNumber reports whether s is a number in JSON's grammar (RFC 8259 section 6) whose exponent, if
// any, has at most four digits.
func isShortJSONNumber(s string) bool {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	digits := func() int {
		start := i
		for i < len(s) && '0' <= s[i] && s[i] <= '9' {
			i++
		}
		return i - start
	}
	if i < len(s) && s[i] == '0' {
		i++
	} else if digits() == 0 {
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		if digits() == 0 {
			return false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if d := digits(); d == 0 || d > 4 {
			return false
		}
	}
	return i == len(s)
}

func equal(a, b any) bool {
	// json.Marshal is injective on these, so the encodings match exactly when the values do.
	switch x := a.(type) {
	case string:
		if y, ok := b.(string); ok && utf8.ValidString(x) && utf8.ValidString(y) {
			return x == y
		}
	case bool:
		if y, ok := b.(bool); ok {
			return x == y
		}
	case nil:
		if b == nil {
			return true
		}
	}
	_, aNumber := a.(json.Number)
	_, bNumber := b.(json.Number)
	if aNumber != bNumber {
		n, bound := a, b
		if bNumber {
			n, bound = b, a
		}
		if cmp, ok := compareNumber(n.(json.Number), bound); ok {
			return cmp == 0
		}
	}
	if aNumber || bNumber {
		if left, ok := preciseNumber(a); ok {
			if right, ok := preciseNumber(b); ok {
				return left.Cmp(right) == 0
			}
		}
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func show(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func pattern(p string) *regexp.Regexp {
	if re, ok := patterns.Load(p); ok {
		return re.(*regexp.Regexp)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil
	}
	patterns.Store(p, re)
	return re
}
