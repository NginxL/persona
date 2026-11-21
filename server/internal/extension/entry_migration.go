package extension

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// migrateFixedSlots moves explicitly mapped fixed fields into the first entry of each new list. The old secret name
// becomes that entry's ID, so plugin_secrets keeps the original sealed row without opening or copying it.
// declared reports that old and new manifests have a migration relationship, even when there is no saved value to move.
func migrateFixedSlots(old, next *Manifest, raw json.RawMessage, sealed map[string]string) (config json.RawMessage, declared bool, err error) {
	oldSchema, err := ParseSchema(old.Configuration)
	if err != nil {
		return nil, false, err
	}
	var oldFields []Field
	if oldSchema != nil {
		oldFields = oldSchema.Fields
	}
	nextSchema, err := ParseSchema(next.Configuration)
	if err != nil {
		return nil, false, err
	}
	config = compactConfig(raw)
	var values map[string]json.RawMessage
	if err := json.Unmarshal(config, &values); err != nil {
		return nil, false, err
	}
	changed := false
	for _, list := range nextSchema.EntryListFields() {
		if len(list.From) == 0 || !slices.ContainsFunc(old.Secrets, func(s SecretSpec) bool { return s.Name == list.Secret.Name }) {
			continue
		}
		declared = true
		for entryField, oldField := range list.From {
			if !slices.ContainsFunc(oldFields, func(f Field) bool { return f.Name == oldField && f.Type == "string" }) {
				return nil, true, fmt.Errorf("configuration.%s.x-from.%s: %q is not an installed fixed text field", list.Name, entryField, oldField)
			}
			if slices.ContainsFunc(nextSchema.Fields, func(f Field) bool { return f.Name == oldField }) {
				return nil, true, fmt.Errorf("configuration.%s.x-from.%s: %q remains a fixed field in the new schema", list.Name, entryField, oldField)
			}
		}
		var existing []map[string]json.RawMessage
		if rawList, ok := values[list.Name]; ok && string(rawList) != "null" {
			if err := json.Unmarshal(rawList, &existing); err != nil {
				return nil, true, fmt.Errorf("configuration.%s: existing entries conflict with fixed-slot migration", list.Name)
			}
		}
		hasInput := false
		for _, oldField := range list.From {
			if _, ok := values[oldField]; ok {
				hasInput = true
			}
		}
		if len(existing) > 0 {
			var legacyID string
			if len(existing) == 1 {
				_ = json.Unmarshal(existing[0][entryIDField], &legacyID)
			}
			if !hasInput && legacyID == list.Secret.Name {
				continue // an interrupted/repeated migration has already produced this exact slot
			}
			return nil, true, fmt.Errorf("configuration.%s: existing entries conflict with fixed-slot migration", list.Name)
		}
		entry := map[string]json.RawMessage{}
		for entryField, oldField := range list.From {
			if value, ok := values[oldField]; ok && string(value) != "null" {
				entry[entryField] = slices.Clone(value)
			}
		}
		_, hasSecret := sealed[list.Secret.Name]
		if len(entry) == 0 && !hasSecret {
			if hasInput { // only null legacy values: nothing becomes an entry, and nothing non-default is lost
				for _, oldField := range list.From {
					delete(values, oldField)
				}
				changed = true
			}
			continue
		}
		entry[entryIDField], _ = json.Marshal(list.Secret.Name)
		values[list.Name], _ = json.Marshal([]map[string]json.RawMessage{entry})
		for _, oldField := range list.From {
			delete(values, oldField)
		}
		changed = true
	}
	if changed {
		config, _ = json.Marshal(values)
	}
	return config, declared, nil
}

// rollbackFixedSlots is the lossless inverse of migrateFixedSlots. Any entry or field that cannot be represented in
// the old fixed slots refuses the rollback; no config or secret is changed in that case.
func rollbackFixedSlots(current, previous *Manifest, raw json.RawMessage) (json.RawMessage, error) {
	currentSchema, err := ParseSchema(current.Configuration)
	if err != nil {
		return nil, err
	}
	previousSchema, err := ParseSchema(previous.Configuration)
	if err != nil {
		return nil, err
	}
	entryLists := currentSchema.EntryListFields()
	if len(entryLists) == 0 {
		return compactConfig(raw), nil // preserve the pre-entry-list rollback behavior for every other schema
	}
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal(compactConfig(raw), &values); err != nil {
		return nil, err
	}
	for _, list := range entryLists {
		if len(list.From) == 0 || !slices.ContainsFunc(previous.Secrets, func(s SecretSpec) bool { return s.Name == list.Secret.Name }) {
			continue
		}
		entries, err := decodeEntries(list, values[list.Name])
		if err != nil {
			return nil, fmt.Errorf("configuration.%s: cannot invert fixed-slot migration: %w", list.Name, err)
		}
		if len(entries) == 0 {
			delete(values, list.Name)
			continue
		}
		if len(entries) != 1 || entries[0].ID != list.Secret.Name {
			return nil, errors.New("rollback would discard entries added after fixed-slot migration")
		}
		entry := entries[0]
		for entryField, value := range entry.Values {
			if entryField == entryIDField || string(value) == "null" {
				continue
			}
			oldField, mapped := list.From[entryField]
			if !mapped {
				return nil, fmt.Errorf("rollback would discard %s.%s, which has no fixed-slot destination", list.Name, entryField)
			}
			if _, exists := values[oldField]; exists {
				return nil, fmt.Errorf("rollback cannot replace the existing fixed field %s", oldField)
			}
			values[oldField] = slices.Clone(value)
		}
		delete(values, list.Name)
	}
	out, _ := json.Marshal(values)
	// This also rejects unrelated new-only configuration instead of carrying values the old manifest cannot describe.
	if err := previousSchema.CheckSaved(out); err != nil {
		return nil, fmt.Errorf("rollback would leave configuration the previous version cannot represent: %w", err)
	}
	return out, nil
}
