package assistant

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/update"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

func TestDevelopmentManagerChecksPolicyActivityAndConfirmedTarget(t *testing.T) {
	target := devicewire.DevelopmentBuild(strings.Repeat("a", 40), "web-dev-aaaaaaaaaaaa-202611230913", 202611230913)
	platform := runtime.GOOS + "-" + runtime.GOARCH
	for _, program := range []string{"persona-manager", "personad"} {
		t.Run(program, func(t *testing.T) {
			t.Setenv("PERSONA_HOME", t.TempDir())
			versionOutput := fmt.Sprintf("%s\nchannel release, build %d\n", target.Version, target.Build)
			bin := updatetest.ProgramSource(t, program, runtime.GOOS, runtime.GOARCH,
				fmt.Sprintf("package main\nimport \"fmt\"\nfunc main(){fmt.Print(%q)}\n", versionOutput))
			raw, err := os.ReadFile(bin)
			if err != nil {
				t.Fatal(err)
			}
			var data bytes.Buffer
			zw := gzip.NewWriter(&data)
			if _, err = zw.Write(raw); err != nil {
				t.Fatal(err)
			}
			if err = zw.Close(); err != nil {
				t.Fatal(err)
			}
			name := program + "-" + platform
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			f := update.File{Name: name + ".gz", Size: int64(data.Len()), SHA256: fmt.Sprintf("%x", sha256.Sum256(data.Bytes())), BinarySHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}
			m := update.Manifest{Version: target.Version, Commit: target.Commit, Build: target.Build, Files: map[string]update.File{platform: f}}
			if program == "persona-manager" {
				m.Programs = map[string]map[string]update.File{program: {platform: f}}
			}
			var requests atomic.Int32
			var gone atomic.Bool
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer fixture" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if gone.Load() {
					w.WriteHeader(http.StatusGone)
					return
				}
				switch r.URL.Path {
				case target.Packages + "/manifest.json":
					_ = json.NewEncoder(w).Encode(m)
				case target.Packages + "/" + f.Name:
					_, _ = w.Write(data.Bytes())
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			binding := &bus.Client{Hub: srv.URL, HTTPClient: srv.Client(), Token: "fixture"}
			d := devices.New(devices.Options{PackageHub: func() *bus.Client { return binding }})
			cfg := devicewire.Config{HubVersion: target.Version, DevelopmentTarget: &target}
			d.Configure(cfg)
			dir := t.TempDir()
			idle := func(context.Context) (bool, string) { return true, "" }
			if image, err := prepareDevelopmentManager(t.Context(), dir, target, d, idle); image != nil || err != nil {
				t.Fatal("disabled fleet updates", image, err)
			}
			cfg.AutoUpdateNodes = true
			d.Configure(cfg)
			if image, err := prepareDevelopmentManager(t.Context(), dir, target, d, func(context.Context) (bool, string) { return false, "active" }); image != nil || err != nil || requests.Load() != 0 {
				t.Fatal("busy manager fetched a program", err)
			}
			checks := 0
			if image, err := prepareDevelopmentManager(t.Context(), dir, target, d, func(context.Context) (bool, string) {
				checks++
				return checks == 1, ""
			}); err != nil || image != nil || checks != 2 {
				t.Fatal("manager became busy during download", err, checks)
			}
			checks = 0
			if image, err := prepareDevelopmentManager(t.Context(), dir, target, d, func(context.Context) (bool, string) {
				checks++
				if checks == 2 {
					gone.Store(true)
				}
				return true, ""
			}); err == nil || image != nil {
				t.Fatal("withdrawn target restarted after an idle wait", err)
			}
			gone.Store(false)
			image, err := prepareDevelopmentManager(t.Context(), dir, target, d, idle)
			if err != nil || image == nil || image.Target != target || image.SHA256 != f.BinarySHA256 {
				t.Fatal("confirmed idle manager could not stage its program", image, err)
			}
		})
	}
}
