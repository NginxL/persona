package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/kbhistory"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

// kbHistoryShared is the import's part of the Service.
type kbHistoryShared struct {
	calls    sync.Mutex // one request in flight to any source, the driver's and the tools' alike
	wake     chan struct{}
	running  atomic.Pointer[string] // the source this process is importing now
	instance string
}

// ---- the Service's side ----

func (s *Service) kbHistoryInit() {
	s.kb.history.wake = make(chan struct{}, 1)
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	s.kb.history.instance = hex.EncodeToString(b)
}

// kbHistoryWake has the loop look again now (a plan was approved, STOP removed).
func (s *Service) kbHistoryWake() {
	select {
	case s.kb.history.wake <- struct{}{}:
	default:
	}
}

// kbHistoryLoop runs the import while ctx lasts, looking every minute (at once when woken).
func (s *Service) kbHistoryLoop(ctx context.Context) {
	if s.kb.dir == "" {
		return
	}
	t := time.NewTicker(kbhistory.Poll)
	defer t.Stop()
	for {
		s.kbRun(ctx, func() { s.kbHistoryRound(ctx) })
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kb.history.wake:
		}
	}
}

// kbHistoryRound runs what is runnable now, if anything: only while connected, not a mesh member's standby, the
// knowledge base writable and kb.history enabled.
func (s *Service) kbHistoryRound(ctx context.Context) {
	if !s.kbConnected() || s.hello(ctx).Assistant.Standby || !s.kbWritable(ctx) {
		return
	}
	if set := s.kbSettings(ctx); set == nil || !set.History.On() {
		return
	}
	e, err := s.kbGet(ctx)
	if err != nil || e.Store.ReadOnly() {
		return
	}
	d := s.historyDriver(e.Store.Dir)
	if err := d.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		s.logf("kb history: %v", err)
	}
}

// historyDriver is an import of the knowledge base in data, reaching the connectors through the hub.
func (s *Service) historyDriver(data string) *kbhistory.Driver {
	return kbhistory.NewDriver(s.historyDeps(data))
}

// historyTools are the assistant's history import tools.
func (s *Service) historyTools() *kbhistory.Tools {
	return kbhistory.NewTools(kbhistory.Host{
		Lang: s.lang, Engine: s.kbGet, Settings: s.kbSettings, Location: s.location,
		Deps: s.historyDeps, Running: &s.kb.history.running, Instance: s.kb.history.instance,
		Wake: s.kbHistoryWake, Logf: s.logf, UpdateSelection: s.updateKBSelection, RefreshNames: s.refreshSelectionNames,
	})
}

func (s *Service) historyDeps(data string) kbhistory.Deps {
	return (kbhistory.Deps{
		Data: data,
		Src:  busHistory{s: s},
		Eng: func(ctx context.Context) (*kb.Engine, error) {
			if !s.kbWritable(ctx) {
				return nil, errors.New("the knowledge base is not writable")
			}
			e, err := s.kbGet(ctx)
			if err == nil && e.Store.ReadOnly() {
				err = kb.ErrReadOnly
			}
			return e, err
		},
		Cfg: func(ctx context.Context) *assistantwire.KBHistory {
			if set := s.kbSettings(ctx); set != nil && !set.Off && !set.ReadOnly {
				return set.History
			}
			return nil
		},
		Connected: s.kbConnected,
		Logf:      s.logf,
		Notify: func(ctx context.Context, source string, why kbhistory.Reason) {
			l := s.lang(ctx)
			title, body := "assistant-kb_history.stopped_title", "assistant-kb_history.stopped_body"
			if why.Code == "plan_changed" {
				title, body = "assistant-kb_history.plan_changed_title", "assistant-kb_history.plan_changed_body"
			}
			s.push(withCard(ctx, cardKBHistory), Notification{Subtitle: "Persona", Title: i18n.T(l, title),
				Body: i18n.T(l, body, i18n.Args{"source": source, "reason": kbhistory.ReasonText(l, why), "count": why.Args["count"]})})
		},
		Calls:    &s.kb.history.calls,
		Instance: s.kb.history.instance,
		Indexed: func() bool {
			o := s.kb.cur.Load()
			return o != nil && kbhistory.Indexed(o.eng)
		},
		Running: &s.kb.history.running,
	})
}

// busHistory reaches the connectors through the hub (connectors.*).
type busHistory struct{ s *Service }

func (b busHistory) Archives(ctx context.Context) ([]assistantwire.ArchiveSource, error) {
	var list assistantwire.Archives
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsArchives, map[string]any{}, &list)
	return list.Archives, err
}

func (b busHistory) Chats(ctx context.Context, id string) ([]connectorwire.HistoryChat, error) {
	var out assistantwire.HistoryChatsResult
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsHistoryChats, map[string]any{"id": id}, &out)
	return out.Chats, err
}

func (b busHistory) Page(ctx context.Context, id string, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	var out connectorwire.HistoryPage
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsHistory, assistantwire.HistoryParams{ID: id, Query: q}, &out)
	return out, err
}

func (b busHistory) Anchors(ctx context.Context, id string, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error) {
	var out connectorwire.HistoryAnchors
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsHistoryAnchors, assistantwire.HistoryAnchorsParams{ID: id, Query: q}, &out)
	return out, err
}

func (b busHistory) Media(ctx context.Context, id, ref string) (connector.Media, error) {
	var out struct {
		Media connector.Media `json:"media"`
	}
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsArchiveMedia, map[string]any{"id": id, "ref": ref}, &out)
	return out.Media, err
}
