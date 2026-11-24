package nodeupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSigner stands in for nodesign: "signing" appends a trailer naming how, which changes the file's sha256 as a
// real signature does and leaves it runnable (Go programs on Linux and macOS run with bytes after their end).
type fakeSigner struct {
	certErr, adhocErr error
	// during runs inside Sign, before it returns: a deployment racing the signing.
	during func()
}

func trailer(kind string) []byte { return []byte("\nfake-signature:" + kind + "\n") }

func signedAs(path, kind string) bool {
	b, err := os.ReadFile(path)
	return err == nil && bytes.HasSuffix(b, trailer(kind))
}

func appendTrailer(path, kind string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	_, err = f.Write(trailer(kind))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (f *fakeSigner) Meets(_ context.Context, path string) bool { return signedAs(path, "cert") }

func (f *fakeSigner) Sign(_ context.Context, path string) error {
	if f.during != nil {
		f.during()
	}
	if f.certErr != nil {
		return f.certErr
	}
	return appendTrailer(path, "cert")
}

func (f *fakeSigner) AdHoc(_ context.Context, path string) error {
	if f.adhocErr != nil {
		return f.adhocErr
	}
	return appendTrailer(path, "adhoc")
}

// signLog collects what the manager logs about signing.
type signLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *signLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *signLog) has(part string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, part) {
			return true
		}
	}
	return false
}

func signingManager(t *testing.T, s *fakeSigner) (*Manager, []byte, *signLog) {
	t.Helper()
	m, data, _ := testManager(t)
	logs := &signLog{}
	m.signer, m.logf = s, logs.logf
	return m, data, logs
}

// stages lists the staged files left beside the program.
func stages(t *testing.T, m *Manager) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(m.exe), ".node-update-stage-*"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// The update installs its signed copy, and the transaction follows that file: the new node confirms it, although the
// installed program's sha256 is no longer the one the hub sent.
func TestSignedCommitConfirmsTheSignedFile(t *testing.T) {
	m, data, logs := signingManager(t, &fakeSigner{})
	sent := spec(data, "next")
	id := receive(t, m, data, sent)
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	if !signedAs(m.exe, "cert") {
		t.Fatal("the installed program is not the signed copy")
	}
	installed := hash(t, m.exe)
	if installed == sent.SHA256 {
		t.Fatal("signing did not change the file: this test proves nothing")
	}
	p, err := loadPending(markerPath(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	if p.NewHash != installed {
		t.Fatalf("the transaction records %s, the installed program is %s", p.NewHash, installed)
	}
	if left := stages(t, m); len(left) != 0 {
		t.Fatalf("staged files left: %v", left)
	}
	if check, err := runCheck(context.Background(), m.exe); err != nil || check.Version != "next" {
		t.Fatalf("the signed program does not run: %+v %v", check, err)
	}
	if !logs.has("signed with the certificate") {
		t.Fatalf("no log of the signing: %q", logs.lines)
	}
	running := &Manager{exe: m.exe, version: "next", startWatch: func(string, string, string) error { return nil }}
	if err := running.recover(); err != nil {
		t.Fatal(err)
	}
	if running.RestartRequired() {
		t.Fatal("the new node took its signed program for someone else's")
	}
	if err := running.Confirm("next"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(markerPath(m.exe)); !os.IsNotExist(err) {
		t.Fatal("the confirmed transaction remains")
	}
}

func TestAdoptionFollowsSigningPolicy(t *testing.T) {
	unavailable := errors.New("signing unavailable")
	for _, tc := range []struct {
		name, installed                  string
		signer                           fakeSigner
		legacy, uploadSigned, superseded bool
	}{
		{name: "matching certificate", installed: "cert"},
		{name: "upload already meets configured signer", installed: "cert", uploadSigned: true},
		{name: "matching ad hoc fallback", signer: fakeSigner{certErr: unavailable}, installed: "adhoc"},
		{name: "as uploaded after both signing attempts fail", signer: fakeSigner{certErr: unavailable, adhocErr: unavailable}},
		{name: "unsigned despite available certificate", superseded: true},
		{name: "legacy could re-sign fallback", signer: fakeSigner{certErr: unavailable}, installed: "adhoc", legacy: true, superseded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.legacy {
				t.Setenv("FIXTURE_LEGACY_ADOPTION", "1")
			}
			m, data, _ := signingManager(t, &tc.signer)
			refuseWatch(t, m)
			installed := data
			if tc.installed != "" {
				installed = append(bytes.Clone(data), trailer(tc.installed)...)
			}
			if tc.uploadSigned {
				data = installed
			}
			onDisk := installExternally(t, m, installed)
			wantHash := hash(t, m.exe)
			id := receive(t, m, data, spec(data, "next"))
			result, err := m.Commit(t.Context(), id, nil)
			if tc.superseded {
				if !errors.Is(err, errSuperseded) {
					t.Fatalf("commit error = %v, want errSuperseded", err)
				}
				if left := stages(t, m); len(left) != 1 || left[0] != m.upload.Path {
					t.Fatalf("refused commit left extra signed stages: %v", left)
				}
				assertInstalledUntouched(t, m.exe, onDisk, wantHash)
				return
			}
			if err != nil || !result.Accepted {
				t.Fatalf("commit result = %+v, %v", result, err)
			}
			p, err := loadPending(markerPath(m.exe))
			if err != nil || p.NewHash != wantHash {
				t.Fatalf("confirmation does not track installed hash %s: %+v, %v", wantHash, p, err)
			}
			if left := stages(t, m); len(left) != 0 {
				t.Fatalf("adoption left staged files: %v", left)
			}
			// Signing may now succeed, including after an earlier fallback. The
			// normal reload and startup signing calls must leave the file alone.
			m.signer = &fakeSigner{}
			if m.SignInstalled(t.Context()) {
				t.Fatal("reload signed over the adopted executable")
			}
			fresh := &Manager{exe: m.exe, version: "next", signer: m.signer, startWatch: m.startWatch}
			if err := fresh.recover(); err != nil || fresh.RestartRequired() {
				t.Fatalf("adopted target should recover without another restart: %v", err)
			}
			if fresh.SignInstalled(t.Context()) {
				t.Fatal("startup signed over the adopted executable")
			}
			if err := fresh.Confirm("next"); err != nil {
				t.Fatal(err)
			}
			assertInstalledUntouched(t, m.exe, onDisk, wantHash)
		})
	}
}

// A signed update that never confirms is still rolled back: the rollback recognizes its own (signed) file. With the
// upload's sha256 in the marker it would take the file for another deployment's and keep the broken build.
func TestSignedCommitRollsBackUnconfirmed(t *testing.T) {
	m, data, _ := signingManager(t, &fakeSigner{})
	old := hash(t, m.exe)
	m.timeout = -time.Second // the confirmation deadline has passed by the time the new node starts
	id := receive(t, m, data, spec(data, "next"))
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	running := &Manager{exe: m.exe, version: "next", startWatch: func(string, string, string) error { return nil }}
	if err := running.recover(); err != nil {
		t.Fatal(err)
	}
	if !running.RestartRequired() || hash(t, m.exe) != old {
		t.Fatal("the unconfirmed signed update was not rolled back")
	}
}

// Signing with the certificate fails (no identity, a keychain nobody unlocks): the update still goes in, signed ad hoc,
// and confirms.
func TestCommitFallsBackToAdHoc(t *testing.T) {
	m, data, logs := signingManager(t, &fakeSigner{certErr: errors.New("codesign with the certificate: no identity found")})
	id := receive(t, m, data, spec(data, "next"))
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	if !signedAs(m.exe, "adhoc") {
		t.Fatal("the update did not install signed ad hoc")
	}
	if !logs.has("no identity found") || !logs.has("installs signed ad hoc") {
		t.Fatalf("the fallback is not logged: %q", logs.lines)
	}
	running := &Manager{exe: m.exe, version: "next", startWatch: func(string, string, string) error { return nil }}
	if err := running.recover(); err != nil {
		t.Fatal(err)
	}
	if err := running.Confirm("next"); err != nil {
		t.Fatal(err)
	}
}

// Neither signing works: the upload goes in as it came, exactly as without a signer.
func TestCommitInstallsTheUploadWhenSigningFails(t *testing.T) {
	m, data, logs := signingManager(t, &fakeSigner{certErr: errors.New("cert"), adhocErr: errors.New("adhoc")})
	sent := spec(data, "next")
	id := receive(t, m, data, sent)
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	if hash(t, m.exe) != sent.SHA256 {
		t.Fatal("the installed program is not the upload")
	}
	if !logs.has("with the signature it came with") {
		t.Fatalf("not logged: %q", logs.lines)
	}
	if left := stages(t, m); len(left) != 0 {
		t.Fatalf("staged files left: %v", left)
	}
	running := &Manager{exe: m.exe, version: "next", startWatch: func(string, string, string) error { return nil }}
	if err := running.recover(); err != nil {
		t.Fatal(err)
	}
	if err := running.Confirm("next"); err != nil {
		t.Fatal(err)
	}
}

// A commit that stops after signing (an agent became busy) keeps the upload as it came, so a retry checks it against
// the hub's sha256 again, and leaves no signed copy behind.
func TestRefusedSignedCommitKeepsTheUploadForARetry(t *testing.T) {
	m, data, _ := signingManager(t, &fakeSigner{})
	sent := spec(data, "next")
	id := receive(t, m, data, sent)
	before := hash(t, m.exe)
	if _, err := m.Commit(context.Background(), id, func() error { return errors.New("busy") }); err == nil {
		t.Fatal("a busy node committed")
	}
	if hash(t, m.exe) != before {
		t.Fatal("the refused commit changed the program")
	}
	left := stages(t, m)
	if len(left) != 1 || left[0] != m.upload.Path || hash(t, left[0]) != sent.SHA256 {
		t.Fatalf("want only the unchanged upload left, got %v", left)
	}
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	if !signedAs(m.exe, "cert") {
		t.Fatal("the retried commit did not install the signed copy")
	}
}

// A build another path put in place is signed when the resident node starts (or reloads), once.
func TestSignInstalledSignsAnUnsignedProgramOnce(t *testing.T) {
	m, _, logs := signingManager(t, &fakeSigner{})
	var err error
	if m.identity, err = os.Stat(m.exe); err != nil {
		t.Fatal(err)
	}
	if !m.SignInstalled(context.Background()) {
		t.Fatalf("not signed: %q", logs.lines)
	}
	if !signedAs(m.exe, "cert") {
		t.Fatal("the program is not the signed copy")
	}
	if check, err := runCheck(context.Background(), m.exe); err != nil || check.Version != "old" {
		t.Fatalf("the signed program does not run: %+v %v", check, err)
	}
	if left := stages(t, m); len(left) != 0 {
		t.Fatalf("staged files left: %v", left)
	}
	if m.SignInstalled(context.Background()) {
		t.Fatal("a signed program was signed again")
	}
	// The manager follows the new file: an update committed afterwards is not refused as superseded.
	data, err := os.ReadFile(filepath.Join(fixtureDir, "next"))
	if err != nil {
		t.Fatal(err)
	}
	id := receive(t, m, data, spec(data, "next"))
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
}

// Without a signer, with an update awaiting confirmation, or when signing fails, the program stays exactly as it is.
func TestSignInstalledLeavesTheProgram(t *testing.T) {
	cases := map[string]func(*testing.T, *Manager){
		"no signer": func(t *testing.T, m *Manager) { m.signer = nil },
		"pending update": func(t *testing.T, m *Manager) {
			if err := os.WriteFile(markerPath(m.exe), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"signing fails": func(t *testing.T, m *Manager) {
			m.signer = &fakeSigner{certErr: errors.New("errSecInternalComponent")}
		},
		"deployed meanwhile": func(t *testing.T, m *Manager) {
			// A deploy renames another build over the program while the copy is being signed: it stays.
			m.signer = &fakeSigner{during: func() {
				_, next := fixtures(t)
				if err := copyFile(next, m.exe+".deploy"); err != nil {
					t.Error(err)
				}
				if err := os.Rename(m.exe+".deploy", m.exe); err != nil {
					t.Error(err)
				}
			}}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			m, _, logs := signingManager(t, &fakeSigner{})
			setup(t, m)
			before := hash(t, m.exe)
			if name == "deployed meanwhile" {
				_, next := fixtures(t)
				before = hash(t, next)
			}
			if m.SignInstalled(context.Background()) {
				t.Fatal("signed")
			}
			if hash(t, m.exe) != before {
				t.Fatal("the program changed")
			}
			if left := stages(t, m); len(left) != 0 {
				t.Fatalf("staged files left: %v", left)
			}
			if name == "signing fails" && !logs.has("keeps its signature") {
				t.Fatalf("the failure is not logged: %q", logs.lines)
			}
		})
	}
}
