package proto

import "time"

// Machine file browser RPC methods and bounds (Phase 6A read-only + Phase 6B human-facing write/upload).
const (
	MethodMachineFilesRoots       = "machine.files.roots"
	MethodMachineFilesList        = "machine.files.list"
	MethodMachineFilesPreview     = "machine.files.preview"
	MethodMachineFilesTail        = "machine.files.tail"
	MethodMachineFilesChunk       = "machine.files.chunk"
	MethodMachineFilesWrite       = "machine.files.write"
	MethodMachineFilesCreate      = "machine.files.create"
	MethodMachineFilesRename      = "machine.files.rename"
	MethodMachineFilesDelete      = "machine.files.delete"
	MethodMachineFilesUploadBegin = "machine.files.upload.begin"
	MethodMachineFilesUploadChunk = "machine.files.upload.chunk"
	MethodMachineFilesUploadEnd   = "machine.files.upload.end"
	MethodMachineFilesUploadAbort = "machine.files.upload.abort"

	MachineFileListDefaultLimit    = 2000
	MachineFileListMaxLimit        = 2000
	MachineFilePreviewDefaultBytes = 256 << 10 // 256 KiB default bounded text preview
	MachineFilePreviewMaxBytes     = 2 << 20   // 2 MiB hard preview cap
	MachineFileEditMaxBytes        = 1 << 20   // 1 MiB hard UTF-8 text edit cap
	MachineFileTailDefaultLines    = 200
	MachineFileTailMaxLines        = 5000
	MachineFileTailDefaultBytes    = 128 << 10 // 128 KiB default log tail window
	MachineFileTailMaxBytes        = 2 << 20   // 2 MiB hard tail byte cap
	MachineFileDownloadChunkBytes  = FileDownloadChunkBytes
	MachineFileUploadChunkBytes    = 256 << 10 // 256 KiB bounded streaming upload chunk
	MachineFileUploadMaxBytes      = 100 << 20 // 100 MiB hard streaming upload cap

	// CodeMachineFileBoundary indicates a path traversal, symlink escape, or off-limits secret boundary refusal.
	CodeMachineFileBoundary = "machine_file_boundary"
	// CodeMachineFileBinary indicates a binary file where raw text preview/tail/edit is refused.
	CodeMachineFileBinary = "machine_file_binary"
	// CodeMachineFileSensitiveConfirm indicates a sensitive target that requires explicit operator confirmation.
	CodeMachineFileSensitiveConfirm = "machine_file_sensitive_confirm"
	// CodeMachineFileNotEmpty indicates a non-empty directory refused by the non-recursive safe-delete guardrail.
	CodeMachineFileNotEmpty = "machine_file_not_empty"
)

// MachineFileRoot describes a quick root shortcut surfaced in the Web file browser.
type MachineFileRoot struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Path   string `json:"path"`
	Kind   string `json:"kind"` // home, workspace, config
	Exists bool   `json:"exists"`
}

// MachineFileRootsResponse lists quick root shortcuts for a machine.
type MachineFileRootsResponse struct {
	Home  string            `json:"home"`
	OS    string            `json:"os"`
	Roots []MachineFileRoot `json:"roots"`
}

// MachineFileListRequest requests a strictly single-level directory listing.
type MachineFileListRequest struct {
	Path  string `json:"path"`
	Root  string `json:"root,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// MachineFileEntry describes one entry in a single-level directory listing.
type MachineFileEntry struct {
	Name            string    `json:"name"`
	Path            string    `json:"path"`
	Kind            string    `json:"kind"` // dir, file, symlink, other
	Size            int64     `json:"size"`
	Mode            string    `json:"mode"`
	Perm            uint32    `json:"perm"`
	ModTime         time.Time `json:"mtime"`
	Symlink         bool      `json:"symlink,omitempty"`
	LinkTarget      string    `json:"link_target,omitempty"`
	LinkBroken      bool      `json:"link_broken,omitempty"`
	TargetKind      string    `json:"target_kind,omitempty"` // dir, file, other when symlink resolves safely
	Sensitive       bool      `json:"sensitive,omitempty"`
	SensitiveReason string    `json:"sensitive_reason,omitempty"`
	Binary          bool      `json:"binary,omitempty"`
}

// MachineFileListResponse returns a single-level directory listing with canonical path metadata.
type MachineFileListResponse struct {
	Path          string             `json:"path"`
	CanonicalPath string             `json:"canonical_path"`
	Parent        string             `json:"parent,omitempty"`
	Entries       []MachineFileEntry `json:"entries"`
	Total         int                `json:"total"`
	Truncated     bool               `json:"truncated,omitempty"`
}

// MachineFilePreviewRequest requests a bounded text preview of a single regular file.
type MachineFilePreviewRequest struct {
	Path     string `json:"path"`
	Root     string `json:"root,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

// MachineFilePreviewResponse returns bounded UTF-8 text or binary refusal metadata.
type MachineFilePreviewResponse struct {
	Path            string    `json:"path"`
	CanonicalPath   string    `json:"canonical_path"`
	Name            string    `json:"name"`
	Size            int64     `json:"size"`
	Mode            string    `json:"mode"`
	Perm            uint32    `json:"perm"`
	ModTime         time.Time `json:"mtime"`
	Mime            string    `json:"mime"`
	Revision        string    `json:"revision"`
	SHA256          string    `json:"sha256,omitempty"`
	Editable        bool      `json:"editable,omitempty"`
	EditMaxBytes    int       `json:"edit_max_bytes,omitempty"`
	Symlink         bool      `json:"symlink,omitempty"`
	LinkTarget      string    `json:"link_target,omitempty"`
	Sensitive       bool      `json:"sensitive,omitempty"`
	SensitiveReason string    `json:"sensitive_reason,omitempty"`
	Binary          bool      `json:"binary,omitempty"`
	Text            string    `json:"text,omitempty"`
	BytesRead       int64     `json:"bytes_read"`
	Truncated       bool      `json:"truncated,omitempty"`
	MaxBytes        int       `json:"max_bytes"`
}

// MachineFileTailRequest requests the last N lines (bounded by MaxBytes) of a text/log file.
type MachineFileTailRequest struct {
	Path     string `json:"path"`
	Root     string `json:"root,omitempty"`
	Lines    int    `json:"lines,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

// MachineFileTailResponse returns the bounded trailing lines of a file.
type MachineFileTailResponse struct {
	Path            string    `json:"path"`
	CanonicalPath   string    `json:"canonical_path"`
	Name            string    `json:"name"`
	Size            int64     `json:"size"`
	ModTime         time.Time `json:"mtime"`
	Mime            string    `json:"mime"`
	Revision        string    `json:"revision"`
	Sensitive       bool      `json:"sensitive,omitempty"`
	SensitiveReason string    `json:"sensitive_reason,omitempty"`
	Binary          bool      `json:"binary,omitempty"`
	Text            string    `json:"text,omitempty"`
	Lines           int       `json:"lines"`
	RequestedLines  int       `json:"requested_lines"`
	BytesRead       int64     `json:"bytes_read"`
	Offset          int64     `json:"offset"`
	Truncated       bool      `json:"truncated,omitempty"`
}

// MachineFileChunkRequest requests a bounded byte chunk for streaming single-file downloads.
type MachineFileChunkRequest struct {
	Path     string `json:"path"`
	Root     string `json:"root,omitempty"`
	Offset   int64  `json:"offset"`
	Length   int    `json:"length"`
	Revision string `json:"revision,omitempty"`
}

// MachineFileChunkResponse returns file metadata and (when Length > 0) one chunk of bytes.
type MachineFileChunkResponse struct {
	Path          string    `json:"path"`
	CanonicalPath string    `json:"canonical_path"`
	Name          string    `json:"name"`
	Size          int64     `json:"size"`
	Mime          string    `json:"mime"`
	ModTime       time.Time `json:"mtime"`
	Revision      string    `json:"revision"`
	Offset        int64     `json:"offset"`
	Data          []byte    `json:"data,omitempty"`
}

// MachineFileWriteRequest atomically saves an edited UTF-8 text file with strict mtime + SHA-256 CAS.
type MachineFileWriteRequest struct {
	Path             string    `json:"path"`
	Root             string    `json:"root,omitempty"`
	Text             string    `json:"text"`
	ExpectedMtime    time.Time `json:"expected_mtime"`
	ExpectedSHA256   string    `json:"expected_sha256"`
	ExpectedRevision string    `json:"expected_revision,omitempty"`
	ConfirmSensitive bool      `json:"confirm_sensitive,omitempty"`
}

// MachineFileWriteResponse returns updated metadata and CAS tokens after an atomic save.
type MachineFileWriteResponse struct {
	Path            string    `json:"path"`
	CanonicalPath   string    `json:"canonical_path"`
	Name            string    `json:"name"`
	Size            int64     `json:"size"`
	Mode            string    `json:"mode"`
	Perm            uint32    `json:"perm"`
	ModTime         time.Time `json:"mtime"`
	Revision        string    `json:"revision"`
	SHA256          string    `json:"sha256"`
	Sensitive       bool      `json:"sensitive,omitempty"`
	SensitiveReason string    `json:"sensitive_reason,omitempty"`
}

// MachineFileCreateRequest creates a new regular file ("file") or directory ("dir") inside Dir.
type MachineFileCreateRequest struct {
	Dir              string `json:"dir"`
	Name             string `json:"name"`
	Kind             string `json:"kind"` // "file" or "dir"
	Root             string `json:"root,omitempty"`
	Text             string `json:"text,omitempty"`
	ConfirmSensitive bool   `json:"confirm_sensitive,omitempty"`
}

// MachineFileCreateResponse returns the created entry metadata.
type MachineFileCreateResponse struct {
	Entry         MachineFileEntry `json:"entry"`
	CanonicalPath string           `json:"canonical_path"`
	Revision      string           `json:"revision,omitempty"`
	SHA256        string           `json:"sha256,omitempty"`
}

// MachineFileRenameRequest renames or moves an entry within allowed roots without overwriting existing targets.
type MachineFileRenameRequest struct {
	Path             string `json:"path"`
	DestPath         string `json:"dest_path"`
	Root             string `json:"root,omitempty"`
	ConfirmSensitive bool   `json:"confirm_sensitive,omitempty"`
}

// MachineFileRenameResponse returns the updated entry metadata after rename/move.
type MachineFileRenameResponse struct {
	OldPath       string           `json:"old_path"`
	Path          string           `json:"path"`
	CanonicalPath string           `json:"canonical_path"`
	Name          string           `json:"name"`
	Entry         MachineFileEntry `json:"entry"`
}

// MachineFileDeleteRequest deletes a single file, symlink (without following), or empty directory.
type MachineFileDeleteRequest struct {
	Path             string `json:"path"`
	Root             string `json:"root,omitempty"`
	ConfirmSensitive bool   `json:"confirm_sensitive,omitempty"`
}

// MachineFileDeleteResponse confirms deletion of a single entry.
type MachineFileDeleteResponse struct {
	Path          string `json:"path"`
	CanonicalPath string `json:"canonical_path"`
	Kind          string `json:"kind"`
	Deleted       bool   `json:"deleted"`
}

// MachineFileUploadBeginRequest opens a bounded streaming upload into a directory temp file.
type MachineFileUploadBeginRequest struct {
	Dir              string `json:"dir"`
	Name             string `json:"name"`
	Root             string `json:"root,omitempty"`
	Size             int64  `json:"size"` // >= 0 when Content-Length is known, -1 when streamed chunked
	Overwrite        bool   `json:"overwrite,omitempty"`
	ConfirmSensitive bool   `json:"confirm_sensitive,omitempty"`
}

// MachineFileUploadBeginResponse identifies the open streaming upload session and its bounds.
type MachineFileUploadBeginResponse struct {
	UploadID   string `json:"upload_id"`
	Dir        string `json:"dir"`
	Name       string `json:"name"`
	MaxBytes   int64  `json:"max_bytes"`
	ChunkBytes int    `json:"chunk_bytes"`
}

// MachineFileUploadChunkRequest appends one sequential byte chunk to an open streaming upload.
type MachineFileUploadChunkRequest struct {
	UploadID string `json:"upload_id"`
	Offset   int64  `json:"offset"`
	Data     []byte `json:"data"`
}

// MachineFileUploadChunkResponse acknowledges the cumulative bytes written.
type MachineFileUploadChunkResponse struct {
	UploadID string `json:"upload_id"`
	Written  int64  `json:"written"`
}

// MachineFileUploadEndRequest finalizes and atomically commits a streaming upload.
type MachineFileUploadEndRequest struct {
	UploadID string `json:"upload_id"`
}

// MachineFileUploadEndResponse returns the committed file metadata and SHA-256 checksum.
type MachineFileUploadEndResponse struct {
	Entry         MachineFileEntry `json:"entry"`
	CanonicalPath string           `json:"canonical_path"`
	Size          int64            `json:"size"`
	SHA256        string           `json:"sha256"`
}

// MachineFileUploadAbortRequest cancels an in-flight upload and removes its partial temp file.
type MachineFileUploadAbortRequest struct {
	UploadID string `json:"upload_id"`
}
