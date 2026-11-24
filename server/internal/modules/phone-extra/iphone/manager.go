package iphone

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
)

// Manager is this computer's paired phones and their sessions. One per node process (Default); its sessions end only
// with Close.
type Manager struct {
	store Store
	// findBridge, dial and browse are the bridge and the network; tests replace them.
	findBridge func() (string, error)
	dial       Dialer
	browse     Browser
	match      Matcher // nil: the bridge's match
	now        func() time.Time
	logf       func(format string, args ...any)

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	usb      []USBDevice // usbList's last answer, at usbAt
	usbAt    time.Time
	closed   bool
	wg       sync.WaitGroup // the sessions' supervisors and the job
	sessions map[string]*session
	job      *Job
	jobStop  context.CancelFunc
}

// New is a Manager keeping its records in dir.
func New(dir string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		store:      Store{Dir: dir},
		findBridge: FindBridge,
		browse:     Browse,
		now:        time.Now,
		logf:       log.Printf,
		ctx:        ctx,
		cancel:     cancel,
		sessions:   map[string]*session{},
	}
}

// Dir is where the node keeps its paired phones: <PERSONA_HOME>/iphone.
func Dir() string { return filepath.Join(identity.HomeDir(), "iphone") }

var defaultManager = sync.OnceValue(func() *Manager { return New(Dir()) })

// Default is the node process's Manager.
func Default() *Manager { return defaultManager() }

// Store is the records the Manager keeps.
func (m *Manager) Store() Store { return m.store }

// Close ends every session and waits for them to stop. The Manager can't be used afterwards.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
}

// spawn runs f in a goroutine Close waits for; false once closed.
func (m *Manager) spawn(f func()) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		f()
	}()
	return true
}

// BridgeInfo is whether the device bridge is installed here.
type BridgeInfo struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"` // installed, but `version` failed
}

// Bridge finds the bridge and asks its version.
func (m *Manager) Bridge(ctx context.Context) BridgeInfo {
	path, err := m.findBridge()
	if err != nil {
		return BridgeInfo{}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, err := Bridge{Path: path}.Version(ctx)
	info := BridgeInfo{Installed: true, Path: path, Version: v}
	if err != nil {
		info.Error = err.Error()
	}
	return info
}

func (m *Manager) bridge() (Bridge, error) {
	path, err := m.findBridge()
	if err != nil {
		return Bridge{}, err
	}
	return Bridge{Path: path}, nil
}

func (m *Manager) matcher() (Matcher, error) {
	if m.match != nil {
		return m.match, nil
	}
	b, err := m.bridge()
	if err != nil {
		return nil, err
	}
	return b.Match, nil
}

func (m *Manager) dialer() (Dialer, error) {
	if m.dial != nil {
		return m.dial, nil
	}
	b, err := m.bridge()
	if err != nil {
		return nil, err
	}
	return b.Serve, nil
}

// Device is one phone: paired here, plugged in over USB, or both.
type Device struct {
	// UDID is the phone's stable id (the same across addresses and reconnections).
	UDID        string `json:"udid"`
	Name        string `json:"name,omitempty"`
	ProductType string `json:"product_type,omitempty"` // the model identifier, e.g. iPhone16,2
	OSVersion   string `json:"os_version,omitempty"`
	Paired      bool   `json:"paired"`
	USB         bool   `json:"usb"` // plugged in over USB now (what pairing needs)
	// State is the session's (State* constants); Since is when it got there.
	State string    `json:"state"`
	Since time.Time `json:"since,omitzero"`
	// Transport is how the session reaches the device: "network" (a phone; USB is only for pairing) or "simulator".
	Transport string `json:"transport,omitempty"`
	// Locked is the phone's lock state from its last device_info; nil when unknown.
	Locked *bool `json:"locked,omitempty"`
	// DeveloperMode is what the phone last said of it (at pairing, then in each device_info); nil when it never did.
	DeveloperMode *bool `json:"developer_mode,omitempty"`
	// AutoPrepare: the owner prepared it once, so this computer mounts its developer disk image again by itself.
	AutoPrepare bool `json:"auto_prepare,omitempty"`
	// Address and Port are where the session is connected, AddressSource how that was found (manual / bonjour /
	// last). Local to this computer: the phone's network address isn't sent to the hub.
	Address       string `json:"address,omitempty"`
	Port          int    `json:"port,omitempty"`
	AddressSource string `json:"address_source,omitempty"`
	// ManualAddress is the address set with `personad iphone address`, if any.
	ManualAddress string `json:"manual_address,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	LastErrorCode string `json:"last_error_code,omitempty"`
	// Info is the bridge's last device_info (the session's ready event, then its health checks).
	Info     json.RawMessage `json:"info,omitempty"`
	PairedAt time.Time       `json:"paired_at,omitzero"`
}

// Devices is the paired phones with their sessions' state, then the phones plugged in over USB that aren't paired.
// It starts no session.
func (m *Manager) Devices(ctx context.Context) []Device {
	recs, err := m.store.List()
	if err != nil {
		m.logf("phone: %v", err)
	}
	usb := map[string]USBDevice{}
	for _, d := range m.usbList(ctx) {
		usb[d.UDID] = d
	}
	out := make([]Device, 0, len(recs)+len(usb))
	for _, r := range recs {
		d := Device{UDID: r.UDID, Name: r.Name, ProductType: r.ProductType, OSVersion: r.OSVersion, Paired: true, State: StateIdle,
			Transport: cmp.Or(r.Transport, TransportNetwork), ManualAddress: r.Address, PairedAt: r.PairedAt, DeveloperMode: r.DeveloperMode,
			AutoPrepare: r.AutoPrepare}
		_, d.USB = usb[r.UDID]
		delete(usb, r.UDID)
		m.mu.Lock()
		s := m.sessions[r.UDID]
		m.mu.Unlock()
		if s != nil {
			s.status(&d)
		}
		out = append(out, d)
	}
	for _, u := range usb {
		out = append(out, Device{UDID: u.UDID, Name: u.Name, ProductType: u.Model, OSVersion: u.ProductVersion, USB: true, State: StateIdle})
	}
	return out
}

// usbFresh is how long a usb-list answer is reused: status is polled every few seconds while the app waits.
var usbFresh = 5 * time.Second

// usbList is the phones plugged in over USB, from the bridge, at most usbFresh old; nil without a bridge.
func (m *Manager) usbList(ctx context.Context) []USBDevice {
	m.mu.Lock()
	if m.now().Sub(m.usbAt) < usbFresh {
		list := m.usb
		m.mu.Unlock()
		return list
	}
	m.mu.Unlock()
	b, err := m.bridge()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := b.USBList(ctx)
	if err != nil {
		return nil
	}
	m.mu.Lock()
	m.usb, m.usbAt = list, m.now()
	m.mu.Unlock()
	return list
}

// Call sends op with args to the phone udid and returns the bridge's result. It connects first when needed, waiting
// within ctx's deadline (which also bounds the operation: the bridge gets it as timeout_ms). Errors are *Error:
//   - not_paired, bridge_missing, unreachable: nothing was sent;
//   - uncertain: an operation that changes the phone was sent, and the link dropped or the deadline passed before its
//     answer; it may have happened, and is not sent again;
//   - the bridge's own codes, which it answered with.
//
// Read-only operations (ReadOnly) are sent once more after a dropped link.
func (m *Manager) Call(ctx context.Context, udid, op string, args json.RawMessage) (json.RawMessage, error) {
	if m.ctx.Err() != nil {
		return nil, &Error{Code: CodeUnreachable, Message: "the node is shutting down"}
	}
	if !ValidUDID(udid) {
		return nil, unsent(&Error{Code: "bad_request", Message: "invalid device id"})
	}
	if _, err := m.store.Load(udid); err != nil {
		if errors.Is(err, ErrNotPaired) {
			return nil, unsent(&Error{Code: CodeNotPaired, Message: "this phone isn't paired with this computer: plug it in over USB and run personad iphone pair"})
		}
		return nil, unsent(&Error{Code: "internal", Message: err.Error()})
	}
	if _, err := m.dialer(); err != nil {
		return nil, unsent(err)
	}
	return m.session(udid).call(ctx, op, args)
}

// Sent says what an error from Call means for the operation: "no" it never reached the phone (no session: not
// paired, no bridge, unreachable, or a failure to connect that needs the user), "unknown" it may have happened
// (uncertain, or an error without a code), "answered" the bridge got it and said how it ended.
func Sent(err error) string {
	var e *Error
	if errors.As(err, &e) && e.beforeSend {
		return "no"
	}
	switch Code(err) {
	case CodeNotPaired, CodeBridgeMissing, CodeUnreachable, CodeBusy:
		return "no"
	case CodeUncertain, "":
		return "unknown"
	default:
		return "answered"
	}
}

// Connect starts udid's session without sending anything (the session then stays up while idle).
func (m *Manager) Connect(udid string) error {
	if _, err := m.store.Load(udid); err != nil {
		return err
	}
	m.session(udid).ensureRunning()
	return nil
}

func (m *Manager) session(udid string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[udid]
	if s == nil {
		s = newSession(m, udid)
		m.sessions[udid] = s
	}
	return s
}

// disconnect ends udid's live link (after pairing again or forgetting it).
func (m *Manager) disconnect(udid string) {
	m.mu.Lock()
	s := m.sessions[udid]
	m.mu.Unlock()
	if s != nil {
		s.stop()
	}
}

// remember keeps ep as the last address udid was reached at (when Bonjour found it), and what the bridge's
// device_info says of the phone now (its name and iOS version change). It re-reads the record so a change made
// meanwhile (by the command line) isn't lost, and writes only when something changed.
func (m *Manager) remember(udid string, ep Endpoint, info json.RawMessage) {
	rec, err := m.store.Load(udid)
	if err != nil {
		return
	}
	was := rec
	if ep.Source == "bonjour" {
		rec.LastHost, rec.LastPort = ep.Host, ep.Port
	}
	var di deviceInfo
	if len(info) > 0 && json.Unmarshal(info, &di) == nil {
		rec.Name = cmp.Or(di.Name, rec.Name)
		rec.ProductType = cmp.Or(di.ProductType, rec.ProductType)
		rec.OSVersion = cmp.Or(di.OSVersion, rec.OSVersion)
		// Turned on after pairing (the phone restarted for it): the record stops saying off, so a phone that can't be
		// reached later reads unreachable, not developer_mode_off (session.go).
		if di.DeveloperMode != nil && (rec.DeveloperMode == nil || *rec.DeveloperMode != *di.DeveloperMode) {
			on := *di.DeveloperMode
			rec.DeveloperMode = &on
		}
	}
	if rec == was {
		return
	}
	if err := m.store.Save(rec); err != nil {
		m.logf("iphone %s: %v", short(udid), err)
	}
}

// PairProgress is what a pairing says while it runs: the step in words (English, for the command line and logs) and,
// while it waits on the person at the phone, the reason as a code (CodeTrustPending: tap Trust and enter the
// passcode; CodeLocked: unlock it), "" otherwise. Said again only when one of them changes.
type PairProgress func(step, waiting string)

// The bridge's answers to pair that only the person at the phone can change: the phone doesn't trust this computer
// yet, or is locked. Pair waits them out (#8199).
const (
	CodeTrustPending = "trust_pending"
	CodeLocked       = "locked"
)

// pairRetry is how often Pair asks the bridge again while the phone waits for its user; revealTimeout bounds
// showing Developer Mode in Settings after pairing.
var (
	pairRetry     = 2 * time.Second
	revealTimeout = 15 * time.Second
)

// Pair pairs the phone udid, plugged in over USB, with this computer: the bridge's trusted pairing, the record kept
// here. The phone asks its user to trust this computer (and for its passcode) meanwhile: while the bridge answers
// that the phone doesn't trust this computer yet, or is locked, Pair asks again every pairRetry until ctx ends (it
// then returns ctx's error). A manual address set before is kept.
func (m *Manager) Pair(ctx context.Context, udid string, progress PairProgress) (Record, error) {
	if progress == nil {
		progress = func(string, string) {}
	}
	said := [2]string{"\x00", ""}
	step := func(text, waiting string) {
		if said != [2]string{text, waiting} {
			said = [2]string{text, waiting}
			progress(text, waiting)
		}
	}
	b, err := m.bridge()
	if err != nil {
		return Record{}, err
	}
	prev, prevErr := m.store.Load(udid)
	dir, err := m.store.ensureDir(udid)
	if err != nil {
		return Record{}, err
	}
	cleanup := func() {
		if errors.Is(prevErr, ErrNotPaired) { // nothing was paired before: leave no half-made directory
			_ = m.store.Forget(udid)
		}
	}
	tmp := filepath.Join(dir, "."+RecordFile+".new")
	_ = os.Remove(tmp)
	defer os.Remove(tmp)
	step("waiting for the phone: unlock it and tap Trust, then enter its passcode", "")
	var info PairInfo
	for {
		info, err = b.Pair(ctx, udid, tmp)
		code := Code(err)
		if err == nil || (code != CodeTrustPending && code != CodeLocked) {
			break
		}
		if code == CodeLocked {
			step("waiting for the phone to be unlocked", code)
		} else {
			step("waiting for the phone: tap Trust on it, then enter its passcode", code)
		}
		select {
		case <-ctx.Done():
		case <-time.After(pairRetry):
		}
		if ctx.Err() != nil { // the wait ran out (or was cancelled) with the phone still waiting for its user
			err = ctx.Err()
			break
		}
	}
	if err != nil {
		cleanup()
		return Record{}, err
	}
	// Paired with Developer Mode off: the phone neither advertises nor takes connections until it is on, and its
	// switch is hidden in Settings until a developer tool asks for it. Ask while the phone is still on the cable;
	// a bridge or phone that can't is only logged (the app still says where the switch is).
	if info.DeveloperMode != nil && !*info.DeveloperMode && info.Transport != TransportSimulator {
		step("showing Developer Mode in the phone's Settings", "")
		rctx, cancel := context.WithTimeout(ctx, revealTimeout)
		if err := b.RevealDeveloperMode(rctx, udid); err != nil {
			m.logf("iphone %s: showing Developer Mode in Settings: %v", short(udid), err)
		}
		cancel()
	}
	step("saving the pairing record", "")
	if err := os.Chmod(tmp, 0o600); err != nil {
		cleanup()
		return Record{}, err
	}
	path, _ := m.store.RecordPath(udid)
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return Record{}, err
	}
	rec := Record{UDID: udid, Name: info.Name, ProductType: info.ProductType, OSVersion: info.ProductVersion, DeveloperMode: info.DeveloperMode, PairedAt: m.now().UTC()}
	if info.Transport == TransportSimulator {
		rec.Transport = TransportSimulator
	}
	if prevErr == nil {
		rec.Address = prev.Address
		rec.AutoPrepare = prev.AutoPrepare // the owner's word for this phone on this computer stands
	}
	if err := m.store.Save(rec); err != nil {
		return Record{}, err
	}
	m.disconnect(udid) // a live session reconnects with the new record
	return rec, nil
}

// Forget removes the phone udid's pairing from this computer and ends its session. The phone keeps listing this
// computer as trusted until its user resets that on the phone.
func (m *Manager) Forget(udid string) error {
	if !ValidUDID(udid) {
		return &Error{Code: "bad_request", Message: "invalid device id"}
	}
	err := m.store.Forget(udid)
	m.disconnect(udid)
	return err
}

// SetAddress sets (or with "" clears) the phone's manual address.
func (m *Manager) SetAddress(udid, address string) error {
	rec, err := m.store.Load(udid)
	if err != nil {
		return err
	}
	if address != "" {
		if _, err := ParseAddress(address); err != nil {
			return err
		}
	}
	rec.Address = address
	if err := m.store.Save(rec); err != nil {
		return err
	}
	m.disconnect(udid)
	return nil
}
