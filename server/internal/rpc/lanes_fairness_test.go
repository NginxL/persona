package rpc

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestBulkGetsATurnDuringSustainedTraffic(t *testing.T) {
	for _, lane := range []Lane{LaneEvents, LaneInteractive} {
		t.Run(lane.String(), func(t *testing.T) {
			lw := newLaneWriter(io.Discard, make(chan struct{}), nil, nil)
			line := queue(t, lw, lane, strings.Repeat("a", 20*framePayloadMax))
			line.credit = false // an event or interactive answer
			queue(t, lw, LaneBulk, "bulk")
			now := time.Now()
			for count := 0; count <= 8; count++ {
				if count == 8 {
					queue(t, lw, LaneControl, "ping")
				}
				batch, _ := lw.fillLocked(nil, nil, now)
				frames, _ := heads(t, batch)
				if count == 8 && (len(frames) == 0 || frames[0].lane != LaneControl) {
					t.Fatal("control no longer has first choice")
				}
				for _, frame := range frames {
					if frame.lane == LaneBulk {
						return
					}
				}
			}
			t.Fatal("bulk got no frame after eight higher-priority frames and a control ping")
		})
	}
}

func TestFramedPingCountsOnlyProgressThroughEarlierTraffic(t *testing.T) {
	for _, tc := range []struct {
		name           string
		before, during uint64
		wantDelayed    bool
	}{
		{"draining traffic ahead", 0, 2, true},
		{"no acknowledgement", 0, 0, false},
		{"repeated acknowledgement", 1, 1, false},
		{"peer already read ping", 0, 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := New("h", io.Discard)
				p.EnableLanes(testControl, testBulk)
				lw := newLaneWriter(io.Discard, p.closed, p.Close, nil)
				p.out.Store(lw)
				lw.start()
				defer p.Close(ErrClosed)
				// Four uncredited frames reach the transport, which may buffer them ahead of the ping.
				if err := p.Notify("session.changed", strings.Repeat("x", 3*framePayloadMax)); err != nil {
					t.Fatal(err)
				}
				ack := func(seq uint64) {
					if err := lw.acked(seq, clockMS(time.Now())); err != nil {
						t.Error(err)
					}
				}
				if tc.before != 0 {
					ack(tc.before)
				}
				if tc.during != 0 {
					time.AfterFunc(4*time.Second, func() { ack(tc.during) })
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				err := p.Call(ctx, "ping", nil, nil)
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("ping = %v", err)
				}
				remaining, delayed := WriteDelayRemaining(err)
				if delayed != tc.wantDelayed || delayed && remaining != time.Second {
					t.Fatalf("remaining = %s, delayed = %v, want delayed = %v", remaining, delayed, tc.wantDelayed)
				}
			})
		})
	}
}
