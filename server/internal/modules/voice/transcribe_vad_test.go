package voice

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/nwverse/persona/server/internal/modules/voice/voicetest"
)

// The speech the adapter hears is where the script's sentences are, to a frame or two, in recording samples, wherever
// the audio starts and however it is cut into frames.
func TestTranscriptionVADFindsTheSentences(t *testing.T) {
	pcm := voicetest.PCM(voicetest.Script{Sentences: trScript}, trScriptLength)
	var v trVAD
	at := int64(0)
	for off := 0; off < len(pcm); {
		n := min(3000+off%7*2, len(pcm)-off) // frames of uneven length
		v.add(at, pcm[off:off+n])
		at += int64(n / 2)
		off += n
	}
	spans := v.speech(0, at)
	if len(spans) != len(trScript) {
		t.Fatalf("spans %v", spans)
	}
	for i, s := range spans {
		l := trScript[i]
		if math.Abs(trSeconds(s[0])-l.Start) > 0.05 || math.Abs(trSeconds(s[1])-l.End) > 0.1 {
			t.Errorf("span %d %.2f-%.2f, said %.1f-%.1f", i, trSeconds(s[0]), trSeconds(s[1]), l.Start, l.End)
		}
	}
	if q := v.quiet(at); trSeconds(q) < 1.9 {
		t.Errorf("quiet %.2f", trSeconds(q))
	}
	if got := v.silenceAfter(trSamples(4e9), trSamples(5e8), at); math.Abs(trSeconds(got)-5.6) > 0.1 {
		t.Errorf("silence after 4 s at %.2f", trSeconds(got))
	}
	if got := v.speechIn(0, trSamples(6e9)); math.Abs(got-(2.5+1.8)) > 0.2 {
		t.Errorf("speech in the first 6 s: %.2f", got)
	}
	v.prune(trSamples(10e9))
	if len(v.speech(0, at)) != 3 {
		t.Errorf("pruned %v", v.speech(0, at))
	}
}

// Silence and a quiet room are no speech; a steady noise louder than a voice stops counting once the floor knows it;
// a jump in the audio ends the speech before it.
func TestTranscriptionVADFloorAndJumps(t *testing.T) {
	var v trVAD
	v.add(0, make([]byte, 2*trRate*3))
	if len(v.speech(0, 3*trRate)) != 0 || v.quiet(3*trRate) != 3*trRate {
		t.Fatalf("silence: %v", v.speech(0, 3*trRate))
	}
	noise := make([]byte, 2*trRate*10)
	for k := 0; k < len(noise)/2; k++ {
		binary.LittleEndian.PutUint16(noise[2*k:], uint16(int16(600*(1-2*(k%2)))))
	}
	var w trVAD
	w.add(0, noise)
	spans := w.speech(0, 10*trRate)
	if len(spans) != 1 || trSeconds(spans[0][1]) > 6.5 {
		t.Errorf("steady noise: %v", spans)
	}
	pcm := voicetest.PCM(voicetest.Script{Sentences: trScript[:1]}, 3)
	var x trVAD
	x.add(0, pcm[:2*trRate*2])        // half the sentence
	x.add(5*trRate, pcm[2*trRate*2:]) // the rest comes after a hole
	if s := x.speech(0, 6*trRate); len(s) != 2 || s[0][1] > 2*trRate {
		t.Errorf("jump: %v", s)
	}
}
