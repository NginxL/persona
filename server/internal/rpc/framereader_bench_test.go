package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"testing"
)

// smallEvents is n short events as lines and as frames on the events lane: what a busy session's activity looks like.
func smallEvents(n int) (lines, frames []byte) {
	for i := range n {
		p := []byte(fmt.Sprintf(`{"event":"session.activity","params":{"n":%d,"s":"abcdefghijklmnopqrstuvwxyz0123456789"}}`, i))
		lines = append(append(lines, p...), '\n')
		var h [frameHeader]byte
		putHead(h[:], frameHead{kind: frameData, lane: LaneEvents, fin: true, size: len(p), seq: uint64(i + 1)})
		frames = append(append(frames, h[:]...), p...)
	}
	return lines, frames
}

// readsOf hands out at most n bytes a read, as a socket does.
type readsOf struct {
	r io.Reader
	n int
}

func (c readsOf) Read(b []byte) (int, error) { return c.r.Read(b[:min(len(b), c.n)]) }

// BenchmarkReadSmallEvents reads a stream of small events as the read loop does (decode each, look at the read-ahead
// for each), as lines and as frames, in reads of a few sizes. Framed must not cost more than lines: looking ahead
// again for every line once made a framed connection read 0.45 MB/s where lines read 2.8 (#10660's review).
func BenchmarkReadSmallEvents(b *testing.B) {
	lines, frames := smallEvents(20000)
	for _, read := range []int{4 << 10, 64 << 10} {
		for _, framed := range []bool{false, true} {
			name, wire := "lines", lines
			if framed {
				name, wire = "framed", frames
			}
			b.Run(fmt.Sprintf("%s/read=%dKiB", name, read>>10), func(b *testing.B) {
				b.SetBytes(int64(len(wire)))
				for b.Loop() {
					var sc lineSource
					if framed {
						fr := newFrameReader(readsOf{bytes.NewReader(wire), read}, 64<<20)
						fr.middle = func() {}
						sc = fr
					} else {
						lr := newLineReader(readsOf{bytes.NewReader(wire), read}, 64<<20)
						lr.middle = func() {}
						sc = lr
					}
					for sc.Scan() {
						var m message
						_ = json.Unmarshal(sc.Bytes(), &m)
						_ = sc.aheadActivity()
					}
				}
			})
		}
	}
}
