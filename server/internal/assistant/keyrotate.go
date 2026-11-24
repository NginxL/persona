package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/secretkey"
)

// The built-in manager's secret.key started as a copy of the hub's (managerconf.MoveFiles), so each part could open
// the other's secrets. Once its data is in its own database (owndb.go), it rotates to a key of its own, once, at its
// start, before anything of it reads a secret (docs/assistant/managers.md "Its own secret.key"):
//
//  1. Under an advisory lock in its database (two starts at once: one rotates, the other finds it done), it keeps the
//     key it has in secret.key.prev and writes a fresh one to secret.key.next-<its fingerprint>.
//  2. One transaction opens every sealed value of its database (db.ResealSecrets) with the old key, seals it with the
//     new one, and records the new key's fingerprint (sealing_key): the values and the record go together. The
//     transaction checks the record is still none, so a process whose lock was lost cannot seal over another's.
//  3. The new key's file is renamed over secret.key.
//  4. The old keys in secret.key.prev stay readable (secretkey.NewWithFallback) until nothing can still be sealed with
//     them: at least oldKeyGrace after the rotation, while no other manager process uses the database (one started
//     before may still seal with the old key), and no legacy mail file waits to be imported. That is checked every
//     minute while it runs (keepDroppingOldKey) and at each start; before the old keys go, what is still sealed with
//     them is sealed again.
//
// Every step can stop (a crash, a full disk, a lost connection) and the next start goes on from there: before the
// commit nothing is sealed with the new key; after it, the record names the new key, and the next start finishes the
// rename. A commit whose answer was lost is told by reading the record again; when that cannot be read either, the
// start stops with the new key's file kept. It touches only the manager's own directory and its own database: never
// the hub's key, nor a database that has the hub's tables.

// keyRotationWait is how long a start waits for another one's rotation to let its lock go.
var keyRotationWait = time.Minute

// oldKeyCheckEvery is how often a running manager that kept the old key looks whether it can go.
var oldKeyCheckEvery = time.Minute

// oldKeyGrace is how long the old key is kept at least after the rotation: an older manager process still running
// (a hub switching versions) may have no connection open when it is counted, and stops within this.
var oldKeyGrace = 10 * time.Minute

// resealTimeout bounds a re-seal, which runs on even when the start is stopped meanwhile: a commit is never left half
// way for want of patience.
var resealTimeout = 10 * time.Minute

// keyRotationStep lets a test stop the rotation after a step, as a crash there would.
var keyRotationStep = func(step string) error { return nil }

// keyRotationReseal is the rotation's re-seal: tests lose its answer, or fail it, there.
var keyRotationReseal = func(ctx context.Context, d *db.DB, fn func(string) (string, db.ResealOutcome, error), expect string, rec *db.SealingKey) (db.ResealReport, error) {
	return d.ResealSecrets(ctx, fn, expect, rec)
}

// commitSettleWait is how long a start waits, after a re-seal's commit did not answer, for that re-seal to commit or
// end on the server (it is ended within db.ResealSecrets' idle timeout).
var commitSettleWait = 3 * time.Minute

// errStopped is what keyRotationStep returns in tests that stop the rotation (a crash there).
var errStopped = errors.New("stopped")

// errRotatedElsewhere: another process rotated while this one staged (db.ErrSealingKeyChanged); run goes on from that.
var errRotatedElsewhere = errors.New("another process rotated the key meanwhile")

// legacyMailFile is the mail accounts file of old builds, imported once when the mail service starts (internal/modules/mail):
// its passwords are sealed with the old key.
const legacyMailFile = "mail_accounts.json"

// rotateSecretKey rotates the built-in manager's secret.key, or goes on with a rotation a start before stopped, at
// its start. home is the data directory, keyPath the key it runs with. pending: the old key is kept for now;
// keepDroppingOldKey goes on while it runs. An error stops the start: another process holds the rotation longer than
// keyRotationWait, or the secrets may be sealed with a key that could not be made secret.key. Anything else that fails
// is said and leaves the key as it is, for the next start to try again.
func rotateSecretKey(ctx context.Context, database *db.DB, home, keyPath string, logf func(string, ...any)) (pending bool, err error) {
	r, ok := newKeyRotation(ctx, database, home, keyPath, logf)
	if !ok {
		return false, nil
	}
	lock, err := database.LockKeyRotation(ctx, keyRotationWait)
	if err != nil {
		return false, err
	}
	defer lock.Release()
	return r.run(ctx)
}

// keepDroppingOldKey looks every oldKeyCheckEvery, while ctx lasts, whether the old key a start kept can go now, and
// deletes it then (after sealing again what is still sealed with it). It never rotates: the services read the key the
// start left. A check that fails is tried again the next time. Once the old key is deleted it goes on, with the old
// keys it read kept in memory (as the services' boxes keep them, secretkey.NewWithFallback): a value a service read
// before and writes back afterwards is sealed again within a minute, and once more when the manager stops.
func keepDroppingOldKey(ctx context.Context, database *db.DB, home, keyPath string, logf func(string, ...any)) {
	r, ok := newKeyRotation(ctx, database, home, keyPath, logf)
	if !ok {
		return
	}
	r.running = true
	t := time.NewTicker(oldKeyCheckEvery)
	defer t.Stop()
	dropped := false
	defer func() {
		if dropped { // the last chance: the next process does not have the old keys
			fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			r.resealRemembered(fctx)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if dropped {
			r.resealRemembered(ctx)
			continue
		}
		lock, err := database.LockKeyRotation(ctx, 5*time.Second)
		if err != nil {
			continue // another process's rotation, or the database away: next time
		}
		pending, _ := r.run(ctx)
		lock.Release()
		if !pending {
			if len(r.oldKeys) == 0 {
				return // nothing was kept: nothing to watch
			}
			dropped = true
		}
	}
}

// resealRemembered seals again, with secret.key, what still opens only with an old key it read before that key was
// deleted. It reads without locking first, and takes the rows' locks only when there is something to do.
func (r *keyRotation) resealRemembered(ctx context.Context) {
	cur, err := secretkey.ReadKey(r.path)
	if err != nil || len(r.oldKeys) == 0 {
		return
	}
	vals, err := r.db.SealedValues(ctx)
	if err != nil {
		r.once("secret.key: looking for values written back with the old key: %v", err)
		return
	}
	need := false
	for _, v := range vals {
		if _, err := secretkey.OpenWith(cur, v); err != nil {
			for _, k := range r.oldKeys {
				if _, err := secretkey.OpenWith(k, v); err == nil {
					need = true
				}
			}
		}
	}
	if !need {
		return
	}
	lock, err := r.db.LockKeyRotation(ctx, 5*time.Second)
	if err != nil {
		return
	}
	defer lock.Release()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resealTimeout)
	defer cancel()
	rep, err := r.db.ResealSecrets(rctx, resealWith(cur, r.oldKeys), secretkey.Fingerprint(cur), nil)
	switch {
	case err != nil:
		r.once("secret.key: sealing again values written back with the old key failed: %v", err)
	case rep.Resealed > 0:
		r.logf("secret.key: sealed %d secrets again that were written back sealed with the old key after it was deleted", rep.Resealed)
	}
}

// newKeyRotation checks it is the manager's own key in a database of its own; ok false (said) when not.
func newKeyRotation(ctx context.Context, database *db.DB, home, keyPath string, logf func(string, ...any)) (*keyRotation, bool) {
	own := filepath.Join(managerconf.DefaultDir(home), managerconf.SecretKey)
	if filepath.Clean(keyPath) != own || sameFile(keyPath, filepath.Join(home, managerconf.SecretKey)) {
		logf("secret.key: %s is not the manager's own (%s): not rotating it", keyPath, own)
		return nil, false
	}
	if hub, err := database.HasHubTables(ctx); err != nil || hub {
		if err == nil {
			err = db.ErrHubTables
		}
		logf("secret.key: not rotating it: %v", err)
		return nil, false
	}
	grace := oldKeyGrace
	if v := identity.Getenv("_PERSONA_OLD_KEY_GRACE"); v != "" { // e2e tests
		if d, err := time.ParseDuration(v); err == nil {
			grace = d
		}
	}
	return &keyRotation{db: database, path: keyPath, home: home, logf: logf, grace: grace}, true
}

// sameFile: a and b are one file (a link of the hub's key into the manager's directory is never rotated).
func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

type keyRotation struct {
	db         *db.DB
	path, home string
	logf       func(string, ...any)
	grace      time.Duration
	// running: the services run (keepDroppingOldKey): it only seals again and deletes the old key, never rotates,
	// tries again after what fails, and says only what changes.
	running bool
	// again: run is running a second time, after another process rotated during its first.
	again bool
	// oldKeys are the old keys it read from secret.key.prev, kept after the file goes (keepDroppingOldKey).
	oldKeys [][]byte
	last    string // the watcher's last line (once)
}

func (r *keyRotation) run(ctx context.Context) (pending bool, err error) {
	prevPath := secretkey.PrevPath(r.path)
	r.removeStaleTemps()
	cur, err := secretkey.ReadKey(r.path)
	if errors.Is(err, os.ErrNotExist) && !r.running {
		return false, nil // not made yet: it is made in its own directory at first use, its own from the start
	}
	if err != nil {
		r.say("secret.key: not rotating it: %v", err)
		return r.running, nil
	}
	prev, err := secretkey.ReadKeys(prevPath)
	prevOK := err == nil || errors.Is(err, os.ErrNotExist)
	if !prevOK {
		r.say("secret.key: %v: kept as it is", err)
	}
	for _, k := range prev {
		if !secretkey.Has(r.oldKeys, k) {
			r.oldKeys = append(r.oldKeys, k)
		}
	}
	rec, err := r.db.SealingKey(ctx)
	if err != nil {
		if r.running {
			return true, nil
		}
		// Which key the secrets are sealed with cannot be read: a rotation stopped before its rename would go
		// unfinished, and the services would seal with a key the database does not name.
		return false, fmt.Errorf("secret.key: which key its secrets are sealed with cannot be read: %w", err)
	}
	if r.running && (rec == nil || secretkey.Fingerprint(cur) != rec.Fingerprint) {
		return false, nil // not the state the start left: the next start sees to it
	}
	rotated := false
	switch {
	case rec == nil:
		if !prevOK {
			return false, nil // the old keys could not be kept beside it
		}
		next, done, err := r.rotate(ctx, cur, prev)
		if errors.Is(err, errRotatedElsewhere) && !r.again {
			r.again = true // another process rotated meanwhile (its lock lost): go on from what it did
			return r.run(ctx)
		}
		if err != nil || !done {
			return false, err
		}
		if !secretkey.Has(prev, cur) {
			prev = append(prev, cur) // kept in secret.key.prev (stage)
		}
		if !secretkey.Has(r.oldKeys, cur) {
			r.oldKeys = append(r.oldKeys, cur)
		}
		cur, rotated = next, true
		if rec, err = r.db.SealingKey(ctx); err != nil || rec == nil {
			return true, nil // the old key stays: the watcher or the next start goes on
		}
	case secretkey.Fingerprint(cur) != rec.Fingerprint:
		nextPath := secretkey.NextPath(r.path, rec.Fingerprint)
		next, err := secretkey.ReadKey(nextPath)
		if err != nil || secretkey.Fingerprint(next) != rec.Fingerprint {
			r.say("secret.key: the secrets in its database are sealed with another key (%s; this one is %s): not rotating; "+
				"they do not open until that key is secret.key again, or is added to %s", shortFP(rec.Fingerprint),
				shortFP(secretkey.Fingerprint(cur)), prevPath)
			return false, nil
		}
		// A rotation stopped between its commit and the rename: finish it, the old key kept beside it first.
		if !prevOK {
			return false, fmt.Errorf("secret.key: the secrets are sealed with %s, and the key it replaces cannot be kept in %s", nextPath, prevPath)
		}
		if !secretkey.Has(prev, cur) {
			if err := secretkey.WriteKeys(prevPath, append(prev, cur)...); err != nil {
				return false, fmt.Errorf("secret.key: the secrets are sealed with %s, and the key it replaces cannot be kept: %w", nextPath, err)
			}
			prev = append(prev, cur)
		}
		if err := r.swap(nextPath, rec.Fingerprint); err != nil {
			return false, err
		}
		r.say("secret.key: finished the rotation a start before stopped (%s)", shortFP(rec.Fingerprint))
		cur = next
	}
	r.removeOtherNexts(rec.Fingerprint)
	if prev == nil {
		return false, nil // nothing kept (or the file of kept keys does not read: left as it is)
	}
	why := r.keepWhy(ctx, rec)
	if !rotated {
		if why != "" && r.running {
			return true, nil // not yet: no re-seal (and no row locks) until it can go
		}
		// What an older process sealed with an old key meanwhile.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resealTimeout)
		rep, err := r.db.ResealSecrets(rctx, resealWith(cur, prev), rec.Fingerprint, nil)
		cancel()
		if err != nil {
			r.say("secret.key: the old key stays in %s: sealing again what it sealed failed: %v", prevPath, err)
			return true, nil
		}
		if rep.Resealed > 0 || len(rep.Unreadable) > 0 {
			r.logf("secret.key: sealed %d secrets again that were still sealed with the old key%s", rep.Resealed, unreadable(rep))
		}
	}
	if why != "" {
		r.say("secret.key: the old key stays in %s, only for reading, %s", prevPath, why)
		return true, nil
	}
	if n, err := r.db.OtherManagers(ctx); err != nil || n > 0 { // and still none after the re-seal
		return true, nil
	}
	return r.dropPrevious(ctx)
}

// rotate stages a fresh key, re-seals every secret with it and makes it secret.key. done false: not rotated (said),
// it runs with the key it has. An error stops the start.
func (r *keyRotation) rotate(ctx context.Context, cur []byte, prev [][]byte) (next []byte, done bool, err error) {
	next, nextPath, err := r.stage(cur, prev)
	if errors.Is(err, errStopped) {
		return nil, false, nil
	}
	if err != nil {
		r.say("secret.key: not rotated (%v); it runs with the key it has and tries again at the next start", err)
		if nextPath != "" {
			_ = os.Remove(nextPath)
		}
		return nil, false, nil
	}
	fp := secretkey.Fingerprint(next)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resealTimeout)
	rep, err := keyRotationReseal(rctx, r.db, resealWith(next, append([][]byte{cur}, prev...)), "",
		&db.SealingKey{Fingerprint: fp, Previous: secretkey.Fingerprint(cur)})
	cancel()
	if err != nil {
		if !errors.Is(err, db.ErrCommitUnknown) {
			// Ended before its COMMIT. The new key's file goes only when that is certain (Postgres said why, or the
			// re-seal refused itself); after a connection that broke it stays, and goes once the record names another
			// key (removeOtherNexts).
			if notCommitted(err) {
				_ = os.Remove(nextPath)
			}
			if errors.Is(err, db.ErrSealingKeyChanged) {
				return nil, false, errRotatedElsewhere
			}
			r.say("secret.key: not rotated (%v); it runs with the key it has and tries again at the next start", err)
			return nil, false, nil
		}
		committed, cerr := r.committed(fp)
		if cerr != nil {
			// Whether the commit went through cannot be told: the new key's file stays, and the next start finishes
			// the rename (the record names it) or starts over (it does not).
			return nil, false, fmt.Errorf("secret.key: the re-seal's commit did not answer (%v) and whether it went through cannot be read (%v): "+
				"not starting; the next start goes on", err, cerr)
		}
		if !committed { // its file stays all the same (removeOtherNexts)
			r.say("secret.key: not rotated (%v); it runs with the key it has and tries again at the next start", err)
			return nil, false, nil
		}
		r.say("secret.key: the re-seal's answer was lost (%v), but it was committed: going on", err)
	}
	if keyRotationStep("resealed") != nil {
		return nil, false, nil
	}
	if err := r.swap(nextPath, fp); err != nil {
		return nil, false, err
	}
	if keyRotationStep("swapped") != nil {
		return nil, false, nil
	}
	r.logf("secret.key: rotated to a key of its own (%s); %d secrets sealed again%s", shortFP(fp), rep.Resealed, unreadable(rep))
	return next, true, nil
}

// notCommitted: err certainly left nothing of the re-seal committed: Postgres answered with an error, or the re-seal
// refused itself before its COMMIT. Not so for a connection that broke.
func notCommitted(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) || errors.Is(err, db.ErrSealingKeyChanged) || errors.Is(err, db.ErrHubTables) ||
		errors.Is(err, db.ErrResealRefused)
}

// committed reads the record again, on a fresh connection and context, once the re-seal is no longer open on the server
// (its COMMIT may still be on the way, or running): whether the re-seal for fp went through.
func (r *keyRotation) committed(fp string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commitSettleWait+30*time.Second)
	defer cancel()
	rec, err := r.db.SealingKeySettled(ctx, commitSettleWait)
	if err != nil {
		return false, err
	}
	return rec != nil && rec.Fingerprint == fp, nil
}

// stage keeps the key it has with the old ones in secret.key.prev and writes a fresh key to its own
// secret.key.next-<fingerprint>.
func (r *keyRotation) stage(cur []byte, prev [][]byte) (next []byte, nextPath string, err error) {
	if !secretkey.Has(prev, cur) {
		if err := secretkey.WriteKeys(secretkey.PrevPath(r.path), append(prev, cur)...); err != nil {
			return nil, "", err
		}
	}
	if err := keyRotationStep("kept"); err != nil {
		return nil, "", err
	}
	if next, err = secretkey.Fresh(); err != nil {
		return nil, "", err
	}
	nextPath = secretkey.NextPath(r.path, secretkey.Fingerprint(next))
	if err := secretkey.WriteKeys(nextPath, next); err != nil {
		return nil, nextPath, err
	}
	if err := keyRotationStep("staged"); err != nil {
		return nil, nextPath, err
	}
	return next, nextPath, nil
}

// swap renames the new key's file over secret.key: the record already names it, so a failure stops the start (sealing
// with the old key now would make secrets the record does not describe). Another process that renamed it first
// (the same rotation, finished by it) is no failure.
func (r *keyRotation) swap(nextPath, fp string) error {
	if err := os.Rename(nextPath, r.path); err != nil {
		if k, rerr := secretkey.ReadKey(r.path); rerr == nil && secretkey.Fingerprint(k) == fp {
			return nil
		}
		return fmt.Errorf("secret.key: the secrets are sealed with %s now, and it could not be made secret.key: %w", nextPath, err)
	}
	if err := secretkey.SyncDir(filepath.Dir(r.path)); err != nil {
		return fmt.Errorf("secret.key: %w", err)
	}
	return nil
}

// keepWhy says why the old key must stay for now ("" when it can go): too soon after the rotation, another manager
// process on the database (or one that cannot be counted), a legacy mail file not imported yet.
func (r *keyRotation) keepWhy(ctx context.Context, rec *db.SealingKey) string {
	if rec.Age < r.grace {
		return fmt.Sprintf("for %s after the rotation (an older manager process may still be stopping)", r.grace)
	}
	others, err := r.db.OtherManagers(ctx)
	switch {
	case err != nil:
		return fmt.Sprintf("while the other manager processes cannot be counted (%v)", err)
	case others > 0:
		return fmt.Sprintf("while another manager process uses its database (%d connections; one started before may still seal with it)", others)
	}
	if _, err := os.Lstat(filepath.Join(r.home, legacyMailFile)); err == nil {
		return fmt.Sprintf("until %s is imported (its passwords are sealed with it)", legacyMailFile)
	}
	return ""
}

// dropPrevious deletes secret.key.prev and records it. pending: it stays (said).
func (r *keyRotation) dropPrevious(ctx context.Context) (pending bool, err error) {
	prevPath := secretkey.PrevPath(r.path)
	if err := keyRotationStep("drop"); err != nil {
		return true, nil
	}
	if err := os.Remove(prevPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.say("secret.key: the old key stays in %s: %v", prevPath, err)
		return true, nil
	}
	_ = secretkey.SyncDir(filepath.Dir(prevPath))
	if err := r.db.MarkPreviousDropped(ctx); err != nil {
		r.logf("secret.key: deleted the old key; recording it failed: %v", err)
		return false, nil
	}
	r.logf("secret.key: deleted the old key: the manager's secrets open only with its own now")
	return false, nil
}

// removeOtherNexts deletes the new keys' files of attempts that did not become secret.key: the record names
// secret.key's (fp), so none of them sealed anything that stays. Their temporary files go with removeStaleTemps.
func (r *keyRotation) removeOtherNexts(fp string) {
	keep := secretkey.NextPath(r.path, fp)
	left, _ := filepath.Glob(secretkey.NextGlob(r.path))
	removed := false
	for _, p := range left {
		if p != keep && !strings.Contains(filepath.Base(p), ".tmp-") && os.Remove(p) == nil {
			removed = true
		}
	}
	if removed {
		_ = secretkey.SyncDir(filepath.Dir(r.path))
	}
}

// removeStaleTemps deletes the temporary files of secretkey.WriteKeys a crash left beside the key (each holds whole
// keys): only ones older than a minute, younger ones may be another process's on their way.
func (r *keyRotation) removeStaleTemps() {
	for _, pat := range []string{secretkey.PrevPath(r.path) + ".tmp-*", secretkey.NextGlob(r.path) + ".tmp-*"} {
		left, _ := filepath.Glob(pat)
		for _, p := range left {
			if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() && time.Since(st.ModTime()) > time.Minute {
				_ = os.Remove(p)
			}
		}
	}
}

// once logs what the watcher hits, but not the same line again and again every minute.
func (r *keyRotation) once(f string, a ...any) {
	if line := fmt.Sprintf(f, a...); line != r.last {
		r.last = line
		r.logf("%s", line)
	}
}

// say logs at the start; the watcher says only what changes.
func (r *keyRotation) say(f string, a ...any) {
	if !r.running {
		r.logf(f, a...)
	}
}

// resealWith is a re-seal's step for one value (db.ResealSecrets): kept when key opens it, else sealed again with key
// when one of olds opens it, else left as it is.
func resealWith(key []byte, olds [][]byte) func(string) (string, db.ResealOutcome, error) {
	return func(v string) (string, db.ResealOutcome, error) {
		if _, err := secretkey.OpenWith(key, v); err == nil {
			return v, db.ResealKept, nil
		}
		for _, k := range olds {
			if plain, err := secretkey.OpenWith(k, v); err == nil {
				s, err := secretkey.SealWith(key, plain)
				if err != nil {
					return v, db.ResealUnreadable, err
				}
				return s, db.ResealDone, nil
			}
		}
		return v, db.ResealUnreadable, nil
	}
}

func shortFP(fp string) string {
	if len(fp) > 8 {
		return fp[:8]
	}
	return fp
}

// unreadable says how many values opened with no key, and where (each place once).
func unreadable(rep db.ResealReport) string {
	if len(rep.Unreadable) == 0 {
		return ""
	}
	var where []string
	for _, w := range rep.Unreadable {
		if !slices.Contains(where, w) {
			where = append(where, w)
		}
	}
	return fmt.Sprintf("; %d did not open with any key it has and were left as they were (%s)", len(rep.Unreadable),
		strings.Join(where, ", "))
}
