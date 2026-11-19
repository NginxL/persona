# Main verification and automatic repair

`main-verify-fix-loop` is a maintainer-side workflow for checking new `main` commits after PRs merge. It is independent
of `pr-review-merge-loop`: the PR watcher performs semantic review and exact-head merging; this watcher runs repository
build/vet/rule checks and opens a normal repair PR when a check fails. Neither service changes GitHub rulesets, pushes
directly to `main` or substitutes for hosted post-merge CI.

The project skill is `.claude/skills/main-verify-fix-loop/SKILL.md`. Its implementation is
`scripts/operations/main_watch.py`, with explicit lanes in `main_watch_verify.py`. It reuses the PR watcher's bounded
process helpers, credential-free verification environment, disposable repository clones and `bwrap` repair sandbox.

## Contract

1. Resolve the configured remote base with `git ls-remote`, fetch the exact SHA and verify its commit object. Do not
   rely on a shared checkout's HEAD or an asynchronously changing remote-tracking ref.
2. Run each configured lane on that commit in a verifier-owned worktree. A passed SHA is recorded with its actual
   commands and results; it is never evidence for another SHA. A missing tool, failed prerequisite or timeout is not a
   pass. Reused worktrees must be owned by this state, clean and at the exact commit; unexpected paths are left alone.
3. The `persona` profile runs `go -C server build ./...`, `go -C server vet ./...`, `scripts/ci/guards.sh` and
   `scripts/ci/scripts.sh`. It does not claim a full Go test suite, web, Android, Apple, Rust or runtime verification.
   Those are explicit `extra_lanes` (`server`, `web`, `android`, `apple`, `macos`, `core`, `runtimes`) and run in full
   when selected. An unavailable platform must be configured on a suitable host or reported as unverified. An ordinary
   verification resolves its checker scripts from the exact-main worktree, not from the resident watcher's older
   implementation checkout. An explicit `trusted_checkout` is reserved for repair verification below.
4. The `commands` profile accepts only trusted local `{name, argv}` objects with per-command timeouts. No shell snippets
   or repository-supplied commands are allowed. `{worktree}` is replaced with the exact verification path, including
   when the repair is verified in its separate clone.
5. On failure, retain full output in private `0600` state. Public issue text contains only lane names, the exact SHA
   and strictly filtered source diagnostics. Raw logs, raw argv, environment values, URLs, possible secrets and host
   paths are not published. Normalize those diagnostics into a stable failure signature. Search existing issues by that
   signature and by title; respect another worker's current claim or an already open repair PR.
   Do not create duplicate issues for the same failure on later main commits.
6. With explicit `publish: true`, create or claim a bug issue, run at most three repair attempts per signature with
   30-minute backoff, and ask the configured repair CLI for the smallest correctness fix. The first valid claim wins;
   a worker that loses a simultaneous claim withdraws its own comment instead of overriding the earlier worker.
   `Progress:` from the claim's GitHub author renews ownership and that author's `Abandon:` releases it.
   Exhaustion publishes one `Abandon:` note.
7. The worker receives no maintainer HOME and cannot commit, push, comment or merge. The outer process accepts only
   production changes and newly added regression tests. It rejects edits to CI/rule scripts, protected files,
   baselines and existing tests. Only explicitly accepted new files enter the patch; unrelated untracked build output
   is never staged. `scripts/ci/common.sh` separates `CI_IMPLEMENTATION_ROOT` from the tested checkout. Its
   `ci_python` / `ci_node` commands resolve checker files only from the exact-main snapshot; the working directory and
   source inputs remain the repaired clone. Compilation and tests therefore target the repair, not old `main` and not
   worker-modified checkers. Require the verified tree to match the proposed branch.
8. Push a normal `fix/<issue>-main-...` branch and create a PR with `Fixes #N`. Persist the verified head and checks
   before the first push, together with the original base and branch. If PR creation has an unknown result, recover by
   that recorded base, branch and exact verified commit even after `main` moves, instead of running the model again or force-pushing. If `main` moved during repair, do not publish the old-base repair as
   the current fix. Hand the PR to the ordinary semantic-review/merge flow. Never merge from this watcher.
   `--dry-run` runs checks without creating issues, comments, commits, branches or PRs; `publish: false` likewise
   does not attempt a repair or publication.

A later `main` commit can recover while an old repair is still pending. Verify the new commit independently; do not
close someone else's issue or PR, cancel their work or report the old commit as repaired without evidence.

## Configure and run

Copy `scripts/operations/main-watch.example.json` to a private location such as
`~/.config/persona/main-watch.json` and restrict it with `chmod 600`. Set the repository, a trusted absolute checkout,
the machine ID from private config and a private absolute state path. The default is `publish: false`. Automatic issue,
branch and PR writes require the maintainer to opt in explicitly; starting a resident service is a separate operational
decision, not a side effect of installing the skill.

```sh
python3 -I <trusted-checkout>/scripts/operations/main_watch.py --config <private-config> --once --dry-run
python3 -I <trusted-checkout>/scripts/operations/main_watch.py --config <private-config>
python3 -B -m unittest -v scripts.operations.test_main_watch
```

The continuous command holds a per-state instance lock and polls every 60 seconds by default. Keep it running through
idle periods rather than repeatedly invoking `--once`. State is below the configured private directory; its `worktrees/`
belong only to this watcher. Do not remove another worktree or run a second process against the same state.

For unattended use, adapt `scripts/operations/persona-main-watch.example.service`, including the trusted checkout and
private environment file. Keep `UMask=0077`, `NoNewPrivileges=true`, bounded stop behavior and the existing PR watcher
independent. Check `systemctl --user status persona-main-watch.service` before starting or changing it. Inspect its logs
with `journalctl --user-unit persona-main-watch.service`. An installed unit is not automatically enabled by a repository
change.

## Verification and limitations

The tests use temporary repositories and fake GitHub/model boundaries. They cover configuration safety, exact-SHA
results, stable failure signatures, duplicate and racing claims, claim abandonment, single-instance locking, real repair
commits, a moved-main publication refusal, protected-check rejection, secret filtering, new-file artifact exclusion,
recovery after an unknown PR-create result, and exhaustion notification. Run the focused test and `scripts/ci/scripts.sh`
before merging changes to the watcher. The first live rollout should use `--once --dry-run` and record the exact SHA and
actual commands. A green diagnostic does not prove automatic repair publication, platform availability or a merged fix.
