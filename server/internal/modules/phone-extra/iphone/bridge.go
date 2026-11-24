package iphone

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/osproc"
)

// BridgeName is the device bridge's program (its wire contract is #3451's): it speaks RemotePairing to the phone;
// this package only runs it.
const BridgeName = "persona-iphone-bridge"

// BridgeEnv names the bridge's path explicitly.
const BridgeEnv = "PERSONA_IPHONE_BRIDGE"

// FindBridge is the bridge's path: $PERSONA_IPHONE_BRIDGE, else in PATH, else <PERSONA_HOME>/bin, else next to
// this program.
func FindBridge() (string, error) {
	if p := os.Getenv(BridgeEnv); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", &Error{Code: CodeBridgeMissing, Message: BridgeEnv + ": " + err.Error()}
		}
		return p, nil
	}
	if p, err := exec.LookPath(BridgeName); err == nil {
		return p, nil
	}
	dirs := []string{filepath.Join(identity.HomeDir(), "bin")}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dirs = append(dirs, filepath.Dir(exe))
	}
	for _, d := range dirs {
		p := filepath.Join(d, BridgeName)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", &Error{Code: CodeBridgeMissing, Message: BridgeName + " is not installed on this computer"}
}

// Bridge runs the bridge at Path.
type Bridge struct{ Path string }

// USBDevice is a phone the bridge sees over USB (usb-list).
type USBDevice struct {
	UDID           string `json:"udid"`
	Name           string `json:"name,omitempty"`
	Model          string `json:"model,omitempty"` // the product type, e.g. iPhone16,2
	ProductVersion string `json:"product_version,omitempty"`
}

// PairInfo is what pair says about the phone it paired.
type PairInfo struct {
	UDID           string `json:"udid"`
	Name           string `json:"name,omitempty"`
	Model          string `json:"model,omitempty"` // the product type, e.g. iPhone16,2
	ProductType    string `json:"product_type,omitempty"`
	ProductVersion string `json:"product_version,omitempty"`
	Transport      string `json:"transport,omitempty"` // "simulator" for a Simulator device
	DeveloperMode  *bool  `json:"developer_mode,omitempty"`
}

// maxOutput bounds what a one-shot command's stdout and stderr are kept to.
const maxOutput = 1 << 20

// Version is `bridge version`.
func (b Bridge) Version(ctx context.Context) (string, error) {
	out, err := b.oneShot(ctx, "version")
	return strings.TrimSpace(string(out)), err
}

// USBList is `bridge usb-list --json`: the phones plugged in over USB now.
func (b Bridge) USBList(ctx context.Context) ([]USBDevice, error) {
	out, err := b.oneShot(ctx, "usb-list", "--json")
	if err != nil {
		return nil, err
	}
	var list []USBDevice
	if err := json.Unmarshal(bytes.TrimSpace(out), &list); err != nil {
		return nil, fmt.Errorf("%s usb-list: %w", BridgeName, err)
	}
	return list, nil
}

// Pair is `bridge pair --udid udid --out out`: the trusted USB pairing, the record written to out. It waits while the
// phone asks its user to trust this computer; ctx bounds that.
func (b Bridge) Pair(ctx context.Context, udid, out string) (PairInfo, error) {
	stdout, err := b.oneShot(ctx, "pair", "--udid", udid, "--out", out)
	if err != nil {
		return PairInfo{}, err
	}
	var info PairInfo
	if err := json.Unmarshal(lastLine(stdout), &info); err != nil {
		return PairInfo{}, fmt.Errorf("%s pair: %w", BridgeName, err)
	}
	if info.UDID == "" {
		info.UDID = udid
	}
	if info.ProductType == "" {
		info.ProductType = info.Model
	}
	return info, nil
}

// RevealDeveloperMode is `bridge reveal-developer-mode --udid udid`: Developer Mode shown in the phone's Settings ›
// Privacy & Security, where iOS keeps it hidden until a developer tool asks (#8199). The phone must be on the cable
// and trust this computer; it turns nothing on.
func (b Bridge) RevealDeveloperMode(ctx context.Context, udid string) error {
	_, err := b.oneShot(ctx, "reveal-developer-mode", "--udid", udid)
	return err
}

// Match is `bridge match --record path --identifier id --auth-tag tag`: whether the advertisement with that TXT
// identifier and authTag is the phone the record was made with (the tag is keyed with a secret in the record).
func (b Bridge) Match(ctx context.Context, recordPath string, svc Service) (bool, error) {
	out, err := b.oneShot(ctx, "match", "--record", recordPath, "--identifier", svc.Identifier(), "--auth-tag", svc.AuthTag())
	if err != nil {
		return false, err
	}
	var v struct {
		Match bool `json:"match"`
	}
	if err := json.Unmarshal(lastLine(out), &v); err != nil {
		return false, fmt.Errorf("%s match: %w", BridgeName, err)
	}
	return v.Match, nil
}

// Matcher says whether svc is the phone of the pairing record at recordPath.
type Matcher func(ctx context.Context, recordPath string, svc Service) (bool, error)

// oneShot runs one command to its end. A failure's error is the JSON on the last line of stderr (an *Error).
func (b Bridge) oneShot(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, b.Path, args...)
	cmd.SysProcAttr = osproc.GroupAttr()
	var stdout, stderr capped
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if e := bridgeError(stderr.Bytes()); e != nil {
		return nil, e
	}
	if line := strings.TrimSpace(string(lastLine(stderr.Bytes()))); line != "" {
		return nil, fmt.Errorf("%s %s: %v: %s", BridgeName, args[0], err, line)
	}
	return nil, fmt.Errorf("%s %s: %w", BridgeName, args[0], err)
}

// bridgeError is the {"ok":false,"error":{…}} on the last line of a bridge's stderr, or nil.
func bridgeError(stderr []byte) *Error {
	var v struct {
		OK    *bool  `json:"ok"`
		Error *Error `json:"error"`
	}
	if json.Unmarshal(lastLine(stderr), &v) != nil || v.Error == nil || v.Error.Code == "" {
		return nil
	}
	return v.Error
}

// lastLine is b's last non-empty line.
func lastLine(b []byte) []byte {
	b = bytes.TrimRight(b, "\r\n\t ")
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	return bytes.TrimSpace(b)
}

// capped keeps the first maxOutput bytes written to it, and the last ones once it is full (so the error line at the
// end of a long stderr survives).
type capped struct {
	mu   sync.Mutex
	head []byte
	tail []byte
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if room := maxOutput - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head = append(c.head, p[:k]...)
		p = p[k:]
	}
	if len(p) > 0 {
		c.tail = append(c.tail, p...)
		if over := len(c.tail) - 64<<10; over > 0 {
			c.tail = c.tail[over:]
		}
	}
	return n, nil
}

func (c *capped) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tail) == 0 {
		return c.head
	}
	return append(append(append([]byte{}, c.head...), '\n'), c.tail...)
}

// Endpoint is where to reach a phone on the network.
type Endpoint struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"` // 0: the bridge's default
	// Source says how it was found: "manual" (the record's Address), "bonjour", or "last" (the last one that worked).
	Source string `json:"source"`
}

func (e Endpoint) String() string {
	if e.Host == "" {
		return e.Source // a Simulator device
	}
	if e.Port == 0 {
		return e.Host
	}
	return hostPort(e.Host, e.Port)
}

func hostPort(host string, port int) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + strconv.Itoa(port)
	}
	return host + ":" + strconv.Itoa(port)
}

// Link is one running bridge session with one phone: requests in, answers and events out, one line of JSON each.
type Link interface {
	// Send writes one request line, giving up (and closing the link) when ctx ends first. A failed Send left no
	// complete line with the bridge, which acts only on whole lines, so nothing of it reached the phone. A write that
	// finished as ctx ended returns nil: the line did reach the bridge.
	Send(ctx context.Context, line []byte) error
	// Lines are the bridge's stdout lines; closed when the session ends.
	Lines() <-chan []byte
	// Err is why the session ended, once Lines is closed.
	Err() error
	// Close ends the session: stdin closed (the bridge stops by itself), killed if it hasn't after a grace period.
	Close()
}

// Dialer starts a session with the phone of rec at ep and returns once the bridge says it is ready, with the
// device_info the ready event carried.
type Dialer func(ctx context.Context, rec Record, recordPath string, ep Endpoint) (Link, json.RawMessage, error)

// readyTimeout bounds how long serve may take to bring the tunnel up.
const readyTimeout = 45 * time.Second

// Serve is the Dialer that runs `bridge serve`.
func (b Bridge) Serve(ctx context.Context, rec Record, recordPath string, ep Endpoint) (Link, json.RawMessage, error) {
	args := []string{"serve", "--udid", rec.UDID, "--record", recordPath}
	if ep.Host != "" { // a Simulator device has none: the bridge finds it from the record
		args = append(args, "--host", ep.Host)
	}
	if ep.Port > 0 {
		args = append(args, "--port", strconv.Itoa(ep.Port))
	}
	cmd := exec.Command(b.Path, args...)
	cmd.SysProcAttr = osproc.GroupAttr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	l := &procLink{cmd: cmd, stdin: stdin, lines: make(chan []byte, 16), done: make(chan struct{})}
	cmd.Stderr = &l.stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	go l.read(stdout)

	timer := time.NewTimer(readyTimeout)
	defer timer.Stop()
	fail := func() {
		l.Close()
		go func() {
			for range l.lines { // let the reader reach the end and reap the process
			}
		}()
	}
	select {
	case line, ok := <-l.lines:
		if !ok {
			return nil, nil, l.Err()
		}
		var ev struct {
			Event  string          `json:"event"`
			Device json.RawMessage `json:"device"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Event != "ready" {
			fail()
			return nil, nil, fmt.Errorf("%s serve: expected the ready event, got %.200s", BridgeName, line)
		}
		return l, ev.Device, nil
	case <-timer.C:
		fail()
		return nil, nil, &Error{Code: CodeUnreachable, Message: "the phone's session didn't come up within " + readyTimeout.String()}
	case <-ctx.Done():
		fail()
		return nil, nil, ctx.Err()
	}
}

// maxLine bounds one line from the bridge (screenshots come as base64).
const maxLine = 64 << 20

type procLink struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr capped
	lines  chan []byte
	done   chan struct{}

	mu        sync.Mutex
	err       error
	closeOnce sync.Once
}

func (l *procLink) Send(ctx context.Context, line []byte) error {
	return sendLine(ctx, l.stdin, l.Close, line)
}

// sendLine writes line (with its newline) to w, giving up when ctx ends first: then it closes the link (which ends a
// blocked write) and still waits for the write's outcome. Only a failed write is a line that never reached the
// bridge; a write that finished as the deadline passed did reach it, so it returns nil and the caller waits for the
// answer (which, past the deadline, makes an input operation uncertain rather than safe to send again).
func sendLine(ctx context.Context, w io.Writer, closeLink func(), line []byte) error {
	if !bytes.HasSuffix(line, []byte("\n")) {
		line = append(line, '\n')
	}
	done := make(chan error, 1)
	go func() {
		_, err := w.Write(line)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		closeLink()
		if err := <-done; err != nil {
			return fmt.Errorf("%w (%v)", ctx.Err(), err)
		}
		return nil
	}
}

func (l *procLink) Lines() <-chan []byte { return l.lines }

func (l *procLink) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *procLink) Close() {
	l.closeOnce.Do(func() {
		l.stdin.Close()
		go func() {
			select {
			case <-l.done:
			case <-time.After(3 * time.Second):
				_ = osproc.StopGroup(l.cmd.Process.Pid, true)
			}
		}()
	})
}

// read passes stdout's lines on until it ends, then reaps the process and records why it ended.
func (l *procLink) read(stdout io.Reader) {
	r := bufio.NewReaderSize(stdout, 64<<10)
	var reason string
	var readErr error
	for {
		line, err := readLine(r)
		if len(line) > 0 {
			var ev struct {
				Event  string `json:"event"`
				Reason string `json:"reason"`
			}
			if json.Unmarshal(line, &ev) == nil && ev.Event == "disconnected" {
				reason = ev.Reason
			}
			l.lines <- line
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
				_ = osproc.StopGroup(l.cmd.Process.Pid, true)
			}
			break
		}
	}
	waitErr := l.cmd.Wait()
	l.mu.Lock()
	switch {
	case reason != "":
		l.err = &Error{Code: bridgeDisconnected, Message: reason}
	case readErr != nil:
		l.err = readErr
	default:
		if e := bridgeError(l.stderr.Bytes()); e != nil {
			l.err = e
		} else if waitErr != nil {
			msg := waitErr.Error()
			if line := strings.TrimSpace(string(lastLine(l.stderr.Bytes()))); line != "" {
				msg += ": " + line
			}
			l.err = &Error{Code: bridgeDisconnected, Message: msg}
		} else {
			l.err = &Error{Code: bridgeDisconnected, Message: "the bridge ended the session"}
		}
	}
	l.mu.Unlock()
	close(l.lines)
	close(l.done)
}

// readLine reads one line (without its newline), refusing ones over maxLine.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			return nil, fmt.Errorf("%s: a line over %d bytes", BridgeName, maxLine)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimRight(buf, "\r\n"), err
	}
}
