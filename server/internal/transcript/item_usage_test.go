package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/tokenuse"
)

// claudeReplyLine is one content block of a reply as Claude Code writes it: a line of its own, repeating the request's
// usage (usage is the JSON object, "" for none).
func claudeReplyLine(uuid, msg, model, block, usage string, extra ...string) string {
	u := ""
	if usage != "" {
		u = `,"usage":` + usage
	}
	return `{"type":"assistant","uuid":"` + uuid + `","timestamp":"2026-11-12T08:00:00Z"` + strings.Join(extra, "") +
		`,"message":{"role":"assistant","model":"` + model + `","id":"` + msg + `","content":[` + block + `]` + u + `}}`
}

func claudeToolResultLine(uuid, call string) string {
	return `{"type":"user","uuid":"` + uuid + `","timestamp":"2026-11-12T08:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + call + `","content":"ok"}]}}`
}

const (
	thinkingBlock = `{"type":"thinking","thinking":"Thinking it over."}`
	textBlock     = `{"type":"text","text":"Here it is."}`
)

func toolBlock(id string) string {
	return `{"type":"tool_use","id":"` + id + `","name":"Bash","input":{"command":"ls"}}`
}

// usageOf maps each item that carries usage to it.
func usageOf(items []proto.Item) map[string][]proto.TokenUsage {
	out := map[string][]proto.TokenUsage{}
	for _, it := range items {
		if it.Usage != nil {
			out[it.ID] = it.Usage
		}
	}
	return out
}

// A request's usage rides once, on the first item its lines made: every content block of the reply repeats it (a tool's
// result may come between them). A line that made no item (a thinking block with nothing shown) leaves it to the next
// one. Claude Code's own lines for a failed request, a gateway's zeros and a subagent's sidechain lines carry none; a
// line without a message id is a request of its own. The fast speed and the 1-hour cache writes are kept.
func TestClaudeRequestUsageOnce(t *testing.T) {
	const opus = "claude-opus-5-5"
	u1 := `{"input_tokens":3,"cache_read_input_tokens":40000,"cache_creation_input_tokens":1200,"output_tokens":90,"cache_creation":{"ephemeral_1h_input_tokens":1000,"ephemeral_5m_input_tokens":200},"speed":"fast"}`
	u2 := `{"input_tokens":5,"cache_read_input_tokens":41200,"cache_creation_input_tokens":0,"output_tokens":12,"speed":"standard"}`
	lines := []string{
		`{"type":"user","uuid":"p1","timestamp":"2026-11-12T08:00:00Z","message":{"role":"user","content":"look at it"}}`,
		claudeReplyLine("a1", "msg_1", opus, thinkingBlock, u1),
		claudeReplyLine("a2", "msg_1", opus, textBlock, u1),
		claudeReplyLine("a3", "msg_1", opus, toolBlock("toolu_1"), u1),
		claudeToolResultLine("r1", "toolu_1"),
		claudeReplyLine("a4", "msg_1", opus, toolBlock("toolu_2"), u1), // after a result, still msg_1's
		claudeToolResultLine("r2", "toolu_2"),
		claudeReplyLine("a5", "msg_2", opus, `{"type":"thinking","thinking":"","signature":"x"}`, u2), // no item
		claudeReplyLine("a6", "msg_2", opus, textBlock, u2),
		claudeReplyLine("a7", "msg_3", "<synthetic>", `{"type":"text","text":"API Error: 500"}`, `{"input_tokens":0,"output_tokens":0}`),
		claudeReplyLine("a8", "msg_4", opus, `{"type":"text","text":"Rate limited."}`, `{"input_tokens":7,"output_tokens":1}`, `,"isApiErrorMessage":true,"error":"rate_limit"`),
		claudeReplyLine("a9", "msg_5", "glm-5.3", textBlock, `{"input_tokens":0,"output_tokens":0}`), // a gateway's zeros
		claudeReplyLine("s1", "msg_6", opus, textBlock, u2, `,"isSidechain":true`),
		claudeReplyLine("n1", "", "glm-5.3", textBlock, `{"input_tokens":100,"output_tokens":10}`),
		claudeReplyLine("n2", "", "glm-5.3", textBlock, `{"input_tokens":100,"output_tokens":10}`),
	}
	var m Meta
	var items []proto.Item
	for i, line := range lines {
		items = append(items, ParseLine(i+1, []byte(line), &m)...)
	}
	want := map[string][]proto.TokenUsage{
		"a1#0": {{Model: opus, Input: 3, Output: 90, CacheRead: 40000, CacheWrite: 1200, CacheWrite1h: 1000, Fast: true}},
		"a6#0": {{Model: opus, Input: 5, Output: 12, CacheRead: 41200}},
		"n1#0": {{Model: "glm-5.3", Input: 100, Output: 10}},
		"n2#0": {{Model: "glm-5.3", Input: 100, Output: 10}},
	}
	if got := usageOf(items); !reflect.DeepEqual(got, want) {
		t.Fatalf("usage on %v,\nwant %v", got, want)
	}
	if b, _ := json.Marshal(want["a1#0"]); string(b) != `[{"model":"claude-opus-5-5","in":3,"out":90,"cache_read":40000,"cache_write":1200,"cache_write_1h":1000,"fast":true}]` {
		t.Fatalf("wire: %s", b)
	}
}

// Claude Code up to 2.1.306 wrote the output generated so far on a reply's earlier blocks and more on each later one:
// a later line adds what grew, so a request's items add up to its last usage. A gateway that first reported nothing
// (zeros, then the real usage) is counted once its usage comes.
func TestClaudeRequestUsageGrowsAcrossLines(t *testing.T) {
	lines := []string{
		claudeReplyLine("a1", "msg_1", "claude-opus-5", thinkingBlock, `{"input_tokens":2,"cache_read_input_tokens":500,"output_tokens":4}`),
		claudeReplyLine("a2", "msg_1", "claude-opus-5", textBlock, `{"input_tokens":2,"cache_read_input_tokens":500,"output_tokens":350}`),
		claudeReplyLine("a3", "msg_1", "claude-opus-5", toolBlock("toolu_1"), `{"input_tokens":2,"cache_read_input_tokens":500,"output_tokens":350}`),
		claudeReplyLine("g1", "resp_1", "gpt-6.1-terra", thinkingBlock, `{"input_tokens":0,"output_tokens":0}`),
		claudeReplyLine("g2", "resp_1", "gpt-6.1-terra", textBlock, `{"input_tokens":800,"cache_read_input_tokens":3000,"output_tokens":60}`),
	}
	var m Meta
	var items []proto.Item
	for i, line := range lines {
		items = append(items, ParseLine(i+1, []byte(line), &m)...)
	}
	want := map[string][]proto.TokenUsage{
		"a1#0": {{Model: "claude-opus-5", Input: 2, Output: 4, CacheRead: 500}},
		"a2#0": {{Model: "claude-opus-5", Output: 346}},
		"g2#0": {{Model: "gpt-6.1-terra", Input: 800, Output: 60, CacheRead: 3000}},
	}
	if got := usageOf(items); !reflect.DeepEqual(got, want) {
		t.Fatalf("usage on %v,\nwant %v", got, want)
	}
}

// claudeSession is a transcript of n prompts, each answered by a few requests whose replies are several lines (with a
// tool's result between them, and now and then a first line that shows nothing).
func claudeSession(n int) []string {
	var lines []string
	for p := 0; p < n; p++ {
		lines = append(lines, fmt.Sprintf(`{"type":"user","uuid":"p%d","timestamp":"2026-11-12T08:00:00Z","message":{"role":"user","content":"prompt %d %s"}}`, p, p, strings.Repeat("x", 200)))
		for r := 0; r < 3; r++ {
			msg := fmt.Sprintf("msg_%d_%d", p, r)
			usage := func(out int) string {
				return fmt.Sprintf(`{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"output_tokens":%d}`, 1+r, 1000*p+r, 10*r, out)
			}
			first := thinkingBlock
			if (p+r)%4 == 0 {
				first = `{"type":"thinking","thinking":"","signature":"x"}`
			}
			call := fmt.Sprintf("toolu_%d_%d", p, r)
			lines = append(lines,
				claudeReplyLine(msg+"a", msg, "claude-opus-5-5", first, usage(5)),
				claudeReplyLine(msg+"b", msg, "claude-opus-5-5", textBlock, usage(40+p)),
				claudeReplyLine(msg+"c", msg, "claude-opus-5-5", toolBlock(call), usage(40+p)),
				claudeToolResultLine(msg+"r", call),
				claudeReplyLine(msg+"d", msg, "claude-opus-5-5", toolBlock(call+"x"), usage(41+p)),
				claudeToolResultLine(msg+"s", call+"x"),
			)
		}
	}
	return lines
}

// The usage items carry is the same whichever way the lines are read: all at once, a few at a time as a watched session
// gets them, or opened by the file's end, whose windows may start among a reply's lines (window.go seedUsage).
func TestClaudeRequestUsageSameEveryRead(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	lines := claudeSession(40)
	data := []byte(strings.Join(lines, "\n") + "\n")
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	windowBytes = 1 << 40
	full := NewFile(p)
	if _, err := full.Refresh(); err != nil {
		t.Fatal(err)
	}
	all, _, _ := full.Page(0, 1<<30)
	var sum []proto.TokenUsage
	for _, it := range all {
		for _, u := range it.Usage {
			sum = AddUsage(sum, u)
		}
	}
	// 40 prompts × 3 requests, each counted once at its last usage.
	var want proto.TokenUsage
	for p := 0; p < 40; p++ {
		for r := 0; r < 3; r++ {
			want.Input += int64(1 + r)
			want.CacheRead += int64(1000*p + r)
			want.CacheWrite += int64(10 * r)
			want.Output += int64(41 + p)
		}
	}
	want.Model = "claude-opus-5-5"
	if len(sum) != 1 || sum[0] != want {
		t.Fatalf("a full read sums %+v, want %+v", sum, want)
	}

	// Pushed: the same lines appended a few at a time.
	pushed := filepath.Join(dir, "pushed.jsonl")
	live := NewFile(pushed)
	var got []proto.Item
	for i := 0; i < len(lines); i += 5 {
		fh, err := os.OpenFile(pushed, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fh.WriteString(strings.Join(lines[i:min(i+5, len(lines))], "\n") + "\n")
		fh.Close()
		fresh, err := live.Refresh()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fresh...)
	}
	if !reflect.DeepEqual(usageOf(got), usageOf(all)) {
		t.Fatal("pushed lines carry other usage than a full read")
	}

	// Opened by the end, with windows of several sizes so that they start at lines of every kind.
	for _, div := range []int64{7, 9, 13, 23} {
		windowBytes = int64(len(data)) / div
		f := NewFile(p)
		if _, err := f.Refresh(); err != nil {
			t.Fatal(err)
		}
		if f.base == 0 {
			t.Fatal("the file was not opened by its end: the test checks nothing")
		}
		var paged []proto.Item
		for before := 0; ; {
			items, more, _ := f.PageTurns(before, 3, 2000)
			paged = append(append([]proto.Item(nil), items...), paged...)
			if !more || len(items) == 0 {
				break
			}
			before = items[0].Seq
		}
		if !reflect.DeepEqual(usageOf(paged), usageOf(all)) {
			t.Fatalf("windows of 1/%d: other usage than a full read", div)
		}
		if !bytes.Equal(marshal(t, paged), marshal(t, all)) {
			t.Fatalf("windows of 1/%d: other items than a full read", div)
		}
	}
}

// seedFixture lays lines out as a window's earlier lines: what seedUsage reads of them, and which it read whole and
// which only as far as their head.
type seedFixture struct {
	data           []byte
	starts         []int64
	whole, headsOf []int
}

func newSeedFixture(lines ...string) *seedFixture {
	s := &seedFixture{}
	for _, l := range lines {
		s.starts = append(s.starts, int64(len(s.data)))
		s.data = append(s.data, l+"\n"...)
	}
	return s
}

func (s *seedFixture) end(n int) int64 {
	if n < len(s.starts) {
		return s.starts[n]
	}
	return int64(len(s.data))
}

func (s *seedFixture) reader() seedLines {
	return seedLines{
		line: func(n int) []byte {
			s.whole = append(s.whole, n)
			return bytes.TrimRight(s.data[s.starts[n-1]:s.end(n)], "\n")
		},
		head: func(n int, most int64) []byte {
			s.headsOf = append(s.headsOf, n)
			return s.data[s.starts[n-1]:min(s.end(n), s.starts[n-1]+most)]
		},
		size: func(n int) int64 { return s.end(n) - s.starts[n-1] },
	}
}

// A window seed never reads a huge line just before it whole. A request blob (it repeats the whole conversation) and
// a subagent's line in the session's transcript are no reply: it looks past them. A reply with a huge tool input says
// its request's id near its start: that request is the one the window goes on from, and its later lines add nothing
// (Claude Code repeats a request's usage on every line of it), where looking past the line would find the request
// before and count this one twice.
func TestClaudeUsageSeedHugeLines(t *testing.T) {
	small := claudeReplyLine("a1", "msg_1", "claude-opus-5-5", textBlock, `{"input_tokens":2,"output_tokens":9}`)
	huge := `{"type":"assistant","uuid":"h","message":{"model":"claude-opus-5-5","id":"msg_2","type":"message","role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_w","name":"Write","input":{"file_path":"/w/a.txt","content":"` + strings.Repeat("x", seedUsageLine) +
		`"}}],"usage":{"input_tokens":1000,"output_tokens":9000}}}`
	blob := `{"type":"api-request-blob","message":{"role":"assistant","id":"msg_9","content":"` + strings.Repeat("x", seedUsageLine) + `"}}`
	side := strings.Replace(huge, `"uuid":"h"`, `"uuid":"h","isSidechain":true`, 1)
	for _, c := range []struct {
		name  string
		lines []string
		id    string
		got   tokenuse.Tokens
	}{
		{"request blob", []string{small, blob}, "msg_1", tokenuse.Tokens{Input: 2, Output: 9}},
		{"sidechain line", []string{small, side}, "msg_1", tokenuse.Tokens{Input: 2, Output: 9}},
		{"huge reply", []string{small, huge}, "msg_2", tokenuse.Saturated("msg_2").Got()},
	} {
		s := newSeedFixture(c.lines...)
		req, _ := (&File{}).seedUsage(s.reader(), 3, false)
		if req.ID() != c.id || req.Got() != c.got {
			t.Errorf("%s: seed %q %+v, want %q %+v", c.name, req.ID(), req.Got(), c.id, c.got)
		}
		if slices.Contains(s.whole, 2) || !reflect.DeepEqual(s.headsOf, []int{2}) {
			t.Errorf("%s: line 2 read whole %v, only its head %v", c.name, s.whole, s.headsOf)
		}
	}
	sat := tokenuse.Saturated("msg_2")
	if _, ok := sat.Add("msg_2", tokenuse.Tokens{Input: 1000, Output: 9000}); ok {
		t.Fatal("a saturated request still adds")
	}
}

// A big file whose window starts right after a request's huge first line gives the usage a full read does: the
// request counted once (#5426 review: 2030/18030 for 1030/9030).
func TestClaudeUsageWindowAfterHugeReply(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	u := func(in, out int) string { return fmt.Sprintf(`{"input_tokens":%d,"output_tokens":%d}`, in, out) }
	huge := `{"type":"assistant","uuid":"h1","timestamp":"2026-11-12T08:00:00Z","message":{"model":"claude-opus-5-5","id":"msg_r","type":"message","role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_w","name":"Write","input":{"file_path":"/w/a.txt","content":"` + strings.Repeat("x", 3*seedUsageLine) +
		`"}}],"usage":` + u(1000, 9000) + `}}`
	head := []string{
		`{"type":"user","uuid":"p1","timestamp":"2026-11-12T08:00:00Z","message":{"role":"user","content":"write it"}}`,
		claudeReplyLine("s1", "msg_s", "claude-opus-5-5", textBlock, u(30, 30)),
		huge,
	}
	tail := []string{
		claudeReplyLine("h2", "msg_r", "claude-opus-5-5", toolBlock("toolu_2"), u(1000, 9000)),
		claudeToolResultLine("t2", "toolu_2"),
		`{"type":"user","uuid":"p2","timestamp":"2026-11-12T08:00:00Z","message":{"role":"user","content":"and then"}}`,
		claudeReplyLine("z1", "msg_z", "claude-opus-5-5", textBlock, u(1, 1)),
	}
	data := []byte(strings.Join(append(head, tail...), "\n") + "\n")
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := func(items []proto.Item) (total proto.TokenUsage) {
		for _, it := range items {
			for _, x := range it.Usage {
				total.Input += x.Input
				total.Output += x.Output
			}
		}
		return total
	}
	windowBytes = 1 << 40
	full := NewFile(p)
	if _, err := full.Refresh(); err != nil {
		t.Fatal(err)
	}
	all, _, _ := full.Page(0, 1<<30)
	if got := sum(all); got.Input != 1031 || got.Output != 9031 {
		t.Fatalf("a full read sums %+v", got)
	}
	windowBytes = int64(len(strings.Join(tail[1:], "\n")) + 1) // the window starts at the request's second line
	f := NewFile(p)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if f.base != len(head) {
		t.Fatalf("the window starts after line %d, not after the huge line: the test checks nothing", f.base)
	}
	window, _, _ := f.After(len(head), 1<<30) // the window's own lines, before any older one is read
	if got := sum(window); got.Input != 1 || got.Output != 1 {
		t.Fatalf("the window's lines sum %+v: the request whose huge line is before it counted again", got)
	}
	if whole, _ := f.Range(1, f.LastSeq(), 1<<30); !reflect.DeepEqual(usageOf(whole), usageOf(all)) {
		t.Fatal("read back to the start, other usage than a full read")
	}
}

// A rollout's window seeds its running total from the latest token_count before it, even past the lines it reads one
// by one (a turn that ended with many items after its last count): a rate-limit update repeating that total at the
// window's start is no new request.
func TestCodexUsageSeedFindsADistantTokenCount(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	const ts = `"timestamp":"2026-11-12T08:00:00.000Z"`
	ev := func(payload string) string { return `{` + ts + `,"type":"event_msg","payload":` + payload + `}` }
	tokens := func(total int) string {
		return ev(fmt.Sprintf(`{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},"last_token_usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":50}}}`, total))
	}
	lines := []string{`{` + ts + `,"type":"session_meta","payload":{"id":"01a1","cwd":"/r/fake","cli_version":"0.170.0"}}`,
		ev(`{"type":"task_started","turn_id":"t0"}`), tokens(550)}
	for i := 0; i < 2*seedUsageLines; i++ {
		lines = append(lines, ev(fmt.Sprintf(`{"type":"item_completed","item":{"type":"AgentMessage","id":"m%d","content":[{"type":"Text","text":"%s"}]}}`, i, strings.Repeat("y", 200))))
	}
	lines = append(lines, ev(`{"type":"task_complete","turn_id":"t0","duration_ms":10}`))
	b := len(lines) + 1
	lines = append(lines, ev(`{"type":"task_started","turn_id":"t1"}`), tokens(550), tokens(1100),
		ev(`{"type":"task_complete","turn_id":"t1","duration_ms":10}`))
	data := []byte(strings.Join(lines, "\n") + "\n")
	p := filepath.Join(t.TempDir(), "rollout-2026-11-12T08-00-00-01a1.jsonl")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	turnUsage := func(items []proto.Item) map[string][]proto.TokenUsage { return usageOf(items) }
	windowBytes = 1 << 40
	full := NewFile(p)
	if _, err := full.Refresh(); err != nil {
		t.Fatal(err)
	}
	all, _, _ := full.Page(0, 1<<30)
	want := turnUsage(all)
	if len(want) != 2 {
		t.Fatalf("turns with usage: %+v", want)
	}
	windowBytes = int64(len(strings.Join(lines[b:], "\n")) + 1) // the window starts at the second turn
	f := NewFile(p)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if f.base != b-1 {
		t.Fatalf("the window starts after line %d, not at the second turn (line %d): the test checks nothing", f.base, b)
	}
	window, _, _ := f.Page(0, 1<<30)
	for id, u := range turnUsage(window) {
		if !reflect.DeepEqual(u, want[id]) {
			t.Fatalf("%s: windowed %+v, full %+v", id, u, want[id])
		}
	}
}

// A Codex turn's usage is the sum of its requests, on the turn_duration item its task_complete makes: a token_count
// that repeats the running total (a rate-limit update) is no new request, and the next turn starts from nothing. An
// interrupted turn's goes on its notice.
func TestCodexTurnUsage(t *testing.T) {
	const ts = `"timestamp":"2026-11-12T08:00:00.000Z"`
	ev := func(payload string) string { return `{` + ts + `,"type":"event_msg","payload":` + payload + `}` }
	tokens := func(total, in, cached, out int) string {
		return ev(fmt.Sprintf(`{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"output_tokens":1,"total_tokens":%d},"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"total_tokens":%d}},"rate_limits":null}`, total, total, in, cached, out, in+out))
	}
	item := func(id string) string {
		return ev(`{"type":"item_completed","item":{"type":"AgentMessage","id":"` + id + `","content":[{"type":"Text","text":"done"}]}}`)
	}
	lines := []string{
		`{` + ts + `,"type":"session_meta","payload":{"id":"01a1","cwd":"/r/fake","cli_version":"0.170.0"}}`,
		ev(`{"type":"task_started","turn_id":"t0"}`),
		`{` + ts + `,"type":"turn_context","payload":{"turn_id":"t0","model":"gpt-6.1"}}`,
		tokens(1000, 900, 600, 100),
		tokens(1000, 900, 600, 100), // the same total again: a rate-limit update
		item("m0"),
		tokens(2500, 1300, 1200, 200),
		ev(`{"type":"task_complete","turn_id":"t0","duration_ms":4000}`),
		ev(`{"type":"task_started","turn_id":"t1"}`),
		tokens(2500, 1300, 1200, 200), // a repeat of the last turn's last
		`{` + ts + `,"type":"turn_context","payload":{"turn_id":"t1","model":"gpt-6.1-sol"}}`,
		tokens(3000, 400, 500, 100), // cached above the input: none left uncached
		ev(`{"type":"task_complete","turn_id":"t1","duration_ms":1000}`),
		ev(`{"type":"task_started","turn_id":"t2"}`),
		tokens(3100, 80, 0, 20),
		ev(`{"type":"turn_aborted","turn_id":"t2","reason":"interrupted"}`),
		ev(`{"type":"task_started","turn_id":"t3"}`),
		ev(`{"type":"task_complete","turn_id":"t3","duration_ms":10}`),
	}
	m := Meta{Codex: true}
	var items []proto.Item
	for i, line := range lines {
		items = append(items, ParseLine(i+1, []byte(line), &m)...)
	}
	var got []string
	for _, it := range items {
		if it.Usage != nil {
			got = append(got, fmt.Sprintf("%s:%s:%+v", it.Kind, it.Text, it.Usage))
		}
	}
	want := []string{
		"turn_duration::[{ID: Model:gpt-6.1 Input:400 Output:300 CacheRead:1800 CacheWrite:0 CacheWrite1h:0 Fast:false}]",
		"turn_duration::[{ID: Model:gpt-6.1-sol Input:0 Output:100 CacheRead:500 CacheWrite:0 CacheWrite1h:0 Fast:false}]",
		"notice:" + InterruptedNotice + ":[{ID: Model: Input:80 Output:20 CacheRead:0 CacheWrite:0 CacheWrite1h:0 Fast:false}]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// codexUsageTurns is codexTurns with the token counts a rollout writes: two requests a turn, and a rate-limit update
// that repeats the running total, at the turn's start.
func codexUsageTurns(n int) []string {
	var out []string
	total := 0
	for _, line := range codexTurns(n) {
		if strings.Contains(line, `"type":"task_started"`) {
			out = append(out, line, fmt.Sprintf(`{"timestamp":"2026-11-10T08:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},"last_token_usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":50}}}}`, total))
			continue
		}
		if strings.Contains(line, `"type":"task_complete"`) {
			for i := 0; i < 2; i++ {
				total += 550
				out = append(out, fmt.Sprintf(`{"timestamp":"2026-11-10T08:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},"last_token_usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":50}}}}`, total))
			}
			line = strings.Replace(line, `"type":"task_complete",`, `"type":"task_complete","duration_ms":1000,`, 1)
		}
		out = append(out, line)
	}
	return out
}

// A rollout opened by its end gives the turns the usage a full read does: its windows start at a turn, after a
// token_count that only repeats the total before it.
func TestCodexTurnUsageFullEqualsWindowed(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	data := []byte(strings.Join(codexUsageTurns(60), "\n") + "\n")
	p := filepath.Join(t.TempDir(), "rollout-2026-11-10T08-00-00-01a1.jsonl")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	windowBytes = 1 << 40
	full := NewFile(p)
	if _, err := full.Refresh(); err != nil {
		t.Fatal(err)
	}
	all, _, _ := full.Page(0, 1<<30)
	carried := 0
	for _, it := range all {
		if it.Usage != nil {
			carried++
			if u := it.Usage[0]; u.Input != 800 || u.CacheRead != 200 || u.Output != 100 {
				t.Fatalf("turn usage %+v", u)
			}
		}
	}
	if carried != 60 {
		t.Fatalf("%d turns carry usage, want 60", carried)
	}
	windowBytes = int64(len(data)) / 8
	f := NewFile(p)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	if f.base == 0 {
		t.Fatal("the file was not opened by its end: the test checks nothing")
	}
	var got []proto.Item
	for before := 0; ; {
		items, more, _ := f.Page(before, 7)
		got = append(append([]proto.Item(nil), items...), got...)
		if !more || len(items) == 0 {
			break
		}
		before = items[0].Seq
	}
	if !bytes.Equal(marshal(t, got), marshal(t, all)) {
		t.Fatalf("a windowed read differs from a full read (%d and %d items)", len(got), len(all))
	}
}

// A fold counts what its items used: the requests summed per model and speed, a subagent's report (an id) as it is.
func TestFoldSumsUsage(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(seq int, kind, call string, usage ...proto.TokenUsage) proto.Item {
		role := "assistant"
		if kind == proto.KindToolResult {
			role = "user"
		}
		return proto.Item{ID: fmt.Sprintf("l%d#0", seq), Seq: seq, TS: ts, Role: role, Kind: kind, ToolUseID: call, Text: "x", Usage: usage}
	}
	opus := proto.TokenUsage{Model: "claude-opus-5-5", Input: 2, Output: 10, CacheRead: 100, CacheWrite: 50, CacheWrite1h: 50}
	fast := proto.TokenUsage{Model: "claude-opus-5-5", Input: 1, Output: 5, Fast: true}
	glm := proto.TokenUsage{Model: "glm-5.3", Input: 30, Output: 3}
	report := proto.TokenUsage{ID: "agent:a1", Model: "claude-opus-5-5", Input: 7, Output: 70}
	page := []proto.Item{
		mk(1, proto.KindThinking, "", opus),
		mk(2, proto.KindToolUse, "a"), mk(3, proto.KindToolResult, "a", report),
		mk(4, proto.KindThinking, "", opus),
		mk(5, proto.KindToolUse, "b", glm), mk(6, proto.KindToolResult, "b"),
		mk(7, proto.KindThinking, "", fast),
		mk(8, proto.KindToolUse, "c", opus), mk(9, proto.KindToolResult, "c", report),
	}
	out := FoldWork(page)
	if len(out) != 1 || out[0].Kind != proto.KindFold {
		t.Fatalf("not one fold: %+v", out)
	}
	want := []proto.TokenUsage{
		{Model: "claude-opus-5-5", Input: 6, Output: 30, CacheRead: 300, CacheWrite: 150, CacheWrite1h: 150},
		report, glm, fast, report,
	}
	if got := out[0].Fold.Usage; !reflect.DeepEqual(got, want) {
		t.Fatalf("fold usage %+v,\nwant %+v", got, want)
	}
	plain := FoldWork([]proto.Item{mk(1, proto.KindThinking, ""), mk(2, proto.KindToolUse, "a"), mk(3, proto.KindToolResult, "a"), mk(4, proto.KindThinking, "")})
	if len(plain) != 1 || plain[0].Fold == nil || plain[0].Fold.Usage != nil {
		t.Fatalf("a fold without usage: %+v", plain)
	}
	if b, _ := json.Marshal(plain[0].Fold); bytes.Contains(b, []byte(`"usage"`)) {
		t.Fatalf("an empty usage went on the wire: %s", b)
	}
}

// tokenuse.ClaudeReply reads a line as the node reads a subagent's transcript: sidechain lines like any other, Claude Code's own
// error lines and zeros not at all.
func TestReplyUsage(t *testing.T) {
	for _, c := range []struct {
		line string
		id   string
		ok   bool
	}{
		{claudeReplyLine("a1", "msg_1", "claude-opus-5-5", textBlock, `{"input_tokens":2,"output_tokens":9,"speed":"fast"}`, `,"isSidechain":true`), "msg_1", true},
		{claudeReplyLine("a2", "msg_2", "<synthetic>", textBlock, `{"input_tokens":2,"output_tokens":9}`), "", false},
		{claudeReplyLine("a3", "msg_3", "claude-opus-5-5", textBlock, `{"input_tokens":2,"output_tokens":9}`, `,"isApiErrorMessage":true`), "", false},
		{claudeReplyLine("a4", "msg_4", "claude-opus-5-5", textBlock, `{"input_tokens":0,"output_tokens":0}`), "", false},
		{claudeReplyLine("a5", "msg_5", "claude-opus-5-5", textBlock, ""), "", false},
		{claudeToolResultLine("r1", "toolu_1"), "", false},
	} {
		_, id, model, u, fast, ok := tokenuse.ClaudeReply([]byte(c.line))
		if ok != c.ok || ok && (id != c.id || u.Input != 2 || u.Output != 9 || !fast || model != "claude-opus-5-5") {
			t.Errorf("%s: %q %+v %v", c.line, id, u, ok)
		}
	}
}

// A subagent's own chat (its transcript read on its own, every line sidechain) carries its requests' usage, but not a
// fork's copy of its launcher's reply that launched it, before the fork's prompt: that request is the launcher's.
func TestSubagentChatUsage(t *testing.T) {
	side := `,"isSidechain":true`
	u := func(in, out int) string { return fmt.Sprintf(`{"input_tokens":%d,"output_tokens":%d}`, in, out) }
	fork := []string{
		`{"type":"fork-context-ref","agentId":"f1","parentSessionId":"s","parentLastUuid":"x","contextLength":394}`,
		claudeReplyLine("c1", "msg_launch", "claude-opus-5-5", toolBlock("toolu_fork"), u(2, 300), side),
		`{"type":"user","isSidechain":true,"uuid":"p1","timestamp":"2026-11-12T08:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_fork","content":"forked"},{"type":"text","text":"Your task: look"}]}}`,
		claudeReplyLine("o1", "msg_own", "claude-opus-5-5", thinkingBlock, u(4, 8), side),
		claudeReplyLine("o2", "msg_own", "claude-opus-5-5", textBlock, u(4, 8), side),
	}
	p := filepath.Join(t.TempDir(), "agent-f1.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(fork, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := NewSubagentFile(p)
	items, err := f.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]proto.TokenUsage{"o1#0": {{Model: "claude-opus-5-5", Input: 4, Output: 8}}}
	if got := usageOf(items); !reflect.DeepEqual(got, want) {
		t.Fatalf("fork chat usage %+v, want %+v", got, want)
	}
	// The same lines in the session's own transcript carry none.
	var m Meta
	var main []proto.Item
	for i, line := range fork {
		main = append(main, ParseLine(i+1, []byte(line), &m)...)
	}
	if got := usageOf(main); len(got) != 0 {
		t.Fatalf("sidechain lines in the session's transcript carry usage: %+v", got)
	}
}

// A subagent's chat opened by its file's end carries the usage a full read does, its window seeded as a session's is.
func TestSubagentChatUsageFullEqualsWindowed(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	var lines []string
	for _, l := range claudeSession(40) {
		lines = append(lines, strings.Replace(l, `{"type":`, `{"isSidechain":true,"type":`, 1))
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	p := filepath.Join(t.TempDir(), "agent-a1.jsonl")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	windowBytes = 1 << 40
	full := NewSubagentFile(p)
	if _, err := full.Refresh(); err != nil {
		t.Fatal(err)
	}
	all, _, _ := full.Page(0, 1<<30)
	var out, want int64
	for _, it := range all {
		for _, u := range it.Usage {
			out += u.Output
		}
	}
	for p := 0; p < 40; p++ {
		want += 3 * int64(41+p) // 3 requests a prompt, each at its last usage
	}
	if out != want {
		t.Fatalf("a full read sums output %d, want %d", out, want)
	}
	for _, div := range []int64{7, 13} {
		windowBytes = int64(len(data)) / div
		f := NewSubagentFile(p)
		if _, err := f.Refresh(); err != nil {
			t.Fatal(err)
		}
		if f.base == 0 {
			t.Fatal("the file was not opened by its end: the test checks nothing")
		}
		whole, _ := f.Range(1, f.LastSeq(), 1<<30)
		if !reflect.DeepEqual(usageOf(whole), usageOf(all)) {
			t.Fatalf("windows of 1/%d: other usage than a full read", div)
		}
	}
}

// Windows that start on either side of a request's second line, its first one huge, page back to the same usage as a
// full read (from the #5426 review's reproduction).
func TestClaudeUsageWindowsAroundHugeReply(t *testing.T) {
	defer func(w int64) { windowBytes = w }(windowBytes)
	huge := `{"type":"tool_use","id":"toolu_w","name":"Write","input":{"file_path":"/w/a.txt","content":"` + strings.Repeat("y", 300<<10) + `"}}`
	big := `{"input_tokens":1000,"cache_read_input_tokens":50000,"output_tokens":9000}`
	var lines []string
	for p := 0; p < 30; p++ {
		lines = append(lines, fmt.Sprintf(`{"type":"user","uuid":"p%d","timestamp":"2026-11-12T08:00:00Z","message":{"role":"user","content":"prompt %s"}}`, p, strings.Repeat("x", 200)),
			claudeReplyLine(fmt.Sprintf("q%d", p), fmt.Sprintf("msg_q%d", p), "claude-opus-5-5", textBlock, `{"input_tokens":1,"output_tokens":1}`))
		if p == 20 {
			lines = append(lines,
				claudeReplyLine("h1", "msg_h", "claude-opus-5-5", huge, big),
				claudeReplyLine("h2", "msg_h", "claude-opus-5-5", toolBlock("toolu_b"), big),
				claudeToolResultLine("hr1", "toolu_w"),
				claudeToolResultLine("hr2", "toolu_b"))
		}
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	windowBytes = 1 << 40
	full := NewFile(p)
	if _, err := full.Refresh(); err != nil {
		t.Fatal(err)
	}
	all, _, _ := full.Page(0, 1<<30)
	h2 := bytes.LastIndexByte(data[:bytes.Index(data, []byte(`"uuid":"h2"`))], '\n') + 1
	for _, delta := range []int{-1, 0, 1, 50} {
		windowBytes = int64(len(data) - h2 + delta)
		f := NewFile(p)
		if _, err := f.Refresh(); err != nil {
			t.Fatal(err)
		}
		var paged []proto.Item
		for before := 0; ; {
			items, more, _ := f.PageTurns(before, 3, 2000)
			paged = append(append([]proto.Item(nil), items...), paged...)
			if !more || len(items) == 0 {
				break
			}
			before = items[0].Seq
		}
		if !reflect.DeepEqual(usageOf(paged), usageOf(all)) {
			t.Errorf("windows of %d bytes: other usage than a full read", windowBytes)
		}
	}
}
