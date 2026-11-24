package hub

import (
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The node writes a transcript's notices, task lines and activities in Chinese (internal/transcript): it does not know
// the user's language, and the node↔hub RPC stays as it is. On their way to the apps the hub rewrites the ones it can
// tell apart, from the item's structured fields (a task note's kind and status) or from the node's exact Chinese
// wording, into the reader's language (docs/protocol/i18n.md). zh-Hans, and anything the hub does not recognize (an older or
// newer node's wording), go out as the node wrote them. The items the assistant and the channels read are not touched:
// they match some of these lines (assistant/done.go's interrupted marker). The runtime bridges' own words in the items
// they give (transcript.Runtime*: Paseo's, opencode's, pi's and Antigravity's runtime notices) are rewritten the same way.
//
// Kept in Chinese because the apps parse them: the compaction divider lines, the API error prefix
// (the API error card), the truncation marker (cut text) and the /compact echo.

const transcriptNS = "hub-transcript_text."

func tt(l i18n.Lang, key string, args ...i18n.Args) string {
	return i18n.T(l, transcriptNS+key, args...)
}

// localizedItems is items as an app reading l gets them: a copy with recognized notices localized, or items itself
// when nothing changes. Refusal records receive an explanation in every language. The node's slice is shared.
func localizedItems(l i18n.Lang, items []proto.Item) []proto.Item {
	if !i18n.Valid(l) {
		l = i18n.ZhHans
	}
	var out []proto.Item
	for i := range items {
		if l == i18n.ZhHans && items[i].ModelRefusal == nil {
			continue
		}
		it, ok := localizedItem(l, items[i])
		if !ok {
			continue
		}
		if out == nil {
			out = append([]proto.Item(nil), items...)
		}
		out[i] = it
	}
	if out == nil {
		return items
	}
	return out
}

// localizedItem is it in l, ok when anything changed.
func localizedItem(l i18n.Lang, it proto.Item) (proto.Item, bool) {
	changed := false
	set := func(dst *string, s string) {
		if s != *dst {
			*dst, changed = s, true
		}
	}
	switch {
	case it.Kind == proto.KindNotice && it.ModelRefusal != nil:
		set(&it.Text, modelRefusalText(l, it.ModelRefusal))
	case it.Kind == proto.KindNotice && it.Task != nil:
		set(&it.Text, taskNoteText(l, it.Task, it.Text))
	case it.Kind == proto.KindNotice && it.Wake && it.Text == transcript.BackgroundTaskUpdate:
		set(&it.Text, tt(l, "task_updated"))
	case it.Kind == proto.KindNotice:
		set(&it.Text, noticeText(l, it.Text))
	case it.Kind == proto.KindText && it.Role == "user" && it.From == transcript.PersonaPeer && it.Text == transcript.StoppedFromPhone:
		set(&it.Text, tt(l, "stopped_from_phone"))
	case it.Kind == proto.KindText && it.Role == "user" && it.From != "":
		set(&it.From, fromName(l, it.From))
		set(&it.Text, teammateText(l, it.Text))
	case it.Kind == proto.KindText && it.Role == "assistant" && it.CodexHeartbeatQuiet:
		set(&it.Text, tt(l, "codex_heartbeat_quiet"))
	case it.Kind == proto.KindText && it.Role == "assistant" && strings.Contains(it.Text, codexNewChatLink):
		set(&it.Text, strings.ReplaceAll(it.Text, codexNewChatLink, "["+tt(l, "open_new_chat")+"](persona://session/"))
	case it.Kind == proto.KindToolResult && strings.Contains(it.Text, loadedToolPrefix):
		set(&it.Text, loadedTools(l, it.Text))
	case it.Kind == proto.KindToolResult:
		set(&it.Text, runtimeResultText(l, it.Text))
	}
	return it, changed
}

// The explanation is localized; Claude Code's refusal signal (the record's fields and its own words) stays verbatim.
// The record's line itself is never part of it (#2528), whatever an older node sent as the notice's text.
func modelRefusalText(l i18n.Lang, r *proto.ModelRefusal) string {
	key := "model_refusal"
	if r.Subtype == "model_refusal_fallback" && r.FallbackModel != "" {
		key = "model_refusal_fallback"
	}
	text := tt(l, key, i18n.Args{"subtype": r.Subtype, "original": r.OriginalModel,
		"fallback": r.FallbackModel, "category": r.Category, "scope": r.Scope})
	for _, words := range []string{r.Explanation, r.Text} {
		if words != "" {
			text += "\n" + words
		}
	}
	return text
}

// ---- notices ----

const (
	loopFired       = "/loop 到点，开始新一轮"
	codexExtension  = "Codex 扩展事件："
	codexEvent      = "Codex 事件："
	codexRecordNote = "（完整记录在原会话中）"
)

// noticeText is one of the node's fixed notices in l; anything else stays as it is.
func noticeText(l i18n.Lang, s string) string {
	switch {
	case s == transcript.InterruptedNotice:
		return tt(l, "interrupted")
	case s == transcript.OmpStoppedNotice:
		return tt(l, "omp_stopped")
	case s == loopFired:
		return tt(l, "loop_fired")
	case strings.HasPrefix(s, loopFired+"："):
		return tt(l, "loop_fired_prompt", i18n.Args{"prompt": strings.TrimPrefix(s, loopFired+"：")})
	case strings.HasPrefix(s, codexExtension) && strings.HasSuffix(s, codexRecordNote):
		return tt(l, "codex_extension", i18n.Args{"kind": strings.TrimSuffix(strings.TrimPrefix(s, codexExtension), codexRecordNote)})
	case strings.HasPrefix(s, codexEvent) && strings.HasSuffix(s, codexRecordNote):
		return tt(l, "codex_event", i18n.Args{"type": strings.TrimSuffix(strings.TrimPrefix(s, codexEvent), codexRecordNote)})
	case s == transcript.RuntimeAgyAutoRefusalNotice:
		return tt(l, "runtime_agy_auto_refused")
	case s == transcript.RuntimeError:
		return tt(l, "runtime_error")
	case strings.HasPrefix(s, transcript.RuntimeErrorLead):
		return tt(l, "runtime_error_cause", i18n.Args{"error": strings.TrimPrefix(s, transcript.RuntimeErrorLead)})
	case s == transcript.PaseoScheduleNotice:
		return tt(l, "schedule_fired")
	case strings.HasPrefix(s, transcript.PaseoScheduleNotice+"："):
		return tt(l, "schedule_fired_what", i18n.Args{"what": strings.TrimPrefix(s, transcript.PaseoScheduleNotice+"：")})
	case s == i18n.T(i18n.Default, "node-engine.agy_pane_integration"), s == legacyAgyPaneNotice:
		return i18n.T(l, "node-engine.agy_pane_integration")
	}
	return s
}

// legacyAgyPaneNotice is the pane notice an older node writes (before #13763 it pointed only at herdr's integration):
// shown in the reader's language as the current one.
const legacyAgyPaneNotice = "你仍可发送消息和使用终端。herdr 确认此窗格对应的对话后，Persona 就会显示该对话。如果尚未安装集成，请运行 herdr integration install antigravity-cli。"

// runtimeResultText is a tool result in l when a runtime bridge wrote words of its own into it (transcript.Runtime*):
// a cancelled call's, and a failed command's exit code after its output; the output itself stays as it is.
func runtimeResultText(l i18n.Lang, s string) string {
	if s == transcript.RuntimeCanceled {
		return tt(l, "runtime_canceled")
	}
	rest, ok := strings.CutSuffix(s, transcript.RuntimeExitTail)
	i := strings.LastIndex(rest, transcript.RuntimeExitHead)
	if !ok || i < 0 {
		return s
	}
	code := rest[i+len(transcript.RuntimeExitHead):]
	if _, err := strconv.Atoi(code); err != nil {
		return s
	}
	return rest[:i] + tt(l, "runtime_exit_code", i18n.Args{"code": code})
}

// taskNoteHead is the key of a task note's heading (transcript/tasknote.go noteText), "" for a task of another kind.
func taskNoteHead(n *proto.TaskNote) (key string, args i18n.Args) {
	failed := func(prefix string) (string, i18n.Args) {
		if n.ExitCode != nil {
			return prefix + "_failed_code", i18n.Args{"code": strconv.Itoa(*n.ExitCode)}
		}
		return prefix + "_failed", nil
	}
	switch n.Kind {
	case "shell":
		switch n.Status {
		case "completed":
			return "shell_completed", nil
		case "failed":
			return failed("shell")
		case "killed", "stopped":
			return "shell_stopped", nil
		}
		return "shell", nil
	case "monitor":
		switch n.Status {
		case "event":
			return "monitor_event", nil
		case "ended", "completed":
			return "monitor_ended", nil
		case "stopped", "killed":
			return "monitor_stopped", nil
		case "failed":
			return failed("monitor")
		}
		return "monitor", nil
	case "agent":
		switch n.Status {
		case "completed":
			return "agent_completed", nil
		case "failed":
			return "agent_failed", nil
		case "waiting":
			return "agent_waiting", nil
		case "killed", "stopped":
			return "agent_stopped", nil
		}
		return "agent", nil
	case "workflow":
		switch n.Status {
		case "completed":
			return "workflow_completed", nil
		case "failed":
			return "workflow_failed", nil // no exit code: noteText does not show one for a workflow
		case "killed", "stopped":
			return "workflow_stopped", nil
		}
		return "workflow", nil
	}
	return "", nil
}

// taskNoteText is a task note's line in l: its heading rewritten from the note's kind and status when text is what the
// node wrote for them (the "background command finished · <title>" heading), what follows the heading (the title, a Monitor's event) kept.
func taskNoteText(l i18n.Lang, n *proto.TaskNote, text string) string {
	// A Paseo child waiting for a permission (transcript/paseo.go): its words, not a subagent's wait for its own work.
	if n.Kind == "agent" && n.Status == "waiting" && (text == transcript.PaseoPermissionNotice || strings.HasPrefix(text, transcript.PaseoPermissionNotice+" · ")) {
		return tt(l, "agent_permission") + strings.TrimPrefix(text, transcript.PaseoPermissionNotice)
	}
	key, args := taskNoteHead(n)
	if key == "" {
		if text == transcript.BackgroundTaskUpdate {
			return tt(l, "task_updated")
		}
		return text
	}
	zh := tt(i18n.ZhHans, key, args)
	switch {
	case text == zh:
		return tt(l, key, args)
	case strings.HasPrefix(text, zh+" · "):
		return tt(l, key, args) + text[len(zh):]
	}
	return text
}

// ---- who a message is from, and what it says ----

// fromName is the sender's name in l when it is the node's stand-in for a sender without one.
func fromName(l i18n.Lang, from string) string {
	switch from {
	case transcript.FromSession:
		return tt(l, "from_session")
	case "队友":
		return tt(l, "from_teammate")
	}
	return from
}

// teammateLabels are the bold headings transcript/teammate.go puts on an agent-teams note, by key; labeled ones are
// followed by a full-width colon and "<what>".
var (
	teammateLabels = map[string]string{"被中断": "tm_interrupted", "同意结束": "tm_shutdown_approved",
		"提交了计划，等批准": "tm_plan_submitted", "计划已批准": "tm_plan_approved", "计划被退回": "tm_plan_rejected", "分配任务": "tm_assigned"}
	teammateLabeled = map[string]string{"出错停下": "tm_failed", "请求结束": "tm_shutdown_request", "拒绝结束": "tm_shutdown_rejected",
		"分配任务": "tm_assigned"}
)

// teammateText is an agent-teams note (transcript/teammate.go teammateNote: Markdown paragraphs) with the node's
// headings in l; the teammate's own words stay.
func teammateText(l i18n.Lang, s string) string {
	if s == "空闲，等待新的任务" {
		return tt(l, "tm_idle")
	}
	if !strings.Contains(s, "**") {
		return s
	}
	parts := strings.Split(s, "\n\n")
	for i, p := range parts {
		if !strings.HasPrefix(p, "**") {
			continue
		}
		end := strings.Index(p[2:], "**")
		if end < 0 {
			continue
		}
		label, rest := p[2:2+end], p[2+end+2:]
		head, ok := teammateHead(l, label)
		if !ok {
			continue
		}
		switch {
		case rest == "":
			parts[i] = "**" + head + "**"
		case strings.HasPrefix(rest, "："):
			if _, ok := teammateLabeled[strings.SplitN(label, " #", 2)[0]]; ok {
				parts[i] = tt(l, "tm_labeled", i18n.Args{"label": "**" + head + "**", "text": strings.TrimPrefix(rest, "：")})
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// teammateHead is one of teammateNote's headings in l ("assigned task #3" with its task's number).
func teammateHead(l i18n.Lang, label string) (string, bool) {
	if id, ok := strings.CutPrefix(label, "分配任务 #"); ok && id != "" {
		return tt(l, "tm_assigned_id", i18n.Args{"id": id}), true
	}
	if key, ok := teammateLabels[label]; ok {
		return tt(l, key), true
	}
	if key, ok := teammateLabeled[label]; ok {
		return tt(l, key), true
	}
	return "", false
}

// codexNewChatLink starts the link transcript/codex.go puts where the Codex app created a thread.
const codexNewChatLink = "[打开新建的对话](persona://session/"

const loadedToolPrefix = "已加载工具 "

// loadedTools rewrites the "loaded tool <name>" lines of a ToolSearch result (transcript.go's tool_reference).
func loadedTools(l i18n.Lang, s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if name, ok := strings.CutPrefix(line, loadedToolPrefix); ok && name != "" && !strings.ContainsAny(name, " \t") {
			lines[i] = tt(l, "loaded_tool", i18n.Args{"name": name})
		}
	}
	return strings.Join(lines, "\n")
}
