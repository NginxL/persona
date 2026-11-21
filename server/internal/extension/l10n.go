package extension

// Translations of a manifest's text (#897, docs/extensions/extensions.md "Translations"): a package's
// l10n/<language>.json files, and an index entry's l10n with the same text so a market can show it before anything
// is downloaded. The manifest itself does not change: hubs read manifests strictly, so a new manifest field would
// make the extension uninstallable on older hubs, while files and entry keys they do not read are harmless. A
// translation that does not check out is left out with a warning; it never blocks an install.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
)

const (
	// L10nDir is the package's directory of translations, one <language>.json each.
	L10nDir = "l10n"
	// l10nFileMost is the largest translation file read; an index entry's l10n may hold one of these per language.
	l10nFileMost = 64 << 10
)

// Translation is a manifest's text in one language. An empty string is not translated.
type Translation struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Configuration: by property name (configuration.properties).
	Configuration map[string]*FieldText `json:"configuration,omitempty"`
	// Secrets: by secret name.
	Secrets map[string]*SecretText `json:"secrets,omitempty"`
	// UI: the titles of its ui contributions (pages), by contribution id.
	UI map[string]string `json:"ui,omitempty"`
	// Groups: the titles of the sidebar groups its nav_pages name (UIGroup), by group id.
	Groups map[string]string `json:"groups,omitempty"`
}

// FieldText is a configuration property's text: its title, description and confirmation (x-confirm), its choices'
// labels by value (a string choice by the string, a number by how JSON writes it; an array's apply to its items), and
// an object's own properties.
type FieldText struct {
	Title       string                `json:"title,omitempty"`
	Description string                `json:"description,omitempty"`
	Confirm     string                `json:"confirm,omitempty"`
	Choices     map[string]string     `json:"choices,omitempty"`
	Properties  map[string]*FieldText `json:"properties,omitempty"`
}

// SecretText is a secret's title and description.
type SecretText struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// Translations are a manifest's translations by language; none (nil) shows the manifest's own text.
type Translations map[i18n.Lang]*Translation

// ParseTranslation reads one language's translation and checks it: a name within the manifest's 1 to 40 characters,
// every other string within StringMost. Keys it does not know are ignored (a later hub may read more).
func ParseTranslation(raw []byte) (*Translation, error) {
	if len(raw) > l10nFileMost {
		return nil, fmt.Errorf("larger than %d KiB", l10nFileMost>>10)
	}
	var t Translation
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	if t.Name != "" {
		if n := utf8.RuneCountInString(strings.TrimSpace(t.Name)); n == 0 || n > 40 {
			return nil, errors.New("name: 1 to 40 characters")
		}
	}
	if err := checkText("description", t.Description); err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(t.Configuration) {
		if err := t.Configuration[name].check("configuration." + name); err != nil {
			return nil, err
		}
	}
	for _, id := range sortedKeys(t.UI) {
		if n := utf8.RuneCountInString(strings.TrimSpace(t.UI[id])); n > 40 {
			return nil, fmt.Errorf("ui.%s: at most 40 characters", id)
		}
	}
	for _, id := range sortedKeys(t.Groups) {
		if n := utf8.RuneCountInString(strings.TrimSpace(t.Groups[id])); n > 40 {
			return nil, fmt.Errorf("groups.%s: at most 40 characters", id)
		}
	}
	for _, name := range sortedKeys(t.Secrets) {
		if s := t.Secrets[name]; s != nil {
			if err := errors.Join(checkText("secrets."+name+".title", s.Title), checkText("secrets."+name+".description", s.Description)); err != nil {
				return nil, err
			}
		}
	}
	return &t, nil
}

func (f *FieldText) check(path string) error {
	if f == nil {
		return nil
	}
	if err := errors.Join(checkText(path+".title", f.Title), checkText(path+".description", f.Description), checkText(path+".confirm", f.Confirm)); err != nil {
		return err
	}
	for _, v := range sortedKeys(f.Choices) {
		if err := checkText(path+".choices."+v, f.Choices[v]); err != nil {
			return err
		}
	}
	for _, name := range sortedKeys(f.Properties) {
		if err := f.Properties[name].check(path + ".properties." + name); err != nil {
			return err
		}
	}
	return nil
}

func checkText(path, s string) error {
	if utf8.RuneCountInString(s) > StringMost {
		return fmt.Errorf("%s: more than %d characters", path, StringMost)
	}
	return nil
}

// ReadTranslations reads the package in dir's translations, l10n/<language>.json for each language this hub has.
// A file that cannot be read or does not check out is left out, with a warning (English, as Lint's are). Files of
// other languages are ignored: a later hub may have them.
func ReadTranslations(dir string) (Translations, []string) {
	if dir == "" {
		return nil, nil
	}
	var ts Translations
	var warnings []string
	for _, l := range i18n.Langs {
		name := L10nDir + "/" + string(l) + ".json"
		raw, err := readL10nFile(filepath.Join(dir, filepath.FromSlash(name)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var t *Translation
		if err == nil {
			t, err = ParseTranslation(raw)
		}
		if err != nil {
			warnings = append(warnings, i18n.T(i18n.En, "extension-status.translation_skipped", i18n.Args{"file": name, "err": err.Error()}))
			continue
		}
		if ts == nil {
			ts = Translations{}
		}
		ts[l] = t
	}
	return ts, warnings
}

// readL10nFile reads a regular file of at most l10nFileMost bytes; a link is refused, so a package cannot show the
// text of a file outside it.
func readL10nFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, l10nFileMost+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > l10nFileMost {
		return nil, fmt.Errorf("larger than %d KiB", l10nFileMost>>10)
	}
	return raw, nil
}

// ParseEntryTranslations reads an index entry's l10n: {"<language>": <translation>, …}. Languages this hub does not
// have are ignored; any translation that does not check out makes the whole l10n an error (the index then shows the
// manifest's text, with a warning).
func ParseEntryTranslations(raw json.RawMessage) (Translations, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if len(raw) > len(i18n.Langs)*l10nFileMost {
		return nil, fmt.Errorf("larger than %d KiB", len(i18n.Langs)*l10nFileMost>>10)
	}
	var by map[string]json.RawMessage
	if err := json.Unmarshal(raw, &by); err != nil {
		return nil, err
	}
	var ts Translations
	for _, k := range sortedKeys(by) {
		l := i18n.Lang(k)
		if !slices.Contains(i18n.Langs, l) {
			continue
		}
		t, err := ParseTranslation(by[k])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		if ts == nil {
			ts = Translations{}
		}
		ts[l] = t
	}
	return ts, nil
}

// chain is what is tried for l, in order: l's translation, then English unless l is Simplified Chinese (whose
// readers are better served by the manifest's own text, most often Chinese); after them comes the manifest's text.
func (ts Translations) chain(l i18n.Lang) []*Translation {
	var out []*Translation
	if t := ts[l]; t != nil {
		out = append(out, t)
	}
	if l != i18n.En && l != i18n.ZhHans {
		if t := ts[i18n.En]; t != nil {
			out = append(out, t)
		}
	}
	return out
}

// firstText is the first of texts that is not blank, else own.
func firstText(own string, texts ...string) string {
	for _, s := range texts {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return own
}

// Name is the manifest's name (own) in l.
func (ts Translations) Name(l i18n.Lang, own string) string {
	var texts []string
	for _, t := range ts.chain(l) {
		texts = append(texts, t.Name)
	}
	return firstText(own, texts...)
}

// UITitle is the title of ui contribution id (own) in l.
func (ts Translations) UITitle(l i18n.Lang, id, own string) string {
	var texts []string
	for _, t := range ts.chain(l) {
		texts = append(texts, t.UI[id])
	}
	return firstText(own, texts...)
}

// GroupTitle is the title of sidebar group id (own) in l.
func (ts Translations) GroupTitle(l i18n.Lang, id, own string) string {
	var texts []string
	for _, t := range ts.chain(l) {
		texts = append(texts, t.Groups[id])
	}
	return firstText(own, texts...)
}

// Description is the manifest's description (own) in l.
func (ts Translations) Description(l i18n.Lang, own string) string {
	var texts []string
	for _, t := range ts.chain(l) {
		texts = append(texts, t.Description)
	}
	return firstText(own, texts...)
}

// Secret is s with its title and description in l.
func (ts Translations) Secret(l i18n.Lang, s SecretSpec) SecretSpec {
	var titles, descriptions []string
	for _, t := range ts.chain(l) {
		if st := t.Secrets[s.Name]; st != nil {
			titles, descriptions = append(titles, st.Title), append(descriptions, st.Description)
		}
	}
	s.Title, s.Description = firstText(s.Title, titles...), firstText(s.Description, descriptions...)
	return s
}

// Schema is a copy of s with its text in l: titles, descriptions, choices' labels, and the confirmations it has (a
// translation never adds one). s itself is shared and stays as it is; with nothing to translate, s is returned.
func (ts Translations) Schema(l i18n.Lang, s *Schema) *Schema {
	chain := ts.chain(l)
	if s == nil || len(chain) == 0 {
		return s
	}
	levels := make([]map[string]*FieldText, 0, len(chain))
	for _, t := range chain {
		levels = append(levels, t.Configuration)
	}
	out := *s
	out.Fields = localizeFields(s.Fields, levels)
	for i := range out.Fields {
		if out.Fields[i].Secret != nil {
			shown := ts.Secret(l, *out.Fields[i].Secret)
			out.Fields[i].Secret = &shown
		}
	}
	return &out
}

func localizeFields(fields []Field, levels []map[string]*FieldText) []Field {
	if len(fields) == 0 {
		return fields
	}
	out := slices.Clone(fields)
	for i := range out {
		f := &out[i]
		var texts []*FieldText
		for _, level := range levels {
			if t := level[f.Name]; t != nil {
				texts = append(texts, t)
			}
		}
		if len(texts) == 0 {
			continue
		}
		var titles, descriptions, confirms []string
		properties := make([]map[string]*FieldText, 0, len(texts))
		for _, t := range texts {
			titles, descriptions, confirms = append(titles, t.Title), append(descriptions, t.Description), append(confirms, t.Confirm)
			properties = append(properties, t.Properties)
		}
		f.Title, f.Description = firstText(f.Title, titles...), firstText(f.Description, descriptions...)
		if f.Confirm != "" {
			f.Confirm = firstText(f.Confirm, confirms...)
		}
		f.Enum = localizeChoices(f.Enum, texts)
		if f.Items != nil {
			items := *f.Items
			items.Enum = localizeChoices(items.Enum, texts)
			items.Fields = localizeFields(items.Fields, properties)
			f.Items = &items
		}
		f.Fields = localizeFields(f.Fields, properties)
	}
	return out
}

func localizeChoices(choices []Choice, texts []*FieldText) []Choice {
	if len(choices) == 0 {
		return choices
	}
	out := slices.Clone(choices)
	for i := range out {
		key := choiceKey(out[i].Value)
		var labels []string
		for _, t := range texts {
			labels = append(labels, t.Choices[key])
		}
		out[i].Title = firstText(out[i].Title, labels...)
	}
	return out
}

// choiceKey is how a translation names a choice: a string by itself, any other value as JSON writes it.
func choiceKey(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(v))
}
