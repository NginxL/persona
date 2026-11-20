package extwire

import (
	"encoding/base64"
	"strings"
)

// Avatars (#10923, docs/protocol/protocol.md "Avatars"): one value for every picture of a person or a conversation a
// source names (an Inbox thing, a conversation, a message's sender, an account), and one hub path the apps load it
// from. A source's value is an https address the manager fetches (on the open internet only) or one of the source's
// own media refs whose bytes its extension gives; either way it names fixed bytes (a changed picture gets a new
// value). The lists give apps AvatarURL's path; thread pages also keep their legacy refs for older clients.

// AvatarsRoute answers the picture an AvatarURL path names, drawn at ?size=<px> (the manager's, extmgr/avatars.go).
const AvatarsRoute = "GET /api/v1/avatars/{source}/{value}"

const avatarsPath = "/api/v1/avatars/"

// AvatarValueMost is the longest avatar value a source may give: a longer one is none (a ref names a picture, it
// does not carry one).
const AvatarValueMost = 2048

// AvatarUsable says whether value names a picture: not empty, at most AvatarValueMost bytes, and not an address the
// hub does not fetch (http://: only https:// is one).
func AvatarUsable(value string) bool {
	return value != "" && len(value) <= AvatarValueMost && !hasPrefixFold(value, "http://")
}

// AvatarAddress says whether value is an address the manager fetches (https://…); any other usable value is the
// source's own media ref.
func AvatarAddress(value string) bool { return hasPrefixFold(value, "https://") }

// AvatarURL is the hub path the apps load source's picture value from (they add ?size=<px>), "" when value names
// none (AvatarUsable) or there is no source.
func AvatarURL(source, value string) string {
	if source == "" || strings.ContainsAny(source, "/?#%") || !AvatarUsable(value) {
		return ""
	}
	return avatarsPath + source + "/" + base64.RawURLEncoding.EncodeToString([]byte(value))
}

// AvatarValue is the value an AvatarURL path's {value} names; false for one AvatarURL would not make.
func AvatarValue(seg string) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil || !AvatarUsable(string(b)) {
		return "", false
	}
	return string(b), true
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
