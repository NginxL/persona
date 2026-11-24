package node

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// An agy pane's row is herdr's agent (sessions.go), titled with the terminal's title. agy sets none, so the row kept the
// shell's ("user@host: /path") or nothing, and the apps showed herdr's tab name ("agent-2") even once the node read the
// pane's conversation (#13639). Once the node knows that conversation (agyConversation, agypane.go), the row is titled
// after it, as the companion titles the conversation's own row (integrations/antigravity, listSessions):
//   - the companion's title for it, unless that is its stand-in for a conversation agy has not titled (agyStandInTitle);
//   - else agy's own title: the summary the CLI keeps for each conversation in cache/conversation_metadata.json. agy
//     does not record whether a person or its model chose it, so its provenance stays unknown and Persona's automatic
//     naming leaves it as it is;
//   - else the first message the user typed into it, clipped like any untitled row's (promptTitle), with title_source
//     "prompt", which Persona's automatic naming may replace.
// With none of them yet, and while the conversation is not known, the terminal's title stands.

// agyStandIn is the title the companion gives a conversation agy has not titled and that has no folder.
const agyStandIn = "Antigravity"

// agyMetadataFile is where agy's CLI keeps a summary of each of its conversations, under its data directory.
var agyMetadataFile = filepath.Join("cache", "conversation_metadata.json")

// agyMetadataMax bounds the metadata file read for titles: a conversation's summary is under 2 KiB.
const agyMetadataMax = 16 << 20

// agyTitleBook is agy's titles by conversation as read from its metadata file, and that file.
type agyTitleBook struct {
	path   string
	file   os.FileInfo
	titles map[string]string
}

// agyPaneTitle titles s, the row of an agy pane, after conversation, the pane's conversation ("" while not known).
func (n *Node) agyPaneTitle(s *proto.Session, conversation string) {
	if conversation == "" {
		return
	}
	if r, _, ok := n.runtimes.rowOf(agyAgent + "-" + conversation); ok && r.Title != "" && !agyStandInTitle(r) {
		s.Title, s.TitleManual, s.TitleSource = r.Title, r.TitleManual, r.TitleSource
		return
	}
	if title := n.agyNativeTitle(conversation); title != "" {
		s.Title, s.TitleManual, s.TitleSource = title, false, proto.TitleSourceUnknown
		return
	}
	if u, ok := n.agyTranscriptMeta(conversation); ok {
		if title := promptTitle(u.first); title != "" {
			s.Title, s.TitleManual, s.TitleSource = title, false, proto.TitleSourcePrompt
		}
	}
}

// agyStandInTitle: the companion lists r under the name it gives a conversation agy has not titled (its folder's name,
// else agyStandIn), not a title of the conversation's.
func agyStandInTitle(r proto.Session) bool {
	return r.Title == agyStandIn || r.Cwd != "" && r.Title == filepath.Base(r.Cwd)
}

// agyNativeTitle is agy's own title for conversation, "" while it has none or its metadata cannot be read. agy rewrites
// the file as its conversations go on and the list is built every second, so it is parsed again only when it changed.
func (n *Node) agyNativeTitle(conversation string) string {
	path := filepath.Join(n.agyDataRoot(), agyMetadataFile)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	n.mu.Lock()
	book := n.agyTitles
	n.mu.Unlock()
	// A file rewritten within the last second can keep its size and time through another write: read it again.
	if book == nil || book.path != path || !sameAgyFile(info, book.file) || time.Since(info.ModTime()) <= time.Second {
		if book = readAgyTitles(path); book == nil {
			return ""
		}
		n.mu.Lock()
		n.agyTitles = book
		n.mu.Unlock()
	}
	return book.titles[conversation]
}

// readAgyTitles reads the titles in agy's metadata file at path, nil when it cannot be opened. A file too large or of
// another shape (one being written included) gives none, until it changes.
func readAgyTitles(path string) *agyTitleBook {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	book := &agyTitleBook{path: path, file: info}
	if info.Size() > agyMetadataMax {
		return book
	}
	raw, err := io.ReadAll(io.LimitReader(f, agyMetadataMax+1))
	if err != nil {
		return nil
	}
	var meta struct {
		Conversations map[string]struct {
			Summary *struct {
				Title string `json:"Title"`
			} `json:"summary"`
		} `json:"conversations"`
	}
	if len(raw) > agyMetadataMax || json.Unmarshal(raw, &meta) != nil {
		return book
	}
	book.titles = make(map[string]string, len(meta.Conversations))
	for id, c := range meta.Conversations {
		if c.Summary == nil {
			continue
		}
		if title := strings.Join(strings.Fields(c.Summary.Title), " "); title != "" {
			book.titles[id] = title
		}
	}
	return book
}
