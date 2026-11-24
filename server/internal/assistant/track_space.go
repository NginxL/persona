package assistant

import (
	"context"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/prompts"
)

// A track's own directory (#13472, docs/assistant/health.md "Tracks and kinds"): a track whose template is space has
// one on the assistant's machine, tracks/<slug> in the state directory, with its SPACE.md, inbox/, recordings/ and
// .space/. The manager names it on assistant.prepare (space); the node writes the first SPACE.md and the instructions,
// this baseline followed by the SPACE.md it reads there. The health space keeps its own directory and template.

// The state block of a SPACE.md: what Persona keeps there, between these two lines (prompts/assistant/space_md.md).
const (
	spaceStateBegin = "<!-- persona:managed:state:begin -->"
	spaceStateEnd   = "<!-- persona:managed:state:end -->"
)

// spaceTrack is registered track slug when it has a directory of its own here: not the health space (its own), not
// archived, its template space, its directory in this manager's tracks directory (tracks/<slug>), where every
// process tells its conversations from the path alone.
func (s *Service) spaceTrack(ctx context.Context, slug string) (trackEntry, bool) {
	e, ok := s.trackEntryOf(ctx, slug)
	if !ok || e.Builtin || e.Archived || e.Config.Template != trackTemplateSpace || !identity.TrackDirOf(e.DirName, s.id) {
		return trackEntry{}, false
	}
	return e, true
}

// spaceTracks are the slugs of the tracks with a directory of their own here (spaceTrack), by slug.
func (s *Service) spaceTracks(ctx context.Context) []string {
	var out []string
	for _, e := range s.tracks(ctx) {
		if _, ok := s.spaceTrack(ctx, e.Slug); ok {
			out = append(out, e.Slug)
		}
	}
	slices.Sort(out)
	return out
}

// trackRel is track e's directory on a machine, relative to its home (node assistant.prepare), spelled as
// assistantRel is.
func trackRel(e trackEntry) string { return path.Join(compat.StateDir, e.DirName) }

// trackSpaceNode: machine's node keeps a track's directory (nodeproto.TrackSpaces).
func (s *Service) trackSpaceNode(machine string) bool {
	nodes, ok := s.physicalDevices().(nodeAnnouncer)
	return ok && nodes.NodeAnnounced(machine, nodeproto.TrackSpaces)
}

// spaceClaudeMD is the baseline of track e's instructions on machine here (prompts/assistant/space_claude_md.md); the
// node follows it with the track's SPACE.md.
func (s *Service) spaceClaudeMD(ctx context.Context, here string, e trackEntry) string {
	hello := s.hello(ctx)
	d := prompts.AssistantSpaceClaudeMD{Owner: strings.TrimSpace(hello.Owner), Title: oneLine(e.Title), DirName: e.DirName,
		Notes: s.notes(ctx), Language: answerLanguage(hello.Locale)}
	if d.Title == "" {
		d.Title = e.Slug
	}
	devices := s.devices(ctx)
	if i := slices.IndexFunc(devices, func(d Device) bool { return d.ID == here }); i >= 0 {
		if cwd := s.assistantCwd(devices[i]); cwd != "" {
			d.StateDir = path.Dir(cwd)
		}
	}
	return prompts.RenderAssistantSpaceClaudeMD(d)
}

// initialSpaceMD is track e's first SPACE.md, in the owner's language (prompts/assistant/space_md.md).
func (s *Service) initialSpaceMD(ctx context.Context, e trackEntry) string {
	title := oneLine(e.Title)
	if title == "" {
		title = e.Slug
	}
	return prompts.RenderAssistantSpaceMD(prompts.AssistantSpaceMD{Lang: directionLang(s.lang(ctx)), Title: title})
}

// prepareTrackSoon makes new track slug's directory on the assistant's machine now, rather than at the next refresh
// (refreshInstructions) or the first conversation, when that machine's node keeps track directories.
func (s *Service) prepareTrackSoon(slug string) {
	ctx := s.runCtx()
	machine := s.settings(ctx).MachineID
	if machine == "" || s.isWorkMachine(machine) || !s.trackSpaceNode(machine) {
		return
	}
	if _, ok := s.spaceTrack(ctx, slug); !ok {
		return
	}
	go func() {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := s.prepareSpace(cctx, machine, slug); err != nil {
			s.logf("tracks: prepare %q on %s: %v", slug, machine, err)
		}
	}()
}
