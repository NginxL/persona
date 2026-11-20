package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

// What extensions wrote for the prompts this manager builds (docs/extensions/extensions.md "Assistant content from
// the data directory"): a project's background for a new Agent, and the Inbox's triage and drafts with the sentences a
// draft must not repeat. The extension manager reads them from the extensions' data directories; an older one that
// does not know the calls, or none at all, is no context. The Inbox's half, the never-quote check with it, is in
// inbox_extension_context.go.

// projectExtensionContext is what the extensions wrote for these projects, by slug.
func (s *Service) projectExtensionContext(ctx context.Context, slugs []string) map[string][]prompts.ExtensionContext {
	out := map[string][]prompts.ExtensionContext{}
	if len(slugs) == 0 {
		return out
	}
	var res assistantwire.ExtensionContextResult
	if err := s.appCall(ctx, AppPlugins, PluginsProjectContext, assistantwire.ProjectContextParams{Projects: slugs}, &res); err != nil {
		return out
	}
	for _, b := range res.Blocks {
		out[b.Project] = append(out[b.Project], prompts.ExtensionContext{Name: oneLine(b.Name), Text: b.Text})
	}
	return out
}
