"""lower_baselines.py lowers every portable count ratchet's baseline and nothing else (#12785).

Fake ratchets (a Python one-liner as the update command) cover the outcomes; one case runs the real sleep and web font
ratchets in a throwaway tree, and one checks the list against this checkout.
"""
import contextlib
import io
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import lower_baselines  # noqa: E402
from lower_baselines import Ratchet, only_lowers  # noqa: E402

ROOT = HERE.parents[1]
PY = sys.executable


def writes(path, value):
    """An update command that writes value as JSON to path (relative to the root)."""
    return [PY, "-c", "import json, sys; open(sys.argv[1], 'w').write(json.dumps(json.loads(sys.argv[2])))",
            path, json.dumps(value)]


class OnlyLowersTests(unittest.TestCase):
    def test_lower_numbers_and_dropped_entries_are_lowerings(self):
        self.assertEqual(only_lowers({"a": 3, "b": 1}, {"a": 2}), [])
        self.assertEqual(only_lowers({"version": 2, "files": {"x.kt": {"dp": 4, "font": 1}}},
                                     {"version": 2, "files": {"x.kt": {"dp": 1}}}), [])
        self.assertEqual(only_lowers({"agents": {"count": 2, "files": ["a.md", "b.md"]}},
                                     {"agents": {"count": 1, "files": ["a.md"]}}), [])

    def test_a_raise_an_added_entry_or_name_and_a_changed_value_are_not(self):
        self.assertEqual(only_lowers({"a": 1}, {"a": 2}), ["a: 1 -> 2"])
        self.assertEqual(only_lowers({"a": 1}, {"a": 1, "b": 1}), ["b: added"])
        self.assertEqual(only_lowers({"d": {"files": ["a.md"]}}, {"d": {"files": ["a.md", "b.md"]}}),
                         ['d/files: adds "b.md"'])
        self.assertEqual(only_lowers({"version": 2}, {"version": "2"}), ['version: 2 -> "2"'])
        self.assertEqual(only_lowers({"a": True}, {"a": False}), ["a: true -> false"])


class LowerTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="lower-baselines-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.write("base/counts.json", {"a.go": 3, "b.go": 1})

    def write(self, path, value):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(json.dumps(value))

    def read(self, path):
        return json.loads((self.root / path).read_text())

    def lower(self, command, dry_run=False, files=("base/*.json",), **kwargs):
        return lower_baselines.lower(self.root, Ratchet("demo", "demo counts", list(files), command, **kwargs), dry_run)

    def test_a_lowering_is_written(self):
        outcome, detail = self.lower(writes("base/counts.json", {"a.go": 2}))
        self.assertEqual((outcome, detail), ("lowered", "lowered base/counts.json"))
        self.assertEqual(self.read("base/counts.json"), {"a.go": 2})

    def test_counts_already_at_the_baseline_are_left_as_they_are(self):
        self.assertEqual(self.lower(writes("base/counts.json", {"a.go": 3, "b.go": 1})),
                         ("unchanged", "already at the counts"))

    def test_a_raise_is_put_back(self):
        outcome, detail = self.lower(writes("base/counts.json", {"a.go": 4, "b.go": 0}))
        self.assertEqual(outcome, "left alone")
        self.assertIn("base/counts.json: a.go: 3 -> 4", detail)
        self.assertEqual(self.read("base/counts.json"), {"a.go": 3, "b.go": 1})

    def test_an_added_entry_or_file_is_put_back(self):
        outcome, detail = self.lower(writes("base/counts.json", {"a.go": 1, "new.go": 1}))
        self.assertEqual(outcome, "left alone")
        self.assertIn("new.go: added", detail)
        self.assertEqual(self.read("base/counts.json"), {"a.go": 3, "b.go": 1})
        outcome, detail = self.lower(writes("base/more.json", {}))
        self.assertEqual(outcome, "left alone")
        self.assertIn("base/more.json: a new baseline file", detail)
        self.assertFalse((self.root / "base/more.json").exists())

    def test_a_removed_file_is_a_lowering_to_zero(self):
        self.write("base/other.json", {"c.go": 1})
        remove = [PY, "-c", "import os; os.remove('base/other.json')"]
        self.assertEqual(self.lower(remove), ("lowered", "lowered base/other.json"))
        self.assertFalse((self.root / "base/other.json").exists())

    def test_a_failed_update_is_put_back_and_its_output_shown(self):
        fail = [PY, "-c", "import sys; open('base/counts.json', 'w').write('{}'); print('a count rose'); sys.exit(1)"]
        outcome, detail = self.lower(fail)
        self.assertEqual(outcome, "left alone")
        self.assertIn("failed (exit 1)", detail)
        self.assertIn("a count rose", detail)
        self.assertEqual(self.read("base/counts.json"), {"a.go": 3, "b.go": 1})

    def test_a_timed_out_update_is_put_back(self):
        slow = [PY, "-c", "import time; open('base/counts.json', 'w').write('{}'); time.sleep(30)"]
        outcome, detail = self.lower(slow, timeout=2)
        self.assertEqual(outcome, "left alone")
        self.assertIn("timed out after 2 s", detail)
        self.assertEqual(self.read("base/counts.json"), {"a.go": 3, "b.go": 1})

    def test_missing_tools_skip_the_ratchet(self):
        self.assertEqual(self.lower(writes("base/counts.json", {}), needs=("no-such-tool-here",)),
                         ("skipped", "no-such-tool-here is not on PATH"))
        self.assertEqual(self.lower(writes("base/counts.json", {}), needs=("web/node_modules",)),
                         ("skipped", "web/node_modules is missing"))
        self.assertEqual(self.read("base/counts.json"), {"a.go": 3, "b.go": 1})

    def test_no_baseline_file_is_left_alone(self):
        outcome, detail = self.lower(writes("base/counts.json", {}), files=("none/*.json",))
        self.assertEqual(outcome, "left alone")
        self.assertIn("no baseline file", detail)

    def test_a_dry_run_writes_nothing(self):
        self.assertEqual(self.lower(writes("base/counts.json", {"a.go": 1}), dry_run=True),
                         ("lowered", "would lower base/counts.json"))
        self.assertEqual(self.read("base/counts.json"), {"a.go": 3, "b.go": 1})


class RealRatchetTests(unittest.TestCase):
    """The sleep and web font ratchets' own --update, in a tree outside Git (no base)."""

    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="lower-baselines-real-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        for name in ("sleep_ratchet.py", "web_font_ratchet.py", "ratchet_base.py", "lower_baselines.py"):
            (self.root / "scripts/ci").mkdir(parents=True, exist_ok=True)
            shutil.copy2(HERE / name, self.root / "scripts/ci" / name)
        self.put("server/internal/demo/demo_test.go", "package demo\n\nfunc TestWait(t *testing.T) {\n"
                 "\ttime.Sleep(time.Second)\n}\n")
        self.put("web/src/Demo.tsx", '<p className="text-[13px]">a</p>\n')
        self.put("scripts/ci/sleep_baseline.json", json.dumps({"server/internal/demo/demo_test.go": 3,
                                                               "server/internal/demo/gone_test.go": 1}))
        self.put("scripts/ci/web_font_baseline.json", json.dumps({"web/src/Demo.tsx": 2}))

    def put(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def run_main(self, *args):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = lower_baselines.main(["--root", str(self.root), "--only", "sleep,web-font", *args])
        return code, out.getvalue()

    def test_both_baselines_come_down_to_the_counts(self):
        code, out = self.run_main("--dry-run")
        self.assertEqual(code, 0, out)
        self.assertIn("would lower 2, 0 already at the counts, 0 left alone, 0 skipped", out)
        self.assertIn('"server/internal/demo/gone_test.go"', (self.root / "scripts/ci/sleep_baseline.json").read_text())
        code, out = self.run_main()
        self.assertEqual(code, 0, out)
        self.assertEqual(json.loads((self.root / "scripts/ci/sleep_baseline.json").read_text()),
                         {"server/internal/demo/demo_test.go": 1})
        self.assertEqual(json.loads((self.root / "scripts/ci/web_font_baseline.json").read_text()),
                         {"web/src/Demo.tsx": 1})
        code, out = self.run_main()
        self.assertEqual(code, 0, out)
        self.assertIn("lowered 0, 2 already at the counts", out)

    def test_a_count_above_its_baseline_leaves_that_ratchet_alone(self):
        self.put("web/src/New.tsx", '<p className="text-[13px] text-[12px]">a</p>\n')  # more than Demo.tsx lost
        code, out = self.run_main()
        self.assertEqual(code, 1, out)
        self.assertIn("web-font: left alone", out)
        self.assertIn("lowered 1, 0 already at the counts, 1 left alone", out)
        self.assertEqual(json.loads((self.root / "scripts/ci/web_font_baseline.json").read_text()),
                         {"web/src/Demo.tsx": 2})

    def test_unknown_names_are_refused(self):
        err = io.StringIO()
        with contextlib.redirect_stderr(err), self.assertRaises(SystemExit) as caught:
            lower_baselines.main(["--only", "sleep,nope"])
        self.assertEqual(caught.exception.code, 2)
        self.assertIn("unknown ratchet(s): nope", err.getvalue())


class CheckoutTests(unittest.TestCase):
    def test_every_ratchet_has_its_script_and_baseline_here(self):
        names = [ratchet.name for ratchet in lower_baselines.RATCHETS]
        self.assertEqual(len(names), len(set(names)))
        for ratchet in lower_baselines.RATCHETS:
            with self.subTest(ratchet.name):
                self.assertTrue(lower_baselines.baseline_files(ROOT, ratchet), ratchet.files)
                scripts = [part for part in ratchet.command if part.endswith(".py")]
                for script in scripts:
                    self.assertTrue((ROOT / ratchet.cwd / script).is_file(), script)
                self.assertTrue((ROOT / ratchet.cwd).is_dir())
                self.assertFalse(any(pattern.startswith("apple/") for pattern in ratchet.files))

    def test_list_names_every_ratchet(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(lower_baselines.main(["--list"]), 0)
        for ratchet in lower_baselines.RATCHETS:
            self.assertIn(ratchet.name + ": ", out.getvalue())


if __name__ == "__main__":
    unittest.main()
