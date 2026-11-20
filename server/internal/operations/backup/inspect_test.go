package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/secretkey"
)

func TestInspectBackupWithoutAnInstallOrStaging(t *testing.T) {
	fixture := newBackupFixture(t, "private_manager_database")
	if _, err := fixture.operator().Create(context.Background(), fixture.options()); err != nil {
		fatal(t, err)
	}
	manifest, size, err := readBundleManifest(fixture.output)
	if err != nil {
		fatal(t, err)
	}
	t.Setenv("PATH", "")
	t.Setenv("TMPDIR", filepath.Join(fixture.home, "unavailable-staging"))
	t.Setenv("PERSONA_DATABASE_URL", "postgres://should-not-connect.example.test")
	t.Setenv("PERSONA_LANG", "en")
	var stdout, stderr bytes.Buffer
	args := []string{"inspect", "--bundle", fixture.output, "--json"}
	if err := Run(context.Background(), args, &stdout, &stderr); err != nil {
		fatal(t, err)
	}
	var report inspectionReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		fatal(t, err)
	}
	if report.Check != "offline_integrity" || report.DatabaseRestoreVerified || report.Format != manifest.Format ||
		!report.CreatedAt.Equal(manifest.CreatedAt) || report.ArchiveBytes != size ||
		report.HubDump.Files != 1 || report.HubDump.Bytes <= 0 || report.ManagerDump.Files != 1 ||
		report.ManagerDump.Bytes <= 0 || report.Runtime.Files < 4 ||
		report.KnowledgeBase.State != "included" || report.KnowledgeBase.Location != "internal" ||
		report.KnowledgeBase.Files == 0 {
		fatalf(t, "inspection report %+v", report)
	}
	for _, omitted := range []string{fixture.output, fixture.home, manifest.HubDatabase, manifest.ManagerDatabase,
		manifest.HubKeyFingerprint, manifest.ManagerKeyFingerprint, "hub-token-secret", "secret.key", "should-not-connect"} {
		if strings.Contains(stdout.String()+stderr.String(), omitted) {
			fatal(t, "the inspection disclosed a private manifest or config value")
		}
	}
	stdout.Reset()
	if err := Run(context.Background(), args[:3], &stdout, &stderr); err != nil {
		fatal(t, err)
	}
	if !strings.Contains(stdout.String(), "Offline backup integrity check passed.") ||
		!strings.Contains(stdout.String(), "personad backup verify") {
		fatalf(t, "text report %q", stdout.String())
	}
}

func TestInspectBackupKnowledgeStates(t *testing.T) {
	for _, tc := range []struct {
		name, state, location string
		locator               KnowledgeBaseLocator
		payload               bool
	}{
		{"absent", "absent", "internal", KnowledgeBaseLocator{Relative: "private-folder"}, false},
		{"empty", "empty", "internal", KnowledgeBaseLocator{Relative: "private-folder", Present: true}, false},
		{"external", "included", "external", KnowledgeBaseLocator{External: filepath.Join(t.TempDir(), "private-folder"), Present: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := inspectionFixture(t, tc.locator, tc.payload)
			report, err := inspectArchive(context.Background(), bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				fatal(t, err)
			}
			kb := report.KnowledgeBase
			wantFiles := 0
			if tc.payload {
				wantFiles = 1
			}
			if kb.State != tc.state || kb.Location != tc.location || kb.Files != wantFiles || (kb.Bytes > 0) != tc.payload {
				fatalf(t, "knowledge base %+v", kb)
			}
		})
	}
}

func TestInspectRejectsBadMembersAndKeys(t *testing.T) {
	raw := inspectionFixture(t, KnowledgeBaseLocator{}, false)
	members := inspectionMembers(t, raw)
	for _, tc := range []struct {
		name   string
		kind   bundleProblemKind
		change func(*testing.T, []inspectionPart) []inspectionPart
	}{
		{"unexpected file", bundleLayout, func(_ *testing.T, parts []inspectionPart) []inspectionPart {
			parts[1].header.Name = "private-member"
			return parts
		}},
		{"duplicate file", bundleLayout, func(_ *testing.T, parts []inspectionPart) []inspectionPart {
			return append(parts, parts[1])
		}},
		{"duplicate manifest", bundleLayout, func(_ *testing.T, parts []inspectionPart) []inspectionPart {
			return append(parts, parts[0])
		}},
		{"missing file", bundleIncomplete, func(_ *testing.T, parts []inspectionPart) []inspectionPart {
			return parts[:len(parts)-1]
		}},
		{"invalid mode", bundleLayout, func(_ *testing.T, parts []inspectionPart) []inspectionPart {
			parts[1].header.Mode = 0o640
			return parts
		}},
		{"non-regular file", bundleLayout, func(_ *testing.T, parts []inspectionPart) []inspectionPart {
			parts[1].header.Typeflag = tar.TypeSymlink
			parts[1].header.Linkname = "private-reference"
			parts[1].data = nil
			return parts
		}},
		{"checksum", bundleDigest, func(_ *testing.T, parts []inspectionPart) []inspectionPart {
			parts[1].data[0] ^= 1
			return parts
		}},
		{"inconsistent key", bundleKey, func(t *testing.T, parts []inspectionPart) []inspectionPart {
			var manifest Manifest
			if err := json.Unmarshal(parts[0].data, &manifest); err != nil {
				fatal(t, err)
			}
			manifest.HubKeyFingerprint = strings.Repeat("0", 32)
			parts[0].data = encodeInspectionManifest(t, manifest)
			return parts
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parts := cloneInspectionParts(members)
			changed := packInspectionParts(t, tc.change(t, parts))
			_, err := inspectArchive(context.Background(), bytes.NewReader(changed), int64(len(changed)))
			requireBundleProblem(t, err, tc.kind)
		})
	}
}

func TestInspectRequiresACompleteArchiveEnd(t *testing.T) {
	raw := inspectionFixture(t, KnowledgeBaseLocator{}, false)
	for _, removed := range []int{1, tarBlock, 2 * tarBlock} {
		truncated := raw[:len(raw)-removed]
		_, err := inspectArchive(context.Background(), bytes.NewReader(truncated), int64(len(truncated)))
		requireBundleProblem(t, err, bundleIncomplete)
	}
	padded := append(bytes.Clone(raw), make([]byte, 4*tarBlock)...)
	if _, err := inspectArchive(context.Background(), bytes.NewReader(padded), int64(len(padded))); err != nil {
		fatalf(t, "standard tar padding: %v", err)
	}
	padded[len(padded)-1] = 1
	_, err := inspectArchive(context.Background(), bytes.NewReader(padded), int64(len(padded)))
	requireBundleProblem(t, err, bundleLayout)
}

func TestInspectReadsAnOlderManifestPosition(t *testing.T) {
	parts := inspectionMembers(t, inspectionFixture(t, KnowledgeBaseLocator{}, false))
	parts[0], parts[1] = parts[1], parts[0]
	raw := packInspectionParts(t, parts)
	if _, err := inspectArchive(context.Background(), bytes.NewReader(raw), int64(len(raw))); err != nil {
		fatal(t, err)
	}
}

func TestInspectRejectsOverlappingFileAndDirectory(t *testing.T) {
	parts := inspectionMembers(t, inspectionFixture(t, KnowledgeBaseLocator{}, false))
	var manifest Manifest
	if err := json.Unmarshal(parts[0].data, &manifest); err != nil {
		fatal(t, err)
	}
	fixture := FileEntry{Size: 0, Mode: 0o600}
	digest := sha256.Sum256(nil)
	fixture.SHA256 = hex.EncodeToString(digest[:])
	for _, path := range []string{"runtime/skills/item", "runtime/skills/item-strip", "runtime/skills/item/nested"} {
		fixture.Path = path
		manifest.Files = append(manifest.Files, fixture)
		parts = append(parts, inspectionPart{header: tar.Header{Name: path, Mode: 0o600}})
	}
	parts[0].data = encodeInspectionManifest(t, manifest)
	raw := packInspectionParts(t, parts)
	_, err := inspectArchive(context.Background(), bytes.NewReader(raw), int64(len(raw)))
	requireBundleProblem(t, err, bundleManifest)
}

func TestInspectStreamsTheDatabasePayload(t *testing.T) {
	parts := inspectionMembers(t, inspectionFixture(t, KnowledgeBaseLocator{}, false))
	var manifest Manifest
	if err := json.Unmarshal(parts[0].data, &manifest); err != nil {
		fatal(t, err)
	}
	parts[1].data = bytes.Repeat([]byte("d"), 2<<20)
	digest := sha256.Sum256(parts[1].data)
	manifest.Files[0].Size = int64(len(parts[1].data))
	manifest.Files[0].SHA256 = hex.EncodeToString(digest[:])
	parts[0].data = encodeInspectionManifest(t, manifest)
	raw := packInspectionParts(t, parts)
	source := &trackingInspectionReader{Reader: bytes.NewReader(raw)}
	report, err := inspectArchive(context.Background(), source, int64(len(raw)))
	if err != nil || report.HubDump.Bytes != manifest.Files[0].Size || source.largest > 64<<10 {
		fatalf(t, "streamed report %+v, largest read %d bytes, error %v", report, source.largest, err)
	}
}

type trackingInspectionReader struct {
	*bytes.Reader
	largest int
}

func (r *trackingInspectionReader) Read(p []byte) (int, error) {
	r.largest = max(r.largest, len(p))
	return r.Reader.Read(p)
}

func TestInspectCancellationReachesArchiveReads(t *testing.T) {
	data := inspectionFixture(t, KnowledgeBaseLocator{}, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &cancelInspectionReader{Reader: bytes.NewReader(data), cancel: cancel, beforeCancel: int64(len(data) - tarBlock)}
	_, err := inspectArchive(ctx, source, int64(len(data)))
	if !errors.Is(err, context.Canceled) || !source.rewound {
		fatalf(t, "inspection did not stop during the payload pass: %v", err)
	}
}

type cancelInspectionReader struct {
	*bytes.Reader
	cancel       context.CancelFunc
	beforeCancel int64
	rewound      bool
}

func (r *cancelInspectionReader) Seek(offset int64, whence int) (int64, error) {
	if offset == 0 && whence == io.SeekStart {
		r.rewound = true
	}
	return r.Reader.Seek(offset, whence)
}

func (r *cancelInspectionReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if r.rewound {
		r.beforeCancel -= int64(n)
		if r.beforeCancel <= 0 {
			r.cancel()
		}
	}
	return n, err
}

func TestInspectCLIHandlesPrivateErrorsAndHelp(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"inspect", "--help"}, &stdout, &stderr); err != nil ||
		!strings.Contains(stdout.String(), "without opening a database") {
		fatalf(t, "help %q: %v", stdout.String(), err)
	}
	var usage clitext.UsageError
	if err := Run(context.Background(), []string{"inspect"}, &stdout, &stderr); !errors.As(err, &usage) {
		fatalf(t, "missing bundle: %v", err)
	}

	parts := inspectionMembers(t, inspectionFixture(t, KnowledgeBaseLocator{}, false))
	parts[1].header.Name = "private-member\n"
	name := filepath.Join(t.TempDir(), "private-backup-filename")
	if err := os.WriteFile(name, packInspectionParts(t, parts), 0o600); err != nil {
		fatal(t, err)
	}
	stdout.Reset()
	err := Run(context.Background(), []string{"inspect", "--bundle", name, "--json"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unexpected") || strings.Contains(err.Error(), "private-member") ||
		strings.Contains(err.Error(), name) || stdout.Len() != 0 || stderr.Len() != 0 {
		fatalf(t, "error surfaced private data or partial output: %v %q %q", err, stdout.String(), stderr.String())
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(filepath.Dir(name), "backup-alias")
		if err := os.Symlink(name, link); err != nil {
			fatal(t, err)
		}
		_, err := inspectBundle(context.Background(), link)
		requireBundleProblem(t, err, bundleUnsafe)

		if err := os.Chmod(name, 0o644); err != nil {
			fatal(t, err)
		}
		_, err = inspectBundle(context.Background(), name)
		requireBundleProblem(t, err, bundleUnsafe)
	}
}

type inspectionPart struct {
	header tar.Header
	data   []byte
}

func inspectionFixture(t *testing.T, kb KnowledgeBaseLocator, withKnowledge bool) []byte {
	t.Helper()
	hubKey, managerKey := bytes.Repeat([]byte("h"), 32), bytes.Repeat([]byte("m"), 32)
	files := []struct {
		name string
		data []byte
	}{
		{hubDumpPath, []byte("synthetic hub dump")}, {managerDumpPath, []byte("synthetic manager dump")},
		{hubKeyPath, hubKey}, {managerKeyPath, managerKey},
		{"runtime/hub.json", []byte(`{"token":"fixture-private-value"}`)},
		{"runtime/managers/assistant/manager.json", []byte(`{}`)},
	}
	if withKnowledge {
		files = append(files, struct {
			name string
			data []byte
		}{"kb/private-filename.md", []byte("fixture-private-note")})
	}
	manifest := Manifest{
		Format: formatVersion, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), KnowledgeBase: kb,
		HubDatabase: "private_hub_database", ManagerDatabase: "private_manager_database",
		Hub:               DatabaseIdentity{Schema: []Migration{{Version: 1, Name: "setup", Part: "hub"}}},
		Manager:           DatabaseIdentity{Schema: []Migration{{Version: 1, Name: "setup", Part: "manager"}}},
		HubKeyFingerprint: secretkey.Fingerprint(hubKey), ManagerKeyFingerprint: secretkey.Fingerprint(managerKey),
	}
	parts := []inspectionPart{{header: tar.Header{Name: manifestPath, Mode: 0o600}}}
	for _, file := range files {
		hash := sha256.Sum256(file.data)
		manifest.Files = append(manifest.Files, FileEntry{Path: file.name, Mode: 0o600, Size: int64(len(file.data)), SHA256: hex.EncodeToString(hash[:])})
		parts = append(parts, inspectionPart{header: tar.Header{Name: file.name, Mode: 0o600}, data: file.data})
	}
	parts[0].data = encodeInspectionManifest(t, manifest)
	return packInspectionParts(t, parts)
}

func encodeInspectionManifest(t *testing.T, manifest Manifest) []byte {
	t.Helper()
	result, err := json.Marshal(manifest)
	if err != nil {
		fatal(t, err)
	}
	return result
}

func inspectionMembers(t *testing.T, raw []byte) []inspectionPart {
	t.Helper()
	var parts []inspectionPart
	r := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return parts
		}
		if err != nil {
			fatal(t, err)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			fatal(t, err)
		}
		parts = append(parts, inspectionPart{header: *h, data: data})
	}
}

func cloneInspectionParts(parts []inspectionPart) []inspectionPart {
	out := make([]inspectionPart, len(parts))
	for i, part := range parts {
		out[i] = inspectionPart{header: part.header, data: bytes.Clone(part.data)}
	}
	return out
}

func packInspectionParts(t *testing.T, parts []inspectionPart) []byte {
	t.Helper()
	var raw bytes.Buffer
	w := tar.NewWriter(&raw)
	for _, part := range parts {
		h := part.header
		h.Size = int64(len(part.data))
		if err := w.WriteHeader(&h); err != nil {
			fatal(t, err)
		}
		if _, err := w.Write(part.data); err != nil {
			fatal(t, err)
		}
	}
	if err := w.Close(); err != nil {
		fatal(t, err)
	}
	return raw.Bytes()
}

func requireBundleProblem(t *testing.T, err error, kind bundleProblemKind) {
	t.Helper()
	var issue *bundleProblem
	if !errors.As(err, &issue) || issue.kind != kind {
		fatalf(t, "error %v, want %s failure", err, kind)
	}
}
