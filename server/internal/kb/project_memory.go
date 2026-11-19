package kb

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
)

// ProjectMemoryRecord is one worker's completed outcome in a project's memory.jsonl. Turn is the transcript sequence
// that opens the turn and ItemIndex the outcome item's offset in it; together with the todo and agent they form ID.
type ProjectMemoryRecord struct {
	ID         string   `json:"id"`
	TodoID     string   `json:"todo_id"`
	Agent      string   `json:"agent"`
	Turn       int      `json:"turn"`
	ItemIndex  int      `json:"item_index"`
	Conclusion string   `json:"conclusion"`
	Links      []string `json:"links"`
	Decision   string   `json:"decision"`
	Reason     string   `json:"reason"`
}

// AppendProjectMemory appends record to a project's memory.jsonl unless its ID is already there. The disk file is the
// dedupe record, so replay after reopening the Engine is safe and a failed append claims nothing.
func (e *Engine) AppendProjectMemory(slug string, record ProjectMemoryRecord) error {
	if record.ID == "" {
		return errors.New("project memory record has no id")
	}
	s := e.Store
	if s.readOnly {
		return ErrReadOnly
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	has, err := s.projectMemoryHasLocked(slug, record.ID)
	if err != nil || has {
		return err
	}
	return appendFile(s.path(KindProject, slug, "memory.jsonl"), "", string(append(raw, '\n')))
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
