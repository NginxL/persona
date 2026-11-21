package proto

import (
	"strings"
	"testing"
)

func TestValidateServiceID(t *testing.T) {
	valid := []struct {
		kind string
		id   string
	}{
		{ServiceKindSystemd, "redis.service"},
		{ServiceKindSystemd, "worker@1.service"},
		{ServiceKindSystemd, "app:bg.timer"},
		{ServiceKindLaunchd, "com.example.worker"},
		{ServiceKindLaunchd, "homebrew.mxcl.postgresql_16"},
		{ServiceKindDocker, "a1b2c3d4e5f6"},
		{ServiceKindDocker, "my_app-web.1"},
	}
	for _, tc := range valid {
		if err := ValidateServiceID(tc.kind, tc.id); err != nil {
			t.Errorf("ValidateServiceID(%q, %q) = %v, want nil", tc.kind, tc.id, err)
		}
	}

	invalid := []struct {
		kind string
		id   string
	}{
		{"unknown", "redis.service"},
		{ServiceKindSystemd, ""},
		{ServiceKindSystemd, "-u"},
		{ServiceKindSystemd, "--all"},
		{ServiceKindSystemd, "../etc/passwd"},
		{ServiceKindSystemd, "foo;rm -rf /"},
		{ServiceKindSystemd, "foo|cat"},
		{ServiceKindSystemd, "foo$(id)"},
		{ServiceKindSystemd, "foo bar.service"},
		{ServiceKindSystemd, "a/b.service"},
		{ServiceKindSystemd, ".hidden.service"},
		{ServiceKindLaunchd, "com.example@bad"},
		{ServiceKindLaunchd, "com.example:bad"},
		{ServiceKindDocker, "abc@def"},
		{ServiceKindDocker, "abc:def"},
		{ServiceKindDocker, strings.Repeat("a", 129)},
		{ServiceKindSystemd, strings.Repeat("a", 257)},
	}
	for _, tc := range invalid {
		if err := ValidateServiceID(tc.kind, tc.id); err == nil {
			t.Errorf("ValidateServiceID(%q, %q) = nil, want error", tc.kind, tc.id)
		}
	}
}

func TestValidateServiceActionAndClampLines(t *testing.T) {
	for _, a := range []string{ServiceActionStart, ServiceActionStop, ServiceActionRestart} {
		if err := ValidateServiceAction(a); err != nil {
			t.Errorf("ValidateServiceAction(%q) = %v", a, err)
		}
	}
	for _, a := range []string{"", "kill", "rm", "exec", "reload"} {
		if err := ValidateServiceAction(a); err == nil {
			t.Errorf("ValidateServiceAction(%q) = nil, want error", a)
		}
	}

	if got := ClampServiceLogLines(0); got != ServiceLogsDefaultLines {
		t.Errorf("ClampServiceLogLines(0) = %d, want %d", got, ServiceLogsDefaultLines)
	}
	if got := ClampServiceLogLines(-5); got != ServiceLogsDefaultLines {
		t.Errorf("ClampServiceLogLines(-5) = %d, want %d", got, ServiceLogsDefaultLines)
	}
	if got := ClampServiceLogLines(42); got != 42 {
		t.Errorf("ClampServiceLogLines(42) = %d, want 42", got)
	}
	if got := ClampServiceLogLines(9999); got != ServiceLogsMaxLines {
		t.Errorf("ClampServiceLogLines(9999) = %d, want %d", got, ServiceLogsMaxLines)
	}
}
