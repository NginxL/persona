package backup

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/secretkey"
)

type fakeDatabase struct {
	dump     string
	identity DatabaseIdentity
}

type fakeBackend struct {
	mu              sync.Mutex
	servers         map[string]map[string]*fakeDatabase
	identities      map[string]DatabaseIdentity
	created         []string
	dropped         []string
	renamed         []string
	restoreErr      error
	restorePart     string
	dumpErr         error
	dumpPart        string
	failInstallPart string
	installFailed   bool
	installErr      error
	// commitThenFail makes one rename of the named part succeed and then report
	// an error, as a connection lost after the server committed would.
	commitThenFail  string // "<part>:install" or "<part>:previous"
	commitThenFired bool
	// shape of an install rename that reports an error after the fault: "" the
	// rename happened, "both" the destination exists next to the source,
	// "neither" the source is gone and the destination was never made.
	faultShape string
	// existsErr makes every Exists fail after the fault; down makes every
	// Exists, Rename and Drop fail (the server is unreachable).
	existsErr bool
	down      bool
	// preCommit makes one rename ("<part>:previous" or "<part>:install") fail
	// without happening; preCommitDown also takes the server down with it.
	preCommit     string
	preCommitDown bool
	preFired      bool
	downNow       bool
	// existsFailures Exists calls fail (armed by the injected failure).
	existsFailures    int
	armExistsFailures int
	// existsErrName makes Exists of that name fail once a fault has fired.
	existsErrName   string
	existsErrPrefix string
	// failUndoPart makes undoing an install of that part fail.
	failUndoPart string
	active       map[string]int
	clientErr    error
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{servers: map[string]map[string]*fakeDatabase{}, identities: map[string]DatabaseIdentity{}, active: map[string]int{}}
}

func fakeServer(target Target) string {
	u, _ := url.Parse(target.URL)
	return u.Host
}

func (f *fakeBackend) server(target Target) map[string]*fakeDatabase {
	name := fakeServer(target)
	if f.servers[name] == nil {
		f.servers[name] = map[string]*fakeDatabase{}
	}
	return f.servers[name]
}

func (f *fakeBackend) add(target Target, name, dump string) {
	f.server(target)[name] = &fakeDatabase{dump: dump, identity: f.identities[dump]}
}

func (f *fakeBackend) Identity(_ context.Context, target Target, part, database string) (DatabaseIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if database == "" {
		database = target.Database
	}
	db := f.server(target)[database]
	if db == nil {
		return DatabaseIdentity{}, fmt.Errorf("%s database %s is missing", part, database)
	}
	identity := db.identity
	identity.Schema = append([]Migration(nil), identity.Schema...)
	return identity, nil
}

func (f *fakeBackend) Quiesced(_ context.Context, target Target, database string, missingOK bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if database == "" {
		database = target.Database
	}
	if f.server(target)[database] == nil {
		if missingOK {
			return nil
		}
		return fmt.Errorf("%s database is missing", target.Part)
	}
	if f.active[target.Part+":"+database] != 0 {
		return errors.New("writes are still active")
	}
	return nil
}

func (f *fakeBackend) Dump(_ context.Context, target Target, destination string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dumpErr != nil && (f.dumpPart == "" || f.dumpPart == target.Part) {
		return f.dumpErr
	}
	db := f.server(target)[target.Database]
	if db == nil {
		return errors.New("database disappeared before dump")
	}
	return os.WriteFile(destination, []byte(db.dump), 0o600)
}

func (f *fakeBackend) Create(_ context.Context, target Target, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.server(target)[name] != nil {
		return errors.New("duplicate temporary database")
	}
	f.server(target)[name] = &fakeDatabase{}
	f.created = append(f.created, name)
	return nil
}

func (f *fakeBackend) Restore(_ context.Context, target Target, database, dumpPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.restoreErr != nil && (f.restorePart == "" || f.restorePart == target.Part) {
		return f.restoreErr
	}
	raw, err := os.ReadFile(dumpPath)
	if err != nil {
		return err
	}
	identity, ok := f.identities[string(raw)]
	if !ok {
		return errors.New("unknown fake dump")
	}
	db := f.server(target)[database]
	if db == nil {
		return errors.New("temporary database is missing")
	}
	db.dump, db.identity = string(raw), identity
	return nil
}

func (f *fakeBackend) Drop(_ context.Context, target Target, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.downNow || (f.down && f.commitThenFired) {
		return errors.New("server unreachable")
	}
	delete(f.server(target), name)
	f.dropped = append(f.dropped, name)
	return nil
}

func (f *fakeBackend) Rename(_ context.Context, target Target, from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.downNow || (f.down && f.commitThenFired) {
		return errors.New("server unreachable")
	}
	if f.failUndoPart == target.Part && from == target.Database && strings.HasPrefix(to, "persona_staged_") {
		return errors.New("injected refusal to undo the install")
	}
	if f.preCommit != "" && !f.preFired {
		kind := ""
		switch {
		case strings.HasPrefix(from, "persona_staged_") && to == target.Database:
			kind = "install"
		case from == target.Database && strings.HasPrefix(to, "persona_previous_"):
			kind = "previous"
		}
		if kind != "" && f.preCommit == target.Part+":"+kind {
			f.preFired = true
			f.downNow = f.preCommitDown
			f.existsFailures = f.armExistsFailures
			return errors.New("injected failure before the rename happened")
		}
	}
	if f.failInstallPart == target.Part && !f.installFailed && strings.HasPrefix(from, "persona_staged_") && to == target.Database {
		f.installFailed = true
		f.existsFailures = f.armExistsFailures
		if f.installErr != nil {
			return f.installErr
		}
		return errors.New("injected staged database installation failure")
	}
	server := f.server(target)
	if server[from] == nil || server[to] != nil {
		return fmt.Errorf("invalid rename %s to %s", from, to)
	}
	server[to] = server[from]
	if !(f.faultShape == "both" && !f.commitThenFired && f.commitThenFail == target.Part+":install" && strings.HasPrefix(from, "persona_staged_")) {
		delete(server, from)
	}
	if f.faultShape == "neither" && !f.commitThenFired && f.commitThenFail == target.Part+":install" && strings.HasPrefix(from, "persona_staged_") {
		delete(server, to)
	}
	f.renamed = append(f.renamed, from+"->"+to)
	if !f.commitThenFired && f.commitThenFail != "" {
		kind := ""
		switch {
		case strings.HasPrefix(from, "persona_staged_") && to == target.Database:
			kind = "install"
		case from == target.Database && strings.HasPrefix(to, "persona_previous_"):
			kind = "previous"
		}
		if kind != "" && f.commitThenFail == target.Part+":"+kind {
			f.commitThenFired = true
			return errors.New("injected connection loss after the rename committed")
		}
	}
	return nil
}

func (f *fakeBackend) CheckClients(Target, ...string) error { return f.clientErr }

func (f *fakeBackend) Exists(_ context.Context, target Target, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.downNow || ((f.existsErr || f.down) && f.commitThenFired) {
		return false, errors.New("cannot check the database")
	}
	if f.existsFailures > 0 {
		f.existsFailures--
		return false, errors.New("cannot check the database (briefly)")
	}
	if f.commitThenFired && f.existsErrPrefix != "" && strings.HasPrefix(name, f.existsErrPrefix) {
		return false, errors.New("cannot check that database")
	}
	if f.existsErrName == name && f.commitThenFired {
		return false, errors.New("cannot check that database")
	}
	return f.server(target)[name] != nil, nil
}

type backupFixture struct {
	home       string
	output     string
	hub        Target
	manager    Target
	backend    *fakeBackend
	managerKey []byte
}

func newBackupFixture(t *testing.T, managerName string) *backupFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "runtime")
	output := filepath.Join(root, "out", "install.backup")
	hubURL := "postgres://persona:hub-secret@db.example:5432/persona?sslmode=disable"
	managerURL, err := withDatabase(hubURL, managerName)
	if err != nil {
		fatal(t, err)
	}
	writeFixtureFile(t, filepath.Join(home, "hub.json"), []byte(`{"database_url":"`+hubURL+`","token":"hub-token-secret"}`))
	writeFixtureFile(t, filepath.Join(home, "managers", "assistant", "manager.json"), []byte(`{"database_name":"`+managerName+`","usage":{"sources":[{"key":"usage-secret"}]}}`))
	writeFixtureFile(t, filepath.Join(home, "secret.key"), bytes.Repeat([]byte{1}, secretkey.Size))
	managerKey := bytes.Repeat([]byte{2}, secretkey.Size)
	writeFixtureFile(t, filepath.Join(home, "managers", "assistant", "secret.key"), managerKey)
	writeFixtureFile(t, filepath.Join(home, "managers", "assistant", "tailcat-hub.json"), []byte("tailcat-key"))
	writeFixtureFile(t, filepath.Join(home, "managers", "assistant", "handles.key"), []byte("handles-key"))
	writeFixtureFile(t, filepath.Join(home, "data", "facts", "note.md"), []byte("original knowledge"))
	writeFixtureFile(t, filepath.Join(home, "extensions", "example", "manifest.json"), []byte("{}"))
	writeFixtureFile(t, filepath.Join(home, "extensions", "example", "run"), []byte("extension executable"))
	if err := os.Chmod(filepath.Join(home, "extensions", "example", "run"), 0o700); err != nil {
		fatal(t, err)
	}
	writeFixtureFile(t, filepath.Join(home, "managers", "m-abcdef", "manager.json"), []byte("independent manager"))

	backend := newFakeBackend()
	hub := Target{Part: "hub", URL: hubURL, Database: "persona"}
	manager := Target{Part: "manager", URL: managerURL, Database: managerName}
	backend.identities["hub-one"] = DatabaseIdentity{ServerVersion: "18.1", Schema: []Migration{{Version: 1, Name: "0001_hub.sql", Part: "hub"}}}
	backend.identities["manager-one"] = DatabaseIdentity{ServerVersion: "18.1", Schema: []Migration{{Version: 430, Name: "0430_sealing_key.sql", Part: "manager"}}, SealingKey: secretkey.Fingerprint(managerKey)}
	backend.identities["hub-two"] = DatabaseIdentity{ServerVersion: "18.1", Schema: []Migration{{Version: 2, Name: "0002_hub.sql", Part: "hub"}}}
	backend.identities["manager-two"] = DatabaseIdentity{ServerVersion: "18.1", Schema: []Migration{{Version: 431, Name: "0431_manager.sql", Part: "manager"}}, SealingKey: secretkey.Fingerprint(managerKey)}
	backend.add(hub, hub.Database, "hub-one")
	backend.add(manager, manager.Database, "manager-one")
	return &backupFixture{home: home, output: output, hub: hub, manager: manager, backend: backend, managerKey: managerKey}
}

func writeFixtureFile(t *testing.T, name string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		fatal(t, err)
	}
	if err := os.WriteFile(name, content, 0o600); err != nil {
		fatal(t, err)
	}
}

func (f *backupFixture) operator() *Operator {
	return &Operator{backend: f.backend, now: func() time.Time { return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC) }, random: rand.Reader}
}

func (f *backupFixture) options() Options {
	return Options{Home: f.home, Output: f.output, WritesQuiesced: true}
}

func (f *backupFixture) mutate(t *testing.T) {
	t.Helper()
	f.backend.add(f.hub, f.hub.Database, "hub-two")
	f.backend.add(f.manager, f.manager.Database, "manager-two")
	writeFixtureFile(t, filepath.Join(f.home, "hub.json"), []byte(`{"database_url":"`+f.hub.URL+`","token":"changed-token"}`))
	writeFixtureFile(t, filepath.Join(f.home, "data", "facts", "note.md"), []byte("changed knowledge"))
	writeFixtureFile(t, filepath.Join(f.home, "managers", "m-abcdef", "manager.json"), []byte("independent manager changed"))
}

func assertNoTemporaryDatabases(t *testing.T, backend *fakeBackend) {
	t.Helper()
	for _, server := range backend.servers {
		for name := range server {
			if strings.HasPrefix(name, "persona_verify_") || strings.HasPrefix(name, "persona_staged_") {
				errorf(t, "temporary database %s remains", name)
			}
		}
	}
}

func TestWholeInstallCreateVerifyRestore(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	operator := fixture.operator()
	created, err := operator.Create(context.Background(), fixture.options())
	if err != nil {
		fatal(t, err)
	}
	if !created.Verified {
		fatal(t, "create returned before temporary-database verification")
	}
	info, err := os.Stat(fixture.output)
	if err != nil || info.Mode().Perm() != 0o600 {
		fatalf(t, "bundle mode = %v, %v", info.Mode(), err)
	}
	manifest, _, err := readBundleManifest(fixture.output)
	if err != nil {
		fatal(t, err)
	}
	if manifest.Manager.SealingKey != secretkey.Fingerprint(fixture.managerKey) || len(manifest.Files) < 8 {
		fatalf(t, "incomplete manifest: %+v", manifest)
	}
	if _, err := operator.Verify(context.Background(), Options{Home: fixture.home, Bundle: fixture.output}); err != nil {
		fatal(t, err)
	}
	assertNoTemporaryDatabases(t, fixture.backend)

	fixture.mutate(t)
	restored, err := operator.Restore(context.Background(), Options{Home: fixture.home, Bundle: fixture.output, WritesQuiesced: true})
	if err != nil {
		fatal(t, err)
	}
	if !restored.Restored || restored.PreviousHubDB == "" || restored.PreviousManagerDB == "" || restored.PreviousFiles == "" {
		fatalf(t, "restore did not preserve predecessors: %+v", restored)
	}
	if got := fixture.backend.server(fixture.hub)[fixture.hub.Database].dump; got != "hub-one" {
		fatalf(t, "hub dump after restore = %q", got)
	}
	if got := fixture.backend.server(fixture.manager)[fixture.manager.Database].dump; got != "manager-one" {
		fatalf(t, "manager dump after restore = %q", got)
	}
	if got := fixture.backend.server(fixture.hub)[restored.PreviousHubDB].dump; got != "hub-two" {
		fatalf(t, "previous hub dump = %q", got)
	}
	if got := fixture.backend.server(fixture.manager)[restored.PreviousManagerDB].dump; got != "manager-two" {
		fatalf(t, "previous manager dump = %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(fixture.home, "hub.json"))
	if err != nil || !bytes.Contains(raw, []byte("hub-token-secret")) {
		fatalf(t, "hub.json was not restored: %q, %v", raw, err)
	}
	raw, _ = os.ReadFile(filepath.Join(fixture.home, "data", "facts", "note.md"))
	if string(raw) != "original knowledge" {
		fatalf(t, "knowledge after restore = %q", raw)
	}
	raw, _ = os.ReadFile(filepath.Join(fixture.home, "managers", "m-abcdef", "manager.json"))
	if string(raw) != "independent manager changed" {
		fatalf(t, "independent manager was touched: %q", raw)
	}
	if info, err := os.Stat(filepath.Join(fixture.home, "extensions", "example", "run")); err != nil || info.Mode().Perm() != 0o700 {
		errorf(t, "extension executable mode = %v, %v", info.Mode(), err)
	}
	assertNoTemporaryDatabases(t, fixture.backend)
}

func writeTLSIdentity(t *testing.T, home string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fatal(t, err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(12422),
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		fatal(t, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		fatal(t, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		fatal(t, err)
	}
	writeFixtureFile(t, filepath.Join(home, "tls", "hub.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	writeFixtureFile(t, filepath.Join(home, "tls", "hub.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return link.Pin(cert)
}

func readTLSFingerprint(t *testing.T, home string) string {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(filepath.Join(home, "tls", "hub.crt"), filepath.Join(home, "tls", "hub.key"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		fatal(t, err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		fatal(t, err)
	}
	return link.Pin(leaf)
}

func TestWholeInstallRestorePreservesTLSFingerprint(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	fingerprint := writeTLSIdentity(t, fixture.home)
	if err := managerconf.WriteBinding(fixture.home, managerconf.Binding{
		Hub: "https://hub.example:18662", HubID: "synthetic-hub", Token: "synthetic-manager-token", Fingerprint: fingerprint,
	}); err != nil {
		fatal(t, err)
	}
	operator := fixture.operator()
	if _, err := operator.Create(context.Background(), fixture.options()); err != nil {
		fatal(t, err)
	}
	if err := os.RemoveAll(fixture.home); err != nil {
		fatal(t, err)
	}
	if _, err := operator.Restore(context.Background(), Options{Home: fixture.home, Bundle: fixture.output, WritesQuiesced: true}); err != nil {
		fatal(t, err)
	}
	binding, err := managerconf.ReadBinding(fixture.home)
	if err != nil {
		fatal(t, err)
	}
	if binding.Fingerprint != fingerprint {
		fatalf(t, "manager fingerprint after restore = %q; want original %q", binding.Fingerprint, fingerprint)
	}
	if restored := readTLSFingerprint(t, fixture.home); restored != binding.Fingerprint {
		fatalf(t, "hub fingerprint after empty-runtime restore = %q, manager fingerprint = %q; want original %q", restored, binding.Fingerprint, fingerprint)
	}
}

func TestRestoreFailureRollsBackFilesAndBothDatabases(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	operator := fixture.operator()
	if _, err := operator.Create(context.Background(), fixture.options()); err != nil {
		fatal(t, err)
	}
	fixture.mutate(t)
	fixture.backend.failInstallPart = "manager"
	_, err := operator.Restore(context.Background(), Options{Home: fixture.home, Bundle: fixture.output, WritesQuiesced: true})
	if err == nil || !strings.Contains(err.Error(), "injected") {
		fatalf(t, "restore error = %v", err)
	}
	if got := fixture.backend.server(fixture.hub)[fixture.hub.Database].dump; got != "hub-two" {
		fatalf(t, "hub was not rolled back: %q", got)
	}
	if got := fixture.backend.server(fixture.manager)[fixture.manager.Database].dump; got != "manager-two" {
		fatalf(t, "manager was not rolled back: %q", got)
	}
	raw, _ := os.ReadFile(filepath.Join(fixture.home, "hub.json"))
	if !bytes.Contains(raw, []byte("changed-token")) {
		fatalf(t, "files were not rolled back: %q", raw)
	}
	raw, _ = os.ReadFile(filepath.Join(fixture.home, "data", "facts", "note.md"))
	if string(raw) != "changed knowledge" {
		fatalf(t, "knowledge was not rolled back: %q", raw)
	}
	assertNoTemporaryDatabases(t, fixture.backend)
	for _, server := range fixture.backend.servers {
		for name := range server {
			if strings.HasPrefix(name, "persona_previous_") {
				errorf(t, "previous database %s was not renamed back", name)
			}
		}
	}
}

func TestCreateRejectsMissingManagerDatabase(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	delete(fixture.backend.server(fixture.manager), fixture.manager.Database)
	_, err := fixture.operator().Create(context.Background(), fixture.options())
	if err == nil || !strings.Contains(err.Error(), "missing") {
		fatalf(t, "missing manager error = %v", err)
	}
	if _, statErr := os.Stat(fixture.output); !errors.Is(statErr, os.ErrNotExist) {
		fatalf(t, "bundle exists after failure: %v", statErr)
	}
}

func TestCreateRejectsMismatchedCurrentKey(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	writeFixtureFile(t, filepath.Join(fixture.home, "managers", "assistant", "secret.key"), bytes.Repeat([]byte{9}, secretkey.Size))
	_, err := fixture.operator().Create(context.Background(), fixture.options())
	if err == nil || !strings.Contains(err.Error(), "different secret.key") {
		fatalf(t, "mismatched key error = %v", err)
	}
	// The operator needs to know what to do: the rotation resumes when the
	// manager starts, and cannot be rolled back once committed.
	for _, wanted := range []string{"start the built-in manager once", "resumes at startup", "stop it and retry", "cannot be rolled back"} {
		if !strings.Contains(err.Error(), wanted) {
			errorf(t, "mismatched key error lacks %q: %v", wanted, err)
		}
	}
	if _, statErr := os.Stat(fixture.output); !errors.Is(statErr, os.ErrNotExist) {
		fatalf(t, "bundle exists after key failure: %v", statErr)
	}
}

func TestInterruptedVerificationPublishesNothingAndCleansDatabases(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	fixture.backend.restoreErr = context.Canceled
	_, err := fixture.operator().Create(context.Background(), fixture.options())
	if !errors.Is(err, context.Canceled) {
		fatalf(t, "interruption error = %v", err)
	}
	if _, statErr := os.Stat(fixture.output); !errors.Is(statErr, os.ErrNotExist) {
		fatalf(t, "bundle exists after interruption: %v", statErr)
	}
	assertNoTemporaryDatabases(t, fixture.backend)
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(fixture.output), ".persona-backup-*"))
	if len(matches) != 0 {
		fatalf(t, "staging remains after interruption: %v", matches)
	}
}

func TestLongManagerDatabaseNameDoesNotGrowTemporaryIdentifiers(t *testing.T) {
	longName := strings.Repeat("manager_with_a_very_long_name_", 8)
	fixture := newBackupFixture(t, longName)
	if _, err := fixture.operator().Create(context.Background(), fixture.options()); err != nil {
		fatal(t, err)
	}
	manifest, _, err := readBundleManifest(fixture.output)
	if err != nil || manifest.ManagerDatabase != longName {
		fatalf(t, "long manager identity was not retained: %v", err)
	}
	for _, name := range fixture.backend.created {
		if len(name) > 63 {
			errorf(t, "temporary identifier is %d bytes: %q", len(name), name)
		}
	}
}

func TestRestoreCanReplaceMissingExistingDatabases(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	operator := fixture.operator()
	if _, err := operator.Create(context.Background(), fixture.options()); err != nil {
		fatal(t, err)
	}
	delete(fixture.backend.server(fixture.hub), fixture.hub.Database)
	delete(fixture.backend.server(fixture.manager), fixture.manager.Database)
	result, err := operator.Restore(context.Background(), Options{Home: fixture.home, Bundle: fixture.output, WritesQuiesced: true})
	if err != nil {
		fatal(t, err)
	}
	if result.PreviousHubDB != "" || result.PreviousManagerDB != "" {
		fatalf(t, "missing predecessors were reported: %+v", result)
	}
	assertNoTemporaryDatabases(t, fixture.backend)
}
