package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
)

// The tier a tiered persona shows (docs/apple/persona.md "Tiered avatars"): the level of the user on the board the package names,
// read every 10 minutes (with the board's ETag, so an unchanged board is a 304). Who the user is on the board is their
// GitHub login: the GitHub connector's account, or hub.json's personas.github_login. No login (not signed in, not on
// the board): the default tier. The board out of reach: the tier stays as it was. The tier last seen is kept in
// Postgres, so a restart (someone redeploying) does not show the default until the board answers; a change is
// broadcast ("personas") and the apps list the personas again, downloading the new tier before they show it.

const (
	// boardKindNWVerse is NWVERSE's contribution board (nwverse/contribution, data/board.json, schema 1): people[] with
	// login, level (null before hatching) and newbie.status (pending, passed, failed).
	boardKindNWVerse = "nwverse-board"
	personaTiersKey  = "persona_tiers"
	// The board is published every 30 minutes and GitHub Pages caches it for 10.
	personaTiersEvery = 10 * time.Minute
	// After a start, a minute for the GitHub connector to learn whose login it has.
	personaTiersFirst = time.Minute
	boardMaxBytes     = 8 << 20
)

// PersonasConfig tunes the looks (docs/apple/persona.md).
type PersonasConfig struct {
	// GitHubLogin is whose level a tiered look shows, instead of the GitHub connector's account.
	GitHubLogin string `json:"github_login,omitempty"`
}

// tierSeen is the tier a package showed last, and for whom.
type tierSeen struct {
	Tier  string    `json:"tier"`
	Login string    `json:"login,omitempty"`
	At    time.Time `json:"at"`
}

// personaTierState is what the hub knows of the tiers: per package the tier last seen, per board the last copy read.
type personaTierState struct {
	mu           sync.Mutex
	writeMu      sync.Mutex // serializes normal and resumed PutSetting writes
	seen         map[string]tierSeen
	boards       map[string]*boardCopy
	saveDeferred bool
	saveGen      uint64
	// http reads the boards (tests: a TLS test server's client).
	http *http.Client
}

type boardCopy struct {
	etag, modified string
	people         map[string]*boardPerson // lower-cased login → the record
}

type boardPerson struct {
	Login  string `json:"login"`
	Level  *int   `json:"level"`
	Newbie struct {
		Status string `json:"status"`
	} `json:"newbie"`
}

// personaTierKey is the tier the package shows now: the one last seen, else the default.
func (h *Hub) personaTierKey(id string, t *personaTiers) string {
	h.tiers.mu.Lock()
	defer h.tiers.mu.Unlock()
	if s, ok := h.tiers.seen[id]; ok && t.tier(s.Tier) != nil {
		return s.Tier
	}
	return t.Default
}

func (h *Hub) personaTiersLoop(ctx context.Context) {
	h.loadPersonaTiers(ctx)
	timer := time.NewTimer(personaTiersFirst)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		h.refreshPersonaTiers(ctx)
		timer.Reset(personaTiersEvery)
	}
}

func (h *Hub) loadPersonaTiers(ctx context.Context) {
	if h.store == nil {
		return
	}
	seen := map[string]tierSeen{}
	if _, err := h.store.Setting(ctx, personaTiersKey, &seen); err != nil {
		h.logf("persona tiers: %v", err)
		return
	}
	h.tiers.mu.Lock()
	h.tiers.seen = seen
	h.tiers.mu.Unlock()
}

// personaLogin is whose level the tiered looks show; known is false while the GitHub connector has not yet said
// whether it is signed in (right after a start), when the tiers stay as they are.
func (h *Hub) personaLogin(ctx context.Context) (login string, known bool) {
	if l := strings.TrimSpace(h.cfg.Personas.GitHubLogin); l != "" {
		return l, true
	}
	// The GitHub connector is the manager's: while it is away the tiers stay as they are.
	var res struct {
		Login string `json:"login"`
		Known bool   `json:"known"`
	}
	if h.busConnOf(h.defaultOwner(ctx)) == nil || h.extCall(ctx, extwire.HubGitHubLogin, map[string]any{}, &res) != nil {
		return "", false
	}
	return res.Login, res.Known
}

// refreshPersonaTiers reads the boards and moves each tiered package to the user's tier.
func (h *Hub) refreshPersonaTiers(ctx context.Context) {
	login, known := h.personaLogin(ctx)
	if !known {
		return
	}
	read := map[string]map[string]*boardPerson{}
	for _, p := range h.personaPackages() {
		t := p.m.Tiers
		if t == nil {
			continue
		}
		key := t.Default
		if login != "" {
			people, ok := read[t.Source.URL]
			if !ok {
				var err error
				if people, err = h.readBoard(ctx, t.Source.URL); err != nil {
					h.logf("persona %s: the board: %v (the tier stays)", p.id, err)
					continue
				}
				read[t.Source.URL] = people
			}
			key = t.tierFor(people[strings.ToLower(login)])
		}
		h.setPersonaTier(ctx, p.id, t, key, login)
	}
}

// tierFor is the tier of a record on the board: the highest level tier at or under its level (a board with a level
// the package does not draw yet shows the highest it has), the newcomer's trial before it hatched, else the default.
func (t *personaTiers) tierFor(p *boardPerson) string {
	if p == nil {
		return t.Default
	}
	best, bestLevel := "", -1
	for _, l := range t.Levels {
		switch {
		case p.Level != nil && l.When.Level != nil && *l.When.Level <= *p.Level && *l.When.Level > bestLevel:
			best, bestLevel = l.Key, *l.When.Level
		case p.Level == nil && l.When.Newbie != "" && l.When.Newbie == p.Newbie.Status:
			return l.Key
		}
	}
	if best != "" {
		return best
	}
	return t.Default
}

func (h *Hub) setPersonaTier(ctx context.Context, id string, t *personaTiers, key, login string) {
	before := h.personaTierKey(id, t)
	h.tiers.mu.Lock()
	prev, had := h.tiers.seen[id]
	if had && prev.Tier == key && prev.Login == login {
		h.tiers.mu.Unlock()
		return
	}
	if h.tiers.seen == nil {
		h.tiers.seen = map[string]tierSeen{}
	}
	h.tiers.seen[id] = tierSeen{Tier: key, Login: login, At: time.Now().UTC()}
	if h.store != nil {
		h.tiers.saveDeferred = true
		h.tiers.saveGen++
	}
	h.tiers.mu.Unlock()
	if h.store != nil {
		h.flushPersonaTiers(ctx)
	}
	if key != before {
		h.logf("persona %s: tier %s → %s", id, before, key)
		h.broadcast(map[string]any{"type": "personas"})
	}
}

func (h *Hub) flushPersonaTiers(ctx context.Context) {
	if h.store == nil {
		return
	}
	h.tiers.writeMu.Lock()
	defer h.tiers.writeMu.Unlock()
	for {
		h.tiers.mu.Lock()
		if !h.tiers.saveDeferred {
			h.tiers.mu.Unlock()
			return
		}
		h.tiers.saveDeferred = false
		gen := h.tiers.saveGen
		seen := make(map[string]tierSeen, len(h.tiers.seen))
		for k, v := range h.tiers.seen {
			seen[k] = v
		}
		h.tiers.mu.Unlock()
		if !h.quiesce.beginOrDefer(func() {
			h.tiers.mu.Lock()
			if h.tiers.saveGen == gen {
				h.tiers.saveDeferred = true
			}
			h.tiers.mu.Unlock()
		}) {
			return
		}
		wctx := ctx
		var cancel context.CancelFunc
		if wctx == nil || wctx.Err() != nil {
			wctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		}
		if err := h.store.PutSetting(wctx, personaTiersKey, seen); err != nil {
			h.logf("persona tiers: %v", err)
		}
		if cancel != nil {
			cancel()
		}
		h.quiesce.end()
	}
}

func (h *Hub) resumePersonaTiers() {
	if h.store == nil {
		return
	}
	h.tiers.mu.Lock()
	deferred := h.tiers.saveDeferred
	h.tiers.mu.Unlock()
	if !deferred {
		return
	}
	h.nodeWorkers.Add(1)
	go func() {
		defer h.nodeWorkers.Done()
		h.flushPersonaTiers(nil)
	}()
}

// readBoard is the board's people by login: a new copy, or the last one when the board has not changed (304).
func (h *Hub) readBoard(ctx context.Context, url string) (map[string]*boardPerson, error) {
	h.tiers.mu.Lock()
	last := h.tiers.boards[url]
	client := h.tiers.http
	h.tiers.mu.Unlock()
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if last != nil {
		if last.etag != "" {
			req.Header.Set("If-None-Match", last.etag)
		}
		if last.modified != "" {
			req.Header.Set("If-Modified-Since", last.modified)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && last != nil {
		return last.people, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	var board struct {
		Schema int           `json:"schema"`
		People []boardPerson `json:"people"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, boardMaxBytes)).Decode(&board); err != nil {
		return nil, err
	}
	if board.Schema != 1 {
		return nil, fmt.Errorf("schema %d, this hub reads 1", board.Schema)
	}
	people := make(map[string]*boardPerson, len(board.People))
	for i := range board.People {
		people[strings.ToLower(board.People[i].Login)] = &board.People[i]
	}
	h.tiers.mu.Lock()
	if h.tiers.boards == nil {
		h.tiers.boards = map[string]*boardCopy{}
	}
	h.tiers.boards[url] = &boardCopy{etag: resp.Header.Get("ETag"), modified: resp.Header.Get("Last-Modified"), people: people}
	h.tiers.mu.Unlock()
	return people, nil
}
