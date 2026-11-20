package node

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Resuming a conversation where its launcher is signed in (#11694).
//
// A conversation resumes where its newest transcript copy is (assistantProfileSelection), so a switch to another
// launcher starts that launcher in the profile the conversation ran in before. A shell launcher (a ~/.zshrc function
// that sets a gateway and its token in the environment) needs no sign-in in its profile, and conversations started on
// it are kept in Persona's private profile of it (<data dir>/profiles/<launcher>). Plain claude signed in with a Claude
// subscription keeps that sign-in in a profile: started in the shell launcher's, it answered "Not logged in" on every
// turn, while `claude auth status` in the user's own setup said signed in, so the card's `claude auth login` changed
// nothing. A managed connection's profile refuses such a switch (#10453); for any other launcher's private profile, the
// conversation goes where the new launcher is signed in:
//
//   - only when Claude Code says the launcher is signed out of the profile (`auth status` run as a start runs it,
//     claudeAuthStatusProfile). Signed in there (a gateway's token in its environment), or not known (an old CLI, a
//     timeout), keeps the resume where it is, as before;
//   - the profile it goes to is the first one Claude Code says the launcher is signed in to: Persona's own profile of
//     the launcher (where its new conversations start by default), then the user's own setup (legacyAssistantProfile:
//     claude_start_dir, the service's or the login shell's CLAUDE_CONFIG_DIR, else ~/.claude). With none, the resume
//     stays and its turn reports the sign-in as before;
//   - the newest transcript and its directory (subagents, tool results) are copied there, and the conversation resumes
//     from the copy. The copy where it was stays: the old launcher, picked again, resumes the newest copy as any
//     launcher switch does. Not while a live process has the conversation (it runs on this launcher, or works: its
//     message goes to that process, assistant.go), so the copy is never made of a transcript still being written.
//
// The answers are kept per launcher and profile as the hub keeps claude.auth_status (signed in 10 minutes, anything
// else a minute), so a conversation resumed on a gateway launcher in another's profile asks once, not every message.

const (
	resumeSignedInKeep = 10 * time.Minute
	resumeOtherKeep    = time.Minute
)

// resumeSignInCache: the sign-in state of a launcher in a profile, as last asked (profileSignIn).
type resumeSignInCache struct {
	mu   sync.Mutex
	seen map[[2]string]resumeSignInAnswer
}

type resumeSignInAnswer struct {
	state string
	at    time.Time
}

// signedInResume is the profile conversation id resumes in on launcher (one that sets no profile of its own), given
// selected, the profile assistantProfileSelection resolved (normalized): selected, or the profile it moved to above.
func (n *Node) signedInResume(ctx context.Context, id, launcher, selected string) string {
	if n.sessionScope != nil || selected == "default" {
		return selected
	}
	if _, managed := n.managedLauncher(launcher); managed {
		return selected
	}
	profile := n.assistantConfigPath(selected)
	owner := filepath.Base(profile)
	if owner == launcher || !n.privateProfile(profile) || !sameScopePath(profile, filepath.Join(n.assistantProfilesRoot(), owner)) {
		return selected
	}
	if _, managed := n.managedCatalog(owner); managed {
		return selected // refused by assistantProfileSelection (#10453)
	}
	newest := n.transcriptPath(id)
	if newest == "" || filepath.Base(newest) != id+".jsonl" {
		return selected
	}
	project := filepath.Base(filepath.Dir(newest))
	src := filepath.Join(profile, "projects", project, id+".jsonl")
	if !holdsNewestCopy(src, newest) {
		return selected
	}
	if n.profileSignIn(ctx, launcher, selected) != proto.ClaudeNotLoggedIn {
		return selected
	}
	for _, to := range n.signInCandidates(ctx, launcher, selected) {
		if n.profileSignIn(ctx, launcher, to) != proto.ClaudeLoggedIn {
			continue
		}
		dst := filepath.Join(n.assistantConfigPath(to), "projects", project)
		if err := copyConversation(filepath.Dir(src), dst, id); err != nil {
			n.logf("assistant %s: %s is signed out of %s and signed in to %s, but the transcript could not be copied there: %v",
				short(id), launcher, profile, n.assistantConfigPath(to), err)
			return selected
		}
		n.setPath(id, filepath.Join(dst, id+".jsonl"))
		n.logf("assistant %s: %s is signed out of %s: resumes in %s, where it is signed in (transcript copied)",
			short(id), launcher, profile, n.assistantConfigPath(to))
		return to
	}
	return selected
}

// signInCandidates are the profiles signedInResume tries for launcher, in order, less selected: Persona's own profile
// of the launcher when it exists, then the user's own setup.
func (n *Node) signInCandidates(ctx context.Context, launcher, selected string) []string {
	var out []string
	add := func(dir string) {
		if dir != "default" {
			dir = filepath.Clean(proto.ExpandHome(dir, n.home))
			if !filepath.IsAbs(dir) {
				return
			}
		}
		if sameAssistantProfile(dir, selected) || slices.ContainsFunc(out, func(c string) bool { return sameAssistantProfile(c, dir) }) {
			return
		}
		out = append(out, dir)
	}
	if own := filepath.Join(n.assistantProfilesRoot(), launcher); dirExists(own) {
		add(own)
	}
	if legacy := n.legacyAssistantProfile(ctx, launcher); legacy != "" {
		add(legacy)
	}
	return out
}

// profileSignIn is Claude Code's sign-in state for launcher in profile selected (proto.ClaudeLoggedIn,
// ClaudeNotLoggedIn or ClaudeAuthUnknown), kept for a while (resumeSignedInKeep, resumeOtherKeep).
func (n *Node) profileSignIn(ctx context.Context, launcher, selected string) string {
	key := [2]string{launcher, n.assistantConfigPath(selected)}
	c := &n.resumeSignIn
	c.mu.Lock()
	if a, ok := c.seen[key]; ok {
		keep := resumeOtherKeep
		if a.state == proto.ClaudeLoggedIn {
			keep = resumeSignedInKeep
		}
		if time.Since(a.at) < keep {
			c.mu.Unlock()
			return a.state
		}
	}
	c.mu.Unlock()
	state := proto.ClaudeAuthUnknown
	if st, err := n.claudeAuthStatusProfile(ctx, launcher, selected); err == nil && st.State != "" {
		state = st.State
	}
	c.mu.Lock()
	if c.seen == nil {
		c.seen = map[[2]string]resumeSignInAnswer{}
	}
	c.seen[key] = resumeSignInAnswer{state: state, at: time.Now()}
	c.mu.Unlock()
	return state
}

// copyConversation copies transcript <from>/<id>.jsonl and its directory <from>/<id> (subagents, tool results), when
// there, into project directory to. The transcript is written last and replaced whole (a temporary file renamed), so a
// copy that fails half way leaves no newer transcript there to resume.
func copyConversation(from, to, id string) error {
	if err := os.MkdirAll(to, 0o700); err != nil {
		return err
	}
	dir := filepath.Join(from, id)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(from, path)
			if err != nil {
				return err
			}
			target := filepath.Join(to, rel)
			switch {
			case d.IsDir():
				return os.MkdirAll(target, 0o700)
			case d.Type().IsRegular():
				return copyFile(path, target)
			}
			return nil // a link or special file: not Claude Code's
		})
		if err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	src, err := os.Open(filepath.Join(from, id+".jsonl"))
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(to, "."+id+".jsonl.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // nothing once renamed
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(to, id+".jsonl"))
}
