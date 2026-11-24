"""scripts/ci/affected.py and server-shards.py's affected run: which checks a change needs (docs/operations/testing.md,
"Affected checks")."""
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), ROOT / "scripts/ci" / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


affected = load("affected")
shards = load("server-shards")
M = affected.SERVER_MODULE + "/"

# A small module: wire <- store <- assistant <- hub <- cmd/personad; lone imports nothing and nothing imports it;
# hubtest's tests import hub (a package whose tests reach a changed package is tested too).
GRAPH = {
    M + "internal/wire": ("server/internal/wire", set(), set()),
    M + "internal/store": ("server/internal/store", {M + "internal/wire"}, set()),
    M + "internal/assistant": ("server/internal/assistant", {M + "internal/store"}, set()),
    M + "internal/hub": ("server/internal/hub", {M + "internal/assistant"}, set()),
    M + "cmd/personad": ("server/cmd/personad", {M + "internal/hub"}, set()),
    M + "internal/lone": ("server/internal/lone", set(), set()),
    M + "internal/hubtest": ("server/internal/hubtest", set(), {M + "internal/hub"}),
    M + "internal/hub/route": ("server/internal/hub/route", set(), set()),
}


def fast(groups=(), guards=False, build=False, error="", lines=("queued: privacy scan",)):
    return list(lines), list(groups), guards, build, error


class FullTests(unittest.TestCase):
    def test_what_selects_or_runs_the_checks_runs_everything(self):
        for path in ("scripts/ci/affected.py", "scripts/ci/server.sh", ".github/workflows/ci.yml", "server/go.mod",
                     "sdk/go/go.sum", "server/build-tags.txt", "scripts/test/check-fast.sh",
                     "scripts/test/go_partitions.py", "scripts/test/server_test_env.py"):
            plan = affected.make_plan([path], graph=GRAPH, fast=fast())
            self.assertTrue(plan["full"], path)
            self.assertIn(path, plan["reason"])
            self.assertEqual(affected.gate_groups(plan), ["all"])

    def test_a_change_check_fast_cannot_plan_or_only_builds_runs_everything(self):
        plan = affected.make_plan(["server/internal/wire/a.go"], graph=GRAPH, fast=fast(error="exit 2"))
        self.assertTrue(plan["full"])
        plan = affected.make_plan(["server/internal/wire/a.go"], graph=GRAPH, fast=fast(build=True))
        self.assertTrue(plan["full"])
        self.assertIn("go build", plan["reason"])

    def test_no_change_needs_nothing(self):
        plan = affected.make_plan([], graph=GRAPH, fast=fast())
        self.assertFalse(plan["full"])
        self.assertEqual(affected.gate_groups(plan), [])


class GoPackageTests(unittest.TestCase):
    def test_a_changed_file_belongs_to_its_nearest_package(self):
        self.assertEqual(affected.owning_packages(["server/internal/hub/route/r.go"], GRAPH), {M + "internal/hub/route"})
        self.assertEqual(affected.owning_packages(["server/internal/hub/testdata/x.json"], GRAPH), {M + "internal/hub"})
        self.assertEqual(affected.owning_packages(["server/README.md", "web/src/a.ts"], GRAPH), set())
        self.assertEqual(affected.owning_packages(["sdk/go/ext/a.go", "sdk/go/doc.go"], GRAPH),
                         {affected.SDK_MODULE + "/ext", affected.SDK_MODULE})

    def test_every_importer_and_every_package_whose_tests_import_one_is_affected(self):
        got = affected.affected_packages({M + "internal/store"}, GRAPH)
        self.assertEqual(got, {M + "internal/store", M + "internal/assistant", M + "internal/hub", M + "cmd/personad",
                               M + "internal/hubtest"})
        self.assertNotIn(M + "internal/wire", got)  # what the change imports is not affected by it
        self.assertEqual(affected.affected_packages({M + "internal/lone"}, GRAPH), {M + "internal/lone"})

    def test_an_sdk_package_reaches_the_server_packages_that_import_it(self):
        graph = dict(GRAPH)
        graph[M + "internal/extmgr"] = ("server/internal/extmgr", {affected.SDK_MODULE + "/ext"}, set())
        got = affected.affected_packages(affected.owning_packages(["sdk/go/ext/a.go"], graph), graph)
        self.assertIn(M + "internal/extmgr", got)

    def test_check_fast_groups_add_the_packages_under_them(self):
        self.assertEqual(affected.expand_groups(["internal/hub"], GRAPH), {M + "internal/hub", M + "internal/hub/route"})
        self.assertEqual(affected.expand_groups(["internal/hu"], GRAPH), set())

    def test_a_plan_tests_the_closure_and_check_fast_groups_with_the_guards(self):
        plan = affected.make_plan(["server/internal/assistant/a.go", "docs/x.md"], graph=GRAPH,
                                  fast=fast(groups=["internal/lone"], guards=True,
                                            lines=("queued: server go test ./internal/lone/...",
                                                   "queued: server repo-wide guards", "queued: privacy scan")))
        self.assertFalse(plan["full"])
        self.assertEqual(plan["go_packages"], sorted({M + "internal/assistant", M + "internal/hub", M + "cmd/personad",
                                                      M + "internal/hubtest", M + "internal/lone"}))
        self.assertTrue(plan["go_guards"])
        groups = affected.gate_groups(plan)
        self.assertEqual(groups[:3], ["server-build", "server-managers", "server-other"])
        self.assertIn("scripts", groups)  # the privacy scan is a check-fast check beyond the server's Go ones

    def test_a_change_outside_go_runs_only_check_fast(self):
        plan = affected.make_plan(["web/src/lib/api.ts"], graph=GRAPH,
                                  fast=fast(lines=("queued: web vitest related", "queued: privacy scan")))
        self.assertEqual(plan["go_packages"], [])
        self.assertFalse(plan["go_guards"])
        self.assertNotIn("server-build", affected.gate_groups(plan))


class AffectedCommandTests(unittest.TestCase):
    def test_guard_lists_come_from_check_fast(self):
        whole, tested, pattern = shards.guard_lists()
        self.assertIn("./internal/archguard", whole)
        self.assertIn("./internal/hub", tested)
        self.assertTrue(pattern)

    def test_the_affected_packages_run_like_a_shard_with_the_guards_they_do_not_cover(self):
        whole, tested, pattern = shards.guard_lists()
        plain = next(p for p in tested if not shards.go_partitions.is_partitioned(p))  # one run in one process
        packages = [M + "internal/lone", M + whole[0][2:], M + plain[2:]]
        runs = shards.affected_commands(packages, "/tmp/db.json", {}, True)
        self.assertEqual(runs[0][-3:], packages)
        self.assertEqual(runs[0][runs[0].index("go"):runs[0].index("go") + 5], ["go", "test", "-json", "-p", "3"])
        self.assertNotIn(whole[0], runs[1])  # already among the packages: not run again whole
        self.assertEqual(runs[1][-len(whole) + 1:], whole[1:])
        self.assertEqual(runs[2][runs[2].index("-run") + 1], pattern)
        self.assertNotIn(plain, runs[2])
        self.assertIn("./internal/hub", runs[2])  # a guard test package the change does not reach still runs its guards

    def test_without_guards_or_packages_nothing_runs(self):
        self.assertEqual(shards.affected_commands([], "/tmp/db.json", {}, False), [])
        self.assertEqual(len(shards.affected_commands([M + "internal/lone"], "/tmp/db.json", {}, False)), 1)

    def test_a_partitioned_affected_package_runs_in_its_partitions(self):
        runs = shards.affected_commands([M + "internal/hub", M + "internal/lone"], "/tmp/db.json",
                                        {"./internal/hub": ["^(TestA)$", "^(TestB)$"]}, False)
        self.assertEqual([run[-3:] for run in runs[1:]], [["-run", "^(TestA)$", "./internal/hub"],
                                                          ["-run", "^(TestB)$", "./internal/hub"]])


if __name__ == "__main__":
    unittest.main()
