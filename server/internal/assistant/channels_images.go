package assistant

import (
	"context"
	"net/url"
	"strings"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Pictures in an answer to a channel that sends them (#11986, docs/extensions/channels.md "Capabilities"): a picture
// on a line of its own (the block images the apps draw, docs/assistant/assistant.md "Pictures in replies (#9473)") is
// taken out of the text and sent as a picture of the chat app. It is fetched as the apps' pictures are, through
// remoteimage (the open internet only, checked and scaled), so a channel never makes the manager fetch what the
// apps would not. Inline pictures, pictures in code and ones past ImagesMost stay in the text; a picture that can't
// be fetched becomes a line naming it with its address, so it is not lost.

// channelImageSides are the long sides a picture a channel sends is drawn at, the first whose picture fits ImageMost:
// as large as a remote image gets, then smaller (each drawn from the source remoteimage keeps).
var channelImageSides = []int{2048, 1024, 512}

// markdownImage is one Markdown image: ![alt](address "title"), the address optionally in <>.
var markdownImage = lazyre.New(`!\[([^\]\n]*)\]\(\s*<?([^\s()<>]+)>?(?:\s+"[^"\n]*")?\s*\)`)

// blockImage is one picture of an answer's block-image line.
type blockImage struct{ alt, address string }

// channelImages takes the pictures on lines of their own out of an answer to a channel that sends them, fetched and
// fitted to the channel's limits. text is the answer without them, a picture that could not be fetched named in its
// place.
func (s *Service) channelImages(ctx context.Context, id, text string) (string, []channel.Blob) {
	lines := strings.Split(text, "\n")
	gone := make([]bool, len(lines)) // lines that were nothing but pictures, all sent
	var blobs []channel.Blob
	fence := ""
	lang := s.lang(ctx)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if mark := fenceOf(trimmed); mark != "" {
			switch {
			case fence == "":
				fence = mark
			case strings.HasPrefix(trimmed, fence) && strings.TrimLeft(trimmed, fence[:1]) == "":
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		pics, ok := blockImages(trimmed)
		if !ok {
			continue
		}
		var kept []string
		for _, pic := range pics {
			if len(blobs) >= channel.ImagesMost {
				kept = append(kept, imageLink(lang, pic))
				continue
			}
			blob, ok := s.channelImage(ctx, id, pic.address)
			if !ok {
				kept = append(kept, imageLink(lang, pic))
				continue
			}
			blobs = append(blobs, blob)
		}
		lines[i], gone[i] = strings.Join(kept, "\n"), len(kept) == 0
	}
	if len(blobs) == 0 {
		return text, nil
	}
	return withoutLines(lines, gone), blobs
}

// channelImage is the picture at address as a channel sends it: JPEG or PNG, at most ImageMost. ok is false for an
// address that isn't on the open internet, a type the chat app can't show (HEIC, AVIF) and a fetch that failed.
func (s *Service) channelImage(ctx context.Context, id, address string) (channel.Blob, bool) {
	get := s.channelPicture
	if get == nil && s.remoteImages != nil {
		get = s.remoteImages.Get
	}
	if get == nil {
		return channel.Blob{}, false
	}
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return channel.Blob{}, false
	}
	for _, side := range channelImageSides { // a smaller size when a picture's detail keeps it over the limit
		img, err := get(ctx, "channel:"+id, address, side)
		if err != nil {
			s.logf("channels: %s: a picture of the answer was not fetched: %v", id, err)
			return channel.Blob{}, false
		}
		if img.Type != "image/jpeg" && img.Type != "image/png" {
			return channel.Blob{}, false
		}
		if len(img.Data) <= channel.ImageMost {
			return channel.Blob{MIME: img.Type, Data: img.Data}, true
		}
	}
	return channel.Blob{}, false
}

// blockImages are the pictures of a line that has nothing but Markdown images (and spaces) on it; ok is false for any
// other line.
func blockImages(line string) ([]blockImage, bool) {
	if !strings.HasPrefix(line, "![") {
		return nil, false
	}
	matches := markdownImage.FindAllStringSubmatchIndex(line, -1)
	if len(matches) == 0 {
		return nil, false
	}
	var pics []blockImage
	at := 0
	for _, m := range matches {
		if strings.TrimSpace(line[at:m[0]]) != "" {
			return nil, false
		}
		pics = append(pics, blockImage{alt: strings.TrimSpace(line[m[2]:m[3]]), address: line[m[4]:m[5]]})
		at = m[1]
	}
	if strings.TrimSpace(line[at:]) != "" {
		return nil, false
	}
	return pics, true
}

// imageLink names a picture that was not sent: its description and its address, which the chat app makes a link.
func imageLink(lang i18n.Lang, pic blockImage) string {
	return i18n.T(lang, "assistant-channels_plugin.image_link", i18n.Args{"alt": i18n.Verbatim(pic.alt), "url": i18n.Verbatim(pic.address)})
}

// fenceOf is the fence a trimmed line opens or closes (``` or ~~~, three or more), "" when it is not one.
func fenceOf(trimmed string) string {
	for _, c := range []string{"`", "~"} {
		if strings.HasPrefix(trimmed, c+c+c) {
			n := len(trimmed) - len(strings.TrimLeft(trimmed, c))
			return trimmed[:n]
		}
	}
	return ""
}

// withoutLines joins lines without the gone ones, and without the blank line a gone one leaves doubled (or leading).
func withoutLines(lines []string, gone []bool) string {
	var out []string
	dropBlank := false
	for i, line := range lines {
		if gone[i] {
			dropBlank = len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == ""
			continue
		}
		if dropBlank && strings.TrimSpace(line) == "" {
			continue
		}
		dropBlank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
