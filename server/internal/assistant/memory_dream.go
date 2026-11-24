package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	memoryDreamKey           = "assistant.memory_dream"
	memoryDreamBatchBytes    = 12000
	memoryDreamMessageBytes  = 3000
	memoryDreamExcerptSuffix = "\n[Message truncated]"
)

type memoryDreamProgress struct {
	Machine   string
	Session   string
	Seq       int
	History   string
	CheckedAt time.Time
}

func (s *Service) memoryDreamLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run, cancel := context.WithTimeout(ctx, 90*time.Second)
			s.memoryDream(run)
			cancel()
		}
	}
}

// memoryDream checks only the current main conversation, once per day when it has new user text.
func (s *Service) memoryDream(ctx context.Context) {
	h := s.hello(ctx)
	if !h.Assistant.MemoryDreaming || h.Assistant.Standby {
		return
	}
	st := s.store()
	if st == nil {
		return
	}
	cfg := s.settings(ctx)
	machine := s.confirmedMainMachine(ctx, cfg)
	if !s.sameMachine(machine, cfg.MachineID) || s.isWorkMachine(machine) {
		return
	}
	chat, ok := s.session(ctx, machine, cfg.Main)
	if !ok || !chat.Assistant || spaceOf(chat) != "" || (chat.Status != proto.StatusDone && chat.Status != proto.StatusIdle) ||
		chat.State == proto.StateWorking || chat.State == proto.StateNeedsYou || chat.PendingInteractions > 0 {
		return
	}
	d, ok := s.device(ctx, machine)
	if !ok || !d.Online && !d.Reachable {
		return
	}
	cwd := s.spaceCwd(ctx, d, "")
	if cwd == "" {
		return
	}

	var progress memoryDreamProgress
	if _, err := st.Setting(ctx, memoryDreamKey, &progress); err != nil {
		s.logf("memory dreaming: read progress: %v", err)
		return
	}
	if time.Since(progress.CheckedAt) < 24*time.Hour {
		return
	}
	if progress.Machine != machine || progress.Session != cfg.Main {
		progress.Seq, progress.History = 0, ""
	}
	var page struct {
		Items   []proto.Item `json:"items"`
		History string       `json:"history"`
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": cfg.Main, "turns": 8, "lazy_images": true}, &page); err != nil {
		return
	}
	if page.History != "" {
		if progress.History != "" && progress.History != page.History {
			progress.Seq = 0
		}
		progress.History = page.History
	}
	messages, seq := memoryDreamMessages(page.Items, progress.Seq)
	if len(messages) == 0 {
		return
	}

	var snapshot struct {
		Note  *string `json:"note"`
		Index string  `json:"index"`
	}
	if err := s.call(ctx, machine, DeviceAssistantMemory, map[string]any{"op": "dream-read", "cwd": cwd}, &snapshot); err != nil {
		s.logf("memory dreaming: read memory: %v", err)
		return
	}
	input, _ := json.Marshal(struct {
		Current  *string  `json:"current_note"`
		Index    string   `json:"memory_index"`
		Messages []string `json:"user_messages"`
	}{snapshot.Note, snapshot.Index, messages})

	// Throttle failed attempts as well. Checkpoint the input only after processing it.
	progress.Machine, progress.Session, progress.CheckedAt = machine, cfg.Main, time.Now()
	if err := st.PutSetting(ctx, memoryDreamKey, progress); err != nil {
		s.logf("memory dreaming: save progress: %v", err)
		return
	}
	var result struct {
		Body string `json:"body"`
	}
	if err := s.purposeModel("kb").JSONMax(ctx, prompts.RenderMemoryDream(), string(input), 1800, &result); err != nil {
		if ctx.Err() == nil && !ai.IsPaused(err) {
			s.logf("memory dreaming: model request failed")
		}
		return
	}
	body := strings.TrimSpace(result.Body)
	if len(body) > 4000 {
		return
	}
	if body != "" {
		current := s.settings(ctx)
		if current.Main != cfg.Main || !s.sameMachine(current.MachineID, machine) ||
			!s.sameMachine(s.confirmedMainMachine(ctx, current), machine) ||
			!s.hello(ctx).Assistant.MemoryDreaming || s.isWorkMachine(machine) || s.sessionBusy(ctx, machine, cfg.Main) {
			return
		}
		if err := s.call(ctx, machine, DeviceAssistantMemory, map[string]any{
			"op": "dream-write", "cwd": cwd, "previous": snapshot.Note, "previous_index": snapshot.Index, "content": body,
		}, nil); err != nil {
			s.logf("memory dreaming: write memory: %v", err)
			return
		}
	}
	progress.Seq = seq
	if err := st.PutSetting(ctx, memoryDreamKey, progress); err != nil {
		s.logf("memory dreaming: checkpoint progress: %v", err)
	}
}

// session.items returns messages in sequence order. Take a prefix of eligible sequence groups so the
// checkpoint cannot advance past a deferred group. The byte limit is soft within a group: all blocks
// sharing an accepted sequence must be processed before its checkpoint advances.
func memoryDreamMessages(items []proto.Item, after int) ([]string, int) {
	var messages []string
	seq, groupSeq := after, after
	left := memoryDreamBatchBytes
	for _, it := range items {
		if it.Seq <= after || it.Role != "user" || it.FullSize > 0 || it.Wake || it.Source != nil || it.Task != nil || it.Compact != nil || it.ToolUseID != "" {
			continue
		}
		// extensionMessage also rejects cross-conversation text and Persona injections,
		// including legacy prompts that carry no Source or Injected metadata.
		msg, ok := extensionMessage(it)
		if !ok {
			continue
		}
		text := msg.Text
		if len(text) > memoryDreamMessageBytes {
			text = clipBytes(text, memoryDreamMessageBytes-len(memoryDreamExcerptSuffix)) + memoryDreamExcerptSuffix
		}
		if it.Seq != groupSeq && (left <= 0 || len(text) > left) {
			break
		}
		messages = append(messages, text)
		seq, groupSeq = max(seq, it.Seq), it.Seq
		left -= len(text)
	}
	return messages, seq
}
