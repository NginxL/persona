package voice

import (
	"math"
	"slices"
	"strings"
	"unicode"
)

// Choosing a window's text (docs/assistant/meeting-minutes.md "The built-in adapter"): every session's text of the
// window is compared with the captions (live) or the reference (the final pass) by normalized character edit
// distance; a session further than trDropOver is dropped for the window, and of the rest the medoid wins, the text
// closest to the others'. Nothing is merged across sessions.

// trDropOver: a session whose text is further than this from the captions, the reference or (with neither) every
// other session is not trusted for the window. Healthy sessions measured 0.25-0.3 from the captions, one that began to
// play a meeting participant 0.89.
const trDropOver = 0.5

// trNorm is s as the distance compares it: letters and digits only (spaces, punctuation and symbols dropped), case
// folded, full-width forms as half-width.
func trNorm(s string) []rune {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 0xFF01 && r <= 0xFF5E {
			r -= 0xFEE0
		}
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			continue
		}
		out = append(out, unicode.ToLower(r))
	}
	return out
}

// trHasText: s has a letter or a digit.
func trHasText(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

// normDistance is the edit distance between a and b in characters, normalized (trNorm) and divided by the longer's
// length: 0 the same, 1 nothing in common. Two empty texts are the same.
func normDistance(a, b string) float64 {
	x, y := trNorm(a), trNorm(b)
	n := max(len(x), len(y))
	if n == 0 {
		return 0
	}
	return float64(levenshtein(x, y)) / float64(n)
}

func levenshtein(a, b []rune) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	prev, cur := make([]int, len(b)+1), make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// trCaptionRevise: a caption frame revises the caption before it when their beginnings are at most this share of
// their length apart (normalized): the service revises a caption's last words as it grows ("andrea 那边" then
// "android 那边的…" measured 0.38), while the next sentence written over it is about 1 away.
const trCaptionRevise = 0.4

// trRevises says whether caption text next revises prev, the text of the caption being written, rather than begin
// the next sentence written over it (the measured service writes a caption session's whole meeting as one item).
// fresh: next came within trCaptionFresh of the caption's first frame, and revises it whatever it says (the measured
// service rewrote a caption's first words at once: "好人", then "how 人" at the same moment). Otherwise, compared
// normalized (trNorm): next not under half as long as prev (that is a new sentence, also one that begins as the last
// one did), and either as much of prev as next has (all of it when next is longer) within trCaptionRevise of a
// beginning of next (grown, its last words maybe revised), or the second half of prev, two characters or more, within
// trCaptionRevise of a stretch of next's beginning about as long as prev (its first words revised, the rest kept).
// Text with no letters or digits revises anything.
func trRevises(prev, next string, fresh bool) bool {
	p, n := trNorm(prev), trNorm(next)
	switch {
	case fresh || len(p) == 0 || len(n) == 0:
		return true
	case 2*len(n) < len(p):
		return false
	}
	k := min(len(p), len(n))
	if float64(prefixDistance(p[:k], n)) <= trCaptionRevise*float64(k) {
		return true
	}
	tail := p[len(p)/2:]
	if len(tail) < 2 {
		return false
	}
	head := n[:min(len(n), len(p)+max(4, len(p)/2))]
	return float64(infixDistance(tail, head)) <= trCaptionRevise*float64(len(tail))
}

// prefixDistance is the least edit distance between a and a beginning of b (any length, none included).
func prefixDistance(a, b []rune) int { return trAlign(a, b, false) }

// infixDistance is the least edit distance between a and a stretch of b (anywhere, of any length).
func infixDistance(a, b []rune) int { return trAlign(a, b, true) }

// trAlign is the least edit distance between all of a and a part of b that ends anywhere and starts at b's beginning,
// or anywhere when anyStart.
func trAlign(a, b []rune, anyStart bool) int {
	prev, cur := make([]int, len(b)+1), make([]int, len(b)+1)
	for j := range prev {
		if !anyStart {
			prev[j] = j
		}
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return slices.Min(prev)
}

// medoid is which of keep (indexes into texts) is closest to the others: the least summed distance. A tie goes to the
// one closer to anchor when there is one (of two, the one closer to the captions or the reference), then to the longer
// (more letters and digits: of two that agree, the one that missed less), then the first.
func medoid(texts []string, keep []int, anchor *string) int {
	best, bestSum, bestAnchor, bestLen := -1, 0.0, 0.0, 0
	for _, i := range keep {
		sum := 0.0
		for _, k := range keep {
			if k != i {
				sum += normDistance(texts[i], texts[k])
			}
		}
		a := 0.0
		if anchor != nil {
			a = normDistance(texts[i], *anchor)
		}
		n := len(trNorm(texts[i]))
		const eps = 1e-9
		switch {
		case best < 0 || sum < bestSum-eps:
		case sum > bestSum+eps:
			continue
		case a < bestAnchor-eps:
		case a > bestAnchor+eps || n <= bestLen:
			continue
		}
		best, bestSum, bestAnchor, bestLen = i, sum, a, n
	}
	return best
}

// trAgree is which of keep agree with another: one further than trDropOver from every other one is left out. Of two,
// both stay when they agree and neither does when they do not.
func trAgree(texts []string, keep []int) []int { return trAgreeWithin(texts, keep, trDropOver) }

// trAgreeWithin is which of keep are within most of another one.
func trAgreeWithin(texts []string, keep []int, most float64) []int {
	var out []int
	for _, i := range keep {
		for _, k := range keep {
			if k != i && normDistance(texts[i], texts[k]) <= most {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

// pickLive chooses a live window's text among the verbatim sessions' (texts, of the sessions that answered for the
// window). With captions (caption has text): a session further than trDropOver from them is dropped and the medoid of
// the rest wins; with none left, pick is -1 and the captions' text is sent. Without captions: a session that agrees
// with no other is dropped (a session alone is taken: there is nothing to check it against); pick -1 sends nothing.
// dropped says which sessions were not trusted, for the rule that replaces a session dropped in two windows in a row;
// without captions only a choice among three or more counts.
func pickLive(caption string, texts []string) (pick int, dropped []bool) {
	dropped = make([]bool, len(texts))
	all := make([]int, len(texts))
	for i := range texts {
		all[i] = i
	}
	if trHasText(caption) {
		var keep []int
		for i, t := range texts {
			if normDistance(t, caption) > trDropOver {
				dropped[i] = true
			} else {
				keep = append(keep, i)
			}
		}
		if len(keep) == 0 {
			return -1, dropped
		}
		return medoid(texts, keep, &caption), dropped
	}
	switch len(texts) {
	case 0:
		return -1, dropped
	case 1:
		return 0, dropped
	}
	keep := trAgree(texts, all)
	if len(texts) >= 3 {
		for _, i := range all {
			dropped[i] = !containsInt(keep, i)
		}
	}
	if len(keep) == 0 {
		return -1, dropped
	}
	return medoid(texts, keep, nil), dropped
}

// How a final-pass window's reference was used (pickReplay).
type trRefUse int

const (
	trRefNone     trRefUse = iota // no reference
	trRefChecked                  // the sessions were checked against it
	trRefShort                    // too short to check against: chosen as without one
	trRefOutvoted                 // the sessions agree with each other and none with it: trusted over it
)

// A final-pass window's reference that has fewer letters and digits (normalized) than trRefShortShare of the text of
// the sessions that wrote any (their lower median), or than trRefPerSecond per second of the window's speech while
// they wrote more, is not checked against: it caught only a few words of the window (measured: a live caption of 22
// characters over 14 s of speech, against one session of 204 that heard all of it and one that wrote nothing), and a
// session near it is no better than one that heard more. Sessions within trAgreeClose of each other are trusted over
// a reference none of them is near.
const (
	trRefShortShare = 0.5
	trRefPerSecond  = 2.0
	trAgreeClose    = 0.3
)

// pickReplay chooses a final-pass window's text (speech: its seconds of speech). With a reference (ref has text): one
// much shorter than the text of the sessions that wrote any (trRefShortShare, trRefPerSecond) counts as none, before
// anything is checked against it (a session near a few words of the window is no better than one that heard all of
// it), and so does one set aside already (aside: the window was given more sessions for it); the window is then
// chosen as without one among the sessions that wrote text (ones that wrote nothing never agree a window empty over a
// reference that has words), and with fewer than two of them that agree it is kept: one session is never taken
// uncorroborated (the job gives the window more sessions, once). Otherwise a session that stopped early or is further
// than trDropOver from it is dropped, and the medoid of the rest wins (of two, the one closer to the reference); with
// none left, sessions that agree with each other (within trAgreeClose, two or more with text) are trusted over it
// (their medoid), else the reference stays (kept). Without one: of the sessions that did not stop early, one that
// agrees with no other is dropped and the medoid of the rest wins; with fewer than two left the window gets no text
// of the pass (kept: the hub keeps what it has).
func pickReplay(ref *string, aside bool, texts []string, stoppedEarly []bool, speech float64) (pick int, kept bool, use trRefUse) {
	var keep, said []int
	for i := range texts {
		if stoppedEarly[i] {
			continue
		}
		keep = append(keep, i)
		if trHasText(texts[i]) {
			said = append(said, i)
		}
	}
	switch {
	case ref == nil || !trHasText(*ref):
	case aside || refShort(*ref, texts, said, speech):
		use, keep = trRefShort, said // chosen as without a reference
	default:
		var near []int
		for _, i := range keep {
			if normDistance(texts[i], *ref) <= trDropOver {
				near = append(near, i)
			}
		}
		if len(near) > 0 {
			return medoid(texts, near, ref), false, trRefChecked
		}
		if close := trAgreeWithin(texts, said, trAgreeClose); len(close) >= 2 {
			return medoid(texts, close, nil), false, trRefOutvoted
		}
		return -1, true, trRefChecked
	}
	keep = trAgree(texts, keep)
	if len(keep) < 2 {
		return -1, true, use
	}
	return medoid(texts, keep, nil), false, use
}

// refShort: reference ref is much shorter than the text of the sessions said (indexes into texts, those that wrote
// text: one that wrote nothing does not make it long enough): under trRefShortShare of their lower median length (one
// session that writes too much does not make it short), or under trRefPerSecond per second of speech while that is
// longer.
func refShort(ref string, texts []string, said []int, speech float64) bool {
	if len(said) == 0 {
		return false
	}
	lens := make([]int, len(said))
	for k, i := range said {
		lens[k] = len(trNorm(texts[i]))
	}
	slices.Sort(lens)
	median, n := float64(lens[(len(lens)-1)/2]), float64(len(trNorm(ref)))
	return n < trRefShortShare*median || (n < trRefPerSecond*speech && n < median)
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// trSentence is one sentence sent to the manager, in recording seconds.
type trSentence struct {
	start, end float64
	text       string
}

// splitSentences cuts a reply into its sentences: after a full stop, question or exclamation mark (and the quotes or
// brackets that close with it), a Latin one only before a space. A reply is mostly one sentence; sometimes two said
// close together come as one. The text is kept as written, only trimmed.
func splitSentences(text string) []string {
	rs := []rune(text)
	var out []string
	from := 0
	cut := func(to int) {
		if s := strings.TrimSpace(string(rs[from:to])); s != "" {
			if !trHasText(s) && len(out) > 0 {
				out[len(out)-1] += s // punctuation alone joins the sentence before it
			} else {
				out = append(out, s)
			}
		}
		from = to
	}
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '。', '！', '？', '!', '?', '.':
		default:
			continue
		}
		j := i + 1
		for j < len(rs) && trCloser(rs[j]) {
			j++
		}
		if rs[i] == '.' && j < len(rs) && !unicode.IsSpace(rs[j]) {
			continue // 4.5, e.g.: not the end of a sentence
		}
		cut(j)
		i = j - 1
	}
	cut(len(rs))
	return out
}

// trCloser: a quote or bracket that closes with the sentence before it.
func trCloser(r rune) bool {
	switch r {
	case '"', '\'', '”', '’', ')', '）', '」', '』', '】':
		return true
	}
	return false
}

// trWeight is about how long s takes to say, in units of one Chinese character: a Latin word is a little under two.
func trWeight(s string) float64 {
	w, inWord := 0.0, false
	for _, r := range s {
		switch {
		case r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if !inWord {
				w += 0.5
			}
			w += 0.25
			inWord = true
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			w++
			inWord = false
		default:
			inWord = false
		}
	}
	return max(w, 0.5)
}

// spreadSentences gives each sentence of a window its time: the window's speech (spans in recording seconds, in
// order) is shared out in the order of the sentences, by how long each takes to say (trWeight), and a boundary
// between two sentences near a pause in the speech moves to the pause (people pause between sentences). Without speech
// the window [from, to] is shared out. About a second is precise enough for the minutes and for jumping to a line.
func spreadSentences(pieces []string, speech [][2]float64, from, to float64) []trSentence {
	if len(pieces) == 0 {
		return nil
	}
	if len(speech) == 0 {
		speech = [][2]float64{{from, max(to, from)}}
	}
	total := 0.0
	var pauses []float64 // the pauses, in seconds of speech before them
	for i, s := range speech {
		total += s[1] - s[0]
		if i < len(speech)-1 {
			pauses = append(pauses, total)
		}
	}
	weights, sum := make([]float64, len(pieces)), 0.0
	for i, p := range pieces {
		weights[i] = trWeight(p)
		sum += weights[i]
	}
	// bounds[i] is where sentence i starts, in seconds of speech.
	bounds := make([]float64, len(pieces)+1)
	for i := range pieces {
		bounds[i+1] = bounds[i] + total*weights[i]/sum
	}
	used := -1
	for i := 1; i < len(pieces); i++ {
		near := min(trSnapMost, trSnapShare*(bounds[i]-bounds[i-1]), trSnapShare*(bounds[i+1]-bounds[i]))
		best := -1
		for k, p := range pauses {
			if k > used && p > bounds[i-1] && p < bounds[i+1] && math.Abs(p-bounds[i]) <= near &&
				(best < 0 || math.Abs(p-bounds[i]) < math.Abs(pauses[best]-bounds[i])) {
				best = k
			}
		}
		if best >= 0 {
			bounds[i], used = pauses[best], best
		}
	}
	// at is the moment the speech's first done seconds end (start: the next span's beginning at a pause).
	at := func(done float64, start bool) float64 {
		for _, s := range speech {
			d := s[1] - s[0]
			if done < d || (!start && done <= d) {
				return s[0] + done
			}
			done -= d
		}
		return speech[len(speech)-1][1]
	}
	out := make([]trSentence, len(pieces))
	for i, p := range pieces {
		out[i] = trSentence{start: at(bounds[i], true), end: at(bounds[i+1], false), text: p}
	}
	out[len(out)-1].end = speech[len(speech)-1][1]
	return out
}

// A boundary between sentences moves to a pause at most this far, in seconds of speech, and at most this share of
// either sentence's time.
const (
	trSnapMost  = 1.5
	trSnapShare = 0.4
)
