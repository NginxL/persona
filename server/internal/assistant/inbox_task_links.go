package assistant

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

var (
	inboxTaskURL = lazyre.New(`https?://[^\s<>"\x60]+`)
	// inboxRepoRef: owner/repo#N written out, as GitHub shows a cross-repository reference.
	inboxRepoRef = lazyre.New(`(?:^|[^\w./-])([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([1-9][0-9]*)\b`)
	// inboxBareRef: a bare #N (not an anchor in a URL, not part of a word or of owner/repo#N).
	inboxBareRef = lazyre.New(`(?:^|[^\w./#&-])#([1-9][0-9]*)\b`)
	// inboxCloses: a pull request's own words that it closes an issue of its repository (GitHub's closing keywords).
	inboxCloses = lazyre.New(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+(?:https://github\.com/([\w.-]+/[\w.-]+)/issues/|([\w.-]+/[\w.-]+)?#)([1-9][0-9]*)\b`)
)

// githubRef is the ref of number n in GitHub repository repo (owner/repo): GitHub numbers issues and pull requests in
// one sequence, so the kind is left out and an issue's link and a bare #N meet a PR's.
func githubRef(repo string, n uint64) string {
	return "github.com/" + strings.ToLower(repo) + "#" + strconv.FormatUint(n, 10)
}

// inboxTaskRefs keeps the host, repository, PR/issue kind and number of every link in text (on github.com without the
// kind: one number is one thing there), and every owner/repo#N. Comment fragments and queries do not change the task.
// A bare #N is not here: it names nothing without a repository (inboxBareRefs).
func inboxTaskRefs(text string) []string {
	var refs []string
	for _, raw := range inboxTaskURL.FindAllString(text, -1) {
		u, err := url.Parse(strings.TrimRight(raw, ".,;:!?)]}，。；：！）》」』")) // i18n-ignore: URL punctuation, not displayed text
		if err != nil || u.Host == "" || u.User != nil {
			continue
		}
		parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
		if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" && parts[2] != "issues" {
			continue
		}
		n, err := strconv.ParseUint(parts[3], 10, 64)
		if err != nil || n == 0 {
			continue
		}
		if strings.EqualFold(u.Hostname(), "github.com") {
			refs = append(refs, githubRef(parts[0]+"/"+parts[1], n))
			continue
		}
		refs = append(refs, strings.ToLower(u.Host)+"/"+strings.Join(parts[:3], "/")+"/"+strconv.FormatUint(n, 10))
	}
	for _, m := range inboxRepoRef.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.ParseUint(m[2], 10, 64); err == nil {
			refs = append(refs, githubRef(m[1], n))
		}
	}
	return refs
}

// inboxBareRefs are the bare #N of text.
func inboxBareRefs(text string) []uint64 {
	var out []uint64
	for _, m := range inboxBareRef.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.ParseUint(m[1], 10, 64); err == nil && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// inboxItemNumber is the number of a GitHub item's own issue or pull request (its thread owner/repo#N); 0 for none.
func inboxItemNumber(it *InboxItem) uint64 {
	if it.Source != "github" || it.Connector == nil {
		return 0
	}
	m := inboxRepoThread.FindStringSubmatch(strings.TrimSpace(it.Connector.Thread))
	if m == nil {
		return 0
	}
	_, num, _ := strings.Cut(strings.TrimSpace(it.Connector.Thread), "#")
	n, _ := strconv.ParseUint(num, 10, 64)
	return n
}

// taskText is what a task's refs are read from: its PR links (the current one and the earlier ones its Agents named),
// title and notes.
func taskText(t Todo) string {
	return t.PRURL + "\n" + strings.Join(t.PRs, "\n") + "\n" + t.Title + "\n" + t.Notes
}

// inboxTaskIndex finds the open tasks an item's refs name (#5578, #6507).
type inboxTaskIndex struct {
	byRef map[string][]string // ref → the open tasks naming it in full
	bare  map[uint64][]Todo   // #N → the open tasks writing it bare; they count for a repository only when of it
	open  map[string]Todo
}

func (x *inboxTaskIndex) add(t Todo) {
	x.open[t.ID] = t
	text := taskText(t)
	for _, ref := range inboxTaskRefs(text) {
		if !slices.Contains(x.byRef[ref], t.ID) {
			x.byRef[ref] = append(x.byRef[ref], t.ID)
		}
	}
	for _, n := range inboxBareRefs(t.Title + "\n" + t.Notes) {
		x.bare[n] = append(x.bare[n], t)
	}
}

// tasksOf are the open tasks naming ref; repo and n, when set, are the GitHub repository and number the ref is, which
// a bare #N of a task of that repository names too (taskOfRepo, inbox_task_scope.go).
func (x *inboxTaskIndex) tasksOf(ref, repo string, n uint64) []string {
	ids := slices.Clone(x.byRef[ref])
	if repo != "" && n != 0 {
		for _, t := range x.bare[n] {
			if !slices.Contains(ids, t.ID) && taskOfRepo(t, repo) {
				ids = append(ids, t.ID)
			}
		}
	}
	return ids
}

// match is the one open task item it names: its links, its own number (a GitHub item), and for a GitHub item no link
// names, the pull requests of its repository that say they close it (closes: the item's ref → those PRs' numbers).
// "" when none does or more than one does (the judge's to decide).
func (x *inboxTaskIndex) match(it *InboxItem, closes map[string][]uint64) string {
	text := it.Title + " " + it.Preview + " " + it.Context
	if it.Connector != nil {
		text += " " + it.Connector.URL
	}
	repo, own := "", inboxItemNumber(it)
	if own != 0 {
		repo = inboxScopeOf(it).repo
	}
	found := map[string]bool{}
	note := func(ids []string) {
		for _, id := range ids {
			found[id] = true
		}
	}
	for _, ref := range inboxTaskRefs(text) {
		note(x.tasksOf(ref, "", 0))
	}
	if repo != "" {
		note(x.tasksOf(githubRef(repo, own), repo, own))
		if len(found) == 0 {
			for _, pr := range closes[githubRef(repo, own)] {
				note(x.tasksOf(githubRef(repo, pr), repo, pr))
			}
		}
	}
	if len(found) != 1 {
		return ""
	}
	for id := range found {
		return id
	}
	return ""
}

// inboxClosesIndex reads, off the GitHub pull request items the Inbox holds (open or done), which issues of their
// repository each says it closes: issue ref → pull request numbers. Under b.mu.
func inboxClosesIndex(items map[string]*InboxItem) map[string][]uint64 {
	out := map[string][]uint64{}
	for _, it := range items {
		pr := inboxItemNumber(it)
		if pr == 0 || it.Connector == nil || !strings.Contains(it.Connector.URL, "/pull/") {
			continue
		}
		repo := inboxScopeOf(it).repo
		for _, m := range inboxCloses.FindAllStringSubmatch(it.Title+"\n"+it.Preview+"\n"+it.Context, -1) {
			if r := cmp.Or(m[1], m[2], repo); !strings.EqualFold(r, repo) {
				continue // another repository's issue
			}
			n, err := strconv.ParseUint(m[3], 10, 64)
			if err != nil || n == pr {
				continue
			}
			if ref := githubRef(repo, n); !slices.Contains(out[ref], pr) {
				out[ref] = append(out[ref], pr)
			}
		}
	}
	return out
}

// linkInboxTaskURLs runs before the AI twin's switch: exact task links need neither triage nor a model. Keep
// existing links, leave ambiguous matches to the judge, and persist new links before notifying their supervisors. An
// existing link of a GitHub item to an open task that does not name its repository (inbox_task_scope.go, #6424) is
// cleared instead: persisted like any item write, and nothing is told about it.
func (s *Service) linkInboxTaskURLs(ctx context.Context) {
	if s.ensureTodosLoaded(ctx) != nil {
		return
	}
	idx := &inboxTaskIndex{byRef: map[string][]string{}, bare: map[uint64][]Todo{}, open: map[string]Todo{}}
	s.todos.mu.RLock()
	for _, t := range s.todos.todos {
		if t.DoneAt != nil || t.Cancelled() || isAskTodo(t.ID) {
			continue
		}
		idx.add(*t)
	}
	s.todos.mu.RUnlock()
	open := idx.open
	b := &s.inbox
	linked := map[*InboxItem]bool{}
	var writes []*inboxWrite
	var items []*InboxItem
	var unlinked []string
	var closes map[string][]uint64
	var done []*InboxItem
	b.mu.Lock()
	for _, it := range b.items {
		if !inboxTaskLinkEligible(it) {
			continue
		}
		if !it.open() {
			if it.Agent != nil && it.Agent.TodoID != "" {
				done = append(done, it) // its detail still names a task: the one doing the work now (#6546)
			}
			continue
		}
		if a := it.Agent; a != nil && a.TodoID != "" {
			if sc := inboxScopeOf(it); sc.repo != "" {
				if t, ok := open[a.TodoID]; ok && !taskOfRepo(t, sc.repo) {
					w := beginWrite(it)
					unlinked = append(unlinked, it.ID+" from "+a.TodoID)
					a.TodoID = ""
					b.settle(w)
					writes = append(writes, w)
					continue // one write per item a pass: a URL of it can link it on the next one
				}
			}
		}
		if it.Agent == nil || it.Agent.TodoID == "" {
			if closes == nil {
				closes = inboxClosesIndex(b.items)
			}
			id := idx.match(it, closes)
			if id != "" {
				w := beginWrite(it)
				if it.Agent == nil {
					it.Agent = &InboxAgent{State: AgentSkipped}
				}
				it.Agent.TodoID = id
				b.settle(w)
				writes = append(writes, w)
				linked[it] = true
			}
		}
		if it.Agent != nil && it.Agent.TodoID != "" {
			items = append(items, it)
		}
	}
	b.mu.Unlock()
	for _, u := range unlinked {
		s.logf("inbox: unlinked %s: the task does not name the item's repository", u)
	}
	failed := s.saveWrites(ctx, writes, "its task URL link")
	kept := items[:0]
	for _, it := range items {
		if !failed[it.ID] {
			kept = append(kept, it)
		}
	}
	s.superviseInbox(ctx, kept, linked)
	s.followContinuations(ctx, done) // a handled item gets no signal, but its link follows the work as an open one's does
}
