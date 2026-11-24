package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/identity"
)

// A track's directory (#13472, nodeproto.TrackSpaces, docs/assistant/health.md "Tracks and kinds"): the manager
// names it on assistant.prepare with space; the node keeps its folders, writes its SPACE.md once, and writes CLAUDE.md
// and AGENTS.md as the instructions given followed by the SPACE.md it reads, so an edit of SPACE.md reaches the next
// conversation without the manager reading the file.

// spacePrepare is assistant.prepare's space.
type spacePrepare struct {
	// Slug is the track's: the directory must be that track's (tracks/<slug>).
	Slug string `json:"slug"`
	// InitialSpaceMD is the SPACE.md written when there is none; an existing one is never overwritten.
	InitialSpaceMD string `json:"initial_space_md"`
}

// spaceFolders are the folders every track's directory has: what the user files into it, its recordings, and the
// platform's own (the SPACE.md history).
var spaceFolders = []string{"inbox", "recordings", filepath.Join(".space", "history")}

// spaceMDLimit bounds the SPACE.md that goes into the instructions; a longer one is cut there, saying so.
const spaceMDLimit = 64 << 10

// prepareSpaceDir keeps track directory dir (made already) as sp names it and answers its SPACE.md.
func prepareSpaceDir(dir string, sp spacePrepare) (string, error) {
	if err := checkSpaceDir(dir, sp.Slug); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	for _, sub := range spaceFolders {
		p := filepath.Join(dir, sub)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return "", err
		}
		if fi, err := os.Lstat(p); err != nil || !fi.IsDir() {
			return "", fmt.Errorf("%s: not a folder", p)
		}
	}
	md := filepath.Join(dir, "SPACE.md")
	if f, err := os.OpenFile(md, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
		_, err = io.WriteString(f, sp.InitialSpaceMD)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return readSpaceMD(md)
}

// checkSpaceDir: dir is track slug's own directory (tracks/<slug> in a state directory), told from the path alone.
func checkSpaceDir(dir, slug string) error {
	if !identity.ValidTrackSlug(slug) || !identity.IsAssistantDir(dir) || identity.SpaceOf(dir) != slug {
		return fmt.Errorf("%s is not the directory of track %q", dir, slug)
	}
	return nil
}

// readSpaceMD reads a track's SPACE.md: a plain file (never a link, nor one put in its place meanwhile: openNoBlock),
// cut at spaceMDLimit.
func readSpaceMD(path string) (string, error) {
	f, err := openNoBlock(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: not a plain file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, spaceMDLimit+1))
	if err != nil {
		return "", err
	}
	if len(b) > spaceMDLimit {
		b = append(bytes.ToValidUTF8(b[:spaceMDLimit], nil), []byte("\n\n[SPACE.md is longer than 64 KB: the rest is not shown here]\n")...)
	}
	return string(b), nil
}

// spaceInstructions are a track's CLAUDE.md and AGENTS.md: the instructions the manager gave, then its SPACE.md.
func spaceInstructions(claudeMD, spaceMD string) string {
	return strings.TrimRight(claudeMD, "\n") + "\n\n" + spaceMD
}

// writeIfChanged writes b to path (atomically, 0600) unless it holds b already: a conversation restarts on new
// instructions (instructionsStale), so the same bytes are never written again.
func writeIfChanged(path string, b []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return nil
	}
	return writeFileAtomic(path, b)
}

// digestOf is the hex SHA-256 of s.
func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
