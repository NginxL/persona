package proto

import "time"

// CloneTTFTMS returns a fresh copy of an optional TTFT measurement, or nil when absent.
func CloneTTFTMS(ms *int64) *int64 {
	if ms == nil {
		return nil
	}
	v := *ms
	return &v
}

// SanitizeTTFTMS returns a non-negative copy of ms, or nil when ms is nil or negative.
func SanitizeTTFTMS(ms *int64) *int64 {
	if ms == nil || *ms < 0 {
		return nil
	}
	v := *ms
	return &v
}

// SetTTFTMS sets an observed non-negative time-to-first-token in milliseconds on it.
// Negative values are ignored so invalid clock readings are never serialized.
func (it *Item) SetTTFTMS(ms int64) {
	if it == nil || ms < 0 {
		return
	}
	it.TTFTMS = Int64(ms)
}

// TTFTDuration returns the item's observed TTFT as a time.Duration, or false when absent or negative.
func (it Item) TTFTDuration() (time.Duration, bool) {
	if it.TTFTMS == nil || *it.TTFTMS < 0 {
		return 0, false
	}
	return time.Duration(*it.TTFTMS) * time.Millisecond, true
}
