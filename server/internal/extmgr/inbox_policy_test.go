package extmgr

import "testing"

// The non-connector tool families have no connector ReadTools interface. Reads are explicit and every other action,
// including browser/computer/phone control and future unknown tools, is not classified as read-only.
func TestToolAccessSubscriptionReadsAndControlWrites(t *testing.T) {
	h := &Manager{}
	for _, tc := range []struct {
		name            string
		known, readOnly bool
	}{
		{"subscription_list", true, true},
		{"subscription_get", true, true},
		{"subscription_sources", true, true},
		{"subscription_dry_run", true, true},
		{"subscription_save", true, false},
		{"subscription_run", true, false},
		{"future_unknown_tool", false, false},
	} {
		known, readOnly := h.ToolAccess(tc.name)
		if known != tc.known || readOnly != tc.readOnly {
			t.Errorf("ToolAccess(%q)=(%v,%v), want (%v,%v)", tc.name, known, readOnly, tc.known, tc.readOnly)
		}
	}
}
