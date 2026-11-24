package devices

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

func fixtureDevelopment(commit string) devicewire.DevelopmentTarget {
	return devicewire.DevelopmentBuild(commit, "web-dev-"+commit[:12]+"-202611230950", 202611230950)
}

// A manager behind a reverse proxy, bound to this test's TLS key. The test site
// only answers requests carrying the binding and the exact advertised path.
func bindDevelopment(t *testing.T, d *Devices, target devicewire.DevelopmentTarget, serve http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ok := strings.CutPrefix(r.URL.Path, "/prefix"+target.Packages+"/")
		if !ok || r.Header.Get("Authorization") != "Bearer test-binding" {
			http.Error(w, "wrong binding", http.StatusUnauthorized)
			return
		}
		r.URL.Path = "/" + path
		w.Header().Set(devicewire.DevelopmentCommitHeader, target.Commit)
		serve(w, r)
	}))
	t.Cleanup(srv.Close)
	binding := &bus.Client{Hub: strings.Replace(srv.URL, "https:", "wss:", 1) + "/prefix", HTTPClient: srv.Client(), Token: "test-binding"}
	d.opts.PackageHub = func() *bus.Client { return binding }
	d.mu.Lock()
	d.cfg.HubVersion, d.cfg.DevelopmentTarget, d.cfg.AutoUpdateNodes = target.Version, &target, true
	d.mu.Unlock()
}

func TestDevelopmentHostedRedeployAndWithdrawal(t *testing.T) {
	program := nodeproto.ProgramPersonad
	fixture := updatetest.Program(t, program, otherOS, otherArch)
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	target := fixtureDevelopment(strings.Repeat("a", 40))
	dir := t.TempDir()
	name := nodeBinaryName(program, otherOS+"-"+otherArch) + ".gz"
	var archive bytes.Buffer
	writer := gzip.NewWriter(&archive)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := update.BuildManifest(dir, update.Manifest{Version: target.Version, Commit: target.Commit, Build: target.Build})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"verified", "archive hash", "binary hash", "withdrawn before commit"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PERSONA_HOME", t.TempDir())
			d, machine, node, _ := linkedDeployFixture(t, kindLink)
			machine.info.OS, machine.info.Arch = otherOS, otherArch
			node.expected, node.target = raw, target.Version
			var withdrawn atomic.Bool
			var downloads atomic.Int32
			m := *manifest
			if mode == "binary hash" {
				f := m.Files[otherOS+"-"+otherArch]
				f.BinarySHA256 = strings.Repeat("0", 64)
				m.Files = map[string]update.File{otherOS + "-" + otherArch: f}
			}
			bindDevelopment(t, d, target, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case withdrawn.Load():
					w.WriteHeader(http.StatusGone)
				case r.URL.Path == "/manifest.json":
					_ = json.NewEncoder(w).Encode(m)
				case r.URL.Path == "/"+name:
					downloads.Add(1)
					data := archive.Bytes()
					if mode == "archive hash" {
						data = make([]byte, len(data))
					}
					if mode == "withdrawn before commit" {
						withdrawn.Store(true)
					}
					_, _ = w.Write(data)
				default:
					http.NotFound(w, r)
				}
			})
			t.Setenv("PATH", t.TempDir()) // Path B works without either build tool.
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			err := d.Redeploy(ctx, machine.cfg.ID)
			node.mu.Lock()
			defer node.mu.Unlock()
			snap := machine.snapshot()
			if mode == "verified" {
				if err != nil || node.begin != 1 || node.commits != 1 || node.confirms != 1 || !bytes.Equal(node.data, raw) || snap.DeploySource != sourceHub {
					t.Fatalf("hosted node transaction: %v, begin/commit/confirm %d/%d/%d, source %q", err, node.begin, node.commits, node.confirms, snap.DeploySource)
				}
				pkg, err := d.lookupDevelopment(ctx, d.packageKey(otherOS, otherArch, program))
				if err != nil {
					t.Fatal(err)
				}
				path, source, err := d.fetchDevelopment(ctx, pkg)
				if err != nil || source != sourceHub || downloads.Load() != 1 {
					t.Fatalf("cache: %q %v", source, err)
				}
				if err := os.WriteFile(path, []byte("damaged cached file"), 0700); err != nil {
					t.Fatal(err)
				}
				if _, _, err := d.fetchDevelopment(ctx, pkg); err != nil || downloads.Load() != 2 {
					t.Fatalf("cache repair: %v", err)
				}
			} else if err == nil || node.commits != 0 || node.confirms != 0 {
				t.Fatalf("unsafe transaction: %v, commit/confirm %d/%d", err, node.commits, node.confirms)
			} else if mode == "withdrawn before commit" && node.aborts != 1 {
				t.Fatal("withdrawn transfer was not aborted")
			} else if mode != "withdrawn before commit" && (snap.DeploySource != "" || node.begin != 0) {
				t.Fatal("unverified download was used")
			}
		})
	}
}

func TestDevelopmentMissingToolsAndAuthority(t *testing.T) {
	target := fixtureDevelopment(strings.Repeat("b", 40))
	for _, status := range []int{http.StatusNotFound, http.StatusGone, http.StatusUnauthorized, http.StatusFound, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Setenv("PERSONA_HOME", t.TempDir())
			t.Setenv("PATH", t.TempDir())
			d := newTestDevices(t)
			var redirects atomic.Int32
			other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirects.Add(1) }))
			defer other.Close()
			var current atomic.Bool
			current.Store(true)
			bindDevelopment(t, d, target, func(w http.ResponseWriter, r *http.Request) {
				if !current.Load() {
					w.Header().Del(devicewire.DevelopmentCommitHeader)
				}
				if status == http.StatusFound {
					http.Redirect(w, r, other.URL, status)
					return
				}
				if status == http.StatusOK {
					_ = json.NewEncoder(w).Encode(update.Manifest{Version: target.Version, Build: target.Build, Commit: strings.Repeat("c", 40), Files: map[string]update.File{"linux-amd64": {}}})
					return
				}
				w.WriteHeader(status)
			})
			lookup := func() error { _, err := d.nodePackageFor(t.Context(), otherOS, otherArch, "personad"); return err }
			err := lookup()
			if err == nil || redirects.Load() != 0 {
				t.Fatalf("unexpected lookup %v redirects %d", err, redirects.Load())
			}
			var message *i18n.Message
			missing := errors.As(err, &message) && message.Key == "devices-deploy.development_tools"
			if missing != (status == http.StatusNotFound) {
				t.Fatalf("wrong fallback decision for %d: %v", status, err)
			}
			if missing {
				for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
					got := i18n.DeploymentText(lang, err.Error())
					want := i18n.T(lang, "devices-deploy.development_tools", i18n.Args{"os": otherOS, "arch": otherArch})
					if got != want || !strings.Contains(got, "Git") || !strings.Contains(got, "Go") {
						t.Fatalf("%s: %s", lang, got)
					}
				}
				current.Store(false)
				if err := d.CheckDevelopmentTarget(t.Context(), &target); err == nil {
					t.Fatal("generic proxy 404 authorized a source build")
				}
			}
			d.mu.Lock()
			d.cfg.DevelopmentHold = true
			d.mu.Unlock()
			if d.DevelopmentTarget() != nil || d.CheckDevelopmentTarget(t.Context(), &target) == nil {
				t.Fatal("withheld target accepted")
			}
		})
	}
}

func TestDevelopmentBuildUsesDetachedCommitAndCache(t *testing.T) {
	t.Setenv("PERSONA_HOME", t.TempDir())
	repo := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// The test's own global config: GIT_CONFIG_GLOBAL needs Git 2.32, and older Git (Debian 11: 2.30) writes the
	// insteadOf below to $HOME/.gitconfig instead, where it outlives the test and sends every later fetch of the
	// repository to a deleted directory. A HOME of its own holding the same file covers both.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git(repo, "init", "-q")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "fixture@example.com")
	git(repo, "config", "--global", "url.file://"+filepath.ToSlash(repo)+".insteadOf", devicewire.DevelopmentRepository)
	write("server/go.mod", "module example.com/fixture\n\ngo 1.22\n")
	write("server/build-tags.txt", "public")
	write("server/internal/runtimepkg/runtime.go", "package runtimepkg\n")
	write("server/cmd/personad/main.go", `package main
import ("fmt"; "os")
var version, channel, build, commit string
func main() {
	if os.Args[1] == "commit" { fmt.Println(commit); return }
	fmt.Printf("%s\nchannel %s, build %s\n",version,channel,build)
}
`)
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "Pinned fixture")
	target := fixtureDevelopment(git(repo, "rev-parse", "HEAD"))
	write("server/cmd/personad/main.go", "this newer commit must never be built")
	git(repo, "commit", "-qam", "Advance the fixture")
	d := newTestDevices(t)
	bindDevelopment(t, d, target, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	p, err := d.nodePackageFor(t.Context(), runtime.GOOS, runtime.GOARCH, "personad")
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	bin, err := buildDevelopmentPackage(t.Context(), stage, p)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(stage, "source")
	if git(checkout, "rev-parse", "HEAD") != target.Commit || git(checkout, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" || git(checkout, "rev-list", "--count", "HEAD") != "1" {
		t.Fatal("source is not exactly the shallow detached commit")
	}
	if err := checkPackageBuild(t.Context(), bin, p); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "commit").Output(); err != nil || strings.TrimSpace(string(out)) != target.Commit {
		t.Fatal("missing embedded commit", err)
	}
	machine := newMachine(d, MachineConfig{ID: "fixture-node"})
	machine.deploying = true
	path, err := machine.fetchDeployment(t.Context(), p)
	if err != nil || machine.snapshot().DeploySource != sourceBuild {
		t.Fatalf("local build deployment: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	p, err = d.nodePackageFor(t.Context(), runtime.GOOS, runtime.GOARCH, "personad")
	if err != nil {
		t.Fatal(err)
	}
	got, source, err := d.fetchDevelopment(t.Context(), p)
	if err != nil || got != path || source != sourceBuild {
		t.Fatalf("cached fallback: %q %v", source, err)
	}
	if err := os.WriteFile(path, []byte("changed after build"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := d.nodePackageFor(t.Context(), runtime.GOOS, runtime.GOARCH, "personad"); err == nil {
		t.Fatal("corrupt cache used without a toolchain")
	}
	d.mu.Lock()
	d.cfg.DevelopmentTarget = nil
	d.cfg.HubVersion = "release"
	d.mu.Unlock()
	if machine.snapshot().DeploySource != "" {
		t.Fatal("previous deployment reported for a different target")
	}
}

func TestDevelopmentManagerWaitsForItsComputers(t *testing.T) {
	d, m, _, _ := linkedDeployFixture(t, kindLink)
	m.sessions = []proto.Session{{Status: proto.StatusWorking}}
	if d.DevelopmentIdle() {
		t.Fatal("manager with a working node considered idle")
	}
	m.sessions = nil
	m.deploying = true
	if d.DevelopmentIdle() {
		t.Fatal("manager with a deployment considered idle")
	}
	m.deploying = false
	if !d.DevelopmentIdle() {
		t.Fatal("idle manager considered busy")
	}
}
