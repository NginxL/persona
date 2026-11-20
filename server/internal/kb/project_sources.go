package kb

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/kbwire"
)

const referenceOnly = "reference-only"

// ErrReferenceOnly reports that a project only accepts pointers and routing metadata.
var ErrReferenceOnly = errors.New("project is reference-only")

// ErrInvalidProjectSource reports an invalid or unauthorized project source setting.
var ErrInvalidProjectSource = errors.New("invalid project source setting")

func (p *Project) referenceOnly() bool { return p.Mode == referenceOnly }

// checkProjectDetailWrite runs under metaMu, so the write and a policy change cannot pass each other.
func (s *Store) checkProjectDetailWrite(slug, field string) error {
	var p struct {
		Mode string `json:"project_mode"`
	}
	if _, err := readJSON(s.entityPath(KindProject, slug), &p); err != nil {
		return err
	}
	if p.Mode == referenceOnly {
		return fmt.Errorf("%w: cannot save %s", ErrReferenceOnly, field)
	}
	return nil
}

// canonicalSourceURL accepts HTTP(S) pointers. GitHub issue and pull-request URLs
// share the same repository number namespace, including links to comments.
func canonicalSourceURL(raw string) (id, link string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ""
	}
	u.Host = strings.ToLower(u.Host)
	path := strings.Split(strings.Trim(u.Path, "/"), "/")
	if (u.Host == "github.com" || u.Host == "www.github.com") && len(path) == 4 &&
		path[0] != "" && path[1] != "" && (path[2] == "issues" || path[2] == "pull") {
		n, err := strconv.ParseUint(path[3], 10, 64)
		if err == nil && n > 0 {
			repo := strings.ToLower(path[0] + "/" + path[1])
			return fmt.Sprintf("github:%s#%d", repo, n), fmt.Sprintf("https://github.com/%s/%s/%d", repo, path[2], n)
		}
	}
	link = u.String()
	u.Fragment = ""
	u.RawFragment = ""
	return u.String(), link
}

// projectSourceUpdate validates a policy change before any link, file or lock can be written.
// The caller holds applyMu, as do other user and extension updates.
func (e *Engine) projectSourceUpdate(id string, u *UserUpdate) error {
	if u.ProjectMode != nil || u.SourceURLs != nil {
		if u.MergeInto != "" {
			return fmt.Errorf("%w: set project_mode or source_urls separately from merge_into", ErrInvalidProjectSource)
		}
	}
	if u.ProjectMode != nil && *u.ProjectMode != referenceOnly && *u.ProjectMode != "full" {
		return fmt.Errorf("%w: project_mode must be full or reference-only", ErrInvalidProjectSource)
	}
	resolved := e.resolve(KindProject, id)
	collision := false
	if resolved == "" && u.Name != "" {
		resolved = Slug(u.Name)
		collision = true
	}
	p, _ := e.Store.Project(resolved)
	if p != nil && p.referenceOnly() || u.ProjectMode != nil && *u.ProjectMode == referenceOnly {
		var blocked []string
		for _, field := range setFields(u.Update) {
			switch field {
			case "goal", "log", "merge_into", "my_role", "note", "pinned", "summary":
				blocked = append(blocked, field)
			}
		}
		if len(blocked) > 0 {
			return fmt.Errorf("%w: cannot save %s", ErrReferenceOnly, strings.Join(blocked, ", "))
		}
	}
	if collision && p != nil {
		return fmt.Errorf("%w: a project with this name already exists; use its id", ErrInvalidProjectSource)
	}
	if u.SourceURLs != nil {
		if len(u.SourceURLs) > 30 {
			return fmt.Errorf("%w: source_urls accepts at most 30 URLs", ErrInvalidProjectSource)
		}
		links := make([]string, 0, len(u.SourceURLs))
		for _, raw := range u.SourceURLs {
			key, _ := canonicalSourceURL(raw)
			if key == "" || len(raw) > 2048 {
				return fmt.Errorf("%w: source_urls requires HTTP(S) URLs without credentials, up to 2048 bytes each", ErrInvalidProjectSource)
			}
			links = addUnique(links, strings.TrimSpace(raw))
		}
		u.SourceURLs = links
	}
	return nil
}

// sourceCatalog resolves a canonical URL from current archive facts, shared across a list.
func (e *Engine) sourceCatalog() map[string]kbwire.ProjectSource {
	out := map[string]kbwire.ProjectSource{}
	for _, c := range e.Store.Chats() {
		id, link := canonicalSourceURL(c.SourceURL)
		if id == "" {
			continue
		}
		cur := chatProjectSource(c, id, link)
		previous, exists := out[id]
		if !exists || previous.DeliveryState == "" && cur.DeliveryState != "" ||
			cur.DeliveryAt != nil && (previous.DeliveryAt == nil || cur.DeliveryAt.After(*previous.DeliveryAt)) {
			out[id] = cur
		}
	}
	return out
}

func (e *Engine) projectSourceLookup() func() map[string]kbwire.ProjectSource {
	return sync.OnceValue(e.sourceCatalog)
}

func chatProjectSource(c *Chat, id, link string) kbwire.ProjectSource {
	out := kbwire.ProjectSource{ID: id, URL: link}
	if c.State != "" || c.Kind == "pr" || c.Kind == "issue" {
		out.DeliveryState = c.State
		out.EnvironmentVerification = "unverified"
		if !c.StateAt.IsZero() {
			out.DeliveryAt = &c.StateAt
		}
	}
	return out
}

func (e *Engine) projectSources(p *Project, catalog func() map[string]kbwire.ProjectSource) []kbwire.ProjectSource {
	var out []kbwire.ProjectSource
	at := map[string]int{}
	add := func(source kbwire.ProjectSource, ref string) {
		if i, ok := at[source.ID]; ok {
			if ref != "" {
				out[i].Refs = addUnique(out[i].Refs, ref)
			}
			return
		}
		if source.URL != "" {
			if observed, ok := catalog()[source.ID]; ok {
				source = observed
			}
		}
		if ref != "" {
			source.Refs = []string{ref}
		}
		at[source.ID] = len(out)
		out = append(out, source)
	}
	for _, link := range p.SourceURLs {
		if id, link := canonicalSourceURL(link); id != "" {
			add(kbwire.ProjectSource{ID: id, URL: link}, "")
		}
	}
	for _, cid := range p.Chats {
		if c, ok := e.Store.Chat(cid); ok {
			ref := KindChat + "/" + c.ID
			id, link := canonicalSourceURL(c.SourceURL)
			if id == "" {
				id = ref
			}
			add(chatProjectSource(c, id, link), ref)
		}
	}
	for _, ref := range p.Items {
		if kind, id, ok := strings.Cut(ref, "/"); ok && slices.Contains([]string{KindDoc, KindMeeting, KindEvent}, kind) {
			if item, ok := e.Store.Item(kind, id); ok {
				if key, link := canonicalSourceURL(item.URL); key != "" {
					add(kbwire.ProjectSource{ID: key, URL: link}, ref)
					continue
				}
			}
		}
		add(kbwire.ProjectSource{ID: ref}, ref)
	}
	for _, moment := range p.Moments {
		ref := "moments/posts/" + moment.ID
		add(kbwire.ProjectSource{ID: ref}, ref)
	}
	return out
}

// shareProjectSource checks whether two views point at the same external resource.
func (e *Engine) shareProjectSource(a, b *Project) bool {
	noLookup := func() map[string]kbwire.ProjectSource { return nil }
	ids := map[string]bool{}
	for _, source := range e.projectSources(a, noLookup) {
		if source.URL != "" {
			ids[source.ID] = true
		}
	}
	return slices.ContainsFunc(e.projectSources(b, noLookup), func(source kbwire.ProjectSource) bool {
		return ids[source.ID]
	})
}
