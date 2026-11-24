package remoteimage

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	"image/png"
	"net/http"
	"slices"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/nwverse/persona/server/internal/imageorient"
)

// sniff is the type of data from its first bytes, and whether the hub serves it: JPEG, PNG, GIF and WebP (decoded),
// HEIC and AVIF (passed as they are). SVG, HTML and everything else are not.
func sniff(data []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg", true
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif", true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", true
	}
	if t := isoImage(data); t != "" {
		return t, true
	}
	return "", false
}

// isoImage is "image/avif" or "image/heic" for an ISO base media file whose ftyp box names one of their brands (the
// major brand or a compatible one), else "".
func isoImage(data []byte) string {
	if len(data) < 16 || string(data[4:8]) != "ftyp" {
		return ""
	}
	n := int(binary.BigEndian.Uint32(data[:4]))
	if n < 16 || n > len(data) || n > 4096 {
		return ""
	}
	brands := [][]byte{data[8:12]}
	for i := 16; i+4 <= n; i += 4 {
		brands = append(brands, data[i:i+4])
	}
	is := func(names ...string) bool {
		return slices.ContainsFunc(brands, func(b []byte) bool { return slices.Contains(names, string(b)) })
	}
	switch {
	case is("avif", "avis"):
		return "image/avif"
	case is("heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1"):
		return "image/heic"
	}
	return ""
}

// render draws src to fit size × size under p: decoded, upright, scaled down (never up) and encoded again. A HEIC or
// AVIF goes as it is where p passes them.
func (p Profile) render(src []byte, size int) (*Image, error) {
	pic, passed, err := p.decode(src)
	if err != nil || passed != nil {
		return passed, err
	}
	return encode(pic, size)
}

// decode is src decoded and upright under p, or, for a HEIC or AVIF p passes, src as it is (passed).
func (p Profile) decode(src []byte) (pic image.Image, passed *Image, err error) {
	kind, _ := sniff(src)
	if !p.served(kind) {
		return nil, nil, p.unsupported()
	}
	if kind == "image/heic" || kind == "image/avif" {
		if len(src) > p.MaxPassThrough {
			return nil, nil, fail(CodeTooLarge, http.StatusRequestEntityTooLarge, "%s of %d bytes, more than %d", kind, len(src), p.MaxPassThrough)
		}
		return nil, &Image{Type: kind, Data: src}, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, nil, fail(CodeBadImage, http.StatusUnprocessableEntity, "%s does not decode", kind)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > p.MaxPixels {
		return nil, nil, fail(CodeTooLarge, http.StatusRequestEntityTooLarge, "%d×%d pixels, more than %d", cfg.Width, cfg.Height, p.MaxPixels)
	}
	pic, _, err = image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, nil, fail(CodeBadImage, http.StatusUnprocessableEntity, "%s does not decode", kind)
	}
	if kind == "image/jpeg" {
		if o := imageorient.JPEG(bytes.NewReader(src), int64(len(src))); o >= 2 && o <= 8 {
			pic = imageorient.Image{Image: pic, Orientation: o}
		}
	}
	return pic, nil, nil
}

// fit is a picture someone sent (a channel's photo sent as the original, #11985) made to hold at most most bytes:
// decoded once under Pictures and drawn at the largest of Sizes whose encoding fits. A HEIC or AVIF is kept as it is
// when it fits. An error is an *Error: the type is not one the hub takes, it does not decode, or no size fits.
// Callers go through Fetcher.Fit, which holds a decode slot while it runs.
func fit(src []byte, most int) (*Image, error) {
	pic, passed, err := Pictures.decode(src)
	if err != nil {
		return nil, err
	}
	if passed != nil {
		if len(passed.Data) > most {
			return nil, fail(CodeTooLarge, http.StatusRequestEntityTooLarge, "%s of %d bytes, more than %d", passed.Type, len(passed.Data), most)
		}
		return passed, nil
	}
	for _, size := range slices.Backward(Pictures.Sizes) {
		img, err := encode(pic, size)
		if err != nil {
			return nil, err
		}
		if len(img.Data) <= most {
			return img, nil
		}
	}
	return nil, fail(CodeTooLarge, http.StatusRequestEntityTooLarge, "no size of it fits in %d bytes", most)
}

// encode fits pic into size × size (never enlarging): JPEG when the source is opaque, PNG to keep transparency.
func encode(pic image.Image, size int) (*Image, error) {
	o, ok := pic.(interface{ Opaque() bool })
	opaque := ok && o.Opaque() // of the source: scaling may leave an edge pixel a hair short of opaque
	b := pic.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > size || h > size {
		if w >= h {
			w, h = size, max(1, h*size/b.Dx())
		} else {
			w, h = max(1, w*size/b.Dy()), size
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), pic, b, draw.Src, nil)
		pic = dst
	}
	var buf bytes.Buffer
	if opaque {
		if err := jpeg.Encode(&buf, pic, &jpeg.Options{Quality: 82}); err != nil {
			return nil, fail(CodeBadImage, http.StatusUnprocessableEntity, "encode: %v", err)
		}
		return &Image{Type: "image/jpeg", Data: buf.Bytes(), Width: w, Height: h}, nil
	}
	if err := png.Encode(&buf, pic); err != nil {
		return nil, fail(CodeBadImage, http.StatusUnprocessableEntity, "encode: %v", err)
	}
	return &Image{Type: "image/png", Data: buf.Bytes(), Width: w, Height: h}, nil
}
