package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// SourcePermission is a node's observation at a tool call, never a tool argument or
// a cached session-list mode. Generation identifies the process that wrote it.
// Digest binds the observation to this particular call, before host rendering.
type SourcePermission struct {
	At         time.Time `json:"at"`
	Session    string    `json:"session"`
	Generation string    `json:"generation"`
	Class      string    `json:"class"`
	Digest     string    `json:"digest"`
}

func SourceCallDigest(name string, args json.RawMessage) string {
	// RPC marshaling compacts RawMessage and escapes HTML characters. Hash the
	// same representation on both sides rather than the caller's whitespace.
	canonical, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append(append([]byte(name), 0), canonical...))
	return hex.EncodeToString(sum[:])
}

func (p SourcePermission) Valid() bool {
	_, err := hex.DecodeString(p.Digest)
	return err == nil && !p.At.IsZero() && p.Session != "" && len(p.Session) <= 256 && p.Generation != "" && len(p.Generation) <= 256 &&
		(p.Class == "bypass" || p.Class == "prompting") && len(p.Digest) == 64
}

func (p SourcePermission) Matches(session, name string, args json.RawMessage) bool {
	return p.Valid() && p.Session == session && p.Digest == SourceCallDigest(name, args)
}
