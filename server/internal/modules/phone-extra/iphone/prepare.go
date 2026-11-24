package iphone

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// opMountDDI is the bridge's operation that mounts the developer disk image (iphone-bridge.md "mount_ddi").
const opMountDDI = "mount_ddi"

// StagePrepare is a prepare job's one stage (Job.Stage): mounting the image.
const StagePrepare = "mount"

// prepareTimeout bounds one mount: the image goes through the tunnel and the ticket comes from Apple's signing server.
var prepareTimeout = 5 * time.Minute

// StartPrepare prepares the paired phone udid in the background (the guided setup's Prepare, one job at a time): its
// developer disk image mounted through its session, which the developer services behind screenshots, input and apps
// need. Done, it allows this computer to mount the image again by itself whenever the phone comes back without it
// (Record.AutoPrepare), as after every restart. A failure carries the bridge's code (ddi_missing, tss_failed,
// developer_mode_off, locked, mount_failed, …).
func (m *Manager) StartPrepare(udid string) error {
	if _, err := m.store.Load(udid); err != nil {
		return err
	}
	return m.startJob("prepare", udid, func(ctx context.Context, step func(string, string)) error {
		ctx, cancel := context.WithTimeout(ctx, prepareTimeout)
		defer cancel()
		step("mounting the developer disk image", StagePrepare)
		if _, err := m.Call(ctx, udid, opMountDDI, json.RawMessage(`{}`)); err != nil {
			return err
		}
		rec, err := m.store.Load(udid)
		if err != nil {
			return err
		}
		if !rec.AutoPrepare {
			rec.AutoPrepare = true
			if err := m.store.Save(rec); err != nil {
				return err
			}
		}
		return nil
	})
}

// The folder the bridge reads the developer disk image from (iphone-bridge.md "mount_ddi"): $PERSONA_IPHONE_DDI_DIR,
// else the one Xcode installs. Nothing downloads it.
const (
	DDIDirEnv     = "PERSONA_IPHONE_DDI_DIR"
	DDIDefaultDir = "/Library/Developer/DeveloperDiskImages/iOS_DDI/Restore"
)

// DDIPresent says whether this computer has the developer disk image Prepare mounts: the bridge's folder holds its
// BuildManifest.plist. A Mac without Xcode, and a Linux computer, don't (#8199: the app says to install Xcode).
func DDIPresent() bool {
	dir := os.Getenv(DDIDirEnv)
	if dir == "" {
		dir = DDIDefaultDir
	}
	st, err := os.Stat(filepath.Join(dir, "BuildManifest.plist"))
	return err == nil && st.Mode().IsRegular()
}
