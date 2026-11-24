package voice

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/nwverse/persona/server/internal/extwire"
)

// The fake dialect's transcription (manager.json only, for local end-to-end runs without a GPT-Live-compatible
// service): no upstream at all. With PERSONA_VOICE_FAKE_TRANSCRIPT (a JSON file, or the JSON itself, of
// [{"start", "end", "text"}] in recording seconds) each of its lines is a sentence once the audio passed its end, with
// a caption growing as the audio goes through it; without, every stretch of speech the adapter hears is one line of a
// made-up bilingual meeting, in turn.

// fakeTranscriptEnv names the fake dialect's script.
const fakeTranscriptEnv = "PERSONA_VOICE_FAKE_TRANSCRIPT"

// fakeMeeting are the fake dialect's lines without a script: Chinese, English and both in one sentence, as said.
var fakeMeeting = []string{
	"我们这周先把 export cap 保持在 4 MB。", // i18n-ignore: a made-up meeting line (data), mixed as spoken
	"Let's revisit pricing next week.",
	"下周三之前 Alice 要把 pricing 的 proposal 发出来。", // i18n-ignore: a made-up meeting line (data)
	"Bo owns the vendor follow-up.",
	"这个 release 先只发 iOS，Android 下个 sprint 再跟。", // i18n-ignore: a made-up meeting line (data)
	"Can we get the dashboard numbers before Friday?",
}

type trFakeLine struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// trFake is one job's fake transcription.
type trFake struct {
	lines     []trFakeLine // the script, by end; nil: fakeMeeting, one per stretch of speech
	sent      int          // the script's lines sent
	next      int          // fakeMeeting's next line
	interimAt int64
}

func loadTrFake(logf func(string, ...any)) *trFake {
	raw := strings.TrimSpace(os.Getenv(fakeTranscriptEnv))
	f := &trFake{}
	if raw == "" {
		return f
	}
	b := []byte(raw)
	if !strings.HasPrefix(raw, "[") {
		var err error
		if b, err = os.ReadFile(expandHome(raw)); err != nil {
			logf("fake transcript: %s cannot be read (%v): the built-in lines instead", fakeTranscriptEnv, err)
			return f
		}
	}
	if err := json.Unmarshal(b, &f.lines); err != nil {
		logf("fake transcript: %s is not [{start, end, text}] (%v): the built-in lines instead", fakeTranscriptEnv, err)
		f.lines = nil
		return f
	}
	sort.SliceStable(f.lines, func(a, b int) bool { return f.lines[a].End < f.lines[b].End })
	logf("fake transcript: %d lines from %s", len(f.lines), fakeTranscriptEnv)
	return f
}

// sentences are the fake's sentences of [from, to): the script's lines that end in it (and any before it not sent
// yet), or a built-in line for each stretch of speech in it.
func (f *trFake) sentences(from, to int64, speech [][2]int64) []trSentence {
	var out []trSentence
	if f.lines != nil {
		for f.sent < len(f.lines) && f.lines[f.sent].End <= trSeconds(to) {
			l := f.lines[f.sent]
			f.sent++
			if trHasText(l.Text) {
				out = append(out, trSentence{start: l.Start, end: l.End, text: strings.TrimSpace(l.Text)})
			}
		}
		return out
	}
	for _, s := range speech {
		out = append(out, trSentence{start: trSeconds(s[0]), end: trSeconds(s[1]), text: fakeMeeting[f.next%len(fakeMeeting)]})
		f.next++
	}
	return out
}

// fakeInterim sends the caption being said, every 200 ms of audio: the script's line the audio is in, or the next
// built-in line while there is speech, as far as it has been said.
func (j *trJob) fakeInterim() {
	f := j.fake
	if j.edge-f.interimAt < trRate/5 {
		return
	}
	f.interimAt = j.edge
	now := trSeconds(j.edge)
	if f.lines != nil {
		for _, l := range f.lines[f.sent:] {
			if l.Start <= now && now < l.End {
				rs := []rune(l.Text)
				n := int(math.Ceil(float64(len(rs)) * (now - l.Start) / max(l.End-l.Start, 0.1)))
				j.send(extwire.TranscriptionFrame{Type: extwire.TFInterim, Start: extwire.F(l.Start), Text: extwire.F(string(rs[:min(n, len(rs))]))})
				return
			}
		}
		return
	}
	if !j.vad.open {
		return
	}
	rs := []rune(fakeMeeting[f.next%len(fakeMeeting)])
	n := int(math.Ceil(trSeconds(j.edge-j.vad.from) * 5))
	j.send(extwire.TranscriptionFrame{Type: extwire.TFInterim, Start: secs(j.vad.from), Text: extwire.F(string(rs[:min(n, len(rs))]))})
}
