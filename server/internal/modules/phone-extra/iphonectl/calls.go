package iphonectl

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LookResult is a read of the screen.
type LookResult struct {
	Screen  *Screen       `json:"screen,omitempty"`
	Changes Diff          `json:"changes"` // since the read before
	Device  *DeviceInfo   `json:"device,omitempty"`
	Elapsed time.Duration `json:"elapsed"`
	// Notes say what could not be read; with no Screen and no screenshot, why there is nothing.
	Notes []string `json:"notes,omitempty"`
	image *Image
}

// look reads the device, the elements and (when asked) a screenshot; each that fails is a note, the rest answer.
func (c *Controller) look(ctx context.Context, b Bridge, device, sessionID string, screenshot bool) LookResult {
	start := time.Now()
	p := c.phone(device)
	if !p.acquire(ctx, c.limits.Busy) {
		return LookResult{Notes: []string{busyNote}, Elapsed: time.Since(start)}
	}
	defer p.release()
	s := p.session(sessionID)
	deadline := start.Add(c.limits.Call)
	res := LookResult{}

	info, err := read(ctx, deadline, c.limits.Primitive, func(ctx context.Context) (DeviceInfo, error) {
		return b.DeviceInfo(ctx, device)
	})
	if err != nil {
		res.Notes = append(res.Notes, "device not read: "+err.Error())
	} else {
		res.Device = &info
	}
	before := s.screen
	if !info.can("ax_tree") {
		res.Notes = append(res.Notes, noTreeNote(info))
	} else if scr, err := c.readScreen(ctx, b, device, p, s, info, deadline); err != nil {
		if code(err) == CodeUnsupported {
			res.Notes = append(res.Notes, noTreeNote(info))
		} else {
			res.Notes = append(res.Notes, "elements not read: "+err.Error())
		}
	} else {
		res.Screen, res.Changes = scr, diff(before, scr)
	}
	if screenshot {
		img, err := read(ctx, deadline, c.limits.Primitive, func(ctx context.Context) (Image, error) {
			return b.Screenshot(ctx, device)
		})
		if err != nil {
			res.Notes = append(res.Notes, "screenshot not taken: "+err.Error())
		} else {
			res.image = &img
		}
	}
	res.Elapsed = time.Since(start)
	last := "look: nothing read"
	if res.Screen != nil {
		last = fmt.Sprintf("look: %s, %s", plural(len(res.Screen.Elements), "element"), round(res.Elapsed))
	}
	p.set(func() { s.last = last })
	return res
}

// noTreeNote says why a phone gives no elements and what works instead.
func noTreeNote(info DeviceInfo) string {
	why := info.Notes["ax_tree"]
	if why == "" {
		why = CodeUnsupported
	}
	note := "elements not read: this phone can't give its elements (" + why + "), so act on points (x, y) read off the screenshot"
	if info.Size.known() {
		note += fmt.Sprintf(", in points of a %s×%s screen", num(info.Size.W), num(info.Size.H))
	}
	return note
}

// readScreen reads the elements and numbers them against the session's latest read.
func (c *Controller) readScreen(ctx context.Context, b Bridge, device string, p *phone, s *session, info DeviceInfo,
	deadline time.Time) (*Screen, error) {
	tree, err := read(ctx, deadline, c.limits.Primitive, func(ctx context.Context) (Tree, error) {
		return b.AXTree(ctx, device)
	})
	if err != nil {
		return nil, err
	}
	size := tree.Size
	if !size.known() {
		size = info.Size
	}
	scr := number(s.screen, snapshot{App: tree.App, Size: size, Elements: tree.Elements(), Locked: info.locked(),
		Truncated: tree.Truncated})
	p.set(func() { s.screen, s.read = scr, time.Now() })
	return scr, nil
}

// StepResult is what happened to one step.
type StepResult struct {
	Step   int    `json:"step"` // 1-based, within the do the model asked for
	Action string `json:"action"`
	// What is the step in words, as the model wrote it ("tap [4]", "type 12 characters").
	What     string      `json:"what"`
	Target   *ElementRef `json:"target,omitempty"`
	At       *Point      `json:"at,omitempty"` // where a touch landed
	Delivery Delivery    `json:"delivery"`
	// Effect is what the screen shows after an input that was sent: the phone does not confirm input, so this is
	// how a step is known to have done something.
	Effect Effect `json:"effect,omitempty"`
	// Detail is why a step was not sent or is unknown, and any fact the delivery alone doesn't say.
	Detail string `json:"detail,omitempty"`
	// Waiting: the step waits for the owner's approval; nothing of it was sent.
	Waiting  bool          `json:"waiting,omitempty"`
	Needs    []Sensitivity `json:"needs,omitempty"`
	Approved bool          `json:"approved,omitempty"` // the owner approved it before it was sent
	Elapsed  time.Duration `json:"elapsed"`
}

// DoResult is what a do did and the screen after it.
type DoResult struct {
	Steps   []StepResult  `json:"steps"`
	Screen  *Screen       `json:"screen,omitempty"`
	Changes Diff          `json:"changes"` // from the read the steps' numbers came from
	Elapsed time.Duration `json:"elapsed"`
	// ReadError is why there is no screen after the steps.
	ReadError string `json:"read_error,omitempty"`
	// Note is about the call as a whole: why nothing was resumed.
	Note string `json:"note,omitempty"`
	// refused is Response.Refused: the resume was turned down before anything was sent.
	refused string

	image      *Image
	approval   *Approval
	approvalID string
}

func (r DoResult) response() Response {
	resp := response(r, r.Text(), r.image)
	if r.approval != nil {
		resp.NeedsApproval, resp.Approval, resp.ApprovalID = true, r.approval, r.approvalID
	}
	resp.Refused = r.refused
	return resp
}

// run is a do to run: the model's steps, or the rest of one that waited for approval.
type run struct {
	steps      []Step
	offset     int // steps[0]'s index in the do the model asked for
	view       int // the numbering the steps' numbers come from; 0 takes the latest
	screenshot bool
	confirm    bool
	// approved is the fingerprint the owner approved for steps[0]; when it included Confirm, it confirms the rest
	// of the do too.
	approved string
	// confirmed: the owner already confirmed this do (Confirm) before it stopped again.
	confirmed bool
	// approvedShot is the screen the owner saw when asked, without an element tree.
	approvedShot sig
	// resume is the approval id of the pending do to run instead.
	resume string
}

func (r run) notSent(res *DoResult, from int, why string) {
	for i := from; i < len(r.steps); i++ {
		res.Steps = append(res.Steps, StepResult{Step: r.offset + i + 1, Action: r.steps[i].Action,
			What: describeStep(r.steps[i]), Delivery: DeliveryNotSent, Detail: why})
	}
}

// call is one do's state.
type call struct {
	deadline  time.Time
	reserve   time.Duration // kept for the read after the steps
	view      int           // the numbering the steps' numbers come from
	invalid   string        // why element numbers no longer mean what the steps meant, when they don't
	stale     bool          // the phone may have changed since the latest read
	info      DeviceInfo
	infoStale bool   // a button was pressed: the phone may have locked or unlocked
	noTree    bool   // the phone gives no element tree: steps act on points, unchecked
	confirmed bool   // the owner confirmed the rest of this do
	confirm   bool   // ask the owner before any input
	remaining []Step // the current step and those after it
	lastImg   *Image // the call's latest screenshot
	lastSig   sig    // and its signature
	dirty     bool   // an input was sent after lastImg
	bridge    Bridge
	device    string
	phone     *phone
	session   *session
}

// waiting is a step stopped for the owner's approval.
type waiting struct {
	approval Approval
	print    string
	shot     sig
}

func (c *Controller) do(ctx context.Context, b Bridge, device, sessionID string, r run) DoResult {
	start := time.Now()
	res := DoResult{}
	p := c.phone(device)
	if !p.acquire(ctx, c.limits.Busy) {
		if r.resume != "" {
			return DoResult{Note: "nothing was sent: " + busyNote + "; the approval is still pending", Elapsed: time.Since(start),
				refused: RefusedBusy}
		}
		r.notSent(&res, 0, "not attempted: "+busyNote)
		res.Elapsed = time.Since(start)
		return res
	}
	defer p.release()
	s := p.session(sessionID)
	if r.resume != "" {
		// Claimed only with the phone's token held, so a busy phone leaves the approval pending.
		var w *pending
		p.set(func() {
			if s.pending != nil && s.pending.id == r.resume {
				w, s.pending = s.pending, nil
			}
		})
		switch {
		case w == nil:
			return DoResult{Note: "nothing was sent: that approval is not pending (already used, replaced by a newer do, " +
				"or the node restarted)", Elapsed: time.Since(start), refused: RefusedNotPending}
		case time.Now().After(w.expires):
			return DoResult{Note: fmt.Sprintf("nothing was sent: the approval expired (steps wait at most %s)",
				round(c.limits.Pending)), Elapsed: time.Since(start), refused: RefusedNotPending}
		}
		r = run{steps: w.steps, offset: w.offset, view: w.view, screenshot: w.screenshot, confirm: w.confirm,
			approved: w.print, approvedShot: w.shot, confirmed: w.confirmed}
	} else {
		// A new do replaces whatever waited for approval: its id no longer resumes anything.
		p.set(func() { s.pending = nil })
	}
	notSent := func(from int, why string) { r.notSent(&res, from, why) }

	base := s.screen
	cl := &call{deadline: start.Add(c.limits.Call), reserve: min(c.limits.Primitive, c.limits.Call/4), stale: true,
		confirm: r.confirm, confirmed: r.confirmed, bridge: b, device: device, phone: p, session: s}
	switch {
	case base != nil:
		cl.view = base.Numbering
		if r.view != 0 && r.view != base.Numbering {
			cl.invalid = fmt.Sprintf("the element numbers are from numbering %d, the latest read has numbering %d",
				r.view, base.Numbering)
		}
	case usesNumbers(r.steps):
		cl.invalid = "there are no element numbers yet (none read, or the node restarted)"
	}
	info, err := read(ctx, cl.deadline, c.limits.Primitive, func(ctx context.Context) (DeviceInfo, error) {
		return b.DeviceInfo(ctx, device)
	})
	cl.info, cl.noTree = info, !info.can("ax_tree")
	if err != nil {
		notSent(0, "not attempted: the phone could not be reached: "+err.Error())
		res.Elapsed = time.Since(start)
		p.set(func() { s.last = "do: " + outcomes(res.Steps) })
		return res
	}

	for i, st := range r.steps {
		if ctx.Err() != nil {
			notSent(i, "not attempted: the call was given up")
			break
		}
		if time.Until(cl.deadline.Add(-cl.reserve)) <= 0 {
			notSent(i, "not attempted: the call's time limit was reached")
			break
		}
		cl.remaining = r.steps[i:]
		var approved resumed
		if i == 0 {
			approved = resumed{print: r.approved, shot: r.approvedShot}
		}
		sr, wait := c.step(ctx, cl, r.offset+i, st, approved)
		res.Steps = append(res.Steps, sr)
		if wait != nil {
			w := &pending{id: newID(), steps: r.steps[i:], offset: r.offset + i, view: cl.view, print: wait.print,
				shot: wait.shot, confirm: cl.confirm, confirmed: cl.confirmed, screenshot: r.screenshot,
				expires: time.Now().Add(c.limits.Pending)}
			p.set(func() { s.pending = w })
			res.approval, res.approvalID = &wait.approval, w.id
			notSent(i+1, fmt.Sprintf("not attempted: step %d waits for the owner's approval", r.offset+i+1))
			break
		}
		if sr.Delivery != DeliverySent && sr.Delivery != DeliveryDone {
			notSent(i+1, fmt.Sprintf("not attempted: step %d did not complete", r.offset+i+1))
			break
		}
	}
	if left := time.Until(cl.deadline); left > 0 {
		if cl.noTree {
			// No elements to list: the screenshot is what shows the steps' effect.
			r.screenshot, res.ReadError = true, noTreeNote(cl.info)
		} else {
			scr, err := c.readScreen(ctx, b, device, p, s, cl.info, cl.deadline)
			switch {
			case err != nil:
				res.ReadError = "screen not read after the steps: " + err.Error()
			case base != nil && scr.Numbering == base.Numbering:
				res.Screen, res.Changes = scr, diff(base, scr)
			default:
				res.Screen, res.Changes = scr, Diff{Replaced: true}
			}
		}
		switch {
		case (r.screenshot || res.approval != nil && cl.noTree) && cl.lastImg != nil && !cl.dirty:
			// The screenshot after the last input (or the one the owner is asked about) is the screen now.
			res.image = cl.lastImg
		case r.screenshot && time.Until(cl.deadline) > 0:
			img, err := read(ctx, cl.deadline, c.limits.Primitive, func(ctx context.Context) (Image, error) {
				return b.Screenshot(ctx, device)
			})
			if err == nil {
				res.image = &img
			}
		}
	} else {
		res.ReadError = "screen not read after the steps: the call's time limit was reached"
	}
	res.Elapsed = time.Since(start)
	p.set(func() { s.last = fmt.Sprintf("do: %s, %s", outcomes(res.Steps), round(res.Elapsed)) })
	return res
}

// resume runs the rest of a do the owner approved, if the id is the one pending and the step is still what they
// approved. The owner's confirm setting is the one the do stopped under.
func (c *Controller) resume(ctx context.Context, b Bridge, req Request) DoResult {
	return c.do(ctx, b, req.Device, req.Session, run{resume: req.Approved})
}

// outcomes sums up steps' deliveries for status ("2 sent, 1 unknown, 1 not_sent").
func outcomes(steps []StepResult) string {
	count := map[Delivery]int{}
	for _, r := range steps {
		count[r.Delivery]++
	}
	var parts []string
	for _, d := range []Delivery{DeliverySent, DeliveryDone, DeliveryUnknown, DeliveryNotSent} {
		if count[d] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count[d], d))
		}
	}
	if len(parts) == 0 {
		return "no steps"
	}
	return strings.Join(parts, ", ")
}

// StatusResult is the phone and this conversation's session now.
type StatusResult struct {
	Device    *DeviceInfo   `json:"device,omitempty"`
	Error     string        `json:"error,omitempty"` // why the device didn't answer
	Busy      bool          `json:"busy,omitempty"`  // a look or do is running on the phone
	Numbering int           `json:"numbering,omitempty"`
	Elements  int           `json:"elements,omitempty"`
	App       *App          `json:"app,omitempty"`
	ReadAgo   time.Duration `json:"read_ago,omitempty"`
	Waiting   bool          `json:"waiting,omitempty"` // a step waits for the owner's approval
	// Stuck is how long an input has been out without an answer, holding back the next ones.
	Stuck   time.Duration `json:"stuck,omitempty"`
	Last    string        `json:"last,omitempty"`
	Elapsed time.Duration `json:"elapsed"`
}

// status asks the phone how it is and adds what the session knows; it doesn't wait for a running call.
func (c *Controller) status(ctx context.Context, b Bridge, device, sessionID string) StatusResult {
	start := time.Now()
	p := c.phone(device)
	s := p.session(sessionID)
	res := StatusResult{Busy: len(p.calls) > 0, Stuck: p.stuck(c.limits.Stuck)}
	info, err := read(ctx, start.Add(c.limits.Call), c.limits.Primitive, func(ctx context.Context) (DeviceInfo, error) {
		return b.DeviceInfo(ctx, device)
	})
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Device = &info
	}
	p.set(func() {
		if s.screen != nil {
			app := s.screen.App
			res.Numbering, res.Elements, res.App, res.ReadAgo = s.screen.Numbering, len(s.screen.Elements), &app, time.Since(s.read)
		}
		res.Waiting = s.pending != nil && time.Now().Before(s.pending.expires)
		res.Last = s.last
	})
	res.Elapsed = time.Since(start)
	return res
}
