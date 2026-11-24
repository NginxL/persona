package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	serviceListTimeout   = 10 * time.Second
	serviceLogsTimeout   = 10 * time.Second
	serviceActionTimeout = 20 * time.Second
	serviceCmdMaxBytes   = 256 * 1024
)

// machineServiceRunner isolates OS commands for machine service & container RPCs
// so unit tests can exercise Linux systemd, macOS launchd, Docker, truncation,
// and self-protection without touching host services.
type machineServiceRunner struct {
	goos     string
	home     string
	uid      func() int
	now      func() time.Time
	lookPath func(string) (string, error)
	run      func(ctx context.Context, maxBytes int, name string, args ...string) ([]byte, bool, error)
	readFile func(string) ([]byte, error)
	readDir  func(string) ([]os.DirEntry, error)
}

func (n *Node) servicesRunner() *machineServiceRunner {
	if n.serviceRunner != nil {
		return n.serviceRunner
	}
	home := n.home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return &machineServiceRunner{
		goos:     runtime.GOOS,
		home:     home,
		uid:      os.Getuid,
		now:      time.Now,
		lookPath: exec.LookPath,
		run:      runBoundedCommand,
		readFile: os.ReadFile,
		readDir:  os.ReadDir,
	}
}

// runBoundedCommand runs name with args (never through a shell), capturing
// combined output up to maxBytes + 1 bytes so huge logs cannot exhaust memory.
// It keeps the tail when output exceeds maxBytes and reports truncated=true.
func runBoundedCommand(ctx context.Context, maxBytes int, name string, args ...string) ([]byte, bool, error) {
	if maxBytes <= 0 {
		maxBytes = serviceCmdMaxBytes
	}
	cmd := exec.CommandContext(ctx, name, args...)
	w := newServiceTailBuffer(maxBytes)
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	return w.Bytes(), w.Truncated(), err
}

// serviceTailBuffer keeps the last limit bytes written to it while discarding older bytes.
type serviceTailBuffer struct {
	limit     int
	buf       []byte
	truncated bool
}

func newServiceTailBuffer(limit int) *serviceTailBuffer {
	return &serviceTailBuffer{limit: limit}
}

func (t *serviceTailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	if n >= t.limit {
		t.buf = append(t.buf[:0], p[n-t.limit:]...)
		t.truncated = true
		return n, nil
	}
	if len(t.buf)+n > t.limit {
		drop := len(t.buf) + n - t.limit
		copy(t.buf, t.buf[drop:])
		t.buf = t.buf[:len(t.buf)-drop]
		t.truncated = true
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

func (t *serviceTailBuffer) Bytes() []byte {
	out := make([]byte, len(t.buf))
	copy(out, t.buf)
	return out
}

func (t *serviceTailBuffer) Truncated() bool {
	return t.truncated
}

// The machine page's services and containers. Each handler decodes its own params, answering bad_request.
func init() {
	nodeRPC(proto.MethodMachineServices, (*Node).machineServices)
	nodeRPC(proto.MethodMachineServiceLogs, (*Node).machineServiceLogs)
	nodeRPC(proto.MethodMachineServiceAction, (*Node).machineServiceAction)
}

func (n *Node) machineServices(ctx context.Context, _ json.RawMessage) (any, error) {
	r := n.servicesRunner()
	report := proto.MachineServicesReport{
		At:         r.now().Unix(),
		OS:         r.goos,
		Managers:   []proto.ServiceManagerStatus{},
		Services:   []proto.MachineServiceItem{},
		Containers: []proto.MachineServiceItem{},
	}

	switch r.goos {
	case "linux":
		status, items := r.listSystemd(ctx)
		report.Managers = append(report.Managers, status)
		report.Services = append(report.Services, items...)
	case "darwin":
		status, items := r.listLaunchd(ctx)
		report.Managers = append(report.Managers, status)
		report.Services = append(report.Services, items...)
	default:
		report.Managers = append(report.Managers, proto.ServiceManagerStatus{
			Kind:        proto.ServiceKindSystemd,
			Available:   false,
			Unavailable: fmt.Sprintf("user service inspection is not supported on %s", r.goos),
		})
	}

	dockerStatus, containers := r.listDocker(ctx)
	report.Managers = append(report.Managers, dockerStatus)
	report.Containers = append(report.Containers, containers...)

	return report, nil
}

func (n *Node) machineServiceLogs(ctx context.Context, params json.RawMessage) (any, error) {
	var a proto.MachineServiceLogsParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, &rpc.Error{Code: "bad_request", Message: "invalid machine.service_logs params"}
		}
	}
	if err := proto.ValidateServiceID(a.Kind, a.ID); err != nil {
		return nil, &rpc.Error{Code: "bad_request", Message: err.Error()}
	}
	lines := proto.ClampServiceLogLines(a.Lines)
	r := n.servicesRunner()

	switch a.Kind {
	case proto.ServiceKindSystemd:
		if r.goos != "linux" {
			return nil, &rpc.Error{Code: "bad_request", Message: "systemd is only available on Linux"}
		}
		return r.systemdLogs(ctx, a.ID, lines)
	case proto.ServiceKindLaunchd:
		if r.goos != "darwin" {
			return nil, &rpc.Error{Code: "bad_request", Message: "launchd is only available on macOS"}
		}
		return r.launchdLogs(ctx, a.ID, lines)
	case proto.ServiceKindDocker:
		return r.dockerLogs(ctx, a.ID, lines)
	default:
		return nil, &rpc.Error{Code: "bad_request", Message: "invalid service kind"}
	}
}

func (n *Node) machineServiceAction(ctx context.Context, params json.RawMessage) (any, error) {
	var a proto.MachineServiceActionParams
	if len(params) == 0 || json.Unmarshal(params, &a) != nil {
		return nil, &rpc.Error{Code: "bad_request", Message: "invalid machine.service_action params"}
	}
	if err := proto.ValidateServiceID(a.Kind, a.ID); err != nil {
		return nil, &rpc.Error{Code: "bad_request", Message: err.Error()}
	}
	if err := proto.ValidateServiceAction(a.Action); err != nil {
		return nil, &rpc.Error{Code: "bad_request", Message: err.Error()}
	}
	if !a.Confirmed {
		return nil, &rpc.Error{Code: "bad_request", Message: "explicit confirmation is required"}
	}

	r := n.servicesRunner()
	if protected, note := isProtectedService(a.Kind, a.ID, a.ID); protected {
		return nil, &rpc.Error{Code: proto.CodeServiceProtected, Message: note}
	}

	switch a.Kind {
	case proto.ServiceKindSystemd:
		if r.goos != "linux" {
			return nil, &rpc.Error{Code: "bad_request", Message: "systemd is only available on Linux"}
		}
		return r.systemdAction(ctx, a.ID, a.Action)
	case proto.ServiceKindLaunchd:
		if r.goos != "darwin" {
			return nil, &rpc.Error{Code: "bad_request", Message: "launchd is only available on macOS"}
		}
		return r.launchdAction(ctx, a.ID, a.Action)
	case proto.ServiceKindDocker:
		return r.dockerAction(ctx, a.ID, a.Action)
	default:
		return nil, &rpc.Error{Code: "bad_request", Message: "invalid service kind"}
	}
}

// ── systemd (Linux user units) ──────────────────────────

func (r *machineServiceRunner) listSystemd(ctx context.Context) (proto.ServiceManagerStatus, []proto.MachineServiceItem) {
	status := proto.ServiceManagerStatus{Kind: proto.ServiceKindSystemd}
	if _, err := r.lookPath("systemctl"); err != nil {
		status.Unavailable = "systemctl not found"
		return status, nil
	}
	cctx, cancel := context.WithTimeout(ctx, serviceListTimeout)
	defer cancel()
	out, _, err := r.run(cctx, serviceCmdMaxBytes, "systemctl", "--user", "list-units",
		"--type=service,socket,timer", "--all", "--no-pager", "--no-legend", "--plain")
	if err != nil {
		status.Unavailable = firstErrorLine(out, err)
		return status, nil
	}
	status.Available = true
	items := parseSystemdUnits(string(out))
	sortServiceItems(items)
	if len(items) > proto.ServiceListMaxItems {
		items = items[:proto.ServiceListMaxItems]
	}
	return status, items
}

func parseSystemdUnits(output string) []proto.MachineServiceItem {
	var items []proto.MachineServiceItem
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		// Strip optional bullet glyph if systemd ignored --plain.
		line = strings.TrimLeft(line, "●* ")
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		unit, load, active, sub := fields[0], fields[1], fields[2], fields[3]
		if proto.ValidateServiceID(proto.ServiceKindSystemd, unit) != nil {
			continue
		}
		if load == "not-found" && active == "inactive" {
			continue
		}
		desc := ""
		if len(fields) > 4 {
			desc = strings.Join(fields[4:], " ")
		}
		state := normalizeSystemdState(active, sub)
		statusText := active
		if sub != "" {
			statusText = fmt.Sprintf("%s (%s)", active, sub)
		}
		protected, protectNote := isProtectedService(proto.ServiceKindSystemd, unit, unit)
		items = append(items, proto.MachineServiceItem{
			Kind:        proto.ServiceKindSystemd,
			ID:          unit,
			Name:        unit,
			State:       state,
			SubState:    sub,
			Status:      statusText,
			Description: desc,
			Protected:   protected,
			ProtectNote: protectNote,
		})
	}
	return items
}

func normalizeSystemdState(active, sub string) string {
	switch strings.ToLower(active) {
	case "active":
		if strings.ToLower(sub) == "failed" {
			return proto.ServiceStateFailed
		}
		return proto.ServiceStateRunning
	case "failed":
		return proto.ServiceStateFailed
	case "activating", "reloading":
		return proto.ServiceStateActivating
	case "inactive", "deactivating":
		return proto.ServiceStateStopped
	default:
		return proto.ServiceStateUnknown
	}
}

func (r *machineServiceRunner) systemdLogs(ctx context.Context, unit string, lines int) (proto.MachineServiceLogsResult, error) {
	res := proto.MachineServiceLogsResult{
		Kind:   proto.ServiceKindSystemd,
		ID:     unit,
		Lines:  lines,
		Source: "journalctl",
	}
	if _, err := r.lookPath("journalctl"); err != nil {
		res.Note = "journalctl not found"
		return res, nil
	}
	cctx, cancel := context.WithTimeout(ctx, serviceLogsTimeout)
	defer cancel()
	out, truncated, err := r.run(cctx, proto.ServiceLogsMaxBytes, "journalctl", "--user",
		"-u", unit, "-n", strconv.Itoa(lines), "--no-pager", "--output=short-iso")
	content, byteTrimmed := trimLogBytes(out, truncated)
	res.Content = content
	res.Truncated = byteTrimmed
	if err != nil {
		if cctx.Err() != nil {
			return proto.MachineServiceLogsResult{}, &rpc.Error{Code: "timeout", Message: "journalctl timed out"}
		}
		if strings.TrimSpace(content) == "" {
			res.Note = firstErrorLine(out, err)
		}
	}
	return res, nil
}

func (r *machineServiceRunner) systemdAction(ctx context.Context, unit, action string) (proto.MachineServiceActionResult, error) {
	if _, err := r.lookPath("systemctl"); err != nil {
		return proto.MachineServiceActionResult{}, errors.New("systemctl not found")
	}
	cctx, cancel := context.WithTimeout(ctx, serviceActionTimeout)
	defer cancel()
	out, _, err := r.run(cctx, 16*1024, "systemctl", "--user", action, "--", unit)
	if err != nil {
		if cctx.Err() != nil {
			return proto.MachineServiceActionResult{}, &rpc.Error{Code: "timeout", Message: "systemctl timed out"}
		}
		return proto.MachineServiceActionResult{}, errors.New(firstErrorLine(out, err))
	}
	return proto.MachineServiceActionResult{
		Kind:   proto.ServiceKindSystemd,
		ID:     unit,
		Action: action,
		OK:     true,
		Output: strings.TrimSpace(string(out)),
	}, nil
}

// ── launchd (macOS user agents) ─────────────────────────

func (r *machineServiceRunner) listLaunchd(ctx context.Context) (proto.ServiceManagerStatus, []proto.MachineServiceItem) {
	status := proto.ServiceManagerStatus{Kind: proto.ServiceKindLaunchd}
	if _, err := r.lookPath("launchctl"); err != nil {
		status.Unavailable = "launchctl not found"
		return status, nil
	}
	cctx, cancel := context.WithTimeout(ctx, serviceListTimeout)
	defer cancel()
	out, _, err := r.run(cctx, serviceCmdMaxBytes, "launchctl", "list")
	if err != nil {
		status.Unavailable = firstErrorLine(out, err)
		return status, nil
	}
	status.Available = true
	userLabels := r.userLaunchAgentLabels()
	items := parseLaunchdList(string(out), userLabels)
	sortServiceItems(items)
	if len(items) > proto.ServiceListMaxItems {
		items = items[:proto.ServiceListMaxItems]
	}
	return status, items
}

func (r *machineServiceRunner) userLaunchAgentLabels() map[string]bool {
	labels := map[string]bool{}
	if r.home == "" || r.readDir == nil {
		return labels
	}
	entries, err := r.readDir(filepath.Join(r.home, "Library", "LaunchAgents"))
	if err != nil {
		return labels
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".plist") {
			labels[strings.TrimSuffix(name, ".plist")] = true
		}
	}
	return labels
}

func parseLaunchdList(output string, userLabels map[string]bool) []proto.MachineServiceItem {
	var items []proto.MachineServiceItem
	seen := map[string]bool{}
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "PID") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pidStr, statusStr, label := fields[0], fields[1], fields[2]
		if proto.ValidateServiceID(proto.ServiceKindLaunchd, label) != nil {
			continue
		}
		// Keep user LaunchAgents and non-Apple third-party agents; skip Apple's
		// internal com.apple.* noise unless explicitly present in ~/Library/LaunchAgents.
		if strings.HasPrefix(label, "com.apple.") && !userLabels[label] {
			continue
		}
		// Skip anonymous application.xxx entries from GUI launches unless user-owned.
		if strings.HasPrefix(label, "application.") && !userLabels[label] {
			continue
		}
		seen[label] = true
		pid := 0
		if pidStr != "-" {
			pid, _ = strconv.Atoi(pidStr)
		}
		exitCode, _ := strconv.Atoi(statusStr)
		state := proto.ServiceStateStopped
		subState := "loaded"
		statusText := "loaded"
		if pid > 0 {
			state = proto.ServiceStateRunning
			subState = "running"
			statusText = fmt.Sprintf("PID %d", pid)
		} else if exitCode != 0 {
			state = proto.ServiceStateFailed
			subState = fmt.Sprintf("exit %d", exitCode)
			statusText = fmt.Sprintf("exited (%d)", exitCode)
		}
		protected, protectNote := isProtectedService(proto.ServiceKindLaunchd, label, label)
		items = append(items, proto.MachineServiceItem{
			Kind:        proto.ServiceKindLaunchd,
			ID:          label,
			Name:        label,
			State:       state,
			SubState:    subState,
			Status:      statusText,
			PID:         pid,
			Protected:   protected,
			ProtectNote: protectNote,
		})
	}
	// Also include unloaded user LaunchAgents present in ~/Library/LaunchAgents.
	for label := range userLabels {
		if seen[label] || proto.ValidateServiceID(proto.ServiceKindLaunchd, label) != nil {
			continue
		}
		protected, protectNote := isProtectedService(proto.ServiceKindLaunchd, label, label)
		items = append(items, proto.MachineServiceItem{
			Kind:        proto.ServiceKindLaunchd,
			ID:          label,
			Name:        label,
			State:       proto.ServiceStateStopped,
			SubState:    "unloaded",
			Status:      "unloaded",
			Protected:   protected,
			ProtectNote: protectNote,
		})
	}
	return items
}

var plistLogPathRe = lazyre.New(`(?s)<key>(?:StandardErrorPath|StandardOutPath)</key>\s*<string>([^<]+)</string>`)

func (r *machineServiceRunner) launchdLogs(ctx context.Context, label string, lines int) (proto.MachineServiceLogsResult, error) {
	res := proto.MachineServiceLogsResult{
		Kind:  proto.ServiceKindLaunchd,
		ID:    label,
		Lines: lines,
	}
	// Prefer the agent's configured StandardErrorPath/StandardOutPath from ~/Library/LaunchAgents/<label>.plist.
	if r.home != "" && r.readFile != nil {
		plistPath := filepath.Join(r.home, "Library", "LaunchAgents", label+".plist")
		if plistBytes, err := r.readFile(plistPath); err == nil {
			for _, m := range plistLogPathRe.FindAllStringSubmatch(string(plistBytes), -1) {
				logPath := strings.TrimSpace(m[1])
				if logPath == "" || strings.Contains(logPath, "..") {
					continue
				}
				if data, trunc, rerr := readTailFile(logPath, proto.ServiceLogsMaxBytes); rerr == nil {
					res.Source = "file"
					res.Content, res.Truncated = trimLogLines(string(data), lines, trunc)
					return res, nil
				}
			}
		}
	}
	if _, err := r.lookPath("log"); err != nil {
		res.Source = "log"
		res.Note = "no log file configured and macOS log command not found"
		return res, nil
	}
	cctx, cancel := context.WithTimeout(ctx, serviceLogsTimeout)
	defer cancel()
	predicate := fmt.Sprintf(`subsystem == "%s"`, label)
	out, truncated, err := r.run(cctx, proto.ServiceLogsMaxBytes, "log", "show", "--last", "1h",
		"--style", "compact", "--predicate", predicate)
	res.Source = "log"
	if err != nil {
		if cctx.Err() != nil {
			return proto.MachineServiceLogsResult{}, &rpc.Error{Code: "timeout", Message: "log show timed out"}
		}
		res.Note = firstErrorLine(out, err)
		return res, nil
	}
	content, byteTrimmed := trimLogBytes(out, truncated)
	res.Content, res.Truncated = trimLogLines(content, lines, byteTrimmed)
	return res, nil
}

func (r *machineServiceRunner) launchdAction(ctx context.Context, label, action string) (proto.MachineServiceActionResult, error) {
	if _, err := r.lookPath("launchctl"); err != nil {
		return proto.MachineServiceActionResult{}, errors.New("launchctl not found")
	}
	uid := 0
	if r.uid != nil {
		uid = r.uid()
	}
	target := fmt.Sprintf("gui/%d/%s", uid, label)
	var args []string
	switch action {
	case proto.ServiceActionStart:
		args = []string{"kickstart", target}
	case proto.ServiceActionRestart:
		args = []string{"kickstart", "-k", target}
	case proto.ServiceActionStop:
		args = []string{"kill", "SIGTERM", target}
	default:
		return proto.MachineServiceActionResult{}, &rpc.Error{Code: "bad_request", Message: "invalid service action"}
	}
	cctx, cancel := context.WithTimeout(ctx, serviceActionTimeout)
	defer cancel()
	out, _, err := r.run(cctx, 16*1024, "launchctl", args...)
	if err != nil {
		if cctx.Err() != nil {
			return proto.MachineServiceActionResult{}, &rpc.Error{Code: "timeout", Message: "launchctl timed out"}
		}
		return proto.MachineServiceActionResult{}, errors.New(firstErrorLine(out, err))
	}
	return proto.MachineServiceActionResult{
		Kind:   proto.ServiceKindLaunchd,
		ID:     label,
		Action: action,
		OK:     true,
		Output: strings.TrimSpace(string(out)),
	}, nil
}

// ── Docker containers ───────────────────────────────────

func (r *machineServiceRunner) listDocker(ctx context.Context) (proto.ServiceManagerStatus, []proto.MachineServiceItem) {
	status := proto.ServiceManagerStatus{Kind: proto.ServiceKindDocker}
	if _, err := r.lookPath("docker"); err != nil {
		status.Unavailable = "docker not found"
		return status, nil
	}
	cctx, cancel := context.WithTimeout(ctx, serviceListTimeout)
	defer cancel()
	out, _, err := r.run(cctx, serviceCmdMaxBytes, "docker", "ps", "-a", "--no-trunc",
		"--format", "{{.ID}}\t{{.Names}}\t{{.Image}}\t{{.State}}\t{{.Status}}")
	if err != nil {
		status.Unavailable = firstErrorLine(out, err)
		return status, nil
	}
	status.Available = true
	items := parseDockerContainers(string(out))
	sortServiceItems(items)
	if len(items) > proto.ServiceListMaxItems {
		items = items[:proto.ServiceListMaxItems]
	}
	return status, items
}

func parseDockerContainers(output string) []proto.MachineServiceItem {
	var items []proto.MachineServiceItem
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 5 {
			continue
		}
		fullID := strings.TrimSpace(parts[0])
		name := strings.TrimPrefix(strings.TrimSpace(parts[1]), "/")
		image := strings.TrimSpace(parts[2])
		rawState := strings.TrimSpace(parts[3])
		statusText := strings.TrimSpace(parts[4])
		id := fullID
		if len(id) > 12 {
			id = id[:12]
		}
		if proto.ValidateServiceID(proto.ServiceKindDocker, id) != nil {
			continue
		}
		if name == "" {
			name = id
		}
		protected, protectNote := isProtectedService(proto.ServiceKindDocker, id, name)
		items = append(items, proto.MachineServiceItem{
			Kind:        proto.ServiceKindDocker,
			ID:          id,
			Name:        name,
			State:       normalizeDockerState(rawState, statusText),
			SubState:    rawState,
			Status:      statusText,
			Description: image,
			Protected:   protected,
			ProtectNote: protectNote,
		})
	}
	return items
}

func normalizeDockerState(rawState, statusText string) string {
	switch strings.ToLower(rawState) {
	case "running":
		return proto.ServiceStateRunning
	case "restarting", "created":
		return proto.ServiceStateActivating
	case "dead":
		return proto.ServiceStateFailed
	case "paused":
		return proto.ServiceStateStopped
	case "exited":
		if strings.HasPrefix(statusText, "Exited (0)") {
			return proto.ServiceStateStopped
		}
		return proto.ServiceStateFailed
	default:
		return proto.ServiceStateUnknown
	}
}

func (r *machineServiceRunner) dockerLogs(ctx context.Context, id string, lines int) (proto.MachineServiceLogsResult, error) {
	res := proto.MachineServiceLogsResult{
		Kind:   proto.ServiceKindDocker,
		ID:     id,
		Lines:  lines,
		Source: "docker",
	}
	if _, err := r.lookPath("docker"); err != nil {
		res.Note = "docker not found"
		return res, nil
	}
	cctx, cancel := context.WithTimeout(ctx, serviceLogsTimeout)
	defer cancel()
	out, truncated, err := r.run(cctx, proto.ServiceLogsMaxBytes, "docker", "logs",
		"--tail", strconv.Itoa(lines), "--timestamps", "--", id)
	content, byteTrimmed := trimLogBytes(out, truncated)
	res.Content = content
	res.Truncated = byteTrimmed
	if err != nil {
		if cctx.Err() != nil {
			return proto.MachineServiceLogsResult{}, &rpc.Error{Code: "timeout", Message: "docker logs timed out"}
		}
		if strings.TrimSpace(content) == "" {
			res.Note = firstErrorLine(out, err)
		}
	}
	return res, nil
}

func (r *machineServiceRunner) dockerAction(ctx context.Context, id, action string) (proto.MachineServiceActionResult, error) {
	if _, err := r.lookPath("docker"); err != nil {
		return proto.MachineServiceActionResult{}, errors.New("docker not found")
	}
	// Resolve the container name before acting so Persona's own containers
	// (e.g. persona-postgres) cannot be controlled by passing a container ID.
	inspectCtx, inspectCancel := context.WithTimeout(ctx, serviceListTimeout)
	inspectOut, _, inspectErr := r.run(inspectCtx, 4096, "docker", "inspect", "--format", "{{.Name}}", "--", id)
	inspectCancel()
	if inspectErr == nil {
		resolvedName := strings.TrimPrefix(strings.TrimSpace(string(inspectOut)), "/")
		if protected, note := isProtectedService(proto.ServiceKindDocker, id, resolvedName); protected {
			return proto.MachineServiceActionResult{}, &rpc.Error{Code: proto.CodeServiceProtected, Message: note}
		}
	}

	cctx, cancel := context.WithTimeout(ctx, serviceActionTimeout)
	defer cancel()
	out, _, err := r.run(cctx, 16*1024, "docker", action, "--", id)
	if err != nil {
		if cctx.Err() != nil {
			return proto.MachineServiceActionResult{}, &rpc.Error{Code: "timeout", Message: "docker command timed out"}
		}
		return proto.MachineServiceActionResult{}, errors.New(firstErrorLine(out, err))
	}
	return proto.MachineServiceActionResult{
		Kind:   proto.ServiceKindDocker,
		ID:     id,
		Action: action,
		OK:     true,
		Output: strings.TrimSpace(string(out)),
	}, nil
}

// ── self-protection guardrails & helpers ────────────────

var protectedExactNames = map[string]bool{
	"personad":         true,
	"persona-hub":      true,
	"persona-node":     true,
	"persona-link":     true,
	"persona-manager":  true,
	"persona-screen":   true,
	"persona-postgres": true,
}

var protectedPrefixes = []string{
	"personad-",
	"persona-hub-",
	"persona-node-",
	"persona-link-",
	"persona-manager-",
	"persona-postgres-",
	"com.persona.",
}

// isProtectedService identifies Persona's own hub, node, link, manager, and
// database services/containers so lifecycle actions cannot sever the active
// Persona connection or corrupt hub state.
func isProtectedService(kind, id, name string) (bool, string) {
	for _, candidate := range []string{id, name} {
		norm := normalizeProtectedCandidate(kind, candidate)
		if norm == "" {
			continue
		}
		if protectedExactNames[norm] {
			return true, "Persona's own service cannot be stopped or restarted from here"
		}
		for _, prefix := range protectedPrefixes {
			if strings.HasPrefix(norm, prefix) {
				return true, "Persona's own service cannot be stopped or restarted from here"
			}
		}
	}
	return false, ""
}

func normalizeProtectedCandidate(kind, raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "/")
	if kind == proto.ServiceKindSystemd {
		for _, suf := range []string{".service", ".socket", ".timer", ".target", ".path", ".scope", ".mount"} {
			if strings.HasSuffix(s, suf) {
				s = strings.TrimSuffix(s, suf)
				break
			}
		}
		if at := strings.IndexByte(s, '@'); at > 0 {
			s = s[:at]
		}
	}
	return s
}

func sortServiceItems(items []proto.MachineServiceItem) {
	rank := func(state string) int {
		switch state {
		case proto.ServiceStateFailed:
			return 0
		case proto.ServiceStateRunning:
			return 1
		case proto.ServiceStateActivating:
			return 2
		case proto.ServiceStateStopped:
			return 3
		default:
			return 4
		}
	}
	sort.Slice(items, func(i, j int) bool {
		ri, rj := rank(items[i].State), rank(items[j].State)
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
}

func firstErrorLine(out []byte, err error) string {
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 200 {
				return line[:200]
			}
			return line
		}
	}
	if err != nil {
		return err.Error()
	}
	return "unavailable"
}

func trimLogBytes(out []byte, truncated bool) (string, bool) {
	if len(out) > proto.ServiceLogsMaxBytes {
		out = out[len(out)-proto.ServiceLogsMaxBytes:]
		truncated = true
	}
	if truncated {
		// Align to the next newline boundary so the first shown line isn't cut mid-UTF8/mid-line.
		if idx := bytes.IndexByte(out, '\n'); idx >= 0 && idx+1 < len(out) {
			out = out[idx+1:]
		}
	}
	return strings.ToValidUTF8(string(out), ""), truncated
}

func trimLogLines(content string, maxLines int, truncated bool) (string, bool) {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return "", truncated
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		truncated = true
	}
	return strings.Join(lines, "\n"), truncated
}

func readTailFile(path string, maxBytes int64) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	truncated := false
	size := info.Size()
	if size > maxBytes {
		if _, err := f.Seek(size-maxBytes, io.SeekStart); err != nil {
			return nil, false, err
		}
		truncated = true
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return nil, false, err
	}
	return data, truncated, nil
}
