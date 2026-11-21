package remoteimage

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"testing"
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
	img, err := Fit(src, most)
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
	img, err := Fit(src, len(src)-1)
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
	if img, err := Fit(heic, len(heic)); err != nil || img.Type != "image/heic" || !bytes.Equal(img.Data, heic) {
		t.Fatalf("got %+v, %v", img, err)
	}
	if _, err := Fit(heic, len(heic)-1); code(err) != CodeTooLarge {
		t.Fatalf("a HEIC over the limit: %v", err)
	}
}

// What isn't a picture, or would decode into too many pixels, is refused.
func TestFitRefuses(t *testing.T) {
	if _, err := Fit([]byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), 4); code(err) != CodeUnsupported {
		t.Fatalf("svg: %v", err)
	}
	if _, err := Fit(hugePNG(t, 20000, 20000), 4); code(err) != CodeTooLarge {
		t.Fatalf("400 megapixels: %v", err)
	}
}

func mustDecode(t *testing.T, src []byte) image.Image {
	t.Helper()
	pic, passed, err := Pictures.decode(src)
	if err != nil || passed != nil {
		t.Fatalf("decode: %v", err)
	}
	return pic
}
