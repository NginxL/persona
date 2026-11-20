package kb

import (
	"time"
)

// A conversation's state at its source (#9427, docs/assistant/knowledge.md "Chat state and finished projects"): a
// pull request merged or closed adds no message, so the chat records the state the source reports with every batch,
// and the projects it belongs to are rewritten with it.

// noteChatSource applies the source observations and reports changes to delivery state and URL separately.
func noteChatSource(c *Chat, sc SourceChat, now time.Time) (stateChanged, urlChanged bool) {
	if _, link := canonicalSourceURL(sc.SourceURL); link != "" && link != c.SourceURL {
		c.SourceURL = link
		urlChanged = true
	}
	if sc.State == "" || sc.State == c.State {
		return false, urlChanged
	}
	c.State, c.StateAt = sc.State, sc.StateAt.UTC()
	if c.StateAt.IsZero() {
		c.StateAt = now.UTC()
	}
	return true, urlChanged
}

// recordChatSource saves changed source metadata when Ingest has no new lines.
func (e *Engine) recordChatSource(id string, sc SourceChat) error {
	if sc.State == "" && sc.SourceURL == "" {
		return nil
	}
	e.Store.chatMu.Lock()
	if err := e.Store.recoverIngest(id); err != nil {
		e.Store.chatMu.Unlock()
		return err
	}
	c, ok := e.Store.Chat(id)
	if !ok {
		e.Store.chatMu.Unlock()
		return nil
	}
	stateChanged, urlChanged := noteChatSource(c, sc, time.Now())
	var err error
	if stateChanged || urlChanged {
		err = e.Store.PutChat(c)
	}
	e.Store.chatMu.Unlock()
	if err != nil {
		return err
	}
	if stateChanged {
		e.chatStateChanged(c)
	}
	return nil
}

// chatStateChanged marks the projects c belongs to for a rewrite, so their summary and one line follow the new state.
// Their Updated stays: nothing was written into them yet.
func (e *Engine) chatStateChanged(c *Chat) {
	for _, slug := range c.Projects {
		p, ok := e.Store.Project(slug)
		if !ok || !p.Live() || p.referenceOnly() || p.Status == "ignored" || p.Dirty {
			continue
		}
		p.Dirty = true
		if err := e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, false); err != nil {
			e.logf("project %s: %v", slug, err)
		}
	}
}

// projectEnded reports whether p links chats and every one of them has ended at its source; last is the latest of
// their last messages and state changes.
func (e *Engine) projectEnded(p *Project) (ended bool, last time.Time) {
	if len(p.Chats) == 0 {
		return false, last
	}
	for _, id := range p.Chats {
		c, ok := e.Store.Chat(id)
		if !ok || !c.Ended() {
			return false, time.Time{}
		}
		for _, at := range []time.Time{c.LastAt, c.StateAt} {
			if at.After(last) {
				last = at
			}
		}
	}
	return true, last
}

// projectFinishQuiet is how long nothing may have happened to a project whose chats have all ended before
// FinishProjects finishes it.
const projectFinishQuiet = 3 * 24 * time.Hour

// FinishProjects sets to done the projects that are over by the sources' own facts, without a model: active or paused,
// not a long-running direction, status not locked, not busy (an unfinished to-do is filed under it; nil: none is),
// every linked chat ended at its source, and nothing (a chat's message or state change, a timeline entry) for
// projectFinishQuiet. New activity revives such a project as any other (evolveProject). It returns the slugs it
// finished.
func (e *Engine) FinishProjects(now time.Time, busy func(slug string) bool) []string {
	if e.Store.ReadOnly() {
		return nil
	}
	var out []string
	for _, p := range e.Store.Projects() {
		if !p.Live() || p.referenceOnly() || p.Status != "active" && p.Status != "paused" || p.Kind == "long" || p.fieldLocked("status") {
			continue
		}
		if busy != nil && busy(p.Slug) {
			continue
		}
		ended, last := e.projectEnded(p)
		if !ended {
			continue
		}
		if at := e.lastLogAt(p.Slug); at.After(last) {
			last = at
		}
		if now.Sub(last) < projectFinishQuiet {
			continue
		}
		p.Status = "done"
		if err := e.Store.PutProject(p); err != nil {
			e.logf("project %s: %v", p.Slug, err)
			continue
		}
		e.index(KindProject, p.Slug, p.Name, p.Tagging, p.OneLine, p.Updated)
		out = append(out, p.Slug)
	}
	return out
}

// lastLogAt is when the newest line of slug's timeline was written (zero: none, or not readable).
func (e *Engine) lastLogAt(slug string) time.Time {
	lines := e.Store.Log(slug, 1)
	if len(lines) == 0 {
		return time.Time{}
	}
	// "- 2006-01-02 15:04 · source · text" (Store.AppendLog)
	line := lines[0]
	if len(line) < len("- 2006-01-02 15:04") {
		return time.Time{}
	}
	at, err := time.ParseInLocation("2006-01-02 15:04", line[2:18], e.Store.Zone())
	if err != nil {
		return time.Time{}
	}
	return at.Add(time.Minute) // the line's minute is cut: count from its end
}
