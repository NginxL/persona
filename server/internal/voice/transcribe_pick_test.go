package voice

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/voice/voicetest"
)

func TestNormDistance(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want float64
	}{
		{"", "", 0},
		{"", "abc", 1},
		{"abcd", "abed", 0.25},
		{"Ｈｅｌｌｏ，世界！", "hello 世界", 0},
		{"我们先把 export cap 保持在 4 MB。", "我们先把export cap保持在4MB", 0},
		{"Bo owns the vendor follow-up.", "bo owns the vendor followup", 0},
		{"我们先把 export cap 保持在 4 MB。", "我们先把出口上限保持在四兆。", 0.63},
	} {
		if got := normDistance(tt.a, tt.b); math.Abs(got-tt.want) > 0.01 {
			t.Errorf("%q %q: %.3f, want %.2f", tt.a, tt.b, got, tt.want)
		}
	}
	// What a rogue session writes is far from what was said, and two rogue sessions do not agree.
	for i, r := range voicetest.InventedReplies {
		if d := normDistance(r, "Bo owns the vendor follow-up."); d <= trDropOver {
			t.Errorf("invented reply %d is %.2f from a sentence", i, d)
		}
		for k, o := range voicetest.InventedReplies[:i] {
			if d := normDistance(r, o); d <= trDropOver {
				t.Errorf("invented replies %d and %d are %.2f apart", i, k, d)
			}
		}
	}
}

func TestMedoid(t *testing.T) {
	texts := []string{"abcdefghij", "abcdefghix", "zzzzzzzzzz", "abcdefghiy"}
	if got := medoid(texts, []int{0, 1, 2, 3}, nil); got != 0 {
		t.Errorf("medoid %d", got)
	}
	// Of two, the one closer to the anchor.
	anchor := "abcdefghiy"
	if got := medoid(texts, []int{1, 3}, &anchor); got != 3 {
		t.Errorf("of two %d", got)
	}
	if got := medoid(texts, []int{1, 3}, nil); got != 1 {
		t.Errorf("of two without an anchor %d", got)
	}
	// Of two that agree, the longer: the other missed what it has more.
	longer := []string{"abcdefghij", "abcdefghijkl"}
	if a, b := medoid(longer, []int{0, 1}, nil), medoid(longer, []int{1, 0}, nil); a != 1 || b != 1 {
		t.Errorf("of two the longer: %d %d", a, b)
	}
}

func TestPickLive(t *testing.T) {
	said := "我们这周先把 export cap 保持在 4 MB。"
	caption := "我们这周先把 export 开 保持在 4 MB"
	rogue := voicetest.InventedReplies[0]
	for _, tt := range []struct {
		name    string
		caption string
		texts   []string
		pick    int
		dropped []bool
	}{
		{"one rogue", caption, []string{rogue, said}, 1, []bool{true, false}},
		{"healthy pair", caption, []string{said, said + "嗯"}, 0, []bool{false, false}},
		{"none near the captions", caption, []string{rogue, voicetest.InventedReplies[1]}, -1, []bool{true, true}},
		{"no captions, three", "", []string{said, rogue, said}, 0, []bool{false, true, false}},
		{"no captions, two apart", "", []string{said, rogue}, -1, []bool{false, false}},
		{"no captions, alone", "", []string{said}, 0, []bool{false}},
		{"no captions, none agree", "", []string{said, rogue, voicetest.InventedReplies[1]}, -1, []bool{true, true, true}},
		{"nothing", caption, nil, -1, []bool{}},
	} {
		pick, dropped := pickLive(tt.caption, tt.texts)
		if pick != tt.pick || !reflect.DeepEqual(dropped, tt.dropped) {
			t.Errorf("%s: %d %v, want %d %v", tt.name, pick, dropped, tt.pick, tt.dropped)
		}
	}
}

func TestPickReplay(t *testing.T) {
	said := "下周三之前 Alice 要把 pricing 的 proposal 发出来。"
	near := "下周三之前 Alice 要把 pricing 的 proposal 发出"
	rogue := voicetest.InventedReplies[0]
	ref := "下周三之前 alice 要把 pricing 的 proposol 发出来。"
	// The live check's opening window: both sessions heard it, the reference was a live caption of a few words.
	opening := "好，人都到齐了，我们开始今天的发布评审。先说 iOS 这边，离线编辑基本做完了，现在在修 sync conflict 的问题。" +
		"The backend merge API now returns the conflicting paragraphs, so the client can show them to the user."
	caption := "Board cap 这词还是保持在 4 兆吗？"
	other := "Let's keep the landing page on the web, so it does not need another app review."
	// A session that heard a window's 14 s of speech (45 letters and digits), one that missed its end (its first 40,
	// 0.11 from it), and a reference of its first 20: the partial session is near it (0.50), the complete one not
	// (0.56).
	complete := "Alice owns the rollout plan and the release notes 2026"
	partial, short := "Alice owns the rollout plan and the release note", "Alice owns the rollout p"
	for _, tt := range []struct {
		name    string
		ref     *string
		texts   []string
		stopped []bool
		speech  float64
		pick    int
		kept    bool
		use     trRefUse
	}{
		{"with a reference", &ref, []string{rogue, said}, []bool{false, false}, 3, 1, false, trRefChecked},
		{"of two, the closer to the reference", &ref, []string{near, said}, []bool{false, false}, 3, 1, false, trRefChecked},
		{"none near: the reference stays", &ref, []string{rogue, voicetest.InventedReplies[1]}, []bool{false, false}, 3, -1, true, trRefChecked},
		{"stopped early", &ref, []string{said, near}, []bool{true, false}, 3, 1, false, trRefChecked},
		{"without: two of three", nil, []string{said, rogue, near}, []bool{false, false, false}, 3, 0, false, trRefNone},
		{"without: none agree", nil, []string{said, rogue, voicetest.InventedReplies[1]}, []bool{false, false, false}, 3, -1, true, trRefNone},
		{"without: one left", nil, []string{said, near, rogue}, []bool{true, false, false}, 3, -1, true, trRefNone},
		{"an empty reference is none", new(""), []string{said, rogue, near}, []bool{false, false, false}, 3, 0, false, trRefNone},
		{"a reference far shorter than the sessions", &caption, []string{opening, opening}, []bool{false, false}, 14, 0, false, trRefShort},
		{"a short reference, sessions apart: kept", &caption, []string{opening, rogue}, []bool{false, false}, 14, -1, true, trRefShort},
		{"a short reference, one session left: kept", &caption, []string{opening, ""}, []bool{false, true}, 14, -1, true, trRefShort},
		{"under two characters a second of speech", new("export cap 保持在四兆"), []string{said, said}, []bool{false, false}, 12, 0, false, trRefShort},
		{"short, but as long as the sessions", new("好的。"), []string{"好的。", "好的"}, []bool{false, false}, 2, 0, false, trRefChecked},
		{"sessions agree, the reference another sentence", &other, []string{opening[:150], opening[:150]}, []bool{false, false}, 8, 0, false, trRefOutvoted},
		{"two rogues far from it and each other", &ref, []string{rogue, voicetest.InventedReplies[2]}, []bool{false, false}, 3, -1, true, trRefChecked},
		{"a rogue that writes much does not make it short", &ref, []string{rogue + " " + rogue, said}, []bool{false, false}, 3, 1, false, trRefChecked},
		{"sessions that wrote nothing do not outvote it", &ref, []string{"", ""}, []bool{false, false}, 3, -1, true, trRefChecked},
		// Too short against the sessions that wrote text: one that wrote nothing does not make it long enough.
		{"a short reference, a silent session", &caption, []string{opening, ""}, []bool{false, false}, 14, -1, true, trRefShort},
		// A too-short reference keeps out no session: the one near it is no better than the one that heard more.
		{"a short reference, the partial session near it", &short, []string{complete, partial}, []bool{false, false}, 14, 0, false, trRefShort},
		{"a short reference, the partial session first", &short, []string{partial, complete}, []bool{false, false}, 14, 1, false, trRefShort},
	} {
		pick, kept, use := pickReplay(tt.ref, false, tt.texts, tt.stopped, tt.speech)
		if pick != tt.pick || kept != tt.kept || use != tt.use {
			t.Errorf("%s: %d %v %d, want %d %v %d", tt.name, pick, kept, use, tt.pick, tt.kept, tt.use)
		}
	}
	// A reference set aside already (the window got a third session for it): chosen as without one, among the
	// sessions that wrote text; one alone is never taken, and two that wrote nothing do not empty the window.
	for _, tt := range []struct {
		name    string
		texts   []string
		stopped []bool
		pick    int
		kept    bool
	}{
		{"set aside: the third session agrees", []string{opening, "", opening}, []bool{false, true, false}, 0, false},
		{"set aside: the third agrees with neither", []string{opening, "", rogue}, []bool{false, true, false}, -1, true},
		{"set aside: two that wrote nothing", []string{opening, "", ""}, []bool{false, false, false}, -1, true},
	} {
		pick, kept, use := pickReplay(&caption, true, tt.texts, tt.stopped, 14)
		if pick != tt.pick || kept != tt.kept || use != trRefShort {
			t.Errorf("%s: %d %v %d, want %d %v %d", tt.name, pick, kept, use, tt.pick, tt.kept, trRefShort)
		}
	}
}

func TestSplitSentences(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want []string
	}{
		{"我们先这样。Then we ship it.", []string{"我们先这样。", "Then we ship it."}},
		{"The cap is 4.5 MB. OK", []string{"The cap is 4.5 MB.", "OK"}},
		{"他说：“好的。”然后走了", []string{"他说：“好的。”", "然后走了"}},
		{"Really?! Yes", []string{"Really?!", "Yes"}},
		{"  下周三之前 Alice 要把 pricing 的 proposal 发出来。  ", []string{"下周三之前 Alice 要把 pricing 的 proposal 发出来。"}},
		{"。", []string{"。"}},
		{"", nil},
	} {
		if got := splitSentences(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSpreadSentences(t *testing.T) {
	speech := [][2]float64{{6.4, 9.6}, {10.6, 12.4}, {13.2, 16.0}}
	pieces := []string{"下周三之前 Alice 要把 pricing 的 proposal 发出来。", "Let's revisit pricing next week.",
		"这个 release 先只发 iOS，Android 下个 sprint 再跟。"}
	got := spreadSentences(pieces, speech, 6.0, 16.5)
	want := [][2]float64{{6.4, 9.6}, {10.6, 12.4}, {13.2, 16.0}}
	for i, s := range got {
		if math.Abs(s.start-want[i][0]) > 0.01 || math.Abs(s.end-want[i][1]) > 0.01 || s.text != pieces[i] {
			t.Errorf("sentence %d: %+v, want %v", i, s, want[i])
		}
	}
	// Two sentences in one stretch of speech share it by length; none: the window.
	got = spreadSentences([]string{"短。", "This sentence is a good deal longer than the first one."}, [][2]float64{{1, 5}}, 0, 6)
	if got[0].start != 1 || got[0].end >= 2 || got[1].start != got[0].end || got[1].end != 5 {
		t.Errorf("one stretch: %+v", got)
	}
	got = spreadSentences([]string{"Hello there."}, nil, 2, 4)
	if len(got) != 1 || got[0].start != 2 || got[0].end != 4 {
		t.Errorf("no speech: %+v", got)
	}
	if spreadSentences(nil, speech, 0, 1) != nil {
		t.Error("no sentences")
	}
	if w := trWeight("Bo owns it"); math.Abs(w-(1.0+1.5+1.0)) > 0.01 || trWeight(strings.Repeat("字", 4)) != 4 {
		t.Errorf("weights %.2f", w)
	}
}

// Which caption frames revise the caption being written, on the measured service's frames of one item: the text grows
// through a sentence (its last words revised now and then, its first words at once as it begins) and the next
// sentence's replaces it. A frame within trCaptionFresh of the caption's first one (fresh) always revises it; later,
// only one that grew it or rewrote its first words and kept the rest does.
func TestTrRevises(t *testing.T) {
	for _, tt := range []struct {
		prev, next string
		fresh      bool
		want       bool
	}{
		{"", "好人", false, true},
		{"好人", "how 人", true, true},
		{"how 人都", "how 人都到齐了我们开始今天的发布评审", false, true},
		{"the back end merge a", "the back end merge a p an now returns the conflicting paragraphs so the client can", false, true},
		{"crate can we also check", "great can we also check the memory usage", false, true},
		{"andrea 那边", "android 那边的 built 昨天挂了是 graded cache 的", false, true},
		{"同意", "同意 pricing the tears 我们下周一再单独开会讨论", false, true},
		{"可以我让", "可以我让 bo 跑一下满血看看是不是满血的", false, true},
		{"merge a p an now returns the conflicting", "merge api now returns", false, true},
		{"好人都", "how 人都到", false, true}, // its first word revised, the rest kept
		{"好人都到齐了", "how 人都到齐了我们开始", false, true},
		{"how 人都到齐了我们开始今天的发布评审", "先说 ios 这边离线编辑基本做完了", false, false},
		{"the back end merge a p an now returns the conflicting paragraphs so the client can", "help 这词还是", false, false},
		{"好的 bill 负责在周五之前把 crash rate 的", "上次会议决定 excel cap", false, false},
		{"that works for me alice can own the rollout plan and the release notes", "好的 bill 负责在周五之前把 crash rate 的", false, false},
		{"上线了 search latency 比之前", "上线", false, false}, // a new sentence that begins as the last one did
		{"同意", "同步一下", false, false},
		{"好人", "how 人", false, false}, // one character in common is too little to tell, later than at once
		{"好的", "我的意思是", false, false},
		{"好的", "我的意思是", true, true},
	} {
		if got := trRevises(tt.prev, tt.next, tt.fresh); got != tt.want {
			t.Errorf("%q then %q (fresh %v): %v, want %v", tt.prev, tt.next, tt.fresh, got, tt.want)
		}
	}
}
