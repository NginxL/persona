package assistant

import (
	"context"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/nodeproto"
)

// spaceDevs are the manager's computers, whose nodes keep track directories and their SPACE.md unless old (neither)
// or noSpaceMD (not SPACE.md) says otherwise.
type spaceDevs struct {
	*fleetDevs
	old, noSpaceMD bool
}

func (d spaceDevs) NodeAnnounced(_, capability string) bool {
	return !d.old && (capability == nodeproto.TrackSpaces || capability == nodeproto.TrackSpaceMD && !d.noSpaceMD)
}

// spaceTrackService is the health hub's assistant on a registry with a space track (qa-plain) and tracks that have
// no directory of their own here: one elsewhere, an archived one, one with another template.
func spaceTrackService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := healthHub()
	f.devices[0].Online = true
	gone := time.Unix(1_800_000_000, 0)
	st := &trackKindMemStore{trackRegistryStore: &trackRegistryStore{memStore: &memStore{}, rows: []db.Track{
		{Slug: "qa-plain", Kind: trackKindSpace, Title: "QA plain", Template: trackTemplateSpace, Revision: 1},
		{Slug: "qa-elsewhere", Kind: trackKindSpace, Title: "QA elsewhere", Template: trackTemplateSpace, DirName: "kept/elsewhere", Revision: 1},
		{Slug: "qa-gone", Kind: trackKindSpace, Title: "QA gone", Template: trackTemplateSpace, ArchivedAt: &gone, Revision: 1},
		{Slug: "qa-health-like", Kind: trackKindSpace, Title: "QA health-like", Template: trackTemplateHealth, Revision: 1},
	}}}
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) { v.MachineID = "server" })
	return s, f
}

// A track whose template is space has a directory of its own (#13472): tracks/<slug> on the assistant's machine,
// prepared with its first SPACE.md and the space baseline, only on a node that keeps track directories; the health
// space and the assistant's own are prepared as before.
func TestSpaceTrackPrepare(t *testing.T) {
	ctx := context.Background()
	s, f := spaceTrackService(t)
	if got := s.spaceTracks(ctx); len(got) != 1 || got[0] != "qa-plain" {
		t.Fatalf("space tracks = %v, want qa-plain only", got)
	}
	d, _ := s.device(ctx, "server")
	if got := s.spaceCwd(ctx, d, "qa-plain"); got != "/home/me/.persona/tracks/qa-plain" {
		t.Errorf("qa-plain's directory = %q", got)
	}
	for _, slug := range []string{"qa-elsewhere", "qa-gone", "qa-health-like", "qa-unknown"} {
		if got := s.spaceCwd(ctx, d, slug); got != "" {
			t.Errorf("%s: directory %q, want none", slug, got)
		}
		if _, err := s.prepareSpace(ctx, "server", slug); err == nil {
			t.Errorf("%s: prepared", slug)
		}
	}
	if got := s.spaceCwd(ctx, d, SpaceHealth); got != "/home/me/.persona/health" {
		t.Errorf("health's directory = %q", got)
	}

	// A node that does not keep track directories is not asked.
	if _, err := s.prepareSpace(ctx, "server", "qa-plain"); err == nil || len(f.prepared) != 0 {
		t.Fatalf("an older node: %v, %d prepared", err, len(f.prepared))
	}
	s.refreshInstructions(ctx)
	if len(f.prepared) != 2 {
		t.Fatalf("an older node's refresh: %d prepared, want the assistant's and health's", len(f.prepared))
	}

	s.devs = spaceDevs{fleetDevs: newFleetDevs()}
	f.prepared = nil
	if _, err := s.prepareSpace(ctx, "server", "qa-plain"); err != nil {
		t.Fatal(err)
	}
	p := f.prepared[0]
	space, _ := p["space"].(map[string]any)
	md, _ := p["claude_md"].(string)
	initial, _ := space["initial_space_md"].(string)
	trackDir := path.Join(compat.StateDir, "tracks/qa-plain")
	if p["dir"] != trackDir || space["slug"] != "qa-plain" || !strings.HasPrefix(initial, "# QA plain\n") ||
		strings.Count(initial, spaceStateBegin) != 1 || strings.Index(initial, spaceStateEnd) < strings.Index(initial, spaceStateBegin) {
		t.Errorf("prepare params: %v", p)
	}
	if !strings.Contains(md, "# Persona 空间：QA plain") || !strings.Contains(md, "`/home/me/.persona/tracks/qa-plain`") ||
		strings.Contains(md, "健康助手") || strings.Contains(md, "kb_update") {
		t.Errorf("qa-plain's instructions:\n%s", md)
	}

	// The refresh prepares the track too, and asks its node every time (its SPACE.md may have changed there).
	f.prepared = nil
	s.preparedMu.Lock()
	s.prepared = nil
	s.preparedMu.Unlock()
	s.refreshInstructions(ctx)
	s.refreshInstructions(ctx)
	var dirs []string
	for _, p := range f.prepared {
		dirs = append(dirs, p["dir"].(string))
	}
	// The assistant's and health's are written once, the track's node asked both times.
	if strings.Join(dirs, " ") != s.assistantRel()+" "+s.healthRel()+" "+trackDir+" "+trackDir {
		t.Errorf("refreshed: %v", dirs)
	}
	if _, ok := f.prepared[1]["space"]; ok {
		t.Error("the health space is prepared as a track")
	}
}

// A space track's baseline (#13472): the rules every assistant conversation keeps in every language, within 2 KB
// without the owner's notes.
func TestSpaceClaudeMDRules(t *testing.T) {
	ctx := context.Background()
	f := healthHub()
	s := newTestService(t, f)
	e := trackEntry{Slug: "qa-plain", Kind: trackKindSpace, Title: "QA plain", Template: trackTemplateSpace, DirName: "tracks/qa-plain"}
	for _, locale := range []string{"", "zh-Hans", "en", "ja"} {
		f.hello.Locale = locale
		md := s.spaceClaudeMD(ctx, "server", e)
		for _, rule := range []string{
			"需要用户完整读到的决定确认、清单、数字、代码或长段内容，放在本轮最后的正式回复里，不要只写在工具调用之间的进度说明里。",
			"工作中写给用户看的每一句（工具调用之间的说明、进度、在等什么）也用这种语言，不因为读的代码、日志或资料是英文就改用英文。",
		} {
			if !strings.Contains(md, rule) {
				t.Errorf("locale %q: no rule %q", locale, rule)
			}
		}
		if len(md) > 2048 {
			t.Errorf("locale %q: %d bytes, over 2 KB", locale, len(md))
		}
	}
}

// The space baseline and the first SPACE.md, in each language (#13472).
func TestGoldenSpaceClaudeMD(t *testing.T) {
	ctx := context.Background()
	e := trackEntry{Slug: "qa-plain", Kind: trackKindSpace, Title: "Example space", Template: trackTemplateSpace, DirName: "tracks/qa-plain"}
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		f := healthHub()
		f.hello.Owner = "Example user"
		f.hello.OwnerNotes = "I run every day."
		f.hello.Locale = locale
		s := newTestService(t, f)
		golden(t, "space_claude_md."+locale, s.spaceClaudeMD(ctx, "server", e))
		golden(t, "space_md."+locale, s.initialSpaceMD(ctx, e))
	}
	s := newTestService(t, newFakeHub())
	golden(t, "space_claude_md.bare", s.spaceClaudeMD(ctx, "server", trackEntry{Slug: "qa-plain", DirName: "tracks/qa-plain"}))
}
