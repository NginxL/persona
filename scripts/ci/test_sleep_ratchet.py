"""Fixed time.Sleep calls in Go tests can stay or fall, never rise.

Comments, string literals and a // sleep-ok: <reason> line do not count. Only a rise fails: a file
under its baseline passes with a notice, and with a base a changed file is also held to its count at
the merge base. The cases build throwaway trees; one case checks this checkout's committed baseline.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import sleep_ratchet

ROOT = Path(__file__).resolve().parents[2]
SERVER = "server/internal/demo/demo_test.go"
SDK = "sdk/go/ext/ext_test.go"


def source(sleeps):
    body = "\n".join("\ttime.Sleep(time.Second)" for _ in range(sleeps))
    return "package demo\n\nimport \"time\"\n\nfunc TestWait(t *testing.T) {\n" + body + "\n}\n"


class CountTests(unittest.TestCase):
    def test_calls_count_and_lookalikes_do_not(self):
        text = "\n".join([
            "package demo",
            "func TestA(t *testing.T) {",
            "\ttime.Sleep(time.Second)",
            "\ttime.Sleep(time.Millisecond)",
            "\ttime.Now()",
            "\ttime.Sleeper()",
            "\tmytime.Sleep(time.Second)",
            "\truntime.Sleep(time.Second)",
            "\ttimeout.Sleep(time.Second)",
            "}",
        ])
        self.assertEqual(2, sleep_ratchet.count_sleeps(text))

    def test_comments_and_strings_are_not_calls(self):
        text = "\n".join([
            "package demo",
            "// time.Sleep(time.Second)",
            "/* time.Sleep(time.Second)",
            "still time.Sleep(time.Second)",
            "*/",
            'const interpreted = "time.Sleep(\\"time.Sleep(\\""',
            "const raw = `time.Sleep(",
            "time.Sleep(",
            "`",
            "func TestA(t *testing.T) { time.Sleep(time.Second) }",
        ])
        self.assertEqual(1, sleep_ratchet.count_sleeps(text))

    def test_a_call_split_across_lines_still_counts(self):
        text = "package demo\nfunc TestA(t *testing.T) {\n\ttime\n\t\t.\n\t\tSleep\n\t\t(\n\t\ttime.Second)\n}\n"
        self.assertEqual(1, sleep_ratchet.count_sleeps(text))
        commented = "package demo\nfunc TestA(t *testing.T) {\n\ttime /* pkg */ . /* name */ Sleep(time.Second)\n}\n"
        self.assertEqual(1, sleep_ratchet.count_sleeps(commented))

    def test_sleep_ok_needs_a_real_comment_and_a_reason(self):
        kept = "\ttime.Sleep(time.Second) // sleep-ok: the child exits on its own"
        self.assertEqual(0, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + kept + "\n}\n"))
        empty = "\ttime.Sleep(time.Second) // sleep-ok:"
        blank = "\ttime.Sleep(time.Second) // sleep-ok:   "
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + empty + "\n}\n"))
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + blank + "\n}\n"))
        both = "\ttime.Sleep(time.Second); time.Sleep(time.Millisecond) // sleep-ok: one bounded wait"
        self.assertEqual(0, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + both + "\n}\n"))
        # The marker has to be the comment itself, on the call's line, and not text inside a string.
        decoy = '\ttime.Sleep(time.Second); s := "// sleep-ok: reason"'
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + decoy + "\n}\n"))
        raw = "\ttime.Sleep(time.Second); s := `// sleep-ok: reason`"
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + raw + "\n}\n"))
        block = "\ttime.Sleep(time.Second) /* sleep-ok: reason */"
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + block + "\n}\n"))
        earlier = "\t// sleep-ok: reason\n\ttime.Sleep(time.Second)"
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + earlier + "\n}\n"))
        aside = "\ttime.Sleep(time.Second) // see sleep-ok: reason"
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + aside + "\n}\n"))

    def test_the_escape_on_the_parenthesis_line_covers_a_split_call(self):
        split = "\ttime.\n\tSleep(time.Second) // sleep-ok: covers the call"
        self.assertEqual(0, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + split + "\n}\n"))
        wrong_line = "\ttime. // sleep-ok: not the call line\n\tSleep(time.Second)"
        self.assertEqual(1, sleep_ratchet.count_sleeps("package p\nfunc TestA(t *testing.T) {\n" + wrong_line + "\n}\n"))

    def test_crlf_and_a_block_comment_do_not_shift_the_count(self):
        text = "/*\r\ntime.Sleep(time.Second)\r\n*/\r\ntime.Sleep(time.Second)\r\n"
        self.assertEqual(1, sleep_ratchet.count_sleeps(text))


class TreeTests(unittest.TestCase):
    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="sleep-ratchet-"))
        self.addCleanup(self._cleanup)
        self._saved_base = os.environ.pop("CI_BASE_SHA", None)

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

    def baseline(self, counts):
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render(counts))

    def run_check(self, *args):
        return sleep_ratchet.main(["--root", str(self.repo), *args])

    def test_a_new_file_starts_at_zero_unless_every_call_is_escaped(self):
        self.baseline({})
        self.write(SERVER, source(1))
        code, err = self._run()
        self.assertEqual(1, code)
        self.assertIn(SERVER, err)
        self.assertIn("baseline 0", err)
        self.assertIn("docs/lessons/testing.md", err)
        self.assertIn("synctest", err)
        self.assertIn("polling helper", err)
        self.assertIn("channel", err)
        self.assertEqual(1, len([line for line in err.splitlines() if line.strip()]))
        self.write(SERVER, "package demo\n\nfunc TestWait(t *testing.T) {\n"
                   "\ttime.Sleep(time.Second) // sleep-ok: waits for the helper process\n}\n")
        code, out = self._run()
        self.assertEqual(0, code, out)

    def test_over_fails_and_under_passes_with_a_notice_that_points_at_lowering(self):
        self.baseline({SERVER: 1})
        self.write(SERVER, source(2))
        code, err = self._run()
        self.assertEqual(1, code)
        self.assertIn("2 time.Sleep call(s), baseline 1", err)
        self.write(SERVER, source(1))
        code, out = self._run()
        self.assertEqual(0, code, out)
        self.assertNotIn("under the baseline", out)
        self.write(SERVER, source(0))
        code, out = self._run()
        self.assertEqual(0, code, out)
        self.assertIn("1 file(s) under the baseline", out)
        self.assertIn("0 time.Sleep call(s), baseline 1", out)
        self.assertIn("python3 scripts/ci/lower_baselines.py", out)
        self.assertIn("python3 scripts/ci/sleep_ratchet.py --update", out)

    def test_the_notice_names_ten_files_and_counts_the_rest(self):
        files = ["server/internal/demo/f{0:02d}_test.go".format(n) for n in range(12)]
        self.baseline({name: 1 for name in files})
        code, out = self._run()
        self.assertEqual(0, code, out)
        self.assertIn("12 file(s) under the baseline", out)
        self.assertEqual(10, out.count("0 time.Sleep call(s), baseline 1\n"))
        self.assertIn("... and 2 more", out)

    def test_other_trees_are_ignored(self):
        self.baseline({})
        self.write("web/src/demo_test.go", source(3))
        self.write("notes/demo_test.go", source(3))
        code, out = self._run()
        self.assertEqual(0, code, out)

    def test_update_lowers_and_drops_a_deleted_file_but_refuses_to_raise_or_add(self):
        self.baseline({SERVER: 2, SDK: 1})
        self.write(SERVER, source(3))
        self.write(SDK, source(1))
        code, err = self._update()
        self.assertEqual(1, code)
        self.assertIn("refusing to raise", err)
        self.assertEqual((self.repo / sleep_ratchet.BASELINE_REL).read_text(),
                         sleep_ratchet.render({SERVER: 2, SDK: 1}))
        self.write("server/internal/demo/extra_test.go", source(1))
        self.write(SERVER, source(1))
        code, err = self._update()
        self.assertEqual(1, code)
        self.assertIn("refusing to add server/internal/demo/extra_test.go", err)
        self.assertIn('"server/internal/demo/demo_test.go": 2', (self.repo / sleep_ratchet.BASELINE_REL).read_text())
        (self.repo / "server/internal/demo/extra_test.go").unlink()
        (self.repo / SDK).unlink()
        self.write(SERVER, source(1))
        code, out = self._update()
        self.assertEqual(0, code, out)
        self.assertIn("lowered", out)
        self.assertEqual({SERVER: 1}, json.loads((self.repo / sleep_ratchet.BASELINE_REL).read_text()))
        code, out = self._update()
        self.assertEqual(0, code, out)
        self.assertIn("already matches", out)

    def test_update_refuses_to_create_a_baseline(self):
        self.write(SERVER, source(1))
        code, err = self._update()
        self.assertEqual(2, code)
        self.assertIn("refusing to create", err)
        self.assertFalse((self.repo / sleep_ratchet.BASELINE_REL).exists())

    def test_a_missing_baseline_fails_closed(self):
        self.write(SERVER, source(0))
        code, err = self._run()
        self.assertEqual(1, code)
        self.assertIn("baseline missing", err)

    def _run(self):
        return self._capture(self.run_check)

    def _update(self):
        return self._capture(lambda: self.run_check("--update"))

    def _capture(self, run):
        from io import StringIO
        out, err = StringIO(), StringIO()
        stdout, stderr = sys.stdout, sys.stderr
        try:
            sys.stdout, sys.stderr = out, err
            code = run()
        finally:
            sys.stdout, sys.stderr = stdout, stderr
        return code, out.getvalue() + err.getvalue()


class GitCase(unittest.TestCase):
    """A repository whose origin/main has SERVER with one sleep and the matching baseline."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="sleep-ratchet-git-"))
        self.addCleanup(self._cleanup)
        self._saved_base = os.environ.pop("CI_BASE_SHA", None)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "t@example.com")
        self.git("config", "user.name", "t")
        self.write(SERVER, source(1))
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 1}))
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "baseline")
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

    def _capture(self, args, root=None):
        from io import StringIO
        out, err = StringIO(), StringIO()
        stdout, stderr = sys.stdout, sys.stderr
        try:
            sys.stdout, sys.stderr = out, err
            code = sleep_ratchet.main(["--root", str(root or self.repo), *args])
        finally:
            sys.stdout, sys.stderr = stdout, stderr
        return code, out.getvalue() + err.getvalue()


class GitTests(GitCase):
    def test_a_clean_tree_passes_and_a_hand_raised_baseline_fails(self):
        code, out = self._capture([])
        self.assertEqual(0, code, out)
        self.assertIn("1 Go test file", out)
        self.write(SERVER, source(2))
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 2}))
        code, err = self._capture([])
        self.assertEqual(1, code)
        self.assertIn("baseline raises", err)
        self.assertIn("baselines are never raised", err)

    def test_a_moved_file_keeps_its_entry_but_may_not_rise(self):
        moved = "server/internal/modules/demo/demo_test.go"
        self.git("checkout", "-q", "-b", "move")
        (self.repo / moved).parent.mkdir(parents=True)
        self.git("mv", SERVER, moved)
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({moved: 1}))
        code, out = self._capture([])
        self.assertEqual(0, code, out)  # uncommitted: against HEAD and the base
        self.git("commit", "-q", "-am", "move")
        code, out = self._capture([])
        self.assertEqual(0, code, out)
        # Moved and raised: still a raise.
        self.write(moved, source(2))
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({moved: 2}))
        code, err = self._capture([])
        self.assertEqual(1, code)
        self.assertIn("baseline raises " + moved + " from 1 to 2", err)

    def test_a_copy_is_no_move(self):
        copy = "server/internal/other/demo_test.go"
        self.write(copy, source(1))
        self.git("add", copy)
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 1, copy: 1}))
        code, err = self._capture([])
        self.assertEqual(1, code)
        self.assertIn("baseline adds " + copy, err)

    def test_carry_renames_keeps_an_existing_entry(self):
        self.assertEqual({"a": 1, "b": 3}, sleep_ratchet.carry_renames({"a": 1, "b": 3}, {"b": "a"}))
        self.assertEqual({"a": 1, "c": 1}, sleep_ratchet.carry_renames({"a": 1}, {"c": "a", "d": "x"}))

    def test_the_first_snapshot_is_allowed_when_the_base_has_no_baseline(self):
        # An older commit has the test and no baseline. The matching baseline on top of that
        # commit is the first snapshot, so there is no ancestor to raise against.
        self.git("checkout", "-q", "--orphan", "older")
        self.git("rm", "-q", "-f", "--cached", sleep_ratchet.BASELINE_REL)
        (self.repo / sleep_ratchet.BASELINE_REL).unlink()
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "tests only")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 1}))
        code, out = self._capture([])
        self.assertEqual(0, code, out)

    def test_update_refuses_a_hand_raised_baseline_that_already_matches_the_code(self):
        self.write(SERVER, source(2))
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 2}))
        code, err = self._capture(["--update"])
        self.assertEqual(1, code, err)
        self.assertIn("never raised", err)
        self.assertEqual({SERVER: 2}, json.loads((self.repo / sleep_ratchet.BASELINE_REL).read_text()))

    def test_update_lowers_against_a_committed_baseline_and_refuses_a_raise(self):
        self.write(SERVER, source(2))
        code, err = self._capture(["--update"])
        self.assertEqual(1, code)
        self.assertIn("refusing to raise", err)
        self.assertIn('"server/internal/demo/demo_test.go": 1',
                      (self.repo / sleep_ratchet.BASELINE_REL).read_text())
        self.write(SERVER, source(0))
        code, out = self._capture(["--update"])
        self.assertEqual(0, code, out)
        self.assertEqual({}, json.loads((self.repo / sleep_ratchet.BASELINE_REL).read_text()))
        code, out = self._capture([])
        self.assertEqual(0, code, out)

    def test_a_staged_raise_fails_even_when_the_worktree_file_was_restored(self):
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 4}))
        self.git("add", "--", sleep_ratchet.BASELINE_REL)
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 1}))
        code, err = self._capture([])
        self.assertEqual(1, code, err)
        self.assertIn("baseline raises", err)

    def test_an_unresolvable_ci_base_sha_does_not_skip_the_raise_check(self):
        # A baseline already raised on HEAD must not pass just because CI_BASE_SHA is missing from
        # this clone. migration_freeze exits 2 in that case; so does this check.
        self.write(SERVER, source(3))
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 3}))
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "raised")
        parent = subprocess.check_output(
            ["git", "-C", str(self.repo), "rev-parse", "HEAD~1"], text=True, timeout=30).strip()
        os.environ["CI_BASE_SHA"] = parent
        code, err = self._capture([])
        self.assertEqual(1, code, err)
        self.assertEqual(1, err.count("baseline raises"))
        os.environ["CI_BASE_SHA"] = "0123456789abcdef0123456789abcdef01234567"
        code, err = self._capture([])
        self.assertEqual(2, code, err)
        self.assertIn("does not resolve", err)
        self.assertNotIn("Traceback", err)

    def test_a_raise_against_head_and_the_base_is_one_line(self):
        self.git("commit", "--allow-empty", "-q", "-m", "move head")
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 4}))
        code, err = self._capture([])
        self.assertEqual(1, code, err)
        self.assertEqual(1, err.count("baseline raises"))
        self.assertIn("HEAD and ", err)

    def test_a_base_baseline_that_cannot_be_fetched_is_an_error_not_absence(self):
        # CI's guards checkout is blobless (#12753): a branch that changed the baseline has the base's copy fetched
        # on first read. A failed fetch must fail the check, not read as "no baseline at the base" and pass.
        self.git("checkout", "-q", "-b", "raise")
        self.write(SERVER, source(2))
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 2}))
        self.git("commit", "-q", "-am", "raise")
        work = Path(tempfile.mkdtemp(prefix="sleep-ratchet-blobless-"))
        self.addCleanup(subprocess.run, ["rm", "-rf", str(work)], check=True)
        origin = work / "origin.git"
        self.git("clone", "-q", "--bare", str(self.repo), str(origin))
        subprocess.run(["git", "-C", str(origin), "symbolic-ref", "HEAD", "refs/heads/main"], check=True, timeout=30)
        subprocess.run(["git", "-C", str(origin), "config", "uploadpack.allowFilter", "true"], check=True, timeout=30)
        clone = work / "clone"
        subprocess.run(["git", "clone", "-q", "--filter=blob:none", "--branch", "raise", origin.as_uri(), str(clone)],
                       check=True, capture_output=True, timeout=60)
        subprocess.run(["git", "-C", str(clone), "remote", "set-url", "origin", (work / "gone.git").as_uri()],
                       check=True, timeout=30)
        code, err = self._capture([], clone)
        self.assertEqual(2, code, err)
        self.assertIn("cannot read", err)
        subprocess.run(["git", "-C", str(clone), "remote", "set-url", "origin", origin.as_uri()], check=True, timeout=30)
        code, err = self._capture([], clone)
        self.assertEqual(1, code, err)
        self.assertIn("baseline raises", err)

    def test_a_git_timeout_is_a_one_line_error(self):
        real = subprocess.run

        def timed_out(command, **kwargs):
            if "merge-base" in command:
                raise subprocess.TimeoutExpired(command, 60)
            return real(command, **kwargs)

        with patch("sleep_ratchet.subprocess.run", side_effect=timed_out):
            code, err = self._capture([])
        self.assertEqual(2, code, err)
        self.assertIn("timed out", err)
        self.assertNotIn("Traceback", err)


class MergeBaseTests(GitCase):
    """With a given base, a file the change touches is held to the lower of its baseline and its merge-base count."""

    def lower_on_main(self):
        # main takes the sleep out and leaves the baseline at 1: allowed now, and the room a change may not refill.
        self.write(SERVER, source(0))
        self.git("commit", "-q", "-am", "drop the sleep")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.git("checkout", "-q", "-b", "change")

    def head(self):
        return subprocess.check_output(["git", "-C", str(self.repo), "rev-parse", "HEAD"], text=True,
                                       timeout=30).strip()

    def test_main_under_its_baseline_passes(self):
        self.lower_on_main()
        code, out = self._capture(["--base", "origin/main"])
        self.assertEqual(0, code, out)
        self.assertIn("1 file(s) under the baseline", out)

    def test_a_change_cannot_add_back_into_room_under_the_baseline(self):
        self.lower_on_main()
        base = self.head()
        self.write(SERVER, source(1))
        code, err = self._capture(["--base", "origin/main"])
        self.assertEqual(1, code, err)
        self.assertIn(sleep_ratchet.rise_message(SERVER, 1, 0, base[:12], 1), err)
        os.environ["CI_BASE_SHA"] = base  # hosted CI's way of giving the same base
        code, err = self._capture([])
        self.assertEqual(1, code, err)
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "a sleep back")
        code, err = self._capture([])
        self.assertEqual(1, code, err)
        self.assertIn("at the merge base", err)

    def test_the_default_branch_alone_is_no_base(self):
        # Main verification's checkout: HEAD is the commit just merged, and origin/main still names its parent,
        # because the verifier pins HEAD by SHA. merge-pr.sh let the re-add in (changed_ratchets.py), so main must
        # not fail it either: without --base or CI_BASE_SHA only the baseline holds.
        self.lower_on_main()
        self.write(SERVER, source(1))
        self.git("commit", "-q", "-am", "a sleep back")
        code, out = self._capture([])
        self.assertEqual(0, code, out)
        self.assertNotIn("at the merge base", out)
        os.environ["CI_BASE_SHA"] = "HEAD"  # hosted CI on main: the base is HEAD itself
        code, out = self._capture([])
        self.assertEqual(0, code, out)
        os.environ["CI_BASE_SHA"] = "origin/main"  # a base given on purpose still holds the change
        code, err = self._capture([])
        self.assertEqual(1, code, err)

    def test_without_a_base_only_the_baseline_holds(self):
        self.lower_on_main()
        self.write(SERVER, source(1))
        self.git("update-ref", "-d", "refs/remotes/origin/HEAD")
        self.git("update-ref", "-d", "refs/remotes/origin/main")
        code, out = self._capture([])
        self.assertEqual(0, code, out)
        self.write(SERVER, source(2))
        code, err = self._capture([])
        self.assertEqual(1, code, err)
        self.assertIn("2 time.Sleep call(s), baseline 1", err)

    def test_a_file_the_change_does_not_touch_keeps_its_room(self):
        # The merge base itself is under the baseline and the change leaves the file alone: nothing rose.
        self.lower_on_main()
        self.write("server/internal/demo/other_test.go", "package demo\n")
        code, out = self._capture(["--base", "origin/main"])
        self.assertEqual(0, code, out)

    def test_a_count_that_falls_on_the_branch_passes(self):
        self.write(SERVER, source(3))
        self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render({SERVER: 3}))
        self.git("commit", "-q", "-am", "three")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.git("checkout", "-q", "-b", "change")
        self.write(SERVER, source(2))
        code, out = self._capture(["--base", "origin/main"])
        self.assertEqual(0, code, out)
        self.assertIn("2 time.Sleep call(s), baseline 3", out)

    def test_an_explicit_base_that_does_not_resolve_is_an_error(self):
        code, err = self._capture(["--base", "no-such-ref"])
        self.assertEqual(2, code, err)
        self.assertIn("does not resolve", err)


class RepoTests(unittest.TestCase):
    def test_this_checkout_matches_its_baseline(self):
        code, out = _capture_main(["--root", str(ROOT)])
        self.assertEqual(0, code, out)

    def test_the_baseline_file_is_canonical(self):
        path = ROOT / sleep_ratchet.BASELINE_REL
        text = path.read_text(encoding="utf-8")
        self.assertEqual(text, sleep_ratchet.render(sleep_ratchet.parse_baseline(text, sleep_ratchet.BASELINE_REL)))

    def test_guards_and_check_fast_run_the_checker(self):
        guards = (ROOT / "scripts/ci/guards.sh").read_text()
        check_fast = (ROOT / "scripts/test/check-fast.sh").read_text()
        self.assertIn("python3 scripts/ci/sleep_ratchet.py", guards)
        self.assertIn("python3 scripts/ci/sleep_ratchet.py", check_fast)
        self.assertIn("server/*_test.go", check_fast)
        self.assertIn("sdk/go/*_test.go", check_fast)


def _capture_main(args):
    from io import StringIO
    out, err = StringIO(), StringIO()
    stdout, stderr = sys.stdout, sys.stderr
    try:
        sys.stdout, sys.stderr = out, err
        code = sleep_ratchet.main(args)
    finally:
        sys.stdout, sys.stderr = stdout, stderr
    return code, out.getvalue() + err.getvalue()


if __name__ == "__main__":
    unittest.main()
