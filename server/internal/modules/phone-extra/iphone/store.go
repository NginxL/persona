// Package iphone keeps this computer's paired iPhones: the pairing records made once over trusted USB, finding each
// phone again on the network (Bonjour, a manual address) and one resident session per phone through the device
// bridge (docs/extensions/iphone-pairing.md). The node's phone capability calls it; it knows nothing of the hub.
package iphone

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Record is what this computer keeps about one paired phone besides the pairing record itself: nothing secret. The
// pairing record (keys) is the bridge's own format in RecordFile next to it, never read here.
type Record struct {
	// UDID is the phone's stable id: the same across addresses, reconnections and iOS updates.
	UDID        string `json:"udid"`
	Name        string `json:"name,omitempty"`
	ProductType string `json:"product_type,omitempty"` // the model identifier, e.g. iPhone16,2
	OSVersion   string `json:"os_version,omitempty"`
	// Transport is how sessions reach it: "" or TransportNetwork for a phone (RemotePairing over the network),
	// TransportSimulator for a Simulator device the bridge drives on this computer (no address to find).
	Transport string `json:"transport,omitempty"`
	// DeveloperMode is what the phone said at pairing; nil when it didn't. Off, it pairs but neither advertises
	// nor takes connections over the network.
	DeveloperMode *bool `json:"developer_mode,omitempty"`
	// Address is a manual "host" or "host:port" (a Tailscale address or name, where Bonjour doesn't reach); tried
	// before Bonjour when set.
	Address string `json:"address,omitempty"`
	// LastHost / LastPort is the last address a session came up on: the fallback when Bonjour finds nothing.
	LastHost string `json:"last_host,omitempty"`
	LastPort int    `json:"last_port,omitempty"`
	// AutoPrepare: the owner prepared this phone from the app once (its developer disk image mounted, StartPrepare),
	// which allows this computer to mount it again by itself whenever the phone comes back without it, as after
	// every restart (#8199). Each mount asks Apple's signing server for the phone's ticket (iphone-bridge.md
	// "mount_ddi"). Forgetting the phone drops it with the record.
	AutoPrepare bool      `json:"auto_prepare,omitempty"`
	PairedAt    time.Time `json:"paired_at"`
}

// Record.Transport values.
const (
	TransportNetwork   = "network"
	TransportSimulator = "simulator"
)

// Simulator says whether r is a Simulator device rather than a phone.
func (r Record) Simulator() bool { return r.Transport == TransportSimulator }

const (
	metaFile   = "device.json"
	RecordFile = "pairing.record"
)

// udidPattern keeps a UDID a single safe path element.
var udidPattern = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9-]{3,63}$`)

// ValidUDID says whether udid can name a device directory.
func ValidUDID(udid string) bool { return udidPattern.MatchString(udid) }

// ErrNotPaired is a UDID with no record here.
var ErrNotPaired = errors.New("phone is not paired with this computer")

// Store is the private directory with one subdirectory per paired phone: <dir>/<udid>/{device.json,pairing.record},
// directories 0700 and files 0600.
type Store struct{ Dir string }

func (s Store) deviceDir(udid string) (string, error) {
	if !ValidUDID(udid) {
		return "", fmt.Errorf("invalid device id %q", udid)
	}
	return filepath.Join(s.Dir, udid), nil
}

// RecordPath is where udid's pairing record lives (whether or not it exists yet).
func (s Store) RecordPath(udid string) (string, error) {
	dir, err := s.deviceDir(udid)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, RecordFile), nil
}

// Load reads udid's record; ErrNotPaired when it or its pairing record is missing.
func (s Store) Load(udid string) (Record, error) {
	dir, err := s.deviceDir(udid)
	if err != nil {
		return Record{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotPaired
	}
	if err != nil {
		return Record{}, err
	}
	if _, err := os.Stat(filepath.Join(dir, RecordFile)); errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotPaired
	} else if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return Record{}, fmt.Errorf("%s: %w", filepath.Join(dir, metaFile), err)
	}
	r.UDID = udid // the directory names it
	return r, nil
}

// List is every paired phone, by name then UDID. Directories without a complete record are skipped.
func (s Store) List() ([]Record, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		if !e.IsDir() || !ValidUDID(e.Name()) {
			continue
		}
		r, err := s.Load(e.Name())
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].UDID < out[j].UDID
	})
	return out, nil
}

// Save writes r's metadata (not the pairing record), atomically and 0600.
func (s Store) Save(r Record) error {
	dir, err := s.ensureDir(r.UDID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, metaFile), append(b, '\n'))
}

// ensureDir makes the store and udid's directory, both 0700 (tightening ones that already exist).
func (s Store) ensureDir(udid string) (string, error) {
	dir, err := s.deviceDir(udid)
	if err != nil {
		return "", err
	}
	for _, d := range []string{s.Dir, dir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// Forget removes udid's directory: its metadata and pairing record. Forgetting a phone that isn't paired is not an error.
func (s Store) Forget(udid string) error {
	dir, err := s.deviceDir(udid)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// writePrivate writes path through a temporary file in the same directory, 0600, then renames it into place.
func writePrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // gone after the rename; left over only on failure
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
