package mail

import (
	"context"
	"encoding/json"
	"sync"
)

// memRecords is an in-memory Records for tests (production uses Postgres via internal/db).
type memRecords struct {
	mu       sync.Mutex
	rows     []AccountRow
	settings map[string][]byte
}

func newMemRecords() *memRecords { return &memRecords{} }

func (m *memRecords) MailAccounts(context.Context) ([]AccountRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]AccountRow(nil), m.rows...), nil
}

func (m *memRecords) ReplaceMailAccounts(_ context.Context, rows []AccountRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append([]AccountRow(nil), rows...)
	return nil
}

// Setting / PutSetting keep small settings as JSON (production: the settings table).
func (m *memRecords) Setting(_ context.Context, key string, out any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.settings[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (m *memRecords) PutSetting(_ context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings == nil {
		m.settings = map[string][]byte{}
	}
	m.settings[key] = raw
	return nil
}

// dump concatenates everything persisted, to check no secret is stored in clear text.
func (m *memRecords) dump() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var s string
	for _, r := range m.rows {
		s += r.ID + r.Email + string(r.Data) + r.PasswordEnc + r.OAuthEnc
	}
	for _, v := range m.settings {
		s += string(v)
	}
	return s
}
