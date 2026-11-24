package node

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// claudeRateLimitLine is a rate_limit_event as Claude Code 2.1.330 printed it after a reply (stream-json, a
// subscription), with placeholder ids and the reset times moved to two hours and four days after now.
func claudeRateLimitLine(now time.Time, fiveHour, week float64) []byte {
	r5, r7 := now.Add(2*time.Hour).Unix(), now.Add(4*24*time.Hour).Unix()
	return fmt.Appendf(nil, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":%d,`+
		`"rateLimitType":"five_hour","overageStatus":"rejected","isUsingOverage":false,"unifiedWindows":{`+
		`"five_hour":{"utilization":%v,"resetsAt":%d,"status":"allowed"},`+
		`"seven_day":{"utilization":%v,"resetsAt":%d,"status":"allowed"}}},`+
		`"uuid":"00000000-0000-4000-8000-000000000001","session_id":"00000000-0000-4000-8000-000000000002"}`,
		r5, fiveHour, r5, week, r7)
}

func TestParseClaudeRateLimitEventReadsBothWindows(t *testing.T) {
	now := time.Date(2026, 11, 22, 4, 0, 0, 0, time.UTC)
	a, ok := parseClaudeRateLimitEvent(claudeRateLimitLine(now, 0.12, 0.29), now)
	if !ok {
		t.Fatal("a rate_limit_event with unifiedWindows read as nothing")
	}
	if a.Provider != "claude" || a.Name != "Claude" || !a.SampledAt.Equal(now) || len(a.Windows) != 2 {
		t.Fatalf("account: %+v", a)
	}
	want := []struct {
		label   string
		minutes int
		used    float64
		resets  time.Time
	}{{"5h", 300, 12, now.Add(2 * time.Hour)}, {"7d", 10080, 29, now.Add(4 * 24 * time.Hour)}}
	for i, w := range want {
		got := a.Windows[i]
		// 0.29 * 100 is 28.999999999999996 in floating point: the reading is rounded, as a gateway's used_ratio is.
		if got.Label != w.label || got.WindowMinutes != w.minutes || got.UsedPercent != w.used ||
			got.ResetsAt == nil || !got.ResetsAt.Equal(w.resets) {
			t.Errorf("window %d: %+v, want %+v", i, got, w)
		}
	}
}

func TestParseClaudeRateLimitEventIgnoresWhatIsNotAWindow(t *testing.T) {
	now := time.Date(2026, 11, 22, 4, 0, 0, 0, time.UTC)
	future, past := now.Add(time.Hour).Unix(), now.Add(-time.Minute).Unix()
	for name, line := range map[string]string{
		"another event":                 `{"type":"result","subtype":"success"}`,
		"not JSON":                      `{"type":"rate_limit_event"`,
		"no unified windows (API key)":  `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour"}}`,
		"no utilization":                fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"resetsAt":%d}}}}`, future),
		"only a window it does not map": fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day_opus":{"utilization":0.5,"resetsAt":%d}}}}`, future),
		"every window already reset":    fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"utilization":0.5,"resetsAt":%d}}}}`, past),
	} {
		if a, ok := parseClaudeRateLimitEvent([]byte(line), now); ok {
			t.Errorf("%s: read as %+v", name, a)
		}
	}
	over := fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"utilization":1.4,"resetsAt":%d}}}}`, future)
	if a, ok := parseClaudeRateLimitEvent([]byte(over), now); !ok || a.Windows[0].UsedPercent != 100 {
		t.Errorf("a window past its limit: %+v %v, want 100%%", a, ok)
	}
}

func TestClaudeStreamUsageKeepsTheNewestSamplePerLauncher(t *testing.T) {
	n := &Node{home: t.TempDir(), dir: t.TempDir()}
	now := time.Now().Truncate(time.Second)
	n.claudeStreamUsage("claude", "", claudeRateLimitLine(now, 0.12, 0.03), now)
	n.claudeStreamUsage("claude", "", claudeRateLimitLine(now, 0.10, 0.03), now.Add(-time.Minute)) // an older reading
	n.claudeStreamUsage("claude-work", "", claudeRateLimitLine(now, 0.50, 0.40), now)
	n.claudeStreamUsage("claude", "private", claudeRateLimitLine(now, 0.70, 0.60), now)

	used := func(launcher, configDir string) float64 {
		t.Helper()
		n.usageWin.mu.Lock()
		defer n.usageWin.mu.Unlock()
		a, ok := n.usageWin.accounts[claudeStreamUsageKey(launcher, configDir)]
		if !ok {
			t.Fatalf("no source for %q / %q", launcher, configDir)
		}
		return a.Windows[0].UsedPercent
	}
	if got := used("claude", ""); got != 12 {
		t.Errorf("claude: %v%%, want 12%% (an older reading does not replace a newer one)", got)
	}
	if got, other := used("claude-work", ""), used("claude", "private"); got != 50 || other != 70 {
		t.Errorf("other sources: %v%% and %v%%, want 50%% and 70%%", got, other)
	}
	if claudeStreamUsageKey("", "") != claudeStreamUsageKey("claude", "") {
		t.Error("a conversation without a launcher name is not the built-in claude's source")
	}
	if snap := n.usageSnapshot(); !snap.Enabled || len(snap.Accounts) != 3 {
		t.Fatalf("snapshot: %+v", snap)
	}

	later := now.Add(time.Minute)
	n.claudeStreamUsage("claude", "", claudeRateLimitLine(later, 0.14, 0.03), later)
	if got := used("claude", ""); got != 14 {
		t.Errorf("after a newer reply: %v%%, want 14%%", got)
	}
	// A poll finds nothing of its own here (no Codex, no status line file) and keeps the readings, as it keeps any
	// quiet source.
	n.refreshUsageWindows(t.Context())
	if got := used("claude", ""); got != 14 {
		t.Errorf("after a poll: %v%%, want 14%%", got)
	}
}

func TestClaudeStreamUsageHonoursTheOffSwitch(t *testing.T) {
	n := &Node{home: t.TempDir(), dir: t.TempDir()}
	now := time.Now().Truncate(time.Second)
	n.setUsageWindows(false)
	n.claudeStreamUsage("claude", "", claudeRateLimitLine(now, 0.12, 0.03), now)
	if snap := n.usageSnapshot(); snap.Enabled || len(snap.Accounts) != 0 {
		t.Fatalf("collection off: %+v", snap)
	}
	n.usageWin.mu.Lock()
	kept := n.usageWin.accounts
	n.usageWin.mu.Unlock()
	if kept != nil {
		t.Fatalf("collection off kept %+v", kept)
	}
	n.setUsageWindows(true)
	n.claudeStreamUsage("claude", "", claudeRateLimitLine(now, 0.12, 0.03), now)
	if snap := n.usageSnapshot(); !snap.Enabled || len(snap.Accounts) != 1 || snap.Accounts[0].Windows[0].UsedPercent != 12 {
		t.Fatalf("collection on: %+v", snap)
	}
}

// The conversation reader hands a rate_limit_event among the stream's other lines to the usage collector: a reply in a
// Claude assistant conversation updates the windows of its launcher and config directory choice (#12855).
func TestReadAssistantFeedsClaudeStreamUsage(t *testing.T) {
	n := assistantNode(t)
	n.dir = t.TempDir()
	now := time.Now().Truncate(time.Second)
	p := &assistantProc{id: "11111111-2222-3333-4444-555555555555", launcher: "claude-work", model: modelExecution{ConfigDir: "private"}}
	n.readAssistant(p, strings.NewReader(strings.Join([]string{
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		string(claudeRateLimitLine(now, 0.42, 0.07)),
		`{"type":"result","subtype":"success"}`,
	}, "\n")+"\n"))
	n.usageWin.mu.Lock()
	a, ok := n.usageWin.accounts[claudeStreamUsageKey("claude-work", "private")]
	n.usageWin.mu.Unlock()
	if !ok || len(a.Windows) != 2 || a.Windows[0].UsedPercent != 42 || a.Windows[1].UsedPercent != 7 {
		t.Fatalf("the stream's rate_limit_event did not reach the usage windows: %+v %v", a, ok)
	}
	if snap := n.usageSnapshot(); !snap.Enabled || len(snap.Accounts) != 1 {
		t.Fatalf("snapshot: %+v", snap)
	}
}
