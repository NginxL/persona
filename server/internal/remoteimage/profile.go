package remoteimage

import (
	"net/http"
	"time"
)

// Profile is what a Fetcher takes and makes: which addresses, which types, how large, how long, which sizes it draws
// and how many fetches one caller may start. Pictures is /remote-image's (the default); Avatars is the avatars route's
// (#10923, docs/protocol/protocol.md "Avatars"), stricter: https only, the four types the hub decodes, small sizes.
type Profile struct {
	// Name keeps two profiles' cache keys apart ("" for Pictures, whose keys stay as they were).
	Name string
	// HTTPSOnly refuses http:// addresses, at the start and on every redirect.
	HTTPSOnly bool
	// PassThrough serves HEIC and AVIF as they are (Go decodes neither), up to MaxPassThrough bytes; without it they
	// are refused like any other type the hub cannot decode.
	PassThrough    bool
	MaxPassThrough int
	// MaxSource is the most bytes of a source (Content-Length checked first, then the body capped), MaxPixels the most
	// pixels it may decode to, Timeout the longest one fetch may take.
	MaxSource int
	MaxPixels int64
	Timeout   time.Duration
	// Sizes are the long sides (px) it draws, smallest first: a request is rounded up to one (Snap), DefaultSize when
	// it names none.
	Sizes       []int
	DefaultSize int
	// PerCaller is how many fetches one caller may start a minute. Only fetches count: a picture from the cache, or
	// a size drawn from a source already kept, costs nothing.
	PerCaller int
}

// Pictures is /remote-image's profile: the pictures a reply names by address.
var Pictures = Profile{PassThrough: true, MaxPassThrough: maxPassThrough, MaxSource: maxSource, MaxPixels: maxPixels,
	Timeout: fetchTimeout, Sizes: Sizes, DefaultSize: DefaultSize, PerCaller: perCaller}

// Avatars is the avatars route's profile: a person's or a conversation's picture, drawn at a row's size. A screen of
// rows asks for tens of them at once, all new the first time: PerCaller lets a few screens of them through a minute.
var Avatars = Profile{Name: "avatar", HTTPSOnly: true, MaxSource: 4 << 20, MaxPixels: 16_000_000, Timeout: 10 * time.Second,
	Sizes: []int{64, 128, 256, 512}, DefaultSize: 128, PerCaller: 300}

// Snap is the size p serves for a requested one: the smallest of its Sizes at least as large (the largest beyond
// them), its DefaultSize for 0 or less.
func (p Profile) Snap(size int) int {
	if size <= 0 {
		return p.DefaultSize
	}
	for _, s := range p.Sizes {
		if size <= s {
			return s
		}
	}
	return p.Sizes[len(p.Sizes)-1]
}

// served says whether p serves a picture of type kind (sniff's): the decoded ones always, HEIC and AVIF only passed
// through.
func (p Profile) served(kind string) bool {
	switch kind {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	case "image/heic", "image/avif":
		return p.PassThrough
	}
	return false
}

// unsupported is the refusal of a type p does not serve.
func (p Profile) unsupported() *Error {
	if p.PassThrough {
		return fail(CodeUnsupported, http.StatusUnsupportedMediaType, "not a JPEG, PNG, GIF, WebP, HEIC or AVIF image")
	}
	return fail(CodeUnsupported, http.StatusUnsupportedMediaType, "not a JPEG, PNG, GIF or WebP image")
}
