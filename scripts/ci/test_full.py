"""scripts/ci/full.sh: which lanes run, how many at once, and the exit code (docs/operations/testing.md, "Affected
checks"). The lanes are stubs in a scratch copy of the tree."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
LANES = ["server-build", "server-managers", "server-node", "server-other", "web", "scripts"]
# Each stub notes its lane, start and end in ran.log; server.sh node fails with 3.
STUB = """#!/bin/bash
lane="$(basename "$0" .sh)${1:+-$1}"
echo "start $lane" >> "$RAN"
sleep "${STUB_SECONDS:-0}"
echo "end $lane" >> "$RAN"
[ "$lane" = server-node ] && exit 3
exit 0
"""


class FullTests(unittest.TestCase):
    def setUp(self):
        self.tree = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tree)
        (self.tree / "scripts/ci").mkdir(parents=True)
        shutil.copy(ROOT / "scripts/ci/full.sh", self.tree / "scripts/ci/full.sh")
        for name in ("server", "web", "scripts"):
            (self.tree / "scripts/ci" / (name + ".sh")).write_text(STUB)
        self.ran = self.tree / "ran.log"

    def full(self, *lanes, **env):
        clean = {k: v for k, v in os.environ.items() if not k.startswith("PERSONA_CI_FULL_")}
        result = subprocess.run(["bash", "scripts/ci/full.sh", *lanes], cwd=self.tree, capture_output=True, text=True,
                                env=dict(clean, RAN=str(self.ran), **env), timeout=60)
        events = self.ran.read_text().split("\n")[:-1] if self.ran.exists() else []
        return result, events

    def started(self, events):
        return [e.split(" ", 1)[1] for e in events if e.startswith("start ")]

    def test_every_lane_runs_and_the_first_failure_in_lane_order_is_the_exit_code(self):
        result, events = self.full(PERSONA_CI_FULL_JOBS="6")
        self.assertEqual(sorted(self.started(events)), sorted(LANES))
        self.assertEqual(result.returncode, 3)
        self.assertIn("FAIL lane server-node (", result.stdout)
        self.assertIn("PASS lane web (", result.stdout)

    def test_one_job_runs_the_lanes_one_after_another_in_order(self):
        _, events = self.full(PERSONA_CI_FULL_JOBS="1")
        expected = []
        for lane in LANES:
            expected += ["start " + lane, "end " + lane]
        self.assertEqual(events, expected)

    def test_two_jobs_never_run_more_than_two_lanes_at_once(self):
        _, events = self.full(PERSONA_CI_FULL_JOBS="2", STUB_SECONDS="0.2")
        running = peak = 0
        for e in events:
            running += 1 if e.startswith("start ") else -1
            peak = max(peak, running)
        self.assertEqual(peak, 2)
        self.assertEqual(sorted(self.started(events)), sorted(LANES))

    def test_include_runs_only_the_named_lanes_in_lane_order(self):
        result, events = self.full(PERSONA_CI_FULL_JOBS="1", PERSONA_CI_FULL_INCLUDE="scripts,server-build")
        self.assertEqual(self.started(events), ["server-build", "scripts"])
        self.assertEqual(result.returncode, 0)
        self.assertIn("server-node left out (not in PERSONA_CI_FULL_INCLUDE)", result.stdout)

    def test_exclude_leaves_lanes_out_and_combines_with_include(self):
        _, events = self.full(PERSONA_CI_FULL_JOBS="1", PERSONA_CI_FULL_EXCLUDE="web")
        self.assertEqual(self.started(events), [lane for lane in LANES if lane != "web"])
        self.ran.unlink()
        _, events = self.full(PERSONA_CI_FULL_JOBS="1", PERSONA_CI_FULL_INCLUDE="web server-node scripts",
                              PERSONA_CI_FULL_EXCLUDE="server-node")
        self.assertEqual(self.started(events), ["web", "scripts"])

    def test_nothing_left_runs_nothing_and_passes(self):
        result, events = self.full(PERSONA_CI_FULL_INCLUDE="web", PERSONA_CI_FULL_EXCLUDE="web")
        self.assertEqual((events, result.returncode), ([], 0))
        self.assertIn("no lanes left to run", result.stdout)

    def test_unknown_names_and_bad_job_counts_are_refused(self):
        for env in ({"PERSONA_CI_FULL_INCLUDE": "webb"}, {"PERSONA_CI_FULL_EXCLUDE": "server"},
                    {"PERSONA_CI_FULL_JOBS": "two"}):
            result, events = self.full(**env)
            self.assertEqual((result.returncode, events), (2, []), env)
        result, events = self.full("server-nod")
        self.assertEqual((result.returncode, events), (2, []))


if __name__ == "__main__":
    unittest.main()
