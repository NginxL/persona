package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const machineFileUploadIdle = 2 * time.Minute

func machineSensitiveConfirmError(reason string) error {
	data, _ := json.Marshal(map[string]any{"sensitive_reason": reason})
	return &rpc.Error{
		Code:    proto.CodeMachineFileSensitiveConfirm,
		Message: "explicit confirmation is required to modify or delete a sensitive file",
		Data:    data,
	}
}

func machineFileChangedError(st os.FileInfo, sha string) error {
	data, _ := json.Marshal(map[string]any{
		"mtime":    st.ModTime().UTC(),
		"sha256":   sha,
		"revision": fileRevision(st),
		"size":     st.Size(),
	})
	return &rpc.Error{
		Code:    proto.CodeFileChanged,
		Message: "file was modified externally since it was opened",
		Data:    data,
	}
}

func machineBinaryError(msg string) error {
	return &rpc.Error{Code: proto.CodeMachineFileBinary, Message: msg}
}

func machineTooLargeError(msg string) error {
	return &rpc.Error{Code: proto.CodeTooLarge, Message: msg}
}

func machineNotEmptyError() error {
	return &rpc.Error{
		Code:    proto.CodeMachineFileNotEmpty,
		Message: "directory is not empty; only empty directories can be deleted",
	}
}

// validateMachineEntryName validates a single file/directory name component before any path join.
func validateMachineEntryName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed == "." || trimmed == ".." {
		return "", machineBoundaryError("entry name is invalid")
	}
	if strings.ContainsAny(trimmed, `/\`) {
		return "", machineBoundaryError("entry name must not contain path separators")
	}
	for _, r := range trimmed {
		if r == 0 || r < 0x20 || r == 0x7f {
			return "", machineBoundaryError("entry name contains invalid control characters")
		}
	}
	return trimmed, nil
}

// validateMachineUTF8Text rejects oversized or binary/non-UTF-8 text payloads.
func validateMachineUTF8Text(path string, data []byte) error {
	if len(data) > proto.MachineFileEditMaxBytes {
		return machineTooLargeError(fmt.Sprintf("text exceeds maximum editable size (%d bytes)", proto.MachineFileEditMaxBytes))
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return machineBinaryError("content must be valid UTF-8 text without NUL bytes")
	}
	if bin, _ := isBinaryPreviewContent(path, data, false, false); bin {
		return machineBinaryError("binary content is not allowed for text editing")
	}
	return nil
}

// resolveMachineEntryNoFollow resolves an entry's parent canonically (enforcing symlink/off-limits/root guards)
// without following the final leaf itself, so rename/delete act on a symlink itself rather than its target.
type machineLeafResolution struct {
	DisplayPath     string
	ParentDisplay   string
	ParentCanonical string
	CanonicalLeaf   string
	Name            string
	RootPath        string
	Sensitive       bool
	SensitiveReason string
}

func (n *Node) resolveMachineEntryNoFollow(asked, boundaryRoot string) (*machineLeafResolution, error) {
	var realRoot, expandedRoot string
	if strings.TrimSpace(boundaryRoot) != "" {
		var err error
		expandedRoot, err = n.expandMachinePath(boundaryRoot, "")
		if err != nil {
			return nil, err
		}
		realRoot, err = n.resolveCanonicalBoundary(boundaryRoot)
		if err != nil {
			return nil, err
		}
	}

	display, err := n.expandMachinePath(asked, expandedRoot)
	if err != nil {
		return nil, err
	}
	absDisplay := absPath(display)
	parentDisplay := filepath.Dir(absDisplay)
	if parentDisplay == absDisplay {
		return nil, machineBoundaryError("cannot modify or delete filesystem root")
	}
	leafName, err := validateMachineEntryName(filepath.Base(absDisplay))
	if err != nil {
		return nil, err
	}

	parentReal, err := n.realPath(parentDisplay)
	if err != nil {
		if errors.Is(err, errOffLimits) || errors.Is(err, errICloud) || errors.Is(err, errTooManyLinks) {
			return nil, machineBoundaryError(err.Error())
		}
		return nil, explainDenied(display, n.home, err)
	}
	parentReal = filepath.Clean(parentReal)
	canonicalLeaf := filepath.Clean(filepath.Join(parentReal, leafName))

	if realRoot != "" && !isWithinCanonicalRoot(canonicalLeaf, realRoot) {
		return nil, machineBoundaryError("path escapes the allowed root boundary")
	}
	if err := n.offLimitsResolved(canonicalLeaf); err != nil {
		return nil, machineBoundaryError(err.Error())
	}
	if canonicalLeaf == parentReal || (n.home != "" && isHome(fold(canonicalLeaf), n.home)) {
		return nil, machineBoundaryError("cannot modify or delete protected root directory")
	}
	if realRoot != "" && fold(canonicalLeaf) == fold(realRoot) {
		return nil, machineBoundaryError("cannot modify or delete the root boundary directory itself")
	}

	sensitive, reason := classifyMachineSensitivePath(display, canonicalLeaf)
	return &machineLeafResolution{
		DisplayPath:     display,
		ParentDisplay:   parentDisplay,
		ParentCanonical: parentReal,
		CanonicalLeaf:   canonicalLeaf,
		Name:            leafName,
		RootPath:        realRoot,
		Sensitive:       sensitive,
		SensitiveReason: reason,
	}, nil
}

func (n *Node) buildMachineEntry(displayPath, canonicalPath, rootPath string, info os.FileInfo) proto.MachineFileEntry {
	name := filepath.Base(displayPath)
	isSymlink := info.Mode()&fs.ModeSymlink != 0
	item := proto.MachineFileEntry{
		Name:    name,
		Path:    displayPath,
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		Perm:    uint32(info.Mode().Perm()),
		ModTime: info.ModTime().UTC(),
		Symlink: isSymlink,
	}
	switch {
	case isSymlink:
		item.Kind = "symlink"
		if target, err := os.Readlink(canonicalPath); err == nil {
			item.LinkTarget = target
		}
		if resolvedTarget, tst, err := n.statSafe(canonicalPath); err == nil {
			if rootPath != "" && !isWithinCanonicalRoot(resolvedTarget, rootPath) {
				item.TargetKind = "other"
			} else if tst.IsDir() {
				item.TargetKind = "dir"
			} else if tst.Mode().IsRegular() {
				item.TargetKind = "file"
				item.Size = tst.Size()
			} else {
				item.TargetKind = "other"
			}
		} else if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errTooManyLinks) {
			item.LinkBroken = true
		}
	case info.IsDir():
		item.Kind = "dir"
	case info.Mode().IsRegular():
		item.Kind = "file"
	default:
		item.Kind = "other"
	}

	sensitive, reason := classifyMachineSensitivePath(displayPath, canonicalPath)
	if !sensitive && n.offLimitsResolved(canonicalPath) != nil {
		sensitive = true
		reason = "protected"
	}
	item.Sensitive = sensitive
	item.SensitiveReason = reason
	if item.Kind == "file" || (item.Symlink && item.TargetKind == "file") {
		item.Binary = isBinaryExtension(name)
	}
	return item
}

func sha256HexBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// machineFileWrite performs bounded UTF-8 text editing with strict mtime + SHA-256 CAS,
// sensitive-file confirmation enforcement, and atomic same-directory temp-file rename preserving mode.
func (n *Node) machineFileWrite(req proto.MachineFileWriteRequest) (*proto.MachineFileWriteResponse, error) {
	if req.ExpectedMtime.IsZero() || strings.TrimSpace(req.ExpectedSHA256) == "" {
		return nil, fmt.Errorf("expected_mtime and expected_sha256 are required for CAS save")
	}

	res, err := n.resolveMachinePath(req.Path, req.Root, machinePathOptions{})
	if err != nil {
		return nil, err
	}
	if res.Sensitive && !req.ConfirmSensitive {
		return nil, machineSensitiveConfirmError(res.SensitiveReason)
	}

	newBytes := []byte(req.Text)
	if err := validateMachineUTF8Text(res.DisplayPath, newBytes); err != nil {
		return nil, err
	}

	f, st, err := n.openSafeMachineRegularFile(res)
	if err != nil {
		return nil, err
	}
	if st.Size() > proto.MachineFileEditMaxBytes {
		_ = f.Close()
		return nil, machineTooLargeError(fmt.Sprintf("file exceeds maximum editable size (%d bytes)", proto.MachineFileEditMaxBytes))
	}
	curBytes, rerr := io.ReadAll(io.LimitReader(f, int64(proto.MachineFileEditMaxBytes)+1))
	_ = f.Close()
	if rerr != nil {
		return nil, rerr
	}
	if len(curBytes) > proto.MachineFileEditMaxBytes {
		return nil, machineTooLargeError(fmt.Sprintf("file exceeds maximum editable size (%d bytes)", proto.MachineFileEditMaxBytes))
	}
	if bin, _ := isBinaryPreviewContent(res.DisplayPath, curBytes, false, false); bin {
		return nil, machineBinaryError("binary files cannot be edited as text")
	}

	curSHA := sha256HexBytes(curBytes)
	curMtime := st.ModTime().UTC()
	if !curMtime.Equal(req.ExpectedMtime.UTC()) || !strings.EqualFold(curSHA, strings.TrimSpace(req.ExpectedSHA256)) {
		return nil, machineFileChangedError(st, curSHA)
	}
	if req.ExpectedRevision != "" && fileRevision(st) != req.ExpectedRevision {
		return nil, machineFileChangedError(st, curSHA)
	}

	dir := filepath.Dir(res.CanonicalPath)
	tmp, err := os.CreateTemp(dir, ".persona-mfedit-*")
	if err != nil {
		return nil, explainDenied(res.DisplayPath, n.home, err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(newBytes); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Chmod(st.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}

	if n.machineFileWriteBeforeVerify != nil {
		n.machineFileWriteBeforeVerify()
	}

	// Re-verify target resolves to the exact same canonical path and inode before atomic rename.
	rechecked, err := n.realPath(absPath(res.DisplayPath))
	if err != nil {
		if errors.Is(err, errOffLimits) || errors.Is(err, errICloud) || errors.Is(err, errTooManyLinks) {
			return nil, machineBoundaryError(err.Error())
		}
		return nil, explainDenied(res.DisplayPath, n.home, err)
	}
	rechecked = filepath.Clean(rechecked)
	if res.RootPath != "" && !isWithinCanonicalRoot(rechecked, res.RootPath) {
		return nil, machineBoundaryError("path escapes the allowed root boundary")
	}
	if rechecked != res.CanonicalPath {
		return nil, machineBoundaryError("target path changed during save")
	}
	now, err := os.Lstat(rechecked)
	if err != nil {
		return nil, explainDenied(res.DisplayPath, n.home, err)
	}
	if !now.Mode().IsRegular() || !sameFileRead(st, now) || !now.ModTime().UTC().Equal(curMtime) || now.Size() != st.Size() {
		return nil, machineFileChangedError(now, "")
	}

	if err := os.Rename(tmpPath, rechecked); err != nil {
		return nil, explainDenied(res.DisplayPath, n.home, err)
	}
	committed = true
	pastRevision(rechecked, st)

	finalSt, err := os.Stat(rechecked)
	if err != nil {
		return nil, err
	}
	return &proto.MachineFileWriteResponse{
		Path:            res.DisplayPath,
		CanonicalPath:   res.CanonicalPath,
		Name:            filepath.Base(res.DisplayPath),
		Size:            finalSt.Size(),
		Mode:            finalSt.Mode().String(),
		Perm:            uint32(finalSt.Mode().Perm()),
		ModTime:         finalSt.ModTime().UTC(),
		Revision:        fileRevision(finalSt),
		SHA256:          sha256HexBytes(newBytes),
		Sensitive:       res.Sensitive,
		SensitiveReason: res.SensitiveReason,
	}, nil
}

// machineFileCreate creates a new regular file or directory inside Dir, never overwriting an existing entry.
func (n *Node) machineFileCreate(req proto.MachineFileCreateRequest) (*proto.MachineFileCreateResponse, error) {
	kind := strings.TrimSpace(req.Kind)
	if kind != "file" && kind != "dir" {
		return nil, fmt.Errorf("kind must be 'file' or 'dir'")
	}
	name, err := validateMachineEntryName(req.Name)
	if err != nil {
		return nil, err
	}
	dirRes, err := n.resolveMachinePath(req.Dir, req.Root, machinePathOptions{})
	if err != nil {
		return nil, err
	}
	dirSt, err := os.Stat(dirRes.CanonicalPath)
	if err != nil {
		return nil, explainDenied(dirRes.DisplayPath, n.home, err)
	}
	if !dirSt.IsDir() {
		return nil, fmt.Errorf("parent path is not a directory")
	}

	targetDisplay := filepath.Join(dirRes.DisplayPath, name)
	targetRes, err := n.resolveMachinePath(targetDisplay, req.Root, machinePathOptions{AllowMissingLeaf: true})
	if err != nil {
		return nil, err
	}
	if targetRes.Sensitive && !req.ConfirmSensitive {
		return nil, machineSensitiveConfirmError(targetRes.SensitiveReason)
	}
	if _, err := os.Lstat(targetRes.CanonicalPath); err == nil {
		return nil, errExists(name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, explainDenied(targetRes.DisplayPath, n.home, err)
	}

	if kind == "dir" {
		if err := os.Mkdir(targetRes.CanonicalPath, 0o755); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return nil, errExists(name)
			}
			return nil, explainDenied(targetRes.DisplayPath, n.home, err)
		}
		st, err := os.Lstat(targetRes.CanonicalPath)
		if err != nil {
			return nil, err
		}
		return &proto.MachineFileCreateResponse{
			Entry:         n.buildMachineEntry(targetRes.DisplayPath, targetRes.CanonicalPath, targetRes.RootPath, st),
			CanonicalPath: targetRes.CanonicalPath,
		}, nil
	}

	data := []byte(req.Text)
	if err := validateMachineUTF8Text(targetRes.DisplayPath, data); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dirRes.CanonicalPath, ".persona-mfnew-*")
	if err != nil {
		return nil, explainDenied(targetRes.DisplayPath, n.home, err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if len(data) > 0 {
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			return nil, err
		}
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := renameExcl(tmpPath, targetRes.CanonicalPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errExists(name)
		}
		return nil, explainDenied(targetRes.DisplayPath, n.home, err)
	}
	committed = true

	st, err := os.Lstat(targetRes.CanonicalPath)
	if err != nil {
		return nil, err
	}
	return &proto.MachineFileCreateResponse{
		Entry:         n.buildMachineEntry(targetRes.DisplayPath, targetRes.CanonicalPath, targetRes.RootPath, st),
		CanonicalPath: targetRes.CanonicalPath,
		Revision:      fileRevision(st),
		SHA256:        sha256HexBytes(data),
	}, nil
}

// machineFileRename renames or moves an entry within allowed roots without silently overwriting an existing target.
func (n *Node) machineFileRename(req proto.MachineFileRenameRequest) (*proto.MachineFileRenameResponse, error) {
	srcRes, err := n.resolveMachineEntryNoFollow(req.Path, req.Root)
	if err != nil {
		return nil, err
	}
	srcInfo, err := os.Lstat(srcRes.CanonicalLeaf)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNotFound(srcRes.DisplayPath)
	}
	if err != nil {
		return nil, explainDenied(srcRes.DisplayPath, n.home, err)
	}

	destRes, err := n.resolveMachineEntryNoFollow(req.DestPath, req.Root)
	if err != nil {
		return nil, err
	}
	destParentSt, err := os.Stat(destRes.ParentCanonical)
	if err != nil {
		return nil, explainDenied(destRes.ParentDisplay, n.home, err)
	}
	if !destParentSt.IsDir() {
		return nil, fmt.Errorf("destination parent is not a directory")
	}

	if (srcRes.Sensitive || destRes.Sensitive) && !req.ConfirmSensitive {
		reason := srcRes.SensitiveReason
		if reason == "" {
			reason = destRes.SensitiveReason
		}
		return nil, machineSensitiveConfirmError(reason)
	}

	if srcRes.CanonicalLeaf == destRes.CanonicalLeaf {
		return &proto.MachineFileRenameResponse{
			OldPath:       srcRes.DisplayPath,
			Path:          destRes.DisplayPath,
			CanonicalPath: destRes.CanonicalLeaf,
			Name:          destRes.Name,
			Entry:         n.buildMachineEntry(destRes.DisplayPath, destRes.CanonicalLeaf, destRes.RootPath, srcInfo),
		}, nil
	}

	// Refuse moving a directory into itself or any of its descendants.
	if srcInfo.IsDir() && isWithinCanonicalRoot(destRes.CanonicalLeaf, srcRes.CanonicalLeaf) {
		return nil, machineBoundaryError("cannot move a directory into itself")
	}

	// Allow case-only rename of the exact same entry on case-insensitive filesystems,
	// without letting distinct hard links to the same inode bypass overwrite protection.
	caseOnlySameEntry := false
	if srcRes.ParentCanonical == destRes.ParentCanonical &&
		srcRes.Name != destRes.Name &&
		strings.EqualFold(srcRes.Name, destRes.Name) {
		if dstSt, err := os.Lstat(destRes.CanonicalLeaf); err == nil && os.SameFile(dstSt, srcInfo) {
			caseOnlySameEntry = true
		}
	}
	if caseOnlySameEntry {
		if err := os.Rename(srcRes.CanonicalLeaf, destRes.CanonicalLeaf); err != nil {
			return nil, explainRename(srcRes.DisplayPath, explainDenied(destRes.DisplayPath, n.home, err))
		}
	} else {
		if err := renameExcl(srcRes.CanonicalLeaf, destRes.CanonicalLeaf); err != nil {
			if errors.Is(err, os.ErrExist) {
				return nil, errExists(destRes.Name)
			}
			return nil, explainRename(srcRes.DisplayPath, explainDenied(destRes.DisplayPath, n.home, err))
		}
	}

	newInfo, err := os.Lstat(destRes.CanonicalLeaf)
	if err != nil {
		return nil, err
	}
	return &proto.MachineFileRenameResponse{
		OldPath:       srcRes.DisplayPath,
		Path:          destRes.DisplayPath,
		CanonicalPath: destRes.CanonicalLeaf,
		Name:          destRes.Name,
		Entry:         n.buildMachineEntry(destRes.DisplayPath, destRes.CanonicalLeaf, destRes.RootPath, newInfo),
	}, nil
}

// machineFileDelete deletes a single regular file, symlink (without following), or empty directory.
// Non-empty directories are refused (non-recursive safe-delete guardrail), and sensitive entries require ConfirmSensitive.
func (n *Node) machineFileDelete(req proto.MachineFileDeleteRequest) (*proto.MachineFileDeleteResponse, error) {
	res, err := n.resolveMachineEntryNoFollow(req.Path, req.Root)
	if err != nil {
		return nil, err
	}
	st, err := os.Lstat(res.CanonicalLeaf)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNotFound(res.DisplayPath)
	}
	if err != nil {
		return nil, explainDenied(res.DisplayPath, n.home, err)
	}

	if res.Sensitive && !req.ConfirmSensitive {
		return nil, machineSensitiveConfirmError(res.SensitiveReason)
	}

	kind := "file"
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		kind = "symlink"
	case st.IsDir():
		kind = "dir"
		df, derr := os.Open(res.CanonicalLeaf)
		if derr != nil {
			return nil, explainDenied(res.DisplayPath, n.home, derr)
		}
		names, rerr := df.Readdirnames(1)
		_ = df.Close()
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return nil, explainDenied(res.DisplayPath, n.home, rerr)
		}
		if len(names) > 0 {
			return nil, machineNotEmptyError()
		}
	}

	if err := os.Remove(res.CanonicalLeaf); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errNotFound(res.DisplayPath)
		}
		if kind == "dir" {
			if ents, rerr := os.ReadDir(res.CanonicalLeaf); rerr == nil && len(ents) > 0 {
				return nil, machineNotEmptyError()
			}
		}
		return nil, explainDenied(res.DisplayPath, n.home, err)
	}

	return &proto.MachineFileDeleteResponse{
		Path:          res.DisplayPath,
		CanonicalPath: res.CanonicalLeaf,
		Kind:          kind,
		Deleted:       true,
	}, nil
}

// --- Streaming file upload (bounded chunks, same-dir temp file, abort/idle/disconnect cleanup) ---

type machineFileUploads struct {
	mu   sync.Mutex
	open map[string]*machineFileUpload
}

type machineFileUpload struct {
	mu      sync.Mutex
	busy    bool
	retired bool
	closed  bool
	epoch   uint64
	idle    *time.Timer

	f             *os.File
	tmpPath       string
	dirDisplay    string
	dirCanonical  string
	destDisplay   string
	destCanonical string
	rootPath      string
	name          string
	expectedSize  int64 // -1 when unknown/chunked
	maxBytes      int64
	written       int64
	overwrite     bool
	hasher        hash.Hash
}

func (up *machineFileUpload) discard() {
	up.mu.Lock()
	if up.closed {
		up.mu.Unlock()
		return
	}
	up.closed = true
	f := up.f
	tmpPath := up.tmpPath
	up.mu.Unlock()
	if f != nil {
		_ = f.Close()
	}
	if tmpPath != "" {
		_ = os.Remove(tmpPath)
	}
}

func (up *machineFileUpload) stopIdleLocked() {
	up.epoch++
	if up.idle != nil {
		up.idle.Stop()
		up.idle = nil
	}
}

func (u *machineFileUploads) armLocked(id string, up *machineFileUpload) {
	up.stopIdleLocked()
	epoch := up.epoch
	up.idle = time.AfterFunc(machineFileUploadIdle, func() { u.expire(id, up, epoch) })
}

func (u *machineFileUploads) retireLocked(id string, up *machineFileUpload) {
	up.retired = true
	up.stopIdleLocked()
	u.mu.Lock()
	if u.open[id] == up {
		delete(u.open, id)
	}
	u.mu.Unlock()
}

func (u *machineFileUploads) lookup(id string) *machineFileUpload {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.open[id]
}

func (u *machineFileUploads) start(id string) (*machineFileUpload, error) {
	up := u.lookup(id)
	if up == nil {
		return nil, errUploadRetired
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.retired {
		return nil, errUploadRetired
	}
	if up.busy {
		return nil, fmt.Errorf("upload is busy")
	}
	up.busy = true
	up.stopIdleLocked()
	return up, nil
}

func (u *machineFileUploads) expire(id string, up *machineFileUpload, epoch uint64) {
	up.mu.Lock()
	if up.retired || up.epoch != epoch {
		up.mu.Unlock()
		return
	}
	u.retireLocked(id, up)
	cleanup := !up.busy
	up.mu.Unlock()
	if cleanup {
		up.discard()
	}
}

func (u *machineFileUploads) abort(id string) {
	up := u.lookup(id)
	if up == nil {
		return
	}
	up.mu.Lock()
	if up.retired {
		up.mu.Unlock()
		return
	}
	u.retireLocked(id, up)
	cleanup := !up.busy
	up.mu.Unlock()
	if cleanup {
		up.discard()
	}
}

func (u *machineFileUploads) abortAll() {
	u.mu.Lock()
	ids := make([]string, 0, len(u.open))
	for id := range u.open {
		ids = append(ids, id)
	}
	u.mu.Unlock()
	for _, id := range ids {
		u.abort(id)
	}
}

func (n *Node) machineUploadLimit() int64 {
	if n.machineFileUploadMaxBytes > 0 {
		return n.machineFileUploadMaxBytes
	}
	return proto.MachineFileUploadMaxBytes
}

func (n *Node) machineFileUploadBegin(req proto.MachineFileUploadBeginRequest) (*proto.MachineFileUploadBeginResponse, error) {
	name, err := validateMachineEntryName(req.Name)
	if err != nil {
		return nil, err
	}
	maxBytes := n.machineUploadLimit()
	if req.Size > maxBytes {
		return nil, machineTooLargeError(fmt.Sprintf("file exceeds maximum upload size (%d bytes)", maxBytes))
	}
	dirRes, err := n.resolveMachinePath(req.Dir, req.Root, machinePathOptions{})
	if err != nil {
		return nil, err
	}
	dirSt, err := os.Stat(dirRes.CanonicalPath)
	if err != nil {
		return nil, explainDenied(dirRes.DisplayPath, n.home, err)
	}
	if !dirSt.IsDir() {
		return nil, fmt.Errorf("upload target is not a directory")
	}

	destDisplay := filepath.Join(dirRes.DisplayPath, name)
	// Resolve the leaf inside dirRes.CanonicalPath without following a leaf symlink,
	// so symlink guards trigger on the link itself and the final rename stays in the temp file's directory.
	destLeaf := filepath.Clean(filepath.Join(dirRes.CanonicalPath, name))
	if dirRes.RootPath != "" && !isWithinCanonicalRoot(destLeaf, dirRes.RootPath) {
		return nil, machineBoundaryError("path escapes the allowed root boundary")
	}
	if err := n.offLimitsResolved(destLeaf); err != nil {
		return nil, machineBoundaryError(err.Error())
	}
	sensitive, sensitiveReason := classifyMachineSensitivePath(destDisplay, destLeaf)
	if sensitive && !req.ConfirmSensitive {
		return nil, machineSensitiveConfirmError(sensitiveReason)
	}

	if lst, err := os.Lstat(destLeaf); err == nil {
		if lst.Mode()&fs.ModeSymlink != 0 {
			return nil, machineBoundaryError("refusing to upload over a symlink")
		}
		if lst.IsDir() {
			return nil, errExists(name)
		}
		if !req.Overwrite {
			return nil, errExists(name)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, explainDenied(destDisplay, n.home, err)
	}

	tmp, err := os.CreateTemp(dirRes.CanonicalPath, ".persona-mfupload-*")
	if err != nil {
		return nil, explainDenied(destDisplay, n.home, err)
	}
	id := randHex(8)
	up := &machineFileUpload{
		f:             tmp,
		tmpPath:       tmp.Name(),
		dirDisplay:    dirRes.DisplayPath,
		dirCanonical:  dirRes.CanonicalPath,
		destDisplay:   destDisplay,
		destCanonical: destLeaf,
		rootPath:      dirRes.RootPath,
		name:          name,
		expectedSize:  req.Size,
		maxBytes:      maxBytes,
		overwrite:     req.Overwrite,
		hasher:        sha256.New(),
	}

	up.mu.Lock()
	n.mfUps.mu.Lock()
	if n.mfUps.open == nil {
		n.mfUps.open = map[string]*machineFileUpload{}
	}
	n.mfUps.open[id] = up
	n.mfUps.mu.Unlock()
	n.mfUps.armLocked(id, up)
	up.mu.Unlock()

	return &proto.MachineFileUploadBeginResponse{
		UploadID:   id,
		Dir:        dirRes.DisplayPath,
		Name:       name,
		MaxBytes:   maxBytes,
		ChunkBytes: proto.MachineFileUploadChunkBytes,
	}, nil
}

func (n *Node) machineFileUploadChunk(req proto.MachineFileUploadChunkRequest) (resp *proto.MachineFileUploadChunkResponse, err error) {
	if len(req.Data) > proto.MachineFileUploadChunkBytes {
		return nil, fmt.Errorf("upload chunk exceeds bound (%d bytes)", proto.MachineFileUploadChunkBytes)
	}
	up, err := n.mfUps.start(req.UploadID)
	if err != nil {
		return nil, err
	}
	defer func() {
		up.mu.Lock()
		up.busy = false
		if up.retired && err == nil {
			resp, err = nil, errUploadRetired
		}
		if err != nil {
			n.mfUps.retireLocked(req.UploadID, up)
		} else {
			n.mfUps.armLocked(req.UploadID, up)
		}
		cleanup := up.retired
		up.mu.Unlock()
		if cleanup {
			up.discard()
		}
	}()

	if req.Offset != up.written {
		return nil, fmt.Errorf("chunk offset %d does not match written %d", req.Offset, up.written)
	}
	nextWritten := up.written + int64(len(req.Data))
	if nextWritten > up.maxBytes {
		return nil, machineTooLargeError(fmt.Sprintf("upload exceeds maximum size (%d bytes)", up.maxBytes))
	}
	if up.expectedSize >= 0 && nextWritten > up.expectedSize {
		return nil, fmt.Errorf("upload exceeded announced size (%d bytes)", up.expectedSize)
	}
	if len(req.Data) > 0 {
		nw, werr := up.f.Write(req.Data)
		if werr != nil {
			return nil, werr
		}
		if nw != len(req.Data) {
			return nil, io.ErrShortWrite
		}
		_, _ = up.hasher.Write(req.Data)
		up.written += int64(nw)
	}
	return &proto.MachineFileUploadChunkResponse{
		UploadID: req.UploadID,
		Written:  up.written,
	}, nil
}

func (n *Node) machineFileUploadEnd(req proto.MachineFileUploadEndRequest) (*proto.MachineFileUploadEndResponse, error) {
	up, err := n.mfUps.start(req.UploadID)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		up.mu.Lock()
		n.mfUps.retireLocked(req.UploadID, up)
		up.busy = false
		up.mu.Unlock()
		if !committed {
			up.discard()
		}
	}()

	if up.expectedSize >= 0 && up.written != up.expectedSize {
		return nil, fmt.Errorf("incomplete upload: got %d of %d bytes", up.written, up.expectedSize)
	}
	if err := up.f.Sync(); err != nil {
		return nil, err
	}
	if err := up.f.Close(); err != nil {
		return nil, err
	}
	up.mu.Lock()
	up.f = nil
	up.mu.Unlock()

	// Re-verify parent directory canonically and non-followed leaf against TOCTOU symlink swaps before commit.
	dirRecheck, err := n.resolveMachinePath(up.dirDisplay, up.rootPath, machinePathOptions{})
	if err != nil {
		return nil, err
	}
	if dirRecheck.CanonicalPath != up.dirCanonical {
		return nil, machineBoundaryError("upload destination changed during transfer")
	}
	if up.rootPath != "" && !isWithinCanonicalRoot(up.destCanonical, up.rootPath) {
		return nil, machineBoundaryError("path escapes the allowed root boundary")
	}
	if err := n.offLimitsResolved(up.destCanonical); err != nil {
		return nil, machineBoundaryError(err.Error())
	}

	mode := os.FileMode(0o644)
	if lst, lerr := os.Lstat(up.destCanonical); lerr == nil {
		if lst.Mode()&fs.ModeSymlink != 0 {
			return nil, machineBoundaryError("refusing to upload over a symlink")
		}
		if lst.IsDir() {
			return nil, errExists(up.name)
		}
		if !up.overwrite {
			return nil, errExists(up.name)
		}
		if lst.Mode().IsRegular() {
			mode = lst.Mode().Perm()
		}
	} else if !errors.Is(lerr, fs.ErrNotExist) {
		return nil, explainDenied(up.destDisplay, n.home, lerr)
	}

	if err := os.Chmod(up.tmpPath, mode); err != nil {
		return nil, err
	}

	// Commit point: serialize against concurrent abort on this upload.
	up.mu.Lock()
	if up.retired {
		up.mu.Unlock()
		return nil, errUploadRetired
	}
	var renameErr error
	if up.overwrite {
		renameErr = os.Rename(up.tmpPath, up.destCanonical)
	} else {
		renameErr = renameExcl(up.tmpPath, up.destCanonical)
	}
	if renameErr == nil {
		up.closed = true
		committed = true
	}
	up.mu.Unlock()

	if renameErr != nil {
		if errors.Is(renameErr, os.ErrExist) {
			return nil, errExists(up.name)
		}
		return nil, explainDenied(up.destDisplay, n.home, renameErr)
	}

	st, err := os.Lstat(up.destCanonical)
	if err != nil {
		return nil, err
	}
	return &proto.MachineFileUploadEndResponse{
		Entry:         n.buildMachineEntry(up.destDisplay, up.destCanonical, up.rootPath, st),
		CanonicalPath: up.destCanonical,
		Size:          st.Size(),
		SHA256:        hex.EncodeToString(up.hasher.Sum(nil)),
	}, nil
}

func (n *Node) machineFileUploadAbort(req proto.MachineFileUploadAbortRequest) {
	if strings.TrimSpace(req.UploadID) == "" {
		return
	}
	n.mfUps.abort(req.UploadID)
}
