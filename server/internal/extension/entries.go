package extension

import "slices"

// EntriesReadAccess checks the installed manifest and current grant under one
// lock, by exact extension ID. Legacy full-hub access alone does not opt into
// personal reads; co-location remains an independent proof for an allowed reader.
func (r *Registry) EntriesReadAccess(id string) (allowed, personal bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.extensions[id]
	if p == nil || !p.Record.Enabled || p.Problem != "" {
		return false, false
	}
	named := slices.Contains(p.Record.Granted.Hub, ScopeAssistantEntriesRead)
	allowed = named || p.Record.Granted.FullHub() && InEveryScope(ScopeAssistantEntriesRead)
	m := p.Full
	if m == nil {
		m = p.Manifest
	}
	personal = named && m != nil && slices.Contains(m.Permissions.Hub, ScopeAssistantEntriesRead)
	return allowed, personal
}
