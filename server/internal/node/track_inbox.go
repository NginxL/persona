package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// A capture filed into a track (#13502, nodeproto.TrackInbox, docs/assistant/health.md "Quick capture"): the manager
// sorts what the user jotted down and hands the node one file for the track's inbox/. The node writes it there only:
// the directory must be the named track's own (tracks/<slug>) and a real folder, inbox/ a real folder in it, the name
// a plain file name, and the write goes to a new file renamed into place inside that folder (os.Root: no step of the
// way leaves the track's directory, nor follows a link out of it).

func init() { nodeRPC(assistantwire.DeviceTrackInboxWrite, (*Node).trackInboxWrite) }

// trackInboxParams is assistant.track_inbox_write's.
type trackInboxParams struct {
	// Dir is the track's directory relative to the home, as assistant.prepare's dir.
	Dir string `json:"dir"`
	// Slug is the track's: Dir must be that track's own directory.
	Slug string `json:"slug"`
	// Name is the file's name in inbox/ (trackInboxName).
	Name    string `json:"name"`
	Content string `json:"content"`
}

// trackInboxResult says where the file is, relative to the track's directory.
type trackInboxResult struct {
	Path string `json:"path"`
}

// trackInboxName is a file name a capture may have: letters, digits, dots, dashes and underscores, ending in .md, not
// starting with a dot.
var trackInboxName = lazyre.New(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,118}\.md$`)

// trackInboxMax bounds one file.
const trackInboxMax = 256 << 10

func (n *Node) trackInboxWrite(_ context.Context, p trackInboxParams) (trackInboxResult, error) {
	if p.Dir == "" || strings.Contains(p.Dir, "..") || !identity.ValidTrackSlug(p.Slug) || !trackInboxName.MatchString(p.Name) {
		return trackInboxResult{}, errors.New("bad track inbox write")
	}
	if len(p.Content) > trackInboxMax {
		return trackInboxResult{}, fmt.Errorf("a capture's file is larger than %d bytes", trackInboxMax)
	}
	dir, err := n.preparedDir(p.Dir)
	if err != nil {
		return trackInboxResult{}, err
	}
	rel, err := writeTrackInbox(dir, p.Slug, p.Name, []byte(p.Content))
	return trackInboxResult{Path: rel}, err
}

// writeTrackInbox writes b as inbox/name in track slug's directory dir and answers its path there. A file already
// holding b is left as it is (a retry); one holding anything else is not overwritten.
func writeTrackInbox(dir, slug, name string, b []byte) (string, error) {
	if !identity.IsAssistantDir(dir) || identity.SpaceOf(dir) != slug {
		return "", fmt.Errorf("%s is not the directory of track %q", dir, slug)
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s: not a folder (the track's directory is made when it is prepared)", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.Mkdir("inbox", 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	if fi, err := root.Lstat("inbox"); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s: inbox is not a folder", dir)
	}
	target := filepath.Join("inbox", name)
	switch fi, err := root.Lstat(target); {
	case err == nil && !fi.Mode().IsRegular():
		return "", fmt.Errorf("%s: not a plain file", target)
	case err == nil:
		if same, err := rootFileHolds(root, target, b); err != nil || same {
			return target, err
		}
		return "", fmt.Errorf("%s: another file is there already", target)
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	tmp := filepath.Join("inbox", "."+name+"."+rand.Text()+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, target)
	}
	if err != nil {
		_ = root.Remove(tmp)
		return "", err
	}
	return target, nil
}

// rootFileHolds: the plain file at path in root holds exactly b.
func rootFileHolds(root *os.Root, path string, b []byte) (bool, error) {
	f, err := root.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	got, err := io.ReadAll(io.LimitReader(f, int64(len(b))+1))
	if err != nil {
		return false, err
	}
	return bytes.Equal(got, b), nil
}
