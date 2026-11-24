package resourcequeue

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestV1QueueAndResult(t *testing.T) {
	raw, err := os.ReadFile("testdata/queue-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	q, err := ParseQueue(raw)
	if err != nil {
		t.Fatal(err)
	}
	if q.Slots != 3 || len(q.Running) != 3 || len(q.Waiting) != 5 || q.Waiting[0].DuplicateOf != "t1" {
		t.Fatalf("bad queue: %+v", q)
	}
	for _, s := range []string{`{"v":2}`, `{"v":1,"slots":3}`, `{"v":1,"slots":3,"now":"bad","running":[],"waiting":[]}`} {
		if _, err := ParseQueue([]byte(s)); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	o, err := ParseResult([]byte(`{"v":1,"exit":0,"run":"example-run","finished_at":"2026-11-22T10:00:00Z"}`))
	if err != nil || o.Exit != 0 {
		t.Fatalf("result: %+v %v", o, err)
	}
	if _, err := ParseResult([]byte(`{"v":1,"run":"example-run","finished_at":"2026-11-22T10:00:00Z"}`)); err == nil {
		t.Fatal("missing exit interpreted as success")
	}
	if o, err := ParseResult([]byte(`null`)); err != nil || o != nil {
		t.Fatalf("missing result: %v %v", o, err)
	}
}
func TestNewJoinedCachedAndFencing(t *testing.T) {
	now := time.Now().UTC()
	rows := []Lease{}
	in := Request{Resource: "ci.example", Project: "example", Commit: strings.Repeat("a", 40), Groups: []string{"web", "scripts", "web"}, Holder: Holder{Machine: "computer-a", Session: "one"}}
	first, err := Acquire(&rows, in, now, "one", 1)
	if err != nil || first.Status != "queued" {
		t.Fatalf("new: %+v %v", first, err)
	}
	Advance(rows, 1, 0, now)
	if rows[0].State != "granted" {
		t.Fatal(rows)
	}
	in.Groups = []string{"scripts", "web"}
	in.Holder.Session = "two"
	joined, err := Acquire(&rows, in, now, "two", 2)
	if err != nil || joined.Status != "joined" || len(rows) != 1 {
		t.Fatalf("join: %+v %v", joined, err)
	}
	if _, err := Mutate(rows, Request{Action: "release", ID: "one", Holder: in.Holder, Token: 1}, now); err == nil {
		t.Fatal("joiner released holder")
	}
	rep, err := Mutate(rows, Request{Action: "release", ID: "one", Holder: rows[0].Holder, Token: 1, Outcome: &Outcome{Exit: 0, Run: "example-run"}}, now)
	if err != nil || rep.Status != "released" {
		t.Fatalf("release: %+v %v", rep, err)
	}
	cached, err := Acquire(&rows, in, now, "three", 3)
	if err != nil || cached.Status != "cached" || cached.Lease.Outcome.Run != "example-run" {
		t.Fatalf("cache: %+v %v", cached, err)
	}
	if CacheOK(rows[0].Outcome, now.Add(13*time.Hour)) {
		t.Fatal("old cache accepted")
	}
}
func TestAdmissionLimitFIFOExpiryAndExternalSlots(t *testing.T) {
	now := time.Now()
	rows := []Lease{}
	for i := 0; i < 3; i++ {
		in := Request{Resource: "ci.example", Project: "example", Commit: strings.Repeat(string(rune('a'+i)), 40), Holder: Holder{Machine: "computer-a", Session: "one"}}
		rep, err := Acquire(&rows, in, now, string(rune('a'+i)), int64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 && rep.Status != "refused" {
			t.Fatal(rep)
		}
	}
	Advance(rows, 3, 2, now)
	if rows[0].State != "granted" || rows[1].State != "queued" {
		t.Fatal(rows)
	}
	if _, err := Mutate(rows, Request{Action: "renew", ID: rows[1].ID, Token: 0, Holder: rows[1].Holder}, now.Add(60*time.Second)); err != nil {
		t.Fatal(err)
	}
	Advance(rows, 3, 2, now.Add(90*time.Second))
	if rows[0].State != "expired" || rows[1].State != "granted" {
		t.Fatal(rows)
	}
	rep, _ := Mutate(rows, Request{Action: "renew", ID: rows[0].ID, Token: 1, Holder: rows[0].Holder}, now.Add(90*time.Second))
	if rep.Status != "fenced" {
		t.Fatal(rep)
	}
}
func TestRunnerUnavailableAndLegacyFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := filepath.Join(dir, "ssh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 255\n"), 0700); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadQueue(context.Background(), Mirror{Target: "runner.example.com"})
	if err == nil || !snap.Unknown || !snap.ObservedAt.IsZero() {
		t.Fatalf("unreachable: %+v %v", snap, err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncase \"$*\" in *--json*) exit 64;; esac\necho 'running: ticket-1'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	snap, err = ReadQueue(context.Background(), Mirror{Target: "runner.example.com"})
	if err != nil || !snap.Unknown || snap.Queue != nil || !strings.Contains(snap.Raw, "ticket-1") {
		t.Fatalf("legacy: %+v %v", snap, err)
	}
}

func TestPriorityCancellationAndWaitTimeout(t *testing.T) {
	now := time.Now()
	rows := []Lease{
		{ID: "normal", State: "queued", Seq: 1, WaitUntil: now.Add(time.Hour), Holder: Holder{Machine: "computer-a", Session: "one"}},
		{ID: "urgent", State: "queued", Seq: 2, Priority: 1, WaitUntil: now.Add(time.Hour), Holder: Holder{Machine: "computer-a", Session: "two"}},
	}
	Advance(rows, 1, 0, now)
	if rows[0].State != "queued" || rows[1].State != "granted" {
		t.Fatal(rows)
	}
	cancelled, err := Mutate(rows, Request{Action: "cancel", ID: "normal", Holder: rows[0].Holder}, now)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	rows = append(rows, Lease{ID: "late", State: "queued", Seq: 3, WaitUntil: now})
	Advance(rows, 1, 0, now)
	if rows[2].State != "timeout" {
		t.Fatal(rows)
	}
}

func TestV1LegacyEmptyFieldsRetainIndependentEntries(t *testing.T) {
	raw, err := os.ReadFile("testdata/queue-v1-legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	queue, err := ParseQueue(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Running) != 3 || queue.Running[0].Ticket != "" || queue.Running[1].Commit != "" || queue.Running[1].Tag != "" {
		t.Fatal(queue)
	}
	if queue.Waiting[0].DuplicateOf != "example-build-one" {
		t.Fatal(queue.Waiting)
	}
	if queue.Running[1].DuplicateOf != "" || queue.Running[2].DuplicateOf != "" || queue.Waiting[1].DuplicateOf != "" {
		t.Fatal("empty commits were coalesced")
	}
	if queue.Running[1].Identity() == queue.Running[2].Identity() {
		t.Fatal("legacy entries lost their identity")
	}
	outcome, err := ParseResult([]byte(`{"v":1,"project":"example","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","groups":["web"],"exit":null,"run":"","finished_at":"","tag":""}`))
	if err != nil || outcome != nil {
		t.Fatalf("no-result response: %+v %v", outcome, err)
	}
}

func TestQueuedClientHeartbeatExpiresWithoutPromotingDeadWork(t *testing.T) {
	now := time.Now()
	rows := []Lease{}
	in := Request{Resource: "ci.example", Project: "example", Commit: strings.Repeat("a", 40), Holder: Holder{Machine: "computer-a", Session: "dead"}}
	if _, err := Acquire(&rows, in, now, "dead", 1); err != nil {
		t.Fatal(err)
	}
	Advance(rows, 1, 1, now)
	Advance(rows, 1, 0, now.Add(90*time.Second))
	if rows[0].State != "expired" {
		t.Fatalf("dead queued client was admitted: %+v", rows[0])
	}
}

func TestLegacyRunIdentitySurvivesAnEmptyRunningTicket(t *testing.T) {
	waiting := Job{Ticket: "000000000034", Run: "example-legacy-run", Position: 1}
	running := Job{Ticket: "", Run: "example-legacy-run", Slot: 1}
	if waiting.Identity() != running.Identity() {
		t.Fatal("legacy queue-to-slot transition lost its result identity")
	}
}

func TestActiveRunTakesPrecedenceOverPreviousCachedSuccess(t *testing.T) {
	now := time.Now().UTC()
	in := Request{Resource: "ci.example", Project: "example", Commit: strings.Repeat("a", 40), Holder: Holder{Machine: "computer-a", Session: "joining"}}
	key, err := Key(in.Project, in.Commit, in.Groups)
	if err != nil {
		t.Fatal(err)
	}
	rows := []Lease{
		{ID: "previous", Key: key, State: "released", Outcome: &Outcome{Run: "previous-run", Exit: 0, FinishedAt: now.Add(-time.Minute)}},
		{ID: "current", Key: key, State: "granted", Holder: Holder{Machine: "computer-a", Session: "holder"}},
	}
	reply, err := Acquire(&rows, in, now, "new", 3)
	if err != nil || reply.Status != "joined" || reply.Lease.ID != "current" || len(rows) != 2 {
		t.Fatalf("did not join the current run: %+v %v", reply, err)
	}
}
