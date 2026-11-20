package kb

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	ProjectMemoryOutcome       = "outcome"
	ProjectMemoryDecision      = "decision"
	ProjectMemoryPitfall       = "pitfall"
	ProjectMemoryAgentReported = "agent_reported"
)

// ProjectMemoryRecord is one worker outcome, decision or pitfall in a project's memory.jsonl. Turn is the transcript
// sequence that opens the turn and ItemIndex the outcome item's offset in it; together with the todo and agent they
// form the base ID. Decision and pitfall IDs add a kind and index. Optional fields keep M1 records readable: an empty
// Type is a legacy outcome, whose Decision and Reason describe one agent-reported decision.
type ProjectMemoryRecord struct {
	ID           string   `json:"id"`
	TodoID       string   `json:"todo_id"`
	Agent        string   `json:"agent"`
	Turn         int      `json:"turn"`
	ItemIndex    int      `json:"item_index"`
	Type         string   `json:"type,omitempty"`
	Conclusion   string   `json:"conclusion"`
	Links        []string `json:"links"`
	Question     string   `json:"question,omitempty"`
	Decision     string   `json:"decision"`
	Reason       string   `json:"reason"`
	Verification string   `json:"verification,omitempty"`
	RecordedAt   string   `json:"recorded_at,omitempty"`
	SupersedesID string   `json:"supersedes_id,omitempty"`
}

// ProjectMemoryDispatch is the bounded, newest-first project context for a dispatch. Decisions superseded by a newer
// record are omitted; distinct IDs are never merged, even when their questions or choices conflict.
type ProjectMemoryDispatch struct {
	Outcomes  []ProjectMemoryRecord
	Decisions []ProjectMemoryRecord
	Pitfalls  []ProjectMemoryRecord
}

// AppendProjectMemory appends record to a project's memory.jsonl unless its ID is already there. The disk file is the
// dedupe record, so replay after reopening the Engine is safe and a failed append claims nothing.
func (e *Engine) AppendProjectMemory(slug string, record ProjectMemoryRecord) error {
	return e.AppendProjectMemoryBatch(slug, []ProjectMemoryRecord{record})
}

// AppendProjectMemoryBatch appends one worker outcome batch under Store.mu. The last record is the base outcome and
// seals the batch: another completion route that sees it must not splice a different extraction into the same IDs.
// A retry with no base outcome fills only missing IDs. An unterminated torn tail is separated by a newline before the
// new records, so a malformed fragment can never consume a repaired record.
func (e *Engine) AppendProjectMemoryBatch(slug string, records []ProjectMemoryRecord) error {
	if len(records) == 0 {
		return nil
	}
	s := e.Store
	if s.readOnly {
		return ErrReadOnly
	}
	raw := make([][]byte, len(records))
	ids := map[string]bool{}
	for i, record := range records {
		if record.ID == "" {
			return errors.New("project memory record has no id")
		}
		if ids[record.ID] {
			return errors.New("project memory batch has duplicate id " + record.ID)
		}
		ids[record.ID] = true
		encoded, err := json.Marshal(record)
		if err != nil {
			return err
		}
		raw[i] = encoded
	}
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if err := s.checkProjectDetailWrite(slug, "project memory"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.projectMemoryIDsLocked(slug)
	if err != nil {
		return err
	}
	if existing[records[len(records)-1].ID] {
		return nil
	}
	var payload bytes.Buffer
	for i, record := range records {
		if existing[record.ID] {
			continue
		}
		payload.Write(raw[i])
		payload.WriteByte('\n')
		existing[record.ID] = true
	}
	if payload.Len() == 0 {
		return nil
	}
	return appendProjectMemoryFile(s.path(KindProject, slug, "memory.jsonl"), payload.Bytes())
}

// projectMemoryIDsLocked reads every valid durable record ID. Callers hold s.mu.
func (s *Store) projectMemoryIDsLocked(slug string) (map[string]bool, error) {
	ids := map[string]bool{}
	f, err := os.Open(s.path(KindProject, slug, "memory.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return ids, nil
	}
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	scanErr := linesBack(f, info.Size(), logChunk, func(line []byte) bool {
		var record ProjectMemoryRecord
		if len(bytes.TrimSpace(line)) != 0 && json.Unmarshal(line, &record) == nil && record.ID != "" {
			ids[record.ID] = true
		}
		return true
	})
	closeErr := f.Close()
	if scanErr != nil {
		return nil, scanErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return ids, nil
}

func appendProjectMemoryFile(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err == nil && info.Size() > 0 {
		last := []byte{0}
		_, err = f.ReadAt(last, info.Size()-1)
		if err == nil && last[0] != '\n' {
			payload = append([]byte{'\n'}, payload...)
		}
	}
	if err == nil {
		_, err = f.Write(payload)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// ProjectMemoryHas reports whether a project's durable memory already holds id. An unreadable file counts as absent;
// the append that follows reports the error instead.
func (e *Engine) ProjectMemoryHas(slug, id string) bool {
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	has, err := s.projectMemoryHasLocked(slug, id)
	return err == nil && has
}

// projectMemoryHasLocked reads backwards until it finds id. Callers hold s.mu.
func (s *Store) projectMemoryHasLocked(slug, id string) (bool, error) {
	f, err := os.Open(s.path(KindProject, slug, "memory.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return false, err
	}
	found := false
	scanErr := linesBack(f, info.Size(), logChunk, func(line []byte) bool {
		var record ProjectMemoryRecord
		if len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &record) != nil {
			return true
		}
		found = record.ID == id
		return !found
	})
	closeErr := f.Close()
	if scanErr != nil {
		return false, scanErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	return found, nil
}

// ProjectMemory returns a project's latest memory records, newest first, reading only enough of the JSONL tail to
// satisfy limit. A missing or unreadable file has no records, like the project's other bounded text readers.
func (e *Engine) ProjectMemory(slug string, limit int) []ProjectMemoryRecord {
	if limit <= 0 {
		return nil
	}
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path(KindProject, slug, "memory.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	var out []ProjectMemoryRecord
	_ = linesBack(f, info.Size(), logChunk, func(line []byte) bool {
		var record ProjectMemoryRecord
		if len(bytes.TrimSpace(line)) != 0 && json.Unmarshal(line, &record) == nil {
			out = append(out, record)
		}
		return len(out) < limit
	})
	return out
}

// ProjectMemoryForDispatch reads backwards until each requested quota is filled or the file ends. ok is false when the
// file is missing, unreadable, empty, or has no usable records, allowing old projects to retain their legacy prompt.
func (e *Engine) ProjectMemoryForDispatch(slug string, outcomesMost, decisionsMost, pitfallsMost int) (out ProjectMemoryDispatch, ok bool) {
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path(KindProject, slug, "memory.jsonl"))
	if err != nil {
		return out, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out, false
	}
	superseded := map[string]bool{}
	_ = linesBack(f, info.Size(), logChunk, func(line []byte) bool {
		var record ProjectMemoryRecord
		if len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &record) != nil {
			return true
		}
		if record.SupersedesID != "" {
			superseded[record.SupersedesID] = true
		}
		switch record.Type {
		case "", ProjectMemoryOutcome:
			ok = true
			if len(out.Outcomes) < outcomesMost {
				out.Outcomes = append(out.Outcomes, record)
			}
			// M1 stored one decision on its outcome row instead of a separate typed record.
			if record.Decision != "" && len(out.Decisions) < decisionsMost && !superseded[record.ID] {
				decision := record
				decision.ID = record.ID + ":legacy-decision"
				decision.Type = ProjectMemoryDecision
				decision.Conclusion = ""
				decision.Links = nil
				if !superseded[decision.ID] {
					out.Decisions = append(out.Decisions, decision)
				}
			}
		case ProjectMemoryDecision:
			ok = true
			if len(out.Decisions) < decisionsMost && !superseded[record.ID] {
				out.Decisions = append(out.Decisions, record)
			}
		case ProjectMemoryPitfall:
			ok = true
			if len(out.Pitfalls) < pitfallsMost {
				out.Pitfalls = append(out.Pitfalls, record)
			}
		}
		return len(out.Outcomes) < outcomesMost || len(out.Decisions) < decisionsMost || len(out.Pitfalls) < pitfallsMost
	})
	return out, ok
}

// ProjectSummary streams summary.md without leading whitespace and returns at most limit runes, adding an ellipsis
// whenever unread content remains. It never turns a read error or an empty summary into fabricated project context.
func (s *Store) ProjectSummary(slug string, limit int) string {
	if limit <= 0 {
		return ""
	}
	f, err := os.Open(s.path(KindProject, slug, "summary.md"))
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var b strings.Builder
	started := false
	count := 0
	for {
		ch, _, err := r.ReadRune()
		if errors.Is(err, io.EOF) {
			return strings.TrimSpace(b.String())
		}
		if err != nil {
			return ""
		}
		if !started && unicode.IsSpace(ch) {
			continue
		}
		started = true
		b.WriteRune(ch)
		count++
		if count >= limit {
			break
		}
	}
	if _, _, err := r.ReadRune(); errors.Is(err, io.EOF) {
		return strings.TrimSpace(b.String())
	} else if err != nil {
		return ""
	}
	if limit == 1 {
		return "…"
	}
	text := []rune(b.String())
	return strings.TrimSpace(string(text[:limit-1])) + "…"
}
