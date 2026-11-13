package assistant

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// What pins an Inbox item to its place before the task judge looks at it (#6424, docs/assistant/inbox.md "Task-related items").
// A model asked "which of these tasks is it about" picks the closest-sounding one even when none is, so the judge only
// sees the tasks that can be the one, and its answer only counts with a line it copied from that task.
//
//   - A GitHub item (its repo tag, or a thread like owner/repo#12) belongs to a task that names owner/repo in its title,
//     notes or PR link, or whose Agent works in a directory named after the repository. None does: no judge, no link.
//     A link that fails this later is cleared on the next pass (linkInboxTaskURLs).
//   - A chat thread item (connector thread chat:…:thread:…, its chat tag): when some tasks name its thread or chat id,
//     only those are offered; when none does, all of them are, as before.

// inboxScope is where an item is from: a GitHub repository (owner/repo, lower case), or else a chat thread's ids.
type inboxScope struct {
	repo string
	keys []string
}

var inboxRepoThread = lazyre.New(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#[0-9]+$`)

// inboxScopeKeyMin: shorter thread or chat ids are not taken as ids (a word in a task's notes could equal them).
const inboxScopeKeyMin = 6

// inboxScopeOf is the scope of item it. Under b.mu.
func inboxScopeOf(it *InboxItem) inboxScope {
	var sc inboxScope
	c := it.Connector
	if c == nil {
		return sc
	}
	for _, tag := range c.Tags {
		if v := strings.TrimSpace(tag.Value); tag.Key == "repo" && strings.Count(v, "/") == 1 && !strings.HasPrefix(v, "/") && !strings.HasSuffix(v, "/") {
			sc.repo = strings.ToLower(v)
		}
	}
	if sc.repo == "" && it.Source == "github" {
		if m := inboxRepoThread.FindStringSubmatch(strings.TrimSpace(c.Thread)); m != nil {
			sc.repo = strings.ToLower(m[1])
		}
	}
	if sc.repo != "" {
		return sc
	}
	add := func(k string) {
		if k = strings.TrimSpace(k); len(k) >= inboxScopeKeyMin && !slices.Contains(sc.keys, k) {
			sc.keys = append(sc.keys, k)
		}
	}
	parts := strings.Split(c.Thread, ":")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "chat" || parts[i] == "thread" {
			add(parts[i+1])
		}
	}
	for _, tag := range c.Tags {
		if tag.Key == "chat" {
			add(tag.Value)
		}
	}
	return sc
}

// repoChar: a character that can be part of an owner or repository name.
func repoChar(r byte) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-'
}

// namesRepo: lower-case text names owner/repo as a whole (not owner/repo-other, not another-owner/repo).
func namesRepo(text, repo string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], repo)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(repo)
		if (start == 0 || !repoChar(text[start-1])) && (end == len(text) || !repoChar(text[end]) || text[end] == '.' && (end+1 == len(text) || !repoChar(text[end+1]))) {
			return true
		}
		i = start + 1
	}
}

// dirOfRepo: path has a directory named after the repository (name, or name-something for a worktree of it).
func dirOfRepo(path, name string) bool {
	for _, seg := range strings.Split(strings.ToLower(path), "/") {
		if seg == name || strings.HasPrefix(seg, name+"-") {
			return true
		}
	}
	return false
}

// taskOfRepo: task t names repository repo (owner/repo, lower case), or one of its Agents or spaces works in it.
func taskOfRepo(t Todo, repo string) bool {
	if namesRepo(strings.ToLower(taskText(t)), repo) {
		return true
	}
	name := repo[strings.IndexByte(repo, '/')+1:]
	for _, a := range t.Agents {
		if a.DismissedAt == nil && dirOfRepo(a.Cwd, name) {
			return true
		}
	}
	for _, sp := range t.Spaces {
		if dirOfRepo(sp.Space, name) {
			return true
		}
	}
	return false
}

// scopeInboxTasks keeps the tasks of tasks that item scope sc can be about (see above). A task it cannot look up is
// kept only where the filter keeps all. Not under b.mu or the todos' lock.
func (s *Service) scopeInboxTasks(sc inboxScope, tasks []prompts.InboxTask) []prompts.InboxTask {
	if sc.repo == "" && len(sc.keys) == 0 {
		return tasks
	}
	var out []prompts.InboxTask
	for _, task := range tasks {
		t, ok := s.todoNow(task.ID)
		if !ok {
			continue
		}
		if sc.repo != "" {
			if taskOfRepo(t, sc.repo) {
				out = append(out, task)
			}
			continue
		}
		if slices.ContainsFunc(sc.keys, func(k string) bool { return strings.Contains(t.Title+"\n"+t.Notes, k) }) {
			out = append(out, task)
		}
	}
	if sc.repo == "" && len(out) == 0 {
		return tasks // no task names the thread: the judge decides among all, as before
	}
	return out
}

// judgeEvidenceMin is the shortest quote taken as evidence, in characters.
const judgeEvidenceMin = 4

// foldSpace is s lower-cased with every run of white space one space.
func foldSpace(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// judgeEvidence: quote ev is copied from task's title or notes as the judge was shown them.
func judgeEvidence(task prompts.InboxTask, ev string) bool {
	ev = foldSpace(ev)
	if utf8.RuneCountInString(ev) < judgeEvidenceMin {
		return false
	}
	return strings.Contains(foldSpace(task.Title), ev) || strings.Contains(foldSpace(task.Notes), ev)
}
