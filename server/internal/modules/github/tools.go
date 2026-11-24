package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/githubwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant's read tools (prompts/mcp/github): they only read, so they run without asking.

var readTools = []string{"github_search", "github_read", "github_diff", "github_checks"}

func (c *Connector) Tools() []proto.Tool {
	out := make([]proto.Tool, 0, len(readTools))
	for _, n := range readTools {
		t := prompts.Tool(n)
		out = append(out, proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// Limits of what a tool returns, so one answer does not fill the model's context.
const (
	bodyCap    = 4000  // a description
	commentCap = 1500  // one comment or review
	patchCap   = 20000 // one file's patch
	logTail    = 6000  // the end of a failed job's log
	failedLogs = 3     // jobs whose log is read
)

// ValidRepo reports whether r is a safe GitHub owner/name pair.
func ValidRepo(r string) bool { return githubwire.ValidRepo(r) }

func validRepo(r string) bool { return githubwire.ValidRepo(r) }

func (c *Connector) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	a, err := c.answer(ctx, name, args)
	if err != nil {
		return "", err
	}
	return a.text(), nil
}

var _ connectorwire.ReadJSON = (*Connector)(nil)

// CallJSON is connectorwire.ReadJSON: every read tool's answer for subscriptions, as JSON with an id on each item
// (tools_json.go). The models get the text Call writes from the same answer.
func (c *Connector) CallJSON(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, bool, error) {
	if !slices.Contains(readTools, name) {
		return nil, false, nil
	}
	a, err := c.answer(ctx, name, args)
	if err != nil {
		return nil, true, err
	}
	out, err := json.Marshal(a.data())
	return out, true, err
}

// answer is what a read tool found: text for the models, data for subscriptions.
type answer interface {
	text() string
	data() any
}

func (c *Connector) answer(ctx context.Context, name string, args json.RawMessage) (answer, error) {
	var a struct {
		Query    string `json:"query"`
		Kind     string `json:"kind"`
		Limit    int    `json:"limit"`
		Repo     string `json:"repo"`
		Number   int    `json:"number"`
		Comments *int   `json:"comments"`
		Path     string `json:"path"`
		Ref      string `json:"ref"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if name != "github_search" && !validRepo(a.Repo) {
		return nil, fmt.Errorf("repo must be owner/name, not %q", a.Repo)
	}
	switch name {
	case "github_search":
		return c.search(ctx, a.Query, a.Kind, a.Limit)
	case "github_read":
		n := 15
		if a.Comments != nil {
			n = min(max(*a.Comments, 0), 50)
		}
		return c.read(ctx, a.Repo, a.Number, n)
	case "github_diff":
		return c.diff(ctx, a.Repo, a.Number, a.Path)
	case "github_checks":
		return c.checks(ctx, a.Repo, a.Number, a.Ref)
	}
	return nil, fmt.Errorf("unknown tool %s", name)
}

type user struct {
	Login string `json:"login"`
	Type  string `json:"type"` // User | Bot
}

func (u user) String() string {
	if u.Login == "" {
		return "(deleted user)"
	}
	return u.Login
}

type issue struct {
	Number      int                     `json:"number"`
	Title       string                  `json:"title"`
	State       string                  `json:"state"`
	User        user                    `json:"user"`
	Body        string                  `json:"body"`
	HTMLURL     string                  `json:"html_url"`
	RepoURL     string                  `json:"repository_url"`
	CreatedAt   time.Time               `json:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
	ClosedAt    *time.Time              `json:"closed_at"`
	Comments    int                     `json:"comments"`
	Draft       bool                    `json:"draft"`
	Labels      []struct{ Name string } `json:"labels"`
	Assignees   []user                  `json:"assignees"`
	PullRequest *struct {
		MergedAt *time.Time `json:"merged_at"`
	} `json:"pull_request"`
}

// repo is "owner/name" of a search result (its repository_url ends with it).
func (it issue) repo() string {
	parts := strings.Split(it.RepoURL, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

func (it issue) state() string {
	switch {
	case it.PullRequest != nil && it.PullRequest.MergedAt != nil:
		return "merged"
	case it.Draft && it.State == "open":
		return "draft"
	}
	return it.State
}

// codeHit is one file a code search found.
type codeHit struct {
	Path       string `json:"path"`
	HTMLURL    string `json:"html_url"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// searchAnswer is github_search's: issues and pull requests, or code.
type searchAnswer struct {
	code   bool
	total  int
	issues []issue
	hits   []codeHit
}

func (c *Connector) search(ctx context.Context, query, kind string, limit int) (searchAnswer, error) {
	if strings.TrimSpace(query) == "" {
		return searchAnswer{}, fmt.Errorf("query is required")
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	q := url.Values{"q": {query}, "per_page": {fmt.Sprint(limit)}}
	if kind == "code" {
		var res struct {
			Total int       `json:"total_count"`
			Items []codeHit `json:"items"`
		}
		if _, err := c.Get(ctx, "/search/code?"+q.Encode(), &res); err != nil {
			return searchAnswer{}, err
		}
		return searchAnswer{code: true, total: res.Total, hits: res.Items}, nil
	}
	var res struct {
		Total int     `json:"total_count"`
		Items []issue `json:"items"`
	}
	if _, err := c.Get(ctx, "/search/issues?"+q.Encode()+"&sort=updated", &res); err != nil {
		return searchAnswer{}, err
	}
	return searchAnswer{total: res.Total, issues: res.Items}, nil
}

func (a searchAnswer) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d results", a.total)
	if a.code {
		for _, it := range a.hits {
			fmt.Fprintf(&b, "\n%s %s %s", it.Repository.FullName, it.Path, it.HTMLURL)
		}
		return b.String()
	}
	if a.total > len(a.issues) {
		fmt.Fprintf(&b, " (the %d most recently updated)", len(a.issues))
	}
	for _, it := range a.issues {
		what := "issue"
		if it.PullRequest != nil {
			what = "PR"
		}
		fmt.Fprintf(&b, "\n%s#%d [%s %s] %s — %s, updated %s, %d comments %s", it.repo(), it.Number, what, it.state(),
			it.Title, it.User, day(it.UpdatedAt), it.Comments, it.HTMLURL)
	}
	return b.String()
}

type comment struct {
	User      user      `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	// reviews
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// remark is a comment or a review as github_read lists it, with its id and link.
type remark struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	comment
	review bool
}

// at is when it was written (a review: submitted).
func (n remark) at() time.Time {
	if n.review {
		return n.SubmittedAt
	}
	return n.CreatedAt
}

// reviewVerbs are the review states github_read lists, as its text says them.
var reviewVerbs = map[string]string{"APPROVED": "approved", "CHANGES_REQUESTED": "requested changes", "COMMENTED": "reviewed",
	"DISMISSED": "review dismissed"}

// pullInfo is what a pull request adds to its issue.
type pullInfo struct {
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
	ChangedFiles   int    `json:"changed_files"`
	Reviewers      []user `json:"requested_reviewers"`
	Teams          []struct {
		Slug string `json:"slug"`
	} `json:"requested_teams"`
}

// readAnswer is github_read's: the issue or pull request, and with comments > 0 its newest comments and reviews.
type readAnswer struct {
	repo     string
	number   int
	it       issue
	pr       *pullInfo
	comments int      // how many were asked for (0: none were read)
	notes    []remark // newest first, at most comments
	total    int      // the comments on GitHub and the reviews listed
}

func (c *Connector) read(ctx context.Context, repo string, number, comments int) (readAnswer, error) {
	a := readAnswer{repo: repo, number: number, comments: comments}
	base := fmt.Sprintf("/repos/%s/issues/%d", repo, number)
	if _, err := c.Get(ctx, base, &a.it); err != nil {
		return readAnswer{}, err
	}
	var reviews []remark
	if a.it.PullRequest != nil {
		a.pr = &pullInfo{}
		if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), a.pr); err != nil {
			return readAnswer{}, err
		}
		if comments > 0 {
			if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, number), &reviews); err != nil {
				return readAnswer{}, err
			}
		}
	}
	if comments == 0 {
		a.total = a.it.Comments
		return a, nil
	}

	// The newest comments: ask for the last page's worth.
	var cs []remark
	page := max(1, (a.it.Comments+99)/100)
	if _, err := c.Get(ctx, fmt.Sprintf("%s/comments?per_page=100&page=%d", base, page), &cs); err != nil {
		return readAnswer{}, err
	}
	all := cs
	for _, r := range reviews {
		if reviewVerbs[r.State] == "" || (r.State == "COMMENTED" && strings.TrimSpace(r.Body) == "") {
			continue
		}
		r.review = true
		all = append(all, r)
	}
	// newest first
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].at().After(all[j-1].at()); j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	a.total = a.it.Comments + len(all) - len(cs)
	if len(all) > comments {
		all = all[:comments]
	}
	a.notes = all
	return a, nil
}

func (a readAnswer) text() string {
	it := a.it
	var b strings.Builder
	what := "Issue"
	if it.PullRequest != nil {
		what = "Pull request"
	}
	fmt.Fprintf(&b, "%s %s#%d: %s\nState: %s · by %s · opened %s · updated %s\n%s\n", what, a.repo, a.number, it.Title, it.state(),
		it.User, day(it.CreatedAt), day(it.UpdatedAt), it.HTMLURL)
	if len(it.Labels) > 0 {
		names := make([]string, len(it.Labels))
		for i, l := range it.Labels {
			names[i] = l.Name
		}
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(names, ", "))
	}
	if len(it.Assignees) > 0 {
		fmt.Fprintf(&b, "Assignees: %s\n", logins(it.Assignees))
	}
	if pr := a.pr; pr != nil {
		fmt.Fprintf(&b, "Branch: %s → %s (head %s) · %d files, +%d −%d", pr.Head.Ref, pr.Base.Ref, short(pr.Head.SHA),
			pr.ChangedFiles, pr.Additions, pr.Deletions)
		if pr.MergeableState != "" {
			fmt.Fprintf(&b, " · mergeable: %s", pr.MergeableState)
		}
		b.WriteString("\n")
		requested := logins(pr.Reviewers)
		for _, t := range pr.Teams {
			requested = strings.TrimPrefix(requested+", team "+t.Slug, ", ")
		}
		if requested != "" {
			fmt.Fprintf(&b, "Review requested from: %s\n", requested)
		}
	}
	fmt.Fprintf(&b, "\n%s\n", cut(strings.TrimSpace(it.Body), bodyCap, "(no description)"))
	if a.comments == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "\n--- %d comments and reviews, newest first", a.total)
	if a.total > len(a.notes) {
		fmt.Fprintf(&b, " (the newest %d)", len(a.notes))
	}
	for _, n := range a.notes {
		var text string
		if n.review {
			text = fmt.Sprintf("%s %s %s", day(n.SubmittedAt), who(n.User), reviewVerbs[n.State])
			if body := strings.TrimSpace(n.Body); body != "" {
				text += ":\n" + cut(body, commentCap, "")
			}
		} else {
			text = fmt.Sprintf("%s %s:\n%s", day(n.CreatedAt), who(n.User), cut(strings.TrimSpace(n.Body), commentCap, ""))
		}
		fmt.Fprintf(&b, "\n\n%s", text)
	}
	return b.String()
}

// diffFile is one file a pull request changes.
type diffFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
	Previous  string `json:"previous_filename"`
}

// diffAnswer is github_diff's: the files a pull request changes, or the one file asked for with its patch.
type diffAnswer struct {
	repo   string
	number int
	files  []diffFile
	one    *diffFile
}

func (c *Connector) diff(ctx context.Context, repo string, number int, path string) (diffAnswer, error) {
	var files []diffFile
	for page := 1; page <= 10; page++ { // GitHub lists at most 3000 files
		var batch []diffFile
		if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", repo, number, page), &batch); err != nil {
			return diffAnswer{}, err
		}
		files = append(files, batch...)
		if len(batch) < 100 || path != "" && containsFile(batch, path, func(f diffFile) string { return f.Filename }) {
			break
		}
	}
	a := diffAnswer{repo: repo, number: number, files: files}
	if path != "" {
		for i, f := range files {
			if f.Filename == path {
				a.one = &files[i]
				return a, nil
			}
		}
		return diffAnswer{}, fmt.Errorf("%s is not among the files %s#%d changes", path, repo, number)
	}
	return a, nil
}

func (a diffAnswer) text() string {
	if f := a.one; f != nil {
		head := fmt.Sprintf("%s (%s, +%d −%d)\n", f.Filename, f.Status, f.Additions, f.Deletions)
		if f.Patch == "" {
			return head + "(no patch: binary, or too big for GitHub to show)"
		}
		return head + cut(f.Patch, patchCap, "")
	}
	var b strings.Builder
	adds, dels := 0, 0
	for _, f := range a.files {
		adds, dels = adds+f.Additions, dels+f.Deletions
	}
	fmt.Fprintf(&b, "%d files changed, +%d −%d", len(a.files), adds, dels)
	for _, f := range a.files {
		name := f.Filename
		if f.Previous != "" {
			name = f.Previous + " → " + f.Filename
		}
		fmt.Fprintf(&b, "\n%s %s +%d −%d", f.Status, name, f.Additions, f.Deletions)
	}
	return b.String()
}

func containsFile[T any](list []T, path string, name func(T) string) bool {
	for _, f := range list {
		if name(f) == path {
			return true
		}
	}
	return false
}

// checkRun is one check of a commit.
type checkRun struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	HTMLURL     string     `json:"html_url"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	App         struct {
		Slug string `json:"slug"`
	} `json:"app"`
	Output struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	} `json:"output"`
}

func (r checkRun) state() string {
	if r.Status == "completed" {
		return r.Conclusion
	}
	return r.Status
}

func (r checkRun) failed() bool { return r.Conclusion == "failure" || r.Conclusion == "timed_out" }

// jobLog is the end of a failed Actions job's log, or why it could not be read.
type jobLog struct {
	tail string
	err  error
}

// checksAnswer is github_checks': the checks of one commit, with the end of the log of the first failed Actions jobs.
type checksAnswer struct {
	repo  string
	ref   string
	total int
	runs  []checkRun
	logs  map[int]jobLog // by index in runs
}

func (c *Connector) checks(ctx context.Context, repo string, number int, ref string) (checksAnswer, error) {
	if ref == "" {
		if number == 0 {
			return checksAnswer{}, fmt.Errorf("number or ref is required")
		}
		var pr struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}
		if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), &pr); err != nil {
			return checksAnswer{}, err
		}
		ref = pr.Head.SHA
	}
	var res struct {
		Total int        `json:"total_count"`
		Runs  []checkRun `json:"check_runs"`
	}
	if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repo, url.PathEscape(ref)), &res); err != nil {
		return checksAnswer{}, err
	}
	a := checksAnswer{repo: repo, ref: ref, total: res.Total, runs: res.Runs, logs: map[int]jobLog{}}
	for i, r := range res.Runs {
		if !r.failed() || len(a.logs) >= failedLogs || r.App.Slug != "github-actions" {
			continue
		}
		// An Actions check run's id is its job's id; the log comes as a redirect to a short-lived URL (no token
		// goes there: Go drops Authorization on a redirect to another host).
		log, err := c.raw(ctx, fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, r.ID))
		if err != nil {
			a.logs[i] = jobLog{err: err}
			continue
		}
		a.logs[i] = jobLog{tail: tail(log, logTail)}
	}
	return a, nil
}

func (a checksAnswer) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d checks on %s", a.total, short(a.ref))
	for i, r := range a.runs {
		fmt.Fprintf(&b, "\n%s: %s %s", r.Name, r.state(), r.HTMLURL)
		if !r.failed() {
			continue
		}
		if t := strings.TrimSpace(r.Output.Title); t != "" {
			fmt.Fprintf(&b, "\n  %s", t)
		}
		l, read := a.logs[i]
		switch {
		case !read:
		case l.err != nil:
			fmt.Fprintf(&b, "\n  (log not readable: %v)", l.err)
		default:
			fmt.Fprintf(&b, "\n  --- the end of the log:\n%s", l.tail)
		}
	}
	return b.String()
}

func logins(us []user) string {
	names := make([]string, len(us))
	for i, u := range us {
		names[i] = u.String()
	}
	return strings.Join(names, ", ")
}

// who is a commenter, with bots marked.
func who(u user) string {
	if u.Type == "Bot" || strings.HasSuffix(u.Login, "[bot]") {
		return u.String() + " (bot)"
	}
	return u.String()
}

func day(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// cut keeps the first n bytes of s (at a rune boundary), saying how much is left out; empty s is none.
func cut(s string, n int, none string) string {
	if s == "" {
		return none
	}
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + fmt.Sprintf("\n[… %d more characters cut]", utf8.RuneCountInString(s[i:]))
}

// tail keeps the last n bytes of s, from a line start.
func tail(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return "[…]\n" + s
}
