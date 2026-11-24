package github

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The read tools' answers as JSON, for source subscriptions (connectorwire.ReadJSON, docs/extensions/subscriptions.md
// "Existing connectors"): an object with one top-level list, whose items carry the names a recipe maps (id, thread,
// title, author, time, body, link). The models read the text (tools.go); these are the same answers.

// excerptCap is how much of an issue's or pull request's description a search result carries.
const excerptCap = 1000

type issueJSON struct {
	ID        string   `json:"id"` // owner/name#number
	Repo      string   `json:"repo"`
	Number    int      `json:"number"`
	Type      string   `json:"type"`  // issue | pr
	State     string   `json:"state"` // open | closed | draft | merged
	Title     string   `json:"title"`
	Author    string   `json:"author"`
	Time      string   `json:"time"` // last updated
	Opened    string   `json:"opened,omitempty"`
	Closed    string   `json:"closed,omitempty"`
	Comments  int      `json:"comments"`
	Labels    []string `json:"labels,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	Link      string   `json:"link"`
	Body      string   `json:"body,omitempty"`
}

func issueData(it issue, repo string, body int) issueJSON {
	out := issueJSON{ID: repo + "#" + strconv.Itoa(it.Number), Repo: repo, Number: it.Number, Type: "issue", State: it.state(),
		Title: it.Title, Author: it.User.Login, Time: rfc3339(it.UpdatedAt), Opened: rfc3339(it.CreatedAt), Comments: it.Comments,
		Link: it.HTMLURL, Body: excerpt(it.Body, body)}
	if it.PullRequest != nil {
		out.Type = "pr"
	}
	if it.ClosedAt != nil {
		out.Closed = rfc3339(*it.ClosedAt)
	}
	for _, l := range it.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	for _, u := range it.Assignees {
		out.Assignees = append(out.Assignees, u.Login)
	}
	return out
}

type codeJSON struct {
	ID    string `json:"id"` // owner/name:path
	Repo  string `json:"repo"`
	Path  string `json:"path"`
	Title string `json:"title"`
	Link  string `json:"link"`
}

type searchJSON struct {
	Kind  string `json:"kind"` // issues | code
	Total int    `json:"total"`
	More  bool   `json:"more"` // there are more than items
	Items any    `json:"items"`
}

func (a searchAnswer) data() any {
	if a.code {
		items := make([]codeJSON, 0, len(a.hits))
		for _, h := range a.hits {
			repo := h.Repository.FullName
			items = append(items, codeJSON{ID: repo + ":" + h.Path, Repo: repo, Path: h.Path, Title: h.Path, Link: h.HTMLURL})
		}
		return searchJSON{Kind: "code", Total: a.total, More: a.total > len(items), Items: items}
	}
	items := make([]issueJSON, 0, len(a.issues))
	for _, it := range a.issues {
		items = append(items, issueData(it, it.repo(), excerptCap))
	}
	return searchJSON{Kind: "issues", Total: a.total, More: a.total > len(items), Items: items}
}

type pullJSON struct {
	Branch          string   `json:"branch"`
	Base            string   `json:"base"`
	Head            string   `json:"head"` // the head commit's SHA
	Files           int      `json:"files"`
	Additions       int      `json:"additions"`
	Deletions       int      `json:"deletions"`
	Mergeable       string   `json:"mergeable,omitempty"`
	ReviewRequested []string `json:"review_requested,omitempty"` // logins
	ReviewTeams     []string `json:"review_teams,omitempty"`
}

type noteJSON struct {
	ID     string `json:"id"`     // comment:<id> | review:<id>
	Thread string `json:"thread"` // owner/name#number
	Kind   string `json:"kind"`   // comment | review
	State  string `json:"state,omitempty"`
	Title  string `json:"title"` // the issue's or pull request's
	Author string `json:"author"`
	Bot    bool   `json:"bot,omitempty"`
	Time   string `json:"time"`
	Body   string `json:"body,omitempty"`
	Link   string `json:"link,omitempty"`
}

// readJSON is the issue or pull request, with its newest comments and reviews as the list.
type readJSON struct {
	Issue    issueJSON  `json:"issue"`
	Pull     *pullJSON  `json:"pull,omitempty"`
	Total    int        `json:"total"` // comments and reviews
	More     bool       `json:"more"`
	Comments []noteJSON `json:"comments"`
}

func (a readAnswer) data() any {
	out := readJSON{Issue: issueData(a.it, a.repo, bodyCap), Total: a.total, More: a.total > len(a.notes), Comments: []noteJSON{}}
	if a.comments == 0 {
		out.More = false
	}
	if pr := a.pr; pr != nil {
		p := &pullJSON{Branch: pr.Head.Ref, Base: pr.Base.Ref, Head: pr.Head.SHA, Files: pr.ChangedFiles, Additions: pr.Additions,
			Deletions: pr.Deletions, Mergeable: pr.MergeableState}
		for _, u := range pr.Reviewers {
			p.ReviewRequested = append(p.ReviewRequested, u.Login)
		}
		for _, t := range pr.Teams {
			p.ReviewTeams = append(p.ReviewTeams, t.Slug)
		}
		out.Pull = p
	}
	for _, n := range a.notes {
		j := noteJSON{Thread: out.Issue.ID, Kind: "comment", Title: a.it.Title, Author: n.User.Login, Bot: isBot(n.User),
			Time: rfc3339(n.at()), Body: excerpt(n.Body, commentCap), Link: n.HTMLURL}
		if n.review {
			j.Kind, j.State = "review", strings.ToLower(n.State)
		}
		if n.ID != 0 {
			j.ID = j.Kind + ":" + strconv.FormatInt(n.ID, 10)
		}
		out.Comments = append(out.Comments, j)
	}
	return out
}

type fileJSON struct {
	ID        string `json:"id"` // its path
	Title     string `json:"title"`
	Path      string `json:"path"`
	Previous  string `json:"previous,omitempty"` // a renamed file's old path
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

func fileData(f diffFile) fileJSON {
	return fileJSON{ID: f.Filename, Title: f.Filename, Path: f.Filename, Previous: f.Previous, Status: f.Status,
		Additions: f.Additions, Deletions: f.Deletions}
}

type diffJSON struct {
	Repo      string     `json:"repo"`
	Number    int        `json:"number"`
	Changed   int        `json:"changed"`
	Additions int        `json:"additions"`
	Deletions int        `json:"deletions"`
	Files     []fileJSON `json:"files"`
}

// patchJSON is the one file asked for: no list, its patch.
type patchJSON struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	fileJSON
	Patch   string `json:"patch,omitempty"`
	NoPatch bool   `json:"no_patch,omitempty"` // binary, or too big for GitHub to show
}

func (a diffAnswer) data() any {
	if f := a.one; f != nil {
		return patchJSON{Repo: a.repo, Number: a.number, fileJSON: fileData(*f), Patch: excerpt(f.Patch, patchCap), NoPatch: f.Patch == ""}
	}
	out := diffJSON{Repo: a.repo, Number: a.number, Changed: len(a.files), Files: make([]fileJSON, 0, len(a.files))}
	for _, f := range a.files {
		out.Additions, out.Deletions = out.Additions+f.Additions, out.Deletions+f.Deletions
		out.Files = append(out.Files, fileData(f))
	}
	return out
}

type checkJSON struct {
	ID         string `json:"id"` // the check run's id
	Title      string `json:"title"`
	App        string `json:"app,omitempty"`
	State      string `json:"state"` // the conclusion once completed, else the status
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	Time       string `json:"time,omitempty"` // completed, else started
	Link       string `json:"link,omitempty"`
	Summary    string `json:"summary,omitempty"` // its output's title
	Body       string `json:"body,omitempty"`    // a failed one's output title, then the end of its job's log when read
}

type checksJSON struct {
	Repo   string      `json:"repo"`
	Ref    string      `json:"ref"`
	Total  int         `json:"total"`
	Checks []checkJSON `json:"checks"`
}

func (a checksAnswer) data() any {
	out := checksJSON{Repo: a.repo, Ref: a.ref, Total: a.total, Checks: make([]checkJSON, 0, len(a.runs))}
	for i, r := range a.runs {
		j := checkJSON{Title: r.Name, App: r.App.Slug, State: r.state(), Status: r.Status, Conclusion: r.Conclusion, Link: r.HTMLURL,
			Summary: strings.TrimSpace(r.Output.Title)}
		if r.ID != 0 {
			j.ID = strconv.FormatInt(r.ID, 10)
		}
		switch {
		case r.CompletedAt != nil:
			j.Time = rfc3339(*r.CompletedAt)
		case r.StartedAt != nil:
			j.Time = rfc3339(*r.StartedAt)
		}
		if r.failed() {
			j.Body = j.Summary
			if l, ok := a.logs[i]; ok && l.err == nil {
				j.Body = strings.TrimSpace(j.Body + "\n\n" + l.tail)
			}
		}
		out.Checks = append(out.Checks, j)
	}
	return out
}

// rfc3339 is t in RFC 3339, UTC; "" for none.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// excerpt is the first n bytes of s trimmed (at a rune boundary), with "…" when more was left out.
func excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return strings.TrimSpace(s[:i]) + "…"
}
