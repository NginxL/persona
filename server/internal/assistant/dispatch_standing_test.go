package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestDispatchReadComputerStanding(t *testing.T) {
	for _, tc := range []struct {
		name, status, state, wording                  string
		missing, unlisted, away, removed, young, read bool
	}{
		{name: "offline computer retains its last list", status: proto.StatusUnknown, away: true,
			state: "computer offline", wording: "is offline now"},
		{name: "recent start on offline computer", away: true, missing: true, young: true,
			state: "computer offline", wording: "is offline now"},
		{name: "removed computer", removed: true, young: true,
			state: "computer removed", wording: "no longer one of the computers"},
		{name: "not in received list", missing: true, state: "not listed", wording: "last reply was not read this time"},
		{name: "list not received", missing: true, unlisted: true, state: "not read", wording: "read it again later"},
		{name: "unknown host status", status: proto.StatusUnknown, state: "state unknown", wording: "may not be its result", read: true},
		{name: "empty host status", state: "state unknown", wording: "may not be its result", read: true},
		{name: "session offline on connected computer", status: proto.StatusOffline, state: "interrupted", read: true},
		{name: "young session not listed", young: true, missing: true, state: "working", wording: "not its result yet", read: true},
		{name: "young session awaits first list", young: true, unlisted: true, missing: true, state: "working", wording: "not its result yet", read: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTest(t)
			if !tc.removed {
				node := ft.d.add("box", "box")
				node.away, node.unlisted = tc.away, tc.unlisted
				if !tc.missing {
					ft.d.setSessions("box", []proto.Session{{SessionID: "s1", Status: tc.status}})
				}
			}
			ft.f.setSessions("box")
			ft.f.setItems(proto.Item{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: "fixture reply"})
			age := time.Hour
			if tc.young {
				age = time.Second
			}
			ctx := context.Background()
			ft.s.addDispatched(ctx, dispatchTask{FromMachine: "mac", FromSession: "caller", Machine: "box",
				Session: "s1", Cwd: "/code/app", At: time.Now().Add(-age)})
			out, err := ft.s.dispatchRead(ctx, "mac", "caller", json.RawMessage(`{"session_id":"s1"}`))
			if err != nil || !strings.HasPrefix(out, "Task s1 on box (/code/app): "+tc.state+".") ||
				!strings.Contains(out, tc.wording) || strings.Contains(out, "fixture reply") != tc.read {
				t.Errorf("state response = %q, error = %v", out, err)
			}
			overview, err := ft.s.dispatchRead(ctx, "mac", "caller", json.RawMessage(`{}`))
			if err != nil || !strings.Contains(overview, "- s1 on box (/code/app): "+tc.state+"\n") {
				t.Errorf("overview = %q, error = %v", overview, err)
			}
			ft.f.mu.Lock()
			defer ft.f.mu.Unlock()
			reads := 0
			for _, call := range ft.f.calls {
				if call.Device == "box" && call.Method == DeviceSessionItems {
					reads++
				}
			}
			want := 0
			if tc.read {
				want = 1
			}
			if reads != want {
				t.Errorf("transcript calls = %d, want %d", reads, want)
			}
		})
	}
}

func TestDispatchReadLookupErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		state string
	}{
		{"missing computer", ErrNoDevice, "computer removed"},
		{"lookup failed", errors.New("fixture lookup failed"), "not read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := newDispatchService(t, true)
			f.answerNode(DeviceSessionGet, func(string, json.RawMessage) (any, error) { return nil, tc.err })
			s.addDispatched(context.Background(), dispatchTask{FromMachine: "mac", FromSession: "caller-0001", Machine: "box",
				Session: "s1", Cwd: "/code/app", At: time.Now().Add(-time.Hour)})
			out, err := s.dispatchRead(context.Background(), "mac", "caller-0001", json.RawMessage(`{"session_id":"s1"}`))
			if err != nil || !strings.Contains(out, ": "+tc.state+".") {
				t.Errorf("state response = %q, error = %v", out, err)
			}
			overview, err := s.dispatchRead(context.Background(), "mac", "caller-0001", json.RawMessage(`{}`))
			if err != nil || !strings.Contains(overview, "- s1 on box (/code/app): "+tc.state+"\n") {
				t.Errorf("overview = %q, error = %v", overview, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, call := range f.calls {
				if call.Method == DeviceSessionItems {
					t.Error("read transcript after a failed lookup")
				}
			}
		})
	}
}
