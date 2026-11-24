package db

import (
	"context"
	"testing"
	"time"
)

// Quick capture (#13502): one capture per client id, so a device's retry is delivered once; the work loop takes up new
// ones and sorted ones still to be filed, and never overwrites a status it does not own.
func TestCaptures(t *testing.T) {
	d, ctx := open(t), context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	first, created, err := d.InsertCapture(ctx, Capture{ID: "cap_qa1", ClientID: "qa-client-1", Source: "watch", Kind: "text", Text: "qa: a placeholder note"})
	must(err)
	if !created || first.ID != "cap_qa1" || first.Status != "new" || string(first.Sort) != "{}" || first.Attempts != 0 {
		t.Fatalf("first = %+v, created %v", first, created)
	}
	again, created, err := d.InsertCapture(ctx, Capture{ID: "cap_qa2", ClientID: "qa-client-1", Source: "watch", Kind: "text", Text: "qa: the retry"})
	must(err)
	if created || again.ID != "cap_qa1" || again.Text != "qa: a placeholder note" {
		t.Fatalf("a retry = %+v, created %v: the first capture, once", again, created)
	}
	_, _, err = d.InsertCapture(ctx, Capture{ID: "cap_qa3", ClientID: "qa-client-3", Source: "web", Kind: "url", URL: "https://example.com/qa"})
	must(err)

	todo, err := d.CapturesToSort(ctx, 3, 10)
	must(err)
	if len(todo) != 2 || todo[0].ID != "cap_qa1" || todo[1].ID != "cap_qa3" {
		t.Fatalf("to sort = %+v", todo)
	}
	// Sorted to be filed on its own: still taken up until it is filed.
	changed, err := d.SetCaptureResult(ctx, "cap_qa1", "sorted", []byte(`{"track":"qa-plain","auto_file":true}`), nil, "qa: node offline", true)
	must(err)
	// Sorted and left for the user: no longer taken up.
	_, err = d.SetCaptureResult(ctx, "cap_qa3", "sorted", []byte(`{"track":"qa-plain","confidence":0.4}`), nil, "", true)
	must(err)
	if !changed {
		t.Fatal("a new capture's result was not kept")
	}
	todo, err = d.CapturesToSort(ctx, 3, 10)
	must(err)
	if len(todo) != 1 || todo[0].ID != "cap_qa1" || todo[0].Attempts != 1 || todo[0].Error != "qa: node offline" {
		t.Fatalf("to sort after one try = %+v", todo)
	}
	// A newer new capture comes ahead of an older one waiting to be filed, which counts no try while it waits.
	_, _, err = d.InsertCapture(ctx, Capture{ID: "cap_qa4", ClientID: "qa-client-4", Source: "web", Kind: "text", Text: "qa: a later note"})
	must(err)
	if todo, err = d.CapturesToSort(ctx, 3, 1); err != nil || len(todo) != 1 || todo[0].ID != "cap_qa4" {
		t.Fatalf("a batch of one = %+v, %v: the new capture first", todo, err)
	}
	_, err = d.SetCaptureResult(ctx, "cap_qa4", "sorted", []byte(`{"track":"qa-plain","confidence":0.4}`), nil, "", true)
	must(err)
	if todo, err = d.CapturesToSort(ctx, 1, 10); err != nil || len(todo) != 0 {
		t.Fatalf("past its tries = %+v, %v", todo, err)
	}
	_, err = d.SetCaptureResult(ctx, "cap_qa1", "filed", nil, []byte(`{"kind":"space_file","ref":"inbox/qa.md"}`), "", false)
	must(err)
	got, err := d.Captures(ctx, CaptureFilter{Statuses: []string{"filed"}})
	must(err)
	if len(got) != 1 || got[0].ID != "cap_qa1" || string(got[0].FiledTo) == "{}" || string(got[0].Sort) == "{}" || got[0].Error != "" {
		t.Fatalf("filed = %+v", got)
	}
	// A filed capture is the loop's no more.
	if changed, err := d.SetCaptureResult(ctx, "cap_qa1", "sorted", nil, nil, "", true); err != nil || changed {
		t.Fatalf("a filed capture changed: %v, %v", changed, err)
	}
	all, err := d.Captures(ctx, CaptureFilter{})
	must(err)
	if len(all) != 3 {
		t.Fatalf("all = %+v", all)
	}
	if later, err := d.Captures(ctx, CaptureFilter{UpdatedSince: time.Now().Add(time.Hour)}); err != nil || len(later) != 0 {
		t.Fatalf("changed in the future = %+v, %v", later, err)
	}
}
