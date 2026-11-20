package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/sessionhistory"
)

// historyNode is a machine of h whose node answers over real rpc lines with answer; asked records the methods.
func historyNode(t *testing.T, h *Hub, id string, answer func(method string, params json.RawMessage) (any, error)) (*Machine, func() []string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hp, np := rpc.New("h", h2nW), rpc.New("n", n2hW)
	m := newMachine(h, MachineConfig{ID: id, Name: id, Kind: "local"})
	m.peer = hp
	var mu sync.Mutex
	var asked []string
	np.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		mu.Lock()
		asked = append(asked, method)
		mu.Unlock()
		return answer(method, params)
	}
	done := make(chan struct{}, 2)
	go func() { defer func() { done <- struct{}{} }(); _ = hp.Serve(ctx, n2hR) }()
	go func() { defer func() { done <- struct{}{} }(); _ = np.Serve(ctx, h2nR) }()
	t.Cleanup(func() {
		cancel()
		for _, c := range []io.Closer{h2nR, h2nW, n2hR, n2hW} {
			_ = c.Close()
		}
		for range 2 {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("rpc cleanup timed out")
			}
		}
	})
	h.fleet.mu.Lock()
	h.machines, h.byID[id] = append(h.machines, m), m
	h.fleet.mu.Unlock()
	return m, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

// nodeHistory answers sessions.history from rows as a node does (window and order by the row's time, an offset cursor).
func nodeHistory(rows []sessionhistory.Row) func(string, json.RawMessage) (any, error) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].At().UnixMilli(), rows[j].At().UnixMilli()
		if a != b {
			return a > b
		}
		return rows[i].NodeKey() < rows[j].NodeKey()
	})
	return func(method string, params json.RawMessage) (any, error) {
		if method != "sessions.history" {
			return nil, fmt.Errorf("unknown method %s", method)
		}
		var p struct {
			Since, Until, Cursor string
			Limit                int
		}
		_ = json.Unmarshal(params, &p)
		since, _ := time.Parse(time.RFC3339Nano, p.Since)
		until, _ := time.Parse(time.RFC3339Nano, p.Until)
		var in []sessionhistory.Row
		for _, r := range rows {
			if t := r.At().UnixMilli(); t >= since.UnixMilli() && t < until.UnixMilli() {
				in = append(in, r)
			}
		}
		from, _ := strconv.Atoi(p.Cursor)
		to := min(len(in), from+p.Limit)
		ans := sessionhistory.Answer{Rows: in[from:to], Complete: true, Sources: []sessionhistory.SourceState{
			{Source: sessionhistory.SourceClaude, Honored: true, Complete: true},
			{Source: sessionhistory.SourceRuntime, Runtime: "toy", Reason: sessionhistory.ReasonUnsupported},
		}}
		if to < len(in) {
			ans.NextCursor = strconv.Itoa(to)
		}
		return ans, nil
	}
}

func historyGet(t *testing.T, srv *httptest.Server, token, path string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// The hub merges every machine's history pages newest first until each row came once, stamps the machine on each,
// keeps the history fields, and says which machines and sources were not honored.
func TestSessionsHistoryMergesMachinesPageByPage(t *testing.T) {
	h := newTestHub(t)
	now := time.Now().UTC().Add(-time.Minute) // the newest row before the window's end (until: now)
	rowsOf := func(prefix string, n int, every time.Duration) []sessionhistory.Row {
		var out []sessionhistory.Row
		for i := 0; i < n; i++ {
			r := sessionhistory.Row{Session: proto.Session{SessionID: fmt.Sprintf("%s%03d", prefix, i), Status: proto.StatusOffline,
				UpdatedAt: now.Add(-time.Duration(i) * every)}, Source: sessionhistory.SourceClaude}
			if i%10 == 0 { // archived just now, last active where it was
				r.ActiveAt, r.Session.UpdatedAt, r.Session.Archived = r.Session.UpdatedAt, now.Add(-time.Second), true
			}
			out = append(out, r)
		}
		return out
	}
	_, askedA := historyNode(t, h, "ma", nodeHistory(rowsOf("a", 300, 5*time.Hour)))
	_, askedB := historyNode(t, h, "mb", nodeHistory(rowsOf("b", 100, 13*time.Hour)))
	historyNode(t, h, "mc", func(method string, _ json.RawMessage) (any, error) {
		return nil, fmt.Errorf("unknown method %s", method)
	})
	off := newMachine(h, MachineConfig{ID: "md", Name: "md", Kind: "local"}) // no node connected
	h.machines, h.byID["md"] = append(h.machines, off), off
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)

	since := now.Add(-90 * 24 * time.Hour).Format(time.RFC3339Nano)
	var got []sessionhistory.Row
	cursor, pages := "", 0
	for {
		path := "/api/v1/sessions?include_inactive=1&limit=150&since=" + since
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		code, body := historyGet(t, srv, h.cfg.Token, path)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		var resp struct {
			Sessions []sessionhistory.Row `json:"sessions"`
			History  struct {
				Complete   bool                         `json:"complete"`
				NextCursor string                       `json:"next_cursor"`
				Since      string                       `json:"since"`
				Sources    []sessionhistory.SourceState `json:"sources"`
			} `json:"history"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatal(err)
		}
		pages++
		got = append(got, resp.Sessions...)
		if pages == 1 {
			reasons := map[string]string{}
			for _, s := range resp.History.Sources {
				if !s.Honored {
					reasons[s.Machine+"/"+s.Runtime] = s.Reason
				}
			}
			want := map[string]string{"ma/toy": "unsupported", "mb/toy": "unsupported", "mc/": "node_unsupported", "md/": "offline"}
			if fmt.Sprint(reasons) != fmt.Sprint(want) {
				t.Errorf("sources not honored: %v, want %v", reasons, want)
			}
			if resp.History.Complete || resp.History.Since == "" {
				t.Errorf("history meta: %+v", resp.History)
			}
		}
		if resp.History.NextCursor == "" {
			break
		}
		cursor = resp.History.NextCursor
		if pages > 20 {
			t.Fatal("no last page")
		}
	}
	// 300 every 5 hours is 62 days; 100 every 13 hours 54: all in 90 days.
	if pages != 3 || len(got) != 400 {
		t.Fatalf("%d pages, %d rows; want 3 pages of 400", pages, len(got))
	}
	seen := map[string]bool{}
	for i, r := range got {
		key := r.Session.MachineID + "/" + r.Session.SessionID
		if seen[key] {
			t.Fatalf("%s twice", key)
		}
		seen[key] = true
		if want := "m" + r.Session.SessionID[:1]; r.Session.MachineID != want {
			t.Fatalf("%s stamped %q", r.Session.SessionID, r.Session.MachineID)
		}
		if i > 0 && r.At().After(got[i-1].At()) {
			t.Fatalf("row %d (%s) is newer than the one before: newest first by active_at", i, key)
		}
		if strings.HasSuffix(r.Session.SessionID, "0") != (r.Session.Archived && !r.ActiveAt.IsZero()) {
			t.Fatalf("%s: archived %v active_at %v; every tenth is archived with its active_at kept", key, r.Session.Archived, r.ActiveAt)
		}
	}
	for _, asked := range [][]string{askedA(), askedB()} {
		for _, m := range asked {
			if m != "sessions.history" {
				t.Fatalf("the hub asked a node %s", m)
			}
		}
	}
}

// Without include_inactive the list is as it was (the nodes are not asked); a malformed history request is refused.
func TestSessionsWithoutIncludeInactiveAreUnchanged(t *testing.T) {
	h := newTestHub(t)
	m, asked := historyNode(t, h, "ma", nodeHistory(nil))
	m.mu.Lock()
	m.sessions = []proto.Session{{MachineID: "ma", SessionID: "live-1", Status: proto.StatusWorking}}
	m.mu.Unlock()
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	for _, path := range []string{"/api/v1/sessions", "/api/v1/sessions?include_inactive=0", "/api/v1/sessions?machine_id=ma"} {
		code, body := historyGet(t, srv, h.cfg.Token, path)
		if code != http.StatusOK || strings.Contains(string(body), `"history"`) || !strings.Contains(string(body), `"sessions"`) {
			t.Fatalf("%s: %d %s", path, code, body)
		}
	}
	for _, path := range []string{"/api/v1/sessions?include_inactive=1", "/api/v1/sessions?include_inactive=yes&since=2026-01-01T00:00:00Z",
		"/api/v1/sessions?include_inactive=1&since=yesterday", "/api/v1/sessions?include_inactive=1&since=2026-01-01T00:00:00Z&limit=0",
		"/api/v1/sessions?include_inactive=1&since=2026-01-01T00:00:00Z&cursor=%25%25"} {
		if code, body := historyGet(t, srv, h.cfg.Token, path); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	if got := asked(); len(got) != 0 {
		t.Fatalf("the node was asked %v", got)
	}
}

// blockedNode answers sessions.history as a node does when its newest rows are left out (a blocked folder): it pages
// past them with its own key cursor, sending only the visible ones.
func blockedNode(rows []sessionhistory.Row, hidden func(sessionhistory.Row) bool) func(string, json.RawMessage) (any, error) {
	keyOf := func(r sessionhistory.Row) sessionhistory.Key { return sessionhistory.KeyOf(r.At(), r.NodeKey()) }
	sort.SliceStable(rows, func(i, j int) bool { return keyOf(rows[i]).Before(keyOf(rows[j])) })
	return func(method string, params json.RawMessage) (any, error) {
		var p struct {
			Since, Until, Cursor string
			Limit                int
		}
		_ = json.Unmarshal(params, &p)
		since, _ := time.Parse(time.RFC3339Nano, p.Since)
		until, _ := time.Parse(time.RFC3339Nano, p.Until)
		after, _ := sessionhistory.DecodeCursor(p.Cursor)
		ans := sessionhistory.Answer{Rows: []sessionhistory.Row{}, Complete: true}
		read := 0
		for _, r := range rows {
			k := keyOf(r)
			if t := r.At().UnixMilli(); t < since.UnixMilli() || t >= until.UnixMilli() || after != nil && !after.Before(k) {
				continue
			}
			if read == p.Limit {
				return ans, nil
			}
			read++
			if !hidden(r) {
				ans.Rows = append(ans.Rows, r)
			}
			ans.NextCursor = ""
			if read == p.Limit {
				ans.NextCursor = sessionhistory.EncodeCursor(k)
			}
		}
		ans.NextCursor = ""
		return ans, nil
	}
}

// A machine whose node leaves out more of its newest rows than the hub reads for a page still has its older visible
// rows listed, before another machine's older ones and without ending the pages early (#10053).
func TestSessionsHistoryPagesPastAMachinesRowsLeftOut(t *testing.T) {
	h := newTestHub(t)
	now := time.Now().UTC()
	row := func(id string, at time.Time) sessionhistory.Row {
		return sessionhistory.Row{Session: proto.Session{SessionID: id, Status: proto.StatusOffline, UpdatedAt: at}, Source: sessionhistory.SourceRuntime, Runtime: "dsh"}
	}
	var rows []sessionhistory.Row
	at := now.Add(-time.Minute)
	for i := 0; i < 1100; i++ {
		rows = append(rows, row(fmt.Sprintf("auto%04d", i), at))
		at = at.Add(-10 * time.Second)
	}
	rows = append(rows, row("a0", at.Add(-time.Minute)), row("a1", at.Add(-2*time.Minute)))
	historyNode(t, h, "ma", blockedNode(rows, func(r sessionhistory.Row) bool { return strings.HasPrefix(r.Session.SessionID, "auto") }))
	historyNode(t, h, "mb", nodeHistory([]sessionhistory.Row{row("b0", now.Add(-20*time.Hour)), row("b1", now.Add(-21*time.Hour))}))
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)

	var ids []string
	cursor := ""
	for page := 0; ; page++ {
		if page > 40 {
			t.Fatalf("no last page; so far %v", ids)
		}
		path := "/api/v1/sessions?include_inactive=1&limit=1&since=" + now.Add(-48*time.Hour).Format(time.RFC3339Nano)
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		code, body := historyGet(t, srv, h.cfg.Token, path)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		var resp struct {
			Sessions []sessionhistory.Row `json:"sessions"`
			History  struct {
				NextCursor string `json:"next_cursor"`
			} `json:"history"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatal(err)
		}
		for _, r := range resp.Sessions {
			ids = append(ids, r.Session.SessionID)
		}
		if cursor = resp.History.NextCursor; cursor == "" {
			break
		}
	}
	if strings.Join(ids, ",") != "a0,a1,b0,b1" {
		t.Fatalf("rows %v; want a0,a1,b0,b1", ids)
	}
}
