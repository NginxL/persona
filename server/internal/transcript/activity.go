package transcript

import (
	"encoding/json"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// ToolCall is the latest tool call of a transcript that no assistant text or new prompt came after (Meta.LastTool):
// what an agent that only runs tools (reads code, runs tests, waits for CI) is doing, while its last text stays put.
type ToolCall struct {
	Name  string
	Input json.RawMessage
	At    time.Time
}

// trackActivity keeps Meta.LastTool for one parsed item: a tool call is the activity until the agent writes text
// (its words come first again) or a new turn starts.
func (m *Meta) trackActivity(it proto.Item) {
	switch {
	case it.Role == "assistant" && it.Kind == proto.KindToolUse:
		if it.ToolName == "" {
			return
		}
		at := it.TS
		if at.IsZero() && m.LastTool != nil {
			at = m.LastTool.At
		}
		m.LastTool = &ToolCall{Name: it.ToolName, Input: it.ToolInput, At: at}
	case it.Role == "assistant" && it.Kind == proto.KindText:
		m.LastTool = nil
	case it.Role == "user" && it.Kind == proto.KindText && !it.Queued:
		m.LastTool = nil
	}
}

// activityLen: an activity is one short line on a task card.
const activityLen = 40

// Activity says in a few words what a tool call is doing: "running tests", "reading tasks.go", "waiting for CI". Shown on the task cards
// and agent rows while an agent only runs tools (docs/assistant/unified-entry.md "Task cards").
func Activity(name string, input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string { v, _ := in[k].(string); return strings.TrimSpace(v) }
	base := func(k string) string {
		if p := str(k); p != "" {
			return path.Base(p)
		}
		return ""
	}
	var s string
	switch name {
	case "Bash", "shell", "exec_command":
		s = commandActivity(str("command"), str("description"))
	case "Read":
		s = join("在读", base("file_path"))
	case "Edit", "MultiEdit":
		s = join("在改", base("file_path"))
	case "Write":
		s = join("在写", base("file_path"))
	case "NotebookEdit":
		s = join("在改", base("notebook_path"))
	case "Grep":
		s = join("在搜", str("pattern"))
	case "Glob":
		s = join("在找文件", str("pattern"))
	case "LS":
		s = join("在看目录", base("path"))
	case "WebFetch":
		s = "在看网页"
		if u, err := url.Parse(str("url")); err == nil && u.Host != "" {
			s = join(s, u.Host)
		}
	case "WebSearch":
		s = join("在搜网页", str("query"))
	case "Task", "Agent":
		s = join("在派子任务", str("description"))
	case "subagent": // Codex's subagent activity (codex.go)
		switch str("event") {
		case "interacted":
			s = join("在联系子任务", str("name"))
		case "completed":
			s = join("子任务完成", str("name"))
		default:
			s = join("在派子任务", str("name"))
		}
	case "TodoWrite", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet":
		s = "在更新计划"
	case "Skill":
		s = join("在用技能", str("skill"))
	case "AskUserQuestion":
		s = "在问你问题"
	case "ExitPlanMode":
		s = "在等你批准计划"
	case "ScheduleWakeup", "Monitor", "Sleep", "sleep", "wait_agent":
		s = "在等待"
	case "BashOutput", "TaskOutput":
		s = "在等后台任务"
	case "KillShell", "KillBash", "TaskStop":
		s = "在停后台任务"
	case "ToolSearch":
		s = "在找工具"
	case "LSP":
		s = "在查代码"
	default:
		tool := name
		if strings.HasPrefix(tool, "mcp__") {
			if i := strings.LastIndex(tool, "__"); i > 4 {
				tool = tool[i+2:]
			}
		}
		s = join("在调用", tool)
	}
	return clipActivity(s)
}

// commandRule: a shell command matching re is doing what says.
type commandRule struct {
	re   *lazyre.Regexp
	says string
}

// commandRules, first match wins: waiting for CI before tests (gh run watch), tests before builds (xcodebuild test,
// go build && go test), releases before builds (release.sh archives).
var commandRules = []commandRule{
	{lazyre.New(`\bgh\s+(pr\s+checks|run\s+(watch|view|list))\b|statuscheckrollup|\bgh\s+pr\s+view\b.*\bchecks\b`), "在等 CI"},
	{lazyre.New(`\bgh\s+pr\s+create\b`), "在提 PR"},
	{lazyre.New(`\bgh\s+pr\s+merge\b`), "在合并 PR"},
	{lazyre.New(`\b(release|publish)\.(sh|py)\b|\b(npm|cargo)\s+publish\b|\basc_testflight\.py\b`), "在发版"},
	{lazyre.New(`\bdeploy\.sh\b|\bkubectl\s+(apply|rollout)\b|\bwrangler\s+deploy\b`), "在部署"},
	{lazyre.New(`\b(go|cargo|swift|mvn|dotnet|deno|bun)\s+test\b|\b(npm|pnpm|yarn)\s+(run\s+)?test\b|\bpytest\b|\bjest\b|\bvitest\b|\bxcodebuild\b.*\btest(-without-building)?\b|\bgradlew\b.*\b(test|check|connected\w*test)\b|\btest[-_][\w-]*\.sh\b|\bscripts/test/|\bmake\s+(test|check)\b|\bctest\b|\brspec\b|\bphpunit\b|\bunittest\b`), "在跑测试"},
	{lazyre.New(`\b(golangci-lint|eslint|swiftlint|ruff|flake8|mypy|shellcheck|prettier\s+--check)\b|\b(go|cargo)\s+(vet|clippy)\b|\b(npm|pnpm|yarn)\s+(run\s+)?lint\b`), "在检查代码"},
	{lazyre.New(`\b(go|cargo|swift)\s+build\b|\bxcodebuild\b|\bxcodegen\b|\bgradlew\b|\b(npm|pnpm|yarn)\s+(run\s+)?build\b|\btsc\b|\bmake\b|\bcmake\b|\bdocker\s+build\b`), "在编译"},
	{lazyre.New(`\b(npm|pnpm|yarn|bun)\s+(install|ci|add)\b|\bpip3?\s+install\b|\bbrew\s+install\b|\bgo\s+(mod|get)\b|\bpod\s+install\b`), "在装依赖"},
	{lazyre.New(`\bgit\s+push\b`), "在推送代码"},
	{lazyre.New(`\bgit\s+commit\b`), "在提交代码"},
	{lazyre.New(`\bgit\s+(rebase|merge|pull|fetch|cherry-pick)\b`), "在同步代码"},
	{lazyre.New(`\bgit\s+(diff|log|show|status|blame)\b`), "在看改动"},
	{lazyre.New(`^\s*(sleep|until|while)\b|\bwait\b`), "在等待"},
	{lazyre.New(`\bsimctl\b`), "在用模拟器"},
	{lazyre.New(`\b(rg|grep|ag|find)\s`), "在搜代码"},
	{lazyre.New(`^\s*(cat|head|tail|less|sed\s+-n|bat)\s`), "在读文件"},
}

// commandActivity is what a shell command is doing: a kind of work when it is a known one, else the command itself
// (or the description the agent gave it).
func commandActivity(cmd, desc string) string {
	low := strings.ToLower(cmd)
	for _, r := range commandRules {
		if r.re.MatchString(low) {
			return r.says
		}
	}
	if desc != "" {
		return join("在跑", actLine(desc))
	}
	if c := actLine(cmd); c != "" {
		return join("在跑", c)
	}
	return "在跑命令"
}

func join(verb, what string) string {
	if what = actLine(what); what == "" {
		return verb
	}
	return verb + " " + what
}

// actLine is the first non-blank line of s, its spaces collapsed.
func actLine(s string) string {
	return strings.Join(strings.Fields(oneLine(s, 200)), " ")
}

func clipActivity(s string) string {
	r := []rune(s)
	if len(r) > activityLen {
		return string(r[:activityLen-1]) + "…"
	}
	return s
}

// activityPhrases are Activity's fixed lines, activityVerbs its verbs ("reading tasks.go", or alone), by the key of their
// words in the hub's transcript texts (i18n hub-transcript_text.<key>; a verb with what it acts on: <key>_what).
var (
	activityPhrases = map[string]string{
		"在更新计划": "act_plan", "在问你问题": "act_asking", "在等你批准计划": "act_plan_approval", "在等待": "act_waiting",
		"在等后台任务": "act_waiting_task", "在停后台任务": "act_stopping_task", "在找工具": "act_finding_tools", "在查代码": "act_lsp",
		"在等 CI": "act_ci", "在提 PR": "act_pr_create", "在合并 PR": "act_pr_merge", "在发版": "act_release", "在部署": "act_deploy",
		"在跑测试": "act_test", "在检查代码": "act_lint", "在编译": "act_build", "在装依赖": "act_install", "在推送代码": "act_push",
		"在提交代码": "act_commit", "在同步代码": "act_sync", "在看改动": "act_diff", "在用模拟器": "act_simulator",
		"在搜代码": "act_search_code", "在读文件": "act_read_files", "在跑命令": "act_command",
	}
	activityVerbs = map[string]string{
		"在读": "act_read", "在改": "act_edit", "在写": "act_write", "在搜": "act_grep", "在找文件": "act_glob", "在看目录": "act_ls",
		"在看网页": "act_fetch", "在搜网页": "act_web_search", "在派子任务": "act_subtask", "在联系子任务": "act_subtask_message",
		"子任务完成": "act_subtask_done", "在用技能": "act_skill", "在调用": "act_tool",
		"在跑": "act_run",
	}
)

// ActivityKey is the key of an activity line's words in the hub's transcript texts (i18n hub-transcript_text.<key>):
// one of its fixed lines, or a verb alone (verb: with what it acts on, <key>_what). "" for one it does not know (an
// older or newer node's wording).
func ActivityKey(s string) (key string, verb bool) {
	if key, ok := activityPhrases[s]; ok {
		return key, false
	}
	key, ok := activityVerbs[s]
	return key, ok
}
