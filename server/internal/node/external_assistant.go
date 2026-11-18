package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// externalRefusal never includes an adapter's raw stderr, configuration or command.
func externalRefusal(ctx context.Context, code string) error {
	return refusal(code, i18n.T(i18n.From(ctx), "external-adapter."+strings.TrimPrefix(code, "adapter_")), nil)
}

func externalAdapterError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var wire *rpc.Error
	if errors.As(err, &wire) && strings.HasPrefix(wire.Code, "adapter_") {
		if code := externalStderrCode(wire.Code); code == wire.Code {
			return externalRefusal(ctx, code)
		}
	}
	return externalRefusal(ctx, "adapter_unavailable")
}

func (n *Node) externalRuntime(id, launcher, requested string) string {
	if launcher == "" && n.state != nil {
		if st, ok := n.state.get(id); ok {
			launcher = st.Launcher
		}
	}
	if proto.IsExternalLauncher(launcher) {
		return requested // do not turn an unknown runtime into the CLI before checking it
	}
	return "" // ordinary conversations always follow the native CLI policy
}

func (n *Node) externalSessionRuntime(id, launcher, requested string, explicit bool) string {
	return n.externalRuntime(id, launcher, requested)
}

// checkExternalAssistant checks execution authority, not merely catalog visibility.
// The manager supplies this binding only for the actual main chat's persisted pick.
func (n *Node) checkExternalAssistant(ctx context.Context, id, launcher string, execution modelExecution, spare bool) error {
	if !proto.IsExternalLauncher(launcher) {
		if execution.External != nil {
			return externalRefusal(ctx, "adapter_scope_required")
		}
		return nil
	}
	binding := execution.External
	if spare || !execution.IsMain || binding == nil || binding.Scope != proto.ExternalMainChatScope ||
		id == "" || binding.SessionID != id || n.guestOf(id) != nil || guestFrom(ctx) != nil {
		return externalRefusal(ctx, "adapter_scope_required")
	}
	if execution.Runtime != "" && execution.Runtime != "cli" {
		return externalRefusal(ctx, "adapter_runtime_unsupported")
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" || runtime.GOOS == "js" {
		return externalRefusal(ctx, "adapter_platform_unsupported")
	}
	if n.external == nil {
		return externalRefusal(ctx, "adapter_disabled")
	}
	target, ok := n.external.lookup(launcher)
	if !ok {
		// An execution can arrive before any directory read on a new resident
		// connection. Join one bounded inventory load, then validate the result.
		if err := n.external.refresh(ctx, true); err != nil {
			return externalAdapterError(ctx, err)
		}
		target, ok = n.external.lookup(launcher)
	}
	if !ok || binding.AdapterID != target.AdapterID || binding.TargetID != target.ID ||
		proto.ExternalLauncherName(binding.AdapterID, binding.TargetID) != launcher {
		return externalRefusal(ctx, "adapter_target_not_found")
	}
	if target.Engine != "claude-code" || !slices.Contains(target.Capabilities.Contexts, "main-chat") ||
		!slices.Contains(target.Capabilities.Runtimes, "cli") || !slices.Contains(target.Capabilities.Platforms, "posix") {
		return externalRefusal(ctx, "adapter_runtime_unsupported")
	}
	// Reuse the existing sandbox switch, but fail closed when its value or file
	// cannot be read. An adapter must not inherit wrapAgent's best-effort fallback.
	if value, set := identity.LookupEnv(agentSandboxEnv); set {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "0", "off", "false", "no", "1", "on", "true", "yes":
		default:
			return externalRefusal(ctx, "adapter_sandbox_unsupported")
		}
	} else if _, err := os.Stat(filepath.Join(n.dir, sandbox.AgentOnFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return externalRefusal(ctx, "adapter_sandbox_unsupported")
	}
	if on, _ := agentSandboxOn(n.dir); on {
		return externalRefusal(ctx, "adapter_sandbox_unsupported")
	}
	return nil
}

func sameExternalProcessModel(p *assistantProc, launcher, model string) bool {
	if p == nil || !p.alive() || !proto.IsExternalLauncher(launcher) || p.launcher != launcher {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.model.Model == model && p.model.External != nil
}

// pinExternalExecution keeps catalog refreshes from restarting an existing process.
// An explicit change of model, effort, launcher, runtime or resolved role policy still gets its normal restart.
func pinExternalExecution(p *assistantProc, launcher string, execution modelExecution) modelExecution {
	if p == nil || !p.alive() || p.launcher != launcher || execution.External == nil {
		return execution
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.model
	if old.External == nil || old.ConfigDir != execution.ConfigDir || old.Origin != execution.Origin ||
		old.External.Scope != execution.External.Scope || old.External.SessionID != execution.External.SessionID ||
		old.External.AdapterID != execution.External.AdapterID || old.External.TargetID != execution.External.TargetID {
		return execution
	}
	// Compare every effective fingerprint field, including new manager policy fields. Only the directory revision may
	// differ; per-request metadata and CLI's two accepted spellings are not a process policy change.
	oldPolicy, nextPolicy := old, execution
	oldPolicy.External, nextPolicy.External = nil, nil
	oldRaw, _ := json.Marshal(oldPolicy)
	nextRaw, _ := json.Marshal(nextPolicy)
	if string(oldRaw) != string(nextRaw) {
		return execution
	}
	binding := *old.External
	execution.External = &binding
	return execution
}

func (n *Node) externalCLICommand(ctx context.Context, id, launcher, cwd string, execution modelExecution, args, env []string) (*exec.Cmd, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, externalAdapterError(ctx, err)
	}
	if err := n.checkExternalAssistant(ctx, id, launcher, execution, false); err != nil {
		return nil, nil, err
	}
	if err := n.externalSettingsModeCheck(cwd, args, env); err != nil {
		return nil, nil, err
	}
	if _, err := n.external.models(ctx, launcher, true); err != nil {
		return nil, nil, externalAdapterError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, externalAdapterError(ctx, err)
	}
	cmd, err := n.external.command(launcher, execution.External, execution.Model, cwd, args, env)
	if err != nil {
		return nil, nil, externalAdapterError(context.Background(), err)
	}
	// The node owns this directory so cleanup still happens after a forced group
	// termination, even when the adapter cannot run its own finally block.
	dir, err := os.MkdirTemp("", "persona-adapter-")
	if err != nil {
		return nil, nil, externalRefusal(context.Background(), "adapter_unavailable")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		cleanup()
		return nil, nil, externalRefusal(context.Background(), "adapter_unavailable")
	}
	boundary := len(cmd.Args) - len(args) - 1
	if boundary < 1 || cmd.Args[boundary] != "--" {
		cleanup()
		return nil, nil, externalRefusal(context.Background(), "adapter_config_conflict")
	}
	prefix := append([]string(nil), cmd.Args[:boundary]...)
	prefix = append(prefix, "--settings-output", filepath.Join(canonical, "settings.json"))
	cmd.Args = append(prefix, cmd.Args[boundary:]...)
	return cmd, cleanup, nil
}

// externalSettingsModeCheck refuses native provider switches in every settings
// layer the CLI can load. It does not disable user/project settings or proxy policy.
func (n *Node) externalSettingsModeCheck(cwd string, args, env []string) error {
	conflict := func() error { return externalRefusal(context.Background(), "adapter_config_conflict") }
	modes := []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}
	for _, key := range modes {
		for _, entry := range env {
			if strings.HasPrefix(entry, key+"=") {
				return conflict()
			}
		}
	}
	profile := envValue(env, "CLAUDE_CONFIG_DIR")
	if profile == "" {
		profile = filepath.Join(n.home, ".claude")
	}
	// Managed and remotely cached policies are not fully interpretable by this
	// bounded adapter. Refuse their presence rather than read or bypass them.
	for _, path := range claudePolicyPaths(profile) {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return conflict()
		}
	}
	files := []string{filepath.Join(profile, "settings.json")}
	if cwd != "" {
		for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
			files = append(files, filepath.Join(dir, ".claude", "settings.json"), filepath.Join(dir, ".claude", "settings.local.json"))
			if parent := filepath.Dir(dir); parent == dir {
				break
			}
		}
	}
	files = append(files, settingsArgs(args)...)
	for _, path := range files {
		path = proto.ExpandHome(path, n.home)
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return conflict()
		}
		file, _, err := externalAdapterOpenConfig(path)
		if err != nil {
			return conflict()
		}
		stat, statErr := file.Stat()
		if statErr != nil || !stat.Mode().IsRegular() || stat.Size() > 1<<20 {
			file.Close()
			return conflict()
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		file.Close()
		if readErr != nil || len(data) > 1<<20 {
			return conflict()
		}
		var settings struct {
			Env map[string]json.RawMessage `json:"env"`
		}
		if json.Unmarshal(data, &settings) != nil {
			return conflict()
		}
		for _, key := range modes {
			if _, exists := settings.Env[key]; exists {
				return conflict()
			}
		}
	}
	return nil
}

// externalStderrCode accepts only protocol diagnostics. Native or adapter stderr
// may contain credentials, so unrecognized lines are never retained or logged.
func externalStderrCode(line string) string {
	switch strings.TrimSpace(line) {
	case "adapter_source_options_ignored":
		return ""
	case "adapter_disabled", "adapter_scope_required", "adapter_sandbox_unsupported", "adapter_probe_unsupported", "adapter_busy",
		"adapter_protocol_unsupported", "adapter_runtime_unsupported", "adapter_purpose_unsupported",
		"adapter_platform_unsupported", "adapter_config_conflict", "adapter_credentials_missing",
		"adapter_target_not_found", "adapter_model_not_found", "adapter_revision_mismatch",
		"adapter_timeout", "adapter_inventory_invalid", "adapter_unavailable":
		return strings.TrimSpace(line)
	default:
		return "adapter_unavailable"
	}
}
