package node

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/identity"
)

const dreamNoteName = "dreaming.md"
const dreamNoteHeader = "---\ntitle: User preferences and corrections\ndescription: Background summary of stable user preferences and corrections\nmetadata:\n  type: user\n---\n\n"
const dreamIndexLine = "- [User preferences and corrections](dreaming.md) — Background summary of stable preferences and corrections"

func checkDreamMemoryDir(cwd string) error {
	cwd = filepath.Clean(cwd)
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	if !identity.IsAssistantDir(cwd) || identity.SpaceOf(cwd) != "" {
		return errors.New("dreaming requires a regular assistant directory")
	}
	return nil
}

// readDreamFile distinguishes an absent file from an empty one. Refuse oversized or indirect files.
func readDreamFile(path string, limit int64) (*string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid file for memory dreaming")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit || !utf8.Valid(data) {
		return nil, errors.New("invalid file content for memory dreaming")
	}
	text := string(data)
	return &text, nil
}

func dreamMemory(dir string, a memoryOp) (any, error) {
	if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
		return nil, errors.New("invalid memory dreaming directory")
	}
	notePath := filepath.Join(dir, dreamNoteName)
	note, err := readDreamFile(notePath, 8<<10)
	if err != nil {
		return nil, err
	}
	indexPath := filepath.Join(dir, "MEMORY.md")
	index, err := readDreamFile(indexPath, 24<<10)
	if err != nil {
		return nil, err
	}
	indexText := ""
	if index != nil {
		indexText = *index
	}
	if a.Op == "dream-read" {
		return map[string]any{"note": note, "index": indexText}, nil
	}
	body := strings.TrimSpace(a.Content)
	if body == "" || len(body) > 4000 || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return nil, errors.New("invalid memory dreaming result")
	}
	if (a.Previous == nil) != (note == nil) || note != nil && *note != *a.Previous || indexText != a.PreviousIndex {
		return nil, errors.New("memory changed during dreaming")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	linked := false
	for _, line := range strings.Split(indexText, "\n") {
		if memoryIndexLinks(line, dreamNoteName) {
			linked = true
			break
		}
	}
	if !linked {
		updated := strings.TrimRight(indexText, "\r\n")
		if updated != "" {
			updated += "\n"
		}
		updated += dreamIndexLine + "\n"
		if len(updated) > 24<<10 {
			return nil, errors.New("memory index has no room for dreaming")
		}
		// Publish the pointer first so a partial write can be retried without creating an invisible note.
		if err := writeFileAtomic(indexPath, []byte(updated)); err != nil {
			return nil, err
		}
	}
	return ok(writeFileAtomic(notePath, []byte(dreamNoteHeader+body+"\n")))
}
