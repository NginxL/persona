package iphonectl

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBoundedAnswersAtTheLimitWhenTheCallIgnoresItsContext(t *testing.T) {
	stuck := make(chan struct{})
	defer close(stuck)
	start := time.Now()
	err, timedOut := bounded(context.Background(), 40*time.Millisecond, func(context.Context) error {
		<-stuck
		return nil
	})
	if !timedOut || !errors.Is(err, errTimeout) {
		t.Fatalf("err %v timedOut %v", err, timedOut)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("waited %s past a 40ms limit", d)
	}
}

func TestBoundedPassesAnAnswerThrough(t *testing.T) {
	boom := errors.New("boom")
	if err, timedOut := bounded(context.Background(), time.Second, func(context.Context) error { return boom }); err != boom || timedOut {
		t.Fatalf("err %v timedOut %v", err, timedOut)
	}
	// A call that gives up at its deadline counts as timed out, not as its own error.
	err, timedOut := bounded(context.Background(), 20*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !timedOut || !errors.Is(err, errTimeout) {
		t.Fatalf("err %v timedOut %v", err, timedOut)
	}
}

func TestDelivery(t *testing.T) {
	for _, c := range []struct {
		err      error
		timedOut bool
		want     Delivery
	}{
		{nil, false, DeliverySent},
		{&Error{Code: CodeUnreachable}, false, DeliveryNotSent},
		{&Error{Code: CodeLocked}, false, DeliveryNotSent},
		{&Error{Code: CodeUnsupported}, false, DeliveryNotSent},
		{&Error{Code: CodeDisconnected}, false, DeliveryUnknown},
		{&Error{Code: CodeTimeout}, false, DeliveryUnknown},
		{&Error{Code: CodeInternal}, false, DeliveryUnknown},
		{errors.New("connection reset"), false, DeliveryUnknown},
		{errTimeout, true, DeliveryUnknown},
	} {
		if got := delivery(c.err, c.timedOut); got != c.want {
			t.Errorf("delivery(%v, %v) = %s, want %s", c.err, c.timedOut, got, c.want)
		}
	}
}

func TestZeroLimitsFallBackToTheDefaults(t *testing.T) {
	l := Limits{Call: time.Second}.withDefaults()
	if l.Call != time.Second || l.Primitive != DefaultLimits.Primitive || l.Pending != DefaultLimits.Pending {
		t.Fatalf("%+v", l)
	}
}

func TestDeliveryTakesTheCallersWordOverTheCode(t *testing.T) {
	for _, c := range []struct {
		err  error
		want Delivery
	}{
		{&Error{Code: CodeLocked}, DeliveryNotSent},
		{&Error{Code: "not_paired"}, DeliveryUnknown},
		{&Error{Code: "not_paired", NotSent: true}, DeliveryNotSent},
		{&Error{Code: CodeLocked, MaybeSent: true}, DeliveryUnknown},
		{&Error{Code: CodeLocked, NotSent: true, MaybeSent: true}, DeliveryUnknown},
	} {
		if got := delivery(c.err, false); got != c.want {
			t.Errorf("%+v: %s, want %s", c.err, got, c.want)
		}
	}
}
