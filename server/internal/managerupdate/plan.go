// Package managerupdate controls an explicitly authorized update of one independent manager.
package managerupdate

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/update"
)

// Plan is produced by the existing release publisher, never inferred from a default manager.
// Idle evidence binds complete local runtime inventory to persisted manager identity.
// BootstrapWindow is explicit maintenance authorization, not an idle assertion.
type Plan struct {
	Schema              int       `json:"schema"`
	RunID               string    `json:"run_id"`
	ManagerID           string    `json:"manager_id"`
	Unassigned          bool      `json:"unassigned,omitempty"`
	Main                string    `json:"main,omitempty"`
	MachineID           string    `json:"machine_id,omitempty"`
	MachineUID          string    `json:"machine_uid,omitempty"`
	Dir                 string    `json:"dir"`
	Binary              string    `json:"binary"`
	Unit                string    `json:"unit"`
	PlistPath           string    `json:"plist_path"`
	UID                 int       `json:"uid"`
	PreviousSHA256      string    `json:"previous_sha256"`
	ConfigSHA256        string    `json:"config_sha256"`
	PlistSHA256         string    `json:"plist_sha256"`
	Source              string    `json:"source"`
	Version             string    `json:"version"`
	BinarySHA256        string    `json:"binary_sha256"`
	Size                int64     `json:"size"`
	ActivationEnabled   bool      `json:"activation_enabled"`
	BootstrapAuthorized bool      `json:"bootstrap_authorized"`
	BootstrapWindow     time.Time `json:"bootstrap_window_until"`
	IdleVerifiedAt      time.Time `json:"idle_verified_at"`
	IdleValidUntil      time.Time `json:"idle_valid_until"`
	IdleSnapshotSHA256  string    `json:"idle_snapshot_sha256"`
	Freeze              bool      `json:"freeze"`
}

var idPattern = lazyre.New(`^[a-z][a-z0-9-]{0,31}$`)
var runPattern = lazyre.New(`^[a-zA-Z0-9_-]{1,80}$`)
var shaPattern = lazyre.New(`^[0-9a-f]{64}$`)
var sourcePattern = lazyre.New(`^[0-9a-f]{40}$`)

func ReadPlan(path string) (Plan, error) {
	var p Plan
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer f.Close()
	return decodePlan(f)
}

func decodePlan(reader io.Reader) (Plan, error) {
	var p Plan
	const maxPlanBytes = 64 * 1024
	data, err := io.ReadAll(io.LimitReader(reader, maxPlanBytes+1))
	if err != nil {
		return p, err
	}
	if len(data) > maxPlanBytes {
		return p, errors.New("plan exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&p); err != nil {
		return p, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return p, errors.New("plan must contain exactly one JSON object")
	}
	return p, nil
}

func hashFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func canonical(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("noncanonical absolute path: %q", path)
	}
	resolved, e := filepath.EvalSymlinks(path)
	if e != nil {
		return e
	}
	if resolved != path {
		return fmt.Errorf("symlink path rejected: %s", path)
	}
	return nil
}

// validateIdentity binds a read to the original manager without requiring a
// release candidate, maintenance authorization or an idle result that is not yet known.
func (p Plan) validateIdentity() error {
	if p.Schema != 1 || !idPattern.MatchString(p.ManagerID) || p.ManagerID == "assistant" || p.UID < 0 {
		return errors.New("invalid schema or independent manager identity")
	}
	if p.Unassigned {
		if p.Main != "" || p.MachineID != "" || p.MachineUID != "" {
			return errors.New("unassigned inventory identity cannot contain a main, machine or UID")
		}
	} else if (p.Main != "" || p.MachineID != "" || p.MachineUID != "") && (p.Main == "" || p.MachineID == "" || p.MachineUID == "") {
		return errors.New("populated inventory identity must include main, machine and UID together")
	}
	for _, s := range []string{p.PreviousSHA256, p.ConfigSHA256, p.PlistSHA256} {
		if !shaPattern.MatchString(s) {
			return errors.New("invalid identity SHA256")
		}
	}
	if !filepath.IsAbs(p.Dir) || filepath.Clean(p.Dir) != p.Dir || p.Binary != filepath.Join(p.Dir, "bin", "personad") {
		return errors.New("target must be the independent manager directory's bin/personad")
	}
	if p.Unit == "" || filepath.Base(p.Unit) != p.Unit || !filepath.IsAbs(p.PlistPath) || filepath.Clean(p.PlistPath) != p.PlistPath || filepath.Base(p.PlistPath) != p.Unit+".plist" {
		return errors.New("invalid original launchd unit")
	}
	return nil
}

func (p Plan) validateShape() error {
	if err := p.validateIdentity(); err != nil {
		return err
	}
	if p.Unassigned || p.Main != "" || p.MachineID != "" || p.MachineUID != "" {
		return errors.New("inventory identity is not an activation plan")
	}
	if !runPattern.MatchString(p.RunID) || !sourcePattern.MatchString(p.Source) || p.Version == "" || p.Size <= 0 {
		return errors.New("invalid release identity")
	}
	for _, s := range []string{p.BinarySHA256, p.IdleSnapshotSHA256} {
		if !shaPattern.MatchString(s) {
			return errors.New("invalid SHA256")
		}
	}
	return nil
}

func (p Plan) authorize(now time.Time) error {
	if p.Freeze || !p.ActivationEnabled || !p.BootstrapAuthorized {
		return errors.New("activation disabled, frozen or bootstrap not authorized")
	}
	if !p.BootstrapWindow.After(now) || p.BootstrapWindow.Sub(now) > 15*time.Minute {
		return errors.New("explicit bootstrap maintenance window missing or too long")
	}
	if p.IdleVerifiedAt.After(now) || !p.IdleValidUntil.After(now) || p.IdleValidUntil.Sub(p.IdleVerifiedAt) > 60*time.Second || now.Sub(p.IdleVerifiedAt) > 60*time.Second {
		return errors.New("fresh scoped session idle evidence required (at most 60 seconds)")
	}
	return nil
}

func validateRelease(p Plan, build update.Build, exe string) error {
	if build.Channel != update.Release || build.Number <= 0 || build.Commit != p.Source || build.Version != p.Version {
		return errors.New("executor must be the exact same-source formal release")
	}
	info, e := buildinfo.ReadFile(exe)
	if e != nil {
		return e
	}
	if info.Path != "github.com/nwverse/persona/server/cmd/personad" {
		return errors.New("executor is not the full personad program")
	}
	revision, modified := "", ""
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if revision != p.Source || modified != "false" {
		return errors.New("executor has no matching clean full source identity")
	}
	st, e := os.Stat(exe)
	if e != nil {
		return e
	}
	h, e := hashFile(exe)
	if e != nil {
		return e
	}
	if st.Size() != p.Size || h != p.BinarySHA256 {
		return errors.New("release size or native SHA256 mismatch")
	}
	return nil
}
