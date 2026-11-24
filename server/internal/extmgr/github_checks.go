package extmgr

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/githubwire"
)

func githubChecksPublicHost(c connector.Connector) bool {
	hosts := c.Manifest().Hosts
	return len(hosts) == 1 && strings.EqualFold(hosts[0], "github.com")
}

// PRChecks uses the active authorized GitHub connector. An extension must
// explicitly offer the versioned structured snapshot tool.
func (h *Manager) PRChecks(ctx context.Context, repo string, number int) (githubwire.PRCheckSnapshot, time.Time, bool, error) {
	if !h.ready(ctx) || h.connectors == nil || !h.connectors.Enabled("github") {
		return githubwire.PRCheckSnapshot{}, time.Time{}, false, nil
	}
	registered := h.connectors.Get("github")
	if registered == nil {
		return githubwire.PRCheckSnapshot{}, time.Time{}, false, nil
	}
	// The watched URL is on github.com; a connector signed into another host
	// can have the same owner/repository and PR number with unrelated checks.
	if !githubChecksPublicHost(registered) {
		return githubwire.PRCheckSnapshot{}, time.Time{}, false, nil
	}
	c, builtin := registered.(githubwire.Connector)
	if !builtin {
		return githubExtensionChecks(ctx, registered, repo, number)
	}
	status := c.Status(ctx)
	if status.State == connector.StateRateLimited {
		return githubwire.PRCheckSnapshot{}, c.PRChecksRateReset(), true, nil
	}
	if status.State != connector.StateOK {
		return githubwire.PRCheckSnapshot{}, time.Time{}, false, nil
	}
	snapshot, err := c.PRChecks(ctx, repo, number)
	return snapshot, c.PRChecksRateReset(), true, err
}

// GitHubGet reads path (a GitHub REST path) into out with the built-in GitHub connector, when it is enabled, signed in
// and on github.com; ok false when there is none to read with (the caller reads some other way). The assistant's deploy
// check uses it (assistant deploy_check.go, #3205).
func (h *Manager) GitHubGet(ctx context.Context, path string, out any) (bool, error) {
	if !h.ready(ctx) || h.connectors == nil || !h.connectors.Enabled("github") {
		return false, nil
	}
	registered := h.connectors.Get("github")
	if registered == nil || !githubChecksPublicHost(registered) {
		return false, nil
	}
	c, builtin := registered.(githubwire.Connector)
	if !builtin || c.Status(ctx).State != connector.StateOK {
		return false, nil
	}
	_, err := c.Get(ctx, path, out)
	return true, err
}
