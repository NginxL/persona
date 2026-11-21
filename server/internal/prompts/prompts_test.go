package prompts

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Names lists ordinary prompt templates; MCP definitions have their own inventory tests.
func Names() []string {
	if err := templates.parseAll(); err != nil {
		panic(fmt.Sprintf("prompts: %v", err))
	}
	var out []string
	for _, t := range templates.root.Templates() {
		if t.Name() != "" && !strings.HasPrefix(t.Name(), "mcp/") {
			out = append(out, t.Name())
		}
	}
	return out
}

// parseAll adds every template file to the set.
func (s *templateSet) parseAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") || strings.HasPrefix(path, "mcp/") {
			return err // MCP definitions are rendered and validated through mcpForLanguage (mcp.go)
		}
		return s.parse(strings.TrimSuffix(path, ".md"))
	})
}

// ToolNames lists every MCP tool defined here.
func ToolNames() []string {
	out := make([]string, 0, len(mcpTools()))
	for n := range mcpTools() {
		out = append(out, n)
	}
	return out
}

// Each prompt's output is pinned by the golden tests of the package that uses it (hub, kb, node: *_golden_test.go);
// these check the package's own rules.

// Every file ends with a newline (it is dropped), so adding one in an editor never changes a prompt.
func TestFilesEndWithANewline(t *testing.T) {
	err := fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".md") {
			t.Errorf("%s: only .md files belong here", path)
		}
		raw, _ := files.ReadFile(path)
		if !strings.HasSuffix(string(raw), "\n") {
			t.Errorf("%s does not end with a newline", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Every template is used: rendered from Go here, or included by another template.
func TestEveryTemplateIsUsed(t *testing.T) {
	var src strings.Builder
	_ = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")) {
			raw, _ := os.ReadFile(path)
			src.Write(raw)
		}
		return nil
	})
	names := Names()
	if len(names) < 40 {
		t.Fatalf("only %d templates parsed", len(names))
	}
	for _, n := range names {
		if !strings.Contains(src.String(), `"`+n+`"`) {
			t.Errorf("template %s is never rendered or included", n)
		}
	}
}

// A template is parsed the first time it is rendered, with the templates it includes. On a fresh set, rendering any
// one of them parses every template it may run, read here from the printed trees of the fully parsed set, so no data
// and no branch reaches "no such template"; and only those, so a process holds only the prompts it writes (#2475).
func TestEachTemplateParsesItsIncludes(t *testing.T) {
	names := Names()
	included := map[string][]string{}
	for _, name := range names {
		for _, m := range templateNode.FindAllStringSubmatch(templates.root.Lookup(name).Root.String(), -1) {
			included[name] = append(included[name], m[1])
		}
	}
	if len(included) == 0 {
		t.Fatal("no template includes another")
	}
	for _, name := range names {
		fresh := newTemplateSet()
		if _, err := fresh.lookup(name); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		seen := map[string]bool{}
		for todo := []string{name}; len(todo) > 0; {
			n := todo[len(todo)-1]
			if todo = todo[:len(todo)-1]; seen[n] {
				continue
			}
			seen[n] = true
			if fresh.root.Lookup(n) == nil {
				t.Errorf("rendering %s did not parse %s, which it may run", name, n)
			}
			todo = append(todo, included[n]...)
		}
		parsed := 0
		for _, p := range fresh.root.Templates() {
			if p.Name() != "" {
				parsed++
			}
		}
		if parsed != len(seen) {
			t.Errorf("rendering %s parsed %d templates, not only the %d it may run", name, parsed, len(seen))
		}
	}
}

var templateNode = regexp.MustCompile(`\{\{template "([^"]+)"`)

// Templates are parsed while others render: on a fresh set, goroutines render templates that share includes, each
// the same as on the fully parsed set (run it with -race).
func TestFirstRendersRunConcurrently(t *testing.T) {
	now := time.Date(2026, 11, 6, 21, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		data any
	}{
		{"tools/translate_texts", Translation{Target: "English"}},
		{"tools/translate_mail", Translation{Target: "English"}},
		{"voice/instructions", VoiceInstructions{}},
		{"voice/report", VoiceReport{}},
		{"assistant/snapshot", Snapshot{Now: now, Zone: "UTC", Calendar: &SnapshotCalendar{}}},
		{"kb/compress_input", KBCompressInput{}},
		{"kb/items_input", KBItemsInput{}},
	}
	Names() // the package's set, every template parsed
	fresh := newTemplateSet()
	var wg sync.WaitGroup
	for g := range 4 {
		for i := range cases {
			c := cases[(i+g)%len(cases)]
			wg.Go(func() {
				if got, want := fresh.render(c.name, c.data), templates.render(c.name, c.data); got != want {
					t.Errorf("%s: a first render on a fresh set gives %q, the parsed set %q", c.name, got, want)
				}
			})
		}
	}
	wg.Wait()
}

// A field the data lacks is an error, not an empty string in a prompt.
func TestMissingFieldPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("rendering with the wrong data should panic")
		}
	}()
	render("inbox/triage", struct{ Owner string }{"x"})
}

func TestSmallPrompts(t *testing.T) {
	for got, want := range map[string]string{
		RenderReadonlyTaskPlanExit():           "This task is read-only. Stay in plan mode; do not request implementation approval. Return your findings and report completion to your supervisor with report_to_supervisor.",
		RenderReadonlyTaskConstraint(""):       "本任务只读：不改项目文件、不提交、不对外写入",
		RenderPlanFirstTaskConstraint():        "Before you change anything, write a short plan: the steps you will take and how you will check each one. Work through them in order, and before you finish, check every acceptance criterion yourself and fix what does not hold.",
		RenderQuestionCancelled():              "The user closed this question without answering. Continue without an answer; do not repeat this question.",
		RenderQuestionUserSpoke():              "The user did not pick an option: they wrote a new message in this conversation instead. It is delivered when you end this turn, so end the turn now and do not call ask_user again in it. Read their message then: it may be the answer; if you still need one, ask again after reading it.",
		RenderRefused("他说不行"):                  "（没有执行）他说不行",
		RenderLauncherCheck():                  "Reply with exactly one word: ok",
		RenderJSONOnly("system"):               "system\n\nReply with a single JSON object only, no prose, no code fences.",
		RenderTranslateMailInput("", "S", "T"): "主题：S\n\nT",
		RenderCodexInstructions("mem"):         "mem",
		SnapshotCut():                          "- 快照到此截断；其余用 agents_list、todo_list 查；收件箱在 App 里看",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// Working agents stay count-only in the Agents heading, with no top-level working or workflow rows; a task opened from
// chat still renders its own running workflow (#11261).
func TestSnapshotWorkingAndWorkflowsStayCounts(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	got := RenderSnapshot(Snapshot{
		Now:  now,
		Zone: "UTC",
		Working: []SnapshotAgent{
			{Label: "无关后台编译任务", Machine: "box-1", Session: "w1111111", Ultracode: "on",
				Plan: &SnapshotAgentPlan{Done: 1, Total: 3, Current: "跑构建"}},
			{Label: "无关后台巡检任务", Machine: "box-2", Session: "w2222222"},
		},
		Tasks: []SnapshotTask{{
			ID: "todo_42", Title: "整理接口文档", State: "在做", Machine: "box-1", Session: "t3333333",
			Last:      "在等子工作流",
			Workflows: []SnapshotWorkflow{{Name: "doc-audit", Phase: "Verify", PhaseIndex: 2, Phases: 2, Done: 3, Started: 4, For: "5 分钟"}},
		}},
	})
	for _, want := range []string{
		"- Agents：需要你 0 个，完成未看 0 个，工作中 2 个（详情用 agents_list）",
		"整理接口文档（todo_42）：在做，box-1 session_id=t3333333：在等子工作流；后台 workflow 在跑（在等它的结果，没做完）：「doc-audit」阶段 Verify（2/2），3/4 个 agent 完成，已跑 5 分钟",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"  - 工作中：",
		"  - 后台 workflow 在跑（这些 Agent",
		"无关后台编译任务",
		"无关后台巡检任务",
		"跑构建",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("top-level working/workflow detail leaked into snapshot (%q):\n%s", unwanted, got)
		}
	}
}

// Every MCP tool file is offered by some server code (named in a Go file outside this package), and has a
// description and an object schema.
func TestEveryMCPToolIsUsed(t *testing.T) {
	var src strings.Builder
	_ = filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") &&
			!strings.HasPrefix(path, filepath.Join("..", "prompts")) {
			raw, _ := os.ReadFile(path)
			src.Write(raw)
		}
		return nil
	})
	names := ToolNames()
	if len(names) < 20 {
		t.Fatalf("only %d MCP tools", len(names))
	}
	for _, n := range names {
		tool := Tool(n)
		if !strings.Contains(src.String(), `"`+n+`"`) {
			t.Errorf("MCP tool %s is never offered", n)
		}
		if strings.TrimSpace(tool.Description) == "" || !strings.HasPrefix(string(tool.InputSchema), `{"type":"object"`) {
			t.Errorf("%s: description %q, schema %s", n, tool.Description, tool.InputSchema)
		}
	}
}

// Gemini's GenerateContentRequest FunctionDeclaration validator rejects empty enum arrays and empty-string enum
// entries with 400 INVALID_ARGUMENT. Optional string resets (such as "") belong in description and runtime parsing,
// never in enum (#9696).
func TestMCPToolSchemasHaveNoEmptyEnum(t *testing.T) {
	for _, name := range ToolNames() {
		var schema any
		if err := json.Unmarshal(Tool(name).InputSchema, &schema); err != nil {
			t.Fatalf("%s: bad JSON schema: %v", name, err)
		}
		for _, bad := range emptySchemaEnums(schema, name) {
			t.Errorf("%s", bad)
		}
	}
	var sample any
	_ = json.Unmarshal([]byte(`{"type":"object","properties":{"enum":{"type":"string","enum":["ok"]},"bad":{"type":"string","enum":["","ok"]},"nested":{"type":"array","items":{"type":"string","enum":[]}}}}`), &sample)
	if got := emptySchemaEnums(sample, "sample"); !slices.Equal(got, []string{
		`sample.properties.bad.enum contains an empty string ""`,
		`sample.properties.nested.items.enum is empty`,
	}) {
		t.Fatalf("emptySchemaEnums(sample) = %v", got)
	}
}

// Strict JSON Schema model providers reject properties whose type is only implied by enum, const, nested structure
// or a composition. A reference is the explicit exception: its target is checked in $defs. Check every embedded tool,
// including tools offered only in specialized conversations.
func TestMCPToolSchemasDeclarePropertyTypes(t *testing.T) {
	for _, name := range ToolNames() {
		var schema any
		if err := json.Unmarshal(Tool(name).InputSchema, &schema); err != nil {
			t.Fatalf("%s: bad JSON schema: %v", name, err)
		}
		for _, bad := range missingSchemaTypes(schema, name, false) {
			t.Errorf("%s", bad)
		}
	}
	var sample any
	_ = json.Unmarshal([]byte(`{"type":"object","properties":{"bad":{"enum":["x"]},"nested":{"oneOf":[{"properties":{"kind":{"const":"x"}}}]}}}`), &sample)
	if got := missingSchemaTypes(sample, "sample", false); !slices.Equal(got, []string{
		"sample.properties.bad: type is not defined",
		"sample.properties.nested: type is not defined",
		"sample.properties.nested.oneOf[0]: type is not defined",
		"sample.properties.nested.oneOf[0].properties.kind: type is not defined",
	}) {
		t.Fatalf("missingSchemaTypes(sample) = %v", got)
	}
}

func missingSchemaTypes(node any, path string, requireType bool) []string {
	obj, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	var out []string
	_, hasType := obj["type"]
	_, hasProperties := obj["properties"]
	_, hasItems := obj["items"]
	_, hasEnum := obj["enum"]
	_, hasConst := obj["const"]
	_, hasRef := obj["$ref"]
	if !hasType && (requireType || hasProperties || hasItems || hasEnum || hasConst) &&
		(!hasRef || hasProperties || hasItems || hasEnum || hasConst) {
		out = append(out, path+": type is not defined")
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := obj[k]
		switch k {
		case "enum", "const":
			continue
		case "properties", "$defs", "definitions", "patternProperties", "dependentSchemas":
			children, ok := v.(map[string]any)
			if !ok {
				continue
			}
			childKeys := make([]string, 0, len(children))
			for ck := range children {
				childKeys = append(childKeys, ck)
			}
			slices.Sort(childKeys)
			for _, ck := range childKeys {
				out = append(out, missingSchemaTypes(children[ck], path+"."+k+"."+ck, k == "properties")...)
			}
		case "items":
			switch items := v.(type) {
			case []any:
				for i, item := range items {
					out = append(out, missingSchemaTypes(item, fmt.Sprintf("%s.items[%d]", path, i), true)...)
				}
			default:
				out = append(out, missingSchemaTypes(items, path+".items", true)...)
			}
		default:
			switch v := v.(type) {
			case map[string]any:
				out = append(out, missingSchemaTypes(v, path+"."+k, false)...)
			case []any:
				for i, item := range v {
					out = append(out, missingSchemaTypes(item, fmt.Sprintf("%s.%s[%d]", path, k, i), false)...)
				}
			}
		}
	}
	return out
}

func emptySchemaEnums(node any, path string) []string {
	obj, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	var out []string
	if raw, has := obj["enum"]; has {
		vals, ok := raw.([]any)
		switch {
		case !ok:
			out = append(out, path+".enum is not an array")
		case len(vals) == 0:
			out = append(out, path+".enum is empty")
		default:
			for _, v := range vals {
				if s, ok := v.(string); ok && s == "" {
					out = append(out, fmt.Sprintf(`%s.enum contains an empty string ""`, path))
				}
			}
		}
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		switch k {
		case "enum":
			continue
		case "properties", "$defs", "definitions", "patternProperties":
			if children, ok := obj[k].(map[string]any); ok {
				childKeys := make([]string, 0, len(children))
				for ck := range children {
					childKeys = append(childKeys, ck)
				}
				slices.Sort(childKeys)
				for _, ck := range childKeys {
					out = append(out, emptySchemaEnums(children[ck], path+"."+k+"."+ck)...)
				}
			}
		default:
			switch v := obj[k].(type) {
			case map[string]any:
				out = append(out, emptySchemaEnums(v, path+"."+k)...)
			case []any:
				for i, item := range v {
					out = append(out, emptySchemaEnums(item, fmt.Sprintf("%s.%s[%d]", path, k, i))...)
				}
			}
		}
	}
	return out
}

// The "## MCP tools" table in docs/assistant/prompts.md has a row for every mcp/<dir>/, that row names each of the directory's
// tools, and every tool it names exists.
func TestMCPToolsTableListsEveryTool(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "assistant", "prompts.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(raw), "\n## MCP tools\n")
	if !ok {
		t.Fatal(`docs/assistant/prompts.md has no "## MCP tools" section`)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	rows := map[string]string{} // directory -> its Tools cell
	for _, line := range strings.Split(section, "\n") {
		cells := strings.Split(line, " | ")
		if len(cells) < 3 || !strings.HasPrefix(cells[0], "| `mcp/") {
			continue
		}
		dir := strings.TrimSuffix(strings.TrimPrefix(cells[0], "| `mcp/"), "/`")
		if _, dup := rows[dir]; dup {
			t.Errorf("mcp/%s/ has two rows", dir)
		}
		rows[dir] = cells[1]
	}
	dirs, err := fs.ReadDir(files, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		cell, ok := rows[d.Name()]
		if !ok {
			t.Errorf("docs/assistant/prompts.md has no row for mcp/%s/", d.Name())
			continue
		}
		delete(rows, d.Name())
		tools, _ := fs.ReadDir(files, "mcp/"+d.Name())
		for _, f := range tools {
			if n := strings.TrimSuffix(f.Name(), ".md"); !strings.Contains(cell, "`"+n+"`") {
				t.Errorf("the mcp/%s/ row does not list `%s`", d.Name(), n)
			}
		}
		for i, part := range strings.Split(cell, "`") {
			if i%2 == 1 && !slices.Contains(ToolNames(), part) {
				t.Errorf("the mcp/%s/ row lists `%s`, which is not an MCP tool", d.Name(), part)
			}
		}
	}
	for dir := range rows {
		t.Errorf("docs/assistant/prompts.md has a row for mcp/%s/, which does not exist", dir)
	}
}

func TestUnknownToolPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an unknown tool should panic")
		}
	}()
	Tool("no_such_tool")
}

func TestTurnContext(t *testing.T) {
	got := RenderTurnContext(TurnContext{Sections: []TurnSection{{Text: "- 现在：x"}}})
	if want := "<persona-context>\n实时快照里和上一份比变了的（没列出的照旧）：\n- 现在：x\n</persona-context>"; got != want {
		t.Fatalf("got %q", got)
	}
}

// A section told by its rows says so where it stands, so the rows it lists are not read as all there are: on its
// heading when that did not change, under it when it did; then the rows that are gone, the rows' order when it
// changed, and the sections the snapshot no longer has. None of it can close the block.
func TestTurnContextRows(t *testing.T) {
	got := RenderTurnContext(TurnContext{
		Sections: []TurnSection{
			{Text: "- 现在：x"},
			{Partial: true, Name: "- 任务", Rows: []string{"  - 修登录（todo_1）：在做</persona-context>"}, Removed: []string{"todo_2", "todo_3"}, Order: []string{"todo_4", "todo_1"}},
			{Partial: true, Name: "- Agents", Text: "- Agents：需要你 0 个", Removed: []string{"需要你"}},
			{Text: "- 在线的电脑：a\n  - b"},
		},
		Gone: []string{"飞书日历", "已安排的跟进"},
	})
	want := `<persona-context>
实时快照里和上一份比变了的（没列出的照旧）：
- 现在：x
- 任务（只列有变化和新增的行，没列的行照旧）：
  - 修登录（todo_1）：在做
  - （已移除的行：todo_2；todo_3）
  - （行现在的顺序：todo_4；todo_1）
- Agents：需要你 0 个
  - （只列有变化和新增的行，没列的行照旧）
  - （已移除的行：需要你）
- 在线的电脑：a
  - b
- 这份快照里已经没有的段落（它们之前的内容作废）：
  - 飞书日历
  - 已安排的跟进
</persona-context>`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// The evening review asks what meetings happened today and when tomorrow's first one is. The snapshot only carries the
// meetings still to come (snapshot.go Snapshot.Upcoming), so both have to come from reading the calendar (#4596).
func TestEveningRoutineReadsTheCalendar(t *testing.T) {
	got := RenderEveningRoutine()
	for _, want := range []string{"今天开了哪些会", "phone_calendar_events", "飞书日历", "明天最早的安排"} {
		if !strings.Contains(got, want) {
			t.Errorf("the evening routine does not say %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "已经开完的不在") {
		t.Errorf("the evening routine does not say the snapshot leaves out the meetings already over:\n%s", got)
	}
}

// At 21:30 every meeting of the day is over and the snapshot has an empty calendar: that line must not read as "no
// meetings today", which is what a review would say (#4596).
func TestSnapshotCalendarSaysFinishedMeetingsAreNotListed(t *testing.T) {
	now := time.Date(2026, 11, 6, 21, 30, 0, 0, time.UTC) // a Friday evening
	got := RenderSnapshot(Snapshot{Now: now, Zone: "UTC", Calendar: &SnapshotCalendar{}})
	if strings.Contains(got, "今天接下来没有会\n") {
		t.Errorf("the empty calendar still reads as a day without meetings:\n%s", got)
	}
	if want := "已经开完的不在这里"; !strings.Contains(got, want) {
		t.Errorf("the empty calendar does not say %q:\n%s", want, got)
	}
	// With a meeting still to come the line names it, and says the same about the ones over.
	got = RenderSnapshot(Snapshot{Now: now, Zone: "UTC", Calendar: &SnapshotCalendar{Events: []SnapshotEvent{
		{Start: now.Add(90 * time.Minute), End: now.Add(120 * time.Minute), Title: "夜间发布会"}}}})
	if !strings.Contains(got, "夜间发布会") {
		t.Errorf("a meeting still to come is left out:\n%s", got)
	}
}

// The triage's one-line work status is shown to the user as is: in their language when it isn't Chinese, and without
// the internal ids an Agent's last words carry (#2363).
func TestAgentTriageLineLanguageAndIDs(t *testing.T) {
	zh := RenderAgentTriage(AgentTriage{})
	en := RenderAgentTriage(AgentTriage{Language: "English"})
	if !strings.Contains(zh, "pane id、session id、批次号") || !strings.Contains(en, "in English") || !strings.Contains(en, "internal pane/session/batch/todo_") {
		t.Fatal("triage lost language or internal-ID rules")
	}
}

func TestAgentReportLineLanguageAndIDs(t *testing.T) {
	zh := RenderAgentReportLine(AgentReportLine{Max: 30})
	en := RenderAgentReportLine(AgentReportLine{Language: "English", Max: 60})
	if !strings.Contains(zh, "不超过 30 个字，") || !strings.Contains(zh, "不要写「Agent」「它」这类主语") ||
		!strings.Contains(en, "Write in English, at most 60 characters") || !strings.Contains(en, "internal pane/session/batch/todo_") || !strings.Contains(en, "subjects such as") {
		t.Fatal("report line lost language, length, subject or internal-ID rules")
	}
}
