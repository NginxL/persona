package transcript

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// Codex's bundled visualize plugin references a self-contained HTML fragment in its final reply with a private-use
// marker:
//
//	<U+E200>visualize<U+E202>{"path":"/abs/title.html","mode":"wide"}<U+E201>
//
// The Codex client reads the file and draws it inline (736 px, or 1024 px in wide mode). Persona parses it into a
// visualization item so every client can do the same; without this the marker is invisible and only the bare path
// shows. The bytes stay on the node and are served through the session visualization route after path checks.
const (
	vizStart = '\ue200'
	vizSep   = '\ue202'
	vizEnd   = '\ue201'
	vizTag   = "visualize"
)

// VisualizationModeWide is the marker's wide mode (up to 1024 px); any other mode or "" is the regular surface.
const VisualizationModeWide = "wide"

// VisualizationRef is one marker worth rendering, in the order it appeared in the message.
type VisualizationRef struct {
	Path  string
	Mode  string
	Title string
}

// splitVisualizations removes the well-formed visualize markers from text and returns the prose that remains plus
// the references in order. A malformed marker (bad JSON, a missing or relative path, an unknown mode) is kept in the
// text verbatim, so nothing the model wrote is silently dropped.
func splitVisualizations(text string) (string, []VisualizationRef) {
	if !strings.ContainsRune(text, vizStart) {
		return text, nil
	}
	var refs []VisualizationRef
	var b strings.Builder
	b.Grow(len(text))
	for {
		i := strings.IndexRune(text, vizStart)
		if i < 0 {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:i])
		text = text[i+len(string(vizStart)):] // skip the start rune (IndexRune returns a byte index)
		if !strings.HasPrefix(text, vizTag) {
			b.WriteRune(vizStart)
			continue
		}
		rest := text[len(vizTag):]
		if !strings.HasPrefix(rest, string(vizSep)) {
			b.WriteRune(vizStart)
			b.WriteString(vizTag)
			text = rest
			continue
		}
		rest = rest[len(string(vizSep)):] // skip the separator rune
		j := strings.IndexRune(rest, vizEnd)
		if j < 0 { // never closed: leave the partial marker where it was
			b.WriteRune(vizStart)
			b.WriteString(vizTag)
			b.WriteRune(vizSep)
			text = rest
			continue
		}
		raw, after := rest[:j], rest[j+len(string(vizEnd)):]
		ref, ok := parseVisualizationRef(raw)
		if !ok {
			b.WriteRune(vizStart)
			b.WriteString(vizTag)
			b.WriteRune(vizSep)
			b.WriteString(raw)
			b.WriteRune(vizEnd)
			text = after
			continue
		}
		refs = append(refs, ref)
		text = after
	}
	clean := strings.TrimSpace(b.String())
	// A marker sits on its own line; removing it leaves blank lines behind. Collapse runs of them, never paragraphs.
	for strings.Contains(clean, "\n\n\n") {
		clean = strings.ReplaceAll(clean, "\n\n\n", "\n\n")
	}
	return clean, refs
}

// parseVisualizationRef decodes one marker's JSON object. It accepts only an absolute path and the known modes.
func parseVisualizationRef(raw string) (VisualizationRef, bool) {
	var v struct {
		Path  string `json:"path"`
		Mode  string `json:"mode"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return VisualizationRef{}, false
	}
	if v.Path == "" || !filepath.IsAbs(v.Path) {
		return VisualizationRef{}, false
	}
	if v.Mode != "" && v.Mode != VisualizationModeWide {
		return VisualizationRef{}, false
	}
	if v.Title != "" && len(v.Title) > 256 {
		return VisualizationRef{}, false
	}
	return VisualizationRef{Path: v.Path, Mode: v.Mode, Title: v.Title}, true
}
