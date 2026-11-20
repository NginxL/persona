package link

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// What persona-link keeps next to link.json besides the credential: the run lock, last reported status, route hint
// and log.

// RouteHintPath is the disposable active route cache beside link.json.
func RouteHintPath(path string) string {
	return strings.TrimSuffix(path, ".json") + ".route.json"
}

func readRouteHint(path string) routeHint {
	b, err := os.ReadFile(RouteHintPath(path))
	if err != nil {
		return routeHint{}
	}
	var hint routeHint
	if json.Unmarshal(b, &hint) != nil || !isFallbackKind(hint.Kind) || hint.Pairing == "" || hint.Network == "" {
		removeRouteHint(path)
		return routeHint{}
	}
	return hint
}

func writeRouteHint(path string, hint routeHint) error {
	b, err := json.Marshal(hint)
	if err != nil {
		return err
	}
	file := RouteHintPath(path)
	tmp := file + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	_, err = f.Write(append(b, '\n'))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func removeRouteHint(path string) {
	_ = os.Remove(RouteHintPath(path))
}

var errBusy = errors.New("locked")

// ErrRunning: another persona-link holds the run lock of this link.json.
var ErrRunning = newTextError("link-errors.running", nil)

// LockRun takes the run lock of the link.json at path: one persona-link per computer, or two would keep taking the
// link from each other. ErrRunning when another one has it; release gives it back (exiting does too).
func LockRun(path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".run", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f, false); err != nil {
		f.Close()
		if errors.Is(err, errBusy) {
			return nil, ErrRunning
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

// ErrLocked: another process holds the lock LockFile was asked for.
var ErrLocked = errBusy

// LockFile takes the exclusive lock of the file at path (made when missing), waiting for it when wait is true; ErrLocked
// when another process holds it and wait is false. release gives it back; the process ending does too, however it ends.
func LockFile(path string, wait bool) (release func(), err error) {
	f, err := LockFileHandle(path, wait)
	if err != nil {
		return nil, err
	}
	return func() { f.Close() }, nil
}

// LockFileHandle holds the lock until the caller closes f. A duplicated descriptor retains the same lock during exec.
func LockFileHandle(path string, wait bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f, wait); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Running says whether a persona-link is running with the link.json at path: whether its run lock is held. It takes the
// lock for an instant to find out, so a persona-link starting in that instant fails and is restarted by whatever runs it.
func Running(path string) bool {
	f, err := os.OpenFile(path+".run", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	return errors.Is(lockFile(f, false), errBusy)
}

// SavedStatus is the last Status a running persona-link reported, as it keeps it on disk for `persona-link status`.
// State is also "stopped" there once it has exited by itself.
type SavedStatus struct {
	Status
	PID     int       `json:"pid"`
	Version string    `json:"version,omitempty"`
	Updated time.Time `json:"updated"`
}

// StatusPath is where the persona-link of the link.json at path keeps its status: link.status.json beside it.
func StatusPath(path string) string {
	return strings.TrimSuffix(path, ".json") + ".status.json"
}

// WriteStatus replaces the saved status at once (another process may read it any time). It holds no credential.
func WriteStatus(path string, s SavedStatus) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	file := StatusPath(path)
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// ReadStatus reads the saved status; the error wraps fs.ErrNotExist when no persona-link has run here yet.
func ReadStatus(path string) (*SavedStatus, error) {
	b, err := os.ReadFile(StatusPath(path))
	if err != nil {
		return nil, err
	}
	var s SavedStatus
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Ranked is the order persona-link tries the candidates in.
func Ranked(eps []Endpoint) []Endpoint { return ranked(eps) }

// KindOf says what kind of address a URL is: lan | ipv6 | name | ipv4.
func KindOf(raw string) string { return kindOf(raw) }
