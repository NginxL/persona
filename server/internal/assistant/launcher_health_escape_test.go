package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A shell startup banner in front of the real 401 must not turn the record into cli_outdated: the noise goes before the
// classification reads the words, and the stored error is the 401 alone.
func TestRecordHealthCleansBeforeClassifying(t *testing.T) {
	s := newStorelessService(t, newFakeHub())
	ctx := context.Background()
	s.recordHealth(ctx, "dev", "work", healthResult{err: "[oh-my-zsh] Would you like to update?\nAPI Error: 401 invalid key"})
	r := healthState(s, "dev", "work")
	if r.Kind != errKindAuth {
		t.Fatalf("kind = %q, want %q (record %+v)", r.Kind, errKindAuth, r)
	}
	if r.Error != "API Error: 401 invalid key" {
		t.Fatalf("error = %q", r.Error)
	}
	if r.State != healthFailing {
		t.Fatalf("state = %q", r.State)
	}
}

func TestRecordHealthStripsEscapesFromError(t *testing.T) {
	s := newStorelessService(t, newFakeHub())
	ctx := context.Background()
	s.recordHealth(ctx, "dev", "work", healthResult{err: "\x1b[1;31mError: command not found: claude\x1b[0m"})
	r := healthState(s, "dev", "work")
	if r.Error != "Error: command not found: claude" {
		t.Fatalf("error = %q", r.Error)
	}
	if strings.ContainsRune(r.Error, 0x1b) {
		t.Fatalf("escape left in %q", r.Error)
	}
}

// Records persisted by an older hub still carry escapes: the lists hand out clean copies and leave the stored record
// as it was.
func TestHealthRecordsCleanStoredRecords(t *testing.T) {
	s := newStorelessService(t, newFakeHub())
	raw := "\x1b[31mError: upstream refused\x1b[0m\n"
	reason := "\x1b[33mwhy it is blocked\x1b[0m\nsecond"
	s.health.mu.Lock()
	s.health.loaded = true // the record is already there: nothing to load from a store
	s.health.recs = map[string]*LauncherHealth{
		healthKey("dev", "work"): {Machine: "dev", Launcher: "work", State: healthBlocked, Kind: errKindOther, Error: raw, BlockedReason: reason},
	}
	s.health.mu.Unlock()
	got := s.healthRecords(context.Background(), "", "dev")
	if len(got) != 1 {
		t.Fatalf("records: %d", len(got))
	}
	if got[0].Error != "Error: upstream refused" {
		t.Fatalf("error = %q", got[0].Error)
	}
	if got[0].BlockedReason != "why it is blocked second" {
		t.Fatalf("reason = %q", got[0].BlockedReason)
	}
	if one, _ := s.launcherHealth(context.Background(), "dev", "work"); one.Error != "Error: upstream refused" {
		t.Fatalf("launcherHealth error = %q", one.Error)
	}
	stored := s.health.recs[healthKey("dev", "work")]
	if stored.Error != raw || stored.BlockedReason != reason {
		t.Fatalf("the stored record was changed: %+v", stored)
	}
}

func TestSetHealthStateCleansBlockedReason(t *testing.T) {
	s := newStorelessService(t, newFakeHub())
	ctx := context.Background()
	s.recordHealth(ctx, "dev", "work", healthResult{err: "Error: broken"})
	s.setHealthState(ctx, "dev", "work", healthBlocked, "\x1b[31mwhy\x1b[0m\nsecond", nil)
	r := healthState(s, "dev", "work")
	if r.State != healthBlocked {
		t.Fatalf("state = %q", r.State)
	}
	if r.BlockedReason != "why second" {
		t.Fatalf("reason = %q", r.BlockedReason)
	}
}

// A check answered by a node from before the cleanup is cleaned before the hub hands it on.
func TestCleanCheck(t *testing.T) {
	c := proto.LauncherCheck{
		Error:  "\x1b[31mAPI Error: 401 invalid key\x1b[0m",
		Answer: "\x1b]1337;CurrentDir=/Users/dev\x07OK",
	}
	cleanCheck(&c)
	if c.Error != "API Error: 401 invalid key" || c.Answer != "OK" {
		t.Fatalf("check = %+v", c)
	}
	blank := proto.LauncherCheck{OK: true}
	cleanCheck(&blank)
	if blank.Error != "" || blank.Answer != "" {
		t.Fatalf("blank check changed: %+v", blank)
	}
}
