#!/usr/bin/env python3
"""Fail when a database migration already on the base branch is edited, deleted or renamed, or a new one is misnamed.

Migrations that have been applied are recorded by file name and are not run again, so a later
edit does not repair existing databases. Comment-only and whitespace-only edits pass (whitespace
outside quoted strings is ignored; text inside quotes is not). A new .sql file may be added,
named by the UTC second it was written: YYYYMMDDHHMMSS_<name>.sql in lower case
(date -u +%Y%m%d%H%M%S), a real time from 2026-11-18 00:00:00 UTC to a day after now. Numbers
picked by hand (NNNN_<name>.sql) collided when two lines of work took the same one, so they
stopped at 0561: a numbered file outside the frozen set (NUMBERED) fails, whatever the base, and
one in it passes even against a base from before it merged.

The frozen files are the .sql files directly under server/internal/db/migrations/hub and
server/internal/db/migrations/manager at the merge base of HEAD and the base ref. The base ref
is --base, else CI_BASE_SHA, else the clone's default branch: origin/HEAD if it points at a
commit, else origin/main, else origin/master. Those files are compared with the index and the
working tree.

One exception: when two frozen files of one set have the same version, either may be renamed or
deleted. The runner refuses such a set before it applies anything, so the one that merged later
ran on no database, and renaming it to a second of its own is the fix (the other keeps its name:
a database may have recorded it before the second one merged).

Duplicate versions across the two sets are not checked here. TestMigrationSets rejects a number
both sets have, with the frozen bothSets and sharedNumbers exceptions kept in that test, and a
timestamped file in both sets; two files of one second may be one in each set, since each set
records its own part.
"""
import argparse
from datetime import datetime, timedelta, timezone
import os
from pathlib import Path
import re
import subprocess
import sys

MIGRATION_DIRS = ("server/internal/db/migrations/hub", "server/internal/db/migrations/manager")
MIGRATIONS = "server/internal/db/migrations/"
MIGRATION_PATH = re.compile(r"^server/internal/db/migrations/(hub|manager)/[^/]+\.sql$")
NUMBERED_NAME = re.compile(r"^([0-9]{4})_")
VERSIONED_NAME = re.compile(r"^([0-9]{4}|[0-9]{14})_")
TIMESTAMPED = re.compile(r"^([0-9]{4})([0-9]{2})([0-9]{2})([0-9]{2})([0-9]{2})([0-9]{2})_[a-z0-9_]+\.sql$")
# When migrations began to be timestamped, and how far after now a timestamp may be: one further ahead (a typo in the
# year, a clock set wrong) would sort after every migration written until then, so new and existing databases would
# run those in different orders. server/internal/db/migrations_test.go has the same bounds (firstStamp, stampAhead).
FIRST_STAMP = datetime(2026, 11, 18, tzinfo=timezone.utc)
STAMP_AHEAD = timedelta(days=1)
STAMP_FORMAT = "%Y%m%d%H%M%S"
HOW_TO_NAME = "name it YYYYMMDDHHMMSS_<name>.sql in lower case, the UTC second you write it: $(date -u +%Y%m%d%H%M%S)_<name>.sql"


def _frozen(part, stems):
    return {"{0}/{1}.sql".format(part, stem) for stem in stems}


# The migrations numbered by hand, by set, frozen when new ones began to be timestamped (#10618). TestMigrationSets
# keeps their count and the last number (numberedFiles, lastNumbered).
NUMBERED = frozenset(_frozen("hub", (
    "0001_init", "0021_device_prefs", "0022_live_activities", "0031_device_tokens", "0071_sent_messages",
    "0081_device_sessions", "0091_phone_tools_device", "0101_client_ops", "0111_pair_tokens",
    "0121_lock_tests", "0131_link_tokens", "0141_device_push_relay", "0151_peers", "0161_admin",
    "0181_device_credential", "0191_hub_center", "0241_sent_echo", "0311_bus_identities",
    "0389_device_locale", "0413_machine_trust", "0417_client_ops_pending", "0419_managers",
    "0421_hub_settings", "0424_device_relay_error", "0425_device_features", "0426_handed_over",
    "0434_live_activity_start_budget", "0435_push_device_lifecycle", "0462_phone_write_queue",
    "0490_owner_subjects", "0491_share_routing", "0505_client_password", "0522_hub_push_sent",
    "0530_device_voip", "0532_device_token_scopes", "0541_push_registration_transfer"
)) | _frozen("manager", (
    "0001_init", "0002_settings", "0003_session_views", "0004_inbox", "0041_kb_entities",
    "0051_machine_metrics", "0061_node_session_tasks", "0071_sent_messages", "0171_todos",
    "0211_connector_secrets", "0221_todo_spaces", "0231_assistant_tasks", "0241_sent_echo",
    "0251_todo_start_error", "0261_todo_done_by", "0291_plugins", "0301_plugin_markets",
    "0302_workspace_board", "0321_todo_decisions", "0331_session_work", "0341_todo_quiet",
    "0353_market_signature", "0358_extension_metrics", "0361_assistant_views",
    "0363_extension_contributions", "0368_assistant_always", "0371_assistant_view_actions",
    "0381_assistant_view_blocks", "0390_assistant_feed_sent_at", "0392_assistant_feed_unsent",
    "0393_interactions_pending", "0397_assistant_view_action_version", "0399_assistant_view_error_marks",
    "0405_todo_supervision", "0415_machine_metrics_raw", "0416_session_views_seen",
    "0418_node_sessions_started", "0420_sent_as_user", "0421_machine_usage", "0422_plugin_audit_at",
    "0423_todo_thread", "0427_manager_outbox", "0428_manager_moved", "0429_todo_source", "0430_sealing_key",
    "0431_extension_tokens", "0432_extension_market_key", "0433_extension_identities",
    "0441_feed_routed_from", "0442_todo_due", "0443_extension_storage", "0451_todo_supervision_auto_review",
    "0452_extension_storage_bytes", "0453_ask_todo_no_origin", "0461_todo_project",
    "0463_sent_as_user_archive", "0467_extension_pack_off", "0471_inbox_json", "0482_subscriptions",
    "0487_todo_kind", "0488_todo_criteria", "0492_agent_shares", "0493_share_ingestion_queue",
    "0494_session_share_ownership", "0495_assistant_feed_updated", "0498_task_briefs", "0499_todo_imports",
    "0503_todo_supervision_waits", "0504_confirmation_metrics", "0506_todo_seen", "0511_assistant_search",
    "0512_todo_inbox_item", "0513_assistant_search_plain", "0514_assistant_search_strong",
    "0516_todo_repositories", "0517_todo_repository_link_receipts", "0521_workspace_board_activity",
    "0523_todo_supervision_pending_since", "0531_urgent_calls", "0532_machine_usage_history",
    "0533_usage_backfill", "0542_todo_request_pieces", "0543_machine_metrics_battery_temp", "0551_usage_by_agent",
    "0552_todo_restored_someday", "0556_session_mode_changes", "0561_model_usage"
)))


def stamp_problem(name, now):
    """Why a new migration file name is not a timestamp a migration written now may have, or None."""
    match = TIMESTAMPED.match(name)
    if not match:
        return "is not named by the UTC second it was written"
    try:
        at = datetime(*(int(part) for part in match.groups()), tzinfo=timezone.utc)
    except ValueError:
        return "does not start with a real UTC time"
    if at < FIRST_STAMP:
        return "is from before migrations were timestamped ({0:%Y-%m-%d %H:%M:%S} UTC)".format(FIRST_STAMP)
    if at > now + STAMP_AHEAD:
        return "is more than a day after now ({0} UTC)".format(now.astimezone(timezone.utc).strftime(STAMP_FORMAT))
    return None


class FreezeError(Exception):
    """The check could not be run (no base commit, or git failed)."""


def normalize_sql(text):
    """SQL with comments removed and whitespace outside quotes removed.

    Quoted strings (and quoted identifiers) keep their exact text, so a space inside a literal
    is still a change. A comment or a blank line is not.
    """
    text = text.replace("\r\n", "\n").replace("\r", "\n")
    out = []
    i = 0
    n = len(text)
    while i < n:
        ch = text[i]
        if ch == "-" and i + 1 < n and text[i + 1] == "-":
            i += 2
            while i < n and text[i] != "\n":
                i += 1
            continue
        if ch == "/" and i + 1 < n and text[i + 1] == "*":
            i += 2
            while i + 1 < n and not (text[i] == "*" and text[i + 1] == "/"):
                i += 1
            i = i + 2 if i + 1 < n else n
            continue
        if ch in ("'", '"'):
            j = i + 1
            while j < n:
                if text[j] == ch:
                    if j + 1 < n and text[j + 1] == ch:
                        j += 2
                        continue
                    j += 1
                    break
                j += 1
            out.append(text[i:j])
            i = j
            continue
        if ch.isspace():
            i += 1
            continue
        out.append(ch)
        i += 1
    return "".join(out)


def frozen_changes(base, current):
    """Paths in base that current deleted, renamed or edited. Extra files in current are new."""
    found = []
    for path in sorted(base):
        if path not in current:
            found.append((path, "deleted"))
        elif normalize_sql(base[path]) != normalize_sql(current[path]):
            found.append((path, "edited"))
    return found


def changes_against(base, views):
    """Report each frozen path once. A missing copy wins over an edited one."""
    kinds = {}
    for view in views:
        for path, kind in frozen_changes(base, view):
            if path not in kinds or kind == "deleted":
                kinds[path] = kind
    return [(path, kinds[path]) for path in sorted(kinds)]


def addition_problems(base, views, now, frozen=NUMBERED):
    """(path, problem) for each new path (in a view, not on the base), once: a numbered file outside the frozen set, or
    a name that is not a timestamp a migration written now may have. problem is None for a numbered file."""
    found = []
    for path in sorted({path for view in views for path in view if path not in base}):
        name = path.rsplit("/", 1)[-1]
        if NUMBERED_NAME.match(name):
            if path[len(MIGRATIONS):] not in frozen:
                found.append((path, None))
            continue
        problem = stamp_problem(name, now)
        if problem:
            found.append((path, problem))
    return found


def colliding(files):
    """Paths another file of their set shares a version with."""
    by_version = {}
    for path in files:
        folder, name = path.rsplit("/", 1)
        match = VERSIONED_NAME.match(name)
        if match:
            by_version.setdefault((folder, int(match.group(1))), []).append(path)
    return {path for paths in by_version.values() if len(paths) > 1 for path in paths}


def _git(root, args, check=True):
    result = subprocess.run(["git", "-C", str(root), *args], capture_output=True, timeout=60)
    if check and result.returncode != 0:
        detail = (result.stderr or result.stdout).decode("utf-8", "replace").strip()
        raise FreezeError("migration freeze: git {0} failed{1}".format(
            " ".join(args[:6]), ": " + detail if detail else ""))
    return result


def _default_ref(root):
    """The clone's default branch, local refs only: origin/HEAD, else origin/main, else origin/master."""
    symbolic = subprocess.run(
        ["git", "-C", str(root), "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"],
        capture_output=True, text=True, timeout=30)
    if symbolic.returncode == 0 and symbolic.stdout.strip():
        candidates = [symbolic.stdout.strip()]
    else:
        candidates = ["refs/remotes/origin/HEAD"]
    candidates.extend(["refs/remotes/origin/main", "refs/remotes/origin/master"])
    for ref in candidates:
        verify = subprocess.run(
            ["git", "-C", str(root), "rev-parse", "--verify", "--quiet", "--end-of-options", ref + "^{commit}"],
            capture_output=True, timeout=30)
        if verify.returncode == 0:
            return ref
    return None


def resolve_base_commit(root, base=None, env=None):
    """The commit whose migrations are already on the base branch (merge-base of HEAD and the ref)."""
    env = os.environ if env is None else env
    ref = base or env.get("CI_BASE_SHA") or _default_ref(root)
    if not ref:
        raise FreezeError(
            "migration freeze: no base commit (set CI_BASE_SHA, pass --base, or fetch origin: "
            "origin/HEAD, else origin/main)")
    sha = _git(root, ["rev-parse", "--verify", "--end-of-options", ref + "^{commit}"]).stdout.decode().strip()
    return _git(root, ["merge-base", "HEAD", sha]).stdout.decode().strip()


def _blobs(root, specs):
    """Object text keyed by the spec that asked for it. Missing objects are absent."""
    if not specs:
        return {}
    result = subprocess.run(
        ["git", "-C", str(root), "cat-file", "--batch"],
        input="".join(spec + "\n" for spec in specs).encode(),
        capture_output=True, timeout=60)
    if result.returncode != 0 and not result.stdout:
        detail = result.stderr.decode("utf-8", "replace").strip()
        raise FreezeError("migration freeze: git cat-file --batch failed{0}".format(": " + detail if detail else ""))
    found = {}
    data = result.stdout
    pos = 0
    for spec in specs:
        newline = data.find(b"\n", pos)
        if newline < 0:
            raise FreezeError("migration freeze: git cat-file --batch ended early")
        header = data[pos:newline].decode("utf-8", "replace")
        pos = newline + 1
        if header.endswith(" missing"):
            continue
        try:
            size = int(header.rsplit(" ", 1)[1])
        except (IndexError, ValueError):
            raise FreezeError("migration freeze: unexpected git cat-file header: " + header)
        blob = data[pos:pos + size]
        pos += size + 1
        found[spec] = blob.decode("utf-8", "backslashreplace")
    return found


def _matching(names):
    return [name for name in names if MIGRATION_PATH.match(name)]


def _named_blobs(root, names, spec_for):
    """Blob text for every name. A name git listed but cannot show is an error, not a pass."""
    specs = [spec_for(name) for name in names]
    blobs = _blobs(root, specs)
    found = {}
    for name, spec in zip(names, specs):
        if spec not in blobs:
            raise FreezeError("migration freeze: git has no blob for {0}".format(spec))
        found[name] = blobs[spec]
    return found


def tree_migrations(root, commit):
    raw = _git(root, ["ls-tree", "-r", "-z", "--name-only", commit, "--", *MIGRATION_DIRS]).stdout
    names = _matching(raw.decode("utf-8", "surrogateescape").split("\0"))
    return _named_blobs(root, names, lambda name: "{0}:{1}".format(commit, name))


def index_migrations(root):
    raw = _git(root, ["ls-files", "-z", "--", *MIGRATION_DIRS]).stdout
    names = _matching(raw.decode("utf-8", "surrogateescape").split("\0"))
    return _named_blobs(root, names, lambda name: ":" + name)


def worktree_migrations(root):
    found = {}
    for directory in MIGRATION_DIRS:
        folder = Path(root) / directory
        if not folder.is_dir():
            continue
        for path in folder.iterdir():
            rel = directory + "/" + path.name
            if path.is_file() and MIGRATION_PATH.match(rel):
                found[rel] = path.read_text(encoding="utf-8", errors="backslashreplace")
    return found


def describe(path, kind):
    if kind == "deleted":
        action = "was deleted or renamed after it reached the base branch"
    else:
        action = "was edited after it reached the base branch"
    return "migration freeze: {0} {1}; add a new migration instead".format(path, action)


def describe_addition(path, problem):
    if problem is None:
        problem = "is numbered by hand, and numbers stopped at 0561 (the frozen numbered files)"
    return "migration freeze: {0} {1}; {2}".format(path, problem, HOW_TO_NAME)


def check(root, base=None, env=None, now=None):
    """Return (commit, frozen count, violation lines). Raises FreezeError when the base cannot be resolved."""
    root = Path(root)
    now = datetime.now(timezone.utc) if now is None else now
    commit = resolve_base_commit(root, base, env)
    base_files = tree_migrations(root, commit)
    views = [index_migrations(root), worktree_migrations(root)]
    renamable = colliding(base_files)
    violations = [describe(path, kind) for path, kind in changes_against(base_files, views)
                  if not (kind == "deleted" and path in renamable)]
    violations += [describe_addition(path, problem) for path, problem in addition_problems(base_files, views, now)]
    return commit, len(base_files), violations


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--base", help="base ref (default: CI_BASE_SHA, else origin/HEAD, else origin/main)")
    parser.add_argument("--root", default=".", help="repository root (default: the working directory)")
    args = parser.parse_args(argv)
    try:
        commit, count, violations = check(args.root, args.base)
    except FreezeError as error:
        print(error, file=sys.stderr)
        return 2
    if violations:
        print("\n".join(violations), file=sys.stderr)
        return 1
    print("migration freeze: {0} migration(s) on {1} are unchanged".format(count, commit[:12]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
