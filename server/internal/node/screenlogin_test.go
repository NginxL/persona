package node

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// rfbVersion is the 12-byte ProtocolVersion macOS sends ("RFB 003.889\n").
var rfbVersion = []byte("RFB 003.889\n")

func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

func TestFollowRFBLogin(t *testing.T) {
	keyLen := 128 // Apple DH key length in bytes (illustrative)
	cases := []struct {
		name   string
		server []byte
		client []byte
		want   rfbLoginResult
	}{
		{
			name:   "none signed in",
			server: bytesJoin(rfbVersion, []byte{1}, []byte{rfbSecNone}, u32(0)),
			client: bytesJoin(rfbVersion, []byte{rfbSecNone}),
			want:   rfbLoginSignedIn,
		},
		{
			name:   "vnc auth signed in",
			server: bytesJoin(rfbVersion, []byte{2}, []byte{rfbSecNone, rfbSecVNCAuth}, make([]byte, 16), u32(0)),
			client: bytesJoin(rfbVersion, []byte{rfbSecVNCAuth}, make([]byte, 16)),
			want:   rfbLoginSignedIn,
		},
		{
			name: "apple dh signed in",
			server: bytesJoin(rfbVersion, []byte{1}, []byte{rfbSecAppleDH},
				u16(2), u16(uint16(keyLen)), make([]byte, keyLen), make([]byte, keyLen), u32(0)),
			client: bytesJoin(rfbVersion, []byte{rfbSecAppleDH}, make([]byte, 128), make([]byte, keyLen)),
			want:   rfbLoginSignedIn,
		},
		{
			name:   "vnc auth rejected",
			server: bytesJoin(rfbVersion, []byte{1}, []byte{rfbSecVNCAuth}, make([]byte, 16), u32(1)),
			client: bytesJoin(rfbVersion, []byte{rfbSecVNCAuth}, make([]byte, 16)),
			want:   rfbLoginFailed,
		},
		{
			name:   "unknown security type refused",
			server: bytesJoin(rfbVersion, []byte{1}, []byte{35}, u32(0)),
			client: bytesJoin(rfbVersion, []byte{35}),
			want:   rfbLoginUnknown,
		},
		{
			name:   "server pre-auth failure (count 0)",
			server: bytesJoin(rfbVersion, []byte{0}),
			client: bytesJoin(rfbVersion),
			want:   rfbLoginUnknown,
		},
		{
			name:   "truncated before security result",
			server: bytesJoin(rfbVersion, []byte{1}, []byte{rfbSecNone}),
			client: bytesJoin(rfbVersion, []byte{rfbSecNone}),
			want:   rfbLoginUnknown,
		},
		{
			name:   "empty streams",
			server: nil,
			client: nil,
			want:   rfbLoginUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := followRFBLogin(bytes.NewReader(tc.server), bytes.NewReader(tc.client))
			if got != tc.want {
				t.Fatalf("followRFBLogin = %d, want %d", got, tc.want)
			}
		})
	}
}

func bytesJoin(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
