package rpc

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// frames encodes lines the way a laneWriter cuts them: each line whole on lane, one after another, pieces of at most
// piece bytes, numbered from seq+1.
func frames(seq uint64, lane Lane, piece int, lines ...[]byte) ([]byte, uint64) {
	var out []byte
	for i, line := range lines {
		off := 0
		for {
			n := min(len(line)-off, piece)
			seq++
			var h [frameHeader]byte
			putHead(h[:], frameHead{kind: frameData, lane: lane, fin: off+n == len(line), slot: uint16(i % laneSlots), size: n, seq: seq})
			out = append(append(out, h[:]...), line[off:off+n]...)
			off += n
			if off == len(line) {
				break
			}
		}
	}
	return out, seq
}

func readLines(t *testing.T, r io.Reader) ([][]byte, error) {
	t.Helper()
	fr := newFrameReader(r, 64<<20)
	var got [][]byte
	for fr.Scan() {
		got = append(got, bytes.Clone(fr.Bytes()))
	}
	return got, fr.Err()
}

func TestFrameHeadRoundTrip(t *testing.T) {
	for _, h := range []frameHead{
		{kind: frameData, lane: LaneBulk, fin: true, slot: 15, size: framePayloadMax, seq: 1<<63 + 5},
		{kind: frameData, lane: LaneControl, size: 0, seq: 1},
		{kind: frameAck, size: ackPayload, seq: 42},
	} {
		var b [frameHeader]byte
		putHead(b[:], h)
		got, err := parseHead(b[:])
		if err != nil || got != h {
			t.Fatalf("%+v: got %+v, %v", h, got, err)
		}
	}
}

func TestFrameHeadRejectsWhatV1NeverWrites(t *testing.T) {
	good := func() []byte {
		var b [frameHeader]byte
		putHead(b[:], frameHead{kind: frameData, lane: LaneEvents, fin: true, size: 10, seq: 1})
		return b[:]
	}
	for name, mutate := range map[string]func(b []byte){
		"kind":          func(b []byte) { b[0] = '{' }, // a JSON line read as a frame
		"lane":          func(b []byte) { b[1] = byte(laneCount) },
		"flags":         func(b []byte) { b[2] = 2 },
		"reserved":      func(b []byte) { b[3] = 1 },
		"too long":      func(b []byte) { b[6], b[7] = 0x7f, 0xf1 }, // framePayloadMax+1
		"ack with lane": func(b []byte) { b[0], b[1] = frameAck, 1 },
		"ack with size": func(b []byte) { b[0], b[6], b[7], b[2] = frameAck, 0, 1, 0 },
	} {
		b := good()
		mutate(b)
		if _, err := parseHead(b); !errors.Is(err, ErrFrame) {
			t.Errorf("%s: %v, want ErrFrame", name, err)
		}
	}
}

// Lines at and around a frame's edge come back exactly, however the transport splits the reads.
func TestFrameReaderEdges(t *testing.T) {
	sizes := []int{0, 1, framePayloadMax - 1, framePayloadMax, framePayloadMax + 1, frameMax, 2 * framePayloadMax, 3<<20 + 7}
	var lines [][]byte
	for i, n := range sizes {
		lines = append(lines, bytes.Repeat([]byte{byte('a' + i)}, n))
	}
	wire, _ := frames(0, LaneInteractive, framePayloadMax, lines...)
	for name, r := range map[string]io.Reader{
		"whole":    bytes.NewReader(wire),
		"one byte": iotest.OneByteReader(bytes.NewReader(wire)),
		"half":     iotest.HalfReader(bytes.NewReader(wire)),
	} {
		got, err := readLines(t, r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != len(lines) {
			t.Fatalf("%s: %d lines, want %d", name, len(got), len(lines))
		}
		for i := range lines {
			if !bytes.Equal(got[i], lines[i]) {
				t.Fatalf("%s: line %d (%d bytes) came back as %d bytes", name, i, len(lines[i]), len(got[i]))
			}
		}
	}
	// A line of exactly framePayloadMax is one frame, one more byte two; 32 KiB with the header is two.
	for _, c := range []struct{ size, frames int }{{framePayloadMax, 1}, {framePayloadMax + 1, 2}, {frameMax, 2}, {0, 1}} {
		wire, seq := frames(0, LaneBulk, framePayloadMax, make([]byte, c.size))
		if int(seq) != c.frames || len(wire) != c.size+c.frames*frameHeader {
			t.Errorf("a line of %d: %d frames, %d bytes", c.size, seq, len(wire))
		}
	}
}

// Pieces of lines on different lanes and slots interleave; each comes back whole, in the order it was finished.
func TestFrameReaderInterleaved(t *testing.T) {
	big := bytes.Repeat([]byte("B"), 3*framePayloadMax+10)
	small := []byte(`{"id":"n1","result":{}}`)
	var wire []byte
	seq := uint64(0)
	add := func(lane Lane, slot uint16, fin bool, p []byte) {
		seq++
		var h [frameHeader]byte
		putHead(h[:], frameHead{kind: frameData, lane: lane, fin: fin, slot: slot, size: len(p), seq: seq})
		wire = append(append(wire, h[:]...), p...)
	}
	add(LaneBulk, 0, false, big[:framePayloadMax])
	add(LaneControl, 0, true, small)
	add(LaneBulk, 0, false, big[framePayloadMax:2*framePayloadMax])
	add(LaneInteractive, 3, false, []byte(`{"id":"n2",`))
	add(LaneBulk, 0, false, big[2*framePayloadMax:3*framePayloadMax])
	add(LaneInteractive, 3, true, []byte(`"result":1}`))
	add(LaneBulk, 0, true, big[3*framePayloadMax:])
	got, err := readLines(t, bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{small, []byte(`{"id":"n2","result":1}`), big}
	if len(got) != len(want) {
		t.Fatalf("%d lines", len(got))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("line %d: %.40q", i, got[i])
		}
	}
}

func TestFrameReaderBrokenInput(t *testing.T) {
	line := bytes.Repeat([]byte("x"), 100)
	wire, _ := frames(0, LaneEvents, framePayloadMax, line)
	t.Run("half a frame", func(t *testing.T) {
		if _, err := readLines(t, bytes.NewReader(wire[:len(wire)-1])); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("%v", err)
		}
		if _, err := readLines(t, bytes.NewReader(wire[:frameHeader-3])); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("half a header: %v", err)
		}
	})
	t.Run("a line cut off on a frame's edge ends quietly", func(t *testing.T) {
		wire, _ := frames(0, LaneEvents, 10, line)
		got, err := readLines(t, bytes.NewReader(wire[:2*(frameHeader+10)]))
		if err != nil || len(got) != 0 {
			t.Fatalf("%d lines, %v", len(got), err)
		}
	})
	t.Run("sequence gap", func(t *testing.T) {
		a, seq := frames(0, LaneEvents, framePayloadMax, line)
		b, _ := frames(seq+1, LaneEvents, framePayloadMax, line)
		if _, err := readLines(t, bytes.NewReader(append(a, b...))); !errors.Is(err, ErrFrame) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a JSON line where a frame belongs", func(t *testing.T) {
		if _, err := readLines(t, strings.NewReader("{\"event\":\"x\",\"params\":{}}\n")); !errors.Is(err, ErrFrame) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("too long", func(t *testing.T) {
		wire, _ := frames(0, LaneBulk, 1000, make([]byte, 5000))
		fr := newFrameReader(bytes.NewReader(wire), 4096)
		for fr.Scan() {
		}
		if !errors.Is(fr.Err(), bufio.ErrTooLong) {
			t.Fatalf("%v", fr.Err())
		}
	})
	t.Run("too many lines at once", func(t *testing.T) {
		var wire []byte
		for i := 0; i <= maxPartialLines; i++ {
			var h [frameHeader]byte
			putHead(h[:], frameHead{kind: frameData, lane: Lane(i % int(laneCount)), slot: uint16(i), size: 1, seq: uint64(i + 1)})
			wire = append(append(wire, h[:]...), 'x')
		}
		if _, err := readLines(t, bytes.NewReader(wire)); !errors.Is(err, ErrFrame) {
			t.Fatalf("%v", err)
		}
	})
}

// The reader hands acknowledgements to the writer, and asks for one of the bulk frames it read before it waits for
// more input; frames of the other lanes ask for none.
func TestFrameReaderAcks(t *testing.T) {
	var acked []uint64
	var asked []uint64
	wire, seq := frames(0, LaneInteractive, framePayloadMax, []byte("a"))
	more, seq2 := frames(seq, LaneBulk, 10, make([]byte, 25))
	wire = append(wire, more...)
	var h [frameHeader + ackPayload]byte
	putHead(h[:], frameHead{kind: frameAck, size: ackPayload, seq: 7})
	binary.BigEndian.PutUint32(h[frameHeader:], 1234)
	wire = append(wire, h[:]...)
	fr := newFrameReader(bytes.NewReader(wire), 1<<20)
	fr.ack = func(seq uint64, _ uint32) { asked = append(asked, seq) }
	var ackedAt uint32
	fr.acked = func(seq uint64, at uint32) error { acked, ackedAt = append(acked, seq), at; return nil }
	n := 0
	for fr.Scan() {
		n++
	}
	if n != 2 || fr.Err() != nil {
		t.Fatalf("%d lines, %v", n, fr.Err())
	}
	if len(acked) != 1 || acked[0] != 7 || ackedAt != 1234 {
		t.Fatalf("acknowledgements read: %v at %d", acked, ackedAt)
	}
	if len(asked) != 1 || asked[0] != seq2 {
		t.Fatalf("acknowledgements asked for: %v, want [%d]", asked, seq2)
	}
}
