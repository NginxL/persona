package node

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/nwverse/persona/server/internal/claudeplugin"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// The Persona plugin for Claude Code (internal/claudeplugin, docs/operations/link.md "Claude Code plugin"): the
// resident node keeps it installed by itself (on macOS and Windows unless turned off on the computer), says how it
// stands in node.info's claude_plugin and, when that changes, in a "claude.plugin" notification. `personad
// claude-plugin` (and `persona-link plugin`, which runs it) checks and changes it by hand.

// claudePluginKeeper is the resident node's keeper (nil in a node that is not resident).
var claudePluginKeeper atomic.Pointer[claudeplugin.Keeper]

// claudePluginAuto: where the plugin is installed without being asked. The desktop app, whose Code sessions need it to
// take the phone's messages, runs on macOS and Windows.
var claudePluginAuto = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// ClaudePluginManager is the plugin's manager for this computer: the user's Claude Code config directories, the CLI
// found the way the node finds it (or the desktop app's own), the plugin this build carries.
func ClaudePluginManager(logf func(string, ...any)) *claudeplugin.Manager {
	home := userHome()
	data := identity.DataDir(home)
	bundle, err := claudeplugin.Embedded()
	if err != nil && logf != nil {
		logf("claude plugin: the plugin this build carries is unreadable: %v", err)
	}
	return &claudeplugin.Manager{
		DataDir:     data,
		Bundle:      bundle,
		Dirs:        claudePluginDirs(home, data, os.Getenv("CLAUDE_CONFIG_DIR")),
		FindClaude:  findClaudeCLI,
		Logf:        logf,
		DefaultAuto: claudePluginAuto,
	}
}

// claudePluginDirs are the config directories the plugin goes into: the user's default (~/.claude, which the desktop
// app's Code sessions use whatever the environment says) and the one this node's environment names. Never a profile
// of Persona's own under its data directory, nor the directories other claudes were seen with (automation runs claude
// with throwaway ones, docs/lessons/agents.md).
func claudePluginDirs(home, data, env string) []string {
	dirs := []string{filepath.Join(home, ".claude")}
	if env = strings.TrimSpace(env); env != "" {
		env = proto.ExpandHome(env, home)
		if rel, err := filepath.Rel(data, env); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			dirs = append(dirs, env)
		}
	}
	return dirs
}

// findClaudeCLI is the newest Claude Code CLI on this computer: the one on PATH (or where the node finds it,
// agentExecutable), or the desktop app's own copy (claudeDesktopCLIs).
func findClaudeCLI(ctx context.Context) (claudeplugin.Claude, bool) {
	var cands []string
	if exe, err := claudeExecutable(); err == nil {
		cands = append(cands, exe)
	}
	if desktop := claudeDesktopCLIs(runtime.GOOS, userHome()); len(desktop) > 0 {
		cands = append(cands, desktop[0]) // the newest the app keeps; older ones are left for it to clean up
	}
	run := claudeplugin.ExecRunner(os.TempDir())
	var best claudeplugin.Claude
	for _, exe := range cands {
		v := claudeplugin.Version(ctx, run, exe, filepath.Join(userHome(), ".claude"))
		if v == "" {
			continue
		}
		if best.Exe == "" || claudeplugin.Newer(v, best.Version) {
			best = claudeplugin.Claude{Exe: exe, Version: v}
		}
	}
	return best, best.Exe != ""
}

// claudeDesktopCLIs are the Claude Code CLIs the desktop app keeps for its Code sessions, newest first:
// <app data>/claude-code/<version>/<build>/claude.exe on Windows (measured 2026-11-16, Claude-3p); on macOS either inside
// <app data>/claude-code/<version>/<build>/claude.app/Contents/MacOS/claude (measured 2026-11-19, Claude-3p 2.1.326) or
// directly under the build/version directory.
func claudeDesktopCLIs(goos, home string) []string {
	return claudeDesktopCLIsIn(goos, desktopDataDirs(goos, home))
}

func claudeDesktopCLIsIn(goos string, dirs []string) []string {
	name := "claude"
	if goos == "windows" {
		name = "claude.exe"
	}
	var out []string
	for _, dir := range dirs {
		patterns := []string{
			filepath.Join(dir, "claude-code", "*", "*", name),
			filepath.Join(dir, "claude-code", "*", name),
		}
		if goos == "darwin" {
			patterns = append([]string{
				filepath.Join(dir, "claude-code", "*", "*", "claude.app", "Contents", "MacOS", name),
				filepath.Join(dir, "claude-code", "*", "claude.app", "Contents", "MacOS", name),
			}, patterns...)
		}
		for _, pattern := range patterns {
			m, _ := filepath.Glob(pattern)
			out = append(out, m...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return claudeplugin.Newer(desktopCLIVersion(out[i]), desktopCLIVersion(out[j])) })
	return out
}

// desktopCLIVersion is the version directory a desktop CLI sits in.
func desktopCLIVersion(path string) string {
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if filepath.Base(filepath.Dir(dir)) == "claude-code" {
			return filepath.Base(dir)
		}
	}
	return ""
}

// StartClaudePlugin starts the resident node's keeper (runResident).
func StartClaudePlugin(ctx context.Context, logger *log.Logger) {
	k := claudeplugin.NewKeeper(ClaudePluginManager(logger.Printf))
	claudePluginKeeper.Store(k)
	go k.Run(ctx)
}

// claudePluginStatus is node.info's claude_plugin: the keeper's latest, else what the last check left (a node that
// is not resident reads the file the resident one or a command wrote). nil when nothing has been checked.
func (n *Node) claudePluginStatus() *proto.ClaudePlugin {
	if k := claudePluginKeeper.Load(); k != nil {
		return k.Status()
	}
	st, err := claudeplugin.ReadStatus(n.dir)
	if err != nil {
		return nil
	}
	return st
}

// watchClaudePlugin tells the hub each new status while this connection lasts.
func (n *Node) watchClaudePlugin(ctx context.Context) {
	k := claudePluginKeeper.Load()
	if k == nil {
		return
	}
	stop := k.Subscribe(func(st proto.ClaudePlugin) { _ = n.peer.Notify("claude.plugin", st) })
	<-ctx.Done()
	stop()
}

// ClaudePluginCommand is `claude-plugin <op>` (nodecmd): the status after op, as JSON.
func ClaudePluginCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	logger := log.New(stderr, "", 0)
	return claudeplugin.Command(ctx, ClaudePluginManager(logger.Printf), args, stdout, stderr)
}
