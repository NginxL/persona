package node

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// The words a block tells a section's rows with (prompts/assistant/turn_context.md), as a reader of the block meets them.
const (
	rowsMark    = "（只列有变化和新增的行，没列的行照旧）"
	rowsRemoved = "  - （已移除的行："
	rowsOrder   = "  - （行现在的顺序："
	goneSection = "- 这份快照里已经没有的段落"
	deltaIntro  = "实时快照里和上一份比变了的（没列出的照旧）："
	fullIntro   = "实时快照（这条消息发出时的；之后的变化用工具查）："
	tasksHead   = "- 优先显示的开放任务详情（含个人待办；最近结束的对话任务也会列出；task_plan；问已有结果看这里，不够用 agent_read 读它的对话；要它新查、新做或改方向才用 agent_send）："
	tasksKey    = "- 优先显示的开放任务详情"
)

func deltaBlock(lines ...string) string {
	return "<persona-context>\n" + deltaIntro + "\n" + strings.Join(lines, "\n") + "\n</persona-context>"
}

// readBlock is what a conversation holds of the snapshot (section key → text) once it has read block, going only by
// what the block says: a whole snapshot starts over, a section replaces the one it had, a section told by its rows
// changes those rows, and the sections named gone are dropped. It fails on a block that cannot be followed: a row
// taken away that was never there, an order that is not of the rows it has.
func readBlock(t *testing.T, view map[string]string, block string) map[string]string {
	t.Helper()
	body, ok := strings.CutPrefix(block, "<persona-context>\n")
	body, ok2 := strings.CutSuffix(body, "\n</persona-context>")
	intro, body, _ := strings.Cut(body, "\n")
	switch {
	case !ok || !ok2:
		t.Fatalf("not a block:\n%s", block)
	case intro == fullIntro:
		view = map[string]string{}
	case intro != deltaIntro || view == nil:
		t.Fatalf("a block of changes for a conversation that has %d sections starts %q", len(view), intro)
	}
	for _, s := range snapshotSections(body) {
		head, rows := sectionRows(s.text)
		if s.key == goneSection {
			for _, r := range rows {
				key := listMark + strings.TrimPrefix(strings.TrimSpace(r), listMark)
				if _, had := view[key]; !had {
					t.Fatalf("%q is told gone and was never there:\n%s", key, block)
				}
				delete(view, key)
			}
			continue
		}
		name, unchanged := strings.CutSuffix(head, rowsMark+"：")
		switch {
		case unchanged:
			head = ""
		case len(rows) > 0 && rows[0] == "  - "+rowsMark:
			name, rows = s.key, rows[1:]
		default:
			view[s.key] = s.text
			continue
		}
		old, had := view[name]
		id := rowIDs[name]
		if !had || id == nil {
			t.Fatalf("%q is told by its rows and the conversation has no such rows:\n%s", name, block)
		}
		oldHead, have := sectionRows(old)
		if head == "" {
			head = oldHead
		}
		at := func(k string) int { return slices.IndexFunc(have, func(r string) bool { return id(r) == k }) }
		var order []string
		for _, r := range rows {
			if list, ok := strings.CutPrefix(r, rowsRemoved); ok {
				for k := range strings.SplitSeq(strings.TrimSuffix(list, "）"), "；") {
					i := at(k)
					if i < 0 {
						t.Fatalf("%s: row %q is removed and was never there:\n%s", name, k, block)
					}
					have = slices.Delete(have, i, i+1)
				}
			} else if list, ok := strings.CutPrefix(r, rowsOrder); ok {
				order = strings.Split(strings.TrimSuffix(list, "）"), "；")
			} else if i := at(id(r)); i >= 0 {
				have[i] = r
			} else {
				have = append(have, r)
			}
		}
		if order != nil {
			sorted := make([]string, 0, len(have))
			for _, k := range order {
				if i := at(k); i >= 0 {
					sorted = append(sorted, have[i])
				}
			}
			if len(sorted) != len(have) || len(order) != len(have) {
				t.Fatalf("%s: the order %v is not of its %d rows:\n%s", name, order, len(have), block)
			}
			have = sorted
		}
		view[name] = strings.Join(append([]string{head}, have...), "\n")
	}
	return view
}

// sameAsSnapshot fails unless view, in the snapshot's order, is the snapshot byte for byte (less its blank lines,
// which no block carries).
func sameAsSnapshot(t *testing.T, view map[string]string, snapshot, block string) {
	t.Helper()
	var lines []string
	for line := range strings.SplitSeq(snapshot, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	snapshot = strings.Join(lines, "\n")
	sections := snapshotSections(snapshot)
	texts := make([]string, 0, len(sections))
	for _, s := range sections {
		texts = append(texts, view[s.key])
	}
	if got := strings.Join(texts, "\n"); got != snapshot || len(view) != len(sections) {
		t.Fatalf("after the block the conversation has %d sections:\n%s\n\nthe snapshot has %d:\n%s\n\nthe block:\n%s", len(view), got, len(sections), snapshot, block)
	}
}

func snapshotOf(sections ...string) string { return strings.Join(sections, "\n") }

func tasksOf(rows ...string) string { return strings.Join(append([]string{tasksHead}, rows...), "\n") }

const (
	rowLogin  = "  - 修登录（todo_1）：在做，server session_id=aaaaaaaa：在跑测试"
	rowDocs   = "  - 写文档（todo_2，链接 http://hub.example.com/todos?id=todo_2）：停着：等评审"
	rowDeploy = "  - 部署：灰度（先 10%）（todo_3）：做完了（用户还没看）"
)

// One task's row changed: that row is all the block says of the tasks, on a heading that says the others stand.
func TestContextDeltaSendsTheChangedRowOnly(t *testing.T) {
	now := "- 现在：2026-11-01 周日 15:04（Asia/Shanghai）"
	before := snapshotOf(now, "- 在线的电脑：服务器（server）", tasksOf(rowLogin, rowDocs, rowDeploy), "- 已安排的跟进：没有")
	_, seen := contextDelta(nil, before)
	changed := strings.Replace(rowLogin, "在跑测试", "测试过了，在开 PR", 1)
	block, seen := contextDelta(seen, strings.Replace(before, rowLogin, changed, 1))
	if want := deltaBlock(now, tasksKey+rowsMark+"：", changed); block != want {
		t.Fatalf("one changed row:\n%s\nwant\n%s", block, want)
	}

	// A row gone and a new one: the new row, and the id of the one that is gone.
	rowNew := "  - 查日志（todo_4）：在做"
	block, seen = contextDelta(seen, snapshotOf(now, "- 在线的电脑：服务器（server）", tasksOf(changed, rowDeploy, rowNew), "- 已安排的跟进：没有"))
	if want := deltaBlock(now, tasksKey+rowsMark+"：", rowNew, rowsRemoved+"todo_2）"); block != want {
		t.Fatalf("a row removed, a row added:\n%s\nwant\n%s", block, want)
	}

	// The same rows in another order: only the order. A new row that is not the last: the row and the order.
	block, seen = contextDelta(seen, snapshotOf(now, "- 在线的电脑：服务器（server）", tasksOf(rowDeploy, changed, rowNew), "- 已安排的跟进：没有"))
	if want := deltaBlock(now, tasksKey+rowsMark+"：", rowsOrder+"todo_3；todo_1；todo_4）"); block != want {
		t.Fatalf("rows reordered:\n%s\nwant\n%s", block, want)
	}
	block, seen = contextDelta(seen, snapshotOf(now, "- 在线的电脑：服务器（server）", tasksOf(rowDocs, rowDeploy, changed, rowNew), "- 已安排的跟进：没有"))
	if want := deltaBlock(now, tasksKey+rowsMark+"：", rowDocs, rowsOrder+"todo_2；todo_3；todo_1；todo_4）"); block != want {
		t.Fatalf("a new first row:\n%s\nwant\n%s", block, want)
	}

	// The section is gone from the snapshot, and so is another: both are named, and seen forgets them, so one that
	// comes back is told whole.
	block, seen = contextDelta(seen, snapshotOf(now, "- 在线的电脑：服务器（server）"))
	if want := deltaBlock(now, goneSection+"（它们之前的内容作废）：", "  - 优先显示的开放任务详情", "  - 已安排的跟进"); block != want {
		t.Fatalf("sections gone:\n%s\nwant\n%s", block, want)
	}
	block, _ = contextDelta(seen, snapshotOf(now, "- 在线的电脑：服务器（server）", tasksOf(rowDocs)))
	if want := deltaBlock(now, tasksOf(rowDocs)); block != want {
		t.Fatalf("a section that came back:\n%s\nwant\n%s", block, want)
	}
}

// The Agents rows go by their label. A heading that changed is sent as it is now, with the note under it; rows whose
// label is gone are named by it.
func TestContextDeltaAgentRowsByLabel(t *testing.T) {
	now := "- 现在：2026-11-01 周日 15:04（Asia/Shanghai）"
	needs := "  - 需要你：修登录（server，session_id=aaaaaaaa）：等你确认迁移"
	unread := "  - 完成未看：写文档（server，session_id=cccccccc）；计划 2/5"
	waiting := "  - 在等的（hub 到时会叫醒它们，不用管）：跑回归（server，session_id=bbbbbbbb）：等 CI"
	head := "- Agents：需要你 1 个，完成未看 1 个，工作中 2 个（详情用 agents_list）"
	_, seen := contextDelta(nil, snapshotOf(now, head+"\n"+needs+"\n"+unread+"\n"+waiting))

	moved := strings.Replace(unread, "计划 2/5", "计划 3/5", 1)
	block, seen := contextDelta(seen, snapshotOf(now, head+"\n"+needs+"\n"+moved+"\n"+waiting))
	if want := deltaBlock(now, "- Agents"+rowsMark+"：", moved); block != want {
		t.Fatalf("one changed row:\n%s\nwant\n%s", block, want)
	}

	answered := "- Agents：需要你 0 个，完成未看 2 个，工作中 3 个（详情用 agents_list）"
	two := moved + "；查日志（server，session_id=dddddddd）"
	block, _ = contextDelta(seen, snapshotOf(now, answered+"\n"+two+"\n"+waiting))
	if want := deltaBlock(now, answered, "  - "+rowsMark, two, rowsRemoved+"需要你）"); block != want {
		t.Fatalf("the heading and a row changed, a row gone:\n%s\nwant\n%s", block, want)
	}
}

// Working agents render only the heading count (#11261): when only that count changes, the new heading still reaches
// the conversation and reconstructs the snapshot cleanly, whether Agents has rows under it or not.
func TestContextDeltaWorkingCountChangesWithoutRows(t *testing.T) {
	base := prompts.Snapshot{
		Now:     time.Date(2026, 11, 1, 15, 4, 0, 0, time.UTC),
		Zone:    "UTC",
		Working: []prompts.SnapshotAgent{{Label: "后台任务 1", Machine: "server", Session: "11111111"}},
	}
	for name, withRows := range map[string]bool{"head only": false, "with other rows": true} {
		s1 := base
		if withRows {
			s1.Needs = []prompts.SnapshotAgent{{Label: "修登录", Machine: "server", Session: "aaaaaaaa", Line: "等你确认"}}
			s1.Done = []prompts.SnapshotAgent{{Label: "查日志", Machine: "server", Session: "bbbbbbbb"}}
		}
		snap1 := prompts.RenderSnapshot(s1)
		first, seen := contextDelta(nil, snap1)
		view := readBlock(t, nil, first)
		sameAsSnapshot(t, view, snap1, first)

		s2 := s1
		s2.Working = []prompts.SnapshotAgent{
			{Label: "后台任务 1", Machine: "server", Session: "11111111"},
			{Label: "后台任务 2", Machine: "server", Session: "22222222"},
			{Label: "后台任务 3", Machine: "server", Session: "33333333"},
		}
		snap2 := prompts.RenderSnapshot(s2)
		block, _ := contextDelta(seen, snap2)
		if !strings.Contains(block, "工作中 3 个（详情用 agents_list）") {
			t.Fatalf("%s: updated working count missing from delta:\n%s", name, block)
		}
		if strings.Contains(block, "后台任务") {
			t.Fatalf("%s: working agent title leaked into delta:\n%s", name, block)
		}
		view = readBlock(t, view, block)
		sameAsSnapshot(t, view, snap2, block)
	}
}

// Rows that cannot be told apart by an id (two with the same one, one with none), sections with no ids of their
// own, and a section told no shorter by its rows are sent whole, as before: unmarked, replacing what the conversation had.
func TestContextDeltaSendsWholeSections(t *testing.T) {
	now := "- 现在：2026-11-01 周日 15:04（Asia/Shanghai）"
	kb := "- 知识库（kb_get 看详情）：\n  重点项目（知识库 projects/）：\n  - 登录改版 [P1 相关高] 在灰度（projects/login）\n  - 文档站 [P2 相关中] 在写（projects/docs）"
	following := "- 在跟的事（2 件）：\n  - 「合同」；在哪：邮件；等谁：法务；下一步：催；再看：11-02 10:00\n  - 「报销」；在哪：没写；等谁：没写；下一步：没写；再看：没定时间"
	for name, c := range map[string]struct{ before, after string }{
		"two rows with one id":              {tasksOf(rowLogin, rowDocs, rowDeploy, rowLogin+"（重复）"), tasksOf(rowLogin+"！", rowDocs, rowDeploy, rowLogin+"（重复）")},
		"a row without an id":               {tasksOf(rowLogin, rowDocs, "  - 没有编号的一行：在做"), tasksOf(rowLogin+"！", rowDocs, "  - 没有编号的一行：在做")},
		"the knowledge digest":              {kb, strings.Replace(kb, "在灰度", "全量了", 1)},
		"inbox follow-ups":                  {following, strings.Replace(following, "下一步：催", "下一步：等回信", 1)},
		"every row and the heading changed": {"- Agents：需要你 1 个，完成未看 1 个，工作中 1 个\n  - 需要你：修登录\n  - 完成未看：写文档", "- Agents：需要你 2 个，完成未看 2 个，工作中 2 个\n  - 需要你：修登录；查日志\n  - 完成未看：写文档；跑回归"},
		"rows left out for size": {tasksOf(rowLogin, rowDocs, rowDeploy),
			"- 优先显示的开放任务详情（含个人待办）：3 个，这份快照放不下，没列出来，要看用 todo_list"},
	} {
		_, seen := contextDelta(nil, snapshotOf(now, c.before))
		block, _ := contextDelta(seen, snapshotOf(now, c.after))
		if want := deltaBlock(now, c.after); block != want {
			t.Errorf("%s:\n%s\nwant\n%s", name, block, want)
		}
	}
}

// What the conversation is, and a one-time notice, come before the snapshot's own sections: a line of them that is no
// longer repeated is not told gone (the notice still holds), and is not sent again when it comes back unchanged.
func TestContextDeltaKeepsLinesBeforeTheSnapshot(t *testing.T) {
	now := "- 现在：2026-11-01 周日 15:04（Asia/Shanghai）"
	main := "**这是主对话**：示例用户平时都在这里找你，接着之前聊过的事往下做。"
	notice := "从现在起，你派出去、没有主管的任务由后台督办对话（conv_bg）跟进：它们的督办消息发到那里。"
	direction := "- conv_a：增长"
	_, seen := contextDelta(nil, snapshotOf(main, notice, direction, now))
	block, seen := contextDelta(seen, snapshotOf(main, now))
	if want := deltaBlock(now, goneSection+"（它们之前的内容作废）：", "  - conv_a"); block != want {
		t.Fatalf("the notice was delivered and a direction is gone:\n%s\nwant\n%s", block, want)
	}
	if block, _ = contextDelta(seen, snapshotOf(main, notice, now)); block != deltaBlock(now) {
		t.Fatalf("the same notice again:\n%s", block)
	}
}

// rowIDs names sections of the snapshot the hub renders, and reads its rows' ids from their text: this renders the
// real templates, so a section renamed or a row laid out another way fails here, not silently as whole sections.
func TestSnapshotRowIDs(t *testing.T) {
	agent := func(label, session string) prompts.SnapshotAgent {
		return prompts.SnapshotAgent{Label: label, Machine: "server", Session: session, Line: "等：确认（方案 A；方案 B）", Link: "http://hub.example.com/s/server/" + session}
	}
	s := prompts.Snapshot{
		Now: time.Date(2026, 11, 1, 15, 4, 0, 0, time.UTC), Zone: "UTC",
		Needs: []prompts.SnapshotAgent{agent("修登录", "aaaaaaaa")}, Unread: []prompts.SnapshotAgent{agent("写文档", "bbbbbbbb")},
		Awaiting: []prompts.SnapshotAgent{agent("问方案", "cccccccc")}, Waiting: []prompts.SnapshotAgent{agent("跑回归", "dddddddd")},
		Done: []prompts.SnapshotAgent{agent("查日志", "eeeeeeee")}, Muted: []prompts.SnapshotAgent{agent("清分支", "ffffffff")},
		Working: []prompts.SnapshotAgent{agent("升级依赖", "11111111")},
		Tasks: []prompts.SnapshotTask{
			{ID: "todo_0123456789abcdef", Title: "调研：登录（iOS）；方案", State: "在做", Link: "http://hub.example.com/todos?id=todo_0123456789abcdef", Last: "读完了（todo_ffff）：在写"},
			{ID: "todo_2", Title: "写文档", State: "停着", Health: "正常", Machine: "server", Session: "bbbbbbbb"},
		},
		OpenTodosLoaded: true, OpenTodosTotal: 2,
		Chats:            &prompts.SnapshotChats{AtMe: []prompts.SnapshotChat{{Name: "示例群", Unread: 2, Preview: "看下：这个"}}, Other: []prompts.SnapshotChat{{Name: "闲聊群", Unread: 1}}},
		KB:               "重点项目（知识库 projects/）：\n- 登录改版 [P1 相关高] 在灰度（projects/login）\n",
		FollowingEnabled: true, FollowingTotal: 1, Following: []prompts.SnapshotFollowing{{Title: "合同"}},
	}
	want := map[string][]string{
		"- Agents":      {"需要你", "完成未看", "在问用户、用户看过还没回的", "在等的", "做完的", "已屏蔽"},
		"- 优先显示的开放任务详情": {"todo_0123456789abcdef", "todo_2"},
		"- 飞书未读会话":      {"@我", "其他"},
	}
	scoped := s
	scoped.Scoped = true
	wantScoped := map[string][]string{"- Agents": want["- Agents"], "- 这个对话派出或督办的任务": want["- 优先显示的开放任务详情"], "- 飞书未读会话": want["- 飞书未读会话"]}
	seenKeys := map[string]bool{}
	for name, c := range map[string]struct {
		text string
		want map[string][]string
	}{"main": {prompts.RenderSnapshot(s), want}, "scoped": {prompts.RenderSnapshot(scoped), wantScoped}} {
		for _, sec := range snapshotSections(c.text) {
			_, rows := sectionRows(sec.text)
			id := rowIDs[sec.key]
			if id == nil {
				if len(c.want[sec.key]) > 0 {
					t.Errorf("%s: section %q has no rowIDs", name, sec.key)
				}
				continue
			}
			seenKeys[sec.key] = true
			if ids, ok := idsOf(rows, id); !ok || !slices.Equal(ids, c.want[sec.key]) {
				t.Errorf("%s: section %q: row ids %q (each row its own: %v), want %q\n%s", name, sec.key, ids, ok, c.want[sec.key], sec.text)
			}
		}
	}
	for key := range rowIDs {
		if !seenKeys[key] {
			t.Errorf("rowIDs has %q, which the snapshot does not render:\n%s", key, prompts.RenderSnapshot(s))
		}
	}
	// The sections with rows and no ids of their own stay whole.
	for _, key := range []string{"- 知识库", "- 在跟的事"} {
		found := false
		for _, sec := range snapshotSections(prompts.RenderSnapshot(s)) {
			if _, rows := sectionRows(sec.text); sec.key == key && len(rows) > 0 {
				found = true
			}
		}
		if !found || rowIDs[key] != nil {
			t.Errorf("section %q: rendered with rows %v, in rowIDs %v", key, found, rowIDs[key] != nil)
		}
	}
}

// A section is the same one whatever it says at the moment: its count, its note in parentheses and its empty form
// do not make it another, so the old one is replaced and not left beside the new.
func TestSectionKeyIsTheSubject(t *testing.T) {
	for _, same := range [][]string{
		{"- 飞书日历今天还没结束的会（已经开完的不在这里）：16:00–17:00 评审", "- 飞书日历：今天接下来没有会了（已经开完的不在这里）"},
		{"- 在跟的事（3 件）：", "- 在跟的事：没有"},
		{"- 飞书未读会话 3 个（免打扰的不算；用 lark_chats / lark_messages 看）", "- 飞书未读会话 12 个（免打扰的不算；用 lark_chats / lark_messages 看）"},
		{"- 等用户点头才能上线的（2 件；合到一起一次列给用户批，别一件件问）：「a」", "- 等用户点头才能上线的（3 件；合到一起一次列给用户批，别一件件问）：「a」；「b」"},
		{"- 开放待办索引（仅列 ID）：todo_1", "- 开放待办索引（列出 ID 和标题）：todo_1「修登录」"},
	} {
		if a, b := sectionKey(same[0]), sectionKey(same[1]); a != b || a == "" {
			t.Errorf("%q and %q are one section: keys %q and %q", same[0], same[1], a, b)
		}
	}
	keys := map[string]string{}
	for _, line := range []string{"- 开放待办：共 3 件（完整列表用 todo_list）", "- 开放待办索引（仅列 ID）：todo_1", "- 飞书：没有未读（免打扰的不算）",
		"- 飞书日历：今天接下来没有会了", "- 已安排的跟进：没有", "- 每天的定时任务：早报（每天 08:00）", "- conv_a：增长", "- conv_b：增长"} {
		k := sectionKey(line)
		if other, taken := keys[k]; taken {
			t.Errorf("%q and %q are two sections with one key %q", other, line, k)
		}
		keys[k] = line
	}
	// Two sections that do share a subject stay two.
	twice := snapshotSections("- 备注：a\n- 备注：b")
	if len(twice) != 2 || twice[0].key == twice[1].key {
		t.Errorf("two sections with one subject: %+v", twice)
	}
}

// A random run of snapshots, with every kind of change at once (rows changed, added, removed, reordered; headings
// changed; sections gone and back, left out for size, with rows that have no id or share one): reading the blocks one
// after another leaves a conversation with exactly the snapshot each was made from, and a conversation that was
// compacted in between gets a whole one.
func TestContextDeltaBlocksAddUpToTheSnapshot(t *testing.T) {
	partial, whole := 0, 0
	for seed := range uint64(40) {
		r := rand.New(rand.NewPCG(seed, 9078))
		g := newSnapshotGen(r)
		var seen, view map[string]string
		for step := range 60 {
			snapshot := g.next()
			if r.IntN(15) == 0 {
				seen = nil // compacted, or the node restarted
			}
			var block string
			block, seen = contextDelta(seen, snapshot)
			if !strings.Contains(block, "\n- 现在：") {
				t.Fatalf("seed %d step %d: no time:\n%s", seed, step, block)
			}
			partial += strings.Count(block, rowsMark)
			whole += strings.Count(block, tasksHead)
			view = readBlock(t, view, block)
			if t.Failed() {
				t.Fatalf("seed %d step %d", seed, step)
			}
			sameAsSnapshot(t, view, snapshot, fmt.Sprintf("(seed %d step %d)\n%s", seed, step, block))
		}
	}
	if partial < 300 || whole < 100 {
		t.Fatalf("the run told %d sections by their rows and the tasks whole %d times: it does not cover both", partial, whole)
	}
}

// snapshotGen makes snapshots of synthetic sections that drift from one to the next.
type snapshotGen struct {
	r        *rand.Rand
	n        int
	tasks    []string // to-do ids, in order
	rev      map[string]int
	labels   map[string]int // Agents row label → revision, 0 for none
	chats    map[string]int
	kb, misc int
	gone     map[string]bool // sections the snapshot leaves out now
	omit     bool
	noID     bool
	nextTask int
}

func newSnapshotGen(r *rand.Rand) *snapshotGen {
	return &snapshotGen{r: r, rev: map[string]int{}, labels: map[string]int{"需要你": 1, "完成未看": 1}, chats: map[string]int{"@我": 1}, gone: map[string]bool{}}
}

var genLabels = []string{"需要你", "完成未看", "在等的（hub 到时会叫醒它们，不用管）", "做完的（24 小时内）"}

func (g *snapshotGen) next() string {
	r := g.r
	g.n++
	for range r.IntN(3) { // a few changes each time
		switch r.IntN(12) {
		case 0, 1, 2: // a task's row changes
			if len(g.tasks) > 0 {
				g.rev[g.tasks[r.IntN(len(g.tasks))]]++
			}
		case 3: // a new task, anywhere
			g.nextTask++
			id := fmt.Sprintf("todo_%04x", g.nextTask)
			g.tasks = slices.Insert(g.tasks, r.IntN(len(g.tasks)+1), id)
		case 4: // one goes
			if len(g.tasks) > 0 {
				i := r.IntN(len(g.tasks))
				g.tasks = slices.Delete(g.tasks, i, i+1)
			}
		case 5: // they change places
			r.Shuffle(len(g.tasks), func(i, j int) { g.tasks[i], g.tasks[j] = g.tasks[j], g.tasks[i] })
		case 6, 7: // an Agents row appears, changes or goes
			l := genLabels[r.IntN(len(genLabels))]
			if g.labels[l] > 0 && r.IntN(3) == 0 {
				g.labels[l] = 0
			} else {
				g.labels[l] = g.n
			}
		case 8: // so does a chats row
			l := []string{"@我", "其他"}[r.IntN(2)]
			if g.chats[l] > 0 && r.IntN(2) == 0 {
				g.chats[l] = 0
			} else {
				g.chats[l] = g.n
			}
		case 9:
			g.kb, g.misc = g.kb+r.IntN(2), g.misc+r.IntN(2)
		case 10: // a section goes or comes back
			k := []string{"tasks", "agents", "chats", "kb", "followups", "plans"}[r.IntN(6)]
			g.gone[k] = !g.gone[k]
		case 11:
			g.omit, g.noID = r.IntN(4) == 0, r.IntN(4) == 0
		}
	}
	out := []string{"**这是主对话**：示例用户平时都在这里找你。", fmt.Sprintf("- 现在：2026-11-01 周日 15:%02d（UTC）", g.n%60), "- 在线的电脑：服务器（server）"}
	if !g.gone["agents"] {
		sec := fmt.Sprintf("- Agents：需要你 %d 个，完成未看 %d 个，工作中 %d 个（详情用 agents_list）", g.labels["需要你"]%3, g.labels["完成未看"]%3, g.n%4)
		for _, l := range genLabels {
			if g.labels[l] > 0 {
				sec += fmt.Sprintf("\n  - %s：示例 Agent %d（server，session_id=%08x）：第 %d 版；还在等", l, g.labels[l], g.labels[l], g.labels[l])
			}
		}
		out = append(out, sec)
	}
	if !g.gone["tasks"] {
		switch {
		case g.omit:
			out = append(out, fmt.Sprintf("%s%d 个，这份快照放不下，没列出来，要看用 todo_list", tasksHead, len(g.tasks)))
		case len(g.tasks) > 0:
			sec := tasksHead
			for i, id := range g.tasks {
				row := fmt.Sprintf("\n  - 示例任务：%s（第 %d 步）（%s，链接 http://hub.example.com/todos?id=%s）：在做；督办：正常：第 %d 版", id[5:], i%3, id, id, g.rev[id])
				if g.noID && i == 0 {
					row = fmt.Sprintf("\n  - 没有编号的任务：第 %d 版", g.rev[id])
				}
				sec += row
			}
			out = append(out, sec)
		}
	}
	if !g.gone["chats"] {
		if g.chats["@我"]+g.chats["其他"] == 0 {
			out = append(out, "- 飞书：没有未读（免打扰的不算）")
		} else {
			sec := fmt.Sprintf("- 飞书未读会话 %d 个（免打扰的不算；用 lark_chats / lark_messages 看）", 1+(g.chats["@我"]+g.chats["其他"])%5)
			for _, l := range []string{"@我", "其他"} {
				if g.chats[l] > 0 {
					sec += fmt.Sprintf("\n  - %s：示例群（%d 条）：第 %d 条预览", l, g.chats[l], g.chats[l])
				}
			}
			out = append(out, sec)
		}
	}
	if !g.gone["kb"] {
		out = append(out, fmt.Sprintf("- 知识库（kb_get 看详情）：\n  重点项目（知识库 projects/）：\n  - 示例项目 [P1 相关高] 第 %d 版（projects/a）\n  - 另一个项目 [P2 相关中] 没变（projects/b）", g.kb))
	}
	if !g.gone["plans"] {
		out = append(out, fmt.Sprintf("- 等用户点「开始」的计划（主对话里的卡片，用户点了才开工）：11-01 15:00 示例计划 %d", g.misc))
	}
	if !g.gone["followups"] {
		out = append(out, "- 已安排的跟进：没有")
	}
	return strings.Join(out, "\n")
}

// A conversation that was compacted, and a node that restarted, send the whole snapshot next, rows and all; in
// between, a changed row goes alone.
func TestTurnContextIsWholeAfterCompactionAndRestart(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	now := "- 现在：2026-11-01 周日 15:04（Asia/Shanghai）"
	first := snapshotOf(now, tasksOf(rowLogin, rowDocs, rowDeploy))
	changed := strings.Replace(rowLogin, "在跑测试", "测试过了", 1)
	second := snapshotOf(now, tasksOf(changed, rowDocs, rowDeploy))
	send := func(n *Node, snapshot string) string {
		block, commit := n.turnContext(id, contextAnswer{Text: snapshot})
		commit()
		return block
	}
	wholeBlock := func(snapshot string) string {
		return "<persona-context>\n" + fullIntro + "\n" + snapshot + "\n</persona-context>"
	}

	n := assistantNode(t)
	if block := send(n, first); block != wholeBlock(first) {
		t.Fatalf("a new conversation:\n%s", block)
	}
	if block, want := send(n, second), deltaBlock(now, tasksKey+rowsMark+"：", changed); block != want {
		t.Fatalf("a changed row:\n%s\nwant\n%s", block, want)
	}
	n.contextCompacted(id)
	if block := send(n, first); block != wholeBlock(first) {
		t.Fatalf("after a compaction:\n%s", block)
	}
	if block := send(assistantNode(t), second); block != wholeBlock(second) {
		t.Fatalf("after a node restart:\n%s", block)
	}
	// A message that did not go out (no commit) leaves the conversation where it was: the row is told again.
	if block, _ := n.turnContext(id, contextAnswer{Text: second}); !strings.Contains(block, changed) {
		t.Fatalf("an unsent block:\n%s", block)
	}
	if block, want := send(n, second), deltaBlock(now, tasksKey+rowsMark+"：", changed); block != want {
		t.Fatalf("the row after an unsent block:\n%s\nwant\n%s", block, want)
	}
}

// sectionDeltaBefore is contextDelta as it was before rows were told apart (#9078): a changed section whole, with
// the section keys of then. The measurement below compares against it.
func sectionDeltaBefore(seen map[string]string, snapshot string) (string, map[string]string) {
	key := func(line string) string {
		k, _, _ := strings.Cut(line, "：")
		k, _, _ = strings.Cut(k, "今天")
		return strings.TrimSpace(k)
	}
	next := map[string]string{}
	for k, v := range seen {
		next[k] = v
	}
	c := prompts.TurnContext{Full: seen == nil}
	for _, s := range snapshotSections(snapshot) {
		head, _, _ := strings.Cut(s.text, "\n")
		k := key(head)
		if seen == nil || k == timeSection || seen[k] != s.text {
			c.Sections = append(c.Sections, prompts.TurnSection{Text: s.text})
		}
		next[k] = s.text
	}
	return prompts.RenderTurnContext(c), next
}

// shapedSnapshots is a synthetic main conversation's snapshots, rendered by the real templates: tasks to-dos with
// an agent each, of which one says something new before every message, one more now and then; the working agents'
// plans move on every third message; every eighth a task ends and a new one starts, and an agent needs the user.
func shapedSnapshots(messages, tasks int) []string {
	say := []string{"读完了相关代码，在写实现", "实现写好了，在跑单元测试", "测试过了，在整理提交说明", "开了 PR，在等检查跑完", "检查过了，在看评审意见"}
	type task struct{ id, rev int }
	var open []task
	for i := range tasks {
		open = append(open, task{id: i + 1})
	}
	nextID := tasks + 1
	var out []string
	for m := range messages {
		if m > 0 {
			open[m%len(open)].rev++
			if m%4 == 0 {
				open[(m+3)%len(open)].rev++
			}
			if m%8 == 0 {
				open = append([]task{{id: nextID}}, open[:len(open)-1]...)
				nextID++
			}
		}
		s := prompts.Snapshot{Now: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC).Add(time.Duration(m) * 7 * time.Minute), Zone: "UTC",
			Machines: []prompts.SnapshotMachine{{Name: "服务器", ID: "server", Online: true}}, OpenTodosLoaded: true, OpenTodosTotal: len(open)}
		for i, t := range open {
			id := fmt.Sprintf("todo_%016x", t.id)
			session := fmt.Sprintf("%08x", 0xa0000000+t.id)
			title := fmt.Sprintf("示例任务 %d：整理第 %d 个模块的接口和文档", t.id, t.id)
			s.Tasks = append(s.Tasks, prompts.SnapshotTask{ID: id, Title: title, State: "在做", Health: "正常", DoneWhen: 2, Machine: "server", Session: session,
				Link: "http://hub.example.com/todos?id=" + id, Last: say[t.rev%len(say)], Plan: &prompts.SnapshotAgentPlan{Done: t.rev % 6, Total: 6, Current: "第 " + fmt.Sprint(t.rev%6+1) + " 步"}})
			s.OpenTodos = append(s.OpenTodos, prompts.SnapshotOpenTodo{ID: id, Title: title})
			a := prompts.SnapshotAgent{Label: title, Machine: "server", Session: session, Link: "http://hub.example.com/s/server/" + session}
			switch {
			case i == 0 && m%8 >= 6:
				a.Line = "等你确认：先合并还是先补测试"
				s.Needs = append(s.Needs, a)
			case i%4 == 3:
				a.Line = "在等检查跑完"
				s.Waiting = append(s.Waiting, a)
			default:
				a.Plan = &prompts.SnapshotAgentPlan{Done: m / 3 % 6, Total: 6, Current: "第 " + fmt.Sprint(m/3%6+1) + " 步"}
				s.Working = append(s.Working, a)
			}
		}
		out = append(out, prompts.RenderSnapshot(s))
	}
	return out
}

// Before and after on the same snapshots (#9078): two synthetic conversations with the shape of the two measured
// ones, a short one whose sections repeated about 1.8 KB of rows that had not changed and a long one that repeated
// about 33 KB. Run with -v for the numbers. Told by rows, a block repeats no unchanged row at all.
func TestContextDeltaBytesBeforeAndAfter(t *testing.T) {
	// repeated is how many bytes of block are rows the conversation already had, unchanged.
	repeated := func(seen map[string]string, block string) int {
		had := map[string]bool{}
		for _, text := range seen {
			_, rows := sectionRows(text)
			for _, r := range rows {
				had[r] = true
			}
		}
		n := 0
		for line := range strings.SplitSeq(block, "\n") {
			if had[line] {
				n += len(line) + 1
			}
		}
		return n
	}
	for _, shape := range []struct {
		name            string
		messages, tasks int
	}{{"short", 4, 3}, {"long", 13, 10}} {
		var seenBefore, seenAfter, view map[string]string
		var before, after, repeatedBefore, repeatedAfter, first int
		for i, snapshot := range shapedSnapshots(shape.messages, shape.tasks) {
			old, nextBefore := sectionDeltaBefore(seenBefore, snapshot)
			block, nextAfter := contextDelta(seenAfter, snapshot)
			view = readBlock(t, view, block)
			sameAsSnapshot(t, view, snapshot, block)
			if i == 0 {
				if first = len(block); old != block {
					t.Fatalf("%s: the first, whole block differs:\n%s\nbefore:\n%s", shape.name, block, old)
				}
			} else {
				before, after = before+len(old), after+len(block)
				repeatedBefore, repeatedAfter = repeatedBefore+repeated(seenBefore, old), repeatedAfter+repeated(seenAfter, block)
			}
			seenBefore, seenAfter = nextBefore, nextAfter
		}
		t.Logf("%s (%d messages, %d tasks): first block %d B; the %d blocks after it: by section %d B (%d B of unchanged rows), by row %d B (%d B of unchanged rows), %d%% fewer",
			shape.name, shape.messages, shape.tasks, first, shape.messages-1, before, repeatedBefore, after, repeatedAfter, 100-100*after/before)
		if repeatedAfter != 0 || after >= before {
			t.Errorf("%s: by row %d B with %d B of unchanged rows, by section %d B", shape.name, after, repeatedAfter, before)
		}
	}
}
