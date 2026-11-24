package volc

import (
	"math"
)

// Audio positions: a job's audio is kept by recording seconds; bytes are PCM16 mono 16 kHz.

func secToBytes(s float64) int64 { return int64(math.Round(s*sampleRate)) * 2 }

func bytesToSec(n int64) float64 { return float64(n/2) / sampleRate }

// ring is the job's recent audio: contiguous PCM from at, at most most bytes (older audio drops off the front).
type ring struct {
	at   float64 // recording second of buf[0]
	buf  []byte
	most int
}

func (r *ring) end() float64 { return r.at + bytesToSec(int64(len(r.buf))) }

// add appends pcm, which begins at r.end(), and drops what is past the limit (in steps of a quarter of it, so the
// buffer is not copied on every packet).
func (r *ring) add(pcm []byte) {
	r.buf = append(r.buf, pcm...)
	if len(r.buf) > r.most+r.most/4 {
		over := len(r.buf) - r.most
		over += over & 1
		r.at += bytesToSec(int64(over))
		r.buf = append(r.buf[:0:0], r.buf[over:]...)
	}
}

// reset empties the ring, which next holds audio from at (a hole).
func (r *ring) reset(at float64) {
	r.at, r.buf = at, r.buf[:0]
}

// slice is the audio from a to b (clipped to what the ring holds).
func (r *ring) slice(a, b float64) []byte {
	i, j := secToBytes(a-r.at), secToBytes(b-r.at)
	i = max(i, 0)
	j = min(j, int64(len(r.buf)))
	if i >= j {
		return nil
	}
	return r.buf[i:j]
}

// level is the root mean square of a 20 ms frame of PCM16.
func level(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
		sum += v * v
	}
	return math.Sqrt(sum / float64(n))
}

// quiet follows how loud the audio is, by 20 ms frames, against a noise floor it learns: how long the audio has been
// quiet up to now. Enough to cut windows at a pause; not a speech detector.
type quiet struct {
	floor float64
	run   float64 // seconds of quiet up to the last frame
	carry []byte
}

const (
	frameBytes = bytesPerSec / 50 // 20 ms
	quietMost  = 150.0            // a frame this soft is quiet whatever the floor
)

func (q *quiet) add(pcm []byte) {
	if len(q.carry) > 0 {
		pcm = append(q.carry, pcm...)
		q.carry = nil
	}
	for len(pcm) >= frameBytes {
		l := level(pcm[:frameBytes])
		pcm = pcm[frameBytes:]
		switch {
		case q.floor == 0 || l < q.floor:
			q.floor = max(l, 1)
		default:
			q.floor += (l - q.floor) * 0.002 // the floor rises slowly with a louder room
		}
		if l < max(quietMost, q.floor*2.5) {
			q.run += 0.02
		} else {
			q.run = 0
		}
	}
	if len(pcm) > 0 {
		q.carry = append([]byte(nil), pcm...)
	}
}
