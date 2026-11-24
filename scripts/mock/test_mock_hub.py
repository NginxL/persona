#!/usr/bin/env python3
"""mock_hub.py takes no new MOCK_* switch (#12737): every MOCK_* name it reads from the environment is on
scripts/mock/mock_hub_switches.txt, and it reads none whose name cannot be seen. A new switch goes into a domain module
(scripts/mock/mock_domains.py, docs/operations/testing.md "Mock hub domains"). A listed name mock_hub.py no longer reads
is a notice, not a failure.

    python3 scripts/mock/test_mock_hub.py

Named after mock_hub.py so check-fast runs it whenever mock_hub.py changes (test_<name>.py for a changed <name>.py).
"""
from pathlib import Path
import sys
import textwrap
import unittest

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import mock_hub_switches  # noqa: E402

LISTED = "# a header\nMOCK_A\nMOCK_B\nMOCK_PENDING #4512\n"


def planted(code):
    """A small mock_hub.py: two listed switches, then `code`."""
    return textwrap.dedent('''\
        import os
        A = os.environ.get("MOCK_A", "")
        if "MOCK_B" in os.environ:
            pass
    ''') + textwrap.dedent(code)


class SwitchFreezeTests(unittest.TestCase):
    def test_mock_hub_reads_only_listed_switches(self):
        failures, notices = mock_hub_switches.check(mock_hub_switches.HUB.read_text(),
                                                    mock_hub_switches.LIST.read_text())
        for notice in notices:
            print("notice:", notice, file=sys.stderr)
        if failures:
            self.fail("\n" + "\n".join(failures))

    def test_a_planted_switch_fails_in_every_form_naming_the_domain_registry(self):
        for code in ('FOO = os.environ.get("MOCK_FOO")\n', 'FOO = os.environ["MOCK_FOO"]\n',
                     'FOO = os.getenv("MOCK_FOO", "")\n', 'FOO = "MOCK_FOO" in os.environ\n',
                     'FOO = "MOCK_FOO" not in os.environ\n', 'os.environ.setdefault("MOCK_FOO", "1")\n',
                     'ENV = dict(os.environ)\nFOO = ENV.get("MOCK_FOO")\n',
                     'import os as system\nFOO = system.environ.get("MOCK_FOO")\n',
                     'from os import environ, getenv\nFOO = environ.get("MOCK_FOO") or getenv("MOCK_FOO")\n'):
            with self.subTest(code=code):
                failures, _ = mock_hub_switches.check(planted(code), LISTED)
                self.assertEqual(len(failures), 1, failures)
                self.assertRegex(failures[0], r"^mock_hub\.py:\d+ reads MOCK_FOO, which is not on the list\.")
                self.assertIn("scripts/mock/mock_domains.py", failures[0])
                self.assertIn("A new value on an existing switch is a new switch too", failures[0])

    def test_a_planted_switch_is_found_at_its_line(self):
        failures, _ = mock_hub_switches.check(planted('\n\nFOO = os.environ.get("MOCK_FOO")\n'), LISTED)
        self.assertEqual([f.split(" reads ")[0] for f in failures], ["mock_hub.py:7"])

    def test_a_read_whose_name_cannot_be_seen_fails(self):
        for code in ('FOO = os.environ.get("MOCK_" + "FOO")\n', 'name = "X"\nFOO = os.environ[name]\n',
                     'FOO = os.getenv(f"MOCK_{1}")\n', 'FOO = any(k in os.environ for k in ("A", "B"))\n',
                     'get = os.environ.get\n', 'ENV = os.environ\n', 'FOO = [k for k in os.environ]\n',
                     'FOO = os.environ.keys()\n', 'from os import getenv\nread = getenv\n'):
            with self.subTest(code=code):
                failures, _ = mock_hub_switches.check(planted(code), LISTED)
                self.assertEqual(len(failures), 1, failures)
                self.assertIn("reads the environment without a literal name", failures[0])
                self.assertIn("scripts/mock/mock_domains.py", failures[0])

    def test_reads_that_are_no_switch_pass(self):
        code = ('PORT = int(os.environ.get("PORT", "18760"))\nCHILD = {k: v for k, v in os.environ.items()}\n'
                'COPY = os.environ.copy()\nos.environ.update({})\nSAME = dict(os.environ)\nMORE = {**os.environ}\n'
                'import subprocess\nsubprocess.run(["true"], env=os.environ)\n'
                'HELP = ["MOCK_HELP_ONLY=1  a help line names no switch it reads"]\n')
        self.assertEqual(mock_hub_switches.check(planted(code), LISTED)[0], [])

    def test_a_name_that_left_is_a_notice(self):
        failures, notices = mock_hub_switches.check(planted("").replace('"MOCK_B" in os.environ', "True"), LISTED)
        self.assertEqual(failures, [])
        self.assertEqual(notices[0], "mock_hub.py no longer reads MOCK_B: delete line 3 of "
                                     "scripts/mock/mock_hub_switches.txt")
        self.assertIn("MOCK_PENDING (#4512)", notices[1])

    def test_a_pending_name_passes_once_its_pr_merges(self):
        failures, notices = mock_hub_switches.check(planted('P = os.environ.get("MOCK_PENDING")\n'), LISTED)
        self.assertEqual((failures, notices), ([], []))

    def test_the_list_is_sorted_unique_and_well_formed(self):
        self.assertEqual(mock_hub_switches.read_list(mock_hub_switches.LIST.read_text())[1], [])
        for text, problem in (("MOCK_B\nMOCK_A\n", "line 2 (MOCK_A) is out of order"),
                              ("MOCK_A\nMOCK_A\n", "line 2 repeats MOCK_A (line 1)"),
                              ("MOCK_A\nmock_b\n", "line 2 is neither a MOCK_* name"),
                              ("MOCK_A # note\n", "line 1 is neither a MOCK_* name"),
                              ("MOCK_X #1\nMOCK_A\n", "line 2 (MOCK_A) has no PR after the pending entries"),
                              ("MOCK_Y #2\nMOCK_X #1\n", "line 2 (MOCK_X) is out of order")):
            with self.subTest(text=text):
                self.assertTrue(any(p.startswith(problem) for p in mock_hub_switches.read_list(text)[1]),
                                mock_hub_switches.read_list(text)[1])
        failures, _ = mock_hub_switches.check(planted(""), "MOCK_B\nMOCK_A\n")
        self.assertTrue(failures[0].startswith("mock_hub_switches.txt: line 2 (MOCK_A) is out of order"), failures)

    def rewrite(self, today, found=None, still_open=(4512,), text=LISTED):
        return mock_hub_switches.rewrite(text, set(today), found or {}, set(still_open), "2026-11-22", 14)

    def test_the_land_day_rewrite_has_both_parts_with_a_fresh_pending_header(self):
        new = self.rewrite({"MOCK_A", "MOCK_B"}, {"MOCK_D": [9, 7], "MOCK_C": [8]})
        header = mock_hub_switches.PENDING_HEADER.format(day="2026-11-22", idle=14)
        self.assertEqual(new, "# a header\nMOCK_A\nMOCK_B\n\n%s\nMOCK_C #8\nMOCK_D #7 #9\nMOCK_PENDING #4512\n"
                         % header)
        self.assertEqual(mock_hub_switches.read_list(new)[1], [])
        self.assertEqual(mock_hub_switches.rewrite(new, {"MOCK_A", "MOCK_B"}, {"MOCK_C": [8], "MOCK_D": [7, 9]},
                                                   {4512, 7, 8, 9}, "2026-11-22", 14), new)

    def test_a_pending_name_whose_pr_merged_before_the_land_day_joins_the_names_main_reads(self):
        # Its PR merged, so it is neither open nor found any more, and main reads it: it has to stay on the list.
        merged = planted('P = os.environ.get("MOCK_PENDING")\n')
        new = self.rewrite(mock_hub_switches.reads(merged).names, still_open=())
        self.assertEqual(new, "# a header\nMOCK_A\nMOCK_B\nMOCK_PENDING\n")
        self.assertEqual(mock_hub_switches.check(merged, new), ([], []))

    def test_the_land_day_rewrite_follows_what_main_gained_and_lost(self):
        gained = planted('N = os.environ.get("MOCK_NEW")\n').replace('"MOCK_B" in os.environ', "True")
        new = self.rewrite(mock_hub_switches.reads(gained).names)
        self.assertEqual([(e.name, e.prs) for e in mock_hub_switches.read_list(new)[0]],
                         [("MOCK_A", []), ("MOCK_NEW", []), ("MOCK_PENDING", ["#4512"])])
        self.assertEqual(mock_hub_switches.check(gained, new)[0], [])

    def test_a_pending_name_stays_while_one_of_its_prs_is_open(self):
        text = LISTED.replace("MOCK_PENDING #4512", "MOCK_GONE #11\nMOCK_PENDING #4512 #4600")
        new = self.rewrite({"MOCK_A", "MOCK_B"}, still_open=(4600,), text=text)
        self.assertEqual(new.splitlines()[-1], "MOCK_PENDING #4600")
        self.assertNotIn("MOCK_GONE", new)  # its PRs all closed unmerged
        self.assertEqual(mock_hub_switches.changes(text, new),
                         ["names mock_hub.py reads: 2", "pending: 1 -MOCK_GONE"])


if __name__ == "__main__":
    unittest.main()
