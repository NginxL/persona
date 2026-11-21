package chatexport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Follow the chat's folded view. Work narration stays under work steps except
// the current answer when the last assistant text is still narration.
func visibleItems(items []proto.Item, work, current bool) []proto.Item {
	latest := -1
	if current && !work {
		for i := len(items) - 1; i >= 0; i-- {
			if items[i].Role == "assistant" && items[i].Kind == proto.KindText {
				latest = i
				break
			}
		}
	}
	visible := items[:0]
	for i, item := range items {
		keep := false
		switch item.Kind {
		case proto.KindText:
			keep = work || !item.Narration || i == latest
		case proto.KindImage:
			keep = work || item.ToolUseID == ""
		case proto.KindThinking, proto.KindToolUse, proto.KindToolResult:
			keep = work
		case proto.KindNotice, proto.KindVisualization:
			keep = true
		case proto.KindFold, proto.KindRecap:
			// The chat's collapsible work and while-you-were-away recap are
			// projections, not additional conversation messages.
		default:
			keep = item.Role == "system" && item.Text != ""
		}
		if keep {
			visible = append(visible, item)
		}
	}
	return visible
}

type markdown struct {
	out     *bufio.Writer
	lang    i18n.Lang
	speaker string
}

func writeMarkdown(out io.Writer, lang i18n.Lang, doc conversation, work bool, exported time.Time) error {
	w := markdown{out: bufio.NewWriter(out), lang: lang}
	title := inline(doc.row.Title)
	if title == "" {
		title = w.word("title")
	}
	fmt.Fprintln(w.out, "# "+title)
	fmt.Fprintln(w.out)
	w.fact(w.word("computer"), doc.row.Machine)
	w.fact(w.word("session"), doc.row.ID)
	w.fact(w.word("folder"), doc.row.Folder)
	w.fact(w.word("exported"), exported.Format(time.RFC3339))
	fmt.Fprintln(w.out)
	for _, s := range doc.stretches {
		w.speaker = ""
		agent := inline(s.agent)
		if doc.row.Assistant {
			agent = w.word("persona")
		} else if agent == "" {
			agent = w.word("agent")
		}
		if len(doc.stretches) > 1 {
			fmt.Fprintln(w.out, "## "+w.word("stretch")+" · "+agent)
			fmt.Fprintln(w.out)
		}
		for _, item := range s.items {
			w.item(item, agent, work)
		}
	}
	return w.out.Flush()
}

func (w *markdown) word(key string) string {
	return i18n.T(w.lang, "chatexport."+key)
}

func (w *markdown) fact(label, value string) {
	if value := inline(value); value != "" {
		fmt.Fprintf(w.out, "- %s: %s\n", label, value)
	}
}

func (w *markdown) block(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	// Preserve indentation and internal blank lines, including inside code.
	fmt.Fprintln(w.out, strings.TrimRight(text, "\r\n"))
	fmt.Fprintln(w.out)
}

func (w *markdown) heading(key, label string, ts time.Time) {
	if w.speaker == key {
		return
	}
	w.speaker = key
	if !ts.IsZero() {
		label += " · " + ts.UTC().Format(time.RFC3339)
	}
	fmt.Fprintln(w.out, "### "+label)
	fmt.Fprintln(w.out)
}

func (w *markdown) item(it proto.Item, agent string, work bool) {
	if it.Role == "user" && (it.Kind == proto.KindText || it.Kind == proto.KindImage) {
		w.user(it)
		return
	}
	if it.Kind == proto.KindNotice || it.Role == "system" {
		w.speaker = ""
		if text := inline(it.Text); text != "" {
			w.block("> " + text)
		}
		return
	}
	w.heading("agent", agent, it.TS)
	switch it.Kind {
	case proto.KindText:
		if it.Narration && work {
			w.details(w.word("narration"), it.Text, "", false)
		} else {
			w.block(it.Text)
		}
	case proto.KindImage:
		w.block("_[" + w.word("image") + "]_")
	case proto.KindVisualization:
		label := w.word("visualization")
		if it.Visualization != nil && inline(it.Visualization.Title) != "" {
			label += " · " + inline(it.Visualization.Title)
		}
		w.block("_[" + label + "]_")
	case proto.KindThinking:
		w.details(w.word("thinking"), it.Text, "", false)
	case proto.KindToolUse:
		label := w.word("tool")
		if name := inline(it.ToolName); name != "" {
			label += " · " + name
		}
		var input bytes.Buffer
		// json.RawMessage arrives from a decoded page and already has valid JSON.
		_ = json.Indent(&input, it.ToolInput, "", "  ")
		w.details(label, input.String(), "json", true)
	case proto.KindToolResult:
		label := w.word("result")
		if it.IsError {
			label = w.word("tool_error")
		}
		w.details(label, it.Text, "", true)
	}
}

func (w *markdown) user(it proto.Item) {
	label := w.word("you")
	switch {
	case it.Injected != nil:
		label = w.word("persona")
	case it.From != "":
		label = inline(it.From)
	}
	text := it.Text
	switch {
	case it.Kind == proto.KindImage:
		text = "_[" + w.word("image") + "]_"
	case it.Injected != nil:
		text = inline(it.Injected.Line) + "\n\n" + it.Injected.Detail
	default:
		// Model-only context envelopes are not the user's message.
		if head := strings.TrimSpace(text); strings.HasPrefix(head, "<persona-context") ||
			strings.HasPrefix(head, "<system-reminder") || strings.HasPrefix(head, "<persona-view-errors") {
			text = transcript.StripInjected(text)
		}
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	key := it.ClientMsgID
	if key == "" {
		key = it.ID
	}
	w.heading("user:"+label+":"+key, label, it.TS)
	w.block(text)
}

func (w *markdown) details(label, text, language string, limit bool) {
	clipped := false
	if limit {
		text, clipped = clipWork(text)
	}
	fmt.Fprintln(w.out, "<details>")
	fmt.Fprintln(w.out, "<summary>"+label+"</summary>")
	fmt.Fprintln(w.out)
	if text != "" {
		fence := fenceFor(text)
		fmt.Fprintln(w.out, fence+language)
		fmt.Fprintln(w.out, text)
		fmt.Fprintln(w.out, fence)
		fmt.Fprintln(w.out)
	}
	if clipped {
		w.block("_" + w.word("clipped") + "_")
	}
	fmt.Fprintln(w.out, "</details>")
	fmt.Fprintln(w.out)
}

// Keep the same 4,000-character display budget as Web Markdown export.
func clipWork(text string) (string, bool) {
	count := 0
	for offset := range text {
		if count == 4000 {
			return text[:offset], true
		}
		count++
	}
	return text, false
}

func fenceFor(text string) string {
	longest, run := 2, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", longest+1)
}

var escapeInline = strings.NewReplacer(
	"\\", "\\\\", "<", "&lt;", ">", "&gt;", "&", "&amp;",
	"[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\"+"`",
)

func inline(text string) string {
	return escapeInline.Replace(strings.Join(strings.Fields(clitext.Inert(text)), " "))
}
