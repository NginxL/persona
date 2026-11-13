package node

import (
	"encoding/binary"
	"io"
)

// Login binding for the live-screen video channel (#3954).
//
// Video is only allowed once the paired RFB stream (port.dial of 5900 for the same `pair`) has signed in to macOS's
// Screen Sharing. The node does not hold any password; it simply *follows* the bytes already flowing between the
// phone and Screen Sharing on that stream and waits for the RFB SecurityResult. No credential travels anywhere new.
//
// The handshake is strictly turn-by-turn, so a single observer fed copies of both directions (server = Screen
// Sharing → phone, client = phone → Screen Sharing) can track it. The observer is fail-closed: it reports signed-in
// ONLY after it positively reads SecurityResult == 0. An unknown security type, a short read, or any surprise leaves
// the result `rfbLoginUnknown`, and screen.dial then refuses with `not_signed_in`.
//
// RFB/VNC handshake followed (RFB 3.7+ as macOS 003.889 speaks):
//  1. server → client: ProtocolVersion, 12 bytes ("RFB 003.889\n").
//  2. client → server: ProtocolVersion, 12 bytes.
//  3. server → client: u8 count, then `count` u8 security types.
//  4. client → server: u8 chosen type.
//  5. type-specific exchange (types 1 None, 2 VNC auth, 30 Apple Diffie-Hellman).
//  6. server → client: SecurityResult, u32 (0 = OK). On failure in 3.8+, a reason string follows; we only need the u32.

type rfbLoginResult int

const (
	rfbLoginUnknown rfbLoginResult = iota // not determined yet, or the handshake could not be followed (fail-closed)
	rfbLoginSignedIn
	rfbLoginFailed
)

// RFB security types the design follows; any other type yields `not_signed_in`.
const (
	rfbSecNone     = 1  // no authentication; SecurityResult follows immediately
	rfbSecVNCAuth  = 2  // 16-byte challenge (server) + 16-byte DES response (client), then SecurityResult
	rfbSecAppleDH  = 30 // Apple's Diffie-Hellman: generator+keyLen+prime+serverKey (server), cipher+clientKey (client)
	rfbProtoVerLen = 12 // "RFB 003.889\n"
)

// followRFBLogin reads tee'd copies of the two handshake directions and returns whether Screen Sharing accepted the
// login. It never writes; callers feed it copies so the live relay is untouched. It returns when SecurityResult is
// read, when the handshake cannot be followed, or when either reader ends (io.EOF → whatever was determined).
func followRFBLogin(server, client io.Reader) rfbLoginResult {
	skip := func(r io.Reader, n int) bool {
		_, err := io.CopyN(io.Discard, r, int64(n))
		return err == nil
	}
	readU8 := func(r io.Reader) (byte, bool) {
		var b [1]byte
		_, err := io.ReadFull(r, b[:])
		return b[0], err == nil
	}
	readU16 := func(r io.Reader) (uint16, bool) {
		var b [2]byte
		_, err := io.ReadFull(r, b[:])
		return binary.BigEndian.Uint16(b[:]), err == nil
	}

	// 1 & 2: ProtocolVersion each way.
	if !skip(server, rfbProtoVerLen) || !skip(client, rfbProtoVerLen) {
		return rfbLoginUnknown
	}
	// 3: the server's offered security types.
	count, ok := readU8(server)
	if !ok || count == 0 {
		return rfbLoginUnknown // count 0 means the server failed before auth (a reason string follows)
	}
	if !skip(server, int(count)) {
		return rfbLoginUnknown
	}
	// 4: the client's chosen type.
	chosen, ok := readU8(client)
	if !ok {
		return rfbLoginUnknown
	}
	// 5: type-specific exchange.
	switch chosen {
	case rfbSecNone:
		// nothing before SecurityResult
	case rfbSecVNCAuth:
		if !skip(server, 16) || !skip(client, 16) { // challenge, then response
			return rfbLoginUnknown
		}
	case rfbSecAppleDH:
		// server: generator(u16) keyLength(u16) prime(keyLength) serverPublicKey(keyLength)
		if _, ok := readU16(server); !ok {
			return rfbLoginUnknown
		}
		keyLen, ok := readU16(server)
		if !ok || keyLen == 0 {
			return rfbLoginUnknown
		}
		if !skip(server, int(keyLen)*2) {
			return rfbLoginUnknown
		}
		// client: credentials ciphertext(128) + clientPublicKey(keyLength)
		if !skip(client, 128) || !skip(client, int(keyLen)) {
			return rfbLoginUnknown
		}
	default:
		return rfbLoginUnknown // the design follows only 1, 2 and 30
	}
	// 6: SecurityResult.
	var res [4]byte
	if _, err := io.ReadFull(server, res[:]); err != nil {
		return rfbLoginUnknown
	}
	if binary.BigEndian.Uint32(res[:]) == 0 {
		return rfbLoginSignedIn
	}
	return rfbLoginFailed
}
