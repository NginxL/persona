package kbhistory

import (
	"context"
	mrand "math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

// The history import (docs/assistant/knowledge.md "History import"): a chat connector that can read its
// conversations backwards (connectors.history_chats / history / history_anchors, read-only) hands their older
// messages to the knowledge base, which merges them into the day files (kb.Engine.WriteHistory) as archive only.
//
// It runs in its own goroutine, never inside the knowledge base's loop, and keeps everything on disk under history/
// (kbhistory/files.go), so a redeploy at any time loses nothing: a restart goes on from each chat's state. It reads
// a source only when all of these hold: the knowledge base is on and writable, kb.history.enabled is true and lists
// the source, the owner approved the source's plan in the assistant chat (kb_history_resume), there is no STOP, and
// the source does not wait for the search index (historyIndexed). One source at a time, in the order kb.history
// lists them; one request in flight, ever; every wait drawn afresh (kbhistory/pace.go). A call that timed out or an
// answer it does not know stops the whole import (STOP, a push) until a person looks; nothing is retried by itself.
// A stop never cancels the call in flight: it takes effect before the next one.

const (
	Poll                 = time.Minute     // how often the loop looks for something to run
	CallTimeout          = 3 * time.Minute // one call (the hub bounds it at two)
	historyWaitStep      = 30 * time.Second
	historyPauseMost     = 2 * time.Hour // paused longer than this without a call going through: stop
	historyTransientWait = 5 * time.Minute
)

// historySource is how the import reaches the connectors (through the hub; a fake in tests).
type historySource interface {
	Archives(ctx context.Context) ([]assistantwire.ArchiveSource, error)
	Chats(ctx context.Context, id string) ([]connectorwire.HistoryChat, error)
	Page(ctx context.Context, id string, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error)
	Anchors(ctx context.Context, id string, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error)
	Media(ctx context.Context, id, ref string) (connector.Media, error)
}

// historyDriver is one import: its dependencies, and the state of the run it is in.
type historyDriver struct {
	data      string
	src       historySource
	eng       func(context.Context) (*kb.Engine, error) // the knowledge base, open and writable
	cfg       func(context.Context) *assistantwire.KBHistory
	connected func() bool
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
	rng       *mrand.Rand
	logf      func(string, ...any)
	notify    func(ctx context.Context, source string, why historyReason)
	calls     *sync.Mutex
	instance  string
	indexed   func() bool
	running   *atomic.Pointer[string]
	timeout   time.Duration

	// The run.
	lock        *historyLock
	key         []byte
	zone        *time.Location
	source      string
	info        assistantwire.ArchiveSource
	pacer       *historyPacer
	pausedSince time.Time
	halted      error // a halt an image download met inside a page's write, acted on after it
	beatAt      time.Time
	// lastOK: the call before went through, with nothing but the interval since (no rest, pause or quiet period).
	lastOK bool
}

// Halt ends a run: stop (write STOP, push; a person must look) or not (the import was turned off, the
// knowledge base closed, STOP was there already: the loop looks again later).
type Halt struct {
	stop bool
	why  historyReason
}

func (h *Halt) Error() string { return h.why.String() }

// halt is a Halt for the reason code, with its arguments as key, value pairs (historyReason).
func halt(stop bool, code string, kv ...string) *Halt {
	return &Halt{stop: stop, why: NewReason(code, kv...)}
}

func HaltWhy(stop bool, why historyReason) *Halt { return &Halt{stop: stop, why: why} }

// failed is what a write or save of the import's own that failed with err ends the run with: a stop for code (with
// err's class), unless the run's context has ended, as it does when the manager shuts down. Then the failure is
// taken as the shutdown cutting it short, whatever err says: the run ends as the gate ends it then (ctx.Err()),
// nothing of the step is saved, and the next start does the step again (a page written twice adds nothing:
// kb.Engine.WriteHistory). A failure that was real fails again then, and stops.
func failed(ctx context.Context, code string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return halt(true, code, "class", errClass(err))
}

// ChatSource is how the import reaches the connectors.
type ChatSource = historySource

// Reason is why a run stopped, as the push says it.
type Reason = historyReason

// Deps is what a Driver takes from the assistant Service.
type Deps struct {
	Data      string
	Src       ChatSource
	Eng       func(context.Context) (*kb.Engine, error) // the knowledge base, open and writable
	Cfg       func(context.Context) *assistantwire.KBHistory
	Connected func() bool
	Notify    func(ctx context.Context, source string, why Reason)
	Calls     *sync.Mutex
	Instance  string
	Indexed   func() bool
	Running   *atomic.Pointer[string]
	Logf      func(string, ...any)
}

// Driver is one import of a knowledge base.
type Driver struct{ d *historyDriver }

// NewDriver is an import with the real clock, waits and randomness.
func NewDriver(dp Deps) *Driver {
	return &Driver{d: &historyDriver{
		data: dp.Data, src: dp.Src, eng: dp.Eng, cfg: dp.Cfg, connected: dp.Connected,
		now:      time.Now,
		sleep:    SleepCtx,
		rng:      mrand.New(mrand.NewPCG(mrand.Uint64(), mrand.Uint64())),
		logf:     dp.Logf,
		notify:   dp.Notify,
		calls:    dp.Calls,
		instance: dp.Instance,
		indexed:  dp.Indexed,
		running:  dp.Running,
		timeout:  CallTimeout,
	}}
}

// RunOnce runs what is runnable now.
func (x *Driver) RunOnce(ctx context.Context) error { return x.d.runOnce(ctx) }

// Indexed reports whether the knowledge base e has its search index.
func Indexed(e *kb.Engine) bool { return historyIndexed(e) }

// ReasonText is why for a person who reads language l.
func ReasonText(l i18n.Lang, why Reason) string { return historyReasonText(l, why) }

// SleepCtx waits d or until ctx ends.
func SleepCtx(ctx context.Context, d time.Duration) error { return sleepCtx(ctx, d) }

// historySearchIndexed is kb.SearchIndexed (a var for the tests).
var historySearchIndexed = kb.SearchIndexed

// historyWriteKB is kb.Engine.WriteHistory (a var for the tests).
var historyWriteKB = (*kb.Engine).WriteHistory

// historyIndexed reports whether the knowledge base e has its search index (kb.Store.SearchIndexReady): the index
// lane (kb_lanes.go) builds it, within its first run on an archive that never had one. Until then a source that
// needs it waits (needs_search_index), as it did before the knowledge base kept an index at all.
func historyIndexed(e *kb.Engine) bool {
	return historySearchIndexed && e != nil && e.Store.SearchIndexReady()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ReasonText is why the run halted, for a person who reads language l.
func (h *Halt) ReasonText(l i18n.Lang) string { return historyReasonText(l, h.why) }

// Indexed reports whether the knowledge base this import reads has its search index.
func (x *Driver) IsIndexed() bool { return x.d.indexed() }
