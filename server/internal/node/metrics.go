package node

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sysmon"
)

// The machine pages (docs/server/machines.md): what this machine is goes out once with node.info, how busy it is as a
// "metrics" notification every proto.MetricEvery. The hub keeps the history; the node keeps nothing.

func (n *Node) system(ctx context.Context) *proto.System {
	n.sysOnce.Do(func() { n.sys = sysmon.ReadSystem(ctx) })
	s := n.sys
	return &s
}

func (n *Node) sampleMetrics(ctx context.Context) {
	c := sysmon.NewCollector(ctx)
	defer c.Close()
	// On the tick's boundary, so samples of all machines line up (and a 10-second bucket gets exactly one).
	every := proto.MetricEvery
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Until(time.Now().Truncate(every).Add(every))):
	}
	t := time.NewTicker(every)
	defer t.Stop()
	var took time.Duration
	for i := 1; ; i++ {
		start := time.Now()
		s := c.Sample(start.Round(time.Second))
		if procs, ok := claudeProcessCount(); ok { // nil off Linux: the hub then skips its process cap, never reads zero
			s.ClaudeProcs = &procs
		}
		took += time.Since(start)
		if i == 30 { // the cost, once per node start (docs/server/machines.md "Overhead")
			n.logger.Printf("metrics: a sample takes %v on average", (took / 30).Round(10*time.Microsecond))
		}
		if n.peer != nil {
			_ = n.peer.Notify("metrics", map[string]any{"sample": s})
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
