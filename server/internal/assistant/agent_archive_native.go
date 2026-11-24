package assistant

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Native archive (docs/protocol/protocol.md "session.archive"): an archive or restore the user makes in Persona
// (agent_archives.go) is also asked of the tool that owns the session, with the tool's own reversible archive (Codex:
// thread/archive). Persona's archive is the source of truth and succeeds whatever the tool answers; what the tool
// did is kept per session in the setting sessions.archived.native:
//
//   - done: the tool has it as Persona does.
//   - pending: not yet (the machine offline, the agent working, Codex still open in a terminal, an error). Tried again
//     when the machine's session list comes in, backing off from nativeArchiveRetry to nativeArchiveRetryMost.
//   - unsupported: the tool has no archive Persona can drive (Claude Code, an older node): Persona's alone.
//
// A Codex thread archived in Codex leaves the node's list; the manager's snapshot keeps showing it as archived (and
// restorable). Persona restoring it on its own, after new activity in it, is followed here too: the next list asks
// the tool to restore it (already restored is done).

const agentNativeArchivesKey = "sessions.archived.native"

// Node methods (node archive.go).
const (
	deviceSessionArchive   = "session.archive"   // {session_id} → {native: done|unsupported, already?, via?, reason?}
	deviceSessionUnarchive = "session.unarchive" // the same
)

const (
	nativeArchiveDone        = "done"
	nativeArchivePending     = "pending"
	nativeArchiveUnsupported = "unsupported"
)

var (
	// nativeArchiveCallTimeout bounds one node call (the node's own bound on a Codex command is 30 s).
	nativeArchiveCallTimeout = 40 * time.Second
	// nativeArchiveAwait is how long an archive request waits for the tool before it answers "pending".
	nativeArchiveAwait = 8 * time.Second
	// nativeArchiveRetry is the first wait before a pending one is tried again; it doubles up to nativeArchiveRetryMost.
	nativeArchiveRetry     = time.Minute
	nativeArchiveRetryMost = 30 * time.Minute
)

type agentNativeArchive struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	Archived  bool      `json:"archived"` // asked of the tool: archived, or restored
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	Via       string    `json:"via,omitempty"`
	At        time.Time `json:"at,omitzero"` // the last try
	Tries     int       `json:"tries,omitempty"`
	// wish numbers the asks in this process: one made while a call was out is made after it, even the same way.
	wish uint64
}

type agentNativeBook struct {
	mu      sync.Mutex
	loaded  bool
	records map[string]agentNativeArchive
	// intents are archives / restores asked for before the records are read (they win over what was read).
	intents map[string]agentNativeArchive
	running map[string]chan struct{} // session key → closed when its worker ends
	wishes  uint64
}

// nativeArchiveWanted asks the tool that owns s for archived (Persona's archive just changed): in the background, at
// once. Called once Persona's own state is saved (noteAgentArchive).
func (svc *Service) nativeArchiveWanted(s proto.Session, archived bool) {
	key := runtimeListID(s.MachineID, s.SessionID)
	rec := agentNativeArchive{MachineID: s.MachineID, SessionID: s.SessionID, Archived: archived, State: nativeArchivePending}
	b := &svc.agentNative
	b.mu.Lock()
	b.wishes++
	rec.wish = b.wishes
	if b.loaded {
		b.records = maps.Clone(b.records)
		b.records[key] = rec
	} else {
		if b.intents == nil {
			b.intents = map[string]agentNativeArchive{}
		}
		b.intents[key] = rec
	}
	b.mu.Unlock()
	svc.nativeArchiveKick(key)
}

// nativeArchiveAwaited waits a little for the tool (at most nativeArchiveAwait, within ctx) for the archive request's
// answer; nil when nothing was asked.
func (svc *Service) nativeArchiveAwaited(ctx context.Context, machine, session string) *proto.NativeArchive {
	key := runtimeListID(machine, session)
	b := &svc.agentNative
	b.mu.Lock()
	done := b.running[key]
	b.mu.Unlock()
	if done != nil {
		t := time.NewTimer(nativeArchiveAwait)
		select {
		case <-done:
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
	}
	return svc.nativeArchiveView(machine, session)
}

// nativeArchiveView is what the archived list shows of the tool's archive; nil when it was never asked.
func (svc *Service) nativeArchiveView(machine, session string) *proto.NativeArchive {
	key := runtimeListID(machine, session)
	b := &svc.agentNative
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.intents[key]
	if !ok {
		rec, ok = b.records[key]
	}
	if !ok {
		return nil
	}
	return &proto.NativeArchive{Archived: rec.Archived, State: rec.State, Error: rec.Error}
}

// nativeArchiveKick starts key's worker unless one runs (it reads the latest wish when it is done).
func (svc *Service) nativeArchiveKick(key string) {
	b := &svc.agentNative
	b.mu.Lock()
	if b.running == nil {
		b.running = map[string]chan struct{}{}
	}
	if b.running[key] != nil {
		b.mu.Unlock()
		return
	}
	done := make(chan struct{})
	b.running[key] = done
	b.mu.Unlock()
	go svc.nativeArchiveRun(key, done)
}

// nativeArchiveRun brings the tool to the latest wish for key: again while the wish changes under a call.
func (svc *Service) nativeArchiveRun(key string, done chan struct{}) {
	b := &svc.agentNative
	for {
		for range 4 {
			ctx, cancel := context.WithTimeout(context.Background(), nativeArchiveCallTimeout)
			again := svc.nativeArchiveOnce(ctx, key)
			cancel()
			if !again {
				break
			}
		}
		// A wish that came in as this worker was ending is made by this worker, decided under the same lock that
		// ends it: ended first and started again after, a request in between found a fresh wish with no worker to
		// wait for (nativeArchiveAwaited) and answered "pending" at once.
		b.mu.Lock()
		if rec := b.records[key]; rec.State == nativeArchivePending && rec.At.IsZero() {
			b.mu.Unlock()
			continue
		}
		delete(b.running, key)
		b.mu.Unlock()
		close(done)
		return
	}
}

// nativeArchiveOnce makes one call for key's pending wish; true when another wish came in meanwhile.
func (svc *Service) nativeArchiveOnce(ctx context.Context, key string) bool {
	if err := svc.loadNativeArchives(ctx); err != nil {
		svc.logf("native archive: %v", err)
		return false
	}
	b := &svc.agentNative
	b.mu.Lock()
	rec, ok := b.records[key]
	b.mu.Unlock()
	if !ok || rec.State != nativeArchivePending {
		return false
	}
	method := deviceSessionUnarchive
	if rec.Archived {
		method = deviceSessionArchive
	}
	var res struct {
		Native string `json:"native"`
		Via    string `json:"via"`
		Reason string `json:"reason"`
	}
	err := svc.nodeCall(ctx, rec.MachineID, method, map[string]any{"session_id": rec.SessionID}, &res)
	next := rec
	next.At, next.Error = time.Now().UTC(), ""
	var re *rpc.Error
	switch {
	case err == nil && res.Native == nativeArchiveDone:
		next.State, next.Via, next.Tries = nativeArchiveDone, res.Via, 0
	case err == nil:
		next.State, next.Error, next.Tries = nativeArchiveUnsupported, res.Reason, 0
	case errors.As(err, &re) && strings.HasPrefix(re.Message, "unknown method"): // a node older than native archive
		next.State, next.Error, next.Tries = nativeArchiveUnsupported, "", 0
	default:
		next.State, next.Error = nativeArchivePending, err.Error()
		next.Tries++
	}
	b.mu.Lock()
	cur, ok := b.records[key]
	if !ok || cur.wish != rec.wish || cur.Archived != rec.Archived || !cur.At.Equal(rec.At) || cur.State != rec.State {
		b.mu.Unlock()
		return ok // the user changed it meanwhile: the latest wish is made next
	}
	records := maps.Clone(b.records)
	records[key] = next
	b.records = records
	b.mu.Unlock()
	if err := svc.saveNativeArchives(ctx); err != nil {
		svc.logf("save native archive: %v", err)
	}
	if next.State == nativeArchivePending {
		svc.logf("native archive %s/%s (%s): %s; tried again later", rec.MachineID, rec.SessionID, method, next.Error)
	}
	return false
}

func (svc *Service) loadNativeArchives(ctx context.Context) error {
	b := &svc.agentNative
	b.mu.Lock()
	loaded := b.loaded
	b.mu.Unlock()
	if loaded {
		return nil
	}
	store := svc.archiveStore()
	if store == nil {
		return errors.New("no database")
	}
	records := map[string]agentNativeArchive{}
	if _, err := store.Setting(ctx, agentNativeArchivesKey, &records); err != nil {
		return err
	}
	if records == nil {
		records = map[string]agentNativeArchive{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.loaded {
		return nil
	}
	maps.Copy(records, b.intents)
	b.records, b.intents, b.loaded = records, nil, true
	return nil
}

// saveNativeArchives writes the records as they are now (a later write carries any change made meanwhile).
func (svc *Service) saveNativeArchives(ctx context.Context) error {
	store := svc.archiveStore()
	if store == nil {
		return errors.New("no database")
	}
	b := &svc.agentNative
	b.mu.Lock()
	records := b.records
	b.mu.Unlock()
	return store.PutSetting(ctx, agentNativeArchivesKey, records)
}

// nativeArchivesListed follows machine's new session list: Persona's archive changed on its own (new activity
// restored a session) or a pending one is due again. Background; nothing when there is nothing to do.
func (svc *Service) nativeArchivesListed(machine string) {
	b := &svc.agentNative
	b.mu.Lock()
	loaded, records := b.loaded, b.records
	var unread []string // wishes whose records could not be read yet (the database was away)
	for key, rec := range b.intents {
		if rec.MachineID == machine {
			unread = append(unread, key)
		}
	}
	b.mu.Unlock()
	if !loaded {
		for _, key := range unread {
			svc.nativeArchiveKick(key)
		}
		return // otherwise read with the first archive: nothing was asked of a tool since this manager started
	}
	persona := svc.agentArchives.snapshot.Load()
	now := time.Now()
	for key, rec := range records {
		if rec.MachineID != machine {
			continue
		}
		if persona != nil {
			if p, ok := (*persona)[key]; ok && p.Archived != rec.Archived {
				svc.nativeArchiveWanted(p.Session, p.Archived) // Persona restored it after new activity
				continue
			}
		}
		if rec.State == nativeArchivePending && now.Sub(rec.At) >= nativeArchiveBackoff(rec.Tries) {
			svc.nativeArchiveKick(key)
		}
	}
}

func nativeArchiveBackoff(tries int) time.Duration {
	d := nativeArchiveRetry
	for i := 1; i < tries && d < nativeArchiveRetryMost; i++ {
		d *= 2
	}
	return min(d, nativeArchiveRetryMost)
}
