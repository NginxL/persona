package link

import (
	"cmp"
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
)

// NodeDownMarker is in the error a computer's link reports when its resident node is not running (nodeMissing,
// listen.go): the hub reads it as error_code node_service_down (hub/machine_trust.go machineErrorCode). An older link
// says it the same way, so it is kept as it is.
const NodeDownMarker = "常驻 node 没在运行" // i18n-ignore: the hub recognizes this exact peer-facing marker

// NodeDownSelf comes right before NodeDownMarker in nodeMissing: the link's own computer. The hub swaps it for the
// computer's name on the machine's page (devices nodeUnavailable, #4080).
const NodeDownSelf = "本机的" // i18n-ignore: the manager recognizes this exact peer-facing prefix

// nodeMissing says the resident node is not answering at target, and how this computer runs it (docs/operations/link.md "Installing
// the node service"): the hub shows it on the machine's page.
func nodeMissing(goos, target string) error {
	how := "link-errors.node_how_other"
	switch goos {
	case "windows":
		how = "link-errors.node_how_windows"
	case "darwin":
		how = "link-errors.node_how_darwin"
	case "linux":
		how = "link-errors.node_how_linux"
	}
	return newTextError("link-errors.node_missing", nil, i18n.Args{
		"self": newTextError("link-errors.node_down_self", nil), "marker": newTextError("link-errors.node_down_marker", nil),
		"target": target, "how": newTextError(how, nil),
	})
}

// These are settings, not measurements.
const (
	stagger     = 300 * time.Millisecond // between starting one candidate and the next (docs/operations/link.md "Route selection")
	dialTimeout = 8 * time.Second        // one candidate: TCP, TLS and the WebSocket handshake
	checkPing   = 3 * time.Second        // a ping after the network changed
	refusedWait = 5 * time.Minute        // after the hub refused the credential, unless link.json changes
	replaceWait = 30 * time.Second       // after another persona-link of this machine took over
	pairCheck   = 2 * time.Second        // how often a connected link looks whether link.json was paired again
)

// errPairedAgain: link.json holds another credential than the link's (paired again meanwhile): nothing of this link's
// is written into it.
var errPairedAgain = errors.New("paired again meanwhile")

var (
	errPin    = newTextError("link-errors.pin_mismatch", nil)
	errRedial = newTextError("link-errors.manual_reconnect", nil)
	// errRepaired: link.json was paired again while the link was up; it reconnects at once with the new pairing.
	errRepaired = newTextError("link-errors.repaired", nil)
	// errDirectReady: while connected over SSH without Always, a direct WebSocket connected and passed validation;
	// serve transfers that exact candidate together with this cause.
	errDirectReady = newTextError("link-errors.direct_ready", nil)
)

// Status is what persona-link reports: Run hands one to Options.Status every time something changes, and
// cmd/persona-link prints each as a JSON line for the menu bar app.
type Status struct {
	// State: unpaired | connecting | connected | waiting | refused; passive mode: unpaired | listening (paired, no
	// stream from the hub open) | connected (one is) | waiting (cannot listen).
	State string `json:"state"`
	// Mode: active (this computer dials the hub) | passive (the hub dials it).
	Mode    string `json:"mode,omitempty"`
	Machine string `json:"machine,omitempty"`
	// HubName: what the hub calls itself, once a hub that says it has (link.json keeps it). MeshHubs, while
	// connected: how many hubs its mesh has, itself included. Both missing with older hubs.
	HubName  string `json:"hub_name,omitempty"`
	MeshHubs int    `json:"mesh_hubs,omitempty"`
	// MachineName: what the hub calls this computer, once a hub that says it has (link.json keeps it); missing with
	// older hubs, which show Machine (its id) instead.
	MachineName string `json:"machine_name,omitempty"`
	// Rename: how the last rename asked of this persona-link went (RequestRename waits for it by its ID).
	Rename *RenameResult `json:"rename,omitempty"`
	// Pending, while connected: the hub waits for the user to confirm this computer ("It's mine" in the app);
	// missing once confirmed, and with older hubs.
	Pending bool `json:"pending,omitempty"`
	// connected: the hub URL in use, its kind (lan | ipv6 | name | ipv4, ssh, proxy) and since when.
	Address string     `json:"address,omitempty"`
	Kind    string     `json:"kind,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
	// Proxy, connected with kind proxy: the outbound proxy the link runs through, never its user name or password.
	Proxy string `json:"proxy,omitempty"`
	// Trust: "system" when the hub is checked with the system's CAs and host name (it sits behind an HTTPS reverse
	// proxy, Config.Trust); empty when it is pinned.
	Trust string `json:"trust,omitempty"`
	// connected: whether the hub got through to the resident node (online | offline | unknown), and why not.
	Node      string `json:"node,omitempty"`
	NodeError string `json:"node_error,omitempty"`
	// waiting, refused: why the last attempt or link failed, and when the next attempt is.
	Error    string          `json:"error,omitempty"`
	SSHError *SSHStatusError `json:"ssh_error,omitempty"`
	Retry    *time.Time      `json:"retry,omitempty"`
	// The candidates, best first; never the credential.
	Endpoints []Endpoint `json:"endpoints,omitempty"`
	// SSH: the optional SSH tunnel configured in link.json.
	SSH *SSH `json:"ssh,omitempty"`
	// Attention is the hub's latest "Needs you" summary (attention.go), as it sent it: active mode while this link is
	// up, passive mode while the paired hub keeps coming in. Missing before the first one, and with older hubs.
	Attention *Attention `json:"attention,omitempty"`
	// Conn, active mode while connected: which link it is, a number that rises with each link and is not reused by a
	// later run (reporter.links). The menu bar app's confirmations name the link whose summary listed the card
	// (presence.go Confirm).
	Conn uint64 `json:"conn,omitempty"`
	// Passive mode: where it listens; this computer's addresses the hub may use (best first, host:port); when the hub
	// last came in and from where; until when a pairing code from `pair --listen` is good. Connected: Address is the
	// hub's address and Since when its stream opened.
	Listen      string     `json:"listen,omitempty"`
	Addrs       []string   `json:"addrs,omitempty"`
	LastSeen    *time.Time `json:"last_seen,omitempty"`
	From        string     `json:"from,omitempty"`
	CodeExpires *time.Time `json:"code_expires,omitempty"`
}

// Options are what Run needs from its caller.
type Options struct {
	Path    string // link.json
	Target  string // the resident node's socket
	Version string
	Status  func(Status)
	// Check: look at the link now (the network changed). Redial: drop it and connect again.
	Check, Redial <-chan struct{}
	Logf          func(string, ...any)
	// Presence is the menu bar app's presence, Confirms the cards it rang for (presence.go); nil without the app.
	Presence *PresenceBox
	Confirms *Confirms
	// PingEvery, PingTimeout replace the defaults (tests). A hub that says its own heartbeat (endpoints, PingHeader) is
	// followed on that link or stream instead.
	PingEvery, PingTimeout time.Duration
	// DirectRetryEvery, PairCheck and SSHBin replace the defaults in tests.
	DirectRetryEvery time.Duration
	PairCheck        time.Duration
	SSHBin           string
	// directReady observes candidate publication in package tests; production leaves it nil.
	directReady chan *directHandoff
	// limiter replaces passive mode's (tests).
	limiter *authLimiter
}

// httpError is a refused handshake: 401 / 403 mean the credential is no good, 429 that the hub wants a pause.
type httpError struct {
	status int
	retry  time.Duration
}

func (e *httpError) Error() string { return "HTTP " + strconv.Itoa(e.status) }

func handshakeError(resp *http.Response, err error) error {
	if resp == nil || resp.StatusCode == http.StatusSwitchingProtocols {
		return err
	}
	he := &httpError{status: resp.StatusCode}
	if s, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil {
		he.retry = time.Duration(s) * time.Second
	}
	return he
}

// backoff: exponential from 1 s to a minute, each wait randomized down to half so links that broke together do not
// come back together.
type backoff struct{ n int }

func (b *backoff) next() time.Duration {
	d := min(time.Second<<b.n, time.Minute)
	if d < time.Minute {
		b.n++
	}
	return d/2 + rand.N(d/2+1)
}

// reporter keeps the status and hands every change to Options.Status, saying which mode it is about.
type reporter struct {
	mu   sync.Mutex
	st   Status
	out  func(Status)
	mode string
	// links numbers the run's links (Status.Conn) across mode switches, so no number is given twice; runActive's
	// alone. It starts at the run's start in Unix milliseconds: a restarted persona-link does not reuse an earlier
	// run's numbers (that run made far fewer links than milliseconds passed), so the app's late confirmations for the
	// earlier run's links match none of the new one's.
	links uint64
}

// switchTo starts reporting about mode.
func (r *reporter) switchTo(mode string) {
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
}

// set applies change and hands the result to out while still holding r.mu, so the statuses reach out in the order
// they were made: handed over after unlocking, two callers at once could swap, and persona-link would keep the older
// one (the App's line, link.status.json) until the next change. out must not call back into the reporter; set may be
// called with passive's lock held (reportLocked), never the other way round.
func (r *reporter) set(change func(*Status)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(&r.st)
	r.st.Mode = r.mode
	if r.out != nil {
		r.out(r.st)
	}
}

// Run keeps this computer reachable from the hub until ctx ends: in active mode by keeping the link to the hub up,
// in passive mode by listening for it (listen.go). It follows link.json: switched to the other mode (persona-link
// mode, pair, pair --listen), it changes over within a second or two.
func Run(ctx context.Context, o Options) error {
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.PingEvery <= 0 || o.PingTimeout <= 0 {
		o.PingEvery, o.PingTimeout = PingEvery, PingTimeout
	}
	rep := &reporter{out: o.Status, links: uint64(time.Now().UnixMilli())}
	for ctx.Err() == nil {
		mode := currentMode(o.Path)
		mctx, cancel := context.WithCancel(ctx)
		go watchMode(mctx, o.Path, mode, cancel)
		if mode == ModePassive {
			runPassive(mctx, o, rep)
		} else {
			runActive(mctx, o, rep)
		}
		cancel()
		if ctx.Err() == nil {
			o.Logf("mode: %s → %s", mode, currentMode(o.Path))
		}
	}
	return nil
}

func currentMode(path string) string {
	c, err := Read(path)
	if err != nil {
		return ModeActive
	}
	return c.CurrentMode()
}

// modeCheck is how often Run looks whether link.json switched modes (a setting).
const modeCheck = time.Second

// watchMode ends the mode being run (cancel) once link.json says another.
func watchMode(ctx context.Context, path, mode string, cancel context.CancelFunc) {
	was := stampOf(path)
	if currentMode(path) != mode {
		cancel()
		return
	}
	t := time.NewTicker(modeCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := stampOf(path)
			if now.Equal(was) {
				continue
			}
			was = now
			if currentMode(path) != mode {
				cancel()
				return
			}
		}
	}
}

// runActive keeps the link to the hub up until ctx ends.
func runActive(ctx context.Context, o Options, rep *reporter) {
	rep.switchTo(ModeActive)
	var b backoff
	// last is the kind of the link that worked last: a proxy or SSH route goes first next time (routeOrder). A new
	// credential, tunnel or proxy setting starts over from direct.
	var last, lastToken, lastProxy string
	var lastSSHConfig *SSH
	for ctx.Err() == nil {
		was := stampOf(o.Path) // a change from here on (paired again, an address added) cuts the wait below short
		c, err := Load(o.Path)
		if err != nil {
			last, lastToken, lastProxy, lastSSHConfig = "", "", "", nil
			if errors.Is(err, os.ErrNotExist) {
				rep.set(func(s *Status) { *s = Status{State: "unpaired"} })
			} else {
				rep.set(func(s *Status) { *s = Status{State: "waiting", Error: err.Error()} })
			}
			o.wait(ctx, time.Hour, was)
			continue
		}
		if c.Token != lastToken || !c.SSH.Equal(lastSSHConfig) || c.Proxy != lastProxy {
			last = ""
			lastToken, lastProxy = c.Token, c.Proxy
			lastSSHConfig = cloneSSH(c.SSH)
		}
		rep.set(func(s *Status) {
			*s = Status{State: "connecting", Machine: c.Machine, HubName: c.HubName, MachineName: c.MachineName, Trust: trustOf(c),
				Endpoints: ranked(c.Endpoints), SSH: cloneSSH(c.SSH)}
		})
		l, err := dialActive(ctx, c, o.SSHBin, last)
		var end error
		state := "waiting"
		if err != nil {
			end = err
		} else {
			for {
				last = l.ep.Kind
				start := time.Now()
				rep.links++
				l.gen = rep.links
				var handoff *directHandoff
				handoff, end = l.serve(ctx, c, o, rep)
				if time.Since(start) > time.Minute {
					b = backoff{}
				}
				if handoff == nil {
					break
				}
				if ctx.Err() != nil {
					// Direct-ready may have won just before the run itself was cancelled. Ownership has transferred, so
					// close the accepted candidate rather than leaving it for the next run iteration.
					handoff.candidate.closeNow()
					return
				}
				// Serve the exact socket the SSH probe connected, under the configuration that validated it. A redial
				// here would both lose that ownership and reintroduce the gap that dropped working SSH links.
				l, c = handoff.candidate, handoff.config
				last = ""
				b = backoff{}
			}
			// The link writes link.json itself (the hub's addresses and names): from here only a new credential or tunnel
			// change, written while the link was up, counts as a change since it was read. The stamp is taken before the
			// read, so a change written in between still counts.
			now := stampOf(o.Path)
			if f, err := Read(o.Path); err != nil || (f.Token == c.Token && f.SSH.Equal(c.SSH) && f.Proxy == c.Proxy) {
				was = now
			}
		}
		if ctx.Err() != nil {
			return
		}
		wait := b.next()
		var he *httpError
		switch {
		case errors.Is(end, errDirectReady):
			last = ""
			b, wait = backoff{}, 0
		case errors.Is(end, errRedial), errors.Is(end, errRepaired):
			b, wait = backoff{}, 0
		case errors.As(end, &he) && (he.status == http.StatusUnauthorized || he.status == http.StatusForbidden):
			state, wait = "refused", refusedWait
			end = newTextError("link-errors.credential_refused", nil)
		case errors.As(end, &he) && he.status == http.StatusTooManyRequests:
			wait = max(wait, he.retry)
		case websocket.CloseStatus(end) == CloseReplaced:
			wait = replaceWait
			end = newTextError("link-errors.replaced", nil)
		case websocket.CloseStatus(end) == CloseRevoked:
			state, wait = "refused", refusedWait
			end = newTextError("link-errors.credential_revoked", nil)
		}
		retry := time.Now().Add(wait)
		o.Logf("link: %s; next attempt in %s", ErrorText(i18n.En, end), wait.Round(time.Second))
		rep.set(func(s *Status) {
			*s = Status{State: state, Machine: c.Machine, HubName: c.HubName, MachineName: c.MachineName, Error: summary(end),
				SSHError: sshStatusOf(end), Retry: &retry, Trust: trustOf(c), Endpoints: ranked(c.Endpoints), SSH: cloneSSH(c.SSH)}
		})
		o.wait(ctx, wait, was)
	}
}

// dialActive dials c's hub over its routes in routeOrder. A pinned hub that refused the credential, on whichever
// route, ends the attempt: no other route can do better.
func dialActive(ctx context.Context, c *Config, sshBin string, last string) (*link, error) {
	p, eps, perr := proxied(c)
	return tryRoutes(routeOrder(c, p != nil || perr != nil, last), func(kind string) (*link, error) {
		switch kind {
		case KindSSH:
			return dialSSH(ctx, c, sshBin)
		case KindProxy:
			if perr != nil {
				return nil, perr
			}
			return dialVia(ctx, c, eps, p)
		}
		return dial(ctx, c)
	}, func(err error) bool {
		var he *httpError
		return ctx.Err() != nil || (c.FP != "" && errors.As(err, &he) &&
			(he.status == http.StatusUnauthorized || he.status == http.StatusForbidden))
	})
}

// routeOrder is the order c's routes are tried in (docs/operations/link.md "Outbound proxy"): direct candidates
// (""), the outbound proxy when there is one, the SSH tunnel when there is one; last, the kind of the link that worked
// last, first when it is one of these. Always use SSH, or a tunnel and no candidates, leaves the tunnel alone.
func routeOrder(c *Config, proxy bool, last string) []string {
	if c.SSH != nil && (c.SSH.Always || len(c.Endpoints) == 0) {
		return []string{KindSSH}
	}
	order := []string{""}
	if proxy {
		order = append(order, KindProxy)
	}
	if c.SSH != nil {
		order = append(order, KindSSH)
	}
	if i := slices.Index(order, last); i > 0 {
		order = slices.Insert(slices.Delete(order, i, i+1), 0, last)
	}
	return order
}

// tryRoutes tries each route in order until one works; stop ends it early after a failure no other route can mend.
// The error is the one failure, or all of them in order.
func tryRoutes[T any](order []string, try func(kind string) (T, error), stop func(error) bool) (T, error) {
	var errs []error
	for _, kind := range order {
		v, err := try(kind)
		if err == nil {
			return v, nil
		}
		errs = append(errs, err)
		if stop(err) {
			break
		}
	}
	var zero T
	if len(errs) == 1 {
		return zero, errs[0]
	}
	return zero, errors.Join(errs...)
}

// wait sleeps up to d, less if the network changed, a reconnect was asked for or link.json changed (paired again,
// an address added) since was: the stamp (stampOf) from when the attempt read it, so a change that landed during the
// attempt (a new credential written while the hub refused the old one) ends the wait at its first poll.
func (o Options) wait(ctx context.Context, d time.Duration, was time.Time) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	poll := time.NewTicker(cmp.Or(o.PairCheck, 2*time.Second))
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-o.Check:
			return
		case <-o.Redial:
			return
		case <-poll.C:
			if !stampOf(o.Path).Equal(was) {
				return
			}
		}
	}
}

// stampOf is when link.json at path was last written (zero when there is none): a different stamp means it changed.
func stampOf(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}
