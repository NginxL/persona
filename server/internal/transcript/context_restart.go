package transcript

const restartContextThreshold = 300_000

type contextRestart struct {
	lastID      string
	pending     bool
	first       bool
	recommended bool
}

// compact waits for the next measured request. Keep the last Claude message ID so replayed blocks of the
// pre-compaction request cannot count as that request.
func (r *contextRestart) compact() {
	r.pending, r.first, r.recommended = true, false, false
}

func (r *contextRestart) observe(id string, tokens int64) {
	high := tokens > restartContextThreshold
	if id != "" && id == r.lastID {
		// Claude can report more usage in another content block of the same request.
		if r.first {
			r.recommended = r.recommended || high
		}
		return
	}
	r.lastID = id
	r.first = r.pending
	r.pending = false
	// Only the first request may turn the recommendation on. A later drop clears it until another compact.
	r.recommended = high && (r.first || r.recommended)
}

// ContextRestartRecommended reports high input on the first measured request after compaction, still above the limit.
func (m *Meta) ContextRestartRecommended() bool {
	return m.restartContext.recommended
}
