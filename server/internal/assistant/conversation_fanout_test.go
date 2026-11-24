package assistant

import (
	"testing"
	"time"
)

// A sender wakes at most two conversations within the window; a third and later are kept for their receivers, a
// conversation already woken still wakes, another sender is counted apart, and the window passing frees it again.
func TestConversationSendFanoutKeepsABroadcastQuiet(t *testing.T) {
	var f sendFanout
	now := time.Now()
	for _, c := range []struct {
		name           string
		sender, target string
		at             time.Duration
		want           bool
	}{
		{"first", "main", "lead1", 0, true},
		{"second", "main", "lead2", time.Minute, true},
		{"third", "main", "lead3", 2 * time.Minute, false},
		{"fourth", "main", "lead4", 3 * time.Minute, false},
		{"one already woken", "main", "lead1", 4 * time.Minute, true},
		{"another sender", "lead1", "lead2", 4 * time.Minute, true},
		{"the window passed for the first two", "main", "lead3", 12 * time.Minute, true},
	} {
		if got := f.admit(c.sender, c.target, now.Add(c.at)); got != c.want {
			t.Errorf("%s: admit %v, want %v", c.name, got, c.want)
		}
	}
}
