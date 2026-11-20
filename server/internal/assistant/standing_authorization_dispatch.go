package assistant

import (
	"context"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// Read grants at dispatch, not while a plan waits to start, so changes made in between apply.
func (s *Service) withStandingAuthorizations(ctx context.Context, machine, cwd, text string, repositories ...string) (string, error) {
	grants, err := s.standingForDirectory(ctx, machine, cwd)
	if err != nil {
		return "", fmt.Errorf("read standing authorizations: %w", err)
	}
	if len(repositories) > 0 {
		seen := map[string]bool{}
		for _, g := range grants {
			seen[g.ID] = true
		}
		for _, dir := range repositories {
			scoped, err := s.standingForDirectory(ctx, machine, dir)
			if err != nil {
				return "", fmt.Errorf("read standing authorizations: %w", err)
			}
			for _, g := range scoped {
				if !seen[g.ID] {
					grants = append(grants, g)
					seen[g.ID] = true
				}
			}
		}
	}
	if len(grants) == 0 {
		return text, nil
	}
	in := prompts.StandingAuthorizations{Text: text, Grants: make([]prompts.StandingAuthorization, 0, len(grants))}
	for _, grant := range grants {
		in.Grants = append(in.Grants, prompts.StandingAuthorization{
			ID: grant.ID, MachineID: grant.MachineID, Directory: grant.Directory,
			Actions: grant.Actions, Conditions: grant.Conditions, Quote: grant.Quote, Source: grant.Source,
			GrantedAt: grant.GrantedAt.Format(time.RFC3339), Reversible: grant.Reversible,
		})
	}
	return prompts.RenderStandingAuthorizations(in), nil
}
