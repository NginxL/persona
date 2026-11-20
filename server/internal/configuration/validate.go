package configuration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxNumber = 9007199254740991

// CheckDocument checks a producer's whole snapshot, including the typed defaults
// and effective values. An older client may display an unknown kind read-only;
// producers never emit a kind they cannot validate themselves.
func CheckDocument(d Document) error {
	if d.SchemaVersion != Version {
		return fmt.Errorf("unsupported configuration schema version %d", d.SchemaVersion)
	}
	if !named(d.ID) || !named(d.Revision) || !named(d.Title) || d.Groups == nil || d.Values == nil {
		return fmt.Errorf("configuration needs id, revision, title, groups and values")
	}
	raw, err := json.Marshal(d)
	if err != nil || len(raw) > MaxBytes {
		return fmt.Errorf("configuration is not JSON within %d bytes", MaxBytes)
	}
	groups, fields := map[string]bool{}, map[string]bool{}
	for _, g := range d.Groups {
		if !named(g.ID) || !named(g.Title) || groups[g.ID] || g.Fields == nil {
			return fmt.Errorf("invalid or duplicate configuration group %q", g.ID)
		}
		groups[g.ID] = true
		for _, f := range g.Fields {
			if fields[f.ID] {
				return fmt.Errorf("duplicate configuration field %q", f.ID)
			}
			fields[f.ID] = true
			if err := checkField(f); err != nil {
				return fmt.Errorf("field %q: %w", f.ID, err)
			}
			if code := ValueError(f, f.Default); code != "" {
				return fmt.Errorf("field %q default: %s", f.ID, code)
			}
			if code := ValueError(f, d.Values[f.ID]); code != "" {
				return fmt.Errorf("field %q effective value: %s", f.ID, code)
			}
		}
	}
	for id := range d.Values {
		if !fields[id] {
			return fmt.Errorf("value for unknown field %q", id)
		}
	}
	for id, n := range d.Notices {
		if !fields[id] || !named(n.Message) || (n.Kind != "info" && n.Kind != "warning") {
			return fmt.Errorf("invalid notice for field %q", id)
		}
	}
	return nil
}

func named(s string) bool { return strings.TrimSpace(s) != "" }

func checkField(f Field) error {
	if !named(f.ID) || !named(f.Title) {
		return fmt.Errorf("id and title are required")
	}
	if f.DisabledReason != "" && !f.ReadOnly {
		return fmt.Errorf("disabled_reason needs read_only")
	}
	for _, d := range f.Details {
		if !named(d.Title) || !named(d.Description) {
			return fmt.Errorf("details need a title and description")
		}
	}
	count := 0
	for _, present := range []bool{f.Text != nil, f.Select != nil, f.Multiselect != nil, f.Number != nil} {
		if present {
			count++
		}
	}
	if (f.Kind == Toggle && count != 0) || (f.Kind != Toggle && count != 1) {
		return fmt.Errorf("kind must have exactly its matching options (toggle has none)")
	}
	switch f.Kind {
	case Toggle:
	case Text:
		if f.Text == nil || !counts(f.Text.MinLength, f.Text.MaxLength) {
			return fmt.Errorf("text options need ordered nonnegative length bounds")
		}
	case Select:
		if f.Select == nil || !slices.Contains([]string{"", "list", "dropdown", "segmented"}, f.Select.Presentation) {
			return fmt.Errorf("select options need a supported presentation")
		}
		return checkOptions(f.Select.Options)
	case Multiselect:
		m := f.Multiselect
		if m == nil || !counts(m.MinSelected, m.MaxSelected) {
			return fmt.Errorf("multiselect options need ordered nonnegative count bounds")
		}
		if m.MinSelected != nil && *m.MinSelected > len(m.Options) {
			return fmt.Errorf("min_selected exceeds available options")
		}
		return checkOptions(m.Options)
	case Number:
		n := f.Number
		if n == nil || !numberBound(n.Minimum) || !numberBound(n.Maximum) || !numberBound(n.Step) {
			return fmt.Errorf("number options need finite portable bounds")
		}
		if n.Minimum != nil && n.Maximum != nil && *n.Minimum > *n.Maximum {
			return fmt.Errorf("minimum exceeds maximum")
		}
		if n.Step != nil && *n.Step <= 0 {
			return fmt.Errorf("step must be positive")
		}
	default:
		return fmt.Errorf("unsupported kind %q", f.Kind)
	}
	return nil
}

func counts(min, max *int) bool {
	return (min == nil || *min >= 0) && (max == nil || *max >= 0) && (min == nil || max == nil || *min <= *max)
}

func numberBound(n *float64) bool {
	return n == nil || (!math.IsNaN(*n) && !math.IsInf(*n, 0) && math.Abs(*n) <= maxNumber)
}

func checkOptions(options []Option) error {
	if len(options) == 0 {
		return fmt.Errorf("options must not be empty")
	}
	seen := map[string]bool{}
	for _, o := range options {
		if !named(o.Value) || !named(o.Title) || seen[o.Value] {
			return fmt.Errorf("options need unique nonempty values and titles")
		}
		seen[o.Value] = true
	}
	return nil
}

// ValueError returns a stable validation code or "". Definitions must have passed
// CheckDocument/checkField. No coercion, trimming, option fallback or null defaulting
// happens on writes; a source handles its own legacy storage when reading.
func ValueError(f Field, raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || !json.Valid(raw) {
		return "type"
	}
	switch f.Kind {
	case Toggle:
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return "type"
		}
	case Text, Select:
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "type"
		}
		if f.Kind == Select {
			if f.Select == nil || !hasOption(f.Select.Options, s) {
				return "option"
			}
			return ""
		}
		if f.Text == nil {
			return "unsupported"
		}
		n := utf8.RuneCountInString(s)
		if f.Text.MinLength != nil && n < *f.Text.MinLength {
			return "min_length"
		}
		if f.Text.MaxLength != nil && n > *f.Text.MaxLength {
			return "max_length"
		}
	case Multiselect:
		var values []json.RawMessage
		if f.Multiselect == nil || json.Unmarshal(raw, &values) != nil {
			return "type"
		}
		seen := map[string]bool{}
		for _, v := range values {
			var s string
			if bytes.Equal(bytes.TrimSpace(v), []byte("null")) || json.Unmarshal(v, &s) != nil {
				return "type"
			}
			if !hasOption(f.Multiselect.Options, s) {
				return "option"
			}
			if seen[s] {
				return "duplicate"
			}
			seen[s] = true
		}
		if min := f.Multiselect.MinSelected; min != nil && len(values) < *min {
			return "min_selected"
		}
		if max := f.Multiselect.MaxSelected; max != nil && len(values) > *max {
			return "max_selected"
		}
	case Number:
		token := string(bytes.TrimSpace(raw))
		// json.Valid already checked the grammar. A number starts with a digit
		// or a minus; in particular a quoted number is still a string.
		if f.Number == nil || (token[0] != '-' && (token[0] < '0' || token[0] > '9')) {
			return "type"
		}
		if code := wireNumberError(token, f.Number.Integer); code != "" {
			return code
		}
		n, err := strconv.ParseFloat(token, 64)
		if err != nil {
			return "range"
		}
		if f.Number.Minimum != nil && n < *f.Number.Minimum {
			return "minimum"
		}
		if f.Number.Maximum != nil && n > *f.Number.Maximum {
			return "maximum"
		}
	default:
		return "unsupported"
	}
	return ""
}

// wireNumberError checks the exact portable magnitude and integrality of a
// valid JSON number. Float64 can round a fractional number onto an integer or
// onto the portable boundary. Keep the exponent as a count, so even an enormous
// exponent needs no more storage than its spelling.
func wireNumberError(token string, integer bool) string {
	mantissa := strings.TrimPrefix(token, "-")
	var exponent int64
	if i := strings.IndexAny(mantissa, "eE"); i >= 0 {
		// On overflow ParseInt saturates in the correct direction. The
		// maximum document size is far smaller than either limit.
		exponent, _ = strconv.ParseInt(mantissa[i+1:], 10, 32)
		mantissa = mantissa[:i]
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return ""
	}
	// Position of the decimal point relative to the first significant digit.
	point := int64(len(digits)-len(fraction)) + exponent
	digits = strings.TrimRight(digits, "0")
	limit := strconv.FormatInt(maxNumber, 10)
	if point > int64(len(limit)) || (point == int64(len(limit)) && digits > limit) {
		return "range"
	}
	if integer && point < int64(len(digits)) {
		return "integer"
	}
	return ""
}

func hasOption(options []Option, value string) bool {
	return slices.ContainsFunc(options, func(o Option) bool { return o.Value == value })
}

// CheckUpdate holds an atomic partial update to the current authoritative snapshot.
// Call this inside the owner's persistence transaction, not only before acquiring it.
func CheckUpdate(d Document, u Update) error {
	if u.SchemaVersion != Version {
		return &Error{Code: "configuration_version", Message: "Unsupported configuration version"}
	}
	if err := CheckDocument(d); err != nil {
		return fmt.Errorf("invalid current configuration: %w", err)
	}
	if u.ID != d.ID || u.Revision != d.Revision {
		return &Error{Code: "configuration_conflict", Message: "Configuration changed; reload before saving"}
	}
	raw, err := json.Marshal(u)
	if err != nil || len(raw) > MaxBytes || len(u.Changes)+len(u.Reset) == 0 {
		return &Error{Code: "configuration_invalid", Message: "Invalid configuration update"}
	}
	fields := map[string]Field{}
	for _, g := range d.Groups {
		for _, f := range g.Fields {
			fields[f.ID] = f
		}
	}
	var problems []FieldError
	check := func(id string, value json.RawMessage, reset bool) {
		f, exists := fields[id]
		code := ""
		switch {
		case !exists:
			code = "unknown"
		case f.ReadOnly:
			code = "read_only"
		case !reset:
			code = ValueError(f, value)
		}
		if code != "" {
			problems = append(problems, FieldError{ID: id, Code: code})
		}
	}
	keys := make([]string, 0, len(u.Changes))
	for id := range u.Changes {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	for _, id := range keys {
		check(id, u.Changes[id], false)
	}
	seen := map[string]bool{}
	for _, id := range u.Reset {
		_, changed := u.Changes[id]
		if changed || seen[id] {
			problems = append(problems, FieldError{ID: id, Code: "duplicate"})
		} else {
			check(id, nil, true)
		}
		seen[id] = true
	}
	if len(problems) > 0 {
		return &Error{Code: "configuration_invalid", Message: "Some configuration values are invalid", FieldErrors: problems}
	}
	return nil
}

// ApplyUpdate validates and returns a detached effective-value map, without mutating
// the snapshot, persisting anything or assigning a revision. The owner also receives
// Update.Reset so it can remove overrides instead of pinning today's defaults.
func ApplyUpdate(d Document, u Update) (map[string]json.RawMessage, error) {
	if err := CheckUpdate(d, u); err != nil {
		return nil, err
	}
	values := make(map[string]json.RawMessage, len(d.Values))
	for id, v := range d.Values {
		values[id] = bytes.Clone(v)
	}
	for id, v := range u.Changes {
		values[id] = bytes.Clone(v)
	}
	for _, g := range d.Groups {
		for _, f := range g.Fields {
			if slices.Contains(u.Reset, f.ID) {
				values[f.ID] = bytes.Clone(f.Default)
			}
		}
	}
	return values, nil
}
