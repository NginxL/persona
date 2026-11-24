package assistant

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A dispatched task's first message carries the memory entries the task needs (#13532, scene 3, docs/assistant/
// health.md "Memory by scene"): general ones its words bear on, and sensitive ones only when the owner confirmed the
// start. That is a plan whose words were tagged with every one of the entry's categories, which therefore waited for
// the owner's "Start" on its card (task_plan, PlanTask.MemoryTopic), and the owner tapped it (OwnerStarted). A leader's
// workers, a task_spawn, a plan the hub started without asking or one whose words were general carry none: no start
// of theirs passed a confirmation. Nothing here asks a model. The to-do's brief lists the entries carried (a memory
// event), once the start went through.

// handOffMemory is what task t, handed to machine, carries of the ledger and the memory extension's entries: entries
// for any executor that apply on machine, personal ones (every one of the extension's) only on the owner's own
// computer, none on a work computer.
func (s *Service) handOffMemory(ctx context.Context, t PlanTask, machine string) assembled {
	if s.memoryEntrySrc() == nil && s.memoryExtension == nil || s.isWorkMachine(machine) {
		return assembled{}
	}
	personal := machine != "" && machine == s.settings(ctx).MachineID
	p := proto.AssistantEntriesParams{Scope: &proto.AssistantEntryScopeFilter{Executor: "any", Machine: []string{machine}}}
	entries, err := s.memoryEntriesFor(ctx, p, personal)
	if err != nil {
		s.logf("task %s: memory: %v", t.TodoID, err)
	}
	all, info := s.memorySources(ctx, entries, s.memoryExtensionEntries(ctx, personal))
	if len(all) == 0 {
		return assembled{}
	}
	f := assembleFor{Role: assembleHandOff, Query: strings.TrimSpace(t.Title + "\n" + t.Prompt)}
	if t.OwnerStarted && t.Parent == "" && len(t.MemoryTopic) > 0 {
		f.Topic, f.TopicKnown = t.MemoryTopic, true
	}
	return assembleEntries(all, f, info)
}

// handOffMemoryContext is the context block of the entries task t carries to machine, empty when none, and the
// entries themselves: the caller notes them in the to-do's brief once the start went through (noteHandOffMemory).
func (s *Service) handOffMemoryContext(ctx context.Context, t PlanTask, machine string) (string, assembled) {
	a := s.handOffMemory(ctx, t, machine)
	if len(a.Entries) == 0 {
		return "", assembled{}
	}
	return s.renderMemoryEntries(ctx, a), a
}

// renderMemoryEntries is the context block of a's entries, in the answer language.
func (s *Service) renderMemoryEntries(ctx context.Context, a assembled) string {
	d := prompts.MemoryEntries{Language: answerLanguage(s.hello(ctx).Locale)}
	for i, e := range a.Entries {
		d.Entries = append(d.Entries, prompts.MemoryEntry{N: i + 1, Text: contextText(e.Text)})
	}
	return prompts.RenderMemoryEntries(d)
}

// noteHandOffMemory writes into to-do todoID's brief which entries its first message carried, once the start went
// through, and once per set: a start tried again with the same entries notes nothing more. A to-do with no brief yet keeps no note (logged).
func (s *Service) noteHandOffMemory(ctx context.Context, todoID string, a assembled) {
	brief, ok := s.briefOfTodo(todoID)
	if todoID == "" || !ok {
		s.logf("task %s: memory: %d entries carried, no brief to note them in", todoID, len(a.Entries))
		return
	}
	ids, sensitive := a.IDs(), slices.Clone(a.Sensitive)
	if _, _, err := s.changeBrief(ctx, brief.ID, func(tb *taskBrief) bool {
		for i := len(tb.st.Log) - 1; i >= 0; i-- {
			if e := tb.st.Log[i]; e.Kind == briefEventMemory && e.Todo == todoID {
				if slices.Equal(e.Entries, ids) {
					return false
				}
				break
			}
		}
		tb.st.Log = append(tb.st.Log, BriefEvent{At: time.Now().UTC(), Kind: briefEventMemory, Todo: todoID, Entries: ids,
			SensitiveEntries: sensitive})
		return true
	}); err != nil {
		s.logf("task %s: memory: note in brief: %v", todoID, err)
	}
}
