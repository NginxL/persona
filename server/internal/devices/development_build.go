package devices

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
)

func developmentTools(goos, arch string) error {
	for _, tool := range []string{"git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			return i18n.Err("devices-deploy.development_tools", i18n.Args{"os": goos, "arch": arch})
		}
	}
	return nil
}

// The detached checkout is private to one cache transaction. Fetch the object
// directly at depth one; the manager never needs to resolve a branch or a tag.
func buildDevelopmentPackage(ctx context.Context, stage string, p nodePackage) (string, error) {
	t := p.development
	if t == nil || !t.Valid() || !platformPart.MatchString(p.goos) || !platformPart.MatchString(p.arch) ||
		(p.Program != "personad" && p.Program != "persona-node" && p.Program != "persona-manager") {
		return "", i18n.Err("devices-deploy.hub_changed")
	}
	if err := developmentTools(p.goos, p.arch); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	checkout := filepath.Join(stage, "source")
	if err := os.Mkdir(checkout, 0700); err != nil {
		return "", err
	}
	run := func(dir string, env []string, tool string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, tool, args...)
		cmd.Dir, cmd.Env = dir, append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "CGO_ENABLED=0"), env...)
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
		if err != nil {
			// Git and the Go module client may print credential-bearing URLs.
			return "", i18n.Err("devices-deploy.development_build", i18n.Args{"step": tool + " " + args[0], "commit": t.Commit[:12]})
		}
		return strings.TrimSpace(string(out)), nil
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"fetch", "--depth=1", "--no-tags", devicewire.DevelopmentRepository, t.Commit}, {"checkout", "--detach", "FETCH_HEAD"}} {
		if _, err := run(checkout, nil, "git", args...); err != nil {
			return "", err
		}
	}
	head, err := run(checkout, nil, "git", "rev-parse", "HEAD")
	if err != nil || head != t.Commit {
		return "", i18n.Err("devices-deploy.hub_changed")
	}
	server := filepath.Join(checkout, "server")
	tags, err := os.ReadFile(filepath.Join(server, "build-tags.txt"))
	if err != nil {
		return "", err
	}
	if _, err := run(server, nil, "go", "generate", "./internal/runtimepkg"); err != nil {
		return "", err
	}
	buildID := fmt.Sprintf("%s.%d.%s", t.Version, t.Build, t.Commit)
	flags := fmt.Sprintf("-s -w -buildid=%s -X main.version=%s -X main.channel=release -X main.build=%d -X main.commit=%s", buildID, t.Version, t.Build, t.Commit)
	out := filepath.Join(stage, nodeBinaryName(p.Program, p.goos+"-"+p.arch))
	if _, err := run(server, []string{"GOOS=" + p.goos, "GOARCH=" + p.arch}, "go", "build", "-buildvcs=true", "-trimpath",
		"-tags", strings.TrimSpace(string(tags)), "-ldflags", flags, "-o", out, "./cmd/"+p.Program); err != nil {
		return "", err
	}
	info, err := buildinfo.ReadFile(out)
	if err != nil {
		return "", err
	}
	// Read both the linker's identity and Go's independent VCS stamp without
	// executing a foreign binary. Native builds also run the Release version check.
	gotID, err := run(server, nil, "go", "tool", "buildid", out)
	if err != nil {
		return "", err
	}
	for _, v := range info.Settings {
		if v.Key == "vcs.revision" && v.Value == t.Commit && gotID == buildID {
			return out, nil
		}
	}
	return "", i18n.Err("devices-deploy.development_identity")
}
