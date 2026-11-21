package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/managerconf"
)

// Built-in GitLab (#11584) reads merge requests on gitlab.com and on the self-hosted instances manager.json names:
//
//	"gitlab": {"instances": [{"instance_url": "https://gitlab.example.com", "token_file": "~/.persona/gitlab.token"}]}
//
// token_file (0600, read at each call, never logged or shown to a model) holds a read_api token; without one only
// public projects answer. gitlab.com may have an entry too, for its token. An instance on a private network also
// needs its repository_trusted_targets entry, as a repository check does.

// repositoryManagerConfig is what manager.json says about repositories.
type repositoryManagerConfig struct {
	Targets []repository.TrustedTarget `json:"repository_trusted_targets"`
	GitHub  struct {
		Proxy string `json:"proxy"`
	} `json:"github"`
	GitLab struct {
		Instances []gitlabInstance `json:"instances"`
	} `json:"gitlab"`
}

type gitlabInstance struct {
	InstanceURL string `json:"instance_url"`
	TokenFile   string `json:"token_file"`
}

var errRepositoryConfig = errors.New("manager.json cannot be read")

// readRepositoryConfig reads manager.json's repository sections; none without a manager.json.
func (s *Service) readRepositoryConfig() (repositoryManagerConfig, error) {
	var cfg repositoryManagerConfig
	if s.repositoryConfig == "" {
		return cfg, nil
	}
	raw, err := managerconf.ReadFile(s.repositoryConfig)
	if err != nil || raw != nil && (len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &cfg) != nil) {
		return cfg, errRepositoryConfig
	}
	return cfg, nil
}

func (c repositoryManagerConfig) checker() repository.Checker {
	return repository.Checker{TrustedTargets: c.Targets, GitHubProxy: c.GitHub.Proxy}
}

// gitlabHostFor is built-in GitLab for host when it is gitlab.com or an instance manager.json names.
func (s *Service) gitlabHostFor(host string) (codeHost, bool) {
	cfg, err := s.readRepositoryConfig()
	if err != nil {
		cfg = repositoryManagerConfig{}
	}
	h := gitlabHost{s: s, cfg: cfg}
	for _, inst := range cfg.GitLab.Instances {
		if u, err := url.Parse(inst.InstanceURL); err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), host) {
			h.instance, h.tokenFile = strings.TrimRight(inst.InstanceURL, "/"), inst.TokenFile
			return h, true
		}
	}
	return h, host == "gitlab.com"
}

// gitlabHost reads merge requests with the GitLab API (repository.Checker.GitLabReview).
type gitlabHost struct {
	s         *Service
	cfg       repositoryManagerConfig
	instance  string // the named instance whose token this is; "" for gitlab.com without an entry
	tokenFile string
}

func (gitlabHost) Name() string { return "GitLab" }

func (g gitlabHost) ReviewStatus(ctx context.Context, link string) (repository.ReviewStatus, error) {
	id, l, err := repository.ParseTaskLink(link)
	if err != nil || id.Provider != repository.GitLab || l.Kind != "pull_request" {
		return repository.ReviewStatus{}, errNotReviewRequest
	}
	token := ""
	if g.tokenFile != "" && !strings.EqualFold(id.InstanceURL, g.instance) {
		return repository.ReviewStatus{}, errors.New("the address is not on the instance manager.json names")
	}
	if g.tokenFile != "" {
		raw, err := os.ReadFile(expandHome(g.tokenFile))
		if err != nil {
			return repository.ReviewStatus{}, errors.New("its token_file cannot be read")
		}
		token = strings.TrimSpace(string(raw))
	}
	if g.s.gitlabReview != nil {
		return g.s.gitlabReview(ctx, id, l, token)
	}
	return g.cfg.checker().GitLabReview(ctx, id, l, token)
}
