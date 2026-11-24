"""The counts lane differ (scripts/test/counts_agreement.py, #11900): how it reads each lane's report against the
fixture and the lane's known differences, and that the real needs-you fixture's marks read as known differences."""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import counts_agreement  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]

CASES = {"version": 1, "cases": [
    {"name": "one_waits", "expected": {"rows": [{"id": "agent:m/a", "cards": ["c1"]}],
                                       "badge_rows": [{"id": "agent:m/a", "cards": ["c1"]}], "badge": 1}},
    {"name": "muted_is_a_row", "expected": {"rows": [{"id": "agent:m/b", "cards": []}], "badge_rows": [], "badge": 0}},
    {"name": "hidden_counts_nowhere", "expected": {"rows": [], "badge_rows": [], "badge": 0},
     "known_differences": {"android": {"decision": "D1", "why": "Android reads no hidden flag.", "source": "x",
                                       "replace": {"rows": [{"id": "agent:m/h", "cards": ["c2"]}],
                                                   "badge_rows": [{"id": "agent:m/h", "cards": ["c2"]}],
                                                   "badge": 1}}}},
]}
TABLE = {"replays": {"needs-you": {"lanes": ["ios", "android"], "surfaces": {
    "agents_tab": {"value": "badge", "keys": "badge_rows"},
    "agents_section": {"value": "rows", "keys": "rows"},
}}}}


def lane_rows(lane, cases=CASES, marked=True, table=TABLE):
    """What a lane that counts as the fixture says (with its marks applied when marked) reports."""
    out = []
    for case in cases["cases"]:
        mark = (case.get("known_differences") or {}).get(lane) if marked else None
        expected = {**case["expected"], **(mark["replace"] if mark else {})}
        for surface, read in table["replays"]["needs-you"]["surfaces"].items():
            value = expected[read["value"]]
            out.append({"lane": lane, "fixture": "needs-you", "case": case["name"], "surface": surface,
                        "value": len(value) if isinstance(value, list) else value, "keys": expected[read["keys"]]})
    return out


class DifferTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="counts-agreement-")
        cls.root = Path(cls.temp.name)
        (cls.root / "spec/fixtures/needs-you").mkdir(parents=True)
        (cls.root / "spec/fixtures/needs-you/cases.json").write_text(json.dumps(CASES))

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def compare(self, rows, require_all=False):
        return counts_agreement.compare(TABLE, rows, self.root, require_all)

    def verdicts(self, result, lane):
        return {(line["case"], line["surface"]): line["lanes"][lane]["verdict"]
                for line in result["fixtures"]["needs-you"]["rows"]}

    def test_lanes_that_count_as_the_fixture_agree_outside_their_marks(self):
        result = self.compare(lane_rows("ios") + lane_rows("android"))
        self.assertEqual([], result["problems"])
        self.assertEqual({"agrees"}, set(self.verdicts(result, "ios").values()))
        android = self.verdicts(result, "android")
        self.assertEqual("known", android[("hidden_counts_nowhere", "agents_tab")])
        self.assertEqual("known", android[("hidden_counts_nowhere", "agents_section")])
        self.assertEqual("agrees", android[("one_waits", "agents_tab")])
        text = "\n".join(counts_agreement.render(result))
        self.assertIn("android: 4 agree, 2 known difference(s) (hidden_counts_nowhere (D1))", text)
        self.assertIn("android and ios differ on 1 case(s): hidden_counts_nowhere", text)
        self.assertIn("OK every lane agrees", text)

    def test_a_value_neither_the_fixtures_nor_the_marks_is_unexplained(self):
        rows = lane_rows("ios")
        rows[0]["value"] = 2
        result = self.compare(rows)
        self.assertEqual("unexplained", self.verdicts(result, "ios")[("one_waits", "agents_tab")])
        self.assertEqual(["ios: one_waits agents_tab is 2, the fixture's 1"], result["problems"])
        # The same number over other rows is no agreement either.
        rows = lane_rows("ios")
        rows[0]["keys"] = [{"id": "agent:m/z", "cards": ["c1"]}]
        self.assertEqual(["ios: one_waits agents_tab is 1, the fixture's 1 (same number, other rows)"],
                         self.compare(rows)["problems"])

    def test_a_lane_that_follows_a_case_it_is_marked_for_has_a_stale_mark(self):
        result = self.compare(lane_rows("android", marked=False))
        self.assertEqual("stale mark", self.verdicts(result, "android")[("hidden_counts_nowhere", "agents_tab")])
        self.assertIn("remove the mark", result["problems"][0])

    def test_a_report_that_misses_or_adds_results_fails(self):
        rows = lane_rows("ios")
        self.assertEqual(["ios: no result for muted_is_a_row agents_section"],
                         self.compare([row for row in rows if not (row["case"] == "muted_is_a_row"
                                                                   and row["surface"] == "agents_section")])["problems"])
        extra = dict(rows[0], case="no_such_case")
        self.assertIn("ios: reports no_such_case, which needs-you does not have", self.compare(rows + [extra])["problems"])
        self.assertIn("ios: reports one_waits agents_tab twice", self.compare(rows + [rows[0]])["problems"])
        self.assertIn("ios: reports home_widget of needs-you, which the table does not",
                      self.compare(rows + [dict(rows[0], surface="home_widget")])["problems"])
        self.assertIn("web: does not replay needs-you in the table",
                      self.compare(rows + [dict(rows[0], lane="web")])["problems"])

    def test_a_lane_without_a_report_is_not_run_and_fails_only_when_required(self):
        result = self.compare(lane_rows("ios"))
        self.assertEqual([], result["problems"])
        self.assertEqual(["android"], result["fixtures"]["needs-you"]["not_run"])
        self.assertIn("needs-you: lanes ios; not run: android", counts_agreement.render(result)[0])
        self.assertEqual(["needs-you: no report from android"], self.compare(lane_rows("ios"), True)["problems"])

    def test_malformed_lines_are_named(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory, "ios.jsonl")
            path.write_text('{"lane": "ios"}\nnot json\n'
                            + json.dumps(dict(lane_rows("ios")[0], value="1")) + "\n\n")
            rows, problems = counts_agreement.read_reports([path])
        self.assertEqual([], rows)
        self.assertEqual(3, len(problems))
        self.assertIn(":1: a row has exactly", problems[0])
        self.assertIn(":2: not JSON", problems[1])
        self.assertIn(":3: value is a number", problems[2])


class CommandLineTests(unittest.TestCase):
    def run_differ(self, *args):
        return subprocess.run([sys.executable, str(ROOT / "scripts/test/counts_agreement.py"), *args],
                              capture_output=True, text=True, timeout=60)

    def write(self, directory, lane, rows):
        Path(directory, f"{lane}.jsonl").write_text("".join(json.dumps(row) + "\n" for row in rows))

    def real_lane(self, lane, cases):
        table = json.loads((ROOT / "spec/fixtures/counts/surfaces.json").read_text())
        return lane_rows(lane, cases, table=table)

    def test_the_real_fixtures_marks_read_as_known_differences(self):
        cases = {"cases": counts_agreement.fixture_cases(ROOT, "needs-you")}
        marked = sorted(case["name"] for case in cases["cases"] if "android" in (case.get("known_differences") or {}))
        self.assertTrue(marked, "the needs-you fixture marks a case for Android (step 3)")
        with tempfile.TemporaryDirectory() as directory:
            self.write(directory, "ios", self.real_lane("ios", cases))
            self.write(directory, "android", self.real_lane("android", cases))
            out = Path(directory, "agreement.json")
            run = self.run_differ(directory, "--require-all", "--json", str(out))
            self.assertEqual(0, run.returncode, run.stdout + run.stderr)
            result = json.loads(out.read_text())
        self.assertEqual([], result["problems"])
        known = sorted({line["case"] for line in result["fixtures"]["needs-you"]["rows"]
                        if line["lanes"]["android"]["verdict"] == "known"})
        self.assertEqual(marked, known)

        # The lanes differ where their marks leave them at different values: a decided change both still count by
        # today's rule (the same mark on each) leaves them agreeing with each other.
        def today(case, lane):
            mark = (case.get("known_differences") or {}).get(lane)
            return {**case["expected"], **(mark["replace"] if mark else {})}
        apart = sorted(case["name"] for case in cases["cases"] if today(case, "android") != today(case, "ios"))
        self.assertTrue(apart, "the needs-you fixture marks a case for Android alone (step 3)")
        self.assertIn(f"android and ios differ on {len(apart)} case(s): {', '.join(apart)}", run.stdout)

    def test_exit_codes(self):
        cases = {"cases": counts_agreement.fixture_cases(ROOT, "needs-you")}
        with tempfile.TemporaryDirectory() as directory:
            rows = self.real_lane("ios", cases)
            rows[0]["value"] += 5
            self.write(directory, "ios", rows)
            run = self.run_differ(directory)
            self.assertEqual(1, run.returncode, run.stdout)
            self.assertIn("UNEXPLAINED", run.stdout)
            self.assertIn("FAIL 1 unexplained", run.stdout)
            self.assertEqual(2, self.run_differ(str(Path(directory, "missing.jsonl"))).returncode)


if __name__ == "__main__":
    unittest.main()
