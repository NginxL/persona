package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
)

// One memory for the assistant (#9440). Claude Code keeps auto memory under the config directory, so conversations in
// different Claude profiles each had their own. memoryDir is now one fixed directory for the assistant's own
// directory, every Claude start is told to use it (assistantCompactionSettings), and the copies the other profiles
// hold are merged into it here: before the directory is handed to a conversation, the Memory page or dreaming, under
// memoryMu. The check runs again each time, because a conversation that was already running keeps the settings it
// started with and can write to its profile's copy until it restarts.
//
// Rules: a note only another copy has is copied in with its index line; the same name with the same bytes is left;
// the same name with different content keeps the one modified later. Nothing is deleted: the other copies are never
// written, and each run first saves the memory as it was (before/) below <data dir>/memory-merge/<project>/<run>/,
// then for each note the copy's version (copies/<n>/) before it is merged and the version it replaced (replaced/<n>/).
// The run's manifest records each note as soon as its merge, backups and index line are done. A note whose bytes a
// run has recorded is skipped, so it is compared once: a note the user deleted or changed after the merge (even back
// to an older version, with its older time) is left as the user has it.
//
// A note that fails is not recorded: it is tried again by a later check, by itself, and the notes after it and the
// other copies are merged meanwhile. A run interrupted part way leaves the notes it had not recorded to the next.

const (
	memoryIndexName    = "MEMORY.md"
	memoryMergeDirName = "memory-merge"
	memoryMergeVersion = 1
	// memoryNoteLimit: a larger file is not a note; it stays where it is.
	memoryNoteLimit = 1 << 20
	// memoryMergeTextLimit is how much of a conflict's other version a report carries.
	memoryMergeTextLimit = 16 << 10
	// memoryMergeRetryAfter is how long a failed merge waits before it is tried again.
	memoryMergeRetryAfter = 10 * time.Minute

	memoryMergeAdded    = "added"
	memoryMergeSame     = "same"
	memoryMergeConflict = "conflict"
	memoryKeptCopy      = "copy"
	memoryKeptMemory    = "memory"
)

// memoryMergeFile is one note a run read from another copy and what it did with it.
type memoryMergeFile struct {
	Name     string    `json:"name"`
	SHA256   string    `json:"sha256"`
	Modified time.Time `json:"modified"`
	Action   string    `json:"action"` // added | same | conflict
	// Kept, for a conflict: "copy" (this note replaced the memory's) or "memory" (the memory's stayed).
	Kept string `json:"kept,omitempty"`
	// MemoryModified is when the memory's own version of a conflict was last modified.
	MemoryModified *time.Time `json:"memory_modified,omitempty"`
}

type memoryMergeCopy struct {
	Dir   string            `json:"dir"`
	Files []memoryMergeFile `json:"files"`
}

// memoryMergeRun is a run's manifest.json.
type memoryMergeRun struct {
	Version   int               `json:"version"`
	ID        string            `json:"id"` // the run's directory name, a UTC time: later runs sort after earlier ones
	At        time.Time         `json:"at"`
	MemoryDir string            `json:"memory_dir"`
	Copies    []memoryMergeCopy `json:"copies"`
}

// memoryMergeConflictOut is a conflict as the manager's report shows it.
type memoryMergeConflictOut struct {
	Name           string    `json:"name"`
	Kept           string    `json:"kept"`
	MemoryModified time.Time `json:"memory_modified"`
	CopyModified   time.Time `json:"copy_modified"`
	// Other is the version that was not kept, from the run's backup.
	Other     string `json:"other"`
	Truncated bool   `json:"truncated,omitempty"`
}

type memoryMergeRunOut struct {
	ID        string                   `json:"id"`
	At        time.Time                `json:"at"`
	Backup    string                   `json:"backup"`
	Added     []string                 `json:"added"`
	Conflicts []memoryMergeConflictOut `json:"conflicts"`
}

// memoryMergeFault, set by tests, fails the merge of the note it returns an error for.
var memoryMergeFault func(name string) error

// memoryMergeRace, set by tests, runs between comparing the memory's note at dst and changing it: the model writing it
// meanwhile.
var memoryMergeRace func(dst string)

// The calls a new note is published with, set by tests to stand for a file system without hard links or without a
// rename that refuses to replace, and for a write that fails.
var (
	memoryLink            = os.Link
	memoryRenameNoReplace = renameNoReplace
	memoryNoteWrite       = (*os.File).Write
)

// errMemoryNoteBusy: the memory's note changed every time it was looked at; the merge of the copy's is tried later.
var errMemoryNoteBusy = errors.New("the memory's note kept changing while it was merged")

type memoryNote struct {
	name string
	data []byte
	mod  time.Time
	sum  string
}

func (f memoryNote) seenKey() string { return f.name + "\x00" + f.sum }

// oneMemory: cwd is an assistant's own directory, whose memory is one for every profile. A space's directory (the
// Health space) is not: its memory stays where Claude Code keeps it.
func oneMemory(cwd string) bool {
	cwd = filepath.Clean(cwd)
	if identity.IsAssistantDir(cwd) {
		return identity.SpaceOf(cwd) == ""
	}
	real, err := filepath.EvalSymlinks(cwd)
	return err == nil && identity.IsAssistantDir(real) && identity.SpaceOf(real) == ""
}

// memoryKey is how Claude Code names cwd's project directory (it encodes the resolved path).
func memoryKey(cwd string) string {
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	return projectKeyRe.ReplaceAllString(cwd, "-")
}

// memoryForUse is memoryDir for something about to read or write the memory: the other profiles' copies are merged
// in first. A merge that fails is logged and the directory is used as it is.
func (n *Node) memoryForUse(cwd string) string {
	n.memoryMu.Lock()
	defer n.memoryMu.Unlock()
	return n.mergedMemoryDir(cwd)
}

// mergedMemoryDir is memoryForUse with memoryMu held.
func (n *Node) mergedMemoryDir(cwd string) string {
	dir := n.memoryDir(cwd)
	if !oneMemory(cwd) {
		return dir
	}
	key := memoryKey(cwd)
	if time.Now().Before(n.memoryMergeRetry[key]) {
		return dir
	}
	if _, err := n.mergeMemoryCopies(cwd, dir); err != nil {
		// Not on every start: a merge that cannot be done now (a directory that cannot be written) is tried again later.
		if n.memoryMergeRetry == nil {
			n.memoryMergeRetry = map[string]time.Time{}
		}
		n.memoryMergeRetry[key] = time.Now().Add(memoryMergeRetryAfter)
		if n.logger != nil {
			n.logger.Printf("assistant memory: merge the other profiles' copies: %v", err)
		}
	}
	return dir
}

// conversationMemoryDir is the autoMemoryDirectory of conversation id's Claude start in cwd, "" when the start keeps
// Claude Code's own: a guest or a meeting's room (auto memory is off there), and any directory but the assistant's own.
func (n *Node) conversationMemoryDir(id, cwd string) string {
	if !oneMemory(cwd) || n.guestOf(id) != nil || n.guestKindOf(id) != "" {
		return ""
	}
	return n.memoryForUse(cwd)
}

func (n *Node) memoryMergeRoot(key string) string {
	dir := n.dir
	if dir == "" {
		dir = filepath.Join(n.home, ".persona")
	}
	return filepath.Join(dir, memoryMergeDirName, key)
}

// memoryCopies lists the other config directories' memory for the project key, each once by real path.
func (n *Node) memoryCopies(key, dir string) []string {
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	seen := []string{real(dir)}
	var out []string
	for _, p := range n.projectDirs() {
		c := filepath.Join(p, key, "memory")
		info, err := os.Stat(c)
		if err != nil || !info.IsDir() {
			continue
		}
		r := real(c)
		dup := false
		for _, s := range seen {
			dup = dup || s == r
		}
		if dup {
			continue
		}
		seen = append(seen, r)
		out = append(out, c)
	}
	return out
}

// readMemoryNotes reads dir's notes: its regular .md files but the index, by name.
func readMemoryNotes(dir string) []memoryNote {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []memoryNote
	for _, e := range entries {
		name := e.Name()
		if name == memoryIndexName || !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			continue
		}
		if f, ok := readMemoryNote(filepath.Join(dir, name)); ok {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func readMemoryNote(path string) (memoryNote, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > memoryNoteLimit {
		return memoryNote{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return memoryNote{}, false
	}
	sum := sha256.Sum256(b)
	return memoryNote{name: filepath.Base(path), data: b, mod: info.ModTime(), sum: hex.EncodeToString(sum[:])}, true
}

// memoryMergeRuns reads the manifests below root, oldest first.
func memoryMergeRuns(root string) []memoryMergeRun {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var runs []memoryMergeRun
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, e.Name(), "manifest.json"))
		if err != nil {
			continue // a run that did not finish: the next check does its work again
		}
		var run memoryMergeRun
		if json.Unmarshal(b, &run) != nil || run.Version != memoryMergeVersion {
			continue
		}
		run.ID = e.Name()
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs
}

// memorySeen is the notes earlier runs already read for the project key, loaded once from their manifests.
func (n *Node) memorySeen(key string) map[string]bool {
	if seen, ok := n.memoryMerged[key]; ok {
		return seen
	}
	seen := map[string]bool{}
	for _, run := range memoryMergeRuns(n.memoryMergeRoot(key)) {
		for _, c := range run.Copies {
			for _, f := range c.Files {
				seen[f.Name+"\x00"+f.SHA256] = true
			}
		}
	}
	if n.memoryMerged == nil {
		n.memoryMerged = map[string]map[string]bool{}
	}
	n.memoryMerged[key] = seen
	return seen
}

// mergeMemoryCopies brings the notes of the other profiles' copies into dir, cwd's memory. It returns the run it
// recorded, nil when no copy had a note it has not seen. memoryMu is held.
func (n *Node) mergeMemoryCopies(cwd, dir string) (_ *memoryMergeRun, err error) {
	key := memoryKey(cwd)
	copies := n.memoryCopies(key, dir)
	if len(copies) == 0 {
		return nil, nil
	}
	seen := n.memorySeen(key)
	type pending struct {
		dir   string
		notes []memoryNote
		index string // the copy's own MEMORY.md
	}
	var todo []pending
	for _, c := range copies {
		var fresh []memoryNote
		for _, f := range readMemoryNotes(c) {
			if !seen[f.seenKey()] {
				fresh = append(fresh, f)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		index, err := os.ReadFile(filepath.Join(c, memoryIndexName))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		todo = append(todo, pending{c, fresh, string(index)})
	}
	if len(todo) == 0 {
		return nil, nil
	}

	root := n.memoryMergeRoot(key)
	at := time.Now().UTC()
	id, runDir, err := newMemoryMergeRun(root, at)
	if err != nil {
		return nil, err
	}
	// run.Copies[i] is todo[i]: a note's backup lives under copies/<i>/ and replaced/<i>/.
	run := &memoryMergeRun{Version: memoryMergeVersion, ID: id, At: at, MemoryDir: dir, Copies: make([]memoryMergeCopy, len(todo))}
	for i, p := range todo {
		run.Copies[i] = memoryMergeCopy{Dir: p.dir, Files: []memoryMergeFile{}}
	}
	applied, recorded := false, false
	defer func() {
		if !applied && !recorded { // nothing was changed or recorded: the backup of a run that did not happen is not kept
			os.RemoveAll(runDir)
		}
	}()
	// Nothing is written to the memory before what it holds is saved.
	if err := copyMemoryTree(dir, filepath.Join(runDir, "before")); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Each note is recorded in the manifest, and so seen, only once its merge, its backups and its index line are
	// done. A note that fails is left for the next check and holds back neither the notes after it nor other copies.
	var errs []error
	for i, p := range todo {
		for _, f := range p.notes {
			rec, changed, err := mergeMemoryCopyNote(dir, filepath.Join(runDir, "copies", strconv.Itoa(i)), filepath.Join(runDir, "replaced", strconv.Itoa(i)), p.index, f)
			applied = applied || changed
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", f.name, err))
				continue
			}
			files := run.Copies[i].Files
			run.Copies[i].Files = append(files, rec)
			if err := writeMemoryMergeManifest(runDir, run); err != nil {
				run.Copies[i].Files = files
				errs = append(errs, err)
				continue
			}
			recorded = true
			seen[rec.Name+"\x00"+rec.SHA256] = true
		}
	}
	if !recorded {
		return nil, errors.Join(errs...)
	}
	return run, errors.Join(errs...)
}

// mergeMemoryCopyNote merges one note of the copy whose backups go to copyBackup and replacedBackup into dir, with
// its index line: the copy's own (from index) or one made from the note. changed says the memory was written.
func mergeMemoryCopyNote(dir, copyBackup, replacedBackup, index string, f memoryNote) (rec memoryMergeFile, changed bool, err error) {
	if memoryMergeFault != nil {
		if err := memoryMergeFault(f.name); err != nil {
			return rec, false, err
		}
	}
	if err := writeMemoryFile(filepath.Join(copyBackup, f.name), f.data, f.mod); err != nil {
		return rec, false, err
	}
	rec = memoryMergeFile{Name: f.name, SHA256: f.sum, Modified: f.mod.UTC()}
	var replaced []byte
	rec.Action, rec.Kept, rec.MemoryModified, replaced, err = mergeMemoryNote(dir, f)
	if err != nil {
		return rec, false, err
	}
	changed = rec.Action == memoryMergeAdded || rec.Kept == memoryKeptCopy
	if replaced != nil {
		// The version this copy's note took the place of, as it was at that moment. Kept apart from before/, which
		// stays the memory as the run found it: a later copy may replace the same name again.
		if err := writeMemoryFile(filepath.Join(replacedBackup, f.name), replaced, *rec.MemoryModified); err != nil {
			return rec, changed, err
		}
	}
	// A note the memory now holds as the copy has it gets an index line if the memory's index has none: also when both
	// held it already, which is what finishes a note whose line an earlier attempt did not write.
	if changed || rec.Action == memoryMergeSame {
		if err := addMemoryIndexLines(dir, []memoryIndexEntry{{f.name, memoryIndexLine(index, f)}}); err != nil {
			return rec, changed, err
		}
	}
	return rec, changed, nil
}

func writeMemoryMergeManifest(runDir string, run *memoryMergeRun) error {
	b, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return writeMemoryFile(filepath.Join(runDir, "manifest.json"), b, time.Time{})
}

// newMemoryMergeRun makes the run's directory, named for the time so that runs sort in order.
func newMemoryMergeRun(root string, at time.Time) (id, dir string, err error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", err
	}
	base := at.Format("20060102T150405Z")
	for i := 0; i < 100; i++ {
		id = base
		if i > 0 {
			id = base + "-" + strconv.Itoa(i)
		}
		dir = filepath.Join(root, id)
		if err = os.Mkdir(dir, 0o700); err == nil {
			return id, dir, nil
		}
		if !os.IsExist(err) {
			return "", "", err
		}
	}
	return "", "", err
}

// mergeMemoryNote applies the rules to one note of another copy. The model may be writing the memory at the same
// moment with its own file tools, outside memoryMu: a new note is created only if the name is still free, and the
// memory's version is replaced only if it has not changed since it was compared. replaced is the memory's version a
// newer copy took the place of.
func mergeMemoryNote(dir string, f memoryNote) (action, kept string, memoryMod *time.Time, replaced []byte, err error) {
	dst := filepath.Join(dir, f.name)
	for try := 0; try < 3; try++ {
		info, err := os.Lstat(dst)
		if os.IsNotExist(err) {
			if memoryMergeRace != nil {
				memoryMergeRace(dst)
			}
			made, err := createMemoryNote(dst, f)
			if err != nil {
				return "", "", nil, nil, err
			}
			if made {
				return memoryMergeAdded, "", nil, nil, nil
			}
			continue // written by someone else meanwhile: compare with it
		}
		if err != nil {
			return "", "", nil, nil, err
		}
		mod := info.ModTime().UTC()
		if !info.Mode().IsRegular() { // a link or a directory under the name: not ours to replace
			return memoryMergeConflict, memoryKeptMemory, &mod, nil, nil
		}
		cur, err := os.ReadFile(dst)
		if err != nil {
			return "", "", nil, nil, err
		}
		if bytes.Equal(cur, f.data) {
			return memoryMergeSame, "", nil, nil, nil
		}
		if !f.mod.After(info.ModTime()) {
			return memoryMergeConflict, memoryKeptMemory, &mod, nil, nil
		}
		if memoryMergeRace != nil {
			memoryMergeRace(dst)
		}
		swapped, err := replaceMemoryNote(dst, f, info, cur)
		if err != nil {
			return "", "", nil, nil, err
		}
		if swapped {
			return memoryMergeConflict, memoryKeptCopy, &mod, cur, nil
		}
	}
	// It changed each time it was looked at: it is being written now. What is there is the memory's version only if
	// it is there and not older than the copy's; otherwise nothing is settled, and the note is not recorded, so that a
	// later check merges it.
	info, err := os.Lstat(dst)
	if err == nil {
		mod := info.ModTime().UTC()
		if !info.Mode().IsRegular() {
			return memoryMergeConflict, memoryKeptMemory, &mod, nil, nil
		}
		if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, f.data) {
			return memoryMergeSame, "", nil, nil, nil
		}
		if !f.mod.After(info.ModTime()) {
			return memoryMergeConflict, memoryKeptMemory, &mod, nil, nil
		}
	}
	return "", "", nil, nil, errMemoryNoteBusy
}

func memoryTemp(dir string, data []byte, mod time.Time) (string, error) {
	tmp, err := os.CreateTemp(dir, ".merge-*.tmp")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, 0o600)
	}
	if err == nil && !mod.IsZero() {
		err = os.Chtimes(name, mod, mod)
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// createMemoryNote writes f at dst unless something is there: whole or not at all, and never over another file. The
// note is written and synced under a temporary name beside dst first, then published by one call that refuses to
// replace: a hard link, or where there are none, a rename that refuses to replace. Only a file system with neither
// gets dst created exclusively and written in place, and taken away again if that write fails.
func createMemoryNote(dst string, f memoryNote) (bool, error) {
	tmp, err := memoryTemp(filepath.Dir(dst), f.data, f.mod)
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp)
	err = memoryLink(tmp, dst)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	err = memoryRenameNoReplace(tmp, dst)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if !errors.Is(err, errNoReplaceRename) {
		return false, err
	}
	return createMemoryNoteInPlace(dst, f)
}

// createMemoryNoteInPlace creates dst exclusively and writes f into it. A write that fails takes the part written away
// again: left there, it would pass for the memory's own, newer version on the next check.
func createMemoryNoteInPlace(dst string, f memoryNote) (bool, error) {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	mine, statErr := out.Stat()
	_, err = memoryNoteWrite(out, f.data)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && !f.mod.IsZero() {
		err = os.Chtimes(dst, f.mod, f.mod)
	}
	if err != nil {
		// Unless the name holds someone else's file by now.
		if now, lerr := os.Lstat(dst); lerr == nil && statErr == nil && os.SameFile(mine, now) {
			os.Remove(dst)
		}
		return false, err
	}
	return true, nil
}

// replaceMemoryNote puts f in the place of dst if dst is still the file that was compared: the same time and size as
// was, and the same bytes as cur, read again as the last thing before the rename.
func replaceMemoryNote(dst string, f memoryNote, was fs.FileInfo, cur []byte) (bool, error) {
	tmp, err := memoryTemp(filepath.Dir(dst), f.data, f.mod)
	if err != nil {
		return false, err
	}
	now, err := os.Lstat(dst)
	if err != nil || !now.Mode().IsRegular() || !now.ModTime().Equal(was.ModTime()) || now.Size() != was.Size() {
		os.Remove(tmp)
		return false, nil
	}
	if again, err := os.ReadFile(dst); err != nil || !bytes.Equal(again, cur) {
		os.Remove(tmp)
		return false, nil
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// writeMemoryFile writes path through a temporary file beside it; a zero mod leaves the time as written.
func writeMemoryFile(path string, data []byte, mod time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := memoryTemp(filepath.Dir(path), data, mod)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// copyMemoryTree copies src's regular files to dst, keeping their times. A missing src is an empty memory. A src that
// is itself a link is followed (WalkDir does not follow its root); links inside it are not.
func copyMemoryTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(src); err == nil {
		src = real
	} else if !os.IsNotExist(err) {
		return err
	}
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == src {
				return fs.SkipAll
			}
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || strings.HasSuffix(d.Name(), ".tmp") && strings.HasPrefix(d.Name(), ".merge-") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return writeMemoryFile(filepath.Join(dst, rel), b, info.ModTime())
	})
	return err
}

// memoryIndexEntry is a MEMORY.md line and the note it points to.
type memoryIndexEntry struct{ name, line string }

// memoryIndexLinks says whether a MEMORY.md line points to the note name: a Markdown link whose destination is that
// file name exactly ("./name" and "<name>" too, a link title after it allowed). The name in a line's description, or
// as part of another file's name, is not a link to it.
func memoryIndexLinks(line, name string) bool {
	for rest := line; ; {
		i := strings.Index(rest, "](")
		if i < 0 {
			return false
		}
		rest = rest[i+2:]
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return false
		}
		target, _, _ := strings.Cut(strings.TrimSpace(rest[:end]), " ")
		target = strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">"), "./")
		if target == name {
			return true
		}
		rest = rest[end+1:]
	}
}

// memoryIndexHas says whether some line of index points to the note name.
func memoryIndexHas(index, name string) bool {
	_, ok := memoryIndexLineOf(index, name)
	return ok
}

// memoryIndexLineOf is index's line for the note name.
func memoryIndexLineOf(index, name string) (string, bool) {
	for _, l := range strings.Split(index, "\n") {
		if memoryIndexLinks(l, name) {
			return strings.TrimRight(l, "\r"), true
		}
	}
	return "", false
}

// memoryIndexLine is the MEMORY.md line for note f: the one its own copy's index has, else one made from the note's
// frontmatter (its name and description).
func memoryIndexLine(index string, f memoryNote) string {
	if line, ok := memoryIndexLineOf(index, f.name); ok {
		return line
	}
	title, hook := memoryFrontmatter(string(f.data))
	if title == "" {
		title = strings.TrimSuffix(f.name, ".md")
	}
	line := "- [" + title + "](" + f.name + ")"
	if hook != "" {
		line += " — " + hook
	}
	return line
}

// memoryFrontmatter reads name and description from a note's leading `---` block.
func memoryFrontmatter(text string) (name, description string) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}
	for _, l := range lines[1:] {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "---" {
			break
		}
		if v, ok := strings.CutPrefix(l, "name:"); ok {
			name = strings.Trim(strings.TrimSpace(v), `"'`)
		}
		if v, ok := strings.CutPrefix(l, "description:"); ok {
			description = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return name, description
}

// addMemoryIndexLines appends to dir's MEMORY.md the lines whose note it does not point to yet. The model edits the
// index too: it is read again just before it is replaced, and the append is redone on what is there then.
func addMemoryIndexLines(dir string, lines []memoryIndexEntry) error {
	if len(lines) == 0 {
		return nil
	}
	path := filepath.Join(dir, memoryIndexName)
	for try := 0; try < 5; try++ {
		cur, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		missing := err != nil
		next := string(cur)
		added := false
		for _, l := range lines {
			if memoryIndexHas(next, l.name) {
				continue
			}
			if next != "" && !strings.HasSuffix(next, "\n") {
				next += "\n"
			}
			next += l.line + "\n"
			added = true
		}
		if !added {
			return nil
		}
		tmp, err := memoryTemp(dir, []byte(next), time.Time{})
		if err != nil {
			return err
		}
		again, err := os.ReadFile(path)
		if (err != nil) != missing || !bytes.Equal(again, cur) {
			os.Remove(tmp)
			continue
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			return err
		}
		return nil
	}
	return errors.New("the memory index kept changing")
}

// memoryMerges answers the manager's "merges": the runs after id `after` that brought a note in or met a conflict,
// oldest first, with each conflict's other version from the run's backup.
func (n *Node) memoryMerges(cwd, after string) []memoryMergeRunOut {
	root := n.memoryMergeRoot(memoryKey(cwd))
	out := []memoryMergeRunOut{}
	for _, run := range memoryMergeRuns(root) {
		if run.ID <= after {
			continue
		}
		o := memoryMergeRunOut{ID: run.ID, At: run.At, Backup: filepath.Join(root, run.ID), Added: []string{}, Conflicts: []memoryMergeConflictOut{}}
		for i, c := range run.Copies {
			for _, f := range c.Files {
				switch f.Action {
				case memoryMergeAdded:
					o.Added = append(o.Added, f.Name)
				case memoryMergeConflict:
					conflict := memoryMergeConflictOut{Name: f.Name, Kept: f.Kept, CopyModified: f.Modified}
					if f.MemoryModified != nil {
						conflict.MemoryModified = *f.MemoryModified
					}
					other := filepath.Join(root, run.ID, "copies", strconv.Itoa(i), f.Name)
					if f.Kept == memoryKeptCopy {
						other = filepath.Join(root, run.ID, "replaced", strconv.Itoa(i), f.Name)
					}
					if b, err := os.ReadFile(other); err == nil {
						if len(b) > memoryMergeTextLimit {
							b, conflict.Truncated = b[:memoryMergeTextLimit], true
						}
						conflict.Other = strings.ToValidUTF8(string(b), "")
					}
					o.Conflicts = append(o.Conflicts, conflict)
				}
			}
		}
		if len(o.Added)+len(o.Conflicts) > 0 {
			out = append(out, o)
		}
	}
	return out
}
