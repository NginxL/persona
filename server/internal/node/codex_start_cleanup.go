package node

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A start already gave its prompt to Codex as an argument. Leaving its dialog open would let it run that prompt
// later without a task tracking it. Close that start's pane before answering, including after cancellation. Unlike
// closeNewTab, failure is retained and reported: a retry must finish this cleanup before creating another pane.
type codexAbandonedStart struct {
	launch    host.Launcher
	created   *host.Created
	workspace string
	session   string // only a fresh thread explicitly identified by this pane
}

// closeAbandonedCodex serializes cleanup only when failed panes are pending. Independent starts do not hold
// this lock while creating their tabs or waiting for threads, and can add failures while a cleanup is in flight.
func (n *Node) closeAbandonedCodex(ctx context.Context, key string) error {
	n.mu.Lock()
	pending := len(n.codexAbandoned[key]) > 0
	n.mu.Unlock()
	if !pending {
		return nil
	}
	defer n.typing.lock("codex-cleanup:" + key)()
	for {
		n.mu.Lock()
		starts := slices.Clone(n.codexAbandoned[key])
		n.mu.Unlock()
		if len(starts) == 0 {
			return nil
		}
		var failures []error
		for _, start := range starts {
			if err := n.closeFailedCodexStart(ctx, start); err != nil {
				failures = append(failures, err)
				continue
			}
			n.mu.Lock()
			n.codexAbandoned[key] = slices.DeleteFunc(n.codexAbandoned[key], func(s codexAbandonedStart) bool {
				return s.created == start.created
			})
			if len(n.codexAbandoned[key]) == 0 {
				delete(n.codexAbandoned, key)
			}
			n.mu.Unlock()
		}
		if len(failures) > 0 {
			return errors.Join(failures...)
		}
		// A concurrent start may have added another failed cleanup while these panes were closing.
	}
}

// The first message can be incomplete, so matching it cannot identify an orphan.
// Accept only the thread named by this pane, with a rollout begun by this launch;
// never infer ownership from a common directory or a matching first line.
func (n *Node) failedCodexSession(ctx context.Context, pane string, began time.Time) string {
	held := n.codexHeldThreads()
	id := ""
	if threads := held.byPane[pane]; len(threads) == 1 {
		id = threads[0].id
	} else if len(threads) > 1 {
		return "" // no unique writer identity
	} else {
		a, err := n.paneAgent(ctx, pane)
		if err != nil || a == nil || a.Agent != "codex" || !validID(a.SessionID()) {
			return ""
		}
		id = n.codexPublicID(n.codexPaneHome(pane), a.SessionID())
	}
	if owner := held.owner[id]; owner != "" && owner != pane {
		return ""
	}
	home, thread, ok := n.codexThread(id)
	if !ok {
		return ""
	}
	at, _ := codexThreadStart(rolloutPath(home, thread))
	if at.IsZero() || at.Before(began.Add(-2*time.Second)) || !n.claimCodexThread(pane, id) {
		return ""
	}
	return id
}

// Closing removes the process; native archive also removes an already-published
// thread from history/attention. Both retain the transcript and are reversible.
// Archive is attempted only after the launch's own pane has closed successfully.
func (n *Node) closeFailedCodexStart(ctx context.Context, start codexAbandonedStart) error {
	if err := closeCodexStart(ctx, start); err != nil {
		n.logf("codex failed start pane %s: close failed: %v", start.created.Pane.String(), err)
		return err
	}
	n.logf("codex failed start pane %s: closed", start.created.Pane.String())
	if start.session == "" {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	n.mu.Lock()
	if a, ok := n.agents[start.session]; ok && a.PaneID == start.created.Pane.String() {
		delete(n.agents, start.session)
	}
	n.mu.Unlock()
	n.state.stopped(start.session, proto.StopExit)
	interrupted := false
	for {
		_, err := n.nativeArchive(cleanup, start.session, true)
		if err == nil {
			n.logf("codex failed start pane %s: native archive done for %s", start.created.Pane.String(), start.session)
			n.unclaimCodexThread(start.session)
			n.notifySessions()
			return nil
		}
		var coded *rpc.Error
		if !interrupted && errors.As(err, &coded) && coded.Code == nativeArchiveBusy {
			// A shared app-server can keep a turn waiting for input after its
			// TUI exits. Only this failed launch's identified thread may stop.
			if a, live := n.agent(start.session); live && a.PaneID != start.created.Pane.String() {
				return err
			}
			_ = n.interruptCodex(cleanup, start.session)
			interrupted = true // a lost reply is resolved by the next archive attempt
		}
		select {
		case <-cleanup.Done():
			return errors.New(i18n.T(i18n.Default, "node-codex-start.session_cleanup_failed", i18n.Args{"reason": err.Error()}))
		case <-time.After(250 * time.Millisecond): // the closed process releases its rollout writer lock
		}
	}
}

// closeCodexStart stops one abandoned start independently of other starts in the same directory.
func closeCodexStart(ctx context.Context, start codexAbandonedStart) error {
	org, ok := start.launch.(host.Organizer)
	var err error
	if !ok {
		err = host.ErrUnsupported
	} else {
		space := start.workspace
		if space == "" {
			space = start.created.SpaceID
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err = org.Close(cleanup, start.created.Pane, space, start.created.SlotID)
		var he *herdr.Error
		if errors.As(err, &he) {
			switch he.Code {
			case "pane_not_found":
				err = nil
			case "tab_not_found", "workspace_not_found":
				// A pane may have moved out of the missing tab. Only a fresh layout proving it absent clears it.
				if abandonedCodexPaneGone(cleanup, start) {
					err = nil
				}
			}
		}
		cancel()
	}
	if err != nil {
		return &rpc.Error{Code: proto.CodeAgentStartStalled,
			Message: i18n.T(i18n.Default, "node-codex-start.cleanup_failed", map[string]any{"pane": start.created.Pane.String(), "reason": err.Error()})}
	}
	return nil
}

func abandonedCodexPaneGone(ctx context.Context, start codexAbandonedStart) bool {
	h, ok := start.launch.(*herdrHost)
	if !ok {
		return false
	}
	target, err := h.target(ctx, start.created.Pane.ID)
	if err != nil {
		return false
	}
	// Host.Snapshot can substitute a cached named-session layout; only query this pane's actual server.
	layout, err := layoutOf(ctx, target.c, target.session)
	if err != nil || layout == nil {
		return false
	}
	matches := func(a host.Agent) bool { return a.PaneID == start.created.Pane.String() }
	return !slices.ContainsFunc(layout.Panes, matches) && !slices.ContainsFunc(layout.Agents, matches)
}
