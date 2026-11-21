package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/nwverse/persona/server/internal/secretkey"
)

const maxManifestSize = 8 << 20

func validMemberName(name string) bool {
	return name != "" && name != "." && !strings.Contains(name, "\\") && !strings.HasPrefix(name, "/") && path.Clean(name) == name &&
		name != ".." && !strings.HasPrefix(name, "../")
}

func validFingerprint(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validPayloadPath(name string) bool {
	if name == hubDumpPath || name == managerDumpPath || strings.HasPrefix(name, "kb/") {
		return true
	}
	if !strings.HasPrefix(name, "runtime/") || strings.HasPrefix(name, "runtime/managers/m-") {
		return false
	}
	rel := strings.TrimPrefix(name, "runtime/")
	for _, prefix := range []string{"managers/assistant/", "extensions/", "skills/", "hub.machines_kept/", "tls/"} {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	if rel == "docker/db-password" {
		return true
	}
	if !strings.Contains(rel, "/") {
		if rel == "hub.json" || rel == "secret.key" || strings.HasPrefix(rel, "secret.key.next-") {
			return true
		}
		for _, optional := range optionalRootFiles {
			if rel == optional {
				return true
			}
		}
	}
	return false
}

func (m *Manifest) validate() error {
	if m.Format != formatVersion {
		return fmt.Errorf("unsupported backup format %d", m.Format)
	}
	if m.HubDatabase == "" || m.ManagerDatabase == "" {
		return errors.New("the manifest does not name both databases")
	}
	if len(m.Hub.Schema) == 0 || len(m.Manager.Schema) == 0 {
		return errors.New("the manifest has no database schema identity")
	}
	if !validFingerprint(m.HubKeyFingerprint) || !validFingerprint(m.ManagerKeyFingerprint) {
		return errors.New("the manifest has an invalid encryption-key fingerprint")
	}
	if m.Manager.SealingKey != "" && m.Manager.SealingKey != m.ManagerKeyFingerprint {
		return errors.New("the built-in manager database and current secret.key do not match")
	}
	if m.KnowledgeBase.Relative != "" && m.KnowledgeBase.External != "" {
		return errors.New("the knowledge-base location is ambiguous")
	}
	if m.KnowledgeBase.Relative != "" && !validMemberName(m.KnowledgeBase.Relative) {
		return errors.New("the knowledge-base relative path is invalid")
	}
	if m.KnowledgeBase.External != "" && (!filepath.IsAbs(m.KnowledgeBase.External) || filepath.Clean(m.KnowledgeBase.External) == string(filepath.Separator)) {
		return errors.New("the knowledge-base external path is invalid")
	}
	seen := make(map[string]bool, len(m.Files))
	for _, entry := range m.Files {
		if !m.KnowledgeBase.Present && strings.HasPrefix(entry.Path, "kb/") {
			return fmt.Errorf("the manifest records no knowledge base but lists the knowledge-base file %q", entry.Path)
		}
		if !validMemberName(entry.Path) || entry.Path == manifestPath || seen[entry.Path] || !validPayloadPath(entry.Path) {
			return fmt.Errorf("invalid or duplicate payload path %q", entry.Path)
		}
		seen[entry.Path] = true
		if entry.Size < 0 || !validSHA256(entry.SHA256) || (entry.Mode != 0o600 && entry.Mode != 0o700) {
			return fmt.Errorf("invalid payload identity for %s", entry.Path)
		}
	}
	if name := overlappingPayload(seen); name != "" {
		return fmt.Errorf("payload path %q is both a file and a parent directory", name)
	}
	for _, required := range []string{hubDumpPath, managerDumpPath, "runtime/hub.json", hubKeyPath, managerKeyPath, "runtime/managers/assistant/manager.json"} {
		if !seen[required] {
			return fmt.Errorf("the bundle lacks required member %s", required)
		}
		for _, entry := range m.Files {
			if entry.Path == required && entry.Mode != 0o600 {
				return fmt.Errorf("required member %s is not mode 0600", required)
			}
		}
	}
	for _, directory := range m.RuntimeDirectories {
		if !validMemberName(directory) || strings.HasPrefix(directory, "managers/m-") {
			return fmt.Errorf("invalid runtime directory %q", directory)
		}
	}
	return nil
}

func overlappingPayload(files map[string]bool) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		prefix := name + "/"
		if i := sort.SearchStrings(names, prefix); i < len(names) && strings.HasPrefix(names[i], prefix) {
			return name
		}
	}
	return ""
}

func manifestFiles(staging string) ([]FileEntry, error) {
	var files []FileEntry
	err := filepath.WalkDir(staging, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("backup staging contains a non-regular file: %s", name)
		}
		rel, err := filepath.Rel(staging, name)
		if err != nil {
			return err
		}
		member := filepath.ToSlash(rel)
		if member == manifestPath {
			return nil
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		files = append(files, FileEntry{Path: member, Size: info.Size(), SHA256: hex.EncodeToString(hash.Sum(nil)), Mode: uint32(info.Mode().Perm())})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

func writeManifest(staging string, manifest *Manifest) error {
	files, err := manifestFiles(staging)
	if err != nil {
		return err
	}
	manifest.Files = files
	if err := manifest.validate(); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(staging, manifestPath), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func publishBundle(ctx context.Context, staging, output string) error {
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".persona-backup-publish-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	tarWriter := tar.NewWriter(file)
	manifest, err := readManifestPath(filepath.Join(staging, manifestPath))
	if err != nil {
		file.Close()
		return err
	}
	members := append([]string{manifestPath}, make([]string, 0, len(manifest.Files))...)
	for _, entry := range manifest.Files {
		members = append(members, entry.Path)
	}
	for _, member := range members {
		if err := appendTarMember(ctx, tarWriter, staging, member); err != nil {
			tarWriter.Close()
			file.Close()
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A hard link publishes the already-synced inode atomically and never
	// overwrites an existing backup, even if another creator races us.
	if err := os.Link(temporary, output); err != nil {
		return fmt.Errorf("publish the complete backup without overwriting: %w", err)
	}
	if err := os.Remove(temporary); err != nil {
		return err
	}
	return secretkey.SyncDir(parent)
}

func appendTarMember(ctx context.Context, writer *tar.Writer, staging, member string) error {
	if !validMemberName(member) {
		return fmt.Errorf("invalid archive member %q", member)
	}
	name := filepath.Join(staging, filepath.FromSlash(member))
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("archive member is not a regular file: %s", member)
	}
	header := &tar.Header{Name: member, Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime().UTC(), Format: tar.FormatPAX}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.CopyN(writer, contextReader{ctx: ctx, reader: file}, info.Size())
	return err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func readManifestPath(name string) (*Manifest, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return decodeManifest(io.LimitReader(file, maxManifestSize+1))
}

func decodeManifest(reader io.Reader) (*Manifest, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("read the backup manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("the backup manifest has trailing data")
	}
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

type bundleProblemKind string

const (
	bundleUnreadable bundleProblemKind = "unreadable"
	bundleUnsafe     bundleProblemKind = "unsafe"
	bundleManifest   bundleProblemKind = "manifest"
	bundleLayout     bundleProblemKind = "layout"
	bundleDigest     bundleProblemKind = "digest"
	bundleIncomplete bundleProblemKind = "incomplete"
	bundleKey        bundleProblemKind = "key"
)

type bundleProblem struct {
	kind bundleProblemKind
	err  error
}

func (e *bundleProblem) Error() string { return e.err.Error() }
func (e *bundleProblem) Unwrap() error { return e.err }

func problem(kind bundleProblemKind, err error) error {
	return &bundleProblem{kind: kind, err: err}
}

func openBundle(bundle string) (*os.File, int64, error) {
	info, err := os.Lstat(bundle)
	if err != nil {
		return nil, 0, problem(bundleUnreadable, err)
	}
	if err := checkBundleFile(info); err != nil {
		return nil, 0, err
	}
	file, err := os.Open(bundle)
	if err != nil {
		return nil, 0, problem(bundleUnreadable, err)
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, problem(bundleUnreadable, err)
	}
	if !os.SameFile(info, opened) {
		file.Close()
		return nil, 0, problem(bundleUnreadable, errors.New("the bundle changed while it was opened"))
	}
	if err := checkBundleFile(opened); err != nil {
		file.Close()
		return nil, 0, err
	}
	return file, opened.Size(), nil
}

func checkBundleFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return problem(bundleUnsafe, errors.New("the bundle is not a regular file"))
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return problem(bundleUnsafe, errors.New("the bundle must be mode 0600 before it is read"))
	}
	return nil
}

func readBundleManifest(bundle string) (*Manifest, int64, error) {
	file, size, err := openBundle(bundle)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	manifest, err := findBundleManifest(file)
	return manifest, size, err
}

func findBundleManifest(source io.Reader) (*Manifest, error) {
	reader := tar.NewReader(source)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil, problem(bundleManifest, errors.New("the bundle has no manifest"))
		}
		if err != nil {
			return nil, problem(bundleManifest, err)
		}
		if header.Name == manifestPath {
			return readArchiveManifest(reader, header)
		}
		if !regularArchiveFile(header) {
			return nil, problem(bundleLayout, errors.New("the archive contains a non-regular member"))
		}
	}
}

func regularArchiveFile(header *tar.Header) bool {
	if header.Typeflag != tar.TypeReg {
		return false
	}
	// The writer only produces regular non-sparse members. PAX sparse fields
	// can turn a small physical archive into a very large logical member.
	for field := range header.PAXRecords {
		if strings.HasPrefix(field, "GNU.sparse.") {
			return false
		}
	}
	return true
}

func readArchiveManifest(reader io.Reader, header *tar.Header) (*Manifest, error) {
	if !regularArchiveFile(header) || header.Size > maxManifestSize {
		return nil, problem(bundleManifest, errors.New("the bundle manifest is invalid"))
	}
	manifest, err := decodeManifest(io.LimitReader(reader, header.Size))
	if err != nil {
		return nil, problem(bundleManifest, err)
	}
	return manifest, nil
}

func extractBundle(bundle, destination string, manifest *Manifest) error {
	file, _, err := openBundle(bundle)
	if err != nil {
		return err
	}
	defer file.Close()
	return walkBundle(file, manifest, func(reader io.Reader, header *tar.Header) error {
		return extractValidatedManifest(reader, destination, header, manifest)
	}, func(reader io.Reader, entry FileEntry) error {
		return extractMember(reader, destination, entry)
	})
}

// walkBundle enforces the same layout for restoration and offline inspection.
// Each visitor consumes its full member and verifies its contents.
func walkBundle(source io.Reader, manifest *Manifest, onManifest func(io.Reader, *tar.Header) error, onMember func(io.Reader, FileEntry) error) error {
	expected := make(map[string]FileEntry, len(manifest.Files))
	for _, entry := range manifest.Files {
		expected[entry.Path] = entry
	}
	seenManifest := false
	reader := tar.NewReader(source)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return problem(bundleIncomplete, err)
		}
		if header.Name == manifestPath {
			if seenManifest {
				return problem(bundleLayout, errors.New("duplicate bundle manifest"))
			}
			seenManifest = true
			if err := onManifest(reader, header); err != nil {
				return err
			}
			continue
		}
		entry, ok := expected[header.Name]
		if !ok {
			return problem(bundleLayout, fmt.Errorf("unlisted or duplicate bundle member %q", header.Name))
		}
		if !regularArchiveFile(header) || header.Size != entry.Size || header.Mode != int64(entry.Mode) {
			return problem(bundleLayout, fmt.Errorf("invalid bundle member %s", header.Name))
		}
		delete(expected, header.Name)
		if err := onMember(reader, entry); err != nil {
			return err
		}
	}
	for name := range expected {
		return problem(bundleIncomplete, fmt.Errorf("bundle member %s is missing", name))
	}
	if !seenManifest {
		return problem(bundleManifest, errors.New("the bundle manifest is missing"))
	}
	return nil
}

func compareArchiveManifest(reader io.Reader, header *tar.Header, expected *Manifest) error {
	manifest, err := readArchiveManifest(reader, header)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(manifest, expected) {
		return problem(bundleManifest, errors.New("the bundle manifest changed between validation passes"))
	}
	return nil
}

func extractValidatedManifest(reader io.Reader, destination string, header *tar.Header, expected *Manifest) error {
	name := filepath.Join(destination, manifestPath)
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	decodeErr := compareArchiveManifest(io.TeeReader(reader, file), header, expected)
	if syncErr := file.Sync(); decodeErr == nil {
		decodeErr = syncErr
	}
	if closeErr := file.Close(); decodeErr == nil {
		decodeErr = closeErr
	}
	return decodeErr
}

func extractMember(reader io.Reader, destination string, entry FileEntry) error {
	name := filepath.Join(destination, filepath.FromSlash(entry.Path))
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(entry.Mode))
	if err != nil {
		return err
	}
	copyErr := checkBundleMember(reader, file, entry)
	if syncErr := file.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := file.Close(); copyErr == nil {
		copyErr = closeErr
	}
	return copyErr
}

func checkBundleMember(reader io.Reader, destination io.Writer, entry FileEntry) error {
	hash := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(destination, hash), reader, entry.Size); err != nil {
		return problem(bundleIncomplete, err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
		return problem(bundleDigest, fmt.Errorf("SHA-256 mismatch for %s", entry.Path))
	}
	return nil
}
