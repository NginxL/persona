"""Tests for scripts/test/name_freezes.py: the name freezes' list format and rewrite, the open PRs it counts, and the
server's central lists it reads through server/internal/archguard/central_lists_test.go."""
from datetime import date, datetime, timedelta, timezone
import io
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import name_freezes  # noqa: E402

ROOT = name_freezes.ROOT
LISTS = ROOT / "server/internal/archguard/testdata/central_lists"
NOW = datetime(2026, 11, 22, tzinfo=timezone.utc)
LISTED = '# a header\n"GET /api/v1/a"\n"GET /api/v1/b"\n\n# pending\n"PUT /api/v1/p" #4512\n'


def node(number, paths, draft=False, days_old=0, total=None):
    """One of the GraphQL query's PR nodes."""
    committed = (NOW - timedelta(days=days_old)).isoformat().replace("+00:00", "Z")
    return {"number": number, "isDraft": draft, "headRefOid": "head%d" % number,
            "commits": {"nodes": [{"commit": {"committedDate": committed}}]},
            "files": {"totalCount": len(paths) if total is None else total, "nodes": [{"path": p} for p in paths]}}


class FormatTests(unittest.TestCase):
    format = name_freezes.CENTRAL

    def test_a_name_with_spaces_and_a_pending_one(self):
        entries, problems = self.format.read(LISTED)
        self.assertEqual(problems, [])
        self.assertEqual([(e.line, e.name, e.prs) for e in entries],
                         [(2, '"GET /api/v1/a"', []), (3, '"GET /api/v1/b"', []), (6, '"PUT /api/v1/p"', ["#4512"])])
        self.assertEqual([(e.name, e.prs) for e in self.format.read('"GET " + P #1 #22\n')[0]],
                         [('"GET " + P', ["#1", "#22"])])

    def test_the_parts_are_sorted_unique_and_pending_last(self):
        for text, problem in (("B\nA\n", "line 2 (A) is out of order"),
                              ("A\nA\n", "line 2 repeats A (line 1)"),
                              ("X #1\nA\n", "line 2 (A) has no PR after the pending entries"),
                              ("A\nY #2\nX #1\n", "line 3 (X) is out of order")):
            with self.subTest(text=text):
                problems = self.format.read(text)[1]
                self.assertTrue(any(p.startswith(problem) for p in problems), problems)

    def test_a_name_check(self):
        strict = name_freezes.Format(what="a MOCK_* name", example="MOCK_X #123", valid=str.isupper)
        self.assertEqual(strict.read("MOCK_A # note\n")[1],
                         ["line 1 is neither a MOCK_* name nor a pending one with its PRs (MOCK_X #123): "
                          "'MOCK_A # note'"])

    def rewrite(self, today, found=None, still_open=(4512,), text=LISTED):
        return self.format.rewrite(text, set(today), found or {}, set(still_open), "2026-11-22", 14)

    def test_the_land_day_rewrite_has_both_parts_with_a_fresh_pending_header(self):
        today = {'"GET /api/v1/a"', '"GET /api/v1/b"'}
        new = self.rewrite(today, {'"POST /api/v1/c"': [9, 7]})
        header = name_freezes.PENDING_HEADER.format(day="2026-11-22", idle=14)
        self.assertEqual(new, '# a header\n"GET /api/v1/a"\n"GET /api/v1/b"\n\n%s\n"POST /api/v1/c" #7 #9\n'
                         '"PUT /api/v1/p" #4512\n' % header)
        self.assertEqual(self.format.read(new)[1], [])
        self.assertEqual(self.rewrite(today, {'"POST /api/v1/c"': [7, 9]}, still_open=(4512, 7, 9), text=new), new)
        self.assertEqual(self.format.changes(LISTED, new), ['names: 2', 'pending: 2 +"POST /api/v1/c"'])

    def test_a_land_day_rerun_drops_closed_prs_and_keeps_merged_names(self):
        # Nothing found added: #4512 merged, so the base has its route; #11's closed unmerged.
        text = LISTED.replace('"PUT /api/v1/p" #4512', '"PUT /api/v1/gone" #11\n"PUT /api/v1/p" #4512')
        new = self.rewrite({'"GET /api/v1/a"', '"GET /api/v1/b"', '"PUT /api/v1/p"'}, still_open=(), text=text)
        self.assertEqual(new, '# a header\n"GET /api/v1/a"\n"GET /api/v1/b"\n"PUT /api/v1/p"\n')
        self.assertEqual(self.format.changes(text, new),
                         ['names: 3 +"PUT /api/v1/p"', 'pending: 0 -"PUT /api/v1/gone" -"PUT /api/v1/p"'])

    def test_a_prune_of_a_landed_list_adds_no_name(self):
        # On the base: "A", "C" (#5 merged), "X" (on no line: a red main, or a branch as the base); "B" has gone.
        text = '# a header\n"A"\n"B"\n\n# pending, 2026-11-01\n"C" #5\n"D" #8\n"E" #9\n'
        new, unlisted = self.format.prune(text, {'"A"', '"C"', '"X"'}, {8})
        self.assertEqual(new, '# a header\n"A"\n"C"\n\n# pending, 2026-11-01\n"D" #8\n')
        self.assertEqual(unlisted, ['"X"'])
        self.assertEqual(self.format.changes(text, new), ['names: 2 +"C" -"B"', 'pending: 1 -"C" -"E"'])
        self.assertEqual(self.format.prune(new, {'"A"', '"C"', '"D"'}, set()), ('# a header\n"A"\n"C"\n"D"\n', []))

    def test_the_committed_lists_read_cleanly(self):
        paths = sorted(LISTS.glob("*.txt"))
        self.assertEqual([p.name for p in paths], ["assistantwire_routes.txt", "extwire_route_scopes.txt",
                                                   "node_handle_method.txt", "prompts_assistant.txt",
                                                   "proto_features.txt"])
        for path in paths:
            with self.subTest(path=path.name):
                text = path.read_text()
                entries, problems = self.format.read(text)
                self.assertEqual(problems, [])
                self.assertTrue(text.startswith("# "), "a list starts with what it is")
                self.assertGreater(len(entries), 50)


class OpenPRTests(unittest.TestCase):
    PATHS = ["server/internal/assistantwire/bus.go", "server/internal/prompts/assistant.go"]

    def test_a_pr_touches_the_paths_its_files_name_or_all_when_they_are_cut_off(self):
        self.assertEqual(name_freezes.read_pr(node(1, ["web/a.ts", self.PATHS[0]]), self.PATHS).touches,
                         {self.PATHS[0]})
        self.assertEqual(name_freezes.read_pr(node(2, ["web/a.ts"]), self.PATHS).touches, set())
        self.assertEqual(name_freezes.read_pr(node(3, ["web/a.ts"], total=101), self.PATHS).touches, set(self.PATHS))

    def test_idle_drafts_are_left_out(self):
        prs = [name_freezes.read_pr(n, self.PATHS) for n in (
            node(1, self.PATHS), node(2, self.PATHS, draft=True, days_old=15),
            node(3, self.PATHS, draft=True, days_old=13), node(4, self.PATHS, days_old=40), node(5, ["web/a.ts"]))]
        log = io.StringIO()
        counted = name_freezes.counted(prs, self.PATHS[1], 14, now=NOW, log=log)
        self.assertEqual([pr.number for pr in counted], [1, 3, 4])
        self.assertEqual(log.getvalue(), "#2: left out, a draft whose head commit is from 2026-11-07\n")


class CentralPendingTests(unittest.TestCase):
    def test_names_a_pr_adds_less_its_merge_base_and_the_base(self):
        lists = [name_freezes.CentralList("routes", "server/bus.go", "server/routes.txt", "route"),
                 name_freezes.CentralList("prompts", "server/prompts.go", "server/prompts.txt", "declaration")]
        files = {("base", "server/bus.go"): "A\nB\n", ("base", "server/prompts.go"): "P\n",
                 ("head1", "server/bus.go"): "A\nB\nC\nM\n", ("mb1", "server/bus.go"): "A\nM\n",
                 ("head2", "server/bus.go"): "A\nC\n", ("mb2", "server/bus.go"): "A\n",
                 ("head2", "server/prompts.go"): "P\nQ\n"}  # mb2 has no prompts.go
        paths = [l.source for l in lists]
        prs = [name_freezes.read_pr(n, paths) for n in (
            node(1, ["server/bus.go"]), node(2, paths), node(3, paths, draft=True, days_old=30))]

        def names(work, sources=None):
            return lists, {key: set(text.splitlines()) for key, text in (sources or {}).items()}

        with mock.patch.object(name_freezes, "show", lambda rev, path: files.get((rev, path))), \
                mock.patch.object(name_freezes, "merge_base", lambda head, base: "mb" + head[4:]), \
                mock.patch.object(name_freezes, "fetch", lambda prs: None), \
                mock.patch.object(name_freezes, "central_names", names):
            today, found = name_freezes.central_pending("base", lists, prs, 14, "unused", log=io.StringIO())
        self.assertEqual(today, {"routes": {"A", "B"}, "prompts": {"P"}})
        self.assertEqual(found, {"routes": {"C": [1, 2]}, "prompts": {"Q": [2]}})


class MainTests(unittest.TestCase):
    """main()'s pending --write: a list the base has already is only pruned, and one it does not have yet takes every
    name the base has and the names the open PRs add."""
    TEXT = '# a header\n"A"\n"B"\n\n# pending, 2026-11-01\n"C" #5\n"D" #8\n"E" #9\n'
    BUS = "server/bus.go"

    def write(self, landed):
        """(exit status, the list's file after, standard error) of pending --write against a made-up base, on which
        "X" is a route the list lacks and "B" has gone, with #7 open and adding "F", #8 open and #5 and #9 closed."""
        lists = [name_freezes.CentralList("routes", self.BUS, "routes.txt", "route")]
        files = {("base", self.BUS): '"A"\n"C"\n"X"\n', ("head7", self.BUS): '"A"\n"F"\n', ("mb7", self.BUS): '"A"\n'}
        prs = [name_freezes.read_pr(node(7, [self.BUS]), [self.BUS]), name_freezes.read_pr(node(8, ["web/a.ts"]), [])]
        central_pending = name_freezes.central_pending

        def names(work, sources=None):
            return lists, {key: set(text.splitlines()) for key, text in (sources or {}).items()}

        with tempfile.TemporaryDirectory() as root:
            path = Path(root, "routes.txt")
            path.write_text(self.TEXT)
            err = io.StringIO()
            with mock.patch.multiple(name_freezes, ROOT=Path(root), run=lambda args, timeout, **kw: "base\n",
                                     show=lambda rev, p: files.get((rev, p)), merge_base=lambda head, base: "mb7",
                                     fetch=lambda prs: None, central_names=names, open_prs=lambda paths: prs,
                                     on_base=lambda base, p: landed,
                                     central_pending=lambda *args: central_pending(*args, log=io.StringIO())), \
                    mock.patch("sys.stdout", io.StringIO()), mock.patch("sys.stderr", err):
                code = name_freezes.main(["pending", "--write"])
            return code, path.read_text(), err.getvalue()

    def test_a_landed_list_is_pruned_and_a_name_it_lacks_fails(self):
        code, text, err = self.write(landed=True)
        self.assertEqual(text, '# a header\n"A"\n"C"\n\n# pending, 2026-11-01\n"D" #8\n')
        self.assertEqual(code, 1)
        self.assertIn('routes.txt: origin/main has "X", which the list does not: --write adds no name', err)
        self.assertIn('routes.txt, names: 2 +"C" -"B" (on origin/main already: pruned, nothing added)', err)

    def test_the_land_day_takes_every_name_the_base_has_and_the_pending_ones(self):
        code, text, err = self.write(landed=False)
        header = name_freezes.PENDING_HEADER.format(day=date.today().isoformat(), idle=14)
        self.assertEqual(text, '# a header\n"A"\n"C"\n"X"\n\n%s\n"D" #8\n"F" #7\n' % header)
        self.assertEqual(code, 0, err)


@unittest.skipUnless(shutil.which("go"), "needs go")
class GoNamesTests(unittest.TestCase):
    """The names come from the archguard test itself, so they match what the freeze compares."""

    def test_the_freeze_reads_the_names_of_the_given_files(self):
        bus = (ROOT / "server/internal/assistantwire/bus.go").read_text()
        planted = bus.replace("var Routes = []string{\n", 'var Routes = []string{\n\t"DELETE /api/v1/planted",\n', 1)
        node = (ROOT / "server/internal/node/node.go").read_text()
        dispatch = "\tswitch method {\n"
        self.assertIn(dispatch, node)
        planted_node = node.replace(dispatch, dispatch + '\tcase "machine.planted", "session.planted":\n\t\treturn nil, nil\n', 1)
        with tempfile.TemporaryDirectory() as work:
            lists, names = name_freezes.central_names(work, {
                ("assistantwire_routes", "now"): bus, ("assistantwire_routes", "planted"): planted,
                ("assistantwire_routes", "broken"): "package x\nvar Routes = []string{\n",
                ("node_handle_method", "now"): node, ("node_handle_method", "planted"): planted_node})
        self.assertEqual([(l.id, l.source, l.file) for l in lists], [
            ("assistantwire_routes", "server/internal/assistantwire/bus.go",
             "server/internal/archguard/testdata/central_lists/assistantwire_routes.txt"),
            ("prompts_assistant", "server/internal/prompts/assistant.go",
             "server/internal/archguard/testdata/central_lists/prompts_assistant.txt"),
            ("extwire_route_scopes", "server/internal/extwire/scopes.go",
             "server/internal/archguard/testdata/central_lists/extwire_route_scopes.txt"),
            ("node_handle_method", "server/internal/node/node.go",
             "server/internal/archguard/testdata/central_lists/node_handle_method.txt"),
            ("proto_features", "server/internal/proto/proto.go",
             "server/internal/archguard/testdata/central_lists/proto_features.txt")])
        # A session-bound label stays in the switch, so only the other one is a name.
        self.assertEqual(names[("node_handle_method", "planted")] - names[("node_handle_method", "now")],
                         {'"machine.planted"'})
        listed = {e.name for e in name_freezes.CENTRAL.read((ROOT / lists[0].file).read_text())[0]}
        now = names[("assistantwire_routes", "now")]
        self.assertGreater(len(now), 100)
        self.assertLessEqual(now, listed)
        self.assertEqual(names[("assistantwire_routes", "planted")] - now, {'"DELETE /api/v1/planted"'})
        self.assertIsNone(names[("assistantwire_routes", "broken")])


if __name__ == "__main__":
    unittest.main()
