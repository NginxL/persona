package larkcli

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"

	"github.com/nwverse/persona/server/internal/i18n"
	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
)

const (
	documentPollInterval   = 2 * time.Minute
	documentRateBackoff    = 30 * time.Second
	documentTimeoutBackoff = 10 * time.Second
	// An unreadable document is probed again after this long: its owner may share it again, or the user may sign in
	// again with the missing permission. A new login (another account or other granted scopes) probes it at once.
	documentUnreadableRetry = 10 * time.Minute
	// owned_all_comments covers the user's own documents without listing them: the ones edited or opened in the last
	// ownedWindow, at most maxOwnedDocuments, re-listed every ownedRefresh, each read every ownedPollEvery.
	ownedWindow          = 30 * 24 * time.Hour
	maxOwnedDocuments    = 50
	maxOwnedPages        = 5
	ownedRefresh         = 30 * time.Minute
	ownedPollEvery       = 10 * time.Minute
	maxCommentPages      = 10
	maxReplyPages        = 10
	maxWikiSpaces        = 20
	maxWikiSpacePages    = 10
	maxWikiNodes         = 500
	maxWatchedDocuments  = 200
	maxDocumentURLLength = 4096
)

// DocumentOptions are the connector's watched-document choices. An empty
// value deliberately watches nothing. document_links is accepted as a
// backwards-compatible input alias; Options always returns documents.
type DocumentOptions struct {
	Documents      []string `json:"documents"`
	WikiSpaces     []string `json:"wiki_spaces"`
	DocumentLinks  []string `json:"-"` // source-compatible alias for the settings label
	WikiSpaceLinks []string `json:"-"`
	// OwnedAllComments keeps every new comment and reply on a watched document the user owns, not only those that
	// mention the user or answer a thread the user wrote in. Off by default.
	OwnedAllComments bool `json:"owned_all_comments"`
}

func (o DocumentOptions) normalized() DocumentOptions {
	documentValues := o.Documents
	if len(documentValues) == 0 {
		documentValues = o.DocumentLinks
	}
	spaceValues := o.WikiSpaces
	if len(spaceValues) == 0 {
		spaceValues = o.WikiSpaceLinks
	}
	documents := uniqueOptions(documentValues)
	spaces := uniqueOptions(spaceValues)
	if documents == nil {
		documents = []string{}
	}
	if spaces == nil {
		spaces = []string{}
	}
	return DocumentOptions{Documents: documents, WikiSpaces: spaces, DocumentLinks: documents, WikiSpaceLinks: spaces, OwnedAllComments: o.OwnedAllComments}
}

func uniqueOptions(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func parseDocumentOptions(raw json.RawMessage) (DocumentOptions, error) {
	var wire struct {
		Documents      json.RawMessage `json:"documents"`
		DocumentLinks  json.RawMessage `json:"document_links"`
		WikiSpaces     json.RawMessage `json:"wiki_spaces"`
		WikiSpaceLinks json.RawMessage `json:"wiki_space_links"`
		Owned          *bool           `json:"owned_all_comments"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return DocumentOptions{}, fmt.Errorf("options: %v", err)
	}
	documents, err := optionStrings(wire.Documents)
	if err != nil {
		return DocumentOptions{}, fmt.Errorf("options.documents: %v", err)
	}
	if len(documents) == 0 {
		documents, err = optionStrings(wire.DocumentLinks)
		if err != nil {
			return DocumentOptions{}, fmt.Errorf("options.document_links: %v", err)
		}
	}
	spaces, err := optionStrings(wire.WikiSpaces)
	if err != nil {
		return DocumentOptions{}, fmt.Errorf("options.wiki_spaces: %v", err)
	}
	if len(spaces) == 0 {
		spaces, err = optionStrings(wire.WikiSpaceLinks)
		if err != nil {
			return DocumentOptions{}, fmt.Errorf("options.wiki_space_links: %v", err)
		}
	}
	o := DocumentOptions{Documents: uniqueOptions(documents), WikiSpaces: uniqueOptions(spaces), OwnedAllComments: wire.Owned != nil && *wire.Owned}
	if len(o.Documents) > maxWatchedDocuments {
		return DocumentOptions{}, fmt.Errorf("options.documents has more than %d links", maxWatchedDocuments)
	}
	if len(o.WikiSpaces) > maxWikiSpaces {
		return DocumentOptions{}, fmt.Errorf("options.wiki_spaces has more than %d spaces", maxWikiSpaces)
	}
	for _, link := range append(append([]string{}, o.Documents...), o.WikiSpaces...) {
		if len(link) > maxDocumentURLLength {
			return DocumentOptions{}, errors.New("options link is too long")
		}
	}
	return o, nil
}

func optionStrings(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var stringsValue []string
	if err := json.Unmarshal(raw, &stringsValue); err == nil {
		return stringsValue, nil
	}
	var objects []struct {
		URL   string `json:"url"`
		Link  string `json:"link"`
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &objects); err != nil {
		return nil, errors.New("must be an array of links")
	}
	out := make([]string, 0, len(objects))
	for _, object := range objects {
		value := object.URL
		if value == "" {
			value = object.Link
		}
		if value == "" {
			value = object.ID
		}
		if value == "" {
			value = object.Token
		}
		out = append(out, value)
	}
	return out, nil
}

type watchedDocument struct {
	URL        string
	Canonical  string
	Title      string
	Type       string
	Cursor     string
	Seen       map[string]string
	Things     map[string]connectorwire.InboxThing
	Unreadable bool
	// Token is the underlying document's token (a wiki link unwrapped); Owner its owner's open_id, read once when
	// OwnedAllComments is on.
	Token string
	Owner string
	// Auto marks one of the user's own documents found by owned_all_comments (not in the watch list); NextPoll is
	// when it is read next.
	Auto     bool
	NextPoll time.Time
	// RetryAt is when an unreadable document is probed again.
	RetryAt time.Time
}

type documentInbox struct {
	mu        sync.Mutex
	options   DocumentOptions
	docs      map[string]*watchedDocument
	things    map[string]connectorwire.InboxThing
	warnings  map[string]string
	rateUntil time.Time
	// owned are the user's own documents owned_all_comments covers, listed at ownedAt.
	owned   map[string]watchedDocument
	ownedAt time.Time
	// login identifies the lark-cli login the unreadable marks were made under; a different login clears them.
	login string
	// missingScopes is set when lark-cli says the login itself lacks a scope for comments. That is the connector's
	// state, not any one document's, so no document is marked for it.
	missingScopes []string
}

func newDocumentInbox() *documentInbox {
	return &documentInbox{docs: map[string]*watchedDocument{}, things: map[string]connectorwire.InboxThing{}, warnings: map[string]string{}}
}

func (d *documentInbox) setOptions(o DocumentOptions) {
	o = o.normalized()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.options = o
	allowed := make(map[string]struct{}, len(o.Documents))
	for _, link := range o.Documents {
		allowed[link] = struct{}{}
	}
	for link := range d.docs {
		if _, ok := allowed[link]; !ok {
			delete(d.docs, link)
			delete(d.warnings, link)
		}
	}
	// A changed watch list starts a new source window. Existing Inbox rows are
	// retained by the hub, but this connector must not resubmit a removed link.
	d.things = map[string]connectorwire.InboxThing{}
	for _, doc := range d.docs {
		doc.Things = map[string]connectorwire.InboxThing{}
	}
}

func (d *documentInbox) warning(lang i18n.Lang) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.warnings) == 0 {
		return ""
	}
	keys := make([]string, 0, len(d.warnings))
	for key := range d.warnings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, i18n.T(lang, "hub-connectors.watched_doc_unreadable", i18n.Args{"title": d.warnings[key]}))
	}
	return strings.Join(parts, " · ")
}

func (d *documentInbox) markUnreadable(link, title string, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if title == "" {
		title = link
	}
	doc := d.docs[link]
	if doc == nil {
		doc = &watchedDocument{URL: link, Title: title, Seen: map[string]string{}, Things: map[string]connectorwire.InboxThing{}}
		d.docs[link] = doc
	}
	doc.Unreadable = true
	doc.RetryAt = now.Add(documentUnreadableRetry)
	d.warnings[link] = title
}

// skipUnreadable reports whether link is marked unreadable and not yet due for another probe. The caller holds d.mu.
func (d *documentInbox) skipUnreadable(doc *watchedDocument, now time.Time) bool {
	return doc != nil && doc.Unreadable && now.Before(doc.RetryAt)
}

// markReadable clears an unreadable mark after a probe succeeded.
func (d *documentInbox) markReadable(link string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if doc := d.docs[link]; doc != nil {
		doc.Unreadable, doc.RetryAt = false, time.Time{}
	}
	delete(d.warnings, link)
}

// setLogin records the current lark-cli login. When it differs from the one the marks were made under, every
// unreadable document is probed again and a missing-scope state is dropped until lark-cli reports it again.
func (d *documentInbox) setLogin(login string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.login == login {
		return
	}
	d.login = login
	d.missingScopes = nil
	for _, doc := range d.docs {
		doc.RetryAt = time.Time{}
	}
}

func (d *documentInbox) setMissingScopes(scopes []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.missingScopes = append([]string(nil), scopes...)
}

func (d *documentInbox) scopesMissing() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.missingScopes...)
}

// scopeError reports whether err says the login lacks a scope (the whole connector), rather than that one document
// cannot be read.
func scopeError(err error) ([]string, bool) {
	var e *adapter.Error
	if errors.As(err, &e) && e != nil && e.Kind == adapter.KindMissingPermission && len(e.MissingScopes) > 0 {
		return e.MissingScopes, true
	}
	return nil, false
}

func (d *documentInbox) rateLimited(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rateUntil.After(now)
}

func (d *documentInbox) setRateLimit(until time.Time) {
	d.mu.Lock()
	if until.After(d.rateUntil) {
		d.rateUntil = until
	}
	d.mu.Unlock()
}

func (c *Connector) pollDocuments(ctx context.Context) bool {
	if !c.inboxShouldCollect(ctx) {
		c.clearDocumentThings()
		return false
	}
	return c.pollDocumentsRound(ctx)
}

func (c *Connector) clearDocumentThings() {
	if c == nil || c.docs == nil {
		return
	}
	c.docs.mu.Lock()
	c.docs.things = map[string]connectorwire.InboxThing{}
	for _, doc := range c.docs.docs {
		doc.Things = map[string]connectorwire.InboxThing{}
	}
	c.docs.mu.Unlock()
}

func (c *Connector) pollDocumentsRound(ctx context.Context) bool {
	if c.runner == nil || c.docs == nil {
		return false
	}
	if c.docs.rateLimited(c.currentTime()) {
		return false
	}
	auth, err := c.runner.AuthStatus(ctx)
	if err != nil || !auth.LoggedIn || auth.OpenID == "" {
		return false
	}
	granted := append([]string(nil), auth.GrantedScopes...)
	sort.Strings(granted)
	c.docs.setLogin(auth.OpenID + "\x00" + strings.Join(granted, " "))
	c.refreshOwned(ctx, auth.OpenID)
	resolved := c.resolveWatched(ctx)
	c.syncWatched(resolved)
	if len(resolved) == 0 {
		return false
	}
	links := make([]string, 0, len(resolved))
	for link := range resolved {
		links = append(links, link)
	}
	sort.Strings(links)
	moved := false
	for _, link := range links {
		c.docs.mu.Lock()
		doc := c.docs.docs[link]
		now := c.currentTime()
		skip := doc == nil || c.docs.skipUnreadable(doc, now) || doc.Auto && now.Before(doc.NextPoll)
		if !skip && doc.Auto {
			doc.NextPoll = now.Add(ownedPollEvery)
		}
		c.docs.mu.Unlock()
		if skip {
			continue
		}
		c.lookUpOwner(ctx, doc)
		changed, err := c.pollDocument(ctx, doc, auth.OpenID, c.lang(ctx))
		if scopes, ok := scopeError(err); ok {
			// The login lacks a comment scope: every document would fail the same way, so none is marked and the
			// round stops. Settings shows the missing scope; a new login clears it.
			c.docs.setMissingScopes(scopes)
			break
		}
		if err != nil {
			switch adapter.KindOf(err) {
			case adapter.KindRateLimited:
				c.docs.setRateLimit(c.currentTime().Add(documentRateBackoff))
			case adapter.KindTimeout:
				c.docs.setRateLimit(c.currentTime().Add(documentTimeoutBackoff))
			case adapter.KindMissingPermission:
				c.docs.markUnreadable(link, doc.Title, c.currentTime())
			}
			if adapter.KindOf(err) == adapter.KindRateLimited || adapter.KindOf(err) == adapter.KindTimeout {
				break
			}
			continue
		}
		// A document read fine: the login has the comment scopes again (re-granted on the Open Platform without a new
		// login, say), so the missing-scope state goes too.
		c.docs.markReadable(link)
		c.docs.setMissingScopes(nil)
		if changed {
			moved = true
		}
	}
	return moved
}

func (c *Connector) syncWatched(resolved map[string]watchedDocument) {
	c.docs.mu.Lock()
	defer c.docs.mu.Unlock()
	direct := make(map[string]struct{}, len(c.docs.options.Documents))
	for _, link := range c.docs.options.Documents {
		direct[link] = struct{}{}
	}
	for link := range c.docs.docs {
		if _, ok := resolved[link]; ok {
			continue
		}
		if _, ok := direct[link]; ok {
			continue
		}
		delete(c.docs.docs, link)
		delete(c.docs.warnings, link)
	}
	for link, metadata := range resolved {
		old := c.docs.docs[link]
		if old == nil {
			old = &watchedDocument{URL: link, Seen: map[string]string{}, Things: map[string]connectorwire.InboxThing{}}
			c.docs.docs[link] = old
		}
		if metadata.Auto && old.NextPoll.IsZero() {
			old.NextPoll = metadata.NextPoll // first read, spread by refreshOwned; later ones the round sets
		}
		old.Canonical, old.Title, old.Type, old.Auto = metadata.Canonical, metadata.Title, metadata.Type, metadata.Auto
		if metadata.Token != "" {
			old.Token = metadata.Token
		}
		if metadata.Owner != "" {
			old.Owner = metadata.Owner
		}
		if old.Seen == nil {
			old.Seen = map[string]string{}
		}
		if old.Things == nil {
			old.Things = map[string]connectorwire.InboxThing{}
		}
	}
}

func (c *Connector) resolveWatched(ctx context.Context) map[string]watchedDocument {
	o := func() DocumentOptions {
		c.docs.mu.Lock()
		defer c.docs.mu.Unlock()
		return c.docs.options.normalized()
	}()
	out := map[string]watchedDocument{}
	for _, link := range o.Documents {
		metadata := watchedDocument{URL: link, Type: documentType(link), Title: link}
		c.docs.mu.Lock()
		cached := c.docs.docs[link]
		if c.docs.skipUnreadable(cached, c.currentTime()) {
			metadata.Canonical, metadata.Title, metadata.Type = cached.Canonical, cached.Title, cached.Type
			c.docs.mu.Unlock()
			out[link] = metadata
			continue
		}
		c.docs.mu.Unlock()
		info, err := c.runner.InspectDocument(ctx, link)
		if scopes, ok := scopeError(err); ok {
			c.docs.setMissingScopes(scopes)
			return out
		}
		if err == nil {
			metadata.Canonical = documentFirstNonEmpty(info.CanonicalURL, info.URL)
			metadata.Token = info.Token
			metadata.Title = documentFirstNonEmpty(info.Title, link)
			metadata.Type = normalizeDocumentType(documentFirstNonEmpty(info.Type, metadata.Type))
		} else if adapter.KindOf(err) == adapter.KindMissingPermission {
			c.docs.markUnreadable(link, link, c.currentTime())
			out[link] = metadata
			continue
		} else if adapter.KindOf(err) == adapter.KindRateLimited {
			c.docs.setRateLimit(c.currentTime().Add(documentRateBackoff))
			return out
		} else if adapter.KindOf(err) == adapter.KindTimeout {
			c.docs.setRateLimit(c.currentTime().Add(documentTimeoutBackoff))
			return out
		}
		out[link] = metadata
	}
	if len(o.WikiSpaces) > 0 {
		for _, space := range o.WikiSpaces {
			for _, node := range c.expandWikiSpace(ctx, space) {
				link := node.URL
				if link == "" {
					link = wikiNodeURL(node.NodeToken)
				}
				if link == "" {
					continue
				}
				metadata := watchedDocument{URL: link, Type: normalizeDocumentType(node.ObjectType), Title: node.Title}
				c.docs.mu.Lock()
				cached := c.docs.docs[link]
				if c.docs.skipUnreadable(cached, c.currentTime()) {
					metadata.Canonical, metadata.Title, metadata.Type = cached.Canonical, cached.Title, cached.Type
					c.docs.mu.Unlock()
					out[link] = metadata
					continue
				}
				c.docs.mu.Unlock()
				info, err := c.runner.InspectDocument(ctx, link)
				if scopes, ok := scopeError(err); ok {
					c.docs.setMissingScopes(scopes)
					return out
				}
				if err == nil {
					metadata.Canonical = documentFirstNonEmpty(info.CanonicalURL, info.URL)
					metadata.Token = info.Token
					metadata.Title = documentFirstNonEmpty(info.Title, metadata.Title, link)
					metadata.Type = normalizeDocumentType(documentFirstNonEmpty(info.Type, metadata.Type))
				} else if adapter.KindOf(err) == adapter.KindMissingPermission {
					c.docs.markUnreadable(link, documentFirstNonEmpty(metadata.Title, link), c.currentTime())
					out[link] = metadata
					continue
				} else if adapter.KindOf(err) == adapter.KindRateLimited {
					c.docs.setRateLimit(c.currentTime().Add(documentRateBackoff))
					return out
				} else if adapter.KindOf(err) == adapter.KindTimeout {
					c.docs.setRateLimit(c.currentTime().Add(documentTimeoutBackoff))
					return out
				}
				out[link] = metadata
			}
		}
	}
	c.docs.mu.Lock()
	for link, doc := range c.docs.owned {
		if _, ok := out[link]; !ok {
			out[link] = doc // a document in the watch list stays a watched one
		}
	}
	c.docs.mu.Unlock()
	if len(out) > maxWatchedDocuments {
		keys := make([]string, 0, len(out))
		for link := range out {
			keys = append(keys, link)
		}
		sort.Strings(keys)
		for _, link := range keys[maxWatchedDocuments:] {
			delete(out, link)
		}
	}
	return out
}

func (c *Connector) expandWikiSpace(ctx context.Context, selected string) []adapter.WikiNode {
	spaceID := ""
	if isLikelyWikiSpaceID(selected) {
		spaceID = wikiSpaceID(selected)
	}
	if spaceID == "" {
		pageToken := ""
		for page := 0; page < maxWikiSpacePages && spaceID == ""; page++ {
			spaces, err := c.runner.ListWikiSpaces(ctx, pageToken, maxWikiSpaces)
			if err != nil {
				return nil
			}
			for _, space := range spaces.Spaces {
				if selected == space.ID || selected == space.URL || strings.EqualFold(selected, space.Name) {
					spaceID = space.ID
					break
				}
			}
			if !spaces.HasMore || spaces.PageToken == "" {
				break
			}
			pageToken = spaces.PageToken
		}
	}
	if spaceID == "" {
		return nil
	}
	var out []adapter.WikiNode
	queue := []string{""}
	scanned := 0
	for len(queue) > 0 && scanned < maxWikiNodes {
		parent := queue[0]
		queue = queue[1:]
		pageToken := ""
		for scanned < maxWikiNodes {
			page, err := c.runner.ListWikiNodes(ctx, spaceID, parent, pageToken, 100)
			if err != nil {
				return out
			}
			for _, node := range page.Nodes {
				if scanned >= maxWikiNodes {
					break
				}
				scanned++
				if node.HasChild {
					if child := documentFirstNonEmpty(node.NodeToken, node.ObjectToken); child != "" && len(queue) < maxWikiNodes {
						queue = append(queue, child)
					}
				}
				if isWatchableDocumentType(node.ObjectType) || strings.TrimSpace(node.URL) != "" {
					out = append(out, node)
				}
			}
			if !page.HasMore || page.PageToken == "" {
				break
			}
			pageToken = page.PageToken
		}
	}
	return out
}

func (c *Connector) pollDocument(ctx context.Context, doc *watchedDocument, me string, lang i18n.Lang) (bool, error) {
	if doc == nil {
		return false, nil
	}
	cursor := ""
	c.docs.mu.Lock()
	if current := c.docs.docs[doc.URL]; current != nil {
		cursor = current.Cursor
	}
	c.docs.mu.Unlock()
	pageToken := ""
	moved := false
	pages := 0
	for pages < maxCommentPages {
		page, err := c.runner.ListComments(ctx, doc.URL, pageToken, 100)
		if err != nil {
			return moved, err
		}
		pages++
		for _, comment := range page.Comments {
			if comment.ID == "" || comment.Solved {
				continue
			}
			if comment.UpdateTime == nil && comment.CreateTime == nil {
				comment.UpdateTime = json.RawMessage(strconv.FormatInt(time.Now().Unix(), 10))
			}
			replies, err := c.commentReplies(ctx, doc.URL, comment)
			if err != nil {
				return moved, err
			}
			if changed, latest := c.consumeComment(doc, comment, replies, me, cursor, lang); changed {
				moved = true
				cursor = maxCursor(cursor, latest)
			} else if latest != "" {
				cursor = maxCursor(cursor, latest)
			}
		}
		if !page.HasMore || page.PageToken == "" {
			break
		}
		pageToken = page.PageToken
	}
	c.docs.mu.Lock()
	if current := c.docs.docs[doc.URL]; current != nil {
		current.Cursor = cursor
	}
	c.docs.mu.Unlock()
	return moved, nil
}

func (c *Connector) commentReplies(ctx context.Context, link string, comment adapter.Comment) ([]adapter.Reply, error) {
	replies := append([]adapter.Reply(nil), comment.ReplyList.Replies...)
	pageToken := comment.ReplyList.PageToken
	more := comment.ReplyList.HasMore
	if pageToken == "" {
		pageToken = comment.ReplyToken
	}
	if !more {
		more = comment.ReplyMore
	}
	if len(replies) == 0 {
		page, err := c.runner.ListReplies(ctx, link, comment.ID, "", 100)
		if err != nil {
			return nil, err
		}
		replies = append(replies, page.Replies...)
		pageToken, more = page.PageToken, page.HasMore
	}
	pages := 0
	for more && pageToken != "" && pages < maxReplyPages {
		page, err := c.runner.ListReplies(ctx, link, comment.ID, pageToken, 100)
		if err != nil {
			return nil, err
		}
		replies = append(replies, page.Replies...)
		pageToken, more = page.PageToken, page.HasMore
		pages++
	}
	return replies, nil
}

type commentEvent struct {
	ID      string
	UserID  string
	Content json.RawMessage
	At      time.Time
	Mention bool
	Root    bool
}

func (c *Connector) consumeComment(doc *watchedDocument, comment adapter.Comment, replies []adapter.Reply, me, cursor string, lang i18n.Lang) (bool, string) {
	events := make([]commentEvent, 0, len(replies)+1)
	rootContent := comment.Content
	if len(rootContent) == 0 && len(replies) > 0 {
		rootContent = replies[0].Content
	}
	// update_time moves when a reply is added; the root event itself keeps its
	// creation time so the newest reply can win the Inbox preview.
	events = append(events, commentEvent{ID: comment.ID, UserID: comment.UserID, Content: rootContent, At: wireTime(comment.CreateTime, comment.UpdateTime), Mention: rawMentions(rootContent, me), Root: true})
	for i, reply := range replies {
		// The first reply on the first page is the root comment body.
		if i == 0 && (comment.Content == nil || reply.ID == comment.ID || reply.UserID == comment.UserID) {
			continue
		}
		id := reply.ID
		if id == "" {
			id = comment.ID + ":reply:" + strconv.Itoa(i)
		}
		events = append(events, commentEvent{ID: id, UserID: reply.UserID, Content: reply.Content, At: wireTime(reply.UpdateTime, reply.CreateTime), Mention: rawMentions(reply.Content, me)})
	}
	participated := comment.UserID == me
	c.docs.mu.Lock()
	ownedAll := c.docs.options.OwnedAllComments && me != "" && doc.Owner == me
	c.docs.mu.Unlock()
	for _, event := range events {
		if event.UserID == me {
			participated = true
		}
	}
	var candidate *commentEvent
	latest := ""
	for _, event := range events {
		if event.ID == "" {
			continue
		}
		revision := eventRevision(event)
		c.docs.mu.Lock()
		seen := doc.Seen[event.ID]
		if cursor != "" && revision <= cursor && seen != "" {
			c.docs.mu.Unlock()
			continue
		}
		if seen == revision {
			c.docs.mu.Unlock()
			continue
		}
		doc.Seen[event.ID] = revision
		c.docs.mu.Unlock()
		latest = maxCursor(latest, revision)
		if event.UserID == me {
			continue
		}
		if !event.Mention && !participated && !ownedAll {
			continue
		}
		if candidate == nil || event.At.After(candidate.At) {
			copy := event
			candidate = &copy
		}
	}
	if candidate == nil {
		return false, latest
	}
	thread := "doc:" + doc.URL + ":" + comment.ID
	thing := c.documentThing(doc, comment, *candidate, me, thread, lang)
	if !candidate.At.IsZero() && !candidate.At.After(time.Now().Add(24*time.Hour)) && cursor != "" && eventRevision(*candidate) <= cursor {
		// The event can be a newly observed reply with an older source clock;
		// Seen still makes it idempotent, while this guard avoids rewinding an
		// item's timestamp after a cursor has advanced.
		c.docs.mu.Lock()
		if old, ok := doc.Things[thread]; ok && !thing.At.After(old.At) {
			c.docs.mu.Unlock()
			return false, latest
		}
		c.docs.mu.Unlock()
	}
	c.docs.mu.Lock()
	doc.Things[thread] = thing
	c.docs.things[thread] = thing
	c.docs.mu.Unlock()
	return true, latest
}

func (c *Connector) documentThing(doc *watchedDocument, comment adapter.Comment, event commentEvent, me, thread string, lang i18n.Lang) connectorwire.InboxThing {
	at := event.At
	if at.IsZero() {
		at = time.Now()
	}
	preview := oneLine(documentRawText(event.Content))
	if preview == "" {
		preview = oneLine(comment.Quote)
	}
	quote := strings.TrimSpace(comment.Quote)
	if quote == "" {
		quote = "Whole document"
	}
	notice := "reply"
	if event.Mention {
		notice = "mention"
	} else if event.Root {
		notice = "comment" // a new comment on a document the user owns (OwnedAllComments)
	}
	commentURL := commentLink(doc.URL, comment.ID)
	docBlock, _ := json.Marshal(map[string]any{
		"url": commentURL, "link": documentFirstNonEmpty(doc.Canonical, doc.URL), "type": normalizeDocumentType(doc.Type),
		"notice": notice, "notice_id": comment.ID, "quote": quote,
	})
	contextText := fmt.Sprintf("Document: %s\nQuoted text: %s\nComment: %s", documentFirstNonEmpty(doc.Title, doc.URL), quote, preview)
	if event.UserID != "" && event.UserID != me {
		contextText += "\nAuthor: " + event.UserID
	}
	last := event.ID + "@" + eventRevision(event)
	thing := connectorwire.InboxThing{
		Thread: thread, Kind: "doc", Label: i18n.T(lang, "hub-connectors.doc_comment_label"), Title: documentFirstNonEmpty(doc.Title, doc.URL), Sender: event.UserID,
		Preview: preview, Context: contextText, At: at, Last: last, URL: commentURL,
		Extra: &connectorwire.ThingExtra{Count: 1, Doc: docBlock}, Rules: c.rules(),
	}
	if event.Mention {
		// A comment that mentions the user asks for them; replies and comments on documents they own are left to the
		// Inbox's triage, like lark-cli's chat items.
		thing.Tier, thing.Why = connectorwire.TierAction, i18n.T(lang, "hub-connectors.doc_comment_rule")
	}
	return thing
}

func eventRevision(event commentEvent) string {
	if !event.At.IsZero() {
		return event.At.UTC().Format(time.RFC3339Nano)
	}
	h := sha1.Sum(event.Content)
	return hex.EncodeToString(h[:])
}

func wireTime(values ...json.RawMessage) time.Time {
	for _, raw := range values {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return t
			}
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return unixTime(n)
			}
			// lark-cli prints message times as local wall-clock minutes; accept the same here in case comments follow.
			for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04"} {
				if t, err := time.ParseInLocation(layout, s, cliLocation()); err == nil {
					return t
				}
			}
		}
		var n int64
		if json.Unmarshal(raw, &n) == nil {
			return unixTime(n)
		}
	}
	return time.Time{}
}

func unixTime(n int64) time.Time {
	if n > 1_000_000_000_000 {
		return time.UnixMilli(n)
	}
	return time.Unix(n, 0)
}

func rawMentions(raw json.RawMessage, me string) bool {
	if me == "" || len(raw) == 0 {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		var nested string
		if json.Unmarshal(raw, &nested) == nil && nested != string(raw) {
			return rawMentions(json.RawMessage(nested), me)
		}
		return false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				switch strings.ToLower(strings.ReplaceAll(key, "_", "")) {
				case "userid", "openid", "unionid":
					if fmt.Sprint(value) == me {
						return true
					}
				}
				if walk(value) {
					return true
				}
			}
		case []any:
			for _, value := range x {
				if walk(value) {
					return true
				}
			}
		case string:
			if x != me && strings.Contains(x, me) {
				return true
			}
		}
		return false
	}
	return walk(value)
}

func documentRawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var parts []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				lower := strings.ToLower(key)
				if lower == "text" || lower == "text_run" || lower == "plain_text" {
					if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
						parts = append(parts, s)
						continue
					}
				}
				if lower == "type" || strings.HasSuffix(lower, "_id") || lower == "id" || lower == "mention_user" {
					continue
				}
				walk(value)
			}
		case []any:
			for _, value := range x {
				walk(value)
			}
		case string:
			var nested any
			if strings.HasPrefix(strings.TrimSpace(x), "{") || strings.HasPrefix(strings.TrimSpace(x), "[") {
				if json.Unmarshal([]byte(x), &nested) == nil {
					walk(nested)
					return
				}
			}
			if strings.TrimSpace(x) != "" {
				parts = append(parts, x)
			}
		}
	}
	walk(value)
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return strings.Join(out, " ")
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if r := []rune(s); len(r) > 280 {
		return string(r[:280]) + "…"
	}
	return s
}

func maxCursor(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if b > a {
		return b
	}
	return a
}

func commentLink(link, commentID string) string {
	if commentID == "" {
		return link
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme == "" {
		sep := "?"
		if strings.Contains(link, "?") {
			sep = "&"
		}
		return link + sep + "comment_id=" + url.QueryEscape(commentID)
	}
	q := u.Query()
	q.Set("comment_id", commentID)
	u.RawQuery = q.Encode()
	return u.String()
}

func wikiSpaceID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.Contains(value, "/") {
		return value
	}
	u, err := url.Parse(value)
	if err != nil {
		return value
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return value
}

func isLikelyWikiSpaceID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.Contains(value, "/") {
		return true
	}
	return strings.HasPrefix(strings.ToLower(value), "spc") || strings.HasPrefix(strings.ToLower(value), "space_")
}

func wikiNodeURL(token string) string {
	if token == "" {
		return ""
	}
	return "https://feishu.cn/wiki/" + token
}

func documentType(link string) string {
	lower := strings.ToLower(link)
	for _, kind := range []string{"docx", "sheet", "base", "bitable", "doc", "wiki"} {
		if strings.Contains(lower, "/"+kind+"/") {
			return normalizeDocumentType(kind)
		}
	}
	return "other"
}

func normalizeDocumentType(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "bitable" {
		return "base"
	}
	if kind == "wiki" {
		return "docx"
	}
	if isWatchableDocumentType(kind) {
		return kind
	}
	return "other"
}

func isWatchableDocumentType(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "doc", "docx", "sheet", "base", "bitable":
		return true
	default:
		return false
	}
}

func documentFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// lookUpOwner reads a watched document's owner once, when OwnedAllComments is on. A failure leaves it unknown for
// the next round; the document's comments are still kept by the mention and participation rules meanwhile.
func (c *Connector) lookUpOwner(ctx context.Context, doc *watchedDocument) {
	c.docs.mu.Lock()
	need := c.docs.options.OwnedAllComments && doc.Owner == "" && doc.Token != "" && doc.Type != ""
	token, docType := doc.Token, doc.Type
	c.docs.mu.Unlock()
	if !need {
		return
	}
	owner, err := c.runner.DocumentOwner(ctx, token, docType)
	if err != nil || owner == "" {
		return
	}
	c.docs.mu.Lock()
	doc.Owner = owner
	c.docs.mu.Unlock()
}

// refreshOwned lists the user's own documents for owned_all_comments: edited or opened within ownedWindow, newest
// edit first, at most maxOwnedDocuments, every ownedRefresh. Their reads are spread over ownedPollEvery. A failed
// listing keeps the previous list; turning the option off drops it.
func (c *Connector) refreshOwned(ctx context.Context, me string) {
	now := c.currentTime()
	c.docs.mu.Lock()
	on := c.docs.options.OwnedAllComments
	due := now.Sub(c.docs.ownedAt) >= ownedRefresh
	if !on {
		c.docs.owned, c.docs.ownedAt = nil, time.Time{}
	}
	c.docs.mu.Unlock()
	if !on || !due {
		return
	}
	owned := map[string]watchedDocument{}
	token := ""
	for page := 0; page < maxOwnedPages && len(owned) < maxOwnedDocuments; page++ {
		docs, next, err := c.runner.OwnedDocuments(ctx, token)
		if err != nil {
			if adapter.KindOf(err) == adapter.KindRateLimited {
				c.docs.setRateLimit(now.Add(documentRateBackoff))
			}
			return
		}
		for _, d := range docs {
			recent := d.Updated.After(now.Add(-ownedWindow)) || d.Opened.After(now.Add(-ownedWindow))
			if !recent || d.Owner != "" && d.Owner != me || len(owned) >= maxOwnedDocuments {
				continue
			}
			// Spread the first reads over the poll interval instead of reading every document in one round.
			first := now.Add(time.Duration(len(owned)%5) * (ownedPollEvery / 5))
			owned[d.URL] = watchedDocument{URL: d.URL, Title: documentFirstNonEmpty(d.Title, d.URL), Type: normalizeDocumentType(d.Type), Token: d.Token, Owner: me, Auto: true, NextPoll: first}
		}
		if next == "" {
			break
		}
		token = next
	}
	c.docs.mu.Lock()
	c.docs.owned, c.docs.ownedAt = owned, now
	c.docs.mu.Unlock()
}
