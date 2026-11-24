package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The assistant has one memory (#9440): its computer's node merges the copies other Claude profiles hold into it
// (node/memory_merge.go) and keeps what each run did. Here the user is told: every run that brought a note in or met
// a conflict is written into memory-merge.md in the memory directory, which the Memory page lists like any note (it
// is not in MEMORY.md, so no conversation loads it), and one push says it is there. The user changes a kept note on
// that page, or asks the assistant to take the other version, which the report quotes in full.

const (
	memoryMergeKey        = "assistant.memory_merge"
	memoryMergeReportName = "memory-merge.md"
	memoryMergeEvery      = 10 * time.Minute
)

// memoryMergeProgress is, for each computer the assistant has run on, the last of its runs the user was told about.
// Each computer keeps its own: run ids are times on that computer, and moving the assistant to another one and back
// tells the runs made meanwhile, and only those.
type memoryMergeProgress struct {
	Told map[string]string `json:"told_by_machine"`
}

type memoryMergeConflict struct {
	Name           string    `json:"name"`
	Kept           string    `json:"kept"` // "copy": the other copy's version replaced the memory's; "memory": the memory's stayed
	MemoryModified time.Time `json:"memory_modified"`
	CopyModified   time.Time `json:"copy_modified"`
	Other          string    `json:"other"` // the version that was not kept
	Truncated      bool      `json:"truncated"`
}

type memoryMergeRun struct {
	ID        string                `json:"id"`
	At        time.Time             `json:"at"`
	Backup    string                `json:"backup"`
	Added     []string              `json:"added"`
	Conflicts []memoryMergeConflict `json:"conflicts"`
}

func (s *Service) memoryMergeLoop(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			run, cancel := context.WithTimeout(ctx, 30*time.Second)
			s.memoryMergeTell(run)
			cancel()
			timer.Reset(memoryMergeEvery)
		}
	}
}

// memoryMergeTell asks the assistant's computer for its merges and, when there is one the user has not been told
// about, writes the report and pushes once. An older node does not know the request: nothing is told until it does.
func (s *Service) memoryMergeTell(ctx context.Context) {
	if s.hello(ctx).Assistant.Standby {
		return
	}
	st := s.store()
	if st == nil {
		return
	}
	cfg := s.settings(ctx)
	if cfg.MachineID == "" || s.isWorkMachine(cfg.MachineID) {
		return
	}
	d, ok := s.device(ctx, cfg.MachineID)
	if !ok || !d.Online && !d.Reachable {
		return
	}
	cwd := s.spaceCwd(ctx, d, "")
	if cwd == "" {
		return
	}
	var res struct {
		Runs []memoryMergeRun `json:"runs"`
	}
	if err := s.call(ctx, d.ID, DeviceAssistantMemory, map[string]any{"op": "merges", "cwd": cwd}, &res); err != nil || len(res.Runs) == 0 {
		return
	}
	var progress memoryMergeProgress
	if _, err := st.Setting(ctx, memoryMergeKey, &progress); err != nil {
		s.logf("memory merge: read progress: %v", err)
		return
	}
	told := progress.Told[d.ID]
	added, conflicts, last := 0, 0, told
	for _, run := range res.Runs {
		if run.ID > told {
			added, conflicts = added+len(run.Added), conflicts+len(run.Conflicts)
			last = max(last, run.ID)
		}
	}
	if last == told {
		return
	}
	l := s.lang(ctx)
	report := memoryMergeReport(l, s.location(ctx), res.Runs)
	if err := s.call(ctx, d.ID, DeviceAssistantMemory, map[string]any{"op": "write", "cwd": cwd, "name": memoryMergeReportName, "content": report}, nil); err != nil {
		s.logf("memory merge: write the report: %v", err)
		return
	}
	if progress.Told == nil {
		progress.Told = map[string]string{}
	}
	progress.Told[d.ID] = last
	if err := st.PutSetting(ctx, memoryMergeKey, progress); err != nil {
		s.logf("memory merge: record what was told: %v", err)
		return
	}
	s.push(withCard(ctx, cardMemoryMerge), Notification{Subtitle: "Persona", DeviceID: d.ID, SessionID: cfg.Main, Level: assistantwire.LevelP1,
		Title: i18n.T(l, "assistant-memory.merge_push_title"),
		Body:  i18n.T(l, "assistant-memory.merge_push_body", i18n.Args{"added": added, "conflicts": conflicts})})
}

// memoryMergeReport is memory-merge.md: every run, the newest first.
func memoryMergeReport(l i18n.Lang, loc *time.Location, runs []memoryMergeRun) string {
	when := func(t time.Time) string { return t.In(loc).Format("2006-01-02 15:04") }
	var b strings.Builder
	b.WriteString("---\nname: " + i18n.T(l, "assistant-memory.merge_report_title") + "\ndescription: " +
		i18n.T(l, "assistant-memory.merge_report_description") + "\nmetadata:\n  type: reference\n---\n\n")
	b.WriteString(i18n.T(l, "assistant-memory.merge_report_intro") + "\n")
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		b.WriteString("\n## " + i18n.T(l, "assistant-memory.merge_report_run", i18n.Args{"time": when(run.At)}) + "\n\n")
		b.WriteString(i18n.T(l, "assistant-memory.merge_report_backup", i18n.Args{"path": "`" + run.Backup + "`"}) + "\n")
		if len(run.Added) > 0 {
			b.WriteString("\n### " + i18n.T(l, "assistant-memory.merge_report_added", i18n.Args{"count": len(run.Added)}) + "\n\n")
			for _, name := range run.Added {
				b.WriteString("- `" + name + "`\n")
			}
		}
		if len(run.Conflicts) > 0 {
			b.WriteString("\n### " + i18n.T(l, "assistant-memory.merge_report_conflicts", i18n.Args{"count": len(run.Conflicts)}) + "\n")
		}
		for _, c := range run.Conflicts {
			key := "assistant-memory.merge_report_kept_memory"
			if c.Kept == "copy" {
				key = "assistant-memory.merge_report_kept_copy"
			}
			b.WriteString("\n#### `" + c.Name + "`\n\n")
			b.WriteString(i18n.T(l, key, i18n.Args{"memory_time": when(c.MemoryModified), "copy_time": when(c.CopyModified)}) + "\n\n")
			b.WriteString(i18n.T(l, "assistant-memory.merge_report_other") + "\n\n")
			fence := memoryMergeFence(c.Other)
			b.WriteString(fence + "text\n" + strings.TrimRight(c.Other, "\n") + "\n" + fence + "\n")
			if c.Truncated {
				b.WriteString("\n" + i18n.T(l, "assistant-memory.merge_report_truncated") + "\n")
			}
		}
	}
	return b.String()
}

// memoryMergeFence is a code fence longer than any run of backticks in text, so a note that has fences of its own
// is quoted whole.
func memoryMergeFence(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}
