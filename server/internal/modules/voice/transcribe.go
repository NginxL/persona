package voice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Meeting transcription (#3862 phase 5, docs/assistant/meeting-minutes.md "The `transcription` extension point" and
// "The built-in adapter"; docs/assistant/voice.md "Transcription"): the built-in voice extension's transcription
// contribution, on its own socket under /transcription/{cid}/, apart from the voice point's /info and /ws. Only the
// manager dials it. A job is one WebSocket: the meeting's audio in, its sentences out, transcribed by one caption
// session and two or three verbatim sessions of a GPT-Live-compatible (live dialect) upstream, or in the final pass
// by fresh verbatim sessions per window at four times real time. The adapter stores nothing.

// TranscriptionContribution is the built-in voice extension's transcription contribution's id.
const TranscriptionContribution = "gpt-live"

// trJobs counts the jobs since the extension started: a job's id in the log.
var trJobs atomic.Int64

// errNoLiveUpstream: upstreams are set up, none of them GPT-Live-compatible (the live dialect).
var errNoLiveUpstream = errors.New("no GPT-Live-compatible upstream is set up")

// transcriptionRoutes adds the transcription point's routes, GET /transcription/{cid}/info and
// GET /transcription/{cid}/ws, to the plugin's API.
func (pl *Plugin) transcriptionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /transcription/{cid}/info", pl.serveTranscriptionInfo)
	mux.HandleFunc("GET /transcription/{cid}/ws", pl.serveTranscriptionWS)
}

// transcriptionCaller answers a request that is not for this contribution (404) or comes from someone other than the
// manager (403: the apps' forward always names the caller's role, and only a full one may get this far; the manager
// dials the extension's socket itself).
func (pl *Plugin) transcriptionCaller(w http.ResponseWriter, r *http.Request) bool {
	if r.PathValue("cid") != TranscriptionContribution {
		writeHTTPJSON(w, http.StatusNotFound, map[string]any{"error": "no such transcription contribution", "code": "not_found"})
		return false
	}
	if role := r.Header.Get(extwire.HeaderRole); role != "" && role != roleFull {
		writeHTTPJSON(w, http.StatusForbidden, map[string]any{"error": "only the manager transcribes meetings", "code": codeForbidden})
		return false
	}
	return true
}

// serveTranscriptionInfo is GET /transcription/{cid}/info: {protocol, features, setup?}. setup is the voice
// extension's when no upstream is set up at all, live_upstream when none is GPT-Live-compatible; interim only once a
// session of the upstream was granted interim captions (the fake dialect always has them).
func (pl *Plugin) serveTranscriptionInfo(w http.ResponseWriter, r *http.Request) {
	if !pl.transcriptionCaller(w, r) {
		return
	}
	out := extwire.TranscriptionInfo{Protocol: extwire.TranscriptionProtocol,
		Features: []string{extwire.TranscriptionLive, extwire.TranscriptionReplay}}
	p, _ := pl.transcribeProvider()
	switch {
	case len(pl.cfg.Providers) == 0:
		out.Setup = firstNonEmpty(pl.cfg.Setup, SetupURLAndKey)
	case p == nil:
		out.Setup = extwire.TSetupLiveUpstream
	case p.Dialect == DialectFake || trMemoOf(p).captions.Load() == trGranted:
		out.Features = append(out.Features, extwire.TranscriptionInterim)
	}
	writeHTTPJSON(w, http.StatusOK, out)
}

// serveTranscriptionWS is GET /transcription/{cid}/ws: one job per connection.
func (pl *Plugin) serveTranscriptionWS(w http.ResponseWriter, r *http.Request) {
	if !pl.transcriptionCaller(w, r) {
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // the extension's own socket, 0600
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20) // a reference frame, or a start with the whole glossary
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	newTrJob(pl, c, ctx, cancel, i18n.Resolve(r.Header.Get(extwire.HeaderLang))).run()
}

// newTrJob is a job on socket c. Its sessions' dial outcomes (ups) go to the loop unbuffered: once the loop is gone
// only the job's end can take one, and the session's socket is closed (a buffered one could hand a started session
// to nobody, its socket open and billed until the service drops it).
func newTrJob(pl *Plugin, c *websocket.Conn, ctx context.Context, cancel context.CancelFunc, uiLang i18n.Lang) *trJob {
	return &trJob{pl: pl, id: trJobs.Add(1), c: c, ctx: ctx, cancel: cancel, began: time.Now(), uiLang: uiLang,
		in: make(chan trIn, 64), ups: make(chan trUp), reads: make(chan trRead, 256)}
}

// usableForTranscription: a provider meeting transcription can use, GPT-Live-compatible (or the fake dialect).
func usableForTranscription(p *Provider) bool {
	return p.Dialect == DialectLive || p.Dialect == DialectFake
}

// transcribeProvider is the upstream meeting transcription uses, without asking any service: the default model's
// when it is GPT-Live-compatible (def), else the first that is; nil when none is.
func (pl *Plugin) transcribeProvider() (p *Provider, def bool) {
	if len(pl.cfg.Providers) == 0 {
		return nil, false
	}
	dp := &pl.cfg.Providers[0]
	if pid, _, ok := splitModel(pl.cfg.Default); ok {
		if x := pl.cat.provider(pid); x != nil {
			dp = x
		}
	}
	if usableForTranscription(dp) {
		return dp, true
	}
	for i := range pl.cfg.Providers {
		if usableForTranscription(&pl.cfg.Providers[i]) {
			return &pl.cfg.Providers[i], false
		}
	}
	return nil, false
}

// transcriber is the upstream and model a job transcribes with: the voice extension's default model when its upstream
// is GPT-Live-compatible, else the first such upstream's default model.
func (pl *Plugin) transcriber(ctx context.Context) (*Provider, string, error) {
	p, def := pl.transcribeProvider()
	switch {
	case p == nil && len(pl.cfg.Providers) == 0:
		return nil, "", errNoUpstream
	case p == nil:
		return nil, "", errNoLiveUpstream
	}
	if def {
		rp, model, err := pl.cat.resolve(ctx, "")
		if err == nil && rp == p {
			return p, model, nil
		}
		if err != nil && !errors.Is(err, errNoProvider) {
			return nil, "", err
		}
	}
	pm, err := pl.cat.providerModels(ctx, p)
	if err != nil {
		return nil, "", err
	}
	if pm.def == "" {
		return nil, "", fmt.Errorf("%s: no model to use (set default_model)", p.name())
	}
	return p, pm.def, nil
}

// What the plugin learned of an upstream's sessions, for as long as it runs (trMemoOf).
const (
	trUnknown    int32 = iota
	trGranted          // a caption session was granted the caption extension
	trNotGranted       // it was not, or the service refused it: its jobs run three verbatim sessions
)

type trMemo struct {
	captions   atomic.Int32
	noTextOnly atomic.Bool // the service refused text-only output: its sessions start without it
}

var trMemos sync.Map // provider id and address → *trMemo

func trMemoOf(p *Provider) *trMemo {
	m, _ := trMemos.LoadOrStore(p.ID+"\x00"+p.URL, &trMemo{})
	return m.(*trMemo)
}
