package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
)

// The Linux agent sandbox uses only user and mount namespaces. In particular it does not unshare the network or pid
// namespace: agents keep their network and the node keeps recognizing the process it started. The extension sandbox's
// read-only-system / private-network model does not apply here.
// linuxAgentProbe deliberately does not cache: bwrap may be installed, removed or moved on PATH, and a temporarily
// unavailable user namespace may recover. Every caller gets a fresh real probe.
func linuxAgentProbe() Support {
	return probeLinuxAgent()
}

// probeLinuxAgent runs a real default-allow mount namespace, rather than treating a bwrap binary on PATH as support.
// The full generated profile is checked separately before each distinct agent start profile.
func probeLinuxAgent() Support {
	s := Support{OS: "linux"}
	path, err := exec.LookPath("bwrap")
	if err != nil {
		s.setReason("extension-status.sandbox_no_bwrap", nil)
		return s
	}
	trueBin := "true"
	if p, err := exec.LookPath("true"); err == nil {
		trueBin = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"--die-with-parent", "--unshare-user", "--bind", "/", "/", "--dev-bind", "/dev", "/dev", "--chdir", "/", "--", trueBin}
	if out, err := exec.CommandContext(ctx, path, args...).CombinedOutput(); err != nil {
		s.setReason("extension-status.sandbox_bwrap_start", i18n.Args{"err": firstLine(out, err)})
		return s
	}
	s.Bwrap = path
	return s
}

type linuxAgentDir struct {
	real    string
	aliases []string
}

// bubblewrap 0.8 rejects more than 9000 arguments before starting the guest. Check the complete command locally so a
// large data directory fails early with an actionable reason instead of bwrap's generic argument-count error.
const linuxAgentMaxArgs = 9000

func linuxAgentArgsError(count int) error {
	return NewStatusReason("extension-sandbox.agent_too_many_args", i18n.Args{"count": count, "limit": linuxAgentMaxArgs})
}

func linuxAgentCheckArgs(count int) error {
	if count > linuxAgentMaxArgs {
		return linuxAgentArgsError(count)
	}
	return nil
}

// LinuxAgent returns the bwrap command prefix for plan, before "--" and the agent argv. It bind-mounts the host root
// read-write, then replaces each data directory (and each explicit symlink name for it) with a view containing only
// entries the agent may use. Secret files and whole closed directories are absent from that view; the view's own root
// is read-only, so a new backup or dot-prefixed copy cannot be created at its top either. Existing allowed children
// stay writable through their own bind mounts.
//
// The classification comes from identity.DataSecretName (DataSecrets plus DataCredentialPattern) and
// identity.DataSecretDirs, with the same additional directories and launcher-settings exception as DarwinAgent.
// Mount points on the data directory and its ancestors make renaming them fail. The socket directories are absent, so
// connecting to their sockets fails without filtering network syscalls. An alias receives the same closed view. Its
// path is also passed to the pre-exec helper, which mounts the symlink inode without following it so the alias itself
// cannot be unlinked or renamed while its parent stays writable.
func LinuxAgent(bwrap string, plan AgentPlan) ([]string, error) {
	if bwrap == "" {
		return nil, errors.New("bwrap path is empty")
	}
	dirs, cwd, err := linuxAgentDirs(plan)
	if err != nil {
		return nil, err
	}
	args := []string{bwrap, "--die-with-parent", "--unshare-user", "--bind", "/", "/", "--dev-bind", "/dev", "/dev"}

	// Renaming a mount point itself fails with EBUSY, while its children remain writable. This is the mount-namespace
	// equivalent of Darwin's no-write rules on the data directory and every folder above it.
	mounted := map[string]bool{"/": true}
	for _, d := range dirs {
		for p := filepath.Dir(d.real); p != "/" && p != "."; p = filepath.Dir(p) {
			if !mounted[p] {
				args = append(args, "--bind", p, p)
				mounted[p] = true
			}
		}
	}

	closed := map[string]bool{}
	for _, sub := range append(append([]string(nil), identity.DataSecretDirs...), agentClosedDirs...) {
		closed[filepath.ToSlash(filepath.Clean(sub))] = true
	}
	aliasCount := 0
	for _, d := range dirs {
		allowed := linuxAgentLauncherSettings(plan.LauncherSettings, d.real)
		var err error
		args, err = linuxAgentView(args, d.real, d.real, closed, allowed)
		if err != nil {
			return nil, err
		}
		for _, alias := range d.aliases {
			// A tmpfs directly on a symlink follows it and masks the real directory. First bind the real directory over
			// the alias so bubblewrap gives this second name its own view, not a second mask over the real path.
			args = append(args, "--bind", d.real, alias)
			args, err = linuxAgentView(args, d.real, alias, closed, allowed)
			if err != nil {
				return nil, err
			}
			// The pre-exec helper consumes one environment entry per alias and removes them before exec. Separate entries
			// avoid a single long argument when many aliases are configured.
			args = append(args, "--setenv", agentAliasEnv(aliasCount), alias)
			aliasCount++
		}
	}
	if cwd != "" {
		args = append(args, "--chdir", cwd)
	}
	// LinuxAgent is also used without WrapAgent, so reserve the command separator and one command element here. An
	// aliased start also needs the resolved executable, the internal helper command and its separator. WrapAgent repeats
	// the check with the actual agent argv length.
	reserve := 2
	if aliasCount > 0 {
		reserve = 5
	}
	if err := linuxAgentCheckArgs(len(args) + reserve); err != nil {
		return nil, err
	}
	return args, nil
}

func agentAliasEnv(i int) string {
	return "PERSONA_AGENT_SANDBOX_ALIAS_" + strconv.Itoa(i)
}

// linuxAgentCommand adds the command after a LinuxAgent prefix. Aliased starts exec the current node binary first so
// it can mount the symlink inodes without following them and then exec the agent at the same pid. A start with no
// aliases keeps the original bwrap command and environment exactly.
func linuxAgentCommand(prefix, argv []string) ([]string, error) {
	wrapped := append([]string(nil), prefix...)
	helper := false
	for i := 0; i+1 < len(prefix); i++ {
		if prefix[i] == "--setenv" && prefix[i+1] == agentAliasEnv(0) {
			helper = true
			break
		}
	}
	if helper {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		wrapped = append(wrapped, "--", realPath(exe), AgentExecCommand, "--")
	} else {
		wrapped = append(wrapped, "--")
	}
	wrapped = append(wrapped, argv...)
	if err := linuxAgentCheckArgs(len(wrapped)); err != nil {
		return nil, err
	}
	return wrapped, nil
}

// linuxAgentDirs resolves and validates plan exactly where DarwinAgent does. Unlike a sandbox-exec profile, argv can
// hold quotes, backslashes and newlines in paths, so Linux does not need DarwinAgent's profile-language path refusal.
func linuxAgentDirs(plan AgentPlan) ([]linuxAgentDir, string, error) {
	home := realPath(plan.Home)
	cwd := ""
	if plan.Cwd != "" {
		cwd = realPath(plan.Cwd)
	}
	var dirs []linuxAgentDir
	for _, d := range plan.DataDirs {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		d = filepath.Clean(d)
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() {
			continue
		}
		r := realPath(d)
		at := -1
		for i := range dirs {
			if dirs[i].real == r {
				at = i
				break
			}
		}
		if at < 0 {
			dirs = append(dirs, linuxAgentDir{real: r})
			at = len(dirs) - 1
		}
		// Keep every explicit name, not just ~/.persona: each one receives the masked view and the pre-exec helper's
		// no-follow symlink mount.
		named := filepath.Join(realPath(filepath.Dir(d)), filepath.Base(d))
		if named != r {
			found := false
			for _, alias := range dirs[at].aliases {
				found = found || alias == named
			}
			if !found {
				dirs[at].aliases = append(dirs[at].aliases, named)
			}
		}
	}
	if len(dirs) == 0 {
		return nil, "", NewStatusReason("extension-sandbox.agent_no_data_dir", nil)
	}
	for _, d := range dirs {
		if home != "" && (d.real == home || within(home, d.real)) {
			return nil, "", NewStatusReason("extension-sandbox.agent_home", i18n.Args{"dir": d.real})
		}
		if cwd != "" && (cwd == d.real || within(cwd, d.real)) {
			rel, _ := filepath.Rel(d.real, cwd)
			first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
			if !slices.Contains(agentWorkspaces, first) {
				return nil, "", NewStatusReason("extension-sandbox.agent_cwd_inside", i18n.Args{"cwd": cwd})
			}
		}
	}
	return dirs, cwd, nil
}

func linuxAgentLauncherSettings(settings []string, dataDir string) map[string]bool {
	allowed := map[string]bool{}
	for _, f := range settings {
		if f == "" || !filepath.IsAbs(f) {
			continue
		}
		f = filepath.Join(realPath(filepath.Dir(filepath.Clean(f))), filepath.Base(f))
		if filepath.Dir(f) == filepath.Join(dataDir, "launcher-settings") {
			rel, err := filepath.Rel(dataDir, f)
			if err == nil {
				allowed[filepath.ToSlash(rel)] = true
			}
		}
	}
	return allowed
}

func linuxAgentTopSecret(name string) bool {
	if identity.DataSecretName(name) {
		return true
	}
	// Darwin's top-level expression permits one leading dot before a known secret name (for example .hub.json.12345).
	return strings.HasPrefix(name, ".") && identity.DataSecretName(strings.TrimPrefix(name, "."))
}

func linuxAgentHasBelow(rules map[string]bool, rel string) bool {
	prefix := rel + "/"
	for path := range rules {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// linuxAgentView makes dest a closed view of source. populate recurses only into directories that hold a closed child
// or an explicitly readable launcher-settings file; everything else is one read-write bind mount.
func linuxAgentView(args []string, source, dest string, closed, allowed map[string]bool) ([]string, error) {
	args = append(args, "--tmpfs", dest)
	args, err := linuxAgentPopulate(args, source, dest, "", false, closed, allowed)
	if err != nil {
		return nil, err
	}
	return append(args, "--remount-ro", dest), nil
}

// linuxAgentPopulate recursively materializes a view. allowed currently contains only direct launcher-settings files,
// never directories; allowing a directory would also expose any closed descendants beneath it.
func linuxAgentPopulate(args []string, source, dest, rel string, closedParent bool, closed, allowed map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(source, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		childRel := filepath.ToSlash(filepath.Join(rel, entry.Name()))
		sourcePath := filepath.Join(source, filepath.FromSlash(childRel))
		destPath := filepath.Join(dest, filepath.FromSlash(childRel))
		info, err := entry.Info() // like Lstat, reports the link itself rather than its target
		if err != nil {
			return nil, err
		}
		isLink := info.Mode()&os.ModeSymlink != 0
		isClosed := closedParent || closed[childRel] || (rel == "" && linuxAgentTopSecret(entry.Name()))
		allowHere := allowed[childRel]
		allowBelow := linuxAgentHasBelow(allowed, childRel)
		closedBelow := linuxAgentHasBelow(closed, childRel)
		readOnly := rel == "" && entry.Name() == AgentOnFile
		if isClosed && !allowHere && !allowBelow {
			continue
		}
		// A special read-only file must not be replaced by a bind of whatever its link points at. A symlink at a closed
		// or partially closed directory would also move those rules to an unmasked tree outside the view, so close it.
		if isLink && ((allowHere || readOnly) || isClosed || closedBelow) {
			continue
		}
		switch {
		case allowHere || readOnly:
			// This start's own --settings file is readable but not writable, as under sandbox-exec. The switch file is
			// likewise visible but read-only. Symlinks for either were closed above.
			args = append(args, "--ro-bind", sourcePath, destPath)
		case isLink:
			// bind follows the source's final symlink and would import a secret target into the view. Recreate the link
			// with its original target instead: opening it then resolves again through this masked view. Relative targets
			// stay relative, and ordinary links to files outside the data directory continue to work.
			target, err := os.Readlink(sourcePath)
			if err != nil {
				return nil, err
			}
			args = append(args, "--symlink", target, destPath)
		case closedBelow || allowBelow:
			args = append(args, "--tmpfs", destPath)
			args, err = linuxAgentPopulate(args, source, dest, childRel, isClosed, closed, allowed)
			if err != nil {
				return nil, err
			}
			// A directory holding a closed folder is itself a read-only mount point: it cannot be renamed away, and a
			// new entry cannot be created beside the selected children.
			args = append(args, "--remount-ro", destPath)
		default:
			args = append(args, "--bind", sourcePath, destPath)
		}
	}
	return args, nil
}

// checkLinuxAgentProfile is the Linux counterpart of checkAgentProfile. The opaque profile is the complete bwrap
// prefix joined by NULs (which cannot occur in an argv element). Every start runs true with every mount and the alias
// helper: no cached success can outlive bwrap's removal, a namespace failure or missing open_tree/move_mount support,
// and per-cwd profiles do not accumulate in memory.
func checkLinuxAgentProfile(profile string) error {
	parts := strings.Split(profile, "\x00")
	if len(parts) < 2 || parts[0] == "" {
		return errors.New("empty Linux agent profile")
	}
	trueBin := "true"
	if p, err := exec.LookPath("true"); err == nil {
		trueBin = p
	}
	command, err := linuxAgentCommand(parts, []string{trueBin})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput(); err != nil {
		return errors.New(firstLine(out, err))
	}
	return nil
}
