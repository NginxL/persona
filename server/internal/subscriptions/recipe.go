package subscriptions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/jsonsubset"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Limits on what is kept of an item.
const (
	bodyMax    = 8000 // subscription_items
	previewMax = 200  // a dry run's body
	exampleMax = 120  // a dry run's path examples
	titleMax   = 500
	idMax      = 500 // an item's, a sender's or a chat's id
	pathsMax   = 40
	keysMax    = 30
)

// A path of the recipe's subset: $, .field and [index] (#2037). step.key is "" for an index step.
type step struct {
	key   string
	index int
}

var (
	pathRE = lazyre.New(`^\$(?:\.[A-Za-z_][A-Za-z0-9_-]*|\[[0-9]+\])*$`)
	keyRE  = lazyre.New(`^[A-Za-z_][A-Za-z0-9_-]*$`) // a key a path can name
)

func parsePath(p string) ([]step, error) {
	if !pathRE.MatchString(p) {
		return nil, fmt.Errorf("path %q is not $, .field or [index]", p)
	}
	var steps []step
	rest := p[1:]
	for rest != "" {
		if rest[0] == '.' {
			end := strings.IndexAny(rest[1:], ".[")
			if end < 0 {
				end = len(rest) - 1
			}
			steps = append(steps, step{key: rest[1 : end+1]})
			rest = rest[end+1:]
			continue
		}
		end := strings.IndexByte(rest, ']')
		n, err := strconv.Atoi(rest[1:end])
		if err != nil {
			return nil, err
		}
		steps = append(steps, step{index: n})
		rest = rest[end+1:]
	}
	return steps, nil
}

// at is the value at path p in v; ok false when it is not there.
func at(v any, p string) (any, bool) {
	steps, err := parsePath(p)
	if err != nil {
		return nil, false
	}
	for _, s := range steps {
		if s.key != "" {
			m, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			if v, ok = m[s.key]; !ok {
				return nil, false
			}
			continue
		}
		a, ok := v.([]any)
		if !ok || s.index >= len(a) {
			return nil, false
		}
		v = a[s.index]
	}
	return v, true
}

// text is a value as a field shows it: a string as it is, a number or boolean written out, anything else as compact
// JSON; "" for null or absent.
func text(v any, ok bool) string {
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// The template variables a recipe's string arguments may use (#2037), nothing else.
var varRE = lazyre.New(`\{\{\s*([A-Za-z_]+)\s*\}\}`)

// vars are the values of the template variables for a run that pulls what came after since.
func vars(since time.Time, loc *time.Location) map[string]string {
	return map[string]string{
		"since":      since.UTC().Format(time.RFC3339),
		"since_date": since.In(loc).Format(time.DateOnly),
	}
}

// fillArgs is the recipe's arguments with the variables filled in: an object, default {}. A variable other than
// those in vals is an error.
func fillArgs(raw json.RawMessage, vals map[string]string) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = emptyObject
	}
	v, err := jsonsubset.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("args are not JSON: %w", err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("args are not an object")
	}
	var bad string
	var fill func(any) any
	fill = func(v any) any {
		switch x := v.(type) {
		case string:
			return varRE.ReplaceAllStringFunc(x, func(m string) string {
				name := varRE.FindStringSubmatch(m)[1]
				val, ok := vals[name]
				if !ok && bad == "" {
					bad = name
				}
				return val
			})
		case map[string]any:
			for k, e := range x {
				x[k] = fill(e)
			}
		case []any:
			for i, e := range x {
				x[i] = fill(e)
			}
		}
		return v
	}
	fill(obj)
	if bad != "" {
		return nil, fmt.Errorf("args use {{%s}}; only {{since}} and {{since_date}} are filled in", bad)
	}
	return obj, nil
}

// checkArgs holds filled arguments to the tool's input schema (the JSON Schema subset of model.complete).
func checkArgs(schema json.RawMessage, args map[string]any) error {
	root, err := jsonsubset.Compile(schema)
	if err != nil {
		return err
	}
	// Numbers as json.Number, as the checker reads them.
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	v, err := jsonsubset.Decode(raw)
	if err != nil {
		return err
	}
	if p := jsonsubset.Validate(root, v); p != "" {
		return fmt.Errorf("%s", p)
	}
	return nil
}

// result is a tool's answer read: JSON when it is JSON (also a JSON string that holds JSON), text otherwise.
type result struct {
	json  any
	isObj bool
	text  string
}

func readResult(s string) result {
	t := strings.TrimSpace(s)
	if v, err := jsonsubset.Decode([]byte(t)); err == nil {
		if inner, ok := v.(string); ok {
			if w, err := jsonsubset.Decode([]byte(strings.TrimSpace(inner))); err == nil {
				if _, isStr := w.(string); !isStr {
					v = w
				}
			}
		}
		if _, isStr := v.(string); !isStr {
			return result{json: v, isObj: true, text: t}
		}
	}
	return result{text: t}
}

// shapeOf is a result's shape: its type, an object's keys and the length of the list at itemsPath.
func shapeOf(r result, itemsPath string) *Shape {
	if !r.isObj {
		return &Shape{Type: "text"}
	}
	sh := &Shape{Type: "json"}
	if m, ok := r.json.(map[string]any); ok {
		for k, v := range m {
			sh.Keys = append(sh.Keys, k)
			if _, isList := v.([]any); isList {
				sh.Arrays = append(sh.Arrays, k)
			}
		}
		sort.Strings(sh.Keys)
		sort.Strings(sh.Arrays)
		if len(sh.Keys) > keysMax {
			sh.Keys = sh.Keys[:keysMax]
		}
		if len(sh.Arrays) > keysMax {
			sh.Arrays = sh.Arrays[:keysMax]
		}
	}
	if list, ok := at(r.json, cmpOr(itemsPath, "$")); ok {
		if a, ok := list.([]any); ok {
			n := len(a)
			sh.Items = &n
		}
	}
	return sh
}

// findItems is where the items are when a recipe leaves items_path empty: the result itself when it is a list, else
// its one top-level list (a key a path can name). "" when there is no such one place: none, or more than one.
func findItems(v any) string {
	if _, ok := v.([]any); ok {
		return "$"
	}
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	found := ""
	for k, x := range m {
		if _, isList := x.([]any); !isList {
			continue
		}
		if found != "" || !keyRE.MatchString(k) {
			return ""
		}
		found = "$." + k
	}
	return found
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// mapped is one item read through the recipe's fields.
type mapped struct {
	item     Item
	idFound  bool
	hash     string // of every mapped value: content changes
	timeBad  bool
	dueBad   bool
	rawTime  string
	rawDue   string
	rawSeen  string
	hasTitle bool
}

func mapItem(v any, f *connector.SubscriptionFields, kind string, loc *time.Location) mapped {
	if f == nil {
		f = &connector.SubscriptionFields{}
	}
	get := func(p string) string {
		if p == "" {
			return ""
		}
		x, ok := at(v, p)
		return text(x, ok)
	}
	m := mapped{}
	it := Item{Kind: kind}
	id := get(f.ID)
	it.Thread = clip(get(f.Thread), titleMax)
	it.Title = clip(oneLine(get(f.Title)), titleMax)
	it.Sender = clip(oneLine(get(f.Sender)), titleMax)
	// Ids as the source writes them: a built-in's mutes match them as they are (Lark's sender and chat ids).
	it.SenderID = clip(strings.TrimSpace(get(f.SenderID)), idMax)
	it.Chat = clip(strings.TrimSpace(get(f.Chat)), idMax)
	it.ChatName = clip(oneLine(get(f.ChatName)), titleMax)
	it.ReplyTo = clip(oneLine(get(f.ReplyTo)), titleMax)
	m.rawSeen = get(f.Seen)
	it.Seen = seenValue(m.rawSeen)
	it.Body = clip(get(f.Body), bodyMax)
	it.Link = clip(get(f.Link), 2000)
	m.rawTime, m.rawDue = get(f.Time), get(f.DueDate)
	if m.rawTime != "" {
		if t, ok := parseTime(m.rawTime, loc); ok {
			it.Time = &t
		} else {
			m.timeBad = true
		}
	}
	if m.rawDue != "" {
		if t, ok := parseTime(m.rawDue, loc); ok {
			it.DueDate = &t
		} else {
			m.dueBad = true
		}
	}
	m.hasTitle = it.Title != ""
	m.hash = hashOf(id, it.Thread, it.Title, it.Sender, m.rawTime, it.Body, it.Link, m.rawDue)
	if id != "" {
		it.UID, m.idFound = clip(id, idMax), true
	} else {
		it.UID = weakPrefix + hashOf(it.Title, m.rawTime, it.Sender)[:24]
	}
	m.item = it
	return m
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// weakPrefix starts the uid of an item without an id: a hash of its title, time and sender, which the source knows
// nothing of (so it cannot be replied to by it).
const weakPrefix = "h:"

// seenValue says whether a seen field's value (as text writes it) means seen: true, a non-zero number, or true, 1,
// yes, read or seen in any case.
func seenValue(s string) bool {
	switch strings.ToLower(s) {
	case "true", "yes", "read", "seen":
		return true
	}
	f, err := strconv.ParseFloat(s, 64)
	return err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) && f != 0
}

func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// parseTime reads the times sources write: RFC 3339, a date and time without a zone (in loc), a date, and Unix
// seconds or milliseconds.
func parseTime(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", time.DateOnly} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UTC(), true
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		switch {
		case f >= 1e9 && f < 1e11:
			sec, frac := math.Modf(f)
			return time.Unix(int64(sec), int64(frac*1e9)).UTC(), true
		case f >= 1e11 && f < 1e14:
			return time.UnixMilli(int64(f)).UTC(), true
		}
	}
	return time.Time{}, false
}

// leaves are the first item's leaf paths and values, for the field pickers.
func leaves(v any) []PathValue {
	var out []PathValue
	var walk func(v any, p string, depth int)
	walk = func(v any, p string, depth int) {
		if len(out) >= pathsMax || depth > 6 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if !keyRE.MatchString(k) {
					continue
				}
				walk(x[k], p+"."+k, depth+1)
			}
		case []any:
			for i, e := range x {
				if i >= 3 {
					break
				}
				walk(e, fmt.Sprintf("%s[%d]", p, i), depth+1)
			}
		default:
			if s := text(x, true); s != "" {
				out = append(out, PathValue{Path: p, Example: clip(oneLine(s), exampleMax)})
			}
		}
	}
	walk(v, "$", 0)
	return out
}

// fingerprint is a tool as a recipe depends on it: its name, its input schema and its read-only declarations.
func fingerprint(t connector.CatalogTool) string {
	schema := []byte(t.InputSchema)
	if v, err := jsonsubset.Decode(schema); err == nil {
		schema, _ = json.Marshal(v) // map keys sorted: the same schema written another way is the same tool
	}
	ann, _ := json.Marshal(t.Annotations)
	return hashOf(t.Name, string(schema), strconv.FormatBool(t.ReadOnly), string(ann))[:32]
}

// pageLimit is the page size the recipe's arguments ask for, when one of the usual names says it (0: not said).
func pageLimit(args map[string]any) int {
	for _, k := range []string{"limit", "top", "max", "max_results", "maxResults", "count", "page_size", "pageSize", "per_page", "perPage", "size", "n"} {
		if n, ok := args[k].(json.Number); ok {
			if i, err := n.Int64(); err == nil && i > 0 {
				return int(i)
			}
		}
	}
	return 0
}
