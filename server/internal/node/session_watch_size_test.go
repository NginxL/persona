package node

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// screenshotLine is a Claude Code user line holding one image of size bytes (before base64), as a computer-use session
// writes for every screenshot.
func screenshotLine(i, size int) string {
	data := base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(rune('a'+i%26)), size)))
	return fmt.Sprintf(`{"type":"user","uuid":"img%d","timestamp":"2026-11-23T09:00:%02dZ","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}},{"type":"text","text":"shot %d"}]}}`, i, i%60, data, i)
}

func imageSession(t *testing.T, n *Node, home, id string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	n.paths[id] = path
	return path
}

// A watch's catch-up of a session full of screenshots carries no image data (#13677): every image is a placeholder
// the apps fetch when they show it, every push stays near pushBytesMax, and the pushes still cover every line.
func TestWatchCatchUpLeavesImagesOut(t *testing.T) {
	n, home := testNode(t)
	const id = "13677000-0000-4000-8000-000000000001"
	path := imageSession(t, n, home, id)
	appendLines(t, path, `{"type":"user","uuid":"u0","timestamp":"2026-11-23T09:00:00Z","message":{"role":"user","content":"start"}}`)
	f, err := n.file(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(); err != nil { // the hub has seen the first line
		t.Fatal(err)
	}
	var lines []string
	for i := range 20 {
		lines = append(lines, screenshotLine(i, 200_000)) // 20 × 267 KB of base64: 5.3 MB, as in #13677
	}
	appendLines(t, path, lines...)
	hub := withHub(t, n)
	if err := n.watch(id, 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.unwatch(id) })
	waitFor(t, "the catch-up", func() bool {
		s := hub.spans()
		return len(s) > 0 && s[len(s)-1].toSeq == 21
	})
	images, imageID := 0, ""
	for _, it := range hub.got() {
		if it.Image == nil {
			continue
		}
		images++
		imageID = it.ID
		if it.Image.Data != "" || !it.Image.Lazy || it.Image.Size/1000 != 200 {
			t.Fatalf("item %s: image pushed with %d bytes of data (lazy=%t size=%d)", it.ID, len(it.Image.Data), it.Image.Lazy, it.Image.Size)
		}
	}
	if images != 20 {
		t.Fatalf("pushed %d images, want 20", images)
	}
	// The placeholder's data is what session.image reads back.
	var img proto.Image
	raw, err := n.handle(t.Context(), "session.image", json.RawMessage(fmt.Sprintf(`{"session_id":%q,"item_id":%q}`, id, imageID)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(raw)
	if json.Unmarshal(b, &img) != nil || len(img.Data) < 200_000 {
		t.Fatalf("session.image gave %d bytes", len(img.Data))
	}
}

// A live push carries a small image whole and a large one as a placeholder.
func TestLivePushKeepsOnlySmallImages(t *testing.T) {
	n, home := testNode(t)
	const id = "13677000-0000-4000-8000-000000000002"
	path := imageSession(t, n, home, id)
	appendLines(t, path, `{"type":"user","uuid":"u0","timestamp":"2026-11-23T09:00:00Z","message":{"role":"user","content":"start"}}`)
	hub := withHub(t, n)
	if err := n.watch(id, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.unwatch(id) })
	appendLines(t, path, screenshotLine(1, 20_000), screenshotLine(2, 400_000))
	waitFor(t, "both screenshots", func() bool {
		count := 0
		for _, it := range hub.got() {
			if it.Image != nil {
				count++
			}
		}
		return count == 2
	})
	var got []*proto.Image
	for _, it := range hub.got() {
		if it.Image != nil {
			got = append(got, it.Image)
		}
	}
	if len(got[0].Data) == 0 || got[0].Lazy {
		t.Fatalf("the 20 KB image went as a placeholder: %+v", *got[0])
	}
	if got[1].Data != "" || !got[1].Lazy || got[1].Size/1000 != 400 {
		t.Fatalf("the 400 KB image went with %d bytes of data", len(got[1].Data))
	}
}

// A batch of new lines carries at most pushImagesMax of image data, the newest images first.
func TestPushImagesBudget(t *testing.T) {
	data := strings.Repeat("A", 100<<10)
	var items []proto.Item
	for i := range 5 {
		items = append(items, proto.Item{ID: fmt.Sprintf("i%d", i), Seq: i + 1, Image: &proto.Image{Mime: "image/png", Data: data}})
	}
	out := pushImages(items)
	for i, it := range out {
		whole := it.Image.Data != ""
		if want := i >= 3; whole != want { // 2 × 100 KiB fit in 256 KiB: the newest two
			t.Fatalf("image %d whole=%t", i, whole)
		}
	}
	if items[0].Image.Data == "" {
		t.Fatal("pushImages changed its input")
	}
}

// pushParts cuts at whole lines, each part's span continuing the last, and keeps an empty batch whole.
func TestPushParts(t *testing.T) {
	old := pushBytesMax
	pushBytesMax = 1000
	t.Cleanup(func() { pushBytesMax = old })
	big := strings.Repeat("x", 600)
	items := []proto.Item{
		{ID: "a", Seq: 3, Text: big}, {ID: "b", Seq: 3, Text: big}, // one line, never cut apart
		{ID: "c", Seq: 4, Text: big},
		{ID: "d", Seq: 6, Text: "short"},
	}
	parts := pushParts(items, transcript.Span{History: "h", From: 2, To: 7})
	if len(parts) != 2 {
		t.Fatalf("%d parts: %+v", len(parts), parts)
	}
	if len(parts[0].items) != 2 || parts[0].span.From != 2 || parts[0].span.To != 3 || parts[0].span.History != "h" {
		t.Fatalf("first part %+v", parts[0])
	}
	if len(parts[1].items) != 2 || parts[1].span.From != 3 || parts[1].span.To != 7 {
		t.Fatalf("second part %+v", parts[1])
	}
	if p := pushParts(nil, transcript.Span{From: 5, To: 9}); len(p) != 1 || p[0].span.From != 5 || p[0].span.To != 9 {
		t.Fatalf("empty batch: %+v", p)
	}
}

// A read of a page does not wait for a watch's catch-up still pushing (#13677): on a slow link that was the whole
// catch-up, past the hub's 30 s for the read.
func TestPageReadDoesNotWaitForTheCatchUp(t *testing.T) {
	n, home := testNode(t)
	const id = "13677000-0000-4000-8000-000000000003"
	path := imageSession(t, n, home, id)
	appendLines(t, path,
		`{"type":"user","uuid":"u0","timestamp":"2026-11-23T09:00:00Z","message":{"role":"user","content":"start"}}`,
		`{"type":"assistant","uuid":"a0","timestamp":"2026-11-23T09:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"on it"}]}}`)
	f, err := n.file(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(); err != nil { // the watch read the file before it answered
		t.Fatal(err)
	}
	mu := n.pushLock(id)
	mu.Lock() // and its catch-up is pushing
	defer mu.Unlock()
	done := make(chan any, 1)
	go func() {
		res, err := n.handle(t.Context(), "session.items", json.RawMessage(fmt.Sprintf(`{"session_id":%q,"turns":2,"lazy_images":true}`, id)))
		if err != nil {
			done <- err
			return
		}
		done <- res
	}()
	select {
	case res := <-done:
		if err, ok := res.(error); ok {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res)
		if !strings.Contains(string(b), "on it") {
			t.Fatalf("page: %s", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the page read waited for the push lock")
	}
}
