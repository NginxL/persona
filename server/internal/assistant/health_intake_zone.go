package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Intake returns a portable zone identifier, never Go's process-local "Local" name.
// Resolve the same zone for both calendar boundaries and the API/page's displayed zone.
func (s *Service) healthIntakeLocation(ctx context.Context) *time.Location {
	name := s.settings(ctx).Timezone
	if name != "" && name != "Local" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	tz, set := os.LookupEnv("TZ")
	return intakeLocalZone(tz, set, func() (string, error) { return filepath.EvalSymlinks("/etc/localtime") })
}

func intakeLocalZone(tz string, set bool, localtime func() (string, error)) *time.Location {
	load := func(name string) *time.Location {
		name = strings.TrimPrefix(name, ":")
		if _, zone, ok := strings.Cut(name, "/zoneinfo/"); ok {
			name = strings.TrimPrefix(strings.TrimPrefix(zone, "posix/"), "right/")
		}
		if name == "" || name == "Local" {
			return nil
		}
		loc, err := time.LoadLocation(name)
		if err != nil {
			return nil
		}
		return loc
	}
	if set {
		if tz == "" {
			return time.UTC // An explicitly empty TZ means UTC, as in Go's local-zone loader.
		}
		if loc := load(tz); loc != nil {
			return loc
		}
	}
	if target, err := localtime(); err == nil {
		if loc := load(target); loc != nil {
			return loc
		}
	}
	return time.UTC // A portable, deterministic fallback when no IANA name can be resolved.
}
