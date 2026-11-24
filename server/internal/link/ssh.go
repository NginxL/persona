package link

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/osproc"
)

// KindSSH is the route kind reported in Status when the link runs through the SSH tunnel.
const KindSSH = "ssh"

// DirectRetryEvery is how often a link running over SSH (without Always) probes the hub's direct addresses to switch
// back when one answers again (docs/operations/network.md "Connecting to hub through SSH").
const DirectRetryEvery = 2 * time.Minute

// DefaultTargetPort is the fallback hub TLS port when neither a pairing link nor saved endpoints carry one: the port
// `personad hub setup` gives public.tls_listen. The hub's HTTP port (18660) would never answer a TLS handshake.
const DefaultTargetPort = "18662"

// SSH is the optional SSH tunnel in link.json: persona-link starts the system ssh client once with -N -L so the link
// and every stream share one SSH session, while TLS still runs end to end to the hub's TLS port (or HTTPS proxy).
type SSH struct {
	// Server is [user@]host[:port], or a host alias from ~/.ssh/config.
	Server string `json:"server"`
	// Target is host:port of the hub as Server sees it (default 127.0.0.1:<TLS port>).
	Target string `json:"target"`
	// Always: only the tunnel is used; direct hub addresses are not tried.
	Always bool `json:"always,omitempty"`
}

// Equal reports whether a and b describe the same SSH tunnel settings.
func (a *SSH) Equal(b *SSH) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Server == b.Server && a.Target == b.Target && a.Always == b.Always
}

func cloneSSH(s *SSH) *SSH {
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}

// Codes for SSHError and SSHStatusError.
const (
	SSHNoBinary          = "no_ssh"
	SSHAuthRefused       = "auth_refused"
	SSHHostKey           = "host_key"
	SSHForwardRefused    = "forward_refused"
	SSHTargetUnreachable = "target_unreachable"
	SSHUnreachable       = "ssh_unreachable"
	SSHInvalid           = "invalid"
)

// SSHStatusError is the structured SSH failure attached to Status while waiting, so surfaces in en, zh-Hans and ja
// show the exact localized sentence without parsing Error.
type SSHStatusError struct {
	Code   string `json:"code"`
	Server string `json:"server,omitempty"`
	Target string `json:"target,omitempty"`
}

// Text formats the SSH failure in language l.
func (e SSHStatusError) Text(l i18n.Lang) string {
	args := i18n.Args{
		"server": e.Server,
		"target": e.Target,
		"cmd":    SSHLoginCommand(e.Server),
	}
	switch e.Code {
	case SSHNoBinary:
		return i18n.T(l, "link-tray.ssh_error_no_ssh", args)
	case SSHAuthRefused:
		return i18n.T(l, "link-tray.ssh_error_auth_refused", args)
	case SSHHostKey:
		return i18n.T(l, "link-tray.ssh_error_host_key", args)
	case SSHForwardRefused:
		return i18n.T(l, "link-tray.ssh_error_forward_refused", args)
	case SSHTargetUnreachable:
		return i18n.T(l, "link-tray.ssh_error_target_unreachable", args)
	case SSHInvalid:
		return i18n.T(l, "link-tray.ssh_error_invalid", args)
	default:
		return i18n.T(l, "link-tray.ssh_error_ssh_unreachable", args)
	}
}

// SSHError is a failure of the SSH tunnel, rendered as one plain sentence per failure mode.
type SSHError struct {
	Code   string
	Server string
	Target string
	Err    error
}

func (e *SSHError) Error() string { return e.Text(i18n.Default) }
func (e *SSHError) Unwrap() error { return e.Err }

// Text returns the single-sentence explanation in language l.
func (e *SSHError) Text(l i18n.Lang) string {
	return SSHStatusError{Code: e.Code, Server: e.Server, Target: e.Target}.Text(l)
}

// StatusError turns an *SSHError anywhere inside err into Status.SSHError, or nil when none failed.
func sshStatusOf(err error) *SSHStatusError {
	var se *SSHError
	if !errors.As(err, &se) {
		return nil
	}
	return &SSHStatusError{Code: se.Code, Server: se.Server, Target: se.Target}
}

// SSHLoginCommand is the terminal command shown when the server's host key is not in known_hosts yet.
func SSHLoginCommand(server string) string {
	dest, port, _, err := parseSSHServer(server)
	if err != nil || dest == "" {
		return "ssh " + strings.TrimSpace(server)
	}
	if port != "" {
		return "ssh -p " + port + " " + dest
	}
	return "ssh " + dest
}

// parseSSHServer splits [user@]host[:port] or a ~/.ssh/config alias into the destination and optional port for ssh.
func parseSSHServer(raw string) (dest, port, canonical string, err error) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", "", "", &SSHError{Code: SSHInvalid, Server: raw}
	}
	if strings.HasPrefix(strings.ToLower(s), "ssh://") {
		u, uerr := url.Parse(s)
		if uerr != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", "", "", &SSHError{Code: SSHInvalid, Server: raw}
		}
		if u.User != nil {
			if _, hasPass := u.User.Password(); hasPass {
				return "", "", "", &SSHError{Code: SSHInvalid, Server: raw}
			}
		}
		host := u.Hostname()
		if p := u.Port(); p != "" {
			if !validPort(p) {
				return "", "", "", &SSHError{Code: SSHInvalid, Server: raw}
			}
			port = p
		}
		user := ""
		if u.User != nil {
			user = u.User.Username()
		}
		if strings.HasPrefix(host, "-") || strings.HasPrefix(user, "-") {
			return "", "", "", &SSHError{Code: SSHInvalid, Server: raw}
		}
		return formatSSHDest(user, host, port)
	}
	user, hp := "", s
	if at := strings.LastIndexByte(s, '@'); at >= 0 {
		user, hp = s[:at], s[at+1:]
		if user == "" || strings.HasPrefix(user, "-") {
			return "", "", "", &SSHError{Code: SSHInvalid, Server: raw}
		}
	}
	if hp == "" || strings.ContainsAny(hp, "/?#@") {
		return "", "", "", &SSHError{Code: SSHInvalid, Server: raw}
	}
	var host string
	if ip, iperr := netip.ParseAddr(strings.Trim(hp, "[]")); iperr == nil {
		host = ip.String() // a bare IPv6 address (user@2001:db8::1) as well as a bracketed one
	} else if h, p, serr := net.SplitHostPort(hp); serr == nil {
		if h == "" || !validPort(p) {
			return "", "", "", &SSHError{Code: SSHInvalid, Server: raw}
		}
		host, port = h, p
	} else {
		if strings.ContainsAny(hp, "[]:") {
			return "", "", "", &SSHError{Code: SSHInvalid, Server: raw}
		}
		host = hp
	}
	if host == "" || strings.HasPrefix(host, "-") {
		return "", "", "", &SSHError{Code: SSHInvalid, Server: raw}
	}
	return formatSSHDest(user, host, port)
}

func formatSSHDest(user, host, port string) (dest, outPort, canonical string, err error) {
	dest = host
	if user != "" {
		dest = user + "@" + host
	}
	switch {
	case port != "":
		hp := net.JoinHostPort(host, port)
		if user != "" {
			canonical = user + "@" + hp
		} else {
			canonical = hp
		}
	case strings.Contains(host, ":"):
		if user != "" {
			canonical = user + "@[" + host + "]"
		} else {
			canonical = "[" + host + "]"
		}
	default:
		canonical = dest
	}
	return dest, port, canonical, nil
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}

// DefaultSSHTarget returns 127.0.0.1:<TLS port> from the first https endpoint in eps, or "" when eps has none.
func DefaultSSHTarget(eps []Endpoint) string {
	for _, e := range eps {
		u, err := url.Parse(e.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			continue
		}
		port := u.Port()
		if port == "" {
			port = "443"
		}
		if validPort(port) {
			return net.JoinHostPort("127.0.0.1", port)
		}
	}
	return ""
}

// AllLoopback reports whether eps is non-empty and every candidate in it points to loopback (127.0.0.0/8, ::1,
// localhost). When a pairing link names only loopback, Always defaults to true.
func AllLoopback(eps []Endpoint) bool {
	if len(eps) == 0 {
		return false
	}
	for _, e := range eps {
		if !isLoopbackURL(e.URL) {
			return false
		}
	}
	return true
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// PairSSHDefaults extracts the default SSH tunnel target (127.0.0.1:<TLS port>) and default Always setting from a
// pairing link, without needing to contact the hub.
func PairSSHDefaults(raw string) (target string, always bool, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !pairScheme(strings.ToLower(u.Scheme)) || (strings.ToLower(u.Host) != "link" && strings.ToLower(u.Host) != "pair") {
		return "", false, false
	}
	q := u.Query()
	var eps []Endpoint
	if q.Get("fp") != "" {
		port := q.Get("tls")
		if validPort(port) {
			host := "127.0.0.1"
			if base, berr := url.Parse(q.Get("url")); berr == nil && base.Hostname() != "" {
				host = base.Hostname()
			}
			u := "https://" + net.JoinHostPort(host, port)
			eps = append(eps, Endpoint{URL: u, Kind: kindOf(u)})
		}
	} else if e, perr := proxyEndpoint(q.Get("url")); perr == nil {
		eps = append(eps, e)
	}
	for _, alt := range q["alt"] {
		if e, aerr := normalize(alt, nil); aerr == nil {
			eps = append(eps, e)
		}
	}
	target = DefaultSSHTarget(eps)
	if target == "" {
		return "", false, false
	}
	return target, AllLoopback(eps), true
}

// parseSSHTarget validates and normalizes target (defaulting from eps when blank).
func parseSSHTarget(raw string, eps []Endpoint) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		s = DefaultSSHTarget(eps)
	}
	if s == "" {
		s = net.JoinHostPort("127.0.0.1", DefaultTargetPort)
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", &SSHError{Code: SSHInvalid, Target: raw}
		}
		port := u.Port()
		if port == "" && u.Scheme == "https" {
			port = "443"
		}
		if port == "" {
			return "", &SSHError{Code: SSHInvalid, Target: raw}
		}
		s = net.JoinHostPort(u.Hostname(), port)
	}
	if strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", &SSHError{Code: SSHInvalid, Target: raw}
	}
	var host, port string
	if h, p, err := net.SplitHostPort(s); err == nil {
		if h == "" {
			h = "127.0.0.1"
		}
		host, port = strings.Trim(h, "[]"), p
	} else if !strings.ContainsAny(s, "[]:") {
		// Host without port: fill the TLS port from eps when known.
		if def := DefaultSSHTarget(eps); def != "" {
			_, p, _ := net.SplitHostPort(def)
			host, port = s, p
		}
	}
	if host == "" || strings.HasPrefix(host, "-") || !validPort(port) {
		return "", &SSHError{Code: SSHInvalid, Target: raw}
	}
	return net.JoinHostPort(host, port), nil
}

// NormalizeSSH validates server and target (filling target from eps when blank) and returns the normalized SSH config.
func NormalizeSSH(server, target string, always bool, eps []Endpoint) (*SSH, error) {
	_, _, canonServer, err := parseSSHServer(server)
	if err != nil {
		return nil, err
	}
	canonTarget, err := parseSSHTarget(target, eps)
	if err != nil {
		var se *SSHError
		if errors.As(err, &se) {
			se.Server = canonServer
		}
		return nil, err
	}
	return &SSH{Server: canonServer, Target: canonTarget, Always: always}, nil
}

// SetSSH writes or removes (when ssh is nil) the SSH tunnel in link.json at path. Always is refused while proxy-only
// mode (Config.ProxyAlways) is on.
func SetSSH(path string, ssh *SSH) (*SSH, error) {
	var saved *SSH
	err := Update(path, func(c *Config) error {
		if ssh == nil {
			c.SSH = nil
			return nil
		}
		norm, err := NormalizeSSH(ssh.Server, ssh.Target, ssh.Always, c.Endpoints)
		if err != nil {
			return err
		}
		if norm.Always && c.ProxyAlways {
			return ErrSSHAlwaysProxyAlways
		}
		c.SSH = norm
		saved = cloneSSH(norm)
		return nil
	})
	return saved, err
}

// stderrCapture keeps up to 4 KB of ssh's stderr and wakes waiters whenever bytes arrive.
type stderrCapture struct {
	mu     sync.Mutex
	b      []byte
	notify chan struct{}
}

func newStderrCapture() *stderrCapture {
	return &stderrCapture{notify: make(chan struct{})}
}

func (s *stderrCapture) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.b = append(s.b, p...)
	if len(s.b) > 4096 {
		s.b = s.b[len(s.b)-4096:]
	}
	ch := s.notify
	s.notify = make(chan struct{})
	s.mu.Unlock()
	close(ch)
	return len(p), nil
}

func (s *stderrCapture) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}

func (s *stderrCapture) changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notify
}

// sshTunnel is one running `ssh -N -L` process carrying both the link and any streams opened on it.
type sshTunnel struct {
	cfg       SSH
	localAddr string
	cmd       *exec.Cmd
	stderr    *stderrCapture
	done      chan struct{}
	closing   chan struct{}
	waitErr   error
	once      sync.Once
}

func pickLoopbackPort() (int, error) {
	ln, err := osproc.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// startSSHTunnel starts ssh -N -L on a free loopback port. Another process can take that port before ssh binds it;
// callers see that as isDone() && localBindBusy() on their first request and start once more.
func startSSHTunnel(sshBin string, cfg SSH) (*sshTunnel, error) {
	bin := strings.TrimSpace(sshBin)
	if bin == "" {
		bin = "ssh"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, &SSHError{Code: SSHNoBinary, Server: cfg.Server, Target: cfg.Target, Err: err}
	}
	dest, port, canonServer, err := parseSSHServer(cfg.Server)
	if err != nil {
		return nil, err
	}
	target, err := parseSSHTarget(cfg.Target, nil)
	if err != nil {
		return nil, err
	}
	cfg.Server, cfg.Target = canonServer, target

	localPort, err := pickLoopbackPort()
	if err != nil {
		return nil, err
	}
	localAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-N",
		"-L", localAddr + ":" + target,
	}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", dest)

	stderr := newStderrCapture()
	cmd := exec.Command(resolved, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	cmd.SysProcAttr = sshProcAttr()
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, &SSHError{Code: SSHNoBinary, Server: cfg.Server, Target: cfg.Target, Err: err}
		}
		return nil, &SSHError{Code: SSHUnreachable, Server: cfg.Server, Target: cfg.Target, Err: err}
	}
	t := &sshTunnel{
		cfg:       cfg,
		localAddr: localAddr,
		cmd:       cmd,
		stderr:    stderr,
		done:      make(chan struct{}),
		closing:   make(chan struct{}),
	}
	go func() {
		t.waitErr = cmd.Wait()
		close(t.done)
	}()
	return t, nil
}

func (t *sshTunnel) isDone() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

func (t *sshTunnel) isClosing() bool {
	select {
	case <-t.closing:
		return true
	default:
		return false
	}
}

func (t *sshTunnel) close() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		close(t.closing)
		if t.cmd != nil && t.cmd.Process != nil {
			pid := t.cmd.Process.Pid
			_ = osproc.StopGroup(pid, true)
			_ = osproc.Kill(pid)
			<-t.done
			osproc.SweepGroup(pid, 200*time.Millisecond)
		}
	})
}

func (t *sshTunnel) localBindBusy() bool {
	low := strings.ToLower(t.stderr.String())
	return strings.Contains(low, "address already in use")
}

// dialTCP connects to ssh's local -L listener, waiting briefly while ssh finishes authenticating and binds the port.
func (t *sshTunnel) dialTCP(ctx context.Context) (net.Conn, error) {
	d := &net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}
	for {
		if t.isDone() {
			return nil, t.classifyExit(t.waitErr)
		}
		raw, err := d.DialContext(ctx, "tcp", t.localAddr)
		if err == nil {
			if t.isDone() {
				_ = raw.Close()
				return nil, t.classifyExit(t.waitErr)
			}
			return raw, nil
		}
		if t.isDone() {
			return nil, t.classifyExit(t.waitErr)
		}
		if ctx.Err() != nil {
			if t.isClosing() {
				return nil, ctx.Err()
			}
			return nil, &SSHError{Code: SSHUnreachable, Server: t.cfg.Server, Target: t.cfg.Target, Err: ctx.Err()}
		}
		select {
		case <-ctx.Done():
			if t.isClosing() {
				return nil, ctx.Err()
			}
			if t.isDone() {
				return nil, t.classifyExit(t.waitErr)
			}
			return nil, &SSHError{Code: SSHUnreachable, Server: t.cfg.Server, Target: t.cfg.Target, Err: ctx.Err()}
		case <-t.done:
			return nil, t.classifyExit(t.waitErr)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func classifyStderrCode(stderr string) string {
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "host key verification failed"),
		strings.Contains(low, "remote host identification has changed"),
		strings.Contains(low, "authenticity of host"),
		strings.Contains(low, "host key") && strings.Contains(low, "not known"):
		return SSHHostKey
	case strings.Contains(low, "permission denied"),
		strings.Contains(low, "authentication failed"),
		strings.Contains(low, "too many authentication failures"),
		strings.Contains(low, "no more authentication methods"):
		return SSHAuthRefused
	case strings.Contains(low, "administratively prohibited"),
		strings.Contains(low, "forwarding is disabled"),
		strings.Contains(low, "port forwarding failed"),
		strings.Contains(low, "forwarding disabled"):
		return SSHForwardRefused
	case strings.Contains(low, "connect failed"),
		strings.Contains(low, "open failed"):
		return SSHTargetUnreachable
	}
	return ""
}

func (t *sshTunnel) classifyExit(fallback error) error {
	code := classifyStderrCode(t.stderr.String())
	if code == "" {
		code = SSHUnreachable
	}
	return &SSHError{Code: code, Server: t.cfg.Server, Target: t.cfg.Target, Err: cmp.Or(fallback, errors.New(strings.TrimSpace(t.stderr.String())))}
}

func isCertError(err error) bool {
	return errors.Is(err, errPin) ||
		errors.As(err, new(x509.HostnameError)) ||
		errors.As(err, new(x509.UnknownAuthorityError)) ||
		errors.As(err, new(*tls.CertificateVerificationError))
}

// wrapHandshakeError preserves TLS pin and certificate errors unchanged, while classifying channel open failures
// (forwarding refused vs target hub not answering) from ssh's stderr.
func (t *sshTunnel) wrapHandshakeError(ctx context.Context, err error) error {
	if isCertError(err) {
		return err
	}
	if t.isClosing() {
		return cmp.Or(ctx.Err(), err)
	}
	// When sshd refuses direct-tcpip or cannot connect to target, local ssh closes the accepted socket and writes
	// "channel N: open failed: ..." to stderr around the same instant. Wait briefly if stderr has not arrived yet.
	deadline := time.NewTimer(100 * time.Millisecond)
	defer deadline.Stop()
	for {
		if code := classifyStderrCode(t.stderr.String()); code != "" {
			return &SSHError{Code: code, Server: t.cfg.Server, Target: t.cfg.Target, Err: err}
		}
		if t.isDone() {
			return t.classifyExit(err)
		}
		ch := t.stderr.changed()
		if code := classifyStderrCode(t.stderr.String()); code != "" {
			return &SSHError{Code: code, Server: t.cfg.Server, Target: t.cfg.Target, Err: err}
		}
		select {
		case <-ch:
		case <-t.done:
			return t.classifyExit(err)
		case <-deadline.C:
			return &SSHError{Code: SSHTargetUnreachable, Server: t.cfg.Server, Target: t.cfg.Target, Err: err}
		case <-ctx.Done():
			if code := classifyStderrCode(t.stderr.String()); code != "" {
				return &SSHError{Code: code, Server: t.cfg.Server, Target: t.cfg.Target, Err: err}
			}
			return &SSHError{Code: SSHTargetUnreachable, Server: t.cfg.Server, Target: t.cfg.Target, Err: err}
		}
	}
}

// tunnelBaseURL is the https base URL spoken over the tunnel: the first candidate's URL when present (preserving any
// reverse-proxy hostname and path prefix), else https://<target>.
func tunnelBaseURL(c *Config, cfg SSH) string {
	if c != nil {
		for _, e := range c.Endpoints {
			if strings.HasPrefix(e.URL, "https://") {
				return e.URL
			}
		}
	}
	return "https://" + cfg.Target
}

// tunnelClient builds an HTTP/1.1 client that dials through t while running the exact same TLS verification as direct
// connections (hubClient): pinned to c.FP when the pairing gave one, else the system's CAs and the endpoint's host
// name. Only a nil c (an unpaired "Test connection" with no pairing link, which sends no credential) skips the check;
// TLS still handshakes there so a non-TLS or dead target is caught.
func tunnelClient(c *Config, t *sshTunnel) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialTLSContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				raw, err := t.dialTCP(ctx)
				if err != nil {
					return nil, err
				}
				host, _, _ := net.SplitHostPort(addr)
				var tlsCfg *tls.Config
				switch {
				case c != nil && c.FP != "":
					tlsCfg = pinnedTLSConfig(c.FP, host)
				case c != nil:
					tlsCfg = &tls.Config{
						MinVersion: tls.VersionTLS12,
						NextProtos: []string{"http/1.1"},
						ServerName: host,
						RootCAs:    systemRoots.Load(),
					}
				default:
					tlsCfg = &tls.Config{
						MinVersion:         tls.VersionTLS12,
						NextProtos:         []string{"http/1.1"},
						InsecureSkipVerify: true, // nothing to check against yet, and no token is sent
					}
					if net.ParseIP(host) == nil {
						tlsCfg.ServerName = host
					}
				}
				tc := tls.Client(raw, tlsCfg)
				if err := tracedHandshake(ctx, tc); err != nil {
					_ = raw.Close()
					return nil, t.wrapHandshakeError(ctx, err)
				}
				return tc, nil
			},
			ResponseHeaderTimeout: dialTimeout,
			MaxIdleConns:          2,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// dialSSH opens the SSH tunnel and dials the hub's WebSocket link over it; the returned link owns the tunnel and uses
// the same tunnel for every stream opened on it.
func dialSSH(ctx context.Context, c *Config, sshBin string) (*link, error) {
	if c.SSH == nil {
		return nil, errors.New("no ssh tunnel configured")
	}
	cfg, err := NormalizeSSH(c.SSH.Server, c.SSH.Target, c.SSH.Always, c.Endpoints)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		t, err := startSSHTunnel(sshBin, *cfg)
		if err != nil {
			return nil, err
		}
		base := tunnelBaseURL(c, *cfg)
		actx, acancel := context.WithTimeout(ctx, dialTimeout)
		client := tunnelClient(c, t)
		conn, resp, err := diagnosticDial(actx, wsURL(base, Path), &websocket.DialOptions{
			HTTPClient: client,
			HTTPHeader: bearer(c.Token),
		}, "control", KindSSH, attempt+1)
		acancel()
		if err != nil {
			retryBind := attempt == 0 && t.isDone() && t.localBindBusy()
			client.CloseIdleConnections()
			t.close()
			if retryBind {
				continue
			}
			he := handshakeError(resp, err)
			var se *SSHError
			if errors.As(he, &se) {
				return nil, se
			}
			return nil, &attemptError{ep: Endpoint{URL: cfg.Server, Kind: KindSSH}, err: he}
		}
		conn.SetReadLimit(1 << 20)
		return &link{
			conn:    conn,
			ep:      Endpoint{URL: cfg.Server, Kind: KindSSH},
			baseURL: base,
			streams: tunnelClient(c, t),
			tunnel:  t,
			token:   c.Token,
		}, nil
	}
	return nil, &SSHError{Code: SSHUnreachable, Server: cfg.Server, Target: cfg.Target}
}

// TestSSH tests an SSH tunnel end to end: starting ssh, forwarding to target, and completing a TLS + HTTP request to
// the hub. When rawLink is non-empty (before pairing) or c is provided (after pairing), TLS checks the hub's pin or
// system certificate too.
func TestSSH(ctx context.Context, sshCfg SSH, rawLink string, c *Config, sshBin string) (*SSH, error) {
	var eps []Endpoint
	var checkCfg *Config
	if strings.TrimSpace(rawLink) != "" {
		if _, parsed, err := parsePairLink(rawLink); err == nil {
			eps = parsed.Endpoints
			checkCfg = &parsed
		} else if defTarget, _, ok := PairSSHDefaults(rawLink); ok && sshCfg.Target == "" {
			sshCfg.Target = defTarget
		}
	}
	if checkCfg == nil && c != nil && (c.FP != "" || c.SystemTrust()) {
		checkCfg = c
		eps = c.Endpoints
	} else if len(eps) == 0 && c != nil {
		eps = c.Endpoints
	}
	norm, err := NormalizeSSH(sshCfg.Server, sshCfg.Target, sshCfg.Always, eps)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		t, err := startSSHTunnel(sshBin, *norm)
		if err != nil {
			return nil, err
		}
		client := tunnelClient(checkCfg, t)
		base := tunnelBaseURL(checkCfg, *norm)
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/health", nil)
		var doErr error
		if reqErr != nil {
			doErr = reqErr
		} else {
			resp, err := client.Do(req)
			if err != nil {
				doErr = err
			} else {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
			}
		}
		retryBind := doErr != nil && attempt == 0 && t.isDone() && t.localBindBusy()
		client.CloseIdleConnections()
		t.close()
		if retryBind {
			continue
		}
		if doErr != nil {
			var se *SSHError
			if errors.As(doErr, &se) {
				return norm, se
			}
			if errors.Is(doErr, errPin) {
				return norm, errPin
			}
			if isCertError(doErr) {
				return norm, errors.New(describe(doErr))
			}
			return norm, &SSHError{Code: SSHTargetUnreachable, Server: norm.Server, Target: norm.Target, Err: doErr}
		}
		return norm, nil
	}
	return norm, &SSHError{Code: SSHUnreachable, Server: norm.Server, Target: norm.Target}
}
