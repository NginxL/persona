package machineops

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

func programName() string {
	if len(os.Args) > 0 && filepath.Base(os.Args[0]) == "persona-node" {
		return "persona-node"
	}
	return "personad"
}

func usageText(prog string) string {
	return fmt.Sprintf(`usage:
  %s ops storage
                           read-only per-mount disk and inode stats (statfs only)
  %s ops hotspots [--timeout-ms N] [--max-depth N]
                           read-only bounded storage hotspot analysis across allowlisted categories
  %s ops toolchain [--refresh]
                           read-only local dev toolchain, agent CLI, Docker/gh, hardware, and env profile
  %s ops network
                           read-only interfaces, redacted proxy config, and allowlisted endpoint probes
  %s ops ports [--protocol tcp|udp]
                           read-only listening sockets with bind scopes and redacted command lines
  %s ops processes [--sort cpu|memory|uptime] [--query TEXT] [--orphan-only] [--limit N]
                           read-only process list with orphan heuristics and redacted command lines
  %s ops services
                           read-only user systemd/launchd services and Docker containers
  %s ops service-logs --kind systemd|launchd|docker --id <id> [--lines N]
                           read-only bounded tail logs (clamped to 1-500 lines and 64 KiB)
  %s ops mcp
                           on-demand read-only machine diagnostics MCP server over stdio
  %s ops skill status|install|uninstall [--dir <skills-dir>]
                           manage opt-in installation of the persona-machine-ops skill`,
		prog, prog, prog, prog, prog, prog, prog, prog, prog, prog)
}

// Main runs `personad ops …` / `persona-node ops …` against the local machine
// and returns the process exit code (0 ok, 1 runtime error, 2 usage error).
func Main(ctx context.Context, version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return MainWithProvider(ctx, version, NewLocalProvider(), args, stdin, stdout, stderr)
}

// MainWithProvider runs the ops CLI against the given Provider (used by tests).
func MainWithProvider(ctx context.Context, version string, provider Provider, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	prog := programName()
	if len(args) == 0 {
		fmt.Fprintln(stderr, usageText(prog))
		return exitUsage
	}
	if isHelp(args[0]) {
		fmt.Fprintln(stdout, usageText(prog))
		return exitOK
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "storage":
		return runNoFlagDiagnostic(ctx, prog, "storage", rest, stdout, stderr, storageTimeout, func(c context.Context) (any, error) {
			return provider.Storage(c)
		})
	case "hotspots":
		return runHotspots(ctx, prog, provider, rest, stdout, stderr)
	case "toolchain":
		return runToolchain(ctx, prog, provider, rest, stdout, stderr)
	case "network":
		return runNoFlagDiagnostic(ctx, prog, "network", rest, stdout, stderr, networkTimeout, func(c context.Context) (any, error) {
			return provider.Network(c)
		})
	case "ports":
		return runPorts(ctx, prog, provider, rest, stdout, stderr)
	case "processes":
		return runProcesses(ctx, prog, provider, rest, stdout, stderr)
	case "services":
		return runNoFlagDiagnostic(ctx, prog, "services", rest, stdout, stderr, servicesTimeout, func(c context.Context) (any, error) {
			return provider.Services(c)
		})
	case "service-logs":
		return runServiceLogs(ctx, prog, provider, rest, stdout, stderr)
	case "mcp":
		return runMCP(ctx, prog, version, provider, rest, stdin, stdout, stderr)
	case "skill":
		return runSkill(prog, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown ops subcommand %q\n%s\n", sub, usageText(prog))
		return exitUsage
	}
}

func isHelp(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

func newFlagSet(prog, sub string, stdout, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(prog+" ops "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func parseFlags(fs *flag.FlagSet, args []string, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 1 && isHelp(args[0]) {
		fs.SetOutput(stdout)
		fs.Usage()
		return exitOK, false
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(stdout)
			fs.Usage()
			return exitOK, false
		}
		return exitUsage, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s takes no positional arguments; got %q\n", fs.Name(), fs.Arg(0))
		return exitUsage, false
	}
	return exitOK, true
}

func writeJSONLine(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func writeErrLine(stderr io.Writer, err error) int {
	_ = writeJSONLine(stderr, map[string]string{"error": err.Error()})
	return exitFailed
}

func runNoFlagDiagnostic(ctx context.Context, prog, sub string, args []string, stdout, stderr io.Writer, timeout time.Duration, fn func(context.Context) (any, error)) int {
	fs := newFlagSet(prog, sub, stdout, stderr)
	if code, ok := parseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := fn(tctx)
	if err != nil {
		return writeErrLine(stderr, err)
	}
	if err := writeJSONLine(stdout, out); err != nil {
		return writeErrLine(stderr, err)
	}
	return exitOK
}

func runHotspots(ctx context.Context, prog string, provider Provider, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet(prog, "hotspots", stdout, stderr)
	timeoutMS := fs.Int("timeout-ms", 0, "optional scan budget in milliseconds (clamped to node bounds)")
	maxDepth := fs.Int("max-depth", 0, "optional maximum walk depth (clamped to node bounds)")
	if code, ok := parseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	params := proto.StorageHotspotsParams{TimeoutMS: *timeoutMS, MaxDepth: *maxDepth}
	if err := validateHotspotsParams(params); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	tctx, cancel := context.WithTimeout(ctx, storageHotspotsTimeout)
	defer cancel()
	out, err := provider.StorageHotspots(tctx, params)
	if err != nil {
		return writeErrLine(stderr, err)
	}
	if err := writeJSONLine(stdout, out); err != nil {
		return writeErrLine(stderr, err)
	}
	return exitOK
}

func runToolchain(ctx context.Context, prog string, provider Provider, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet(prog, "toolchain", stdout, stderr)
	refresh := fs.Bool("refresh", false, "bypass the 60-second in-process cache")
	if code, ok := parseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	tctx, cancel := context.WithTimeout(ctx, toolchainTimeout)
	defer cancel()
	out, err := provider.Toolchain(tctx, proto.MachineToolchainParams{Refresh: *refresh})
	if err != nil {
		return writeErrLine(stderr, err)
	}
	if err := writeJSONLine(stdout, out); err != nil {
		return writeErrLine(stderr, err)
	}
	return exitOK
}

func runPorts(ctx context.Context, prog string, provider Provider, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet(prog, "ports", stdout, stderr)
	protocol := fs.String("protocol", "", "optional protocol filter: tcp or udp")
	if code, ok := parseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	params, err := validatePortsParams(proto.MachinePortsParams{Protocol: *protocol})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	tctx, cancel := context.WithTimeout(ctx, portsTimeout)
	defer cancel()
	out, err := provider.Ports(tctx, params)
	if err != nil {
		return writeErrLine(stderr, err)
	}
	if err := writeJSONLine(stdout, out); err != nil {
		return writeErrLine(stderr, err)
	}
	return exitOK
}

func runProcesses(ctx context.Context, prog string, provider Provider, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet(prog, "processes", stdout, stderr)
	sortBy := fs.String("sort", "", "sort key: cpu (default), memory, or uptime")
	query := fs.String("query", "", "case-insensitive filter on name, cmdline, user, or PID")
	orphanOnly := fs.Bool("orphan-only", false, "return only likely stale/orphan test processes")
	limit := fs.Int("limit", 0, "maximum number of processes to return (default 100, capped at 300)")
	if code, ok := parseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	params, err := validateProcessesParams(proto.MachineProcessesParams{
		Sort:       *sortBy,
		Query:      *query,
		OrphanOnly: *orphanOnly,
		Limit:      *limit,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	tctx, cancel := context.WithTimeout(ctx, processesTimeout)
	defer cancel()
	out, err := provider.Processes(tctx, params)
	if err != nil {
		return writeErrLine(stderr, err)
	}
	if err := writeJSONLine(stdout, out); err != nil {
		return writeErrLine(stderr, err)
	}
	return exitOK
}

func runServiceLogs(ctx context.Context, prog string, provider Provider, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet(prog, "service-logs", stdout, stderr)
	kind := fs.String("kind", "", "service manager kind: systemd, launchd, or docker")
	id := fs.String("id", "", "validated systemd unit name, launchd label, or Docker container ID/name")
	lines := fs.Int("lines", proto.ServiceLogsDefaultLines, "tail line count (clamped to 1-500)")
	if code, ok := parseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	if *lines <= 0 {
		fmt.Fprintln(stderr, "lines must be a positive integer")
		return exitUsage
	}
	params, err := validateServiceLogsParams(proto.MachineServiceLogsParams{
		Kind:  *kind,
		ID:    *id,
		Lines: *lines,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	tctx, cancel := context.WithTimeout(ctx, serviceLogsTimeout)
	defer cancel()
	out, err := provider.ServiceLogs(tctx, params)
	if err != nil {
		return writeErrLine(stderr, err)
	}
	if err := writeJSONLine(stdout, out); err != nil {
		return writeErrLine(stderr, err)
	}
	return exitOK
}

func runMCP(ctx context.Context, prog, version string, provider Provider, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet(prog, "mcp", stdout, stderr)
	if code, ok := parseFlags(fs, args, stdout, stderr); !ok {
		return code
	}
	if err := ServeMCP(ctx, version, provider, stdin, stdout); err != nil {
		return writeErrLine(stderr, err)
	}
	return exitOK
}

func runSkill(prog string, args []string, stdout, stderr io.Writer) int {
	usage := fmt.Sprintf("usage: %s ops skill status|install|uninstall [--dir <skills-dir>]", prog)
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if isHelp(args[0]) {
		fmt.Fprintln(stdout, usage)
		return exitOK
	}
	action, rest := strings.ToLower(args[0]), args[1:]
	switch action {
	case "status", "install", "uninstall":
	default:
		fmt.Fprintf(stderr, "unknown ops skill action %q\n%s\n", args[0], usage)
		return exitUsage
	}
	fs := newFlagSet(prog, "skill "+action, stdout, stderr)
	dir := fs.String("dir", "", "target skills directory (default: ~/.persona/skills)")
	if code, ok := parseFlags(fs, rest, stdout, stderr); !ok {
		return code
	}
	var (
		status assistantskills.MachineOpsSkillStatus
		err    error
	)
	switch action {
	case "status":
		status, err = assistantskills.MachineOpsStatus(*dir)
	case "install":
		status, err = assistantskills.InstallMachineOps(*dir)
	case "uninstall":
		status, err = assistantskills.UninstallMachineOps(*dir)
	}
	if err != nil {
		_ = writeJSONLine(stderr, map[string]any{"error": err.Error(), "status": status})
		return exitFailed
	}
	if err := writeJSONLine(stdout, status); err != nil {
		return writeErrLine(stderr, err)
	}
	return exitOK
}
