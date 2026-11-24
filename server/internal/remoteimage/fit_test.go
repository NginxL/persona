package remoteimage

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"testing"
	"time"
)

// noisePNG is a w × h PNG of random opaque pixels: it compresses badly, so it is large for its size.
func noisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(r.Uint32())
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 0xff
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A large photo comes back under the limit at the largest size that fits, its shape kept (#11985).
func TestFitScalesDownToTheLargestSizeThatFits(t *testing.T) {
	src := noisePNG(t, 2600, 1300)
	most := 1 << 20
	if len(src) <= most {
		t.Fatalf("the source is only %d bytes", len(src))
	}
	img, err := fit(src, most)
	if err != nil || img.Type != "image/jpeg" || len(img.Data) > most {
		t.Fatalf("got %+v, %v", img, err)
	}
	if img.Width > 2048 || img.Width != 2*img.Height {
		t.Fatalf("drawn %d×%d", img.Width, img.Height)
	}
	if bigger, err := encode(mustDecode(t, src), Pictures.Snap(img.Width+1)); err == nil && img.Width < 2048 && len(bigger.Data) <= most {
		t.Fatalf("drawn at %d although %d fits too", img.Width, bigger.Width)
	}
}

// A JPEG taken sideways (EXIF orientation 6) comes back upright.
func TestFitTurnsAPhotoUpright(t *testing.T) {
	src := withOrientation(jpegBytes(t, 4000, 1000), 6)
	img, err := fit(src, len(src)-1)
	if err != nil {
		t.Fatal(err)
	}
	if img.Width >= img.Height {
		t.Fatalf("drawn %d×%d, want it tall", img.Width, img.Height)
	}
	if _, err := jpeg.Decode(bytes.NewReader(img.Data)); err != nil {
		t.Fatal(err)
	}
}

// A HEIC is kept as it is when it fits, and refused when it does not: the hub can't scale it.
func TestFitPassesHEICOnlyWhenItFits(t *testing.T) {
	heic := heicBytes()
	if img, err := fit(heic, len(heic)); err != nil || img.Type != "image/heic" || !bytes.Equal(img.Data, heic) {
		t.Fatalf("got %+v, %v", img, err)
	}
	if _, err := fit(heic, len(heic)-1); code(err) != CodeTooLarge {
		t.Fatalf("a HEIC over the limit: %v", err)
	}
}

// What isn't a picture, or would decode into too many pixels, is refused.
func TestFitRefuses(t *testing.T) {
	if _, err := fit([]byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), 4); code(err) != CodeUnsupported {
		t.Fatalf("svg: %v", err)
	}
	if _, err := fit(hugePNG(t, 20000, 20000), 4); code(err) != CodeTooLarge {
		t.Fatalf("400 megapixels: %v", err)
	}
}

// Channel-picture fitting waits for the same two decode slots as remote-image rendering and gives its slot back on
// return (#11985). Reserving the Fetcher's slots directly keeps the scheduling exact; the deadline is only an upper bound.
func TestFitSharesRemoteImageDecodeSlots(t *testing.T) {
	f := New(Options{})
	for range decodeSlots {
		f.decodes <- struct{}{} // two remote-image decodes are in flight
	}
	drain := func() {
		for {
			select {
			case <-f.decodes:
			default:
				return
			}
		}
	}
	defer drain()

	src := noisePNG(t, 1100, 550)
	type result struct {
		img *Image
		err error
	}
	started := make(chan struct{})
	done := make(chan result, 1)
	go func() {
		close(started)
		img, err := f.Fit(t.Context(), src, 1<<20)
		done <- result{img, err}
	}()
	<-started
	select {
	case r := <-done:
		if r.img == nil {
			t.Fatalf("Fit returned (nil, %v) while both remote-image decode slots were occupied", r.err)
		}
		t.Fatalf("Fit returned (%s %d×%d, %d bytes, %v) while both remote-image decode slots were occupied",
			r.img.Type, r.img.Width, r.img.Height, len(r.img.Data), r.err)
	case <-time.After(250 * time.Millisecond):
	}

	<-f.decodes // one remote-image decode finishes; the queued channel picture may now run
	select {
	case r := <-done:
		if r.err != nil || r.img == nil {
			t.Fatalf("Fit after a slot was released: image=%t, %v", r.img != nil, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Fit stayed blocked after a remote-image decode slot was released")
	}
	if got := len(f.decodes); got != 1 {
		t.Fatalf("Fit returned with %d slots occupied, want only the remaining remote-image decode", got)
	}
	<-f.decodes
}

func mustDecode(t *testing.T, src []byte) image.Image {
	t.Helper()
	pic, passed, err := Pictures.decode(src)
	if err != nil || passed != nil {
		t.Fatalf("decode: %v", err)
	}
	return pic
}

// Fetcher.Fit decodes within the decode slots the fetches share: with every slot taken it waits, and a wait ends with
// its context; a freed slot lets it run (review of #12167).
func TestFetcherFitWaitsForADecodeSlot(t *testing.T) {
	f := New(Options{})
	for range cap(f.decodes) {
		f.decodes <- struct{}{}
	}
	src := noisePNG(t, 1100, 1100)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := f.Fit(ctx, src, len(src)/2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("with every slot taken: %v", err)
	}
	<-f.decodes
	img, err := f.Fit(t.Context(), src, len(src)/2)
	if err != nil || len(img.Data) > len(src)/2 {
		t.Fatalf("with a slot free: %v", err)
	}
	if len(f.decodes) != cap(f.decodes)-1 {
		t.Fatalf("Fit kept its slot: %d of %d taken", len(f.decodes), cap(f.decodes))
	}
}
