// Package feishubot talks to a Feishu / Lark open platform app (a custom app with a bot): its long connection for
// events and card callbacks, and the REST calls a bot makes (send, reply, update a card, reactions, resources).
// It knows nothing of the hub; the hub decides what a message means (docs/extensions/bots.md).
package feishubot

import (
	"errors"
	"strconv"
)

// Frame is one message on the long connection (pbbp2.Frame in the official SDKs), protobuf on the wire. The SDK's
// generated code would pull in gogo/protobuf; the message is small enough to encode by hand, field for field in the
// order the SDK writes them (TestFrameMatchesTheSDK holds bytes it produced).
type Frame struct {
	SeqID           uint64
	LogID           uint64
	Service         int32
	Method          int32 // frameControl | frameData
	Headers         []Header
	PayloadEncoding string
	PayloadType     string
	Payload         []byte
	LogIDNew        string
}

type Header struct {
	Key, Value string
}

const (
	frameControl = 0
	frameData    = 1
)

// Header returns the value of a header, or "".
func (f *Frame) Header(key string) string {
	for _, h := range f.Headers {
		if h.Key == key {
			return h.Value
		}
	}
	return ""
}

func (f *Frame) headerInt(key string) int {
	n, _ := strconv.Atoi(f.Header(key))
	return n
}

// Marshal encodes the frame the way the SDK does: the scalars always, the three strings always (empty or not), the
// payload only when set.
func (f *Frame) Marshal() []byte {
	var b []byte
	b = appendVarintField(b, 1, f.SeqID)
	b = appendVarintField(b, 2, f.LogID)
	b = appendVarintField(b, 3, uint64(int64(f.Service)))
	b = appendVarintField(b, 4, uint64(int64(f.Method)))
	for _, h := range f.Headers {
		var hb []byte
		hb = appendBytesField(hb, 1, []byte(h.Key))
		hb = appendBytesField(hb, 2, []byte(h.Value))
		b = appendBytesField(b, 5, hb)
	}
	b = appendBytesField(b, 6, []byte(f.PayloadEncoding))
	b = appendBytesField(b, 7, []byte(f.PayloadType))
	if f.Payload != nil {
		b = appendBytesField(b, 8, f.Payload)
	}
	b = appendBytesField(b, 9, []byte(f.LogIDNew))
	return b
}

var errFrame = errors.New("feishubot: malformed frame")

// UnmarshalFrame decodes a frame; fields it does not know are skipped.
func UnmarshalFrame(b []byte) (*Frame, error) {
	f := &Frame{}
	err := eachField(b, func(num int, v uint64, data []byte) error {
		switch num {
		case 1:
			f.SeqID = v
		case 2:
			f.LogID = v
		case 3:
			f.Service = int32(v)
		case 4:
			f.Method = int32(v)
		case 5:
			var h Header
			if err := eachField(data, func(num int, _ uint64, data []byte) error {
				switch num {
				case 1:
					h.Key = string(data)
				case 2:
					h.Value = string(data)
				}
				return nil
			}); err != nil {
				return err
			}
			f.Headers = append(f.Headers, h)
		case 6:
			f.PayloadEncoding = string(data)
		case 7:
			f.PayloadType = string(data)
		case 8:
			f.Payload = append([]byte{}, data...)
		case 9:
			f.LogIDNew = string(data)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// eachField walks a protobuf message: fn gets each field's number and its varint value or its bytes.
func eachField(b []byte, fn func(num int, v uint64, data []byte) error) error {
	for len(b) > 0 {
		key, n := readVarint(b)
		if n == 0 {
			return errFrame
		}
		b = b[n:]
		num, wire := int(key>>3), key&7
		switch wire {
		case 0:
			v, n := readVarint(b)
			if n == 0 {
				return errFrame
			}
			b = b[n:]
			if err := fn(num, v, nil); err != nil {
				return err
			}
		case 2:
			l, n := readVarint(b)
			if n == 0 || uint64(len(b)-n) < l {
				return errFrame
			}
			data := b[n : n+int(l)]
			b = b[n+int(l):]
			if err := fn(num, 0, data); err != nil {
				return err
			}
		case 1:
			if len(b) < 8 {
				return errFrame
			}
			b = b[8:]
		case 5:
			if len(b) < 4 {
				return errFrame
			}
			b = b[4:]
		default:
			return errFrame
		}
	}
	return nil
}

func readVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func appendVarintField(b []byte, num int, v uint64) []byte {
	return appendVarint(appendVarint(b, uint64(num)<<3), v)
}

func appendBytesField(b []byte, num int, data []byte) []byte {
	b = appendVarint(appendVarint(b, uint64(num)<<3|2), uint64(len(data)))
	return append(b, data...)
}
