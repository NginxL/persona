package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/secretkey"
)

// Operator coordinates bundle, database, and file transactions.
type Operator struct {
	backend Backend
	now     func() time.Time
	random  io.Reader
}

func NewOperator() *Operator {
	return &Operator{backend: newPostgresBackend(), now: time.Now, random: rand.Reader}
}

func (o *Operator) Create(ctx context.Context, opts Options) (_ Result, err error) {
	if !opts.WritesQuiesced {
		return Result{}, errors.New("create requires --writes-quiesced after stopping the hub and built-in manager")
	}
	if opts.Output == "" {
		return Result{}, errors.New("create requires --output")
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return Result{}, err
	}
	if err := refuseOutputInsideCollectedTree(opts.Home, output); err != nil {
		return Result{}, err
	}
	// The knowledge-base layout is checked before anything is staged or dumped
	// (collectPayload checks it again): a bundle restore would refuse is not
	// written.
	if home, err := filepath.Abs(opts.Home); err != nil {
		return Result{}, err
	} else if _, kbDir, err := knowledgeBaseLocator(home); err != nil {
		return Result{}, err
	} else if err := refuseKnowledgeBaseOverlap(home, kbDir); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return Result{}, err
	}
	staging, err := os.MkdirTemp(filepath.Dir(output), ".persona-backup-stage-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(staging)
	hub, manager, err := Discover(opts)
	if err != nil {
		return Result{}, err
	}
	if err := o.checkClients(hub, manager, "pg_dump", "pg_restore"); err != nil {
		return Result{}, err
	}
	for _, target := range []Target{hub, manager} {
		if err := o.backend.Quiesced(ctx, target, target.Database, false); err != nil {
			return Result{}, err
		}
	}
	hubIdentity, err := o.backend.Identity(ctx, hub, "hub", hub.Database)
	if err != nil {
		return Result{}, err
	}
	managerIdentity, err := o.backend.Identity(ctx, manager, "manager", manager.Database)
	if err != nil {
		return Result{}, err
	}
	// Sealed values are only for verification, not part of the identity kept.
	managerIdentity.SealedValues = nil
	manifest := &Manifest{
		Format: formatVersion, CreatedAt: o.now().UTC(), Hub: sortedIdentities(hubIdentity), Manager: sortedIdentities(managerIdentity),
		HubDatabase: hub.Database, ManagerDatabase: manager.Database,
	}
	if err := os.MkdirAll(filepath.Join(staging, "db"), 0o700); err != nil {
		return Result{}, err
	}
	if err := o.backend.Dump(ctx, hub, filepath.Join(staging, hubDumpPath)); err != nil {
		return Result{}, err
	}
	if err := o.backend.Dump(ctx, manager, filepath.Join(staging, managerDumpPath)); err != nil {
		return Result{}, err
	}
	for _, target := range []Target{hub, manager} {
		if err := o.backend.Quiesced(ctx, target, target.Database, false); err != nil {
			return Result{}, fmt.Errorf("writes resumed during backup: %w", err)
		}
	}
	notes, err := collectPayload(opts.Home, staging, manifest)
	if err != nil {
		return Result{}, err
	}
	if err := writeManifest(staging, manifest); err != nil {
		return Result{}, err
	}
	if err := o.verifyPayload(ctx, hub, manager, staging, manifest); err != nil {
		return Result{}, fmt.Errorf("temporary-database verification failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := publishBundle(ctx, staging, output); err != nil {
		return Result{}, err
	}
	return Result{Bundle: output, Verified: true, Notes: notes}, nil
}

func (o *Operator) Verify(ctx context.Context, opts Options) (Result, error) {
	if opts.Bundle == "" {
		return Result{}, errors.New("verify requires --bundle")
	}
	bundle, err := filepath.Abs(opts.Bundle)
	if err != nil {
		return Result{}, err
	}
	manifest, _, err := readBundleManifest(bundle)
	if err != nil {
		return Result{}, err
	}
	opts.Manifest = manifest
	staging, err := os.MkdirTemp("", "persona-backup-verify-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(staging)
	if err := extractBundle(bundle, staging, manifest); err != nil {
		return Result{}, err
	}
	opts.BundleHome = staging
	hub, manager, err := Discover(opts)
	if err != nil {
		return Result{}, err
	}
	if err := o.checkClients(hub, manager, "pg_restore"); err != nil {
		return Result{}, err
	}
	if err := o.verifyPayload(ctx, hub, manager, staging, manifest); err != nil {
		return Result{}, fmt.Errorf("temporary-database verification failed: %w", err)
	}
	return Result{Bundle: bundle, Verified: true}, nil
}

func (o *Operator) Restore(ctx context.Context, opts Options) (_ Result, err error) {
	if !opts.WritesQuiesced {
		return Result{}, errors.New("restore requires --writes-quiesced after stopping the hub and built-in manager")
	}
	if opts.Bundle == "" {
		return Result{}, errors.New("restore requires --bundle")
	}
	bundle, err := filepath.Abs(opts.Bundle)
	if err != nil {
		return Result{}, err
	}
	manifest, _, err := readBundleManifest(bundle)
	if err != nil {
		return Result{}, err
	}
	opts.Manifest = manifest
	staging, err := os.MkdirTemp("", "persona-backup-restore-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(staging)
	if err := extractBundle(bundle, staging, manifest); err != nil {
		return Result{}, err
	}
	opts.BundleHome = staging
	hub, manager, err := Discover(opts)
	if err != nil {
		return Result{}, err
	}
	if err := o.checkClients(hub, manager, "pg_restore"); err != nil {
		return Result{}, err
	}
	if hub.Database != manifest.HubDatabase || manager.Database != manifest.ManagerDatabase {
		return Result{}, errors.New("the target database names differ from the bundle; use the original names so the restored configuration and databases agree")
	}
	if err := verifyPayloadKeyFiles(staging, manifest); err != nil {
		return Result{}, err
	}
	// The file layout is checked before any database is staged; applyFiles
	// checks it again before it changes anything.
	if home, err := filepath.Abs(opts.Home); err != nil {
		return Result{}, err
	} else if _, err := restoreItems(home, staging, manifest, opts.AllowExternalKB); err != nil {
		return Result{}, err
	}
	managerKeys, err := bundledManagerKeys(staging)
	if err != nil {
		return Result{}, err
	}
	for _, target := range []Target{hub, manager} {
		if err := o.backend.Quiesced(ctx, target, target.Database, true); err != nil {
			return Result{}, err
		}
	}
	// Staged databases are restore's to drop until the swap takes them over.
	var staged []stagedCopy
	defer func() {
		for _, stage := range staged {
			err = errors.Join(err, o.dropStage(stage.target, stage.name))
		}
	}()
	hubStage, err := o.stageDatabase(ctx, hub, filepath.Join(staging, hubDumpPath), manifest.Hub, manifest.HubKeyFingerprint, nil, "h")
	if err != nil {
		return Result{}, err
	}
	staged = append(staged, stagedCopy{hub, hubStage})
	managerStage, err := o.stageDatabase(ctx, manager, filepath.Join(staging, managerDumpPath), manifest.Manager, manifest.ManagerKeyFingerprint, managerKeys, "m")
	if err != nil {
		return Result{}, err
	}
	staged = append(staged, stagedCopy{manager, managerStage})
	id, err := o.identifier()
	if err != nil {
		return Result{}, err
	}
	keptPassword := dockerPasswordKept(opts.Home, manifest) // before applyFiles installs the bundle's copy
	notes, err := leftInPlaceNotes(opts.Home, staging, manifest, opts.AllowExternalKB)
	if err != nil {
		return Result{}, err
	}
	previousFiles, previousKB, rollbackFiles, err := applyFiles(ctx, opts.Home, staging, manifest, opts.AllowExternalKB, id)
	if err != nil {
		return Result{}, err
	}
	for _, target := range []Target{hub, manager} {
		if err := o.backend.Quiesced(ctx, target, target.Database, true); err != nil {
			return Result{}, errors.Join(err, rollbackFiles())
		}
	}
	staged = nil // the swap installs them or disposes of them
	outcome, err := o.swapDatabases(ctx, hub, manager, hubStage, managerStage)
	if err != nil && outcome.bundleMayBeLive() {
		// A database from the bundle may be live: the rollback could not move
		// it back, or could not establish that it is not installed. Putting the
		// files back under it would make the install inconsistent, so they stay
		// as restored, and the error says where everything previous is. Nothing
		// is lost; running restore again converges.
		return Result{}, fmt.Errorf("%w; a database from the bundle may be live and could not be rolled back, so the runtime files were left as restored to match it; "+
			"previous runtime files: %s; previous external knowledge base: %s. "+
			"Check the server, then run restore again, or rename the previous databases back by hand",
			err, nameOrNone(previousFiles), nameOrNone(previousKB))
	}
	if err != nil {
		return Result{}, errors.Join(err, rollbackFiles())
	}
	previous := outcome.previous()
	return Result{Bundle: bundle, Verified: true, Restored: true, PreviousHubDB: previous[0], PreviousManagerDB: previous[1], PreviousFiles: previousFiles, KeptDockerPassword: keptPassword, PreviousExternalKB: previousKB, Notes: notes}, nil
}

func nameOrNone(name string) string {
	if name == "" {
		return "(none)"
	}
	return name
}

// refuseOutputInsideCollectedTree stops a bundle from being written inside a
// tree it collects (the knowledge base and the runtime directories): staging
// and the bundle would be copied into themselves, and every later backup would
// carry the earlier ones.
func refuseOutputInsideCollectedTree(home, output string) error {
	homeAbs, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	_, kbDir, err := knowledgeBaseLocator(homeAbs)
	if err != nil {
		return err
	}
	trees := []string{kbDir, filepath.Join(homeAbs, "managers", "assistant")}
	for _, name := range optionalRuntimeDirectories {
		trees = append(trees, filepath.Join(homeAbs, name))
	}
	resolvedOutput := resolveExisting(filepath.Dir(output))
	for _, tree := range trees {
		for _, candidate := range []string{filepath.Dir(output), resolvedOutput} {
			for _, base := range []string{tree, resolveExisting(tree)} {
				if rel, err := filepath.Rel(base, candidate); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return fmt.Errorf("--output is inside %s, which the backup collects: choose a directory outside the knowledge base and the runtime's managers/assistant, extensions, skills, hub.machines_kept and tls directories", tree)
				}
			}
		}
	}
	return nil
}

// resolveExisting resolves symlinks in the longest existing prefix of a path.
func resolveExisting(name string) string {
	rest := ""
	for current := filepath.Clean(name); ; current = filepath.Dir(current) {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, rest)
		}
		if filepath.Dir(current) == current {
			return name
		}
		rest = filepath.Join(filepath.Base(current), rest)
	}
}

type stagedCopy struct {
	target Target
	name   string
}

// dropStage drops a staged copy of a bundle database that was never
// installed; one that cannot be dropped is named, so it can be dropped by hand.
func (o *Operator) dropStage(target Target, name string) error {
	ctx, cancel := cleanupContext()
	defer cancel()
	if err := o.backend.Drop(ctx, target, name); err != nil {
		return fmt.Errorf("the staged copy %s of the bundle's %s database may remain, not in use and safe to drop: %w", name, target.Part, err)
	}
	return nil
}

func (o *Operator) stageDatabase(ctx context.Context, target Target, dump string, expected DatabaseIdentity, keyFingerprint string, keys [][]byte, role string) (_ string, err error) {
	name, err := o.uniqueName(ctx, target, "persona_staged_"+role+"_")
	if err != nil {
		return "", err
	}
	if err := o.backend.Create(ctx, target, name); err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, o.dropStage(target, name))
		}
	}()
	if err := o.backend.Restore(ctx, target, name, dump); err != nil {
		return "", err
	}
	actual, err := o.backend.Identity(ctx, target, target.Part, name)
	if err != nil {
		return "", err
	}
	if err := compareIdentity(expected, actual, target.Part, keyFingerprint, keys); err != nil {
		return "", err
	}
	return name, nil
}

func verifyPayloadKeyFiles(staging string, manifest *Manifest) error {
	hubKey, err := secretkey.ReadKey(filepath.Join(staging, hubKeyPath))
	if err != nil {
		return errors.New("the bundled hub secret.key is invalid")
	}
	managerKey, err := secretkey.ReadKey(filepath.Join(staging, managerKeyPath))
	if err != nil {
		return errors.New("the bundled built-in manager secret.key is invalid")
	}
	if secretkey.Fingerprint(hubKey) != manifest.HubKeyFingerprint || secretkey.Fingerprint(managerKey) != manifest.ManagerKeyFingerprint {
		return errors.New("the bundled encryption keys do not match their manifest fingerprints")
	}
	return nil
}

// bundledManagerKeys are the keys the built-in manager opens its secrets with:
// its secret.key and, while a rotation keeps them, the ones in secret.key.prev
// (secretkey.NewWithFallback).
func bundledManagerKeys(staging string) ([][]byte, error) {
	current, err := secretkey.ReadKey(filepath.Join(staging, managerKeyPath))
	if err != nil {
		return nil, errors.New("the bundled built-in manager secret.key is invalid")
	}
	previous, err := secretkey.ReadKeys(secretkey.PrevPath(filepath.Join(staging, managerKeyPath)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("the bundled built-in manager secret.key.prev is invalid")
	}
	return append([][]byte{current}, previous...), nil
}

func (o *Operator) verifyPayload(ctx context.Context, hub, manager Target, staging string, manifest *Manifest) error {
	if err := verifyPayloadKeyFiles(staging, manifest); err != nil {
		return err
	}
	managerKeys, err := bundledManagerKeys(staging)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		target   Target
		dump     string
		expected DatabaseIdentity
		key      string
		keys     [][]byte
		role     string
	}{
		{hub, hubDumpPath, manifest.Hub, manifest.HubKeyFingerprint, nil, "h"},
		{manager, managerDumpPath, manifest.Manager, manifest.ManagerKeyFingerprint, managerKeys, "m"},
	} {
		if err := o.verifyOne(ctx, item.target, filepath.Join(staging, item.dump), item.expected, item.key, item.keys, item.role); err != nil {
			return err
		}
	}
	return nil
}

func (o *Operator) verifyOne(ctx context.Context, target Target, dump string, expected DatabaseIdentity, key string, keys [][]byte, role string) (returnErr error) {
	name, err := o.uniqueName(ctx, target, "persona_verify_"+role+"_")
	if err != nil {
		return err
	}
	if err := o.backend.Create(ctx, target, name); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := cleanupContext()
		defer cancel()
		returnErr = errors.Join(returnErr, o.backend.Drop(cleanupCtx, target, name))
	}()
	if err := o.backend.Restore(ctx, target, name, dump); err != nil {
		return err
	}
	actual, err := o.backend.Identity(ctx, target, target.Part, name)
	if err != nil {
		return err
	}
	return compareIdentity(expected, actual, target.Part, key, keys)
}

// compareIdentity checks the restored schema, and for the built-in manager that
// the bundled key is the one its secrets are sealed with: by the sealing_key
// record when there is one, otherwise (before its first rotation) by opening the
// sealed values themselves. Values that open with no key are already unreadable
// on the source install, as a rotation leaves them, so one that opens suffices;
// none opening means the bundle carries the wrong key.
func compareIdentity(expected, actual DatabaseIdentity, part, keyFingerprint string, keys [][]byte) error {
	expected, actual = sortedIdentities(expected), sortedIdentities(actual)
	if !reflect.DeepEqual(expected.Schema, actual.Schema) {
		return fmt.Errorf("%s schema identity differs after restore", part)
	}
	if part == "manager" {
		if expected.SealingKey != actual.SealingKey {
			return errors.New("the built-in manager encryption-key identity differs after restore")
		}
		if actual.SealingKey != "" && actual.SealingKey != keyFingerprint {
			return errors.New("the restored built-in manager database does not match the bundled current secret.key")
		}
		if actual.SealingKey == "" && !anySealedValueOpens(actual.SealedValues, keys) {
			return fmt.Errorf("the built-in manager database records no encryption key, and none of its %d sealed secret(s) opens with the bundled secret.key", len(actual.SealedValues))
		}
	}
	return nil
}

func anySealedValueOpens(values []string, keys [][]byte) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		for _, key := range keys {
			if _, err := secretkey.OpenWith(key, value); err == nil {
				return true
			}
		}
	}
	return false
}

// checkClients stops early when the backend cannot run the PostgreSQL client
// programs a command needs (fakes without clients have nothing to check).
func (o *Operator) checkClients(hub, manager Target, programs ...string) error {
	checker, ok := o.backend.(interface {
		CheckClients(Target, ...string) error
	})
	if !ok {
		return nil
	}
	for _, target := range []Target{hub, manager} {
		if err := checker.CheckClients(target, programs...); err != nil {
			return err
		}
	}
	return nil
}

func (o *Operator) uniqueName(ctx context.Context, target Target, prefix string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		id, err := o.identifier()
		if err != nil {
			return "", err
		}
		name := prefix + id
		if len(name) > 63 {
			return "", errors.New("internal temporary database name exceeds PostgreSQL's identifier limit")
		}
		exists, err := o.backend.Exists(ctx, target, name)
		if err != nil {
			return "", err
		}
		if !exists {
			return name, nil
		}
	}
	return "", errors.New("could not allocate a unique temporary database name")
}

func (o *Operator) identifier() (string, error) {
	bytes := make([]byte, 8)
	if _, err := io.ReadFull(o.random, bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
