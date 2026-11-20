package main

import (
	"strings"
	"testing"
)

func TestExplicitTeamDatabase(t *testing.T) {
	t.Setenv("PGDATABASE", "ambient")
	for _, raw := range []string{"", "postgres://host", "postgres://host/", "postgres://host/?dbname=ambient", "postgres://host/a/b", "postgres://hidden:%zz@host/team"} {
		_, err := teamDatabase(raw)
		if err == nil || strings.Contains(err.Error(), "hidden") {
			t.Fatal("malformed or unspecified database should yield a generic error")
		}
	}
	config, err := teamDatabase("postgres://localhost/team?sslmode=disable")
	if err != nil || config.ConnConfig.Database != "team" {
		t.Fatal("an explicit team database should be accepted")
	}
}

func TestOnlyLiteralLoopback(t *testing.T) {
	for _, addr := range []string{":18082", "localhost:80", "192.0.2.10:8080", "[::]:8080"} {
		err := run([]string{"serve", "-listen", addr})
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("unexpected result for %q: %v", addr, err)
		}
	}
}
