package assistant

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// The deployed commits (#3205, docs/assistant/todo-status-sop.md "Deployed needs evidence"): a hub that cannot reach
// GitHub still knows what it runs when the deploy puts, next to the binaries, the commits the deployed one contains.
// The file is deployedCommitsName in the hub's state directory or next to the running binary:
//
//	# persona deployed commits v1
//	# repo: <owner>/<name>
//	<full sha> <subject>        the deployed commit first, then its ancestors, newest first (git log --format='%H %s')
//
// It is believed only when its first commit is the one the running hub reports (a file left from another deploy is
// ignored) and only for PRs of its repo. A PR can be identified by its subject, as GitHub writes it: a squash merge's
// "<title> (#N)", a merge commit's "Merge pull request #N from …". Found: deployed. Not found: not in the running hub as
// far as the file goes (it holds the last few thousand commits). Upstream merge status comes from the forge.
//
// A build of a local branch can run a PR before it is merged upstream (#4867): the deploy names such PRs by their head
// commit, "# pr: <N> <full sha>" (scripts/deploy/deployed_commits.py), and the PR counts as deployed when that commit is
// one the file lists, so in the deployed one's history. A head the file does not list is not believed.

const deployedCommitsName = "deployed-commits.txt"

const deployedCommitsHeader = "# persona deployed commits v1"

var (
	squashSubject = lazyre.New(`\(#([0-9]+)\)\s*$`)
	mergeSubject  = lazyre.New(`^Merge pull request #([0-9]+)\b`)
	// prHeadLine is a PR the deployed commit contains by its head ("pr: <N> <sha>", after the "#").
	prHeadLine = lazyre.New(`^pr:\s*#?([0-9]+)\s+([0-9a-fA-F]{40})\s*$`)
)

// deployReceipt identifies the PR's commit in the running build and how the file named it.
type deployReceipt struct {
	sha    string
	byHead bool // the explicit "# pr:" line, rather than a commit subject
}

// deployedList is one deployed-commits file as read.
type deployedList struct {
	repo  string                // owner/name, as the header says
	head  string                // the deployed commit (its first line)
	prs   map[int]deployReceipt // PR number → the newest subject match or the explicit head
	count int
}

// parseDeployedCommits reads a deployed-commits file; nil when it is not one (no header, no repo, no commit).
func parseDeployedCommits(text string) *deployedList {
	l := &deployedList{prs: map[int]deployReceipt{}}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	first := true
	heads := map[int]string{}   // "# pr:" lines: PR number → its head commit, believed once the history lists it
	listed := map[string]bool{} // the commits the file lists
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if first {
			if line != deployedCommitsHeader {
				return nil
			}
			first = false
			continue
		}
		if line == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "#"); ok {
			rest = strings.TrimSpace(rest)
			if v, ok := strings.CutPrefix(rest, "repo:"); ok {
				l.repo = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(v), ".git"))
			} else if m := prHeadLine.FindStringSubmatch(rest); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					heads[n] = strings.ToLower(m[2])
				}
			}
			continue
		}
		sha, subject, _ := strings.Cut(line, " ")
		sha = strings.ToLower(sha)
		if len(sha) != 40 || !isHex(sha) {
			continue
		}
		if l.head == "" {
			l.head = sha
		}
		l.count++
		listed[sha] = true
		m := squashSubject.FindStringSubmatch(subject)
		if m == nil {
			m = mergeSubject.FindStringSubmatch(subject)
		}
		if m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				if _, seen := l.prs[n]; !seen {
					l.prs[n] = deployReceipt{sha: sha}
				}
			}
		}
	}
	if l.repo == "" || l.head == "" {
		return nil
	}
	for n, sha := range heads {
		if _, bySubject := l.prs[n]; !bySubject && listed[sha] {
			l.prs[n] = deployReceipt{sha: sha, byHead: true}
		}
	}
	return l
}

// runs: the file is of the commit the running hub reports (a full one, or the short one of its version).
func (l *deployedList) runs(hub string) bool {
	hub = strings.ToLower(hub)
	return len(hub) >= 7 && strings.HasPrefix(l.head, hub)
}

// deployedFiles is where the deployed-commits file is looked for: the hub's state directory, then next to the binary.
func deployedFiles() []string {
	paths := []string{filepath.Join(identity.HomeDir(), deployedCommitsName)}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		paths = append(paths, filepath.Join(filepath.Dir(exe), deployedCommitsName))
	}
	return paths
}

// deployedFileCache keeps each file as last read, by path, read again when its size or time changes.
type deployedFileCache struct {
	mu    sync.Mutex
	files map[string]deployedFile
}

type deployedFile struct {
	size int64
	mod  time.Time
	list *deployedList
}

// deployedListFor is the deployed-commits file of the running hub's commit hub, nil when there is none.
func (s *Service) deployedListFor(hub string) *deployedList {
	paths := s.deploy.listPaths
	if paths == nil {
		paths = deployedFiles()
	}
	c := &s.deploy.files
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.files == nil {
		c.files = map[string]deployedFile{}
	}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || st.Size() > 8<<20 {
			continue
		}
		f, ok := c.files[p]
		if !ok || f.size != st.Size() || !f.mod.Equal(st.ModTime()) {
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			f = deployedFile{size: st.Size(), mod: st.ModTime(), list: parseDeployedCommits(string(raw))}
			if f.list == nil {
				s.logf("deploys: %s is not a deployed-commits file", p)
			}
			c.files[p] = f
		}
		if f.list != nil && f.list.runs(hub) {
			return f.list
		}
	}
	return nil
}
