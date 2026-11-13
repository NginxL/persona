package voice

import "time"

// Speech detection for transcription (docs/assistant/meeting-minutes.md "The built-in adapter"): the adapter's own
// energy-based detection cuts windows at silences and spreads sentences over the speech. The rule is the call's
// (gate.go: over voiceRMS and floorMargin times over the noise floor), on 20 ms frames and on the recording's clock,
// never the wall clock: the final pass runs at four times real time.

// trRate is the transcription audio's sample rate: PCM16 little-endian, mono, 16 kHz, from the manager and to the
// upstream. Positions are counted in samples of it.
const trRate = 16000

// trFrame is the frame speech is measured on: 20 ms.
const trFrame = trRate / 50

// trMergeGap: speech parted by less silence than this is one span (the gaps between words).
var trMergeGap = trSamples(300 * time.Millisecond)

// trSamples is d of audio in samples.
func trSamples(d time.Duration) int64 { return int64(d) * trRate / int64(time.Second) }

// trSeconds is n samples in seconds.
func trSeconds(n int64) float64 { return float64(n) / trRate }

// trVAD finds the speech in the audio it is given, in recording samples.
type trVAD struct {
	floor  noiseFloor
	part   []byte // the start of a frame not yet whole
	next   int64  // the sample after the last one taken
	any    bool
	spans  [][2]int64 // closed speech spans, in order
	open   bool
	from   int64 // the open span's first voiced sample
	last   int64 // the end of the open span's last voiced frame
	voiced int64 // the end of the last voiced frame, open span or not
}

// add takes pcm, whose first sample is at. A jump (audio that does not follow what came before) closes the speech
// open before it.
func (v *trVAD) add(at int64, pcm []byte) {
	if v.any && at != v.next {
		v.closeOpen()
		v.part = nil
	}
	v.any = true
	start := at - int64(len(v.part)/2)
	buf := append(v.part, pcm...)
	n := len(buf) / (2 * trFrame) * (2 * trFrame)
	for off := 0; off < n; off += 2 * trFrame {
		fs := start + int64(off/2)
		v.frame(fs, fs+trFrame, meanSquare(buf[off:off+2*trFrame]))
	}
	v.part = append([]byte(nil), buf[n:]...)
	v.next = at + int64(len(pcm)/2)
}

func (v *trVAD) frame(from, to int64, ms float64) {
	now := time.Unix(0, 0).Add(time.Duration(from) * time.Second / trRate)
	floor := v.floor.add(now, ms)
	voiced := ms > voiceRMS*voiceRMS && ms > floorMargin*floorMargin*floor
	switch {
	case voiced && v.open && from-v.last < trMergeGap:
		v.last = to
	case voiced:
		v.closeOpen()
		v.open, v.from, v.last = true, from, to
	case v.open && to-v.last >= trMergeGap:
		v.closeOpen()
	}
	if voiced {
		v.voiced = to
	}
}

func (v *trVAD) closeOpen() {
	if v.open {
		v.spans = append(v.spans, [2]int64{v.from, v.last})
		v.open = false
	}
}

// quiet is how long there has been no voice before edge, in samples (edge itself when there has been none).
func (v *trVAD) quiet(edge int64) int64 {
	if v.voiced == 0 && !v.open {
		return edge
	}
	return max(edge-v.voiced, 0)
}

// speech is the speech in [from, to), clipped to it, the open span up to its last voice included.
func (v *trVAD) speech(from, to int64) [][2]int64 {
	var out [][2]int64
	add := func(s [2]int64) {
		a, b := max(s[0], from), min(s[1], to)
		if b > a {
			out = append(out, [2]int64{a, b})
		}
	}
	for _, s := range v.spans {
		add(s)
	}
	if v.open {
		add([2]int64{v.from, v.last})
	}
	return out
}

// speechStart is where the speech going on at at began, or where the next speech before to begins when at is in a
// silence; at when there is none.
func (v *trVAD) speechStart(at, to int64) int64 {
	spans := v.spans
	if v.open {
		spans = append(spans[:len(spans):len(spans)], [2]int64{v.from, v.last})
	}
	for _, s := range spans {
		if s[1] <= at {
			continue
		}
		if s[0] < to {
			return s[0]
		}
		break
	}
	return at
}

// speechIn is the seconds of speech in [from, to).
func (v *trVAD) speechIn(from, to int64) float64 {
	var n int64
	for _, s := range v.speech(from, to) {
		n += s[1] - s[0]
	}
	return trSeconds(n)
}

// silenceAfter is the first moment at or after from that begins at least gap of silence within what was heard (up to
// edge), or -1 when there is none yet.
func (v *trVAD) silenceAfter(from, gap, edge int64) int64 {
	at := from
	for _, s := range v.speech(from, edge) {
		if s[0]-at >= gap {
			return at
		}
		at = max(at, s[1])
	}
	if edge-at >= gap {
		return at
	}
	return -1
}

// prune forgets the speech that ended before at.
func (v *trVAD) prune(at int64) {
	i := 0
	for i < len(v.spans) && v.spans[i][1] < at {
		i++
	}
	v.spans = v.spans[i:]
}
