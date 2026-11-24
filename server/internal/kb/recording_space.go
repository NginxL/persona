package kb

// Clean checks upload u as PutRecording does and puts its segments in order (empty ones dropped, Persona's echo
// marked), without storing anything: for a recording the manager saves into a track instead (#13501, Space).
func (u *RecordingUpload) Clean() error { return u.clean() }

// HoldsRecording reports whether the knowledge base has recording id, kept or as a tombstone: an upload naming a
// track for it is refused, so one recording never lives in both places.
func (e *Engine) HoldsRecording(id string) bool {
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	if _, gone := st.tombstones()[id]; gone {
		return true
	}
	_, ok := st.Recording(id)
	return ok
}
