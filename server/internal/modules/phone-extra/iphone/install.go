package iphone

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
)

// InstallSource is a release's build of the bridge for this computer, as the manager found it in the release's
// manifest (helpers, docs/extensions/iphone-bridge.md "Releases"): where to fetch it (tried in order) and what it must
// be: the archive's size and sha256, and the sha256 of the binary it unpacks to.
type InstallSource struct {
	URLs         []string `json:"urls"`
	Size         int64    `json:"size"`
	SHA256       string   `json:"sha256"`
	BinarySHA256 string   `json:"binary_sha256"`
}

// The install job's stages (Job.Stage) and its failures' codes (Job.Code).
const (
	StageDownload = "download"
	StageVerify   = "verify"
	StageInstall  = "install"

	CodeDownloadFailed   = "download_failed"
	CodeChecksumMismatch = "checksum_mismatch"
	CodeBadHelper        = "bad_helper"
	CodeInstallFailed    = "install_failed"
)

// installTimeout bounds an install job; installMax is the largest archive it takes.
var (
	installTimeout = 10 * time.Minute
	installMax     = int64(128 << 20)
)

// installClient fetches the build; tests replace it.
var installClient = &http.Client{Timeout: 0}

// BinDir is where an installed bridge goes, <PERSONA_HOME>/bin: one of the places FindBridge looks.
func BinDir() string { return filepath.Join(identity.HomeDir(), "bin") }

// StartInstall installs the bridge from src in the background (the app's Install, one job at a time): download, check
// it against src, check that it runs and says it is the bridge, then put it at <PERSONA_HOME>/bin in one rename. A
// bridge already there is replaced only once the new one checked out.
func (m *Manager) StartInstall(src InstallSource) error {
	if len(src.URLs) == 0 || src.Size <= 0 || src.Size > installMax || len(src.SHA256) != 64 || len(src.BinarySHA256) != 64 {
		return &Error{Code: "bad_request", Message: "no build to install"}
	}
	for _, u := range src.URLs {
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return &Error{Code: "bad_request", Message: "a build's address must be http(s)"}
		}
	}
	return m.startJob("install", "", func(ctx context.Context, step func(string, string)) error {
		ctx, cancel := context.WithTimeout(ctx, installTimeout)
		defer cancel()
		return m.install(ctx, src, BinDir(), step)
	})
}

func (m *Manager) install(ctx context.Context, src InstallSource, dir string, step func(string, string)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &Error{Code: CodeInstallFailed, Message: err.Error()}
	}
	archive := filepath.Join(dir, "."+BridgeName+".download")
	staged := filepath.Join(dir, "."+BridgeName+".new")
	defer os.Remove(archive)
	defer os.Remove(staged)

	step("downloading the iPhone bridge", StageDownload)
	checksumErr := &Error{Code: CodeChecksumMismatch, Message: "the download is not the release's build (size or sha256)"}
	var lastErr error
	integrityFailed := false
	for _, u := range src.URLs {
		if lastErr = download(ctx, u, archive, src.Size); lastErr == nil {
			sum, n, err := fileSHA256(archive)
			if err == nil && n == src.Size && sum == src.SHA256 {
				break
			}
			integrityFailed = true
			if err != nil {
				lastErr = err
			} else {
				lastErr = checksumErr
			}
		}
		if ctx.Err() != nil {
			break
		}
		m.logf("iphone bridge: download from the next address (%v)", lastErr)
	}
	if lastErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if integrityFailed {
			return checksumErr
		}
		return &Error{Code: CodeDownloadFailed, Message: "couldn't download the iPhone bridge: " + lastErr.Error()}
	}

	step("checking the iPhone bridge", StageVerify)
	if err := unpack(archive, staged); err != nil {
		return &Error{Code: CodeChecksumMismatch, Message: "couldn't unpack the download: " + err.Error()}
	}
	if sum, _, err := fileSHA256(staged); err != nil || sum != src.BinarySHA256 {
		return &Error{Code: CodeChecksumMismatch, Message: "the download doesn't unpack to the release's build (binary_sha256)"}
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return &Error{Code: CodeInstallFailed, Message: err.Error()}
	}
	vctx, vcancel := context.WithTimeout(ctx, 10*time.Second)
	out, err := exec.CommandContext(vctx, staged, "version").Output()
	vcancel()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), BridgeName+" ") {
		return &Error{Code: CodeBadHelper, Message: fmt.Sprintf("the downloaded bridge doesn't run here (%v)", err)}
	}

	step("installing the iPhone bridge", StageInstall)
	if err := os.Rename(staged, filepath.Join(dir, BridgeName)); err != nil {
		return &Error{Code: CodeInstallFailed, Message: err.Error()}
	}
	m.mu.Lock()
	m.usbAt = time.Time{} // ask the new bridge what is on the cable at the next look
	m.mu.Unlock()
	return nil
}

// download writes u to path, refusing anything but a 200 and more than want bytes.
func download(ctx context.Context, u, path string, want int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := installClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, want+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != want {
		return fmt.Errorf("%d bytes, not %d", n, want)
	}
	return nil
}

func unpack(archive, out string) error {
	in, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(zr, installMax*4))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
