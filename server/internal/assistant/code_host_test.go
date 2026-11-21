package assistant

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repository"
)

// mergeRequestProvider is Acme Code with merge requests under /merge_requests/N, whose describe says state (""
// none) or fails with describeErr.
type mergeRequestProvider struct {
	*acmeProvider
	mu          sync.Mutex
	state       string
	describeErr error
}

func (f *mergeRequestProvider) RepositoryLink(_ context.Context, repo repository.Identity, in repository.LinkInput) (repository.TaskLink, error) {
	f.call("link")
	rest := strings.Split(strings.TrimPrefix(in.URL, repo.CanonicalURL+"/"), "/")
	if len(rest) != 2 || rest[0] != "merge_requests" && rest[0] != "issues" {
		return repository.TaskLink{}, &repository.ProviderError{Code: "not_found", Provider: "Acme Code"}
	}
	kind := map[string]string{"merge_requests": "pull_request", "issues": "issue"}[rest[0]]
	return repository.CheckLinkAnswer(repo, in, repository.LinkAnswer{Kind: kind, Number: rest[1], CanonicalURL: in.URL})
}

func (f *mergeRequestProvider) RepositoryDescribe(_ context.Context, _, canonicalURL string) (repository.Description, error) {
	f.call("describe")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.describeErr != nil {
		return repository.Description{}, f.describeErr
	}
	return repository.CheckDescribeAnswer("Review", f.state), nil
}

const acmeMR = "https://code.acme.test/team/app/merge_requests/3"

// codeHostReviewService is a supervised task with done_needs_delivery on whose delivery is pr; the Acme Code
// provider is installed when p is not nil.
func codeHostReviewService(t *testing.T, p *mergeRequestProvider, pr string) (*Service, Todo) {
	t.Helper()
	s, _, todo := newSupervisedService(t)
	s.deploy.forge = &fakeForge{} // GitHub must not be asked about another host
	if p != nil {
		s.repoExt = p
	}
	deliveryOn(t, s)
	s.setTaskStage(context.Background(), todo.ID, "review", pr)
	if got := todoNow(s, todo.ID); got.PRURL != pr {
		t.Fatalf("delivery not recorded: %q", got.PRURL)
	}
	return s, todo
}

// #11584: a review request on another host is read by that host's code host. The provider's state holds accepted while
// the request is open and lets it through once merged; a host no one reads, a provider that is off, cannot say its
// state or fails, never holds it, and the reply says the delivery was not checked.
func TestAcceptedReadsOtherCodeHosts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider bool
		state    string
		off      bool
		fail     error
		pr       string
		refuse   string
		note     string // in the reply: "" none
	}{
		{name: "open on a provider", provider: true, state: "open", pr: acmeMR, refuse: "not merged"},
		{name: "closed on a provider", provider: true, state: "closed", pr: acmeMR, refuse: "not merged"},
		{name: "merged on a provider", provider: true, state: "merged", pr: acmeMR},
		{name: "provider without states", provider: true, pr: acmeMR, note: "code.acme.test"},
		{name: "provider off", provider: true, state: "open", off: true, pr: acmeMR, note: "provider_off"},
		{name: "provider fails", provider: true, state: "open", fail: &repository.ProviderError{Code: "not_signed_in", Provider: "Acme Code"}, pr: acmeMR, note: "not_signed_in"},
		{name: "host no one reads", pr: "https://git.example.test/team/app/merge_requests/3", note: "git.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p *mergeRequestProvider
			if tc.provider {
				p = &mergeRequestProvider{acmeProvider: newAcmeProvider(), state: tc.state, describeErr: tc.fail}
			}
			s, todo := codeHostReviewService(t, p, tc.pr)
			if tc.off {
				p.set(true, repository.ProviderOff)
			}
			res, err := acceptReview(s, todo.ID, nil)
			got := todoNow(s, todo.ID)
			if tc.refuse != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refuse) || !strings.Contains(err.Error(), tc.pr) {
					t.Fatalf("accepted (%v): %s", err, res)
				}
				if got.DoneAt != nil {
					t.Fatalf("closed at %v", got.DoneAt)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if got.DoneAt == nil {
				t.Fatalf("not closed: %s", res)
			}
			if tc.note == "" && strings.Contains(res, tc.pr) || tc.note != "" && (!strings.Contains(res, tc.pr) || !strings.Contains(res, tc.note)) {
				t.Fatalf("reply %q, want a note with %q", res, tc.note)
			}
		})
	}
}

// The review's own link may name the other host's request, as it may a GitHub PR.
func TestAcceptedLinkNamesOtherHostRequest(t *testing.T) {
	p := &mergeRequestProvider{acmeProvider: newAcmeProvider(), state: "open"}
	s, todo := codeHostReviewService(t, p, acmeMR)
	if _, err := acceptReview(s, todo.ID, map[string]any{"link": acmeMR}); err == nil || !strings.Contains(err.Error(), "not merged") {
		t.Fatalf("an open request named by the review's link was accepted: %v", err)
	}
}

// codeHostFor routes by host: GitHub pull requests and gitlab.com to the built-ins, claimed hosts to their provider, the
// rest to none.
func TestCodeHostRouting(t *testing.T) {
	s := &Service{}
	s.deploy.forge = &fakeForge{}
	s.repoExt = &mergeRequestProvider{acmeProvider: newAcmeProvider()}
	for url, want := range map[string]string{
		"https://github.com/o/r/pull/9": "GitHub",
		acmeMR:                          "Acme Code",
		"https://gitlab.com/g/r/-/merge_requests/1":         "GitLab",
		"https://github.com/o/r/issues/9":                   "",
		"https://elsewhere.example.test/g/r/merge_requests": "",
	} {
		h, _ := s.codeHostFor(url)
		name := ""
		if h != nil {
			name = h.Name()
		}
		if name != want {
			t.Errorf("%s: %q, want %q", url, name, want)
		}
	}
}

// prStatusProvider is mergeRequestProvider declaring pr_status: repo.pr_status says prState, repo.describe the state.
type prStatusProvider struct {
	*mergeRequestProvider
	prState string
}

func (f *prStatusProvider) RepositoryProviderForHost(host string) (repository.ProviderInfo, bool) {
	info, ok := f.mergeRequestProvider.RepositoryProviderForHost(host)
	info.Capabilities = []string{"pr_status"}
	return info, ok
}

func (f *prStatusProvider) RepositoryPRStatus(_ context.Context, provider, canonicalURL string) (repository.ReviewStatus, error) {
	f.call("pr_status")
	if provider != acmeProviderID || canonicalURL != acmeMR {
		return repository.ReviewStatus{}, &repository.ProviderError{Code: "not_found", Provider: "Acme Code"}
	}
	return repository.ReviewStatus{State: f.prState}, nil
}

// #11584: a provider declaring pr_status is read with repo.pr_status, not repo.describe.
func TestAcceptedReadsProviderPRStatus(t *testing.T) {
	for _, tc := range []struct{ describe, prState, refuse string }{
		{describe: "open", prState: "merged"},
		{describe: "merged", prState: "open", refuse: "not merged"},
	} {
		p := &prStatusProvider{mergeRequestProvider: &mergeRequestProvider{acmeProvider: newAcmeProvider(), state: tc.describe}, prState: tc.prState}
		s, todo := codeHostReviewService(t, nil, acmeMR)
		s.repoExt = p
		p.asked()
		_, err := acceptReview(s, todo.ID, nil)
		if tc.refuse == "" && err != nil || tc.refuse != "" && (err == nil || !strings.Contains(err.Error(), tc.refuse)) {
			t.Fatalf("pr_status %s, describe %s: %v", tc.prState, tc.describe, err)
		}
		if asked := p.asked(); !slices.Contains(asked, "pr_status") || slices.Contains(asked, "describe") {
			t.Fatalf("asked %v", asked)
		}
	}
}
