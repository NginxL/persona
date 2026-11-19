---
name: main-verify-fix-loop
description: Continuously verify new main commits with compile, vet and repository-rule checks, then automatically repair failures through a separate issue/PR workflow. Use for "watch main and fix build failures", "verify main after merges", "run main compile/vet/guards and repair failures", or /main-verify-fix-loop. This does not perform PR semantic review or merge PRs.
---

# Main verification and repair loop

This is maintainer infrastructure, not a Persona end-user feature. It watches the configured default branch, runs real
checks on each exact commit and repairs reproduced failures in isolation. The existing `pr-review-merge-loop` owns PR
semantic review and merging; this skill never duplicates that responsibility.

## Read first

Read `docs/operations/main-verification.md`, `docs/operations/collaboration.md`, `docs/SUPER_RULES.md` and the relevant
verification instructions in `CLAUDE.md`. Do not weaken checks, raise baselines, delete failing tests or edit protected
rules to make a run green.

## Default workflow

1. Resolve the private configuration and its trusted checkout. Do not use or change HEAD in the shared main checkout.
2. Run `scripts/operations/main_watch.py` continuously, not repeated `--once` commands. Its state lock permits one
   instance; `publish: false` is the default and performs checks only.
3. On each new remote main SHA, fetch the exact commit and run every configured check in an isolated worktree. A passed
   old SHA proves nothing about a newer one. Missing tools, timeouts and unavailable services are not passes.
4. The Persona profile runs Go build, Go vet, `scripts/ci/guards.sh` and `scripts/ci/scripts.sh`. Extra platform lanes
   are explicit configuration, never silently claimed to have run.
5. On failure, retain exact private evidence, normalize its public diagnostic and search for the same signature before
   creating or claiming a bug issue. Respect an existing issue claim or open repair PR.
6. Invoke the configured repair worker through the existing sandbox. It edits only an isolated copy; it cannot publish
   or access the maintainer HOME. Accept only production changes and new regression tests; never accept modified CI/rule
   scripts, protected files, baselines or existing tests. Filter new-file patches instead of staging build output.
7. Commit with the configured maintainer identity, rerun all configured checks on a separate clone using a clean
   exact-main snapshot of the check scripts, and require the same verified tree. Refuse publication if main moved
   during repair. Push a normal issue-numbered branch and open a PR linked to the issue. Recover an unknown PR-create
   result by branch and verified commit, never by repeating the model or force-pushing. Never push directly to main or
   merge the repair from this watcher.
8. Allow at most three bounded repair attempts for a failure signature, with backoff and persistent evidence. Continue
   serving later main commits. Report an exhausted or unverified repair honestly.

## Commands

Read the configuration path from `MAIN_WATCH_CONFIG` or use `~/.config/persona/main-watch.json`. Never print private
configuration, state, credentials or unfiltered host logs.

```bash
# A single local, non-publishing diagnostic.
python3 -I <trusted-checkout>/scripts/operations/main_watch.py \
  --config <private-config> --once --dry-run

# The continuous loop, with publication only after explicit private opt-in.
python3 -I <trusted-checkout>/scripts/operations/main_watch.py --config <private-config>

# Focused implementation tests.
python3 -B -m unittest -v scripts.operations.test_main_watch
```

For unattended operation, adapt `scripts/operations/persona-main-watch.example.service`, retain its private state and
check `systemctl --user status persona-main-watch.service` before starting it. Do not stop or modify the existing PR
watcher. Starting or changing a live service requires confirming its repository, checks, `publish` setting and current
status; the skill file itself does not install or enable a service.

## Reporting

Report the exact main SHA, checks that actually passed or failed, issue/PR URLs after reading them back, and any repair
attempt or publication blocker. Distinguish a code failure from missing prerequisites or a timeout. Never describe a
local dry run, a repaired old commit or a PR awaiting semantic review as main being fixed.
