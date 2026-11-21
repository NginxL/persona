package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
)

// fakeGitLab is the GitLab API as GitLabReview reads it: one state for every merge request, the reads it was asked.
type fakeGitLab struct {
	mu     sync.Mutex
	state  string
	err    error
	asked  []string // instance path!iid token
	tokens []string
}

func (f *fakeGitLab) read(_ context.Context, id repository.Identity, link repository.TaskLink, token string) (repository.ReviewStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, id.InstanceURL+" "+id.Path+"!"+link.Number)
	f.tokens = append(f.tokens, token)
	if f.err != nil {
		return repository.ReviewStatus{}, f.err
	}
	return repository.ReviewStatus{State: f.state}, nil
}

func (f *fakeGitLab) set(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = state
}

// gitlabConfig writes a manager.json naming instance with a token file holding token, for s.
func gitlabConfig(t *testing.T, s *Service, instance, token string) {
	t.Helper()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "gitlab.token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.repositoryConfig = filepath.Join(dir, "manager.json")
	body := `{"gitlab":{"instances":[{"instance_url":"` + instance + `","token_file":"` + tokenFile + `"}]}}`
	if err := os.WriteFile(s.repositoryConfig, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// #11584: built-in GitLab reads merge requests on gitlab.com and on the instances manager.json names, with that
// instance's token; an instance it does not name is not read.
func TestAcceptedReadsGitLabMergeRequests(t *testing.T) {
	const selfHosted = "https://gitlab.example.test/team/app/-/merge_requests/7"
	for _, tc := range []struct {
		name, pr, state string
		configured      bool
		refuse, note    string
		read            string // what GitLab was asked, "" nothing
		token           string
	}{
		{name: "open on gitlab.com", pr: "https://gitlab.com/team/app/-/merge_requests/3", state: "open", refuse: "not merged", read: "https://gitlab.com team/app!3"},
		{name: "merged on gitlab.com", pr: "https://gitlab.com/team/app/-/merge_requests/3", state: "merged", read: "https://gitlab.com team/app!3"},
		{name: "closed on a named instance", pr: selfHosted, state: "closed", configured: true, refuse: "not merged", read: "https://gitlab.example.test team/app!7", token: "read-api-token"},
		{name: "merged on a named instance", pr: selfHosted, state: "merged", configured: true, read: "https://gitlab.example.test team/app!7", token: "read-api-token"},
		{name: "an instance not named", pr: selfHosted, state: "open", note: "gitlab.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, todo := codeHostReviewService(t, nil, tc.pr)
			gl := &fakeGitLab{state: tc.state}
			s.gitlabReview = gl.read
			if tc.configured {
				gitlabConfig(t, s, "https://gitlab.example.test", "read-api-token")
			}
			res, err := acceptReview(s, todo.ID, nil)
			if tc.refuse != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refuse) {
					t.Fatalf("accepted (%v): %s", err, res)
				}
			} else if err != nil {
				t.Fatalf("refused: %v", err)
			} else if tc.note != "" && !strings.Contains(res, tc.note) {
				t.Fatalf("reply %q has no note about %s", res, tc.note)
			}
			if tc.read == "" && len(gl.asked) != 0 || tc.read != "" && (len(gl.asked) != 1 || gl.asked[0] != tc.read || gl.tokens[0] != tc.token) {
				t.Fatalf("GitLab asked %q with %q, want %q with %q", gl.asked, gl.tokens, tc.read, tc.token)
			}
		})
	}
}

// The status sweep polls a GitLab merge request too: once merged it wakes the supervisor its merge wait parked.
func TestPRStatusPollsGitLabMergeRequests(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx := context.Background()
	url := "https://gitlab.com/example/app/-/merge_requests/12"
	gl := &fakeGitLab{state: "open"}
	s.gitlabReview = gl.read
	s.setTaskStage(ctx, task.ID, "review", url)
	if _, err := reviewErr(s, task.ID, mergePark(url)); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.sweepPRStatuses(ctx, now)
	got := todoNow(s, task.ID)
	if got.PRStatus == nil || got.PRStatus.State != "open" || got.Supervision.State != reviewParked {
		t.Fatalf("open merge request: %+v", got.PRStatus)
	}
	gl.set("merged")
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))
	got = todoNow(s, task.ID)
	if got.DoneAt != nil || got.PRStatus.State != "merged" || got.Supervision.State != reviewPending || got.Stage != "merge" {
		t.Fatalf("merged merge request did not return to review: %+v, %+v", got.PRStatus, got.Supervision)
	}
}
