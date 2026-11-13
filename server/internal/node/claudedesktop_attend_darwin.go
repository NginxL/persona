//go:build darwin

package node

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// claudeAttendCache keeps the answer briefly: one list asks for every desktop session that rests.
var claudeAttendCache struct {
	sync.Mutex
	at time.Time
	v  bool
}

// claudeAppAttendedNow: Claude's desktop app is the application in front (lsappinfo) and the person did something within
// claudeAttendIdle (the HID system's idle time, ioreg). Both commands come with macOS and read nothing of the app's.
func claudeAppAttendedNow() bool {
	claudeAttendCache.Lock()
	defer claudeAttendCache.Unlock()
	if time.Since(claudeAttendCache.at) < 2*time.Second {
		return claudeAttendCache.v
	}
	claudeAttendCache.at, claudeAttendCache.v = time.Now(), askClaudeAttended()
	return claudeAttendCache.v
}

func askClaudeAttended() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	front, err := exec.CommandContext(ctx, "lsappinfo", "front").Output()
	if err != nil {
		return true
	}
	info, err := exec.CommandContext(ctx, "lsappinfo", "info", "-only", "bundleid", strings.TrimSpace(string(front))).Output()
	if err != nil {
		return true
	}
	if frontBundleID(string(info)) != claudeDesktopBundle {
		return false
	}
	out, err := exec.CommandContext(ctx, "ioreg", "-c", "IOHIDSystem", "-d", "4", "-r", "-k", "HIDIdleTime").Output()
	if err != nil {
		return true
	}
	idle, ok := hidIdle(string(out))
	return !ok || idle < claudeAttendIdle
}
