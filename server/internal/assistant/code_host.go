package assistant

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistant/repository"
)

// Code hosts (#11584) are where a task's review requests live. The link's host picks one (codeHostFor): built-in
// GitHub for github.com pull requests, otherwise the repository provider that claims the host
// (docs/extensions/repository-providers.md), then built-in GitLab (code_host_gitlab.go). A code host has three
// layers, kept apart:
//
//   - read: ReviewStatus, what the delivery gate asks (supervise_review_delivery.go). GitHub's deploy check
//     (deploy_check.go, prForge.Contains) stays GitHub's own.
//   - events: none are pushed; the minute status sweep (pr_status.go) polls.
//   - writes (comments, reviews, creating and merging requests) are connector tools behind the write gate and the
//     node's git.pr, never a code host's.
//
// A host no code host can read never holds a task: the supervisor's evidence stands, and its reply says so.

// codeHost reads review requests on one host.
type codeHost interface {
	// Name is the host's name for the supervisor (GitHub, a provider's name).
	Name() string
	// ReviewStatus is url's review request as the host says. errReviewUnsupported: the host cannot tell;
	// errNotReviewRequest: url is not a review request there.
	ReviewStatus(ctx context.Context, url string) (repository.ReviewStatus, error)
}

var (
	errReviewUnsupported = errors.New("it cannot read review requests")
	errNotReviewRequest  = errors.New("not a review request")
)

// codeHostFor is the code host that reads url and url's host; nil when none can (an unknown host, a GitHub address
// that is not a pull request, a host neither an installed provider claims nor built-in GitLab reads).
func (s *Service) codeHostFor(url string) (codeHost, string) {
	if ciPRURL.MatchString(url) {
		return githubHost{forge: s.forge()}, "github.com"
	}
	host, ok := repository.RemoteHost(url)
	if !ok {
		return nil, ""
	}
	if s.repoExt != nil && !slices.Contains(repository.BuiltinHosts, host) {
		if info, ok := s.repoExt.RepositoryProviderForHost(host); ok {
			return providerHost{s: s, info: info}, host
		}
	}
	if h, ok := s.gitlabHostFor(host); ok {
		return h, host
	}
	return nil, host
}

// githubHost is built-in GitHub: the deploy check's forge, read the way it always was (one merge read per check).
type githubHost struct{ forge prForge }

func (githubHost) Name() string { return "GitHub" }

func (g githubHost) ReviewStatus(ctx context.Context, url string) (repository.ReviewStatus, error) {
	m := ciPRURL.FindStringSubmatch(url)
	if m == nil {
		return repository.ReviewStatus{}, errNotReviewRequest
	}
	number, err := strconv.Atoi(m[2])
	if err != nil {
		return repository.ReviewStatus{}, errNotReviewRequest
	}
	merged, sha, err := g.forge.PRMerge(ctx, m[1], number)
	if err != nil || !merged {
		return repository.ReviewStatus{}, err
	}
	return repository.ReviewStatus{State: repository.ReviewMerged, MergeCommit: sha}, nil
}

// providerHost is the repository provider claiming a host: which repository and link url is (repo.parse,
// repo.link), then the link's state, within one provider budget: repo.pr_status when its contribution declares
// pr_status, else repo.describe's state.
type providerHost struct {
	s    *Service
	info repository.ProviderInfo
}

func (p providerHost) Name() string { return p.info.Name }

func (p providerHost) ReviewStatus(ctx context.Context, url string) (repository.ReviewStatus, error) {
	if p.info.State != repository.ProviderAvailable {
		return repository.ReviewStatus{}, &repository.ProviderError{Code: "provider_off", Provider: p.info.Name}
	}
	call, cancel := providerCall(withProviderBudget(ctx))
	defer cancel()
	parsed, err := p.s.repositoryRegistry().Parse(call, url, p.info.ID)
	if err != nil {
		return repository.ReviewStatus{}, err
	}
	link, err := p.s.repoExt.RepositoryLink(call, parsed.Identity, repository.LinkInput{URL: url})
	if err != nil {
		return repository.ReviewStatus{}, err
	}
	if link.Kind != "pull_request" {
		return repository.ReviewStatus{}, errNotReviewRequest
	}
	if r, ok := p.s.repoExt.(repository.ReviewReader); ok && slices.Contains(p.info.Capabilities, wire.RepositoryCapabilityPrStatus) {
		return r.RepositoryPRStatus(call, p.info.ID, link.CanonicalURL)
	}
	d, err := p.s.repoExt.RepositoryDescribe(call, p.info.ID, link.CanonicalURL)
	if err != nil {
		return repository.ReviewStatus{}, err
	}
	if d.State == "" {
		return repository.ReviewStatus{}, errReviewUnsupported
	}
	return repository.ReviewStatus{State: d.State}, nil
}
