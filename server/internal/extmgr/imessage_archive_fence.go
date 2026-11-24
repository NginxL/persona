package extmgr

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/imessagewire"
	"github.com/nwverse/persona/server/internal/kb"
)

var errIMessageArchiveRetired = errors.New("iMessage archive generation is no longer active")

const imessageArchiveReservationsKey = "imessage.archive_reservations"

type imessageArchiveReservations struct {
	Version int      `json:"version"`
	Tokens  []string `json:"tokens"`
}

// A settings request or a reservation call can stop waiting when its context
// ends, including while another reservation is blocked on the database.
type imessageContextLock struct {
	once  sync.Once
	token chan struct{}
}

func (l *imessageContextLock) lock(ctx context.Context) error {
	l.once.Do(func() {
		l.token = make(chan struct{}, 1)
		l.token <- struct{}{}
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.token:
		if err := ctx.Err(); err != nil {
			l.unlock()
			return err
		}
		return nil
	}
}

func (l *imessageContextLock) unlock() { l.token <- struct{}{} }

// The source manager owns the generation and commit reservations. A node read
// holds no lock; disable retires its generation and waits only for commits that
// already obtained a reservation. The reservation survives a disconnected
// caller until it is explicitly ended: an uncertain commit cannot be treated as done.
type imessageArchiveFence struct {
	once       sync.Once
	mu         imessageContextLock
	revoked    atomic.Bool
	enabled    bool
	generation string
	store      Store
	loaded     bool
	active     map[string]struct{}
	drained    chan struct{}
}

func (f *imessageArchiveFence) init(enabled bool, store Store) {
	f.once.Do(func() {
		f.enabled = enabled
		f.revoked.Store(!enabled)
		f.generation = randomHex(16)
		f.store = store
		f.active = make(map[string]struct{})
	})
}

// loadLocked recovers reservations before accepting reads or acknowledging a
// disable. An unreadable record is not evidence that no writer is active.
func (f *imessageArchiveFence) loadLocked(ctx context.Context) error {
	if f.loaded {
		return nil
	}
	if f.store == nil {
		return errors.New("archive reservation store unavailable")
	}
	var saved imessageArchiveReservations
	ok, err := f.store.Setting(ctx, imessageArchiveReservationsKey, &saved)
	if err != nil {
		return err
	}
	if !ok && len(f.active) > 0 {
		return errors.New("archive reservation record disappeared")
	}
	active := make(map[string]struct{})
	if ok {
		if saved.Version != 1 || len(saved.Tokens) > 10000 {
			return errors.New("invalid archive reservation record")
		}
		active = make(map[string]struct{}, len(saved.Tokens))
		for _, token := range saved.Tokens {
			if len(token) != 32 {
				return errors.New("invalid archive reservation record")
			}
			if _, err := hex.DecodeString(token); err != nil {
				return errors.New("invalid archive reservation record")
			}
			if _, exists := active[token]; exists {
				return errors.New("invalid archive reservation record")
			}
			active[token] = struct{}{}
		}
	}
	f.active = active
	if len(f.active) > 0 {
		if f.drained == nil {
			f.drained = make(chan struct{})
		}
	} else if f.drained != nil {
		close(f.drained)
		f.drained = nil
	}
	f.loaded = true
	return nil
}

func (f *imessageArchiveFence) saveLocked(ctx context.Context, add, remove string) error {
	tokens := make([]string, 0, len(f.active)+1)
	for token := range f.active {
		if token != remove {
			tokens = append(tokens, token)
		}
	}
	if add != "" {
		tokens = append(tokens, add)
	}
	sort.Strings(tokens)
	return f.store.PutSetting(ctx, imessageArchiveReservationsKey, imessageArchiveReservations{Version: 1, Tokens: tokens})
}

func (f *imessageArchiveFence) snapshot(ctx context.Context) (string, error) {
	if err := f.mu.lock(ctx); err != nil {
		return "", err
	}
	defer f.mu.unlock()
	if err := f.loadLocked(ctx); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if f.revoked.Load() || !f.enabled || f.generation == "" {
		return "", errIMessageArchiveRetired
	}
	return f.generation, nil
}

func (f *imessageArchiveFence) begin(ctx context.Context, generation, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.mu.lock(ctx); err != nil {
		return err
	}
	defer f.mu.unlock()
	if err := f.loadLocked(ctx); err != nil {
		return err
	}
	if f.revoked.Load() || !f.enabled || generation == "" || generation != f.generation || token == "" {
		return errIMessageArchiveRetired
	}
	if _, exists := f.active[token]; exists {
		return errors.New("archive reservation already exists")
	}
	if len(f.active) >= 10000 {
		return errors.New("too many archive reservations")
	}
	if err := f.saveLocked(ctx, token, ""); err != nil {
		f.loaded = false // the write may have committed despite the error
		return err
	}
	if len(f.active) == 0 {
		f.drained = make(chan struct{})
	}
	f.active[token] = struct{}{}
	if f.revoked.Load() {
		return errIMessageArchiveRetired
	}
	return ctx.Err()
}

func (f *imessageArchiveFence) end(ctx context.Context, token string) error {
	if err := f.mu.lock(ctx); err != nil {
		return err
	}
	defer f.mu.unlock()
	if err := f.loadLocked(ctx); err != nil {
		return err
	}
	if _, ok := f.active[token]; !ok {
		return nil
	}
	if err := f.saveLocked(ctx, "", token); err != nil {
		f.loaded = false
		return err
	}
	delete(f.active, token)
	if len(f.active) == 0 {
		close(f.drained)
		f.drained = nil
	}
	return nil
}

// abort handles an unanswered begin: retiring the generation also rejects a
// begin that arrives after the abort on a reordered or interrupted connection.
func (f *imessageArchiveFence) abort(ctx context.Context, generation, token string) error {
	if err := f.mu.lock(ctx); err != nil {
		return err
	}
	defer f.mu.unlock()
	if err := f.loadLocked(ctx); err != nil {
		return err
	}
	if generation != "" && generation == f.generation {
		f.generation = randomHex(16)
	}
	if _, ok := f.active[token]; ok {
		if err := f.saveLocked(ctx, "", token); err != nil {
			f.loaded = false
			return err
		}
		delete(f.active, token)
		if len(f.active) == 0 {
			close(f.drained)
			f.drained = nil
		}
	}
	return nil
}

func (f *imessageArchiveFence) retire(ctx context.Context) (<-chan struct{}, error) {
	// Invalidate admissions before waiting for a database write holding mu.
	// A cancelled request cannot leave its previous generation usable.
	f.revoked.Store(true)
	if err := f.mu.lock(ctx); err != nil {
		return nil, err
	}
	f.enabled = false
	f.generation = randomHex(16)
	if err := f.loadLocked(ctx); err != nil {
		f.mu.unlock()
		return nil, err
	}
	drained := f.drained
	f.mu.unlock()
	return drained, nil
}

func waitIMessageArchive(ctx context.Context, drained <-chan struct{}) error {
	if drained == nil {
		return ctx.Err()
	}
	select {
	case <-drained:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *imessageArchiveFence) disable(ctx context.Context) error {
	drained, err := f.retire(ctx)
	if err != nil {
		return err
	}
	return waitIMessageArchive(ctx, drained)
}

func (f *imessageArchiveFence) enable(ctx context.Context) error {
	if err := f.mu.lock(ctx); err != nil {
		return err
	}
	defer f.mu.unlock()
	if f.revoked.Load() {
		// A previous retirement may have timed out before it acquired mu.
		f.generation = randomHex(16)
	}
	f.enabled = true
	f.revoked.Store(false)
	return nil
}

func (h *Manager) kbArchiveFenceCall(ctx context.Context, p assistantwire.AppCall) (json.RawMessage, bool, error) {
	var req assistantwire.ArchiveFence
	if err := json.Unmarshal(p.Params, &req); err != nil {
		return nil, true, err
	}
	if req.ID != imessagewire.ID || len(req.Generation) != 32 || len(req.Token) != 32 {
		return nil, true, errors.New("invalid archive reservation")
	}
	if _, err := hex.DecodeString(req.Generation); err != nil {
		return nil, true, errors.New("invalid archive generation")
	}
	if _, err := hex.DecodeString(req.Token); err != nil {
		return nil, true, errors.New("invalid archive reservation")
	}
	switch p.Method {
	case assistantwire.ConnectorsArchiveBegin:
		if h.kbOff() {
			return nil, true, errKBOff
		}
		if h.kbReadOnly() {
			return nil, true, kb.ErrReadOnly
		}
		if _, err := h.kbArchive(ctx, req.ID); err != nil {
			return nil, true, err
		}
		if err := h.imessageArchive.begin(ctx, req.Generation, req.Token); err != nil {
			return nil, true, err
		}
	case assistantwire.ConnectorsArchiveEnd:
		if err := h.imessageArchive.end(ctx, req.Token); err != nil {
			return nil, true, err
		}
	case assistantwire.ConnectorsArchiveAbort:
		if err := h.imessageArchive.abort(ctx, req.Generation, req.Token); err != nil {
			return nil, true, err
		}
	}
	raw, err := json.Marshal(assistantwire.ArchiveFenceResult{Acquired: true})
	return raw, true, err
}
