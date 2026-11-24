package volc

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// The speech service's WebSocket binary protocol (streaming recognition, docs/extensions/volcengine-speech.md "Recognition"):
// every frame is a 4-byte header, then by its type a sequence number, a payload size and the payload. Integers are
// big-endian. Byte 0: version (1) and header size in 4-byte words (1); byte 1: message type and its flags; byte 2:
// serialization and compression; byte 3: reserved.
const (
	protoVersion = 1

	msgFullClient = 0x1 // the session's request (JSON)
	msgAudioOnly  = 0x2 // audio
	msgFullServer = 0x9 // a result (JSON)
	msgError      = 0xF // an error: code, message

	flagPosSeq = 0x1 // a positive sequence number follows the header
	flagLast   = 0x2 // the last frame of the session
	flagNegSeq = 0x3 // the last frame, its sequence number negative
	flagEvent  = 0x4 // an event number follows (not used by recognition; read past)

	serNone = 0x0
	serJSON = 0x1

	compNone = 0x0
	compGzip = 0x1
)

// frameMost bounds a payload read from the service (a result with every utterance of a long session).
const frameMost = 8 << 20

// clientFrame is one frame to the service: the request (msgFullClient, JSON) or audio (msgAudioOnly), gzip-compressed,
// with its sequence number (negative on the last audio frame).
func clientFrame(msgType byte, seq int32, payload []byte) []byte {
	flags, ser := byte(flagPosSeq), byte(serJSON)
	if seq < 0 {
		flags = flagNegSeq
	}
	if msgType == msgAudioOnly {
		ser = serNone
	}
	z := gzipBytes(payload)
	var b bytes.Buffer
	b.Grow(12 + len(z))
	b.WriteByte(protoVersion<<4 | 1)
	b.WriteByte(msgType<<4 | flags)
	b.WriteByte(ser<<4 | compGzip)
	b.WriteByte(0)
	_ = binary.Write(&b, binary.BigEndian, seq)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(z)))
	b.Write(z)
	return b.Bytes()
}

// serverFrame is one frame from the service, decoded.
type serverFrame struct {
	msgType byte
	seq     int32
	last    bool
	code    uint32 // an error frame's code
	payload []byte // a result's JSON, an error's message
}

var errShortFrame = errors.New("a frame shorter than its header says")

// parseServerFrame reads one frame from the service.
func parseServerFrame(b []byte) (serverFrame, error) {
	var f serverFrame
	if len(b) < 4 {
		return f, errShortFrame
	}
	size := int(b[0]&0x0f) * 4
	if size < 4 || len(b) < size {
		return f, errShortFrame
	}
	f.msgType = b[1] >> 4
	flags, ser, comp := b[1]&0x0f, b[2]>>4, b[2]&0x0f
	rest := b[size:]
	if flags&flagPosSeq != 0 {
		if len(rest) < 4 {
			return f, errShortFrame
		}
		f.seq = int32(binary.BigEndian.Uint32(rest))
		rest = rest[4:]
	}
	f.last = flags&flagLast != 0
	if flags&flagEvent != 0 {
		if len(rest) < 4 {
			return f, errShortFrame
		}
		rest = rest[4:]
	}
	switch f.msgType {
	case msgFullServer:
		if len(rest) < 4 {
			return f, errShortFrame
		}
		n := binary.BigEndian.Uint32(rest)
		rest = rest[4:]
		if uint64(n) > uint64(len(rest)) {
			return f, errShortFrame
		}
		rest = rest[:n]
	case msgError:
		if len(rest) < 8 {
			return f, errShortFrame
		}
		f.code = binary.BigEndian.Uint32(rest)
		n := binary.BigEndian.Uint32(rest[4:])
		rest = rest[8:]
		if uint64(n) > uint64(len(rest)) {
			return f, errShortFrame
		}
		f.payload = rest[:n] // the message is plain text
		return f, nil
	default:
		return f, fmt.Errorf("a frame of type %#x", f.msgType)
	}
	if comp == compGzip && len(rest) > 0 {
		out, err := gunzipBytes(rest)
		if err != nil {
			return f, err
		}
		rest = out
	}
	if ser != serJSON && ser != serNone {
		return f, fmt.Errorf("a payload serialized as %#x", ser)
	}
	f.payload = rest
	return f, nil
}

func gzipBytes(p []byte) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write(p)
	_ = w.Close()
	return b.Bytes()
}

func gunzipBytes(p []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(p))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, frameMost+1))
	if err != nil {
		return nil, err
	}
	if len(out) > frameMost {
		return nil, fmt.Errorf("a payload over %d bytes", frameMost)
	}
	return out, nil
}
