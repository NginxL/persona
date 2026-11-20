// Package i18n is the hub's text for people in the user's language (docs/protocol/i18n.md): pushes, task notifications,
// injected messages, status and error text. Logs, CLI output and prompts for models are not translated here.
//
// Catalogs are locales/<lang>/<namespace>.json, flat "name": "text", embedded in the binary; a key is
// "<namespace>.<name>". Placeholders are {name}; a numeric "count" picks <key>_one / <key>_other when those exist.
// A key missing in a language falls back to en, then zh-Hans, then the key itself (the catalog test keeps them equal).
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
)

// Lang is an interface language: a directory under locales (docs/protocol/i18n.md "Adding a language").
type Lang string

// The languages the code names: the source, the fallback, and Japanese (some text treats CJK differently). Any other
// language is just its directory; nothing here lists them.
const (
	ZhHans Lang = "zh-Hans"
	En     Lang = "en"
	Ja     Lang = "ja"
)

// Default is the language when no client has said one: the hub's text was Chinese before i18n.
const Default = ZhHans

// Fallback is the language for a tag no catalog matches, and the first one a missing key is looked up in.
const Fallback = En

// meta is a language's locales/<lang>/_meta.json.
type meta struct {
	// Name is the language by its own name.
	Name string `json:"name"`
	// Matches are the BCP 47 language subtags (lowercase) that resolve to it.
	Matches []string `json:"matches"`
}

//go:embed all:locales
var files embed.FS

// Langs lists the supported languages: the source first, then the fallback, then the rest by code.
var Langs, metas, matches = languages()

func languages() ([]Lang, map[Lang]meta, map[string]Lang) {
	entries, err := fs.ReadDir(files, "locales")
	if err != nil {
		panic(err)
	}
	var langs []Lang
	metas := map[Lang]meta{}
	matches := map[string]Lang{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		l := Lang(e.Name())
		raw, err := files.ReadFile(path.Join("locales", e.Name(), "_meta.json"))
		if err != nil {
			panic(fmt.Sprintf("i18n: %s has no _meta.json", l))
		}
		var m meta
		if err := json.Unmarshal(raw, &m); err != nil {
			panic(fmt.Sprintf("i18n: %s/_meta.json: %v", l, err))
		}
		langs = append(langs, l)
		metas[l] = m
		for _, sub := range m.Matches {
			matches[strings.ToLower(sub)] = l
		}
	}
	rank := func(l Lang) int {
		switch l {
		case Default:
			return 0
		case Fallback:
			return 1
		}
		return 2
	}
	slices.SortFunc(langs, func(a, b Lang) int {
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		return strings.Compare(string(a), string(b))
	})
	return langs, metas, matches
}

// Name is the language by its own name (what a prompt asks the model to answer in); an unknown one is the default's.
func (l Lang) Name() string {
	if m, ok := metas[l]; ok {
		return m.Name
	}
	return metas[Default].Name
}

// Resolve maps any BCP 47 tag to a supported language (docs/protocol/i18n.md): its language subtag picks the language whose
// _meta.json matches it (any Chinese, Traditional too, is zh-Hans), the rest is en. An empty tag is "" (unknown), so
// callers can tell "not said" from "said English".
func Resolve(tag string) Lang {
	if l := Match(tag); l != "" || strings.TrimSpace(tag) == "" {
		return l
	}
	return Fallback
}

// Match is the supported language that tag's language subtag picks, as for Resolve, or "" when no _meta.json matches it
// (ko-KR, fr) or tag is empty: for a caller with a better fallback than en.
func Match(tag string) Lang {
	primary, _, _ := strings.Cut(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"), "-")
	return matches[strings.ToLower(primary)]
}

// Valid: one of the supported codes exactly (what a client sends).
func Valid(l Lang) bool {
	_, ok := metas[l]
	return ok
}

// FromAcceptLanguage resolves an Accept-Language header: its first entry (an app sends just "en"; a browser its whole
// list, most preferred first). "" when there is none or it is "*".
func FromAcceptLanguage(h string) Lang {
	first, _, _ := strings.Cut(h, ",")
	first, _, _ = strings.Cut(first, ";")
	first = strings.TrimSpace(first)
	if first == "" || first == "*" {
		return ""
	}
	return Resolve(first)
}

// FromRequest is the request's Accept-Language, resolved ("" when absent).
func FromRequest(r *http.Request) Lang {
	return FromAcceptLanguage(r.Header.Get("Accept-Language"))
}

type ctxKey struct{}

// With carries a language in a context (the request's, set by the hub's HTTP middleware).
func With(ctx context.Context, l Lang) context.Context {
	if l == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, l)
}

// From is the context's language, "" when none was set.
func From(ctx context.Context) Lang {
	l, _ := ctx.Value(ctxKey{}).(Lang)
	return l
}

// Or is l, or fallback when l is unknown.
func (l Lang) Or(fallback Lang) Lang {
	if Valid(l) {
		return l
	}
	return fallback
}

// Catalogs are read on first use, one language at a time: a short-lived process (a hook) that never needs a text does
// not pay for parsing them (#1308: the largest part of every `personad hook`'s start), and one that only ever speaks
// en (with zh-Hans for a missing key and Has) does not parse or keep the others.
var catalogOf = func() map[Lang]*langCatalog {
	out := make(map[Lang]*langCatalog, len(Langs))
	for _, l := range Langs {
		c := &langCatalog{}
		c.get = sync.OnceValue(func() map[string]string {
			cat := loadLang(l)
			c.parsed.Store(true)
			return cat
		})
		out[l] = c
	}
	return out
}()

// langCatalog is one language's catalog, parsed by its first get.
type langCatalog struct {
	get    func() map[string]string
	parsed atomic.Bool // get has parsed it (readCatalog)
}

// catalog is l's catalog, parsed now if nothing has read it yet; nil for a language there is no catalog for.
func catalog(l Lang) map[string]string {
	if c := catalogOf[l]; c != nil {
		return c.get()
	}
	return nil
}

// readCatalog is l's catalog for a read that keeps none of it (the reverse templates): the kept one when it is
// parsed already, else a fresh parse the caller drops, so building templates leaves every other language unparsed.
func readCatalog(l Lang) map[string]string {
	if c := catalogOf[l]; c != nil && c.parsed.Load() {
		return c.get()
	}
	return loadLang(l)
}

func loadLang(l Lang) map[string]string {
	out := map[string]string{}
	entries, err := fs.ReadDir(files, "locales/"+string(l))
	if err != nil {
		panic(fmt.Sprintf("i18n: %s: %v", l, err))
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), "_") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		raw, err := files.ReadFile(path.Join("locales", string(l), e.Name()))
		if err != nil {
			panic(err)
		}
		var m map[string]string
		if err := json.Unmarshal(raw, &m); err != nil {
			panic(fmt.Sprintf("i18n: %s/%s: %v", l, e.Name(), err))
		}
		ns := strings.TrimSuffix(e.Name(), ".json")
		for k, v := range m {
			out[ns+"."+k] = v
		}
	}
	return out
}

// Args are a text's placeholder values.
type Args map[string]any

// Verbatim is a source value whose punctuation must survive insertion, such as a quoted name or message.
type Verbatim string

func lookup(l Lang, key string, args Args) (string, bool) {
	cat := catalog(l)
	if n, ok := count(args); ok {
		if s, ok := cat[key+"_"+pluralForm(l, n)]; ok {
			return s, true
		}
		if s, ok := cat[key+"_other"]; ok {
			return s, true
		}
	}
	s, ok := cat[key]
	return s, ok
}

// pluralForm is n's CLDR cardinal category in l (one, other…), the suffix of a plural key.
func pluralForm(l Lang, n int64) string {
	if n < 0 {
		n = -n
	}
	switch plural.Cardinal.MatchPlural(language.Make(string(l)), int(n), 0, 0, 0, 0) {
	case plural.Zero:
		return "zero"
	case plural.One:
		return "one"
	case plural.Two:
		return "two"
	case plural.Few:
		return "few"
	case plural.Many:
		return "many"
	}
	return "other"
}

func count(args Args) (int64, bool) {
	switch v := args["count"].(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case float64:
		return int64(v), v == float64(int64(v))
	}
	return 0, false
}

// T is key's text in l (an unknown l is the default), placeholders filled from args.
func T(l Lang, key string, args ...Args) string {
	var a Args
	if len(args) > 0 {
		a = args[0]
	}
	l = l.Or(Default)
	s, ok := lookup(l, key, a)
	if !ok {
		if s, ok = lookup(Fallback, key, a); !ok {
			if s, ok = lookup(ZhHans, key, a); !ok {
				return key
			}
		}
	}
	if a == nil {
		return s
	}
	return fill(s, a)
}

// fill replaces each {name} in s (name an ASCII word, [0-9A-Za-z_]+) with a[name]; an unknown name or a brace
// that does not close such a word stays as written. It is the byte-scan form of a `\{(\w+)\}` regexp replace,
// which T runs on every call with args. A value that ends a sentence, filled in right before the text's own
// punctuation ("（{reason}），", "{error}。"), loses its full stop, so the text never reads "。），" or "。。" (#6822);
// the web's interpolate does the same (web/src/i18n/index.ts).
func fill(s string, a Args) string {
	i := strings.IndexByte(s, '{')
	if i < 0 {
		return s
	}
	var b strings.Builder
	done := 0 // s[:done] is already in b
	for i >= 0 {
		j := i + 1
		for j < len(s) && isWordByte(s[j]) {
			j++
		}
		if j > i+1 && j < len(s) && s[j] == '}' {
			if v, ok := a[s[i+1:j]]; ok {
				val := argText(v)
				if _, source := v.(Verbatim); !source && markAfter(s[j+1:]) {
					val = withoutFullStop(val)
				}
				if done == 0 {
					b.Grow(len(s) + len(val)) // one allocation for the common one-placeholder text
				}
				b.WriteString(s[done:i])
				b.WriteString(val)
				done = j + 1
			}
			j++
		}
		// Word bytes are never '{', so the next placeholder starts at or after j.
		k := strings.IndexByte(s[j:], '{')
		if k < 0 {
			break
		}
		i = j + k
	}
	if done == 0 {
		return s
	}
	b.WriteString(s[done:])
	return b.String()
}

// markAfter: rest (the text after a placeholder) starts with punctuation that makes a value's own full stop a second one.
func markAfter(rest string) bool {
	r, _ := utf8.DecodeRuneInString(rest)
	return strings.ContainsRune("）)，,。．.：:；;、", r)
}

// withoutFullStop is v without the full stop it ends with (and space after it); an ellipsis ("...", "。。") keeps its
// dots, and a value that ends otherwise is v.
func withoutFullStop(v string) string {
	t := strings.TrimRightFunc(v, unicode.IsSpace)
	last, n := utf8.DecodeLastRuneInString(t)
	if n == 0 || !isFullStop(last) {
		return v
	}
	if prev, _ := utf8.DecodeLastRuneInString(t[:len(t)-n]); isFullStop(prev) {
		return v
	}
	return t[:len(t)-n]
}

func isFullStop(r rune) bool { return r == '。' || r == '．' || r == '.' }

func isWordByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_'
}

func argText(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case Verbatim:
		return string(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return fmt.Sprint(v)
}

// Has: key is in the catalogs (any plural form counts).
func Has(key string) bool {
	cat := catalog(ZhHans)
	_, a := cat[key]
	_, b := cat[key+"_other"]
	return a || b
}

// Message is an error whose text is a catalog entry: written to a client (apierr) it reads in that client's
// language; anywhere else (logs, older paths) Error() is the default language's text, what it said before i18n.
type Message struct {
	Key            string
	Args           Args
	legacyLanguage Lang
}

// Err is an error with key's text.
func Err(key string, args ...Args) error {
	m := &Message{Key: key}
	if len(args) > 0 {
		m.Args = args[0]
	}
	return m
}

// ErrEnglish keeps an English-origin error's no-locale/log text while explicit client locales translate it.
func ErrEnglish(key string, args ...Args) error {
	m := Err(key, args...).(*Message)
	m.legacyLanguage = En
	return m
}

func (m *Message) Error() string { return T(m.legacyLanguage.Or(Default), m.Key, m.Args) }

// Text is the message in l.
func (m *Message) Text(l Lang) string {
	args := Args{}
	for key, value := range m.Args {
		if err, ok := value.(error); ok {
			args[key] = Localize(err, l)
		} else {
			args[key] = value
		}
	}
	return T(l, m.Key, args)
}

// Localize is err's text in l: a Message (not wrapped in more text) reads in l, any other error as it is.
func Localize(err error, l Lang) string {
	// Direct typed errors may localize their own structured fields. Do not discard an arbitrary wrapper's text.
	if text, ok := err.(interface{ Text(Lang) string }); ok && l != "" {
		return text.Text(l)
	}
	var m *Message
	if l != "" && errors.As(err, &m) && err.Error() == m.Error() {
		return m.Text(l)
	}
	if l != "" {
		return InboxText(l, err.Error())
	}
	return err.Error()
}

// Carrier is a ResponseWriter that knows its request's language (the hub's request logger sets it).
type Carrier interface{ Lang() Lang }

// OfWriter is the language of the request w answers: the first writer in its Unwrap chain that carries one.
func OfWriter(w http.ResponseWriter) Lang {
	for range 16 {
		if c, ok := w.(Carrier); ok {
			return c.Lang()
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return ""
		}
		w = u.Unwrap()
	}
	return ""
}
