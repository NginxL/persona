package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// topicModel answers with replies in turn and keeps what it was asked; during runs before answering (a forget racing
// the call).
type topicModel struct {
	mu      sync.Mutex // a run's calls go out topicWorkers at a time
	replies []string
	asked   []string // user prompts
	during  func()
}

func (m *topicModel) JSONMax(_ context.Context, _, user string, _ int, out any) error {
	m.mu.Lock()
	m.asked = append(m.asked, user)
	r := m.replies[min(len(m.asked)-1, len(m.replies)-1)]
	during := m.during
	m.mu.Unlock()
	if during != nil {
		during()
	}
	return json.Unmarshal([]byte(r), out)
}

// quietProject is a project with timeline lines, last written long enough ago to be due.
func quietProject(t *testing.T, e *Engine, slug string, lines ...string) {
	t.Helper()
	if err := e.Store.PutProject(&Project{Slug: slug, Name: "项目 " + slug, Status: "active", Goal: "把它做完"}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.Local)
	for i, l := range lines {
		if err := e.Store.AppendLog(slug, at.Add(time.Duration(i)*time.Minute), "Agent", l); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(e.Store.path(KindProject, slug, "log.md"), old, old); err != nil {
		t.Fatal(err)
	}
}

func putTopic(t *testing.T, e *Engine, topic *Topic, f topicFile) {
	t.Helper()
	e.Store.topicMu.Lock()
	defer e.Store.topicMu.Unlock()
	if err := e.Store.writeTopic(topic, f, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func topicFileOf(t *testing.T, e *Engine, slug string) topicFile {
	t.Helper()
	e.Store.topicMu.Lock()
	defer e.Store.topicMu.Unlock()
	f, err := e.Store.topicFile(slug)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A project's new timeline lines become candidates under the topics the model names: an existing topic by its alias,
// a new one by name. A point of an unknown kind or without text is dropped; the source's mark moves to the end, so
// the same lines are not sent again.
func TestDistillFilesCandidatesUnderTopics(t *testing.T) {
	m := &topicModel{replies: []string{`{"points":[
		{"topic":"Persona 知识库","path":["后台整理"],"kind":"decision","text":"项目摘要每轮最多重写 3 个","why":"控制模型调用","when":"2026-11-09"},
		{"topic":"kb","path":"搜索 / 缓存","kind":"fact","text":"热搜索约 85 毫秒"},
		{"topic":"Persona 知识库","kind":"progress","text":"正在改第三版"},
		{"topic":"Persona 知识库","kind":"pitfall","text":""}
	]}`}}
	e := testEngine(t, m)
	putTopic(t, e, &Topic{Slug: "search", Name: "知识库搜索", Aliases: []string{"KB"}}, topicFile{})
	quietProject(t, e, "p", "定下每轮最多重写 3 个项目", "测了搜索：热搜索 85ms", "改到第三版")

	if n := e.DistillTopics(context.Background(), 2); n != 1 || len(m.asked) != 1 {
		t.Fatalf("distilled %d with %d call(s), want 1 and 1", n, len(m.asked))
	}
	for _, want := range []string{"项目：项目 p", "时间线（第 1–3 条，共 3 条）", "定下每轮最多重写 3 个项目", "- 知识库搜索（KB）"} {
		if !strings.Contains(m.asked[0], want) {
			t.Errorf("the prompt lacks %q:\n%s", want, m.asked[0])
		}
	}
	slug := Slug("Persona 知识库")
	f := topicFileOf(t, e, slug)
	if len(f.Pending) != 1 || f.Pending[0].Kind != "decision" || !slices.Equal(f.Pending[0].Path, []string{"后台整理"}) {
		t.Fatalf("new topic's candidates = %+v", f.Pending)
	}
	if src := f.Pending[0].Sources; len(src) != 1 || src[0] != (TopicSource{Kind: KindProject, ID: "p", Day: "2026-11-09"}) {
		t.Errorf("sources = %+v", src)
	}
	s := topicFileOf(t, e, "search")
	if len(s.Pending) != 1 || !slices.Equal(s.Pending[0].Path, []string{"搜索", "缓存"}) || s.Pending[0].When == "" {
		t.Fatalf("the alias's topic got %+v (a path in one string is split; a missing day is today's)", s.Pending)
	}
	if topic, ok := e.Store.Topic(slug); !ok || !topic.Dirty || topic.Name != "Persona 知识库" {
		t.Fatalf("new topic = %+v", topic)
	}
	if mark := e.Store.topicMark(KindProject, "p"); mark.Lines != 3 || mark.Size == 0 {
		t.Errorf("mark = %+v", mark)
	}
	if n := e.DistillTopics(context.Background(), 2); n != 0 || len(m.asked) != 1 {
		t.Fatalf("nothing new, yet %d call(s)", len(m.asked)-1)
	}
	// One fresh line on a timeline that was just written: not due until it has a batch or goes quiet.
	_ = e.Store.AppendLog("p", time.Now(), "Agent", "又一行")
	if n := e.DistillTopics(context.Background(), 2); n != 0 {
		t.Fatalf("a single fresh line was distilled at once")
	}
}

// A long timeline goes in stretches, oldest first, one stretch a call.
func TestDistillReadsALongTimelineInStretches(t *testing.T) {
	m := &topicModel{replies: []string{`{"points":[]}`}}
	e := testEngine(t, m)
	line := strings.Repeat("长", distillLineRunes)
	var lines []string
	for range 60 { // AppendLog clips each line: enough of them to pass distillInput
		lines = append(lines, line)
	}
	quietProject(t, e, "p", lines...)
	e.DistillTopics(context.Background(), 1)
	first := e.Store.topicMark(KindProject, "p")
	if first.Lines == 0 || first.Lines >= 60 || first.Size != 0 {
		t.Fatalf("first stretch = %+v, want part of the timeline and no size yet", first)
	}
	for range 60 {
		if e.DistillTopics(context.Background(), 1) == 0 {
			break
		}
	}
	if last := e.Store.topicMark(KindProject, "p"); last.Lines != 60 || last.Size == 0 {
		t.Fatalf("after the stretches: %+v", last)
	}
	if !strings.Contains(m.asked[1], "时间线（第 "+strconv.Itoa(first.Lines+1)+"–") {
		t.Errorf("the second stretch does not start after the first:\n%.200s", m.asked[1])
	}
}

// The merge applies the model's edits: a new point from candidates (their sources and latest day), candidates attached
// to an existing point with a better wording, a dropped point; edits naming unknown points or candidates do nothing,
// and candidates no edit names are let go.
func TestMergeAppliesEdits(t *testing.T) {
	m := &topicModel{replies: []string{`{"note":"Persona 的知识库怎么整理","ops":[
		{"op":"add","from":["c1","c3"],"path":["后台整理"],"kind":"decision","text":"项目摘要每轮最多重写 3 个","why":"控制调用"},
		{"op":"update","id":"p1","from":["c2"],"text":"热搜索约 85 毫秒（缓存后）"},
		{"op":"drop","id":"p2"},
		{"op":"add","from":["c9"],"kind":"fact","text":"编出来的"},
		{"op":"update","id":"p9","text":"不存在"}
	]}`}}
	e := testEngine(t, m)
	var indexed []string
	e.Index = func(kind, id, name string, _ Tagging, one string, _ time.Time) {
		indexed = append(indexed, kind+"/"+id+"="+one)
	}
	src := func(kind, id, day string) []TopicSource { return []TopicSource{{Kind: kind, ID: id, Day: day}} }
	putTopic(t, e, &Topic{Slug: "kb", Name: "Persona 知识库"}, topicFile{Next: 3,
		Points: []TopicPoint{
			{ID: "p1", Path: []string{"搜索"}, Kind: "fact", Text: "热搜索 85ms", Sources: src(KindProject, "a", "2026-11-01"), When: "2026-11-01"},
			{ID: "p2", Kind: "question", Text: "要不要缓存", Sources: src(KindChat, "lark-1", "2026-10-01"), When: "2026-10-01"},
		},
		Pending: []TopicPoint{
			{Kind: "decision", Text: "每轮最多重写 3 个项目", Sources: src(KindProject, "a", "2026-11-08"), When: "2026-11-08"},
			{Kind: "fact", Text: "缓存后热搜索 85ms", Sources: src(KindChat, "lark-2", "2026-11-09"), When: "2026-11-09"},
			{Kind: "decision", Text: "项目重写限 3 个", Sources: src(KindProject, "b", "2026-11-09"), When: "2026-11-09"},
			{Kind: "fact", Text: "没人提到的候选", Sources: src(KindProject, "c", "2026-11-09"), When: "2026-11-09"},
		}})

	if n := e.RefreshTopics(context.Background(), 2); n != 1 {
		t.Fatalf("merged %d", n)
	}
	for _, want := range []string{"- p1 [搜索] fact · 热搜索 85ms · 2026-11-01", "- c3 [] decision · 项目重写限 3 个 · 2026-11-09"} {
		if !strings.Contains(m.asked[0], want) {
			t.Errorf("the prompt lacks %q:\n%s", want, m.asked[0])
		}
	}
	f := topicFileOf(t, e, "kb")
	if len(f.Pending) != 0 || len(f.Points) != 2 || f.Next != 4 {
		t.Fatalf("after the merge: %d pending, %d points, next %d", len(f.Pending), len(f.Points), f.Next)
	}
	p1, p3 := f.Points[0], f.Points[1]
	if p1.ID != "p1" || p1.Text != "热搜索约 85 毫秒（缓存后）" || p1.When != "2026-11-09" || len(p1.Sources) != 2 {
		t.Errorf("updated point = %+v", p1)
	}
	if p3.ID != "p3" || p3.Kind != "decision" || p3.When != "2026-11-09" || len(p3.Sources) != 2 || !slices.Equal(p3.Path, []string{"后台整理"}) {
		t.Errorf("new point = %+v", p3)
	}
	topic, _ := e.Store.Topic("kb")
	if topic.Dirty || topic.Points != 2 || topic.Note != "Persona 的知识库怎么整理" || !slices.Equal(topic.Projects, []string{"a", "b"}) || !slices.Equal(topic.Chats, []string{"lark-2"}) {
		t.Errorf("topic = %+v", topic)
	}
	sum := e.Store.Summary(KindTopic, "kb")
	for _, want := range []string{"# Persona 知识库", "## 搜索", "- 【事实】热搜索约 85 毫秒（缓存后） · 2026-11-09", "## 后台整理", "- 【决定】项目摘要每轮最多重写 3 个（控制调用）"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary.md lacks %q:\n%s", want, sum)
		}
	}
	if !slices.Equal(indexed, []string{"topics/kb=Persona 的知识库怎么整理"}) {
		t.Errorf("indexed = %q", indexed)
	}
}

// An answer to a question that changed meanwhile (a source forgotten during the call) is not applied: the topic is
// merged again next round.
func TestMergeAnswerIsDroppedWhenTheTopicChanged(t *testing.T) {
	m := &topicModel{replies: []string{`{"ops":[{"op":"add","from":["c1"]}]}`}}
	e := testEngine(t, m)
	putTopic(t, e, &Topic{Slug: "kb", Name: "知识库"}, topicFile{Pending: []TopicPoint{
		{Kind: "fact", Text: "来自会话的", Sources: []TopicSource{{Kind: KindChat, ID: "lark-1"}}},
		{Kind: "fact", Text: "来自项目的", Sources: []TopicSource{{Kind: KindProject, ID: "a"}}},
	}})
	m.during = func() {
		if _, err := e.forgetTopicSources([]string{"lark-1"}, nil); err != nil {
			t.Error(err)
		}
	}
	e.RefreshTopics(context.Background(), 1)
	f := topicFileOf(t, e, "kb")
	if len(f.Points) != 0 || len(f.Pending) != 1 || f.Pending[0].Text != "来自项目的" {
		t.Fatalf("after a merge raced by a forget: %+v", f)
	}
	if topic, _ := e.Store.Topic("kb"); !topic.Dirty {
		t.Error("the topic is no longer due")
	}
}

// Turned off (assistant.kb_topics), the curation makes no model calls at all.
func TestTopicsOffMakesNoCalls(t *testing.T) {
	m := &topicModel{replies: []string{`{"points":[]}`}}
	e := testEngine(t, m)
	e.TopicsOn = func() bool { return false }
	quietProject(t, e, "p", "一行")
	putTopic(t, e, &Topic{Slug: "kb", Name: "知识库"}, topicFile{Pending: []TopicPoint{{Kind: "fact", Text: "x"}}})
	if e.DistillTopics(context.Background(), 2)+e.RefreshTopics(context.Background(), 2) != 0 || len(m.asked) != 0 {
		t.Fatalf("%d call(s) while off", len(m.asked))
	}
}

// A note the user wrote stays over the merge's.
func TestTopicNoteByHandIsKept(t *testing.T) {
	m := &topicModel{replies: []string{`{"note":"模型写的","ops":[]}`}}
	e := testEngine(t, m)
	putTopic(t, e, &Topic{Slug: "kb", Name: "知识库"}, topicFile{Pending: []TopicPoint{{Kind: "fact", Text: "x"}}})
	if _, err := e.Apply(KindTopic, "kb", Update{Note: "我写的"}); err != nil {
		t.Fatal(err)
	}
	e.RefreshTopics(context.Background(), 1)
	if topic, _ := e.Store.Topic("kb"); topic.Note != "我写的" || !slices.Contains(topic.LockedFields, "note") {
		t.Fatalf("topic = %+v", topic)
	}
	if _, err := e.Apply(KindTopic, "kb", Update{Summary: "手写"}); err == nil {
		t.Error("a topic's summary was replaced by hand")
	}
}

// Forgetting a source takes its chats out of the points: a point from them alone goes, one with another source keeps
// that; a topic left empty is removed and reported. Timeline lines the forget dropped take their project out too, and
// the project's timeline is distilled again from the start.
func TestForgetSourceTakesItsKnowledgePointsAway(t *testing.T) {
	e := testEngine(t, &topicModel{replies: []string{`{"points":[]}`}})
	st := e.Store
	_ = st.PutChat(&Chat{ID: "lark-1", Source: "lark", Connector: "lark", Kind: "group", Name: "需求群"})
	_ = st.PutChat(&Chat{ID: "mail-1", Source: "mail", Kind: "mail", Name: "邮件"})
	_ = st.PutProject(&Project{Slug: "a", Name: "项目 a", Status: "active", Chats: []string{"lark-1"}})
	_ = st.AppendLog("a", time.Now(), "需求群", "群里定的")
	_ = st.AppendLog("a", time.Now(), "Persona", "主对话记的")
	_ = st.putTopicMark(KindProject, "a", topicMark{Lines: 2, Size: 1})
	chat := func(id string) TopicSource { return TopicSource{Kind: KindChat, ID: id} }
	putTopic(t, e, &Topic{Slug: "mixed", Name: "混合"}, topicFile{Points: []TopicPoint{
		{ID: "p1", Kind: "fact", Text: "只来自需求群", Sources: []TopicSource{chat("lark-1")}},
		{ID: "p2", Kind: "fact", Text: "两处都有", Sources: []TopicSource{chat("lark-1"), chat("mail-1")}},
		{ID: "p3", Kind: "fact", Text: "来自项目 a 的时间线", Sources: []TopicSource{{Kind: KindProject, ID: "a"}}},
	}})
	putTopic(t, e, &Topic{Slug: "only", Name: "只有需求群"}, topicFile{Points: []TopicPoint{
		{ID: "p1", Kind: "fact", Text: "x", Sources: []TopicSource{chat("lark-1")}},
	}})

	out, err := e.ForgetSource("lark")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Items[KindTopic], []string{"only"}) {
		t.Errorf("forgotten topics = %v", out.Items[KindTopic])
	}
	if _, ok := st.Topic("only"); ok {
		t.Error("the emptied topic is still there")
	}
	f := topicFileOf(t, e, "mixed")
	if len(f.Points) != 1 || f.Points[0].ID != "p2" || len(f.Points[0].Sources) != 1 || f.Points[0].Sources[0].ID != "mail-1" {
		t.Fatalf("points left = %+v", f.Points)
	}
	if strings.Contains(st.Summary(KindTopic, "mixed"), "只来自需求群") {
		t.Error("summary.md still has the forgotten point")
	}
	if mark := st.topicMark(KindProject, "a"); mark.Lines != 0 || mark.Size != 0 {
		t.Errorf("project a's mark = %+v, want back at the start", mark)
	}
}

// A topic is read, listed and found like the other kinds: its points come with it, the biggest topic lists first, and
// search reads summary.md but not points.json.
func TestTopicsAreReadListedAndFound(t *testing.T) {
	e := testEngine(t, &topicModel{replies: []string{`{}`}})
	_ = e.Store.PutProject(&Project{Slug: "a", Name: "项目 a", Status: "active"})
	putTopic(t, e, &Topic{Slug: "small", Name: "小主题"}, topicFile{Points: []TopicPoint{{ID: "p1", Kind: "fact", Text: "一条"}}})
	putTopic(t, e, &Topic{Slug: "big", Name: "大主题", Note: "两条的"}, topicFile{
		Points: []TopicPoint{
			{ID: "p1", Kind: "practice", Text: "升级前先备份数据目录", Sources: []TopicSource{{Kind: KindProject, ID: "a"}}},
			{ID: "p2", Kind: "pitfall", Text: "换机后要重新扫码", Why: "会话会失效"},
		},
		Pending: []TopicPoint{{Kind: "fact", Text: "待合并的独特词语"}},
	})
	d, err := e.Get(KindTopic, "big", 0)
	if err != nil || len(d.Points) != 2 || len(d.Links) != 1 || d.Links[0].ID != "a" || !strings.Contains(d.Summary, "升级前先备份数据目录") {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	list := e.List(KindTopic, "")
	if len(list) != 2 || list[0].ID != "big" || list[0].Extra != "2" || list[0].OneLine != "两条的" {
		t.Fatalf("list = %+v", list)
	}
	if res := e.Find("重新扫码", 10); len(res) != 1 || res[0].Entry.Kind != KindTopic || res[0].Entry.ID != "big" {
		t.Errorf("Find = %+v", res)
	}
	if hits := e.Search("待合并的独特词语", false, 10); len(hits) != 0 {
		t.Errorf("search reached points.json: %+v", hits)
	}
	if hits := e.SearchKind("备份", KindTopic, false, 10); len(hits) == 0 {
		t.Error("SearchKind found nothing in topics")
	}
}

// The prompts and the summary, byte for byte.
func TestTopicPromptsGolden(t *testing.T) { goldenTopics(t, "") }

func goldenTopics(t *testing.T, language string) {
	m := &captureModel{reply: `{"points":[]}`}
	e := testEngine(t, m)
	setKnowledgeLanguage(e, m, language)
	putTopic(t, e, &Topic{Slug: "kb", Name: "知识库", Aliases: []string{"KB"}, Note: "知识库怎么整理"}, topicFile{})
	quietProject(t, e, "p", "定下每轮最多重写 3 个项目", "测了搜索：热搜索 85ms")
	e.DistillTopics(context.Background(), 1)
	m.golden(t, "topic_distill")

	m.reply = `{"ops":[]}`
	putTopic(t, e, &Topic{Slug: "kb", Name: "知识库", Aliases: []string{"KB"}, Note: "知识库怎么整理"}, topicFile{Next: 2,
		Points:  []TopicPoint{{ID: "p1", Path: []string{"搜索"}, Kind: "fact", Text: "热搜索 85ms", Why: "有缓存", When: "2026-11-01"}},
		Pending: []TopicPoint{{Path: []string{"后台整理"}, Kind: "decision", Text: "每轮最多重写 3 个项目", When: "2026-11-09"}}})
	pinned := "项目摘要的上限是 3 个" // the user's, read-only to the merge
	if _, err := e.Apply(KindTopic, "kb", Update{Pinned: &pinned}); err != nil {
		t.Fatal(err)
	}
	e.RefreshTopics(context.Background(), 1)
	m.golden(t, "topic_merge")

	golden(t, "topic_summary", topicSummary(&Topic{Name: "知识库", Note: "知识库怎么整理"}, []TopicPoint{
		{ID: "p1", Kind: "concept", Text: "主题下按子主题列知识点"},
		{ID: "p2", Path: []string{"搜索", "缓存"}, Kind: "fact", Text: "热搜索 85ms", When: "2026-11-01"},
		{ID: "p3", Path: []string{"搜索"}, Kind: "pitfall", Text: "冷启动慢", Why: "要读全部文件"},
		{ID: "p4", Path: []string{"后台整理"}, Kind: "question", Text: "要不要定时整理主题"},
		{ID: "p5", Path: []string{"索引", "表"}, Kind: "fact", Text: "kb_entities 没有外键"}, // a subtopic with only a deeper one
	}))
}

// timeoutModel runs out of time on its first fails calls and answers with no points after; it keeps the user prompts.
type timeoutModel struct {
	mu    sync.Mutex
	fails int
	asked []string
}

func (m *timeoutModel) JSONMax(_ context.Context, _, user string, _ int, out any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, user)
	if len(m.asked) <= m.fails {
		return fmt.Errorf("ai: read stream: %w", context.DeadlineExceeded)
	}
	return json.Unmarshal([]byte(`{"points":[]}`), out)
}

// One call carries at most distillInput bytes of timeline lines (#4350).
func TestDistillStretchFitsTheInput(t *testing.T) {
	m := &topicModel{replies: []string{`{"points":[]}`}}
	e := testEngine(t, m)
	line := strings.Repeat("a", 500)
	var lines []string
	for range 40 {
		lines = append(lines, line)
	}
	quietProject(t, e, "p", lines...)
	e.DistillTopics(context.Background(), 1)
	got := e.Store.topicMark(KindProject, "p").Lines
	size := len(e.Store.timeline("p")[0]) // the line as the timeline holds it (day and source before it)
	if got == 0 || got*size > distillInput || (got+1)*size <= distillInput {
		t.Fatalf("first stretch = %d lines of %d bytes, want as many as fit in %d", got, size, distillInput)
	}
}

// A stretch that ran out of time is tried again with half its lines, then half again; the mark moves only over what
// was distilled, and a success lifts the cap (#4350).
func TestDistillRetriesATimedOutStretchSmaller(t *testing.T) {
	m := &timeoutModel{fails: 2}
	e := testEngine(t, m)
	line := "★" + strings.Repeat("a", 150) // ★ counts a call's lines (AppendLog keeps 200 characters)
	var lines []string
	for range 100 {
		lines = append(lines, line)
	}
	quietProject(t, e, "p", lines...)
	key := KindProject + "/p"

	e.DistillTopics(context.Background(), 1)
	full := strings.Count(m.asked[0], "★") // lines that fit in distillInput
	if full < 8 {
		t.Fatalf("the first call carried %d lines", full)
	}
	if mark := e.Store.topicMark(KindProject, "p"); mark.Lines != 0 {
		t.Fatalf("a call that ran out of time moved the mark: %+v", mark)
	}
	if f, _ := e.retries().topics.get(key); f.failures != 1 || f.chunk != full/2 {
		t.Fatalf("after the first timeout: %+v, want 1 failure and a cap of %d lines", f, full/2)
	}
	if e.DistillTopics(context.Background(), 1) != 0 {
		t.Fatal("tried again before its wait was over")
	}

	e.retries().topics.over()
	e.DistillTopics(context.Background(), 1)
	if f, _ := e.retries().topics.get(key); f.failures != 2 || f.chunk != max(distillMinLines, full/4) {
		t.Fatalf("after the second timeout: %+v, want a cap of %d lines", f, max(distillMinLines, full/4))
	}

	e.retries().topics.over()
	e.DistillTopics(context.Background(), 1)
	if mark := e.Store.topicMark(KindProject, "p"); mark.Lines != max(distillMinLines, full/4) {
		t.Fatalf("mark after the smaller stretch = %+v, want %d lines", mark, max(distillMinLines, full/4))
	}
	if _, ok := e.retries().topics.get(key); ok {
		t.Fatal("a success kept the failure and its cap")
	}
	e.DistillTopics(context.Background(), 1) // no cap: the next stretch is a full one again
	if mark := e.Store.topicMark(KindProject, "p"); mark.Lines != max(distillMinLines, full/4)+full {
		t.Fatalf("mark after a full stretch = %+v", mark)
	}
	for i, want := range []int{full, full / 2, max(distillMinLines, full/4), full} {
		if got := strings.Count(m.asked[i], "★"); got != want {
			t.Errorf("call %d carried %d lines, want %d", i+1, got, want)
		}
	}
}

// A call that failed otherwise (not out of time) is tried again with the same stretch.
func TestDistillKeepsTheStretchAfterOtherFailures(t *testing.T) {
	e := testEngine(t, &topicModel{replies: []string{`not json`}})
	quietProject(t, e, "p", "一行", "两行")
	e.DistillTopics(context.Background(), 1)
	if f, ok := e.retries().topics.get(KindProject + "/p"); !ok || f.chunk != 0 {
		t.Fatalf("after a bad answer: %+v, %v; want a failure without a cap", f, ok)
	}
}
