package assistant

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// What an extension granted assistant.memory.read reads of the assistant's memory (docs/extensions/extensions.md
// "Assistant memory"): the entries of the assistant's own space, never another space's, with what their frontmatter
// says and, when asked, their text within bounds. The hub checked the grant; this only reads (node memory.go's list).

// memoryIndexFile is the memory's index (one line per entry), not an entry of its own.
const memoryIndexFile = "MEMORY.md"

// memoryFileWire is one file of the node's memory list (node.MemoryFile).
type memoryFileWire struct {
	Name      string     `json:"name"`
	Content   string     `json:"content"`
	UpdatedAt time.Time  `json:"updated_at"`
	CreatedAt *time.Time `json:"created_at,omitempty"` // absent from older nodes and where the file system has none
}

func (s *Service) extensionMemory(ctx context.Context, p assistantwire.AssistantMemoryParams) (assistantwire.AssistantMemoryResult, error) {
	out := assistantwire.AssistantMemoryResult{Entries: []assistantwire.AssistantMemoryEntry{}}
	budget := assistantwire.AssistantMemoryTotalMax
	// Every track keeps its memory apart: the reading extension gets a track's only when it may read what comes from
	// it (extensionHides, scene 6: the user's own extension every track's, a third party's the main track's and those
	// of tracks it was granted every default category of).
	for _, space := range s.memorySpaces(ctx) {
		if s.extensionHides(ctx, space) {
			continue
		}
		var res struct {
			Files []memoryFileWire `json:"files"`
		}
		if err := s.memory(ctx, map[string]any{"op": "list", "space": space}, &res); err != nil {
			if space != "" {
				s.logf("extension memory: track %s: %v", space, err)
				continue
			}
			return out, memoryCallError(err)
		}
		budget = s.extensionMemoryFiles(&out, res.Files, space, p.IncludeContent, budget)
	}
	return out, nil
}

// memorySpaces are the tracks whose memory an extension may be shown, the main track first.
func (s *Service) memorySpaces(ctx context.Context) []string {
	out := []string{""}
	for _, e := range s.tracks(ctx) {
		if e.Slug != "" {
			out = append(out, e.Slug)
		}
	}
	return out
}

// extensionMemoryFiles adds files, track space's memory, to out within budget bytes of content, and is what is left.
func (s *Service) extensionMemoryFiles(out *assistantwire.AssistantMemoryResult, files []memoryFileWire, space string, content bool,
	budget int) int {
	for _, f := range files {
		if f.Name == memoryIndexFile {
			continue
		}
		desc, typ := memoryFrontmatter(f.Content)
		e := assistantwire.AssistantMemoryEntry{Name: f.Name, Description: desc, Type: typ, Size: len(f.Content),
			Created: f.CreatedAt, Modified: f.UpdatedAt, Track: space}
		if content {
			text := f.Content
			if len(text) > assistantwire.AssistantMemoryEntryMax {
				text, e.Truncated = cutUTF8(text, assistantwire.AssistantMemoryEntryMax), true
			}
			if len(text) > budget {
				text, e.Truncated = "", true
			}
			budget -= len(text)
			e.Content = text
		}
		out.Entries = append(out.Entries, e)
	}
	return budget
}

// memoryCallError codes what reaching the assistant's memory failed on as the conversations' calls code it: no
// assistant computer, or one not connected, is machine_offline.
func memoryCallError(err error) error {
	switch {
	case errors.Is(err, errNoAssistantMachine):
		return &rpc.Error{Code: assistantwire.CodeMachineOffline, Message: "the assistant's computer is not set up"}
	case errors.Is(err, errMemoryMachineOffline):
		return &rpc.Error{Code: assistantwire.CodeMachineOffline, Message: "the assistant's computer is not connected"}
	}
	return conversationsCallError(err)
}

// errMemoryMachineOffline: the assistant's computer has not reported its home yet, so its memory cannot be found.
var errMemoryMachineOffline = errors.New("assistant machine is offline")

// cutUTF8 is s cut to at most n bytes, never inside a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// memoryFrontmatter reads a memory file's description and type from its YAML frontmatter (a block between two "---"
// lines at the top): top-level description and type, or type under metadata. Only plain one-line values are read; a
// file without frontmatter has neither.
func memoryFrontmatter(content string) (description, typ string) {
	rest, ok := strings.CutPrefix(strings.TrimPrefix(content, string(rune(0xFEFF))), "---") // a byte-order mark first
	if !ok {
		return "", ""
	}
	lines := strings.Split(strings.ReplaceAll(rest, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "" {
		return "", ""
	}
	section := ""
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return description, typ
		}
		indented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		key, value = strings.TrimSpace(key), unquoteYAML(strings.TrimSpace(value))
		if !indented {
			section = ""
			if value == "" {
				section = key
				continue
			}
		}
		switch {
		case !indented && key == "description":
			description = value
		case !indented && key == "type", indented && section == "metadata" && key == "type" && typ == "":
			typ = value
		}
	}
	return "", "" // never closed: not frontmatter
}

func unquoteYAML(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}
