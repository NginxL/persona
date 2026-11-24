package assistant

import (
	"context"
	"runtime"
	"time"

	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/managerupdate"
	"github.com/nwverse/persona/server/internal/update"
)

// Only the independent assistant uses this loop. The Hub restarts its own child.
func followDevelopment(ctx context.Context, dir string, build update.Build, d *devices.Devices, idle Idle, logf func(string, ...any)) *managerupdate.DevelopmentImage {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" || build.Channel == update.Docker {
		return nil
	}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	var lastError string
	var ready bool
	for {
		t := d.DevelopmentTarget()
		if t != nil && (t.Version != build.Version || t.Commit != build.Commit || t.Build != build.Number) {
			image, err := prepareDevelopmentManager(ctx, dir, *t, d, idle)
			if err != nil && err.Error() != lastError {
				lastError = err.Error()
				logf("development manager update: %v", err)
			}
			if image != nil {
				return image
			}
		} else if t != nil && !ready && d.CheckDevelopmentTarget(ctx, t) == nil {
			if err := managerupdate.DevelopmentReady(dir, build, *t); err != nil {
				logf("development manager readiness: %v", err)
			} else {
				ready = true
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func prepareDevelopmentManager(ctx context.Context, dir string, t devicewire.DevelopmentTarget, d *devices.Devices, idle Idle) (*managerupdate.DevelopmentImage, error) {
	tried, err := managerupdate.DevelopmentTried(dir, t)
	if err != nil || tried {
		return nil, err
	}
	canStop := func() bool {
		current := d.DevelopmentTarget()
		if current == nil || *current != t || !d.DevelopmentIdle() {
			return false
		}
		check, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		ok, _ := idle(check)
		current = d.DevelopmentTarget()
		return ok && current != nil && *current == t && d.DevelopmentIdle()
	}
	if !canStop() {
		return nil, nil
	}
	program, err := d.ManagerDevelopmentPackage(ctx, t)
	if err != nil {
		return nil, err
	}
	image, err := managerupdate.StageDevelopment(dir, program, t)
	if err != nil {
		return nil, err
	}
	if !canStop() {
		return nil, nil
	}
	if err := d.CheckDevelopmentTarget(ctx, &t); err != nil {
		return nil, err
	}
	return image, nil
}
