package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// A recording saved into a track (#13501, nodeproto.TrackRecordings, docs/assistant/health.md "Tracks and kinds"):
// the manager renders the transcript and names the file; the node writes it into the track's recordings/ on this
// machine, or removes it, and nothing else. The track's conversations read it there like any file of the track.

func init() { nodeRPC(assistantwire.DeviceAssistantSpaceRecording, (*Node).spaceRecording) }

// spaceRecordingParams are assistant.space_recording's.
type spaceRecordingParams struct {
	// Dir is the track's directory relative to the home, as on assistant.prepare.
	Dir string `json:"dir"`
	// Slug is the track's: Dir must be that track's directory.
	Slug string `json:"slug"`
	// Name is the transcript's file name in recordings/ (spaceRecordingName).
	Name string `json:"name"`
	// Content is what the file holds; ignored with Remove.
	Content string `json:"content"`
	// Remove removes the file instead.
	Remove bool `json:"remove"`
}

// spaceRecordingName is the only file name the manager gives: the day it started and the recording's UUID.
var spaceRecordingName = lazyre.New(`^[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.md$`)

// spaceRecordingLimit bounds a transcript file: the phone's 4 MB of text with the timestamps and the header on top.
const spaceRecordingLimit = 8 << 20

func (n *Node) spaceRecording(_ context.Context, p spaceRecordingParams) (map[string]any, error) {
	if p.Dir == "" || strings.Contains(p.Dir, "..") || !identity.ValidTrackSlug(p.Slug) || !spaceRecordingName.MatchString(p.Name) {
		return nil, fmt.Errorf("bad space recording")
	}
	if !p.Remove && len(p.Content) > spaceRecordingLimit {
		return nil, fmt.Errorf("space recording over %d bytes", spaceRecordingLimit)
	}
	dir, err := n.preparedDir(p.Dir)
	if err != nil {
		return nil, err
	}
	return spaceRecordingIn(dir, p)
}

// spaceRecordingIn writes or removes p's file in recordings/ of track directory dir, which must be p.Slug's and be
// there already (assistant.prepare made it): recordings/ is made when missing, never followed when it is a link.
func spaceRecordingIn(dir string, p spaceRecordingParams) (map[string]any, error) {
	if !identity.IsAssistantDir(dir) || identity.SpaceOf(dir) != p.Slug {
		return nil, fmt.Errorf("%s is not the directory of track %q", dir, p.Slug)
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("the directory of track %q is not there", p.Slug)
	}
	recs := filepath.Join(dir, "recordings")
	fi, err := os.Lstat(recs)
	if errors.Is(err, fs.ErrNotExist) {
		if p.Remove {
			return map[string]any{"removed": false}, nil
		}
		if err = os.Mkdir(recs, 0o700); err == nil {
			fi, err = os.Lstat(recs)
		}
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s: not a folder", recs)
	}
	path := filepath.Join(recs, p.Name)
	if p.Remove {
		err := os.Remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{"removed": false}, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"removed": true}, nil
	}
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a plain file", path)
	}
	if err := writeIfChanged(path, []byte(p.Content)); err != nil {
		return nil, err
	}
	return map[string]any{"path": path}, nil
}
