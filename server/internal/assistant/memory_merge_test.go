package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// mergeTestHub answers the node's side of assistant.memory: the merges it has recorded, and the report's write.
type mergeTestHub struct {
	*fakeHub
	runs     []memoryMergeRun
	byDevice map[string][]memoryMergeRun // when set, each computer's own runs
	err      error
	writes   []struct{ Device, Cwd, Name, Content string }
}

func (h *mergeTestHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method != DeviceAssistantMemory {
		return h.fakeHub.DeviceCall(ctx, p)
	}
	var op struct{ Op, Cwd, Name, Content string }
	if err := json.Unmarshal(p.Params, &op); err != nil {
		return nil, err
	}
	switch op.Op {
	case "merges":
		if h.err != nil {
			return nil, h.err
		}
		if h.byDevice != nil {
			return json.Marshal(map[string]any{"runs": h.byDevice[p.Device]})
		}
		return json.Marshal(map[string]any{"runs": h.runs})
	case "write":
		h.writes = append(h.writes, struct{ Device, Cwd, Name, Content string }{p.Device, op.Cwd, op.Name, op.Content})
		return json.RawMessage(`{"ok":true}`), nil
	}
	return nil, errors.New("unexpected memory op " + op.Op)
}

// What the node merged reaches the user once: a report among the notes of the Memory page, with both sides of every
// conflict, and one push. A later run is told again, with the earlier ones still in the report.
func TestMemoryMergeIsToldOnce(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	f.devices = []Device{{ID: "server", Online: true, Home: "/home/example"}}
	f.hello.Locale = "en"
	h := &mergeTestHub{fakeHub: f}
	st := &memStore{}
	s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return st }, Logf: testLogf(t)}))
	setSettings(t, s, func(v *Settings) {
		v.Main, v.MainMachineID, v.MachineID, v.Timezone = "main", "server", "server", "UTC"
	})
	pushes := func() []Notification {
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]Notification(nil), f.pushes...)
	}

	s.memoryMergeTell(ctx) // nothing merged: nothing said
	h.err = errors.New("unknown memory op merges")
	s.memoryMergeTell(ctx) // a node that does not know the request yet
	h.err = nil
	if len(h.writes) != 0 || len(pushes()) != 0 {
		t.Fatalf("told with nothing merged: %+v %+v", h.writes, pushes())
	}

	at := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
	h.runs = []memoryMergeRun{{
		ID: "20261117T093000Z", At: at, Backup: "/data/memory-merge/key/20261117T093000Z", Added: []string{"tunnel.md", "tea.md"},
		Conflicts: []memoryMergeConflict{
			{Name: "scope.md", Kept: "copy", MemoryModified: at.Add(-48 * time.Hour), CopyModified: at.Add(-24 * time.Hour), Other: "skip the guards\n```sh\nmake\n```\n"},
			{Name: "tone.md", Kept: "memory", MemoryModified: at.Add(-2 * time.Hour), CopyModified: at.Add(-72 * time.Hour), Other: "be formal", Truncated: true},
		},
	}}
	s.memoryMergeTell(ctx)
	s.memoryMergeTell(ctx)
	if len(h.writes) != 1 || h.writes[0].Name != memoryMergeReportName || h.writes[0].Cwd == "" {
		t.Fatalf("report writes %+v", h.writes)
	}
	report := h.writes[0].Content
	for _, want := range []string{
		"---\nname: Memory merge report\n", "## Merged on 2026-11-17 09:30", "`/data/memory-merge/key/20261117T093000Z`",
		"### Notes brought in (2)\n\n- `tunnel.md`\n- `tea.md`\n", "### Notes that differed (2)",
		"#### `scope.md`\n\nKept: the other copy's version, modified 2026-11-16 09:30. It replaced this memory's version, modified 2026-11-15 09:30.",
		"````text\nskip the guards\n```sh\nmake\n```\n````\n", // a note with a fence of its own is quoted whole
		"#### `tone.md`\n\nKept: this memory's version, modified 2026-11-17 07:30. The other copy's version, modified 2026-11-14 09:30, was not used.",
		"```text\nbe formal\n```\n\nThe text above is cut short",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
	got := pushes()
	if len(got) != 1 || got[0].SessionID != "main" || got[0].DeviceID != "server" || got[0].Level != "P1" ||
		!strings.Contains(got[0].Body, "other copy: 2.") || !strings.Contains(got[0].Body, "differed: 2.") {
		t.Fatalf("pushes %+v", got)
	}

	// A later run (a conversation that was still writing to its profile's copy): told again, the first run kept below it.
	h.runs = append(h.runs, memoryMergeRun{ID: "20261118T080000Z", At: at.Add(24 * time.Hour), Backup: "/data/memory-merge/key/20261118T080000Z", Added: []string{"late.md"}})
	f.mu.Lock()
	f.hello.Locale = "zh-Hans"
	f.mu.Unlock()
	s.memoryMergeTell(ctx)
	s.memoryMergeTell(ctx)
	if len(h.writes) != 2 || len(pushes()) != 2 {
		t.Fatalf("after a second run: %d writes, %d pushes", len(h.writes), len(pushes()))
	}
	report = h.writes[1].Content
	first, second := strings.Index(report, "2026-11-18 09:30 的合并"), strings.Index(report, "2026-11-17 09:30 的合并")
	if first < 0 || second < first || !strings.Contains(report, "name: 记忆合并清单") || !strings.Contains(report, "并进来的条目（1）\n\n- `late.md`") {
		t.Errorf("the second report:\n%s", report)
	}
	if body := pushes()[1].Body; !strings.Contains(body, "并进来 1 条") || !strings.Contains(body, "不一样的 0 条") {
		t.Errorf("the second push counts only the new run: %q", body)
	}

	// Never on a computer marked for work, and not by a standby manager.
	h.runs = append(h.runs, memoryMergeRun{ID: "20261119T080000Z", At: at.Add(48 * time.Hour), Added: []string{"more.md"}})
	f.mu.Lock()
	f.hello.Assistant.Standby = true
	f.mu.Unlock()
	s.memoryMergeTell(ctx)
	if len(h.writes) != 2 || len(pushes()) != 2 {
		t.Fatal("a standby manager told the user")
	}
}

// Moving the assistant to another computer and back: each computer's runs are told once, by that computer's own
// progress. Runs made on the first computer while the assistant was away are told on return, and the ones told before
// are not told again.
func TestMemoryMergeIsToldOncePerComputer(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	f.devices = []Device{{ID: "server", Online: true, Home: "/home/example"}, {ID: "laptop", Online: true, Home: "/home/example"}}
	f.hello.Locale = "en"
	at := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
	h := &mergeTestHub{fakeHub: f, byDevice: map[string][]memoryMergeRun{
		"server": {{ID: "20261117T093000Z", At: at, Added: []string{"a.md"}}},
		"laptop": {{ID: "20261116T080000Z", At: at.Add(-24 * time.Hour), Added: []string{"b.md", "c.md"}}},
	}}
	st := &memStore{}
	s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return st }, Logf: testLogf(t)}))
	on := func(machine string) { // the hub names the assistant's computer
		f.mu.Lock()
		f.hello.Assistant.MachineID = machine
		f.mu.Unlock()
		setSettings(t, s, func(v *Settings) {
			v.Main, v.MainMachineID, v.MachineID, v.Timezone = "main", machine, machine, "UTC"
		})
	}
	pushes := func() []Notification {
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]Notification(nil), f.pushes...)
	}

	on("server")
	s.memoryMergeTell(ctx)
	on("laptop")
	s.memoryMergeTell(ctx)
	// Back on the server, which merged once more meanwhile.
	h.byDevice["server"] = append(h.byDevice["server"], memoryMergeRun{ID: "20261118T080000Z", At: at.Add(24 * time.Hour), Added: []string{"d.md"}})
	on("server")
	s.memoryMergeTell(ctx)
	s.memoryMergeTell(ctx)
	on("laptop")
	s.memoryMergeTell(ctx) // nothing new there

	got := pushes()
	if len(got) != 3 || len(h.writes) != 3 {
		t.Fatalf("%d pushes, %d reports, want 3 each: %+v", len(got), len(h.writes), got)
	}
	for i, want := range []struct{ device, body string }{{"server", "other copy: 1."}, {"laptop", "other copy: 2."}, {"server", "other copy: 1."}} {
		if got[i].DeviceID != want.device || !strings.Contains(got[i].Body, want.body) || h.writes[i].Device != want.device {
			t.Errorf("push %d: %s %q (report on %s), want %s %q", i, got[i].DeviceID, got[i].Body, h.writes[i].Device, want.device, want.body)
		}
	}
}
