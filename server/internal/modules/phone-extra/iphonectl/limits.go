package iphonectl

import (
	"context"
	"errors"
	"time"
)

// Limits bound how long anything may take, so a call always answers: at a limit the controller stops waiting,
// reports the step that didn't finish and whether its input may have reached the phone, and goes on to answer.
type Limits struct {
	// Primitive is one bridge call, plus the time its input itself takes (a long press, a swipe, a drag).
	Primitive time.Duration
	// Action is one step, which may be several bridge calls (read the screen to find the target, tap it, type), plus
	// its input time.
	Action time.Duration
	// Call is a whole look, do or status.
	Call time.Duration
	// Busy is how long a call waits for the call before it on the same phone; that call is itself bounded.
	Busy time.Duration
	// Pending is how long a step waiting for the owner's approval stays pending; after it the approval is refused.
	Pending time.Duration
	// Stuck is how long an input still out past its limit holds back the next ones; after it the bridge call counts
	// as dead.
	Stuck time.Duration
	// Settle is how long after an input the screen is let settle before the screenshot that checks its effect.
	Settle time.Duration
}

// DefaultLimits are the limits a zero Limits field falls back to.
var DefaultLimits = Limits{
	Primitive: 15 * time.Second,
	Action:    30 * time.Second,
	Call:      90 * time.Second,
	Busy:      10 * time.Second,
	Pending:   15 * time.Minute,
	Stuck:     2 * time.Minute,
	Settle:    400 * time.Millisecond,
}

func (l Limits) withDefaults() Limits {
	or := func(v, d time.Duration) time.Duration {
		if v > 0 {
			return v
		}
		return d
	}
	return Limits{
		Primitive: or(l.Primitive, DefaultLimits.Primitive),
		Action:    or(l.Action, DefaultLimits.Action),
		Call:      or(l.Call, DefaultLimits.Call),
		Busy:      or(l.Busy, DefaultLimits.Busy),
		Pending:   or(l.Pending, DefaultLimits.Pending),
		Stuck:     or(l.Stuck, DefaultLimits.Stuck),
		Settle:    or(l.Settle, DefaultLimits.Settle),
	}
}

// Delivery is whether a step's input reached the phone.
type Delivery string

const (
	DeliverySent    Delivery = "sent"     // the phone took it
	DeliveryNotSent Delivery = "not_sent" // it certainly did not reach the phone
	DeliveryUnknown Delivery = "unknown"  // no clear answer: it may have reached the phone, so it is never sent again
	DeliveryDone    Delivery = "done"     // a step with no input (wait) finished
)

// errTimeout is what bounded returns when the limit (or the caller's context) ended the wait.
var errTimeout = errors.New("no answer in time")

// bounded runs fn with a context that ends after limit and returns at the latest then, even when fn ignores its
// context: fn keeps running in its goroutine and its late answer, value included, is dropped. timedOut says the
// wait ended without an answer.
func bounded(ctx context.Context, limit time.Duration, fn func(context.Context) error) (err error, timedOut bool) {
	_, err, timedOut = boundedValue(ctx, limit, func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) })
	return err, timedOut
}

// boundedValue is bounded for a call with a value, which comes back only with an answer in time.
func boundedValue[T any](ctx context.Context, limit time.Duration, fn func(context.Context) (T, error)) (v T, err error, timedOut bool) {
	if limit <= 0 {
		return v, errTimeout, true
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	type answer struct {
		v   T
		err error
	}
	done := make(chan answer, 1)
	go func() {
		v, err := fn(ctx)
		done <- answer{v, err}
	}()
	select {
	case a := <-done:
		if a.err != nil && ctx.Err() != nil && errors.Is(a.err, ctx.Err()) {
			return v, errTimeout, true
		}
		return a.v, a.err, false
	case <-ctx.Done():
		return v, errTimeout, true
	}
}

// delivery is what an input's answer says about whether it reached the phone: only a refusal before sending is
// not_sent; a timeout, a dropped link or any other failure may have come after the input was written.
func delivery(err error, timedOut bool) Delivery {
	switch {
	case timedOut:
		return DeliveryUnknown
	case err == nil:
		return DeliverySent
	case refused(err):
		return DeliveryNotSent
	}
	return DeliveryUnknown
}
