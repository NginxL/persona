"""A migration already on the base branch cannot be edited, deleted or renamed.

Comment-only and whitespace-only edits pass. A new file may be added, named by the UTC second it
was written. The cases build a throwaway repository; they do not touch this checkout's migrations.
"""
from datetime import datetime, timezone
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import migration_freeze

ROOT = Path(__file__).resolve().parents[2]
HUB = "server/internal/db/migrations/hub/0001_init.sql"
MANAGER = "server/internal/db/migrations/manager/0002_settings.sql"
STAMPED = "server/internal/db/migrations/hub/20261119123000_extra.sql"
NUMBERED = "server/internal/db/migrations/manager/0009_extra.sql"
# A fixed now for the naming cases: STAMPED is a few hours before it.
NOW = datetime(2026, 11, 19, 18, 0, 0, tzinfo=timezone.utc)
ORIGINAL = "CREATE TABLE devices (\n    id int\n);\n"
OTHER = "-- the manager's settings\nCREATE TABLE settings (id int);\n"


class NormalizeTests(unittest.TestCase):
    def test_comments_blank_lines_and_indentation_match(self):
        edited = "-- added\nCREATE TABLE devices (id int);\n\n"
        self.assertEqual(migration_freeze.normalize_sql(ORIGINAL), migration_freeze.normalize_sql(edited))
        crlf = ORIGINAL.replace("\n", "\r\n")
        self.assertEqual(migration_freeze.normalize_sql(ORIGINAL), migration_freeze.normalize_sql(crlf))
        block = "CREATE TABLE devices (/* key */ id int);"
        self.assertEqual(migration_freeze.normalize_sql(ORIGINAL), migration_freeze.normalize_sql(block))

    def test_whitespace_inside_a_literal_is_kept(self):
        self.assertNotEqual(
            migration_freeze.normalize_sql("SELECT 'a b';"),
            migration_freeze.normalize_sql("SELECT 'a  b';"))
        self.assertNotEqual(
            migration_freeze.normalize_sql("SELECT 'a--b';"),
            migration_freeze.normalize_sql("SELECT 'a--c';"))
        self.assertEqual(
            migration_freeze.normalize_sql("SELECT 'a''b';"),
            migration_freeze.normalize_sql("SELECT 'a''b' ;"))

    def test_reindenting_a_dollar_quoted_body_matches(self):
        spaced = "DO $$\nBEGIN\n    INSERT INTO t VALUES ('a b');\nEND $$;\n"
        flat = "DO $$ BEGIN INSERT INTO t VALUES ('a b'); END $$;"
        self.assertEqual(migration_freeze.normalize_sql(spaced), migration_freeze.normalize_sql(flat))
        changed = "DO $$ BEGIN INSERT INTO t VALUES ('a  b'); END $$;"
        self.assertNotEqual(migration_freeze.normalize_sql(spaced), migration_freeze.normalize_sql(changed))

    def test_commenting_out_the_statement_is_an_edit(self):
        self.assertNotEqual(
            migration_freeze.normalize_sql(ORIGINAL),
            migration_freeze.normalize_sql("/* " + ORIGINAL + " */\n"))

    def test_a_real_sql_change_does_not_match(self):
        self.assertNotEqual(
            migration_freeze.normalize_sql(ORIGINAL),
            migration_freeze.normalize_sql("CREATE TABLE devices (id text);\n"))


class CompareTests(unittest.TestCase):
    def test_reports_edits_deletions_and_renames_but_not_additions_or_comments(self):
        base = {HUB: ORIGINAL, MANAGER: OTHER}
        self.assertEqual(migration_freeze.frozen_changes(base, dict(base)), [])
        commented = {HUB: "-- note\n" + ORIGINAL, MANAGER: OTHER}
        self.assertEqual(migration_freeze.frozen_changes(base, commented), [])
        added = dict(base)
        added["server/internal/db/migrations/hub/0009_extra.sql"] = "CREATE TABLE extra (id int);\n"
        self.assertEqual(migration_freeze.frozen_changes(base, added), [])
        edited = dict(base)
        edited[HUB] = "CREATE TABLE devices (id text);\n"
        self.assertEqual(migration_freeze.frozen_changes(base, edited), [(HUB, "edited")])
        renamed = {MANAGER: OTHER, "server/internal/db/migrations/hub/0001_init_v2.sql": ORIGINAL}
        self.assertEqual(migration_freeze.frozen_changes(base, renamed), [(HUB, "deleted")])
        self.assertEqual(migration_freeze.frozen_changes(base, {MANAGER: OTHER}), [(HUB, "deleted")])

    def test_a_missing_copy_wins_over_an_edited_one(self):
        base = {HUB: ORIGINAL}
        index = {HUB: "CREATE TABLE devices (id text);\n"}
        worktree = {}
        self.assertEqual(migration_freeze.changes_against(base, [index, worktree]), [(HUB, "deleted")])

    def test_messages_tell_the_author_to_add_a_migration(self):
        edited = migration_freeze.describe(HUB, "edited")
        deleted = migration_freeze.describe(HUB, "deleted")
        self.assertEqual(edited.count("\n"), 0)
        self.assertIn("was edited", edited)
        self.assertIn("was deleted or renamed", deleted)
        self.assertIn("add a new migration instead", edited)
        self.assertIn("add a new migration instead", deleted)
        self.assertTrue(edited.startswith("migration freeze: " + HUB))
        for problem in (None, "is not named by the UTC second it was written"):
            line = migration_freeze.describe_addition(NUMBERED, problem)
            self.assertEqual(line.count("\n"), 0)
            self.assertTrue(line.startswith("migration freeze: " + NUMBERED))
            self.assertIn("YYYYMMDDHHMMSS_<name>.sql", line)
            self.assertIn("$(date -u +%Y%m%d%H%M%S)_<name>.sql", line)
        self.assertIn("numbers stopped at 0561", migration_freeze.describe_addition(NUMBERED, None))


class NamingTests(unittest.TestCase):
    def test_a_new_name_is_a_real_utc_second_from_the_switch_to_a_day_ahead(self):
        for name in ("20261119123000_probe.sql", "20261118000000_first_second.sql", "20261120180000_a_day_ahead.sql",
                     "20261119235959_end_of_day.sql"):
            self.assertIsNone(migration_freeze.stamp_problem(name, NOW), name)
        for name, problem in (
                ("0562_probe.sql", "not named by the UTC second"),
                ("202611191230_probe.sql", "not named by the UTC second"),
                ("2026111912300_probe.sql", "not named by the UTC second"),
                ("202611191230000_probe.sql", "not named by the UTC second"),
                ("20261119123000_Probe.sql", "not named by the UTC second"),
                ("20261119123000-probe.sql", "not named by the UTC second"),
                ("20261119123000_probe.SQL", "not named by the UTC second"),
                ("20261119123000.sql", "not named by the UTC second"),
                ("20261301123000_probe.sql", "not start with a real UTC time"),
                ("20260230123000_probe.sql", "not start with a real UTC time"),
                ("20261119240000_probe.sql", "not start with a real UTC time"),
                ("20261119126000_probe.sql", "not start with a real UTC time"),
                ("20261119123060_probe.sql", "not start with a real UTC time"),
                ("20261117235959_probe.sql", "before migrations were timestamped (2026-11-18 00:00:00 UTC)"),
                ("20261120180001_probe.sql", "more than a day after now (20261119180000 UTC)"),
                ("20271119123000_probe.sql", "more than a day after now")):
            self.assertIn(problem, migration_freeze.stamp_problem(name, NOW) or "", name)

    def test_the_frozen_numbered_files_are_this_checkout_s(self):
        names = set()
        for directory in migration_freeze.MIGRATION_DIRS:
            for path in (ROOT / directory).iterdir():
                if migration_freeze.NUMBERED_NAME.match(path.name):
                    names.add(directory[len(migration_freeze.MIGRATIONS):] + "/" + path.name)
        self.assertEqual(names, set(migration_freeze.NUMBERED))
        self.assertEqual(122, len(migration_freeze.NUMBERED))
        self.assertIn("manager/0561_model_usage.sql", migration_freeze.NUMBERED)

    def test_a_new_file_is_timestamped_or_one_of_the_frozen_numbered_ones(self):
        base = {HUB: ORIGINAL, MANAGER: OTHER}
        frozen = {"hub/0001_init.sql", "manager/0002_settings.sql", "manager/0003_older.sql"}
        older = "server/internal/db/migrations/manager/0003_older.sql"
        current = dict(base)
        current[STAMPED] = "CREATE TABLE extra (id int);\n"
        current[older] = "CREATE TABLE older (id int);\n"
        self.assertEqual(migration_freeze.addition_problems(base, [current, dict(current)], NOW, frozen), [])
        current[NUMBERED] = "CREATE TABLE more (id int);\n"
        self.assertEqual(migration_freeze.addition_problems(base, [current, dict(current)], NOW, frozen), [(NUMBERED, None)])
        self.assertEqual(migration_freeze.addition_problems(base, [{NUMBERED: ""}, base], NOW, frozen), [(NUMBERED, None)])
        # A frozen number in the other set is new there.
        moved = "server/internal/db/migrations/hub/0003_older.sql"
        self.assertEqual(migration_freeze.addition_problems(base, [{moved: ""}], NOW, frozen), [(moved, None)])
        late = "server/internal/db/migrations/hub/20271119123000_late.sql"
        self.assertEqual(migration_freeze.addition_problems(base, [{late: ""}], NOW, frozen),
                         [(late, "is more than a day after now (20261119180000 UTC)")])

    def test_files_of_one_version_in_a_set_collide(self):
        files = [
            "server/internal/db/migrations/hub/0421_hub_settings.sql",
            "server/internal/db/migrations/manager/0421_machine_usage.sql",
            "server/internal/db/migrations/hub/20261119123000_a.sql",
            "server/internal/db/migrations/manager/20261119123000_b.sql",
            "server/internal/db/migrations/manager/20261120123000_c.sql",
            "server/internal/db/migrations/manager/20261120123000_d.sql",
        ]
        self.assertEqual(migration_freeze.colliding(files), set(files[4:]))


class GitFreezeTests(unittest.TestCase):
    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="migration-freeze-"))
        self.addCleanup(self._cleanup)
        self._saved_base = os.environ.pop("CI_BASE_SHA", None)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "t@example.com")
        self.git("config", "user.name", "t")
        self.write(HUB, ORIGINAL)
        self.write(MANAGER, OTHER)
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "base")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

    def _cleanup(self):
        if self._saved_base is None:
            os.environ.pop("CI_BASE_SHA", None)
        else:
            os.environ["CI_BASE_SHA"] = self._saved_base
        subprocess.run(["rm", "-rf", str(self.repo)], check=True)

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True, timeout=30)

    def run_check(self, *args):
        code = migration_freeze.main(["--root", str(self.repo), *args])
        return code

    def test_a_clean_tree_passes(self):
        stdout = sys.stdout
        try:
            from io import StringIO
            buf = StringIO()
            sys.stdout = buf
            code = self.run_check()
        finally:
            sys.stdout = stdout
        self.assertEqual(0, code)
        self.assertIn("2 migration(s)", buf.getvalue())

    def test_a_comment_or_reindent_passes_and_a_new_file_passes(self):
        self.write(HUB, "-- added\r\nCREATE TABLE devices (id int);\n")
        self.assertEqual(0, self.run_check())
        self.write(STAMPED, "CREATE TABLE extra (id int);\n")
        self.assertEqual(0, self.run_check())

    def run_check_err(self, *args):
        stderr = sys.stderr
        try:
            from io import StringIO
            buf = StringIO()
            sys.stderr = buf
            code = self.run_check(*args)
        finally:
            sys.stderr = stderr
        return code, buf.getvalue()

    def test_a_new_numbered_migration_fails(self):
        self.write(STAMPED, "CREATE TABLE extra (id int);\n")
        self.write(NUMBERED, "CREATE TABLE more (id int);\n")
        self.git("add", NUMBERED)
        code, err = self.run_check_err()
        self.assertEqual(1, code)
        self.assertEqual(1, err.count(NUMBERED), err)
        self.assertIn("numbers stopped at 0561", err)
        self.assertNotIn(STAMPED, err)

    def test_a_misnamed_timestamp_fails(self):
        minute = "server/internal/db/migrations/hub/202611191230_extra.sql"
        self.write(minute, "CREATE TABLE extra (id int);\n")
        code, err = self.run_check_err()
        self.assertEqual(1, code)
        self.assertIn(minute + " is not named by the UTC second it was written", err)

    def test_a_frozen_numbered_file_passes_against_an_older_base(self):
        # The base is from before a numbered migration of the frozen set merged: it is new there, and still passes.
        older = "server/internal/db/migrations/manager/0561_model_usage.sql"
        self.write(older, "CREATE TABLE model_usage (id int);\n")
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "numbered")
        self.assertEqual(0, self.run_check("--base", "HEAD~1"))
        self.write(NUMBERED, "CREATE TABLE more (id int);\n")
        code, err = self.run_check_err("--base", "HEAD~1")
        self.assertEqual(1, code)
        self.assertNotIn(older, err)
        self.assertIn(NUMBERED, err)

    def test_one_of_two_files_of_one_version_may_be_renamed(self):
        first = "server/internal/db/migrations/manager/20261119123000_first.sql"
        later = "server/internal/db/migrations/manager/20261119123000_later.sql"
        self.write(first, "CREATE TABLE first (id int);\n")
        self.write(later, "CREATE TABLE later (id int);\n")
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "both merged")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.git("mv", later, "server/internal/db/migrations/manager/20261119123001_later.sql")
        self.assertEqual(0, self.run_check())
        self.write(first, "CREATE TABLE first (id text);\n")
        code, err = self.run_check_err()
        self.assertEqual(1, code)
        self.assertIn(first + " was edited", err)
        (self.repo / HUB).unlink()
        code, err = self.run_check_err()
        self.assertIn(HUB + " was deleted or renamed", err)

    def test_an_edited_migration_fails(self):
        self.write(HUB, "CREATE TABLE devices (id text);\n")
        stderr = sys.stderr
        try:
            from io import StringIO
            buf = StringIO()
            sys.stderr = buf
            code = self.run_check()
        finally:
            sys.stderr = stderr
        self.assertEqual(1, code)
        self.assertIn("was edited", buf.getvalue())
        self.assertIn("add a new migration instead", buf.getvalue())
        self.assertNotIn(MANAGER, buf.getvalue())

    def test_a_deleted_migration_fails(self):
        (self.repo / HUB).unlink()
        stderr = sys.stderr
        try:
            from io import StringIO
            buf = StringIO()
            sys.stderr = buf
            code = self.run_check()
        finally:
            sys.stderr = stderr
        self.assertEqual(1, code)
        self.assertIn("was deleted or renamed", buf.getvalue())
        self.assertIn(HUB, buf.getvalue())

    def test_a_rename_fails_even_when_the_sql_is_unchanged(self):
        target = "server/internal/db/migrations/hub/0001_devices.sql"
        self.git("mv", HUB, target)
        stderr = sys.stderr
        try:
            from io import StringIO
            buf = StringIO()
            sys.stderr = buf
            code = self.run_check()
        finally:
            sys.stderr = stderr
        self.assertEqual(1, code)
        self.assertIn(HUB, buf.getvalue())
        self.assertIn("was deleted or renamed", buf.getvalue())
        self.assertNotIn("0001_devices.sql was", buf.getvalue())

    def test_a_staged_edit_fails_even_when_the_worktree_was_restored(self):
        self.write(HUB, "CREATE TABLE devices (id text);\n")
        self.git("add", HUB)
        self.write(HUB, ORIGINAL)
        stderr = sys.stderr
        try:
            from io import StringIO
            buf = StringIO()
            sys.stderr = buf
            code = self.run_check()
        finally:
            sys.stderr = stderr
        self.assertEqual(1, code)
        self.assertIn("was edited", buf.getvalue())

    def test_migrations_added_on_the_base_after_the_fork_are_not_required(self):
        self.git("branch", "topic")
        self.write("server/internal/db/migrations/hub/0009_later.sql", "CREATE TABLE later (id int);\n")
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "later")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
        self.git("checkout", "-q", "topic")
        self.assertEqual(0, self.run_check())

    def test_origin_head_wins_over_a_moved_origin_main(self):
        # HEAD is the rewritten migration. The default branch (origin/HEAD) is still the parent, so the rewrite is
        # not on the base and must fail. origin/main pointing at HEAD would have hidden it.
        self.git("branch", "trunk")
        self.git("update-ref", "refs/remotes/origin/trunk", "HEAD")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
        self.write(HUB, "CREATE TABLE devices (id text);\n")
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "rewritten")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.assertEqual(1, self.run_check())

    def test_ci_base_sha_is_the_ref(self):
        # The tip matches the default branch again, but CI_BASE_SHA is the commit that rewrote the migration. Freezing
        # that commit fails; ignoring it and using origin/main would pass.
        self.write(HUB, "CREATE TABLE devices (id text);\n")
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "edited")
        os.environ["CI_BASE_SHA"] = subprocess.check_output(
            ["git", "-C", str(self.repo), "rev-parse", "HEAD"], text=True, timeout=30).strip()
        self.write(HUB, ORIGINAL)
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "reverted")
        self.assertEqual(1, self.run_check())
        os.environ.pop("CI_BASE_SHA")
        self.assertEqual(0, self.run_check())

    def test_no_base_exits_2(self):
        self.git("update-ref", "-d", "refs/remotes/origin/main")
        self.git("symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
        stderr = sys.stderr
        try:
            from io import StringIO
            buf = StringIO()
            sys.stderr = buf
            code = self.run_check()
        finally:
            sys.stderr = stderr
        self.assertEqual(2, code)
        self.assertIn("no base commit", buf.getvalue())


class WiringTests(unittest.TestCase):
    def test_guards_and_check_fast_run_the_checker(self):
        guards = (ROOT / "scripts/ci/guards.sh").read_text()
        check_fast = (ROOT / "scripts/test/check-fast.sh").read_text()
        self.assertIn("python3 scripts/ci/migration_freeze.py", guards)
        self.assertIn("python3 scripts/ci/migration_freeze.py", check_fast)
        self.assertIn("server/internal/db/migrations/", check_fast)


if __name__ == "__main__":
    unittest.main()
