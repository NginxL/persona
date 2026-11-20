package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const (
	AssistantViewMost  = 64 << 10
	AssistantPagesMost = 8
)

// assistantPageName is compiled on first use: 64 copies of two Unicode classes take about 1 ms and 2.5 MB, paid by
// every program that links this package when it starts (a hook, #1308).
var assistantPageName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[\p{L}\p{N}_-]{1,64}$`) })

func ParseAssistantView(raw json.RawMessage) (*View, error) {
	if len(raw) > AssistantViewMost {
		return nil, fmt.Errorf("view: at most %d bytes", AssistantViewMost)
	}
	if err := checkAssistantJSONDepth(raw); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("view: %w", err)
	}
	if fields == nil {
		return nil, errors.New("view: an object with sections and fallback is required")
	}
	if _, exists := fields["fallback"]; !exists {
		return nil, errors.New("fallback: include a plain text summary under the exact fallback field name")
	}
	if _, component := fields["type"]; component {
		fallback := fields["fallback"]
		delete(fields, "fallback")
		item, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		if err := ValidateJSON("Component", item); err != nil {
			return nil, err
		}
		if len(fallback) == 0 {
			return nil, errors.New("fallback: include a plain text summary of the card")
		}
		raw = append([]byte(`{"fallback":`), fallback...)
		raw = append(raw, []byte(`,"sections":[{"items":[`)...)
		raw = append(raw, item...)
		raw = append(raw, []byte(`]}]}`)...)
	}
	var v View
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if len(v.Pages) > AssistantPagesMost {
		return nil, fmt.Errorf("pages: at most %d named pages", AssistantPagesMost)
	}
	for name, page := range v.Pages {
		if len(page.Pages) > 0 {
			return nil, fmt.Errorf("pages.%s: detail pages cannot nest pages", name)
		}
	}
	if err := ValidateJSON("View", raw); err != nil {
		return nil, err
	}
	return &v, CheckAssistantView(&v)
}

func checkAssistantJSONDepth(raw []byte) error {
	depth := 0
	quoted, escaped := false, false
	for _, b := range raw {
		if quoted {
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				quoted = false
			}
			continue
		}
		switch b {
		case '"':
			quoted = true
		case '{', '[':
			if depth++; depth > 32 {
				return errors.New("view: JSON nesting exceeds 32 levels")
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

func CheckAssistantView(v *View) error {
	if v == nil {
		return errors.New("view: an object with sections and fallback is required")
	}
	if strings.TrimSpace(v.Fallback) == "" {
		return errors.New("fallback: include a plain text summary of the card")
	}
	if len(v.Pages) > AssistantPagesMost {
		return fmt.Errorf("pages: at most %d named pages", AssistantPagesMost)
	}
	for name, page := range v.Pages {
		if !assistantPageName().MatchString(name) {
			return fmt.Errorf("pages.%s: use 1–64 letters, numbers, underscores or hyphens", name)
		}
		if len(page.Pages) > 0 {
			return fmt.Errorf("pages.%s: detail pages refer to the root pages; do not nest pages", name)
		}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("view: %w", err)
	}
	if len(raw) > AssistantViewMost {
		return fmt.Errorf("view: at most %d bytes", AssistantViewMost)
	}
	ids := map[string]bool{}
	checkPage := func(page *View, where string) error {
		if err := CheckView(page); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		for i, s := range page.Sections {
			for j, e := range s.Items {
				if e.Type == "field" && e.Kind == "secret" {
					return fmt.Errorf("%s.sections[%d].items[%d]: assistant cards must not collect secrets", where, i, j)
				}
				// UI version 6: no extension serves an assistant card's pictures, and a control acts at once, which a
				// card's actions (a reply the user confirms) do not.
				if HasImageRef(&e) {
					return fmt.Errorf("%s.sections[%d].items[%d]: assistant cards show pictures by https url, not ref", where, i, j)
				}
				if e.Control != nil || slices.ContainsFunc(e.Tiles, func(t Tile) bool { return t.Control != nil }) {
					return fmt.Errorf("%s.sections[%d].items[%d]: assistant cards have no controls; use a button", where, i, j)
				}
			}
		}
		for _, a := range Actions(page) {
			if ids[a.ID] {
				return fmt.Errorf("%s: action %q is repeated; use a unique id across the card's pages", where, a.ID)
			}
			ids[a.ID] = true
			if err := checkAssistantAction(a, v.Pages); err != nil {
				return fmt.Errorf("%s.action.%s: %w", where, a.ID, err)
			}
		}
		return nil
	}
	if err := checkPage(v, "view"); err != nil {
		return err
	}
	for _, name := range sortedPageNames(v.Pages) {
		page := v.Pages[name]
		if err := checkPage(&page, "pages."+name); err != nil {
			return err
		}
	}
	return nil
}

func sortedPageNames(pages map[string]View) []string {
	names := make([]string, 0, len(pages))
	for name := range pages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func checkAssistantAction(a *Action, pages map[string]View) error {
	n := 0
	for _, yes := range []bool{a.Reply != "", a.Open != "", a.Push != nil, a.Pop} {
		if yes {
			n++
		}
	}
	if n != 1 {
		return errors.New("choose exactly one of reply, open, push or pop")
	}
	if a.Submit && a.Reply == "" {
		return errors.New("submit requires reply")
	}
	if a.Reply != "" && strings.TrimSpace(a.Reply) == "" {
		return errors.New("reply must contain text")
	}
	if a.Open != "" {
		return CheckAssistantOpen(a.Open)
	}
	if p := a.Push; p != nil {
		if p.Page == "" || p.UI != "" || p.State != "" {
			return errors.New("push requires page, without extension ui or state")
		}
		if _, exists := pages[p.Page]; !exists {
			return fmt.Errorf("push.page %q is missing from the root pages", p.Page)
		}
	}
	return nil
}

func CheckAssistantOpen(target string) error {
	if strings.IndexFunc(target, unicode.IsControl) >= 0 || strings.Contains(target, "\\") {
		return errors.New("open: no control characters or backslashes")
	}
	u, err := url.Parse(target)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return errors.New("open: a URL without credentials is required")
	}
	if u.Scheme == "https" {
		return nil
	}
	if !assistantURLScheme(u.Scheme) || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("open: use https or a persona session, task, kb or file link")
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	decoded := make([]string, len(parts))
	for i, part := range parts {
		value, err := url.PathUnescape(part)
		if err != nil || value == "" || strings.IndexFunc(value, unicode.IsControl) >= 0 || strings.Contains(value, "\\") {
			return errors.New("open: invalid path segment")
		}
		if u.Host != "file" && strings.Contains(value, "/") {
			return errors.New("open: do not escape multiple path segments into one")
		}
		decoded[i] = value
	}
	switch u.Host {
	case "session":
		if len(parts) == 2 {
			return nil
		}
	case "task":
		if len(parts) == 1 {
			return nil
		}
	case "kb":
		for _, part := range decoded {
			if part == "." || part == ".." {
				return errors.New("open: knowledge paths cannot traverse directories")
			}
		}
		return nil
	case "file":
		if len(parts) == 2 && !strings.Contains(decoded[0], "/") && path.IsAbs(decoded[1]) {
			return nil
		}
	}
	return errors.New("open: the target has the wrong kind or number of path segments")
}

func AssistantOnlyAction(a *Action) bool {
	return a != nil && (a.Reply != "" || a.Open != "" || a.Pop || a.Push != nil && a.Push.Page != "")
}

func StripAssistantFields(v *View) []string {
	var dropped []string
	drop := func(name string) {
		for _, old := range dropped {
			if old == name {
				return
			}
		}
		dropped = append(dropped, name)
	}
	if v == nil {
		return dropped
	}
	if v.Fallback != "" {
		drop("fallback")
		v.Fallback = ""
	}
	if len(v.Pages) > 0 {
		drop("pages")
		v.Pages = nil
	}
	if v.Search != nil && AssistantOnlyAction(&v.Search.Action) {
		drop("assistant_action")
		v.Search = nil
	}
	if v.Filter != nil && AssistantOnlyAction(&v.Filter.Action) {
		drop("assistant_action")
		v.Filter = nil
	}
	if v.Tabs != nil && AssistantOnlyAction(&v.Tabs.Action) {
		drop("assistant_action")
		v.Tabs = nil
	}
	sections := make([]Section, 0, len(v.Sections))
	for i := range v.Sections {
		s := &v.Sections[i]
		hadItems := len(s.Items) > 0
		if s.More != nil && AssistantOnlyAction(&s.More.Action) {
			drop("assistant_action")
			s.More = nil
		}
		if s.Link != nil && AssistantOnlyAction(&s.Link.Action) {
			drop("assistant_action")
			s.Link = nil
		}
		items := make([]Component, 0, len(s.Items))
		for _, e := range s.Items {
			if e.Type == "html" {
				drop("html")
				continue
			}
			if AssistantOnlyAction(e.Action) {
				drop("assistant_action")
				if e.Type == "button" || e.Type == "segmented" {
					continue
				}
				e.Action = nil
			}
			if e.Button != nil && AssistantOnlyAction(&e.Button.Action) {
				drop("assistant_action")
				e.Button = nil
			}
			// a graph's nodes keep their place; only their assistant-only action goes
			for k := range e.Nodes {
				if AssistantOnlyAction(e.Nodes[k].Action) {
					drop("assistant_action")
					e.Nodes[k].Action = nil
				}
			}
			items = append(items, e)
		}
		s.Items = items
		if !hadItems || len(items) > 0 || s.Footer != "" || s.More != nil {
			sections = append(sections, *s)
		}
	}
	v.Sections = sections
	return dropped
}
