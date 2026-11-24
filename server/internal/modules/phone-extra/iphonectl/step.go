package iphonectl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// needsScreen says whether a step needs a current read: to find its target, or to check what it touches.
func needsScreen(st Step) bool { return touches(st.Action) || st.Target != nil || st.To != nil }

// current is a read that reflects the phone now, read first when the phone may have changed; it notes when the read
// started a new numbering, after which the steps' numbers mean nothing.
func (c *Controller) current(ctx context.Context, cl *call, deadline time.Time) (*Screen, error) {
	if !cl.stale && cl.session.screen != nil {
		return cl.session.screen, nil
	}
	scr, err := c.readScreen(ctx, cl.bridge, cl.device, cl.phone, cl.session, cl.info, deadline)
	if err != nil {
		return nil, err
	}
	cl.stale = false
	if cl.invalid == "" && cl.view != 0 && scr.Numbering != cl.view {
		cl.invalid = fmt.Sprintf("the screen was replaced (numbering %d is now %d)", cl.view, scr.Numbering)
	}
	return scr, nil
}

// resolved is a step's targets on the current read.
type resolved struct {
	target, to *Numbered
	at, toAt   *Point // bare points
}

func resolve(cl *call, scr *Screen, st Step) (resolved, error) {
	var r resolved
	one := func(t *Target) (*Numbered, *Point, error) {
		if t == nil {
			return nil, nil, nil
		}
		switch {
		case t.byNumber():
			if cl.invalid != "" {
				return nil, nil, fmt.Errorf("element [%d]: %s; look again", t.N, cl.invalid)
			}
			e, ok := scr.Find(t.N)
			if !ok {
				return nil, nil, fmt.Errorf("element [%d] is no longer on screen", t.N)
			}
			return placed(e)
		case t.Label != "":
			found := scr.byLabel(t.Label, t.Role)
			switch len(found) {
			case 0:
				return nil, nil, fmt.Errorf("nothing on screen is labeled %q", t.Label)
			case 1:
				return placed(found[0])
			}
			nums := make([]string, len(found))
			for i, e := range found {
				nums[i] = fmt.Sprintf("[%d]", e.N)
			}
			return nil, nil, fmt.Errorf("%q matches %s: give a number or a role", t.Label, strings.Join(nums, " "))
		}
		p, _ := t.point()
		if scr.Size.known() && (p.X < 0 || p.Y < 0 || p.X > scr.Size.W || p.Y > scr.Size.H) {
			return nil, nil, fmt.Errorf("point (%s,%s) is off the screen (%s×%s)", num(p.X), num(p.Y), num(scr.Size.W), num(scr.Size.H))
		}
		var hit *Numbered
		if e, ok := scr.At(p); ok {
			hit = &e
		}
		return hit, &p, nil
	}
	var err error
	if r.target, r.at, err = one(st.Target); err != nil {
		return r, err
	}
	r.to, r.toAt, err = one(st.To)
	return r, err
}

// placed is e as a target, which needs a frame to touch.
func placed(e Numbered) (*Numbered, *Point, error) {
	if e.Frame.Area() == 0 {
		return nil, nil, fmt.Errorf("element [%d] has no frame on screen to touch; use a point", e.N)
	}
	return &e, nil, nil
}

// byLabel finds elements by label (and role): exact first (ignoring case and spacing), else containing it.
func (s *Screen) byLabel(label, role string) []Numbered {
	norm := func(v string) string { return strings.Join(strings.Fields(strings.ToLower(v)), " ") }
	want := norm(label)
	var exact, partial []Numbered
	for _, e := range s.Elements {
		if role != "" && e.Role != role {
			continue
		}
		switch l := norm(e.Label); {
		case l == want:
			exact = append(exact, e)
		case strings.Contains(l, want):
			partial = append(partial, e)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

// touchOf is where the step's input lands, for the rules.
func touchOf(st Step, tg resolved, scr *Screen) Touch {
	t := Touch{Target: tg.target, To: tg.to}
	point := func(e *Numbered, p *Point) *Point {
		switch {
		case p != nil:
			return p
		case e != nil:
			c := e.Frame.Center()
			return &c
		}
		return nil
	}
	switch st.Action {
	case ActTap, ActLongPress, ActType:
		t.From = point(tg.target, tg.at)
	case ActDrag:
		t.From, t.End = point(tg.target, tg.at), point(tg.to, tg.toAt)
	case ActSwipe:
		var size Size
		if scr != nil {
			size = scr.Size
		}
		if a, b, err := swipeEnds(st, tg, size); err == nil {
			t.From, t.End = &a, &b
		}
	}
	if t.From != nil {
		t.Under = scr.under(*t.From)
	}
	if t.End != nil {
		t.UnderTo = scr.under(*t.End)
	}
	return t
}

// fingerprint is what the owner approves: the step's targets (with their labels and values: a relabeled amount is
// not what they approved), the field that has the keyboard, the app, and what the rules found. A resumed step goes ahead only when it is the same on the screen then.
func fingerprint(st Step, tg resolved, scr *Screen, needs []Sensitivity) string {
	part := func(e *Numbered) string {
		if e == nil {
			return "-"
		}
		return fmt.Sprintf("%d\x01%s\x01%s\x01%s", e.N, identity(e.Element), e.Label, e.Value)
	}
	app, focused := "", "-"
	if scr != nil {
		app = scr.App.BundleID + "\x01" + scr.App.Name
		if f, ok := scr.Focused(); ok {
			focused = part(&f)
		}
	}
	ns := make([]string, len(needs))
	for i, n := range needs {
		ns[i] = string(n)
	}
	slices.Sort(ns)
	return strings.Join([]string{st.Action, app, part(tg.target), part(tg.to), focused, strings.Join(ns, ",")}, "\x00")
}

// resumed is what the owner approved for a step: its fingerprint ("" for none) and, without an element tree, the
// screen they were shown.
type resumed struct {
	print string
	shot  sig
}

// step runs one step, or stops it for the owner's approval.
func (c *Controller) step(ctx context.Context, cl *call, i int, st Step, resume resumed) (r StepResult, w *waiting) {
	approved := resume.print
	start := time.Now()
	r = StepResult{Step: i + 1, Action: st.Action, What: describeStep(st)}
	defer func() { r.Elapsed = time.Since(start) }()
	fail := func(d Delivery, why string) (StepResult, *waiting) {
		r.Delivery, r.Detail = d, why
		return r, nil
	}
	deadline := time.Now().Add(c.limits.Action + gestureTime(st))
	if end := cl.deadline.Add(-cl.reserve); end.Before(deadline) {
		deadline = end
	}

	if st.Action == ActType {
		if at, ok := untypable(st.Text); !ok {
			// Where, not what: what a type step types never goes into a result, a log or an error.
			return fail(DeliveryNotSent, fmt.Sprintf("not attempted: the phone's keyboard input types only printable ASCII, "+
				"tab and newline, and character %d of the text is none of those; nothing was typed. Sending the same text "+
				"again fails the same way", at))
		}
	}
	if input(st.Action) {
		if cl.infoStale {
			info, err := read(ctx, deadline, c.limits.Primitive, func(ctx context.Context) (DeviceInfo, error) {
				return cl.bridge.DeviceInfo(ctx, cl.device)
			})
			if err != nil {
				return fail(DeliveryNotSent, "not attempted: the phone could not be read first: "+err.Error())
			}
			cl.info = info
			cl.infoStale = false
		}
		switch {
		case cl.phone.stuck(c.limits.Stuck) > 0:
			return fail(DeliveryNotSent, fmt.Sprintf("not attempted: an input sent to this phone %s ago has not answered yet, "+
				"and nothing is sent on top of it (for up to %s); look to see what it did", round(cl.phone.stuck(c.limits.Stuck)), round(c.limits.Stuck)))
		case cl.info.locked() && st.Action != ActButton:
			return fail(DeliveryNotSent, "not attempted: the phone is locked, so only button presses can be sent; it has to be unlocked first")
		case !cl.info.can("input"):
			why := "the phone can't take input now"
			if n := cl.info.Notes["input"]; n != "" {
				why += " (" + n + ")"
			}
			return fail(DeliveryNotSent, "not attempted: "+why)
		}
	}

	var scr *Screen
	var tg resolved
	if needsScreen(st) && !cl.noTree {
		var err error
		if scr, err = c.current(ctx, cl, deadline); code(err) == CodeUnsupported {
			cl.noTree = true
		} else if err != nil {
			return fail(DeliveryNotSent, "not attempted: the screen could not be read first: "+err.Error())
		}
	}
	switch {
	case cl.noTree && (st.Target.named() || st.To.named()):
		return fail(DeliveryNotSent, "not attempted: this phone can't give its elements, so a step can't name one; "+
			"use a point (x, y) from the screenshot")
	case cl.noTree:
		// Points only, checked against the screen's size from device_info.
		var err error
		if tg, err = resolve(cl, &Screen{Size: cl.info.Size}, st); err != nil {
			return fail(DeliveryNotSent, "not attempted: "+err.Error())
		}
	case scr != nil:
		var err error
		if tg, err = resolve(cl, scr, st); err != nil {
			return fail(DeliveryNotSent, "not attempted: "+err.Error())
		}
		r.Target = ref(tg.target)
	}

	sized := scr
	if sized == nil {
		sized = &Screen{Size: cl.info.Size}
	}
	v := c.ruleSet().Check(st, touchOf(st, tg, sized), scr)
	if v.Refused != "" {
		return fail(DeliveryNotSent, "refused: "+v.Refused)
	}
	needs := v.Needs
	if !cl.confirmed && input(st.Action) {
		if cl.confirm {
			needs = append(slices.Clone(needs), Confirm)
		}
	}
	// Without an element tree the rules can't see what any input hits: every one waits for the owner, each on its
	// own (fail-closed; one answer never covers the steps after it, whose screen nobody has seen yet).
	if cl.noTree && input(st.Action) {
		needs = append(slices.Clone(needs), Unchecked)
	}
	// A resumed step is compared with what the owner approved even when the rules find nothing now: a password
	// approved for one field must not go to another that happens to need no approval.
	if len(needs) > 0 || approved != "" {
		print := fingerprint(st, tg, scr, needs)
		switch {
		case approved == "":
			r.Needs, r.Waiting = needs, true
			a := newApproval(i, st, tg.target, scr, Verdict{Needs: needs, Evidence: v.Evidence})
			a.At = tg.at
			if slices.Contains(needs, Confirm) {
				named := scr
				if cl.invalid != "" {
					named = nil
				}
				for _, rest := range cl.remaining {
					a.Steps = append(a.Steps, summary(rest, named))
				}
			}
			w := &waiting{approval: a, print: print}
			if cl.noTree {
				// The owner is shown the screen as it is now; the step goes only if it is still that screen.
				w.shot = c.before(ctx, cl, deadline)
			}
			r.Delivery = DeliveryNotSent
			r.Detail = "waits for the owner's approval (" + joinNeeds(needs) + "); nothing of it was sent"
			return r, w
		case approved != print:
			return fail(DeliveryNotSent, "not sent: the screen changed since the owner approved this step; look again")
		case cl.noTree:
			now := c.before(ctx, cl, deadline)
			if resume.shot == nil || now == nil || differs(resume.shot, now) {
				return fail(DeliveryNotSent, "not sent: the screen changed since the owner approved this step (or could not "+
					"be compared); look again")
			}
		}
		r.Approved = true
		if slices.Contains(needs, Confirm) {
			cl.confirmed = true
		}
	}
	return c.act(ctx, cl, st, scr, tg, deadline, r), nil
}

func joinNeeds(needs []Sensitivity) string {
	s := make([]string, len(needs))
	for i, n := range needs {
		s[i] = string(n)
	}
	return strings.Join(s, ", ")
}

// act sends the step's input and, when it was sent (or may have been), checks the screen for its effect.
func (c *Controller) act(ctx context.Context, cl *call, st Step, scr *Screen, tg resolved, deadline time.Time, r StepResult) StepResult {
	if !input(st.Action) {
		return c.deliver(ctx, cl, st, scr, tg, deadline, r)
	}
	before := c.before(ctx, cl, deadline)
	r = c.deliver(ctx, cl, st, scr, tg, deadline, r)
	if r.Delivery == DeliveryNotSent {
		// Nothing reached the phone (a type step whose focusing tap went out is reported unknown).
		return r
	}
	cl.dirty = true
	effect, note := c.effect(ctx, cl, before, deadline)
	r.Effect = effect
	if note != "" {
		if r.Detail != "" {
			r.Detail += "; "
		}
		r.Detail += note
	}
	return r
}

// untypable is the first character the bridge's keyboard input can't type: it types printable ASCII, tab and
// newline only, and refuses the whole text otherwise.
// untypable is the 1-based position (in characters) of the first character the keyboard input can't type; ok when
// there is none.
func untypable(text string) (at int, ok bool) {
	n := 0
	for _, r := range text {
		n++
		if r != '\t' && r != '\n' && (r < 0x20 || r > 0x7e) {
			return n, false
		}
	}
	return 0, true
}

// deliver sends the step's input.
func (c *Controller) deliver(ctx context.Context, cl *call, st Step, scr *Screen, tg resolved, deadline time.Time, r StepResult) StepResult {
	dev := cl.device
	size := cl.info.Size
	if scr != nil && scr.Size.known() {
		size = scr.Size
	}
	at := func(e *Numbered, p *Point) Point {
		if p != nil {
			return *p
		}
		return e.Frame.Center()
	}
	// send sends one input within the step's deadline. An input still out at its limit counts as in flight until
	// it answers, so nothing more is sent on top of it.
	send := func(extra time.Duration, fn func(context.Context) error) (Delivery, string) {
		limit := min(c.limits.Primitive+extra, time.Until(deadline))
		switch {
		case ctx.Err() != nil:
			return DeliveryNotSent, "not attempted: the call was given up"
		case limit <= 0:
			return DeliveryNotSent, "not attempted: the step's time limit was reached"
		}
		answered := cl.phone.sending()
		err, timedOut := bounded(ctx, limit, func(ctx context.Context) error {
			defer answered()
			return fn(ctx)
		})
		d := delivery(err, timedOut)
		switch {
		case timedOut:
			return d, fmt.Sprintf("no answer from the phone within %s; it may have been sent, so it is not sent again: look first", round(limit))
		case d == DeliveryNotSent:
			return d, "the phone did not take it: " + err.Error()
		case code(err) == CodeDisconnected:
			return d, "the link to the phone dropped; it may have been sent, so it is not sent again: look first"
		case err != nil:
			return d, "it may have been sent, so it is not sent again: " + err.Error()
		}
		return d, ""
	}
	if input(st.Action) {
		cl.stale = true
	}
	ms := time.Duration(st.Ms) * time.Millisecond
	switch st.Action {
	case ActTap:
		p := at(tg.target, tg.at)
		r.At = &p
		count := max(st.Count, 1)
		r.Delivery, r.Detail = send(0, func(ctx context.Context) error { return cl.bridge.Tap(ctx, dev, p, count) })
	case ActLongPress:
		p := at(tg.target, tg.at)
		r.At = &p
		d := orDur(ms, LongPress)
		r.Delivery, r.Detail = send(d, func(ctx context.Context) error { return cl.bridge.LongPress(ctx, dev, p, d) })
	case ActDrag:
		a, b := at(tg.target, tg.at), at(tg.to, tg.toAt)
		r.At = &a
		d, hold := orDur(ms, DragTime), time.Duration(st.HoldMs)*time.Millisecond
		r.Delivery, r.Detail = send(d+hold, func(ctx context.Context) error { return cl.bridge.Drag(ctx, dev, a, b, d, hold) })
	case ActSwipe:
		a, b, err := swipeEnds(st, tg, size)
		if err != nil {
			r.Delivery, r.Detail = DeliveryNotSent, "not attempted: "+err.Error()
			return r
		}
		r.At = &a
		d := orDur(ms, SwipeTime)
		r.Delivery, r.Detail = send(d, func(ctx context.Context) error { return cl.bridge.Swipe(ctx, dev, a, b, d) })
	case ActType:
		if tg.target != nil || tg.at != nil {
			p := at(tg.target, tg.at)
			r.At = &p
			if d, why := send(0, func(ctx context.Context) error { return cl.bridge.Tap(ctx, dev, p, 1) }); d != DeliverySent {
				r.Delivery, r.Detail = d, "tapping the field first: "+why
				return r
			}
			t := time.NewTimer(min(FocusSettle, time.Until(deadline)))
			select {
			case <-t.C:
			case <-ctx.Done():
			}
			t.Stop()
			d, why := send(0, func(ctx context.Context) error { return cl.bridge.Keys(ctx, dev, st.Text) })
			r.Delivery, r.Detail = d, why
			if d == DeliveryNotSent {
				// The tap reached the phone: the step is not safe to send again as a whole.
				r.Delivery, r.Detail = DeliveryUnknown, "the field was tapped, the text was not sent: "+why
			}
			return r
		}
		r.Delivery, r.Detail = send(0, func(ctx context.Context) error { return cl.bridge.Keys(ctx, dev, st.Text) })
	case ActKey:
		repeat := max(st.Repeat, 1)
		r.Delivery, r.Detail = send(0, func(ctx context.Context) error { return cl.bridge.Key(ctx, dev, st.Key, repeat) })
	case ActButton:
		cl.infoStale = true
		r.Delivery, r.Detail = send(ms, func(ctx context.Context) error { return cl.bridge.Button(ctx, dev, st.Button, ms) })
	case ActLaunch:
		r.Delivery, r.Detail = send(0, func(ctx context.Context) error { return cl.bridge.LaunchApp(ctx, dev, strings.TrimSpace(st.App)) })
	case ActTerminate:
		r.Delivery, r.Detail = send(0, func(ctx context.Context) error { return cl.bridge.TerminateApp(ctx, dev, strings.TrimSpace(st.App)) })
	case ActWait:
		left := time.Until(deadline)
		t := time.NewTimer(min(ms, left))
		select {
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
		r.Delivery = DeliveryDone
		if left < ms || ctx.Err() != nil {
			r.Detail = "cut short by the time limit"
		}
	}
	return r
}

// swipeEnds are where a swipe starts and ends: from its target (or the middle of the screen) to its To, or by
// Distance points (default half the screen along the axis) in Direction.
func swipeEnds(st Step, tg resolved, size Size) (Point, Point, error) {
	var a Point
	switch {
	case tg.at != nil:
		a = *tg.at
	case tg.target != nil:
		a = tg.target.Frame.Center()
	case size.known():
		a = size.center()
	default:
		return a, a, errors.New("the screen's size is unknown: give the swipe a start")
	}
	if tg.toAt != nil {
		return a, *tg.toAt, nil
	}
	if tg.to != nil {
		return a, tg.to.Frame.Center(), nil
	}
	if !size.known() {
		return a, a, errors.New("the screen's size is unknown: give the swipe an end")
	}
	b := a
	switch dist := st.Distance; st.Direction {
	case "up":
		b.Y -= orFloat(dist, size.H/2)
	case "down":
		b.Y += orFloat(dist, size.H/2)
	case "left":
		b.X -= orFloat(dist, size.W/2)
	case "right":
		b.X += orFloat(dist, size.W/2)
	}
	b.X, b.Y = min(max(b.X, 0), size.W), min(max(b.Y, 0), size.H)
	return a, b, nil
}

func orFloat(v, d float64) float64 {
	if v > 0 {
		return v
	}
	return d
}
