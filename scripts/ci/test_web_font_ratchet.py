"""Hard-coded pixel font sizes in web/src can stay or fall, never rise.

Only a rise fails: a file under its baseline passes with a notice, and with a base a changed file is
also held to its count at the merge base. The cases build throwaway trees; one case checks this
checkout's committed baseline.
"""
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import web_font_ratchet as ratchet

ROOT = Path(__file__).resolve().parents[2]


def tree(files, baseline=None):
    root = Path(tempfile.mkdtemp(prefix="font-ratchet-"))
    for rel, text in files.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
    if baseline is not None:
        path = root / ratchet.BASELINE_REL
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(baseline), encoding="utf-8")
    return root


class CountTests(unittest.TestCase):
    def test_arbitrary_sizes_count_and_named_ones_do_not(self):
        text = '<p className="text-[13px] sm:text-[12.5px] text-xs text-sm text-[length:11px] text-[var(--x)] text-ink">'
        self.assertEqual(ratchet.count_sizes(text), 3)

    def test_inline_and_css_sizes_count(self):
        self.assertEqual(ratchet.count_sizes("style={{ fontSize: 13 }} {{ fontSize: '12px' }} {{ fontSize: size }}"), 2)
        self.assertEqual(ratchet.count_sizes("a { font-size: 13px; line-height: 14px } b { max-font-size: 9px }"), 1)

    def test_tests_and_other_files_are_not_scanned(self):
        root = tree({
            "web/src/a.tsx": "text-[13px]",
            "web/src/a.test.tsx": "text-[13px] text-[13px]",
            "web/src/b.json": "text-[13px]",
            "web/other/c.tsx": "text-[13px]",
        })
        self.assertEqual(ratchet.scan(root), {"web/src/a.tsx": 1})


class CheckTests(unittest.TestCase):
    def test_at_the_baseline_passes(self):
        root = tree({"web/src/a.tsx": "text-[13px] text-[12px]"}, {"web/src/a.tsx": 2})
        self.assertEqual(ratchet.check(root), [])

    def test_a_rise_fails(self):
        root = tree({"web/src/a.tsx": "text-[13px] text-[12px] text-[11px]"}, {"web/src/a.tsx": 2})
        self.assertEqual(len(ratchet.check(root)), 1)
        self.assertIn("baseline 2", ratchet.check(root)[0])

    def test_a_new_file_fails(self):
        root = tree({"web/src/a.tsx": "text-[13px]", "web/src/new.tsx": "text-[13px]"}, {"web/src/a.tsx": 1})
        found = ratchet.check(root)
        self.assertEqual(len(found), 1)
        self.assertIn("new.tsx", found[0])

    def test_a_fall_passes_with_a_notice(self):
        root = tree({"web/src/a.tsx": "text-[13px]"}, {"web/src/a.tsx": 2})
        notes = []
        self.assertEqual(ratchet.check(root, notes=notes), [])
        self.assertEqual(notes, ["web/src/a.tsx: 1 hard-coded px font size(s), baseline 2"])
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(ratchet.main(["--root", str(root)]), 0)
        self.assertIn("1 file(s) under the baseline", out.getvalue())
        self.assertIn("python3 scripts/ci/lower_baselines.py", out.getvalue())
        self.assertIn("python3 scripts/ci/web_font_ratchet.py --update", out.getvalue())

    def test_a_missing_baseline_is_not_invented(self):
        self.assertIn("baseline missing", ratchet.check(tree({"web/src/a.tsx": "text-[13px]"}))[0])


class UpdateTests(unittest.TestCase):
    def test_update_lowers_and_drops_files_without_sizes(self):
        root = tree({"web/src/a.tsx": "text-[13px]", "web/src/b.tsx": "text-xs"}, {"web/src/a.tsx": 3, "web/src/b.tsx": 1})
        self.assertEqual(ratchet.update(root), [])
        self.assertEqual(json.loads((root / ratchet.BASELINE_REL).read_text()), {"web/src/a.tsx": 1})

    def test_update_refuses_to_raise_or_add(self):
        root = tree({"web/src/a.tsx": "text-[13px] text-[12px]", "web/src/n.tsx": "text-[13px]"}, {"web/src/a.tsx": 1})
        refusals = ratchet.update(root)
        self.assertEqual(len(refusals), 2)
        self.assertEqual(json.loads((root / ratchet.BASELINE_REL).read_text()), {"web/src/a.tsx": 1})

    def test_update_lets_a_split_file_take_what_the_old_one_lost(self):
        root = tree({"web/src/a.tsx": "text-[13px]", "web/src/n.tsx": "text-[13px] text-[12px]"}, {"web/src/a.tsx": 3})
        self.assertEqual(ratchet.update(root), [])
        self.assertEqual(json.loads((root / ratchet.BASELINE_REL).read_text()), {"web/src/a.tsx": 1, "web/src/n.tsx": 2})

    def test_update_refuses_a_new_file_that_takes_more_than_was_lost(self):
        root = tree({"web/src/a.tsx": "text-[13px]", "web/src/n.tsx": "text-[13px] text-[12px] text-[11px]"}, {"web/src/a.tsx": 3})
        self.assertEqual(len(ratchet.update(root)), 1)
        self.assertEqual(json.loads((root / ratchet.BASELINE_REL).read_text()), {"web/src/a.tsx": 3})

    def test_update_does_not_create_a_baseline(self):
        with self.assertRaises(ratchet.FontError):
            ratchet.update(tree({"web/src/a.tsx": "text-[13px]"}))

    def test_a_broken_baseline_is_an_error(self):
        root = tree({"web/src/a.tsx": ""})
        (root / ratchet.BASELINE_REL).parent.mkdir(parents=True, exist_ok=True)
        (root / ratchet.BASELINE_REL).write_text("[1]")
        with self.assertRaises(ratchet.FontError):
            ratchet.check(root)


class MergeBaseTests(unittest.TestCase):
    """With a given base, a file the change touches is held to the lower of its baseline and its merge-base count."""

    def setUp(self):
        self._saved_base = os.environ.pop("CI_BASE_SHA", None)
        self.addCleanup(self._restore_base)
        temp = tempfile.TemporaryDirectory(prefix="font-ratchet-git-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "t@example.com")
        self.git("config", "user.name", "t")
        self.git("config", "commit.gpgSign", "false")
        # main has a.tsx under its baseline (3 recorded, 1 left): allowed now, and room a change may not refill.
        self.write("web/src/a.tsx", "text-[13px]")
        self.write(ratchet.BASELINE_REL, ratchet.render({"web/src/a.tsx": 3}))
        self.git("add", "-A")
        self.git("commit", "-q", "-m", "main")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.git("checkout", "-q", "-b", "change")
        self.label = self.git("rev-parse", "HEAD")[:12]

    def _restore_base(self):
        if self._saved_base is not None:
            os.environ["CI_BASE_SHA"] = self._saved_base

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.root), *args], check=True, capture_output=True, text=True,
                              timeout=30).stdout.strip()

    def write(self, rel, text):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def test_main_under_its_baseline_passes(self):
        self.assertEqual(ratchet.check(self.root, base="origin/main"), [])

    def test_a_change_cannot_add_back_into_room_under_the_baseline(self):
        self.write("web/src/a.tsx", "text-[13px] text-[12px]")
        found = ratchet.check(self.root, base="origin/main")
        self.assertEqual(len(found), 1)
        self.assertIn("web/src/a.tsx: 2 hard-coded px font size(s), 1 at the merge base " + self.label, found[0])
        self.assertIn("(baseline 3)", found[0])
        self.assertEqual(len(ratchet.check(self.root, env={"CI_BASE_SHA": "origin/main"})), 1)
        self.git("commit", "-q", "-am", "one back")
        self.assertEqual(len(ratchet.check(self.root, base="origin/main")), 1)
        self.assertEqual(len(ratchet.check(self.root, base="main")), 1)

    def test_the_default_branch_alone_is_no_base(self):
        # Main verification's checkout: HEAD is the commit just merged, and origin/main still names its parent,
        # because the verifier pins HEAD by SHA. merge-pr.sh let the re-add in (changed_ratchets.py), so main must
        # not fail it either: without --base or CI_BASE_SHA only the baseline holds.
        self.write("web/src/a.tsx", "text-[13px] text-[12px]")
        self.git("commit", "-q", "-am", "one back")
        self.assertEqual(ratchet.check(self.root, env={}), [])
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(ratchet.main(["--root", str(self.root)]), 0)
        self.assertEqual(ratchet.check(self.root, env={"CI_BASE_SHA": "HEAD"}), [])  # hosted CI on main
        self.assertEqual(len(ratchet.check(self.root, env={"CI_BASE_SHA": "origin/main"})), 1)

    def test_a_hand_raised_entry_does_not_lift_the_limit(self):
        self.write("web/src/a.tsx", "text-[13px] text-[12px] text-[11px] text-[10px]")
        self.write(ratchet.BASELINE_REL, ratchet.render({"web/src/a.tsx": 4}))
        self.assertIn("1 at the merge base", ratchet.check(self.root, base="origin/main")[0])

    def test_sizes_that_move_to_a_new_file_with_its_entry_pass(self):
        self.write("web/src/a.tsx", "")
        self.write("web/src/part.tsx", "text-[13px]")
        self.assertEqual(len(ratchet.check(self.root, base="origin/main")), 1)  # no entry yet: it starts at zero
        self.assertEqual(ratchet.update(self.root), [])
        self.assertEqual(json.loads((self.root / ratchet.BASELINE_REL).read_text()), {"web/src/part.tsx": 1})
        self.assertEqual(ratchet.check(self.root, base="origin/main"), [])

    def test_without_a_base_only_the_baseline_holds(self):
        self.write("web/src/a.tsx", "text-[13px] text-[12px]")
        self.git("update-ref", "-d", "refs/remotes/origin/main")
        self.assertEqual(ratchet.check(self.root), [])
        self.write("web/src/a.tsx", "text-[13px] text-[12px] text-[11px] text-[10px]")
        self.assertIn("4 hard-coded px font size(s), baseline 3", ratchet.check(self.root)[0])

    def test_an_explicit_base_that_does_not_resolve_is_an_error(self):
        with self.assertRaises(ratchet.FontError) as caught:
            ratchet.check(self.root, base="no-such-ref")
        self.assertIn("does not resolve", str(caught.exception))
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            self.assertEqual(ratchet.main(["--root", str(self.root), "--base", "no-such-ref"]), 2)


class CheckoutTests(unittest.TestCase):
    def test_session_insights_and_extension_view_stay_within_baseline(self):
        targets = (
            "web/src/views/chat/SessionInsightsPopover.tsx:",
            "web/src/views/settings/ExtensionView.tsx:",
        )
        found = [violation for violation in ratchet.check(ROOT) if violation.startswith(targets)]
        self.assertEqual(found, [])

    def test_usage_bars_and_popover_use_named_font_tokens(self):
        targets = (
            "web/src/views/UsageBars.tsx",
            "web/src/views/UsagePopover.tsx",
        )
        counts = ratchet.scan(ROOT)
        self.assertEqual({path: counts.get(path, 0) for path in targets}, {path: 0 for path in targets})
        prefixes = tuple(path + ":" for path in targets)
        found = [violation for violation in ratchet.check(ROOT) if violation.startswith(prefixes)]
        self.assertEqual(found, [])

    def test_task_editor_inbox_links_and_context_chips_use_named_font_tokens(self):
        targets = (
            "web/src/views/assistant/TaskPromptEditor.tsx",
            "web/src/views/inbox/InboxTaskLinks.tsx",
            "web/src/views/link/ContextChipList.tsx",
        )
        counts = ratchet.scan(ROOT)
        self.assertEqual({path: counts.get(path, 0) for path in targets}, {path: 0 for path in targets})
        for path in targets:
            text = (ROOT / path).read_text()
            self.assertNotIn("text-[11px]", text)
            self.assertNotIn("text-[11.5px]", text)
            self.assertNotIn("text-[12px]", text)

    def test_this_checkout_is_at_its_baseline(self):
        self.assertEqual(ratchet.check(ROOT), [])


if __name__ == "__main__":
    unittest.main()
