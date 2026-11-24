package screenlink

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The node frames every session message as the shared cases say (spec/fixtures/screen-link), which the iPhone's
// client checks too.
func TestTheWireMatchesTheSharedCases(t *testing.T) {
	raw, err := os.ReadFile("../../../../../spec/fixtures/screen-link/messages.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int `json:"version"`
		Cases   []struct {
			Name  string `json:"name"`
			Input struct {
				FrameHex string `json:"frame_hex"`
			} `json:"input"`
			Expected map[string]any `json:"expected"`
			Exact    bool           `json:"exact"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != 1 || len(doc.Cases) == 0 {
		t.Fatalf("fixture: %v", err)
	}
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			b, err := hex.DecodeString(c.Input.FrameHex)
			if err != nil {
				t.Fatal(err)
			}
			typ, body, err := newReader(bytes.NewReader(b)).next()
			if err != nil {
				t.Fatal(err)
			}
			got := decodeForTest(t, typ, body)
			if !reflect.DeepEqual(got, c.Expected) {
				t.Fatalf("decoded %v, want %v", got, c.Expected)
			}
			if c.Exact {
				if enc := encodeForTest(t, c.Expected); !bytes.Equal(enc, b) {
					t.Fatalf("encoded %x, want %x", enc, b)
				}
			}
		})
	}
}

// Close confirmation has its own wire type so a final-data ack crossing with close cannot be mistaken for proof that
// the phone processed that close. Its body follows the existing channel/offset convention. Older nodes skip type 14 as
// unknown; PersonaScreenKit's ScreenLinkWireTests checks these same bytes.
func TestCloseConfirmationWire(t *testing.T) {
	const (
		closeAckForTest = byte(14)
		final           = uint64(0x0102030405060708)
	)
	want, err := hex.DecodeString("0000000a0e020102030405060708")
	if err != nil {
		t.Fatal(err)
	}
	got := frame(closeAckForTest, chanOffset(FirstVideo, final))
	if !bytes.Equal(got, want) {
		t.Fatalf("close confirmation %x, want %x", got, want)
	}
	typ, body, err := newReader(bytes.NewReader(want)).next()
	if err != nil {
		t.Fatal(err)
	}
	id, off, rest, err := readChanOffset(body)
	if typ != closeAckForTest || id != FirstVideo || off != final || len(rest) != 0 || err != nil {
		t.Fatalf("confirmation type %d channel %d offset %d rest %x, err %v", typ, id, off, rest, err)
	}
	decoded := decodeForTest(t, typ, body)
	expected := map[string]any{"type": "close_ack", "channel": float64(FirstVideo), "offset": float64(final)}
	if !reflect.DeepEqual(decoded, expected) {
		t.Fatalf("decoded close confirmation %v, want %v", decoded, expected)
	}
	closed := frame(TypeClose, chanOffset(FirstVideo, final))
	if !bytes.Equal(closed[5:], got[5:]) {
		t.Fatalf("close body %x and confirmation body %x differ", closed[5:], got[5:])
	}
}

func decodeForTest(t *testing.T, typ byte, body []byte) map[string]any {
	t.Helper()
	named := map[byte]string{TypeData: "data", TypeAck: "ack", TypeClose: "close", TypeReset: "reset", TypeUse: "use",
		TypeResumed: "resumed", TypePing: "ping", TypePong: "pong", TypeAttached: "attached", TypePaths: "paths", TypeAttach: "attach",
		closeAckTypeForTest: "close_ack"}
	name, ok := named[typ]
	if !ok {
		return map[string]any{"type": "unknown", "code": float64(typ)}
	}
	out := map[string]any{"type": name}
	switch typ {
	case TypeData, TypeAck, TypeClose, TypeReset, closeAckTypeForTest:
		ch, off, rest, err := readChanOffset(body)
		if err != nil {
			t.Fatal(err)
		}
		out["channel"], out["offset"] = float64(ch), float64(off)
		if typ == TypeData {
			out["bytes_hex"] = hex.EncodeToString(rest)
		}
	case TypeUse, TypeResumed:
		if len(body)%9 != 0 {
			t.Fatal("ragged entries")
		}
		entries := []any{}
		for i := 0; i < len(body); i += 9 {
			entries = append(entries, []any{float64(body[i]), float64(binary.BigEndian.Uint64(body[i+1 : i+9]))})
		}
		out["entries"] = entries
	case TypePing:
		out["value"] = float64(binary.BigEndian.Uint64(body))
	case TypePong:
		out["value"] = float64(binary.BigEndian.Uint64(body))
		out["retransmitted"] = float64(binary.BigEndian.Uint32(body[8:]))
		out["sent"] = float64(binary.BigEndian.Uint32(body[12:]))
	case TypePaths:
		var p Paths
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		// Through the node's own type and back: what it would send for these paths.
		again, _ := json.Marshal(p)
		var m map[string]any
		_ = json.Unmarshal(again, &m)
		out["paths"] = m
	case TypeAttach:
		var a struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(body, &a); err != nil {
			t.Fatal(err)
		}
		out["token"] = a.Token
	}
	return out
}

func encodeForTest(t *testing.T, m map[string]any) []byte {
	t.Helper()
	num := func(k string) uint64 { return uint64(m[k].(float64)) }
	switch m["type"] {
	case "data":
		b, _ := hex.DecodeString(m["bytes_hex"].(string))
		return frame(TypeData, chanOffset(byte(num("channel")), num("offset")), b)
	case "ack":
		return frame(TypeAck, chanOffset(byte(num("channel")), num("offset")))
	case "close_ack":
		return frame(closeAckTypeForTest, chanOffset(byte(num("channel")), num("offset")))
	case "close":
		return frame(TypeClose, chanOffset(byte(num("channel")), num("offset")))
	case "reset":
		return frame(TypeReset, chanOffset(byte(num("channel")), num("offset")))
	case "use", "resumed":
		var body []byte
		for _, e := range m["entries"].([]any) {
			pair := e.([]any)
			body = append(body, chanOffset(byte(pair[0].(float64)), uint64(pair[1].(float64)))...)
		}
		typ := TypeUse
		if m["type"] == "resumed" {
			typ = TypeResumed
		}
		return frame(typ, body)
	case "ping":
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], num("value"))
		return frame(TypePing, b[:])
	case "pong":
		var b [16]byte
		binary.BigEndian.PutUint64(b[:8], num("value"))
		binary.BigEndian.PutUint32(b[8:12], uint32(num("retransmitted")))
		binary.BigEndian.PutUint32(b[12:], uint32(num("sent")))
		return frame(TypePong, b[:])
	case "attached":
		return frame(TypeAttached)
	case "unknown":
		return frame(byte(num("code")), []byte{0xab})
	}
	t.Fatalf("no encoding for %v", m["type"])
	return nil
}
