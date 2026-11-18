package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	externalAdapterConfigEnv   = "PERSONA_LAUNCHER_ADAPTER_CONFIG"
	externalAdapterConfigLimit = 64 << 10
	externalAdapterStdoutLimit = 1 << 20
	externalAdapterStderrLimit = 8 << 10
	externalAdapterTargetLimit = 256
	externalAdapterModelLimit  = 4096
	externalAdapterTimeout     = 5 * time.Second
	externalAdapterTTL         = time.Minute
	externalAdapterRetryMin    = 5 * time.Second
	externalAdapterRetryMax    = time.Minute
	externalAdapterPipeGrace   = 100 * time.Millisecond
	externalAdapterGroupSweep  = time.Second
)

// A registration is read once, from the node's startup environment, never from the hub.
// Its executable paths and fixed arguments remain local even when its inventory is advertised.
type externalAdapterConfig struct {
	Version       int      `json:"version"`
	ID            string   `json:"id"`
	Command       []string `json:"command"`
	NativeProgram string   `json:"native_program"`
}

type externalAdapterRefresh struct {
	done       chan struct{}
	cancel     context.CancelFunc
	waiters    int
	background bool
	err        error // published by closing done
}

type externalAdapterRegistry struct {
	config externalAdapterConfig
	dir    string
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	targets  []proto.ExternalTarget // immutable until replaced, only read under mu
	byName   map[string]int
	at       time.Time
	lastErr  error
	failures uint
	nextTry  time.Time
	flight   *externalAdapterRefresh
	closed   bool
	onChange func()
	workers  sync.WaitGroup
	now      func() time.Time
	timeout  time.Duration
	ttl      time.Duration
}

// loadExternalAdapterRegistry has no discovery fallback. In particular, a disabled adapter
// neither opens a file nor looks for an executable on PATH.
func loadExternalAdapterRegistry() (*externalAdapterRegistry, error) {
	name := os.Getenv(externalAdapterConfigEnv)
	if name == "" {
		return nil, nil
	}
	if !externalAdapterPlatformSupported() {
		return nil, externalAdapterFailure("adapter_platform_unsupported")
	}
	f, canonical, err := externalAdapterOpenConfig(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || before.Size() > externalAdapterConfigLimit {
		return nil, externalAdapterFailure("adapter_config_conflict")
	}
	data, err := io.ReadAll(io.LimitReader(f, externalAdapterConfigLimit+1))
	if err != nil || len(data) > externalAdapterConfigLimit {
		return nil, externalAdapterFailure("adapter_config_conflict")
	}
	after, err := f.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, externalAdapterFailure("adapter_config_conflict")
	}
	var config externalAdapterConfig
	if err := externalAdapterDecode(data, &config); err != nil || config.Version != 1 ||
		!externalAdapterToken(config.ID, 64, "_-.") || len(config.Command) == 0 || len(config.Command) > 32 {
		return nil, externalAdapterFailure("adapter_config_conflict")
	}
	if err := externalAdapterPaths(&config, true); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &externalAdapterRegistry{
		config: config, dir: filepath.Dir(canonical), ctx: ctx, cancel: cancel,
		byName: make(map[string]int), now: time.Now, timeout: externalAdapterTimeout, ttl: externalAdapterTTL,
	}, nil
}

// SetOnChange installs a nonblocking directory notification. It is called without mu held,
// after a changed snapshot (including a failure's stale status) has been published.
func (r *externalAdapterRegistry) SetOnChange(fn func()) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.onChange = fn
	r.mu.Unlock()
}

// Close cancels and joins inventory work. Commands returned by command belong to their
// caller, which owns native initialization, the process lifetime and session-scope checks.
func (r *externalAdapterRegistry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.workers.Wait()
}

// launchers never waits on an ordinary directory read, even on a cold cache. An explicit
// refresh joins the current inventory request; failure leaves the previous entries stale.
func (r *externalAdapterRegistry) launchers(ctx context.Context, refresh bool) []proto.Launcher {
	if r == nil {
		return nil
	}
	_ = r.refresh(ctx, refresh)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.launchersLocked(r.now())
}

func (r *externalAdapterRegistry) launchersLocked(now time.Time) []proto.Launcher {
	out := make([]proto.Launcher, 0, len(r.targets))
	for _, target := range r.targets {
		external := r.externalLocked(target, now)
		out = append(out, proto.Launcher{
			Name: proto.ExternalLauncherName(r.config.ID, target.ID), Source: proto.ExternalLauncherSource,
			Agent: "claude", External: &external,
			Assistant: target.Engine == "claude-code" && slices.Contains(target.Capabilities.Contexts, "main-chat") &&
				slices.Contains(target.Capabilities.Runtimes, "cli") && slices.Contains(target.Capabilities.Platforms, "posix"),
		})
	}
	return out
}

func (r *externalAdapterRegistry) externalLocked(target proto.ExternalTarget, now time.Time) proto.ExternalLauncher {
	return proto.ExternalLauncher{
		AdapterID: r.config.ID, ExternalTarget: externalAdapterCloneTarget(target),
		Stale: r.closed || r.lastErr != nil || r.at.IsZero() || !now.Before(r.at.Add(r.ttl)),
	}
}

// lookup uses the reserved identity, not a display name or any other launcher's source.
func (r *externalAdapterRegistry) lookup(name string) (proto.ExternalLauncher, bool) {
	if r == nil {
		return proto.ExternalLauncher{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.byName[name]
	if !ok {
		return proto.ExternalLauncher{}, false
	}
	return r.externalLocked(r.targets[i], r.now()), true
}

func (r *externalAdapterRegistry) models(ctx context.Context, name string, refresh bool) (proto.LauncherModels, error) {
	if r == nil {
		return proto.LauncherModels{}, externalAdapterFailure("adapter_target_not_found")
	}
	if err := r.refresh(ctx, refresh); err != nil && refresh {
		return proto.LauncherModels{}, err
	}
	target, ok := r.lookup(name)
	if !ok {
		return proto.LauncherModels{}, externalAdapterFailure("adapter_target_not_found")
	}
	return proto.LauncherModels{Agent: "claude", Models: target.Models}, nil
}

// command only prepares a new process. A saved binding must still match the local
// registration and latest revision; it is never silently upgraded to a new target.
func (r *externalAdapterRegistry) command(name string, binding *proto.ExternalExecution, model, cwd string, args, env []string) (*exec.Cmd, error) {
	if r == nil || !externalAdapterPlatformSupported() {
		return nil, externalAdapterFailure("adapter_unavailable")
	}
	if binding == nil || binding.Scope != proto.ExternalMainChatScope || binding.SessionID == "" {
		return nil, externalAdapterFailure("adapter_purpose_unsupported")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.byName[name]
	if !ok {
		return nil, externalAdapterFailure("adapter_target_not_found")
	}
	target := r.externalLocked(r.targets[i], r.now())
	if binding.AdapterID != r.config.ID || binding.TargetID != target.ID ||
		name != proto.ExternalLauncherName(binding.AdapterID, binding.TargetID) {
		return nil, externalAdapterFailure("adapter_target_not_found")
	}
	if binding.Revision != target.Revision {
		return nil, externalAdapterFailure("adapter_revision_mismatch")
	}
	if target.Stale {
		return nil, externalAdapterFailure("adapter_unavailable")
	}
	if target.UnavailableCode != "" {
		return nil, externalAdapterFailure(target.UnavailableCode)
	}
	if target.CredentialState == "missing" {
		return nil, externalAdapterFailure("adapter_credentials_missing")
	}
	if !slices.ContainsFunc(target.Models, func(m proto.AgentModel) bool { return m.ID == model }) {
		return nil, externalAdapterFailure("adapter_model_not_found")
	}
	config := r.config
	if err := externalAdapterPaths(&config, false); err != nil {
		return nil, err
	}
	argv := append(slices.Clone(config.Command[1:]), "exec", "--protocol", "1", "--context", "main-chat",
		"--runtime", "cli", "--target", target.ID, "--model", model, "--revision", binding.Revision,
		"--native-program", config.NativeProgram, "--")
	argv = append(argv, args...)
	cmd := exec.Command(config.Command[0], argv...)
	cmd.Dir, cmd.Env = cwd, slices.Clone(env)
	return cmd, nil
}

// refresh is a single flight with stale-while-revalidate reads and bounded failure
// backoff. Explicit refresh bypasses TTL/backoff but still joins an existing flight.
// A canceled sole waiter cancels and joins its process; background work belongs to Close.
func (r *externalAdapterRegistry) refresh(ctx context.Context, wait bool) error {
	if err := ctx.Err(); err != nil {
		return externalAdapterContextError(err)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return externalAdapterFailure("adapter_unavailable")
	}
	now := r.now()
	if !wait && r.lastErr == nil && !r.at.IsZero() && now.Before(r.at.Add(r.ttl)) {
		r.mu.Unlock()
		return nil
	}
	f := r.flight
	if f == nil {
		if !wait && now.Before(r.nextTry) {
			err := r.lastErr
			r.mu.Unlock()
			return err
		}
		fctx, cancel := context.WithCancel(r.ctx)
		f = &externalAdapterRefresh{done: make(chan struct{}), cancel: cancel}
		r.flight = f
		r.workers.Add(1)
		go r.readInventory(fctx, f)
	}
	if wait {
		f.waiters++
	} else {
		f.background = true
	}
	r.mu.Unlock()
	if !wait {
		return nil
	}
	var err error
	select {
	case <-f.done:
		err = f.err
	case <-ctx.Done():
		err = externalAdapterContextError(ctx.Err())
	}
	r.mu.Lock()
	f.waiters--
	join := r.flight == f && f.waiters == 0 && !f.background
	if join {
		f.cancel()
	}
	r.mu.Unlock()
	if join {
		<-f.done
	}
	return err
}

func (r *externalAdapterRegistry) readInventory(ctx context.Context, f *externalAdapterRefresh) {
	defer r.workers.Done()
	defer f.cancel()
	targets, err := r.inventory(ctx)
	r.mu.Lock()
	now := r.now()
	before := r.launchersLocked(now)
	if err == nil {
		r.targets, r.byName = targets, make(map[string]int, len(targets))
		for i, t := range targets {
			r.byName[proto.ExternalLauncherName(r.config.ID, t.ID)] = i
		}
		r.at, r.lastErr, r.failures, r.nextTry = now, nil, 0, time.Time{}
	} else {
		r.lastErr = err
		r.failures++
		delay := min(externalAdapterRetryMin*time.Duration(1<<min(r.failures-1, 4)), externalAdapterRetryMax)
		r.nextTry = now.Add(delay)
	}
	changed := !reflect.DeepEqual(before, r.launchersLocked(now))
	fn := r.onChange
	if !changed || r.closed {
		fn = nil
	}
	f.err = err
	r.flight = nil
	close(f.done)
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (r *externalAdapterRegistry) inventory(parent context.Context) ([]proto.ExternalTarget, error) {
	config := r.config
	if err := externalAdapterPaths(&config, false); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	args := append(slices.Clone(config.Command[1:]), "inventory", "--protocol", "1", "--offline")
	cmd := exec.CommandContext(ctx, config.Command[0], args...)
	cmd.Dir = r.dir
	cmd.SysProcAttr = osproc.GroupAttr()
	cmd.Cancel = func() error { return osproc.StopGroup(cmd.Process.Pid, true) }
	cmd.WaitDelay = externalAdapterPipeGrace
	stdout := &externalAdapterBuffer{limit: externalAdapterStdoutLimit, cancel: cancel}
	stderr := &externalAdapterBuffer{limit: externalAdapterStderrLimit}
	defer func() { clear(stderr.data) }()
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// nil Stdin is the null device: inventory never consumes the conversation's input.
	if err := cmd.Start(); err != nil {
		return nil, externalAdapterContextError(ctx.Err())
	}
	err := cmd.Wait()
	// A successful parent must not leave a child holding its output or other resources.
	if !osproc.SweepGroup(cmd.Process.Pid, externalAdapterGroupSweep) {
		return nil, externalAdapterFailure("adapter_unavailable")
	}
	if stdout.overflow {
		return nil, externalAdapterFailure("adapter_inventory_invalid")
	}
	if ctx.Err() != nil {
		return nil, externalAdapterContextError(ctx.Err())
	}
	if err != nil {
		return nil, externalAdapterFailure("adapter_unavailable")
	}
	var inventory proto.ExternalInventory
	if externalAdapterDecode(stdout.data, &inventory) != nil {
		return nil, externalAdapterFailure("adapter_inventory_invalid")
	}
	if inventory.Protocol != proto.ExternalAdapterProtocol {
		return nil, externalAdapterFailure("adapter_protocol_unsupported")
	}
	if err := externalAdapterValidateInventory(&inventory); err != nil {
		return nil, err
	}
	return inventory.Targets, nil
}

// externalAdapterBuffer drains both pipes without retaining unbounded output. Stderr is
// deliberately never made part of a returned error, log, or public advertisement.
type externalAdapterBuffer struct {
	data     []byte
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *externalAdapterBuffer) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, b.limit-len(b.data))
	b.data = append(b.data, p[:keep]...)
	if keep < n && !b.overflow {
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
	}
	return n, nil
}

// No decoder, filesystem, exec, or adapter error is interpolated into a public error.
func externalAdapterFailure(code string) error { return &rpc.Error{Code: code, Message: code} }

func externalAdapterContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return externalAdapterFailure("adapter_timeout")
	}
	return externalAdapterFailure("adapter_unavailable")
}

func externalAdapterKnownCode(code string) bool {
	switch code {
	case "", "adapter_protocol_unsupported", "adapter_runtime_unsupported", "adapter_purpose_unsupported",
		"adapter_platform_unsupported", "adapter_config_conflict", "adapter_credentials_missing", "adapter_target_not_found",
		"adapter_model_not_found", "adapter_revision_mismatch", "adapter_timeout", "adapter_inventory_invalid", "adapter_unavailable":
		return true
	}
	return false
}

// Decode the public DTO itself, not an extensible map: paths, URLs, env and args have no
// place in inventory. Also reject duplicate members, extra JSON values and invalid UTF-8.
func externalAdapterDecode(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return errors.New("invalid adapter JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := externalAdapterJSONValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("invalid adapter JSON")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

func externalAdapterJSONValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("invalid adapter JSON")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	kind, container := t.(json.Delim)
	if !container {
		return nil
	}
	keys := map[string]bool{}
	for d.More() {
		if kind == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || s != strings.ToLower(s) || keys[s] {
				return errors.New("invalid adapter JSON")
			}
			keys[s] = true
		}
		if err := externalAdapterJSONValue(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

// Paths are canonicalized once and rechecked before each process is prepared. A later
// symlink replacement cannot redirect an already loaded registration to a different path.
func externalAdapterPaths(config *externalAdapterConfig, normalize bool) error {
	check := func(path string, executable bool) (string, error) {
		canonical, err := externalAdapterTrustedPath(path, executable, true)
		if err != nil || (!normalize && path != canonical) {
			return "", externalAdapterFailure("adapter_config_conflict")
		}
		return canonical, nil
	}
	// Clone before normalizing: the registry's argv is immutable and shared by readers.
	config.Command = slices.Clone(config.Command)
	var err error
	config.NativeProgram, err = check(config.NativeProgram, true)
	if err != nil {
		return err
	}
	for i, arg := range config.Command {
		if arg == "" || len(arg) > 4096 || strings.ContainsFunc(arg, unicode.IsControl) || !utf8.ValidString(arg) {
			return externalAdapterFailure("adapter_config_conflict")
		}
		if i == 0 || filepath.IsAbs(arg) {
			config.Command[i], err = check(arg, i == 0)
			if err != nil {
				return err
			}
		} else if strings.ContainsAny(arg, `/\`) { // a script must be absolute, not relative to the node's cwd
			return externalAdapterFailure("adapter_config_conflict")
		}
	}
	return nil
}

func externalAdapterValidateInventory(inv *proto.ExternalInventory) error {
	invalid := func() error { return externalAdapterFailure("adapter_inventory_invalid") }
	if inv.Targets == nil || len(inv.Targets) > externalAdapterTargetLimit {
		return invalid()
	}
	ids, count := map[string]bool{}, 0
	for i := range inv.Targets {
		t := &inv.Targets[i]
		if !externalAdapterToken(t.ID, 128, "_-.:") || ids[t.ID] || !externalAdapterText(t.Name, 160, false) ||
			!externalAdapterToken(t.Engine, 32, "_-.") || !externalAdapterToken(t.Revision, 160, "_-.:") ||
			!externalAdapterKnownCode(t.UnavailableCode) || t.Models == nil {
			return invalid()
		}
		ids[t.ID] = true
		switch t.CredentialState {
		case "configured", "missing", "unknown":
		default:
			return invalid()
		}
		caps := &t.Capabilities
		if !externalAdapterCapabilities(caps.Contexts) || !externalAdapterCapabilities(caps.Runtimes) ||
			!externalAdapterCapabilities(caps.Platforms) {
			return invalid()
		}
		caps.Contexts = externalAdapterIntersection(caps.Contexts, "main-chat")
		caps.Runtimes = externalAdapterIntersection(caps.Runtimes, "cli")
		caps.Platforms = externalAdapterIntersection(caps.Platforms, "posix")
		if t.UnavailableCode == "" {
			switch {
			case t.Engine != "claude-code", len(caps.Runtimes) == 0:
				t.UnavailableCode = "adapter_runtime_unsupported"
			case len(caps.Contexts) == 0:
				t.UnavailableCode = "adapter_purpose_unsupported"
			case len(caps.Platforms) == 0:
				t.UnavailableCode = "adapter_platform_unsupported"
			case t.CredentialState == "missing":
				t.UnavailableCode = "adapter_credentials_missing"
			}
		}
		models, defaults := map[string]bool{}, 0
		for _, m := range t.Models {
			count++
			if count > externalAdapterModelLimit || models[m.ID] || !externalAdapterValidateModel(m) {
				return invalid()
			}
			models[m.ID] = true
			if m.Default {
				defaults++
			}
		}
		if defaults > 1 {
			return invalid()
		}
	}
	return nil
}

func externalAdapterCapabilities(values []string) bool {
	if values == nil || len(values) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !externalAdapterToken(value, 32, "_-.") || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func externalAdapterIntersection(values []string, supported string) []string {
	if slices.Contains(values, supported) {
		return []string{supported}
	}
	return []string{}
}

func externalAdapterValidateModel(m proto.AgentModel) bool {
	if !externalAdapterModelID(m.ID) || !externalAdapterText(m.Name, 160, false) ||
		!externalAdapterText(m.Description, 512, true) || (m.Resolved != "" && !externalAdapterModelID(m.Resolved)) ||
		len(m.Efforts) > 8 || len(m.ServiceTiers) > 16 || m.Custom {
		return false
	}
	seen := map[string]bool{}
	for _, effort := range m.Efforts {
		if !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, effort) || seen[effort] {
			return false
		}
		seen[effort] = true
	}
	if m.DefaultEffort != "" && !seen[m.DefaultEffort] {
		return false
	}
	clear(seen)
	for _, tier := range m.ServiceTiers {
		if !externalAdapterToken(tier.ID, 64, "_-.") || seen[tier.ID] || !externalAdapterText(tier.Name, 160, false) ||
			!externalAdapterText(tier.Description, 512, true) {
			return false
		}
		seen[tier.ID] = true
	}
	if p := m.Price; p != nil {
		valid := func(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1e6 }
		if !valid(p.Input) || !valid(p.Output) {
			return false
		}
		for _, n := range []*float64{p.CacheRead, p.CacheWrite, p.CacheWrite1h} {
			if n != nil && !valid(*n) {
				return false
			}
		}
	}
	return true
}

func externalAdapterModelID(value string) bool {
	if !proto.ValidModelID(value) || strings.Contains(value, "//") ||
		strings.Contains(strings.ToLower(value), "www.") {
		return false
	}
	for _, scheme := range []string{"http:", "https:", "file:", "mailto:", "data:", "ftp:", "ssh:"} {
		if strings.HasPrefix(strings.ToLower(value), scheme) {
			return false
		}
	}
	return true
}

func externalAdapterToken(value string, limit int, extra string) bool {
	if value == "" || len(value) > limit || !externalAdapterAlnum(value[0]) {
		return false
	}
	for i := range len(value) {
		if !externalAdapterAlnum(value[i]) && !strings.ContainsRune(extra, rune(value[i])) {
			return false
		}
	}
	return true
}

func externalAdapterAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func externalAdapterText(value string, limit int, optional bool) bool {
	if value == "" {
		return optional
	}
	if len(value) > limit || strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "=\\`$<>") || strings.Contains(value, "--") {
		return false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"://", "www.", "https:", "http:", "file:", "mailto:", "data:"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	for _, c := range value {
		if c == utf8.RuneError || !unicode.IsPrint(c) || unicode.Is(unicode.Cf, c) {
			return false
		}
	}
	return true
}

func externalAdapterCloneTarget(t proto.ExternalTarget) proto.ExternalTarget {
	t.Capabilities.Contexts = slices.Clone(t.Capabilities.Contexts)
	t.Capabilities.Runtimes = slices.Clone(t.Capabilities.Runtimes)
	t.Capabilities.Platforms = slices.Clone(t.Capabilities.Platforms)
	t.Models = slices.Clone(t.Models)
	for i := range t.Models {
		m := &t.Models[i]
		m.Efforts = slices.Clone(m.Efforts)
		m.ServiceTiers = slices.Clone(m.ServiceTiers)
		if m.Price != nil {
			p := *m.Price
			for _, n := range []**float64{&p.CacheRead, &p.CacheWrite, &p.CacheWrite1h} {
				if *n != nil {
					v := **n
					*n = &v
				}
			}
			m.Price = &p
		}
	}
	return t
}
