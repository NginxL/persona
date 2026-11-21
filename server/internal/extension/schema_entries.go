package extension

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/nwverse/persona/server/internal/i18n"
)

const (
	entryIDField     = "_id"
	entryListMost    = 32
	entryFieldsMost  = 8
	entryIDGenerated = "entry_"
)

// EntryList reports whether f is the deliberately small list-of-entries form.
// Entry IDs are configuration metadata, not manifest-declared properties.
func (f Field) EntryList() bool {
	return f.Type == "array" && f.Items != nil && f.Items.Entry
}

func validateEntryList(f Field) error {
	if f.Entry {
		if f.Default != nil {
			return errors.New("default: entry objects have no defaults")
		}
		return nil
	}
	if !f.EntryList() {
		if f.Secret != nil {
			return errors.New("x-secret: only on a top-level array of flat entry objects")
		}
		if f.From != nil {
			return errors.New("x-from: only with an entry list's x-secret")
		}
		return nil
	}
	if f.Secret == nil {
		return errors.New("x-secret: required for an entry list")
	}
	if !secretPattern.MatchString(f.Secret.Name) {
		return errors.New("x-secret.name: a-z0-9_, a letter first, at most 32")
	}
	if f.Secret.Title == "" {
		return errors.New("x-secret.title: required")
	}
	if f.Secret.Hint != "" && f.Secret.Hint != HintLast4 {
		return fmt.Errorf("x-secret.hint: %q (only %q)", f.Secret.Hint, HintLast4)
	}
	if f.MaxItems == nil || *f.MaxItems < 1 || *f.MaxItems > entryListMost {
		return fmt.Errorf("maxItems: required, from 1 to %d", entryListMost)
	}
	if f.MinItems != nil && *f.MinItems > *f.MaxItems {
		return errors.New("minItems: no more than maxItems")
	}
	if f.UniqueItems {
		return errors.New("uniqueItems: entry identity, not whole-object uniqueness")
	}
	if f.Default != nil || f.Items.Default != nil {
		return errors.New("default: entry lists and their entries have no defaults")
	}
	if len(f.Items.Fields) == 0 || len(f.Items.Fields) > entryFieldsMost {
		return fmt.Errorf("items.properties: from 1 to %d text fields", entryFieldsMost)
	}
	for _, sub := range f.Items.Fields {
		if sub.Name == entryIDField {
			return errors.New("items.properties._id: reserved for the host")
		}
		if sub.Type != "string" {
			return errors.New("items.properties: text fields only")
		}
		if sub.Default != nil {
			return fmt.Errorf("items.properties.%s.default: entry fields have no defaults", sub.Name)
		}
		if sub.Secret != nil || sub.From != nil {
			return fmt.Errorf("items.properties.%s: entry annotations belong on the list", sub.Name)
		}
	}
	for entryField, oldField := range f.From {
		if !slices.ContainsFunc(f.Items.Fields, func(sub Field) bool { return sub.Name == entryField && sub.Type == "string" }) {
			return fmt.Errorf("x-from.%s: not an entry text field", entryField)
		}
		if !fieldName.MatchString(oldField) || oldField == f.Name || oldField == entryIDField {
			return fmt.Errorf("x-from.%s: not a fixed top-level field", entryField)
		}
		for otherEntry, otherOld := range f.From {
			if entryField < otherEntry && oldField == otherOld {
				return fmt.Errorf("x-from: %q is mapped twice", oldField)
			}
		}
	}
	return nil
}

// validateEntryMigrationSources gives each removed fixed slot to exactly one new list. A source that remains in the
// new schema is not moved, so x-from cannot describe a lossless migration or its inverse.
func validateEntryMigrationSources(fields []Field) error {
	owners := map[string]string{}
	for _, list := range fields {
		if !list.EntryList() {
			continue
		}
		for entryField, oldField := range list.From {
			if slices.ContainsFunc(fields, func(f Field) bool { return f.Name == oldField }) {
				return fmt.Errorf("configuration.%s.x-from.%s: %q remains a fixed field in the new schema", list.Name, entryField, oldField)
			}
			if owner := owners[oldField]; owner != "" && owner != list.Name {
				return fmt.Errorf("configuration.%s.x-from.%s: %q is already mapped by %s", list.Name, entryField, oldField, owner)
			}
			owners[oldField] = list.Name
		}
	}
	return nil
}

// checkEntrySecretNamespace keeps host-owned entry IDs disjoint from fixed-secret storage keys. Manifest declaration
// names are checked at parse time; this checks saved runtime identities when an upgrade or reload changes the fixed set.
func checkEntrySecretNamespace(m *Manifest, s *Schema, raw json.RawMessage) error {
	if m == nil || s == nil {
		return nil
	}
	fixed := map[string]bool{}
	for _, secret := range m.Secrets {
		fixed[secret.Name] = true
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil {
		return nil // schema validation has the useful JSON error
	}
	for _, list := range s.EntryListFields() {
		entries, err := decodeEntries(list, config[list.Name])
		if err != nil {
			continue
		}
		for i, entry := range entries {
			if fixed[entry.ID] {
				return fieldError(fmt.Sprintf("%s[%d].%s", list.Name, i, entryIDField), i18n.ErrEnglish("extension-status.schema_entry_id_duplicate"))
			}
		}
	}
	return nil
}

// EntryListFields returns the settings lists whose entries each own a secret.
func (s *Schema) EntryListFields() []Field {
	if s == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(s.Fields), func(f Field) bool { return !f.EntryList() })
}

func checkEntryNamespace(fields []Field, raw json.RawMessage) error {
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil {
		return nil // checkObject has already returned the useful error
	}
	seen := map[string]string{}
	for _, f := range fields {
		if !f.EntryList() {
			continue
		}
		entries, err := decodeEntries(f, config[f.Name])
		if err != nil {
			continue
		}
		for i, entry := range entries {
			if first, exists := seen[entry.ID]; exists {
				return fieldError(fmt.Sprintf("%s[%d].%s", f.Name, i, entryIDField),
					fmt.Errorf("the same entry id is already used by %s", first))
			}
			seen[entry.ID] = f.Name
		}
	}
	return nil
}

// Entry identifies one saved list entry and its flat, write-only secret key.
type Entry struct {
	ID     string
	Values map[string]json.RawMessage
}

func decodeEntries(f Field, raw json.RawMessage) ([]Entry, error) {
	var rows []map[string]json.RawMessage
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		var id string
		if err := json.Unmarshal(row[entryIDField], &id); err != nil {
			return nil, fmt.Errorf("%s: a string", entryIDField)
		}
		out = append(out, Entry{ID: id, Values: row})
	}
	return out, nil
}
