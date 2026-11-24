"""What main's ratchets will say about a change's files once it merges (#7977).

Each case builds a throwaway repository: main with both baselines, a branch named change from it, and sometimes more
commits on main after the branch. It then checks change merged onto main.
"""
import contextlib
import io
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import changed_ratchets  # noqa: E402
import sleep_ratchet  # noqa: E402
import web_font_ratchet  # noqa: E402

GO = "server/internal/demo/demo_test.go"
NEW_GO = "server/internal/demo/new_test.go"
WEB = "web/src/views/Demo.tsx"
NEW_WEB = "web/src/views/DemoPart.tsx"


def go(first, second=0, note=False):
    """A Go test file: first fixed sleeps in TestFirst, second in TestSecond, far enough apart to merge separately."""
    def test(name, sleeps, extra=""):
        calls = "".join("\ttime.Sleep(time.Second)\n" for _ in range(sleeps))
        return "func {0}(t *testing.T) {{\n{1}{2}\tt.Log(\"done\")\n}}\n".format(name, calls, extra)
    return ("package demo\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\n" + test("TestFirst", first)
            + "\n// TestSecond waits for the same thing.\n\n" + test("TestSecond", second, "\t// noted\n" if note else ""))


def tsx(sizes):
    """A web component with that many hard-coded px font sizes."""
    spans = "".join("    <span className=\"text-[13px]\">a</span>\n" for _ in range(sizes))
    return "export function Demo() {\n  return (\n" + spans + "  );\n}\n"


class RepositoryCase(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="changed-ratchets-")
        self.addCleanup(temp.cleanup)
        self.repo = Path(temp.name)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "t@example.com")
        self.git("config", "user.name", "t")
        self.git("config", "commit.gpgSign", "false")
        self.write(GO, go(1))
        self.write(WEB, tsx(2))
        self.baselines(sleeps={GO: 1}, fonts={WEB: 2})
        self.commit("main")
        self.git("checkout", "-q", "-b", "change")

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True, text=True,
                              timeout=30).stdout.strip()

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def baselines(self, sleeps=None, fonts=None):
        if sleeps is not None:
            self.write(sleep_ratchet.BASELINE_REL, sleep_ratchet.render(sleeps))
        if fonts is not None:
            self.write(web_font_ratchet.BASELINE_REL, web_font_ratchet.render(fonts))

    def commit(self, message):
        self.git("add", "-A")
        self.git("commit", "-q", "-m", message)

    def on_main(self, edit):
        """Commit edit on main after the branch, then come back to the branch."""
        self.git("checkout", "-q", "main")
        edit()
        self.commit("main moves on")
        self.git("checkout", "-q", "change")


class CheckTests(RepositoryCase):
    def check(self):
        failures, notes, _ = changed_ratchets.check(self.repo, "main", "change")
        return failures, notes

    def test_a_sleep_added_to_a_file_fails_with_the_ratchets_own_message(self):
        self.write(GO, go(2))
        self.commit("one more sleep")
        self.assertEqual(self.check(), ([sleep_ratchet.over_message(GO, 2, 1)], []))

    def test_a_new_file_starts_at_zero_unless_marked_sleep_ok(self):
        self.write(NEW_GO, go(1))
        self.commit("a new test that sleeps")
        self.assertEqual(self.check(), ([sleep_ratchet.over_message(NEW_GO, 1, 0)], []))
        self.write(NEW_GO, go(1).replace("time.Sleep(time.Second)", "time.Sleep(time.Second) // sleep-ok: a real tick"))
        self.commit("marked")
        self.assertEqual(self.check(), ([], []))

    def test_sleeps_taken_out_merge_with_or_without_the_baseline_lowered(self):
        self.write(GO, go(0))
        self.commit("no more sleeps")
        self.assertEqual(self.check(), ([], []))
        self.baselines(sleeps={})
        self.commit("lowered")
        self.assertEqual(self.check(), ([], []))

    def test_a_raised_baseline_fails_against_the_base(self):
        self.write(GO, go(2))
        self.baselines(sleeps={GO: 2})
        self.commit("raised")
        base = self.git("rev-parse", "main")
        self.assertEqual(self.check(), (sleep_ratchet.raised_against({GO: 2}, {GO: 1}, base[:12]), []))

    def test_a_moved_file_keeps_its_entry_and_a_moved_raise_still_fails(self):
        moved = "server/internal/modules/demo/demo_test.go"
        (self.repo / moved).parent.mkdir(parents=True)
        self.git("mv", GO, moved)
        self.baselines(sleeps={moved: 1})
        self.commit("moved")
        self.assertEqual(self.check(), ([], []))
        self.write(moved, go(2))
        self.baselines(sleeps={moved: 2})
        self.commit("moved and raised")
        base = self.git("rev-parse", "main")
        self.assertEqual(self.check(), (sleep_ratchet.raised_against({moved: 2}, {moved: 1}, base[:12]), []))

    def test_an_entry_lowered_for_a_file_the_change_does_not_touch_is_checked(self):
        self.baselines(sleeps={})
        self.commit("lowered by hand")
        self.assertEqual(self.check(), ([sleep_ratchet.over_message(GO, 1, 0)], []))

    def test_a_deleted_file_merges_with_or_without_its_baseline_entry(self):
        self.git("rm", "-q", GO)
        self.commit("deleted")
        self.assertEqual(self.check(), ([], []))
        self.baselines(sleeps={})
        self.commit("entry removed")
        self.assertEqual(self.check(), ([], []))

    def test_a_lowered_count_merges_and_a_raised_one_still_does_not(self):
        self.write(GO, go(0))
        self.write(WEB, tsx(1))
        self.commit("fewer sleeps and sizes, baselines left")
        self.assertEqual(self.check(), ([], []))
        self.write(GO, go(2))
        self.write(WEB, tsx(3))
        self.commit("more of both")
        self.assertEqual(self.check(), ([sleep_ratchet.over_message(GO, 2, 1)]
                                        + web_font_ratchet.violations({WEB: 3}, {WEB: 2}), []))

    def test_a_count_raised_back_into_room_a_fall_left_merges(self):
        # Merge time holds a file to its baseline only, never to its count at the base: re-adding what main took out
        # without lowering the baseline merges ("Count ratchets" in docs/operations/testing.md). The checkers' --base
        # mode refuses it; a merge-time refusal would be a separate change.
        self.on_main(lambda: (self.write(GO, go(0)), self.write(WEB, tsx(1))))
        self.git("merge", "-q", "--no-edit", "main")
        self.write(GO, go(1))
        self.write(WEB, tsx(2))
        self.commit("adds back what main took out")
        self.assertEqual(self.check(), ([], []))

    def test_worse_refuses_only_a_rise_above_what_the_base_has(self):
        worse = changed_ratchets.worse
        self.assertFalse(worse(0, 1, 1, 1))  # lowered under the baseline
        self.assertFalse(worse(1, 1, 1, 1))  # unchanged
        self.assertTrue(worse(2, 1, 1, 1))  # raised over the baseline
        self.assertFalse(worse(3, 1, 3, 1))  # the base is already over by as much
        self.assertTrue(worse(4, 1, 3, 1))  # and the change adds more
        self.assertFalse(worse(2, 1, 3, 1))  # or takes some of it back
        self.assertFalse(worse(0, 3, 1, 3))  # the base is under and the change goes further under

    def test_a_file_main_already_fails_is_mains_unless_the_change_makes_it_worse(self):
        self.on_main(lambda: self.write(NEW_GO, go(3)))  # merged without a baseline entry: main is red
        self.write("README.md", "unrelated\n")
        self.commit("unrelated")
        self.assertEqual(self.check(), ([], []))
        self.git("merge", "-q", "--no-edit", "main")
        self.write(NEW_GO, go(3, note=True))
        self.commit("touches the red file, no new sleep")
        self.assertEqual(self.check(), ([], []))
        self.write(NEW_GO, go(3, 1, note=True))
        self.commit("adds a sleep to it")
        self.assertEqual(self.check(), ([sleep_ratchet.over_message(NEW_GO, 4, 0)], []))
        self.write(NEW_GO, go(0, 0, note=True))
        self.commit("fixes it")
        self.assertEqual(self.check(), ([], []))

    def test_the_file_is_counted_as_the_merge_leaves_it(self):
        def main_drops_the_sleep():
            self.write(GO, go(0))
            self.baselines(sleeps={})
        self.on_main(main_drops_the_sleep)
        self.write(GO, go(1, note=True))
        self.commit("edits TestSecond on the old base")
        self.assertEqual(self.check(), ([], []))  # the branch alone still has main's old sleep
        self.write(GO, go(1, 1, note=True))
        self.commit("and adds a sleep there")
        self.assertEqual(self.check(), ([sleep_ratchet.over_message(GO, 1, 0)], []))

    def test_conflicting_edits_are_left_to_the_merge(self):
        def main_drops_the_sleep():
            self.write(GO, go(0))
            self.baselines(sleeps={})
        self.on_main(main_drops_the_sleep)
        self.write(GO, go(3))
        self.commit("edits the same lines")
        self.assertEqual(self.check(), ([], [GO + ": conflicts with the base; fixed sleeps not checked"]))

    def test_web_font_sizes_count_outside_tests(self):
        self.write(WEB, tsx(3))
        self.write("web/src/views/Demo.test.tsx", tsx(4))
        self.commit("one more size, and a test with sizes")
        self.assertEqual(self.check(), (web_font_ratchet.violations({WEB: 3}, {WEB: 2}), []))

    def test_font_sizes_that_move_to_a_new_file_with_the_baseline_pass(self):
        self.write(WEB, tsx(0))
        self.write(NEW_WEB, tsx(2))
        self.commit("split")
        self.assertEqual(self.check(), (web_font_ratchet.violations({WEB: 0, NEW_WEB: 2}, {WEB: 2}), []))
        self.baselines(fonts={NEW_WEB: 2})  # what web_font_ratchet.py --update writes for a move
        self.commit("baseline follows the sizes")
        self.assertEqual(self.check(), ([], []))

    def test_files_outside_the_ratchets_are_not_counted(self):
        self.write("tools/demo/demo_test.go", go(4))
        self.write("web/app/Demo.tsx", tsx(4))
        self.commit("outside")
        self.assertEqual(changed_ratchets.check(self.repo, "main", "change"), ([], [], 0))

    def test_nothing_is_checked_against_a_base_without_the_baselines(self):
        def no_ratchets():
            self.git("rm", "-q", sleep_ratchet.BASELINE_REL, web_font_ratchet.BASELINE_REL)
        self.on_main(no_ratchets)
        self.write(NEW_GO, go(2))
        self.write(NEW_WEB, tsx(2))
        self.commit("sleeps and sizes")
        self.assertEqual(self.check(), ([], []))


class MainTests(RepositoryCase):
    def run_main(self, *args):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = changed_ratchets.main(["--root", str(self.repo), "--base", "main", *args])
        return code, out.getvalue(), err.getvalue()

    def test_exit_codes(self):
        self.write(NEW_GO, "package demo\n")
        self.commit("no sleeps")
        code, out, _ = self.run_main("--head", "change")
        self.assertEqual(code, 0)
        self.assertIn("1 changed file(s)", out)
        self.write(NEW_GO, go(1))
        self.commit("a sleep")
        code, _, err = self.run_main("--head", "change")
        self.assertEqual(code, 1)
        self.assertIn(sleep_ratchet.over_message(NEW_GO, 1, 0), err)
        code, _, err = self.run_main("--head", "no-such-branch")
        self.assertEqual(code, 2)
        self.assertIn("rev-parse", err)

    def test_a_broken_baseline_is_an_error(self):
        self.write(sleep_ratchet.BASELINE_REL, "{")
        self.commit("broken")
        code, _, err = self.run_main("--head", "change")
        self.assertEqual(code, 2)
        self.assertIn("not valid JSON", err)


if __name__ == "__main__":
    unittest.main()
