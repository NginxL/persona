package extension

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// preparedConfig is one canonical config write. IDs are minted here, once, never in a read projection.
type preparedConfig struct {
	base    json.RawMessage
	raw     json.RawMessage
	deleted []string
}

func compactConfig(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return slices.Clone(raw)
	}
	out, _ := json.Marshal(value)
	return out
}

func mintEntryID(reserved map[string]bool) (string, error) {
	for range 16 {
		var b [13]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		id := entryIDGenerated + hex.EncodeToString(b[:])
		if !reserved[id] {
			return id, nil
		}
	}
	return "", errors.New("could not allocate a unique entry id")
}

// prepareEntryConfig canonicalizes a full configuration write. A legacy writer has no entry-list editor: every list
// comes from the saved configuration, even when its stale full-config draft submitted an older array.
func prepareEntryConfig(p *Extension, raw json.RawMessage, preserveLists bool) (preparedConfig, error) {
	base := compactConfig(p.Record.Config)
	if err := checkEntrySecretNamespace(p.Manifest, p.Schema, base); err != nil {
		return preparedConfig{}, err
	}
	var next map[string]json.RawMessage
	if err := json.Unmarshal(raw, &next); err != nil || next == nil {
		return preparedConfig{}, fieldError("", i18n.Err("extension-status.schema_object"))
	}
	var oldConfig map[string]json.RawMessage
	_ = json.Unmarshal(base, &oldConfig)

	fixed := map[string]bool{}
	for _, secret := range p.Manifest.Secrets {
		fixed[secret.Name] = true
	}
	oldEntries := map[string]map[string]Entry{}
	oldOwner := map[string]string{}
	reserved := map[string]bool{}
	for name := range fixed {
		reserved[name] = true
	}
	for _, f := range p.Schema.EntryListFields() {
		entries, err := decodeEntries(f, oldConfig[f.Name])
		if err != nil {
			return preparedConfig{}, fmt.Errorf("saved entry list %s: %w", f.Name, err)
		}
		oldEntries[f.Name] = map[string]Entry{}
		for _, entry := range entries {
			oldEntries[f.Name][entry.ID] = entry
			oldOwner[entry.ID] = f.Name
			reserved[entry.ID] = true
		}
	}

	seenNext := map[string]bool{}
	for name := range fixed {
		seenNext[name] = true
	}
	for _, f := range p.Schema.EntryListFields() {
		if preserveLists {
			if old, ok := oldConfig[f.Name]; ok {
				next[f.Name] = slices.Clone(old)
			} else {
				delete(next, f.Name)
			}
			continue
		}
		rawEntries, present := next[f.Name]
		if !present || string(rawEntries) == "null" {
			continue
		}
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(rawEntries, &rows); err != nil {
			// The schema below returns the usual localized list error.
			continue
		}
		for i, row := range rows {
			if row == nil {
				return preparedConfig{}, fieldError(fmt.Sprintf("%s[%d]", f.Name, i), i18n.Err("extension-status.schema_object"))
			}
			var id string
			if rawID, ok := row[entryIDField]; ok && string(rawID) != "null" {
				if err := json.Unmarshal(rawID, &id); err != nil {
					return preparedConfig{}, fieldError(fmt.Sprintf("%s[%d].%s", f.Name, i, entryIDField), i18n.ErrEnglish("extension-status.schema_entry_id"))
				}
			}
			if id == "" {
				var err error
				id, err = mintEntryID(reserved)
				if err != nil {
					return preparedConfig{}, err
				}
				row[entryIDField], _ = json.Marshal(id)
			} else if oldOwner[id] != f.Name {
				return preparedConfig{}, fieldError(fmt.Sprintf("%s[%d].%s", f.Name, i, entryIDField), i18n.ErrEnglish("extension-status.schema_entry_id"))
			}
			if seenNext[id] {
				return preparedConfig{}, fieldError(fmt.Sprintf("%s[%d].%s", f.Name, i, entryIDField), i18n.ErrEnglish("extension-status.schema_entry_id_duplicate"))
			}
			seenNext[id], reserved[id] = true, true
			rows[i] = row
		}
		next[f.Name], _ = json.Marshal(rows)
	}
	compact, _ := json.Marshal(next)
	var checkErr error
	if preserveLists {
		checkErr = p.Schema.CheckPreservingEntries(compact)
	} else {
		checkErr = p.Schema.Check(compact)
	}
	if checkErr != nil {
		return preparedConfig{}, checkErr
	}
	if err := checkEntrySecretNamespace(p.Manifest, p.Schema, compact); err != nil {
		return preparedConfig{}, err
	}

	deleted := map[string]bool{}
	for list, entries := range oldEntries {
		var nextRows []Entry
		if !preserveLists {
			nextRows, _ = decodeEntries(Field{}, next[list])
		} else {
			nextRows, _ = decodeEntries(Field{}, oldConfig[list])
		}
		nextByID := map[string]Entry{}
		for _, entry := range nextRows {
			nextByID[entry.ID] = entry
		}
		for id, old := range entries {
			nextEntry, kept := nextByID[id]
			if !kept {
				deleted[id] = true
				continue
			}
			for _, network := range p.Manifest.Permissions.ConfiguredNetworks() {
				if network.List != list || network.Malformed {
					continue
				}
				if entryBindingChanged(network, old.Values[network.Field], nextEntry.Values[network.Field]) {
					deleted[id] = true
				}
			}
		}
	}
	out := preparedConfig{base: base, raw: compact}
	for id := range deleted {
		out.deleted = append(out.deleted, id)
	}
	slices.Sort(out.deleted)
	return out, nil
}

func entryBindingChanged(network ConfiguredNetwork, oldRaw, nextRaw json.RawMessage) bool {
	var oldValue, nextValue string
	oldOK := json.Unmarshal(oldRaw, &oldValue) == nil
	nextOK := json.Unmarshal(nextRaw, &nextValue) == nil
	if oldOK && nextOK && strings.TrimSpace(oldValue) == strings.TrimSpace(nextValue) {
		return false
	}
	if !oldOK || !nextOK {
		return true
	}
	oldTarget, _ := configuredTargetValue(network, oldValue)
	nextTarget, _ := configuredTargetValue(network, nextValue)
	return oldTarget.Host == "" || nextTarget.Host == "" || oldTarget != nextTarget
}

// entrySecretBinding resolves a flat secret key to the one saved entry that owns it.
type entrySecretBinding struct {
	List  Field
	Entry Entry
	Spec  SecretSpec
	Index int
}

func activeEntrySecrets(p *Extension) map[string]entrySecretBinding {
	out := map[string]entrySecretBinding{}
	if p == nil || p.Schema == nil {
		return out
	}
	var config map[string]json.RawMessage
	_ = json.Unmarshal(p.Record.Config, &config)
	fixed := map[string]bool{}
	for _, secret := range p.Manifest.Secrets {
		fixed[secret.Name] = true
	}
	for _, f := range p.Schema.EntryListFields() {
		entries, err := decodeEntries(f, config[f.Name])
		if err != nil {
			continue
		}
		for i, entry := range entries {
			if fixed[entry.ID] {
				continue // a corrupt/ambiguous row never turns a fixed storage key into an entry credential
			}
			spec := *f.Secret
			spec.Name = entry.ID
			out[entry.ID] = entrySecretBinding{List: f, Entry: entry, Spec: spec, Index: i}
		}
	}
	return out
}

func activeEntrySecretsIn(p *Extension, config json.RawMessage) map[string]entrySecretBinding {
	copy := *p
	copy.Record.Config = config
	return activeEntrySecrets(&copy)
}

func entryLabel(binding entrySecretBinding) string {
	fields := binding.List.Items.Fields
	for pass := range 2 {
		for _, f := range fields {
			if pass == 0 && f.Name != "name" {
				continue
			}
			var value string
			if json.Unmarshal(binding.Entry.Values[f.Name], &value) == nil && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

// IsLiveEntrySecret reports whether name is the storage ID of a currently saved list entry.
func (p *Extension) IsLiveEntrySecret(name string) bool {
	_, ok := activeEntrySecrets(p)[name]
	return ok
}

// EntrySecretTitle resolves a live entry storage ID to its display title in l, using the editor's numbered fallback
// when every text field is empty. The boolean is false for fixed secrets and removed entries.
func (p *Extension) EntrySecretTitle(name string, l i18n.Lang) (string, bool) {
	binding, ok := activeEntrySecrets(p)[name]
	if !ok {
		return "", false
	}
	if title := entryLabel(binding); title != "" {
		return title, true
	}
	return i18n.T(l, "extension-status.entry_untitled", i18n.Args{"count": binding.Index + 1}), true
}

// SecretDeclaration resolves either a fixed secret name or a live entry ID to its declared text. For an entry the
// returned Name is the list's logical declaration name; callers keep the entry ID as the storage key separately.
func (p *Extension) SecretDeclaration(name string) (SecretSpec, bool) {
	for _, spec := range p.Manifest.Secrets {
		if spec.Name == name {
			return spec, true
		}
	}
	if binding, ok := activeEntrySecrets(p)[name]; ok {
		return *binding.List.Secret, true
	}
	return SecretSpec{}, false
}

func activeSecretNames(p *Extension, config json.RawMessage) map[string]bool {
	out := map[string]bool{}
	for _, spec := range p.Manifest.Secrets {
		out[spec.Name] = true
	}
	for id := range activeEntrySecretsIn(p, config) {
		out[id] = true
	}
	return out
}
