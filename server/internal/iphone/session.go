package iphone

// One resident session per phone: a supervisor goroutine finds the phone (manual address, Bonjour, the last address
// that worked), starts the bridge's serve on it, and when the link drops starts again after a backoff. It belongs to
// the Manager, which lives as long as the node process: a client closing or the hub restarting doesn't touch it, and
// an idle connected phone stays connected. Calls go one at a time, as the bridge answers them; an input operation
// whose answer never came is reported as uncertain and never sent again (docs/extensions/iphone-pairing.md).

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Session states (Device.State).
const (
	StateIdle       = "idle"       // no session: never used since the node started, or parked after a long time unreachable
	StateConnecting = "connecting" // finding the phone and bringing the bridge's session up
	StateConnected  = "connected"
	StateBackoff    = "backoff" // the last attempt failed (LastError); the next one comes after a delay, or sooner when a call waits
)

// readOnlyOps are the bridge's operations that change nothing on the phone (its wire contract, #3451): the only ones
// sent again after a dropped link.
var readOnlyOps = map[string]bool{"device_info": true, "screenshot": true, "ax_tree": true, "list_apps": true}

// ReadOnly says whether op changes nothing on the phone, so it may be retried.
func ReadOnly(op string) bool { return readOnlyOps[op] }

// Timing; variables so tests can shorten them.
var (
	backoffMin    = time.Second
	backoffMax    = 2 * time.Minute
	stableAfter   = 30 * time.Second // a link that lasted this long resets the backoff
	parkAfter     = 10 * time.Minute // unreachable and no call for this long: stop trying until the next call
	minGap        = time.Second      // between attempts even when a call asks for one now
	browseWait    = 3 * time.Second
	answerMargin  = 300 * time.Millisecond // timeout_ms leaves this much of the caller's deadline for the answer to come back
	readOnlyRetry = 1                      // times a read-only call is sent again after its link dropped
	healthEvery   = time.Minute            // an idle link is probed (device_info) and its record checked this often
	healthTimeout = 15 * time.Second
)

// conn is one live link and what a call needs of it.
type conn struct {
	link    Link
	answers chan []byte   // lines with an id
	down    chan struct{} // closed when the link has ended
	stamp   stamp         // the record it was started with
}

func (c *conn) ended() bool {
	select {
	case <-c.down:
		return true
	default:
		return false
	}
}

// stamp is what of a record a live link depends on: a change means the link has to start again (the command line
// paired the phone again, forgot it, or set its address, from another process).
type stamp struct {
	address string
	mod     time.Time
	size    int64
}

func (s *session) stampOf(rec Record) (stamp, error) {
	path, err := s.m.store.RecordPath(rec.UDID)
	if err != nil {
		return stamp{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return stamp{}, err
	}
	return stamp{address: rec.Address, mod: st.ModTime(), size: st.Size()}, nil
}

// current says whether c still matches the record on disk.
func (s *session) current(c *conn) bool {
	rec, err := s.m.store.Load(s.udid)
	if err != nil {
		return false
	}
	st, err := s.stampOf(rec)
	return err == nil && st == c.stamp
}

type session struct {
	m    *Manager
	udid string
	wake chan struct{}
	turn chan struct{} // one call at a time (a semaphore, so waiting for it can be cancelled)

	mu         sync.Mutex
	running    bool
	restart    bool // the live link was ended on purpose: connect again at once, not as a failure
	state      string
	lastErr    error
	errAt      time.Time // when lastErr was set
	ep         Endpoint
	info       json.RawMessage // the last device_info (the ready event's, then the health probe's)
	cur        *conn
	up         chan struct{} // closed while cur != nil
	lastDemand time.Time     // last time a call wanted the link
	lastUse    time.Time     // last time a call finished
	since      time.Time     // when the state last changed
	// prepared: this link already tried to mount the developer disk image by itself (maybePrepare), once per link.
	prepared *conn
}

func newSession(m *Manager, udid string) *session {
	return &session{m: m, udid: udid, wake: make(chan struct{}, 1), turn: make(chan struct{}, 1), state: StateIdle, up: make(chan struct{}), since: m.now()}
}

// ensureRunning starts the supervisor if it isn't running, and notes that a call wants the link.
func (s *session) ensureRunning() {
	s.mu.Lock()
	s.lastDemand = s.m.now()
	start := !s.running && s.m.ctx.Err() == nil
	if start {
		s.running = true
		s.state, s.since = StateConnecting, s.m.now()
		if Code(s.lastErr) == CodeNotPaired { // from before the phone was paired (again)
			s.lastErr = nil
		}
	}
	s.mu.Unlock()
	if start && !s.m.spawn(s.run) {
		s.mu.Lock()
		s.running, s.state = false, StateIdle
		s.mu.Unlock()
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *session) setState(state string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != state {
		s.since = s.m.now()
	}
	s.state = state
	if err != nil || state == StateConnected {
		s.lastErr, s.errAt = err, s.m.now()
	}
}

// run is the supervisor: connect, serve until the link drops, back off, again.
func (s *session) run() {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.state, s.since = StateIdle, s.m.now() // no link outlives the supervisor
		s.mu.Unlock()
	}()
	var backoff time.Duration
	for {
		if s.m.ctx.Err() != nil {
			return
		}
		rec, err := s.m.store.Load(s.udid)
		if errors.Is(err, ErrNotPaired) {
			s.setState(StateIdle, &Error{Code: CodeNotPaired, Message: err.Error()})
			return
		}
		var c *conn
		var ep Endpoint
		started := s.m.now()
		if err == nil {
			s.setState(StateConnecting, nil)
			c, ep, err = s.connect(rec)
		}
		if err == nil {
			s.serve(c, ep, rec)
			if s.m.now().Sub(started) >= stableAfter {
				backoff = 0
			}
			err = c.link.Err()
		}
		if s.m.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		restart := s.restart
		s.restart = false
		s.mu.Unlock()
		if restart { // ended on purpose (pairing again, a new address): straight back
			backoff = 0
			continue
		}
		backoff = nextBackoff(backoff)
		s.setState(StateBackoff, err)
		s.m.logf("iphone %s: %v; again in %s", short(s.udid), err, backoff.Round(time.Second))
		if !s.wait(backoff) {
			return
		}
	}
}

// wait sleeps d, cut short (but not below minGap) by a call wanting the link. False: stop (the manager closed, or
// nobody has wanted the link for parkAfter).
func (s *session) wait(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-s.m.ctx.Done():
			return false
		case <-t.C:
		case <-s.wake:
			if d > minGap {
				t.Reset(minGap)
				d = minGap
			}
			continue
		}
		s.mu.Lock()
		parked := s.m.now().Sub(s.lastDemand) >= parkAfter
		s.mu.Unlock()
		return !parked
	}
}

func nextBackoff(d time.Duration) time.Duration {
	if d < backoffMin {
		d = backoffMin
	} else {
		d *= 2
	}
	d = min(d, backoffMax)
	// ±20%, so several phones don't retry in step
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// connect tries where the phone may be, in order: the manual address, Bonjour, the last address that worked.
func (s *session) connect(rec Record) (*conn, Endpoint, error) {
	path, err := s.m.store.RecordPath(rec.UDID)
	if err != nil {
		return nil, Endpoint{}, err
	}
	st, err := s.stampOf(rec)
	if err != nil {
		return nil, Endpoint{}, err
	}
	dial, err := s.m.dialer()
	if err != nil {
		return nil, Endpoint{}, err
	}
	var tried []string
	var lastErr error
	try := func(ep Endpoint) *conn {
		key := ep.String()
		for _, t := range tried {
			if t == key {
				return nil
			}
		}
		tried = append(tried, key)
		ctx, cancel := context.WithTimeout(s.m.ctx, readyTimeout+5*time.Second)
		defer cancel()
		link, info, err := dial(ctx, rec, path, ep)
		if err != nil {
			lastErr = err
			return nil
		}
		s.mu.Lock()
		s.info = info
		s.mu.Unlock()
		return startConn(link, st)
	}
	if rec.Simulator() { // driven by the bridge on this computer: nothing to find on the network
		ep := Endpoint{Source: TransportSimulator}
		if c := try(ep); c != nil {
			return c, ep, nil
		}
		return nil, Endpoint{}, lastErr
	}
	if rec.Address != "" {
		if ep, err := ParseAddress(rec.Address); err == nil {
			if c := try(ep); c != nil {
				return c, ep, nil
			}
		} else {
			lastErr = err
		}
	}
	if svc, err := s.find(path); svc != nil {
		for _, a := range svc.Addrs {
			ep := Endpoint{Host: a.String(), Port: svc.Port, Source: "bonjour"}
			if c := try(ep); c != nil {
				return c, ep, nil
			}
		}
	} else if err != nil && lastErr == nil {
		lastErr = err
	}
	if rec.LastHost != "" {
		ep := Endpoint{Host: rec.LastHost, Port: rec.LastPort, Source: "last"}
		if c := try(ep); c != nil {
			return c, ep, nil
		}
	}
	if lastErr == nil {
		lastErr = &Error{Code: CodeUnreachable, Message: "the phone wasn't found on the network (it advertises itself over Bonjour on the same Wi-Fi; elsewhere set its address with personad iphone address)"}
	}
	if rec.DeveloperMode != nil && !*rec.DeveloperMode && Code(lastErr) == CodeUnreachable {
		// With Developer Mode off the phone pairs, then neither advertises nor answers on the network: say why.
		lastErr = &Error{Code: "developer_mode_off", Message: developerModeHint}
	}
	return nil, Endpoint{}, lastErr
}

// find is the phone's Bonjour advertisement, asking the bridge about each new one as it arrives (only the record
// can tell its phone's advertisement from others'), or nil.
func (s *session) find(recordPath string) (*Service, error) {
	match, err := s.m.matcher()
	if err != nil {
		return nil, err
	}
	// Browse's own rounds take at most 1.5 × browseWait; its context only stops it with the manager.
	ctx, cancel := context.WithTimeout(s.m.ctx, 2*browseWait)
	defer cancel()
	return findPhone(ctx, s.m.browse, match, recordPath)
}

// findPhone browses for the advertisement match says is the record's phone.
func findPhone(ctx context.Context, browse Browser, match Matcher, recordPath string) (*Service, error) {
	checked := map[string]bool{}
	var found *Service
	var matchErr error
	check := func(services []Service) bool {
		for _, svc := range services {
			key := svc.Instance + "\x00" + svc.Identifier() + "\x00" + svc.AuthTag()
			if found != nil || !svc.usable() || checked[key] {
				continue
			}
			checked[key] = true
			mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			ok, err := match(mctx, recordPath, svc)
			cancel()
			if err != nil {
				matchErr = err
				continue
			}
			if ok {
				found = &svc
			}
		}
		return found != nil
	}
	services, err := browse(ctx, browseWait, check)
	if found == nil && err == nil {
		check(services) // what the last round added
	}
	if found != nil {
		return found, nil
	}
	if matchErr != nil {
		return nil, matchErr
	}
	if err != nil && ctx.Err() == nil {
		return nil, err
	}
	return nil, nil
}

// startConn wraps a ready link and passes its answers on until it ends.
func startConn(link Link, st stamp) *conn {
	c := &conn{link: link, answers: make(chan []byte, 4), down: make(chan struct{}), stamp: st}
	go func() {
		defer close(c.down)
		for line := range link.Lines() {
			var head struct {
				ID *string `json:"id"`
			}
			if json.Unmarshal(line, &head) != nil || head.ID == nil {
				continue // an event; the link records a disconnect's reason itself
			}
			select {
			case c.answers <- line:
			default: // nobody is waiting (the call timed out); the next call skips it by its id anyway
			}
		}
	}()
	return c
}

// serve publishes c as the session's link until it ends, remembering where the phone was found and checking on it
// while idle.
func (s *session) serve(c *conn, ep Endpoint, rec Record) {
	s.m.remember(s.udid, ep, s.infoNow())
	s.mu.Lock()
	s.cur, s.ep = c, ep
	close(s.up)
	s.mu.Unlock()
	s.setState(StateConnected, nil)
	s.m.logf("iphone %s: connected at %s (%s)", short(s.udid), ep, ep.Source)
	s.maybePrepare(c)
	tick := time.NewTicker(healthEvery)
	defer tick.Stop()
loop:
	for {
		select {
		case <-c.down:
			break loop
		case <-s.m.ctx.Done():
			c.link.Close()
			<-c.down
			break loop
		case <-tick.C:
			s.health(c)
		}
	}
	s.mu.Lock()
	s.cur, s.up = nil, make(chan struct{})
	s.mu.Unlock()
}

// health checks an idle link: the record it was started with is still the one on disk, and the phone still answers
// device_info. A link that fails either is ended (the supervisor starts another); the probe's answer refreshes what
// Devices shows (locked, iOS version).
func (s *session) health(c *conn) {
	if !s.current(c) {
		s.restartLink(c)
		return
	}
	s.mu.Lock()
	busy := s.m.now().Sub(s.lastUse) < healthEvery
	s.mu.Unlock()
	if busy {
		return
	}
	select {
	case s.turn <- struct{}{}:
	default:
		return // a call is under way: that says enough
	}
	defer func() { <-s.turn }()
	ctx, cancel := context.WithTimeout(s.m.ctx, healthTimeout)
	defer cancel()
	info, err := s.roundTrip(ctx, c, "device_info", nil)
	switch {
	case err == nil:
		s.mu.Lock()
		s.info = info
		s.mu.Unlock()
		defer s.maybePrepare(c) // only starts the mount, which then waits for the turn like any call
	case errors.Is(err, errLinkLost) || errors.Is(err, errNotSent):
	case Code(err) == bridgeDisconnected || Code(err) == bridgeTimeout || ctx.Err() != nil:
		s.m.logf("iphone %s: health check: %v", short(s.udid), err)
		c.link.Close()
	}
}

// maybePrepare mounts the developer disk image by itself, once per link, when the phone came up without it and its
// owner allowed that (Record.AutoPrepare, after preparing it from the app): a restarted phone has dropped the image.
// In the background, as one call like any other; a failure is only logged (the app's status says ddi_not_mounted).
func (s *session) maybePrepare(c *conn) {
	s.mu.Lock()
	info, done := s.info, s.prepared == c
	s.mu.Unlock()
	if done || len(info) == 0 {
		return
	}
	var di deviceInfo
	if json.Unmarshal(info, &di) != nil || di.DDIMounted == nil || *di.DDIMounted {
		return // mounted, or Developer Mode off (null): nothing to mount now
	}
	rec, err := s.m.store.Load(s.udid)
	if err != nil || !rec.AutoPrepare {
		return
	}
	s.mu.Lock()
	s.prepared = c
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(s.m.ctx, prepareTimeout)
		defer cancel()
		if _, err := s.m.Call(ctx, s.udid, opMountDDI, json.RawMessage(`{}`)); err != nil {
			s.m.logf("iphone %s: mounting the developer disk image again: %v", short(s.udid), err)
			return
		}
		s.m.logf("iphone %s: mounted the developer disk image again", short(s.udid))
	}()
}

// restartLink ends c on purpose: the supervisor connects again at once.
func (s *session) restartLink(c *conn) {
	s.mu.Lock()
	s.restart = true
	s.mu.Unlock()
	c.link.Close()
}

func (s *session) infoNow() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// await is the live link, waiting for one (and asking the supervisor for it) until ctx ends. A failure the user has
// to fix on the phone or this computer ends the wait at once instead.
func (s *session) await(ctx context.Context) (*conn, error) {
	start := s.m.now()
	for {
		s.ensureRunning()
		s.mu.Lock()
		c, up, state, lastErr, errAt := s.cur, s.up, s.state, s.lastErr, s.errAt
		s.mu.Unlock()
		if c != nil && !c.ended() {
			return c, nil
		}
		if state == StateIdle && Code(lastErr) == CodeNotPaired {
			return nil, unsent(lastErr)
		}
		if lastErr != nil && !errAt.Before(start) && needsUser[Code(lastErr)] {
			return nil, unsent(lastErr)
		}
		select {
		case <-up:
			if c != nil { // up was the ended link's: wait for the supervisor to take it down
				time.Sleep(5 * time.Millisecond)
			}
		case <-ctx.Done():
			msg := "the phone couldn't be reached in time"
			s.mu.Lock()
			if s.lastErr != nil {
				msg += ": " + s.lastErr.Error()
			}
			s.mu.Unlock()
			return nil, unsent(&Error{Code: CodeUnreachable, Message: msg})
		case <-time.After(100 * time.Millisecond): // to look at the state again
		}
	}
}

// needsUser are the failures to connect that waiting won't fix: someone has to act on the phone or this computer.
// Retrying goes on in the background all the same.
var needsUser = map[string]bool{
	CodeNotPaired: true, CodeBridgeMissing: true, "pairing_missing": true, "pairing_rejected": true,
	"developer_mode_off": true, "ddi_not_mounted": true, "unsupported": true, "wrong_device": true,
}

// call sends one operation and waits for its answer. See Manager.Call.
func (s *session) call(ctx context.Context, op string, args json.RawMessage) (json.RawMessage, error) {
	select {
	case s.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, unsent(&Error{Code: CodeUnreachable, Message: "another call to this phone didn't finish in time"})
	}
	defer func() {
		s.mu.Lock()
		s.lastUse = s.m.now()
		s.mu.Unlock()
		<-s.turn
	}()

	readOnly := ReadOnly(op)
	retries := 0
	for {
		c, err := s.await(ctx)
		if err != nil {
			return nil, err
		}
		if !s.current(c) { // the record changed under the link (another process): a new link first
			s.restartLink(c)
			if !waitDown(ctx, c) {
				return nil, unsent(&Error{Code: CodeUnreachable, Message: "the phone's session was starting again"})
			}
			continue
		}
		answer, err := s.roundTrip(ctx, c, op, args)
		if err == nil {
			if op == opMountDDI {
				s.refreshInfo(ctx, c)
			}
			return answer, nil
		}
		if errors.Is(err, errNotSent) {
			// No whole line reached the bridge, so nothing reached the phone: send it on the next link.
			if ctx.Err() != nil || !waitDown(ctx, c) {
				return nil, unsent(&Error{Code: CodeUnreachable, Message: "the link to the phone broke before " + op + " was sent"})
			}
			continue
		}
		lost := errors.Is(err, errLinkLost) || Code(err) == bridgeDisconnected
		switch {
		case readOnly && lost && retries < readOnlyRetry:
			retries++
			continue
		case readOnly && errors.Is(err, errLinkLost):
			return nil, &Error{Code: bridgeDisconnected, Message: "the link to the phone dropped"}
		case readOnly && ctx.Err() != nil && Code(err) == "":
			return nil, &Error{Code: bridgeTimeout, Message: op + " didn't answer in time"}
		case readOnly:
			return nil, err
		case lost || Code(err) == bridgeTimeout || ctx.Err() != nil && Code(err) == "":
			s.m.logf("iphone %s: %s sent, outcome unknown: %v", short(s.udid), op, err)
			return nil, &Error{Code: CodeUncertain, Message: op + " was sent to the phone but its result never came back (" + err.Error() + "); it may or may not have happened, and it won't be sent again: look at the screen before trying again"}
		default:
			return nil, err
		}
	}
}

// refreshInfo reads device_info again on c right after the developer disk image was mounted, so the phone's status
// (its capabilities, ddi_mounted) says so at once rather than at the next health check, up to healthEvery later. A
// failure keeps what was there: the next health check, or the next link's ready event, reads it again.
func (s *session) refreshInfo(ctx context.Context, c *conn) {
	info, err := s.roundTrip(ctx, c, "device_info", nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.info = info
	s.mu.Unlock()
}

// waitDown waits for c to end; false when ctx ended first.
func waitDown(ctx context.Context, c *conn) bool {
	c.link.Close()
	select {
	case <-c.down:
		return true
	case <-ctx.Done():
		return false
	}
}

var (
	errLinkLost = errors.New("the link to the phone dropped")
	errNotSent  = errors.New("the request didn't reach the bridge")
)

// roundTrip sends one request on c and waits for its answer: the result, the bridge's *Error, errNotSent (nothing
// reached the bridge; c is being closed), errLinkLost, or ctx's error.
func (s *session) roundTrip(ctx context.Context, c *conn, op string, args json.RawMessage) (json.RawMessage, error) {
	id := newID()
	req := map[string]any{"v": 1, "id": id, "op": op}
	if len(args) > 0 {
		req["args"] = args
	}
	if dl, ok := ctx.Deadline(); ok {
		req["timeout_ms"] = max(int64(time.Until(dl)-answerMargin)/int64(time.Millisecond), 100)
	}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.link.Send(ctx, line); err != nil {
		c.link.Close()
		return nil, fmt.Errorf("%w: %v", errNotSent, err)
	}
	return waitAnswer(ctx, c, id)
}

// waitAnswer waits for the answer with id: its result, its *Error, errLinkLost, or ctx's error.
func waitAnswer(ctx context.Context, c *conn, id string) (json.RawMessage, error) {
	for {
		select {
		case line := <-c.answers:
			if result, ok, err := matchAnswer(line, id); ok {
				return result, err
			}
		case <-c.down:
			// The link may have answered just before it ended: those answers are already queued.
			for {
				select {
				case line := <-c.answers:
					if result, ok, err := matchAnswer(line, id); ok {
						return result, err
					}
				default:
					return nil, errLinkLost
				}
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// matchAnswer reads line as the answer to id; ok false when it answers something else (a late answer to an earlier
// call that timed out).
func matchAnswer(line []byte, id string) (result json.RawMessage, ok bool, err error) {
	var a struct {
		ID     string          `json:"id"`
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if json.Unmarshal(line, &a) != nil || a.ID != id {
		return nil, false, nil
	}
	if a.OK {
		if len(a.Result) == 0 {
			return json.RawMessage("{}"), true, nil
		}
		return a.Result, true, nil
	}
	if a.Error == nil || a.Error.Code == "" {
		return nil, true, &Error{Code: "internal", Message: "the bridge failed without saying why"}
	}
	return nil, true, a.Error
}

// status is the session's part of Device.
func (s *session) status(d *Device) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d.State = s.state
	d.Since = s.since
	if s.state == StateConnected {
		d.Address, d.Port, d.AddressSource = s.ep.Host, s.ep.Port, s.ep.Source
	}
	if s.lastErr != nil {
		d.LastError = s.lastErr.Error()
		d.LastErrorCode = Code(s.lastErr)
	}
	if len(s.info) > 0 {
		d.Info = s.info
		var di deviceInfo
		if json.Unmarshal(s.info, &di) == nil {
			d.Locked = di.Locked
			if di.OSVersion != "" {
				d.OSVersion = di.OSVersion
			}
			if di.DeveloperMode != nil {
				d.DeveloperMode = di.DeveloperMode
			}
		}
	}
}

// stop ends the live link, if any, on purpose: the supervisor connects again at once with what the record says now.
func (s *session) stop() {
	s.mu.Lock()
	c := s.cur
	s.mu.Unlock()
	if c != nil {
		s.restartLink(c)
	}
}

// deviceInfo is what the node reads of the bridge's device_info.
type deviceInfo struct {
	Name          string `json:"name"`
	ProductType   string `json:"product_type"`
	OSVersion     string `json:"os_version"`
	Locked        *bool  `json:"locked"`
	DeveloperMode *bool  `json:"developer_mode"`
	DDIMounted    *bool  `json:"ddi_mounted"`
}

func newID() string {
	var b [8]byte
	_, _ = cryptoRand(b[:])
	return hex.EncodeToString(b[:])
}

var cryptoRand = crand.Read

// ParseAddress reads a manual address: "host", "host:port", "[v6]:port" or a bare IPv6 address.
func ParseAddress(s string) (Endpoint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Endpoint{}, errors.New("empty address")
	}
	if host, port, err := net.SplitHostPort(s); err == nil {
		p, err := strconv.Atoi(port)
		if err != nil || p <= 0 || p > 65535 || host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, " /") {
			return Endpoint{}, fmt.Errorf("invalid address %q", s)
		}
		return Endpoint{Host: host, Port: p, Source: "manual"}, nil
	}
	host := strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	if strings.ContainsAny(host, " /[]") || strings.HasPrefix(host, "-") {
		return Endpoint{}, fmt.Errorf("invalid address %q", s)
	}
	return Endpoint{Host: host, Source: "manual"}, nil
}

func short(udid string) string {
	if len(udid) > 8 {
		return udid[:8]
	}
	return udid
}
