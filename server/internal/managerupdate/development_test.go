//go:build unix

package managerupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/update"
)

func stageFixtureManager(t *testing.T, dir, letter, script string) *DevelopmentImage {
	t.Helper()
	target := devicewire.DevelopmentBuild(strings.Repeat(letter, 40), "web-dev-"+strings.Repeat(letter, 12)+"-202611230907", 202611230907)
	path := filepath.Join(t.TempDir(), "persona-manager")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	image, err := StageDevelopment(dir, path, target)
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func TestDevelopmentManagerFailedTrialKeepsThePreviousProgram(t *testing.T) {
	dir := t.TempDir()
	bad := stageFixtureManager(t, dir, "b", "exit 23\n")
	// The previous program asks once for a new image. After the trial fails it
	// starts again, using exactly the same manager directory and working binary.
	good := stageFixtureManager(t, dir, "a", `if [ -f "$4/offer.json" ]; then
    mv "$4/offer.json" "$PERSONA_DEVELOPMENT_MANAGER_WORKER/next.json"
else
    echo recovered > "$4/recovered"
fi
`)
	if err := update.Save(filepath.Join(dir, "offer.json"), bad); err != nil {
		t.Fatal(err)
	}
	if err := update.Save(developmentStatePath(dir), developmentState{Launcher: "stable/1/", Current: good}); err != nil {
		t.Fatal(err)
	}
	launcher := func(context.Context) error { return errors.New("unexpected use of original launcher") }
	if err := RunDevelopment(t.Context(), dir, update.Build{Version: "stable", Number: 1}, io.Discard, launcher); err != nil {
		t.Fatal(err)
	}
	var s developmentState
	if ok, err := update.Load(developmentStatePath(dir), &s); !ok || err != nil || s.Current == nil || *s.Current != *good || s.Tried != bad.Target.Version {
		t.Fatalf("rollback state: %+v, %v", s, err)
	}
	if tried, err := DevelopmentTried(dir, bad.Target); !tried || err != nil {
		t.Fatal("failed image can repeat", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "recovered")); err != nil {
		t.Fatal("old manager did not restart", err)
	}
	if err := good.check(dir); err != nil {
		t.Fatal("previous binary changed", err)
	}
}

func TestDevelopmentManagerPromotesOnlyReadyImageAndRecoversDamage(t *testing.T) {
	dir := t.TempDir()
	build := update.Build{Version: "stable", Number: 1}
	gate := filepath.Join(dir, "gate")
	if err := syscall.Mkfifo(gate, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(gate, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	good := stageFixtureManager(t, dir, "a", `cp "$4/fixture-ready.json" "$PERSONA_DEVELOPMENT_MANAGER_WORKER/ready.json"
if [ -p "$4/gate" ]; then read -r reply < "$4/gate"; fi
`)
	if err := update.Save(filepath.Join(dir, "fixture-ready.json"), good.Target); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunDevelopment(ctx, dir, build, io.Discard, func(context.Context) error { return good })
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var s developmentState
		if ok, _ := update.Load(developmentStatePath(dir), &s); ok && s.Current != nil && *s.Current == *good && s.Tried == "" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("launcher exited before promotion: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	if _, err := f.WriteString("stop\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	// Later startup detects damage to a confirmed slot and restores the original launcher.
	if err := os.WriteFile(good.binary(dir), []byte("corrupt"), 0700); err != nil {
		t.Fatal(err)
	}
	var restored bool
	if err := RunDevelopment(t.Context(), dir, build, io.Discard, func(context.Context) error { restored = true; return nil }); err != nil || !restored {
		t.Fatal("original launcher did not recover", err)
	}
	if tried, err := DevelopmentTried(dir, good.Target); err != nil || !tried {
		t.Fatal("damaged image can repeat", err)
	}
}

func TestDevelopmentManagerRejectsReadinessForDifferentCommit(t *testing.T) {
	dir := t.TempDir()
	bad := stageFixtureManager(t, dir, "b", `cp "$4/fixture-ready.json" "$PERSONA_DEVELOPMENT_MANAGER_WORKER/ready.json"
exit 0
`)
	wrong := bad.Target
	wrong.Commit = strings.Repeat("c", 40)
	encoded, err := json.Marshal(wrong)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture-ready.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	count := 0
	err = RunDevelopment(t.Context(), dir, update.Build{Version: "original"}, io.Discard, func(context.Context) error {
		count++
		if count == 1 {
			return bad
		}
		return nil
	})
	var s developmentState
	ok, loadErr := update.Load(developmentStatePath(dir), &s)
	if err != nil || !ok || loadErr != nil || count != 2 || s.Current != nil || s.Tried != bad.Target.Version {
		t.Fatalf("wrong identity advanced state: %+v, run %v, load %v", s, err, loadErr)
	}
}
