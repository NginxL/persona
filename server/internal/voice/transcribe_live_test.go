package voice

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// trCaptionJob is a live job with one open window and its caption session, to give caption frames to caption() as
// the session's reader does. Nothing reaches a manager: the job has not sent ready.
func trCaptionJob() (*trJob, *trSession) {
	j := &trJob{windows: []*trWin{{n: 0, from: 0, to: -1}}, nextWin: 1}
	sl := &trSlot{kind: trCaption}
	s := &trSession{j: j, kind: trCaption, slot: sl, last: -1, passed: map[int]time.Time{}}
	sl.cur = s
	j.slots = []*trSlot{sl}
	return j, s
}

// trAt is recording second at in samples.
func trAt(at float64) int64 { return int64(math.Round(at * trRate)) }

// trFrameAt is one caption frame and where the session's audio was when it came, in recording seconds.
type trFrameAt struct {
	at    float64
	item  string
	text  string
	final bool
}

// feedCaptions gives frames to caption session s of j in order and returns its captions' texts.
func feedCaptions(j *trJob, s *trSession, frames []trFrameAt) []string {
	for _, f := range frames {
		j.caption(s, tevent{kind: tevCaption, item: f.item, text: f.text, final: f.final}, trAt(f.at), trAt(f.at))
	}
	var out []string
	for _, it := range s.items {
		out = append(out, it.text)
	}
	return out
}

// The measured caption session's frames (one item, never final): the service revised a caption's first words at the
// moment it began ("好人", then "how 人" at once), and the next sentence's text replaced the last one's. The revised
// hypothesis is part of its caption, not a caption of its own: the window's captions are what was said, once.
func TestCaptionRevisedAsMeasured(t *testing.T) {
	const item = "029f54e8"
	frames := []trFrameAt{
		{2.50, item, "好人", false},
		{2.50, item, "how 人", false},
		{2.66, item, "how 人都", false},
		{5.37, item, "how 人都到齐了我们开始今天的发布评审", false},
		{9.60, item, "先说 ios 这边离线编辑基本做完了", false},
		{14.09, item, "the back end merge a", false},
		{17.49, item, "the back end merge a p an now returns the conflicting paragraphs so the client can", false},
		{21.18, item, "help 这词还是", false},
		{59.37, item, "andrea 那边", false},
		{61.89, item, "android 那边的 built 昨天挂了是 graded cache 的", false},
		{65.46, item, "i already filed a fix it should land before", false},
	}
	want := []string{
		"how 人都到齐了我们开始今天的发布评审",
		"先说 ios 这边离线编辑基本做完了",
		"the back end merge a p an now returns the conflicting paragraphs so the client can",
		"help 这词还是",
		"android 那边的 built 昨天挂了是 graded cache 的",
		"i already filed a fix it should land before",
	}
	j, s := trCaptionJob()
	if got := feedCaptions(j, s, frames); !reflect.DeepEqual(got, want) {
		t.Fatalf("captions:\n%q\nwant:\n%q", got, want)
	}
	if got := j.captionText(s, j.windows[0]); got != strings.Join(want, "") {
		t.Errorf("the window's captions %q", got)
	}
	// The revised caption keeps the start its first frame gave it.
	if s.items[0].start != trAt(2.50-trCaptionLag.Seconds()) {
		t.Errorf("the first caption starts at %.2f s", trSeconds(s.items[0].start))
	}
}

// A final frame of the caption's item replaces its interim text, however unlike it is: a service that marks a
// sentence's last caption final may write it in another form ("twenty five", then the final "25."). The final text
// is the window's captions, and sessions that wrote it are trusted against them.
func TestCaptionFinalRevises(t *testing.T) {
	for _, tt := range []struct {
		name   string
		frames []trFrameAt
		want   []string
	}{
		{"its own item", []trFrameAt{{1.0, "it-1", "twenty", false}, {1.4, "it-1", "twenty five", false}, {3.0, "it-1", "25.", true},
			{5.0, "it-2", "Bo owns", false}, {6.0, "it-2", "Bo owns it.", true}}, []string{"25.", "Bo owns it."}},
		{"one item", []trFrameAt{{1.0, "it", "twenty", false}, {1.4, "it", "twenty five", false}, {3.0, "it", "25.", true},
			{5.0, "it", "Bo owns", false}, {6.0, "it", "Bo owns it.", true}}, []string{"25.", "Bo owns it."}},
		{"withdrawn", []trFrameAt{{1.0, "it-1", "twenty five", false}, {3.0, "it-1", "", true}}, []string{""}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			j, s := trCaptionJob()
			if got := feedCaptions(j, s, tt.frames); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("captions %q, want %q", got, tt.want)
			}
			caption := j.captionText(s, j.windows[0])
			if caption != strings.Join(tt.want, "") {
				t.Errorf("the window's captions %q", caption)
			}
			if !trHasText(caption) {
				return
			}
			if pick, dropped := pickLive(caption, []string{caption, caption}); pick != 0 || dropped[0] || dropped[1] {
				t.Errorf("sessions that wrote the final text: %d %v", pick, dropped)
			}
		})
	}
}

// trAssignStep is a reply that begins when the session's audio is at second at, while the job's audio (and its speech
// detection) is ahead seconds further; window is the window it is of.
type trAssignStep struct {
	at, ahead float64
	window    int
}

// assignReplies has a verbatim session begin a reply at each step, in order, in a live job whose speech is spans
// (recording seconds) and whose first window closes at cut: at each step the job has heard what was said up to its
// audio's end (a span going on then is open, up to there), and the windows are as far as that.
func assignReplies(t *testing.T, spans [][2]float64, cut float64, steps []trAssignStep) {
	t.Helper()
	j := &trJob{}
	s := &trSession{j: j, kind: trVerbatim, slot: &trSlot{kind: trVerbatim}, last: -1}
	for _, st := range steps {
		edge := trAt(st.at + st.ahead)
		j.edge, j.vad = edge, trVAD{}
		for _, sp := range spans {
			from, to := trAt(sp[0]), trAt(sp[1])
			switch {
			case from >= edge:
			case to+trMergeGap <= edge:
				j.vad.spans = append(j.vad.spans, [2]int64{from, to})
			default:
				j.vad.open, j.vad.from, j.vad.last = true, from, min(to, edge)
			}
		}
		j.windows, j.nextWin = []*trWin{{n: 0, from: 0, to: -1}}, 1
		if edge >= trAt(cut) {
			j.windows[0].to = trAt(cut)
			j.windows, j.nextWin = append(j.windows, &trWin{n: 1, from: trAt(cut), to: -1}), 2
		}
		if got := j.assign(s, trAt(st.at)); got != st.window {
			t.Errorf("a reply at %.2f s (the job %.1f s ahead): window %d, want %d", st.at, st.ahead, got, st.window)
		}
	}
}

// A reply is of the speech that ended before it began, however soon after: the measured service replied 0.05-0.3 s
// after each sentence, and a reply that claimed the sentence before its own fell a sentence behind and, across a
// window's end, landed in the window before. Replies 1.5-3 s after their sentence (the next one under way) are still
// of their own; so is one whose sentence the job's speech detection, ahead of the session, heard run on a moment.
func TestTrAssignReplies(t *testing.T) {
	spans := [][2]float64{{1, 3}, {8, 14.8}, {16, 18}}
	for _, tt := range []struct {
		name  string
		steps []trAssignStep
	}{
		{"0.1 s after", []trAssignStep{{3.1, 0, 0}, {14.9, 0, 0}, {18.1, 0, 1}}},
		{"0.05 s after", []trAssignStep{{3.05, 0, 0}, {14.85, 0, 0}, {18.05, 0, 1}}},
		{"0.3 s after", []trAssignStep{{3.3, 0, 0}, {15.1, 0, 0}, {18.3, 0, 1}}},
		{"1.5 to 3 s after", []trAssignStep{{5.0, 0, 0}, {16.3, 0, 0}, {20.5, 0, 1}}},
		{"3 s after, the next sentence under way", []trAssignStep{{6.0, 0, 0}, {17.8, 0, 0}, {21.0, 0, 1}}},
		{"the detection ahead", []trAssignStep{{3.1, 0.1, 0}, {14.9, 0.1, 0}, {17.9, 0.2, 1}}},
	} {
		t.Run(tt.name, func(t *testing.T) { assignReplies(t, spans, 15.5, tt.steps) })
	}
}
