package iphonectl

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The text the model gets: only facts (what is on screen, what each step did, how long it took), in English like
// the other tools' results. What the owner reads is not here: approvals are data the manager words.

// round writes a duration the way the results do: 0.42s, 12.3s, 1m30s.
func round(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return strconv.FormatFloat(d.Seconds(), 'f', 2, 64) + "s"
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// describe is an element in a line: role, label, value and the states that matter. A secure field's value is
// never shown.
func describe(e Element) string {
	var b strings.Builder
	b.WriteString(words(e.Role))
	if e.Label != "" {
		b.WriteString(" " + strconv.Quote(clipRunes(e.Label, 120)))
	}
	switch {
	case e.Value == "" || e.Value == e.Label:
	case e.secure():
		b.WriteString(" = " + hiddenValue)
	default:
		b.WriteString(" = " + strconv.Quote(clipRunes(e.Value, 120)))
	}
	for _, s := range []struct {
		on   bool
		word string
	}{{e.Focused, "focused"}, {e.Selected, "selected"}, {e.Disabled, "disabled"}} {
		if s.on {
			b.WriteString(" " + s.word)
		}
	}
	return b.String()
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func line(e Numbered, mark string) string {
	return fmt.Sprintf("%s[%d] %s %s", mark, e.N, describe(e.Element), e.Frame)
}

func screenHead(s *Screen) string {
	head := fmt.Sprintf("App: %s\nNumbering %d · %s", s.App, s.Numbering, plural(len(s.Elements), "element"))
	if s.Size.known() {
		head += fmt.Sprintf(" · screen %s×%s points", num(s.Size.W), num(s.Size.H))
	}
	if s.Truncated {
		head += " · the list stops at the bridge's limit"
	}
	if s.Locked {
		head += "\nThe phone is locked."
	}
	return head
}

func screenshotLine(img *Image) string {
	switch {
	case img == nil:
		return ""
	case img.Width > 0 && img.Height > 0:
		return fmt.Sprintf("Screenshot attached (%d×%d pixels).", img.Width, img.Height)
	}
	return "Screenshot attached."
}

// list writes every element, marking those new (+) or moved (~) since the read before when the numbering stayed.
func list(s *Screen, d Diff) []string {
	marks := map[int]string{}
	if !d.Replaced {
		for _, c := range d.Added {
			marks[c.N] = "+"
		}
		for _, c := range d.Moved {
			marks[c.N] = "~"
		}
	}
	out := make([]string, len(s.Elements))
	for i, e := range s.Elements {
		out[i] = line(e, marks[e.N])
	}
	return out
}

func gone(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("[%d]", n)
	}
	return strings.Join(parts, " ")
}

// Text is the look for the model.
func (r LookResult) Text() string {
	var lines []string
	if r.Screen != nil {
		head := screenHead(r.Screen)
		if r.Device != nil && r.Device.locked() && !r.Screen.Locked {
			head += "\nThe phone is locked."
		}
		lines = append(lines, head+"\nRead in "+round(r.Elapsed)+".")
		if !r.Changes.Replaced && !r.Changes.Empty() {
			lines = append(lines, fmt.Sprintf("Since the last read: %d new (+), %d moved (~), %d gone%s.",
				len(r.Changes.Added), len(r.Changes.Moved), len(r.Changes.Gone), goneSuffix(r.Changes.Gone)))
		}
		lines = append(lines, list(r.Screen, r.Changes)...)
	} else if r.Device != nil && r.Device.locked() {
		lines = append(lines, "The phone is locked.")
	}
	for _, n := range r.Notes {
		lines = append(lines, capitalize(n)+".")
	}
	if s := screenshotLine(r.image); s != "" {
		lines = append(lines, s)
		if d, img := r.Device, r.image; r.Screen == nil && d != nil && d.Size.known() && img.Width > 0 && img.Height > 0 {
			lines = append(lines, fmt.Sprintf("Points = screenshot pixels × %s (x: %s pt over %d px, y: %s pt over %d px).",
				strconv.FormatFloat(d.Size.W/float64(img.Width), 'f', 4, 64), num(d.Size.W), img.Width, num(d.Size.H), img.Height))
		}
	}
	if r.Screen == nil {
		lines = append(lines, "Took "+round(r.Elapsed)+".")
	}
	return strings.Join(lines, "\n")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func goneSuffix(nums []int) string {
	if len(nums) == 0 {
		return ""
	}
	return " " + gone(nums)
}

// describeStep is a step as the model wrote it; a typed text is only counted.
func describeStep(st Step) string {
	target := func(t *Target) string {
		switch {
		case t == nil:
			return ""
		case t.N > 0:
			return fmt.Sprintf("[%d]", t.N)
		case t.Label != "":
			if t.Role != "" {
				return words(t.Role) + " " + strconv.Quote(t.Label)
			}
			return strconv.Quote(t.Label)
		}
		p, _ := t.point()
		return fmt.Sprintf("(%s,%s)", num(p.X), num(p.Y))
	}
	switch st.Action {
	case ActTap:
		if st.Count == 2 {
			return "double tap " + target(st.Target)
		}
		return "tap " + target(st.Target)
	case ActLongPress:
		return "long press " + target(st.Target)
	case ActDrag:
		return "drag " + target(st.Target) + " to " + target(st.To)
	case ActSwipe:
		s := "swipe"
		if st.Direction != "" {
			s += " " + st.Direction
		}
		if st.Target != nil {
			s += " from " + target(st.Target)
		}
		if st.To != nil {
			s += " to " + target(st.To)
		}
		return s
	case ActType:
		s := "type " + plural(utf8.RuneCountInString(st.Text), "character")
		if st.Target != nil {
			s += " into " + target(st.Target)
		}
		return s
	case ActKey:
		if st.Repeat > 1 {
			return fmt.Sprintf("key %s ×%d", st.Key, st.Repeat)
		}
		return "key " + st.Key
	case ActButton:
		return "button " + st.Button
	case ActLaunch:
		return "launch " + st.App
	case ActTerminate:
		return "terminate " + st.App
	case ActWait:
		return fmt.Sprintf("wait %d ms", st.Ms)
	}
	return st.Action
}

// effectWords say a step's effect in its line. "sent" only means the bridge wrote the input to the phone, which
// doesn't confirm it; the screen is the evidence.
var effectWords = map[Effect]string{EffectChanged: "screen changed", EffectUnchanged: "no visible change",
	EffectUnknown: "screen not checked"}

// Text is the do for the model.
func (r DoResult) Text() string {
	if r.Note != "" {
		return capitalize(r.Note) + "."
	}
	var lines []string
	for _, s := range r.Steps {
		l := fmt.Sprintf("%d. %s", s.Step, s.What)
		if s.Target != nil && s.Target.Role != "" {
			l += " → " + describe(Element{Role: s.Target.Role, Label: s.Target.Label})
		}
		if s.At != nil {
			l += fmt.Sprintf(" at (%s,%s)", num(s.At.X), num(s.At.Y))
		}
		l += ": " + string(s.Delivery)
		if w := effectWords[s.Effect]; w != "" {
			l += ", " + w
		}
		if s.Elapsed > 0 {
			l += ", " + round(s.Elapsed)
		}
		if s.Approved {
			l += ", approved by the owner"
		}
		if s.Detail != "" {
			l += " — " + s.Detail
		}
		lines = append(lines, l)
	}
	if r.approval != nil {
		lines = append(lines, "Nothing from the waiting step on was sent; it runs only once the owner approves it.")
	}
	lines = append(lines, "Took "+round(r.Elapsed)+".")
	switch {
	case r.Screen == nil:
		if r.ReadError != "" {
			lines = append(lines, capitalize(r.ReadError)+".")
		}
	case r.Changes.Replaced:
		lines = append(lines, "The screen was replaced; every element is listed:", screenHead(r.Screen))
		lines = append(lines, list(r.Screen, r.Changes)...)
	default:
		lines = append(lines, screenHead(r.Screen))
		if r.Changes.Empty() {
			lines = append(lines, "No change on screen.")
			break
		}
		for _, c := range r.Changes.Added {
			lines = append(lines, line(c.Numbered, "+"))
		}
		for _, c := range r.Changes.Moved {
			lines = append(lines, line(c.Numbered, "~")+" from "+c.From.String())
		}
		if len(r.Changes.Gone) > 0 {
			lines = append(lines, "Gone: "+gone(r.Changes.Gone))
		}
	}
	if s := screenshotLine(r.image); s != "" {
		lines = append(lines, s)
	}
	return strings.Join(lines, "\n")
}

// Text is the status for the model.
func (r StatusResult) Text() string {
	var lines []string
	switch d := r.Device; {
	case d == nil:
		lines = append(lines, "Phone: did not answer: "+r.Error+".")
	default:
		l := "Phone: " + orUnknown(d.Name)
		var about []string
		if d.ProductType != "" {
			about = append(about, d.ProductType)
		}
		if d.OSVersion != "" {
			about = append(about, "iOS "+d.OSVersion)
		}
		if len(about) > 0 {
			l += " (" + strings.Join(about, ", ") + ")"
		}
		if d.locked() {
			l += " · locked"
		}
		lines = append(lines, l)
		for _, capability := range []string{"screenshot", "ax_tree", "input", "apps"} {
			if !d.can(capability) {
				lines = append(lines, fmt.Sprintf("Can't %s now (%s).", map[string]string{"screenshot": "take screenshots",
					"ax_tree": "read elements", "input": "send input", "apps": "manage apps"}[capability], orUnknown(d.Notes[capability])))
			}
		}
	}
	if r.App != nil {
		lines = append(lines, fmt.Sprintf("Latest read: numbering %d · %s in %s · %s ago", r.Numbering,
			plural(r.Elements, "element"), *r.App, round(r.ReadAgo)))
	} else {
		lines = append(lines, "Latest read: none yet.")
	}
	if r.Waiting {
		lines = append(lines, "A step waits for the owner's approval.")
	}
	if r.Busy {
		lines = append(lines, "A look or do is running.")
	}
	if r.Stuck > 0 {
		lines = append(lines, "An input sent "+round(r.Stuck)+" ago has not answered yet; no other input is sent until it does.")
	}
	if r.Last != "" {
		lines = append(lines, "Last call: "+r.Last+".")
	}
	return strings.Join(lines, "\n")
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
