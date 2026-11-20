"""The counts surface table, spec/fixtures/counts/surfaces.json, kept in step with the code (#11900).

docs/plans/plan-derived-counts.md (section 3.5, step 4) asks for one row per surface that shows a count: the count it
draws, its scope, each client's code symbol and test, and whether drawing the manager's view would show today's number
(`matches`) or not (`differs`, naming the decision that settles it). The guard below holds the table to that:

- its shape: known clients, scopes and counts; a `differs` row names decisions the plan's section 6 lists, a `matches`
  row none; a row that draws a count whose rule waits on a decision differs, naming it;
- every code symbol is declared in its file, and every test file names its symbol;
- `matches` rows with the same label draw the same count;
- the replays' report surfaces (`replays`) are rows of each lane, read from keys every fixture case has, and a
  lane's mark in a case (`known_differences.<lane>`) on a key a surface reads is in that row's `decisions`;
- the `differs` rows only go down: the README states how many there are, and none is new against the base commit;
- a hub-fed surface (a row with `view`) names its fallback in no source file of its client but the fallback's own;
- check-fast runs this guard for every file the table names (scripts/ci/check_fast_map.py).

The unit tests run the guard on the real table, then seed each violation into a copy and check that it is caught.

    python3 -B scripts/test/test_counts_surfaces.py
"""

import copy
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts" / "ci"))
import check_fast_map  # noqa: E402

TABLE = "spec/fixtures/counts/surfaces.json"
README = "spec/fixtures/counts/README.md"
PLAN = "docs/plans/plan-derived-counts.md"
SUITE = "scripts/test/test_counts_surfaces.py"

# Each client's source: the directories and extensions a fallback must not be named in outside its own file.
CLIENTS = {
    "ios": (("apple/",), (".swift",)),
    "android": (("android/",), (".kt", ".java")),
    "web": (("web/src/",), (".ts", ".tsx")),
    "desktop": (("desktop/src/", "web/src/"), (".ts", ".cts", ".mts", ".tsx")),
    "server": (("server/",), (".go",)),
    "harmony": (("harmony/", "native/app-core/"), (".ets", ".ts", ".rs")),
}
# A test or fixture file, which may name a fallback: the fallback's replay keeps it until the plan's step 13.
_TEST_PATH = re.compile(r"(^|/)(PersonaTests|PersonaUITests|Tests|tests|test|androidTest|testFixtures|testShadow|e2e)/"
                        r"|\.(test|spec)\.[cm]?[jt]sx?$|_test\.go$")
_SKIP_DIRS = {".git", "node_modules", "build", ".build", "dist", "DerivedData", "target", ".gradle"}
SCOPES = {"assistant", "unscoped", "default_manager", "manager", "each_assistant", "all_hubs", "list"}
STATUSES = ("matches", "differs")
ROW_KEYS = {"client", "surface", "label", "shown_on", "count", "scope", "code", "tests", "status", "why"}
OPTIONAL_KEYS = {"decisions", "local", "view"}
_DECISION = re.compile(r"D[0-9]+")
_PLAN_DECISION = re.compile(r"^- \*\*(D[0-9]+)\*\*", re.M)
_README_COUNT = re.compile(r"`differs` rows today: ([0-9]+)")

# How each language declares a name: a dotted symbol's every part must be declared in its file.
_DECLARATIONS = {
    ".swift": r"\b(?:func|var|let|case|class|struct|enum|actor|protocol|extension|typealias)\s+{name}\b",
    ".kt": r"\b(?:fun|val|var|class|object|interface|typealias)\s+(?:<[^>]*>\s*)?(?:[^\s(=:<]*(?:<[^>]*>)?\.)?{name}\b",
    ".java": r"\b(?:class|interface|enum|record|[\w<>\[\]]+)\s+{name}\s*[({=;]",
    ".ts": r"\b(?:function\*?|const|let|var|class|interface|type|enum)\s+{name}\b",
    ".go": r"\bfunc\s+(?:\([^)]*\)\s*)?{name}\b|\btype\s+{name}\b|\b(?:var|const)\s+{name}\b",
    ".rs": r"\b(?:fn|struct|enum|const|static|type|mod|trait)\s+{name}\b",
}
for _ext in (".tsx", ".js", ".mjs", ".cts", ".mts"):
    _DECLARATIONS[_ext] = _DECLARATIONS[".ts"]


def rid(row):
    """A row's id: <client>.<surface>."""
    return f"{row.get('client')}.{row.get('surface')}"


def declared(text, symbol, suffix):
    """Whether every part of a dotted symbol is declared in a source file's text."""
    pattern = _DECLARATIONS.get(suffix)
    if pattern is None:
        return False
    return all(re.search(pattern.format(name=re.escape(part)), text) for part in symbol.split("."))


def plan_decisions(plan_text):
    """The decisions the plan's section 6 lists (- **D<n>** …)."""
    start = plan_text.find("## 6.")
    end = plan_text.find("\n## ", start + 1)
    return set(_PLAN_DECISION.findall(plan_text[start:end if end > 0 else None])) if start >= 0 else set()


def differs(table):
    """The ids of the rows that differ."""
    return {rid(row) for row in table.get("surfaces", []) if isinstance(row, dict) and row.get("status") == "differs"}


def shape_problems(table, decisions, root):
    found = []
    if table.get("version") != 1:
        found.append("version: must be 1")
    counts = table.get("counts")
    if not isinstance(counts, dict) or not counts:
        return found + ["counts: missing"]
    for name, count in counts.items():
        if not isinstance(count, dict) or set(count) != {"rule", "pinned_by", "waits_on"}:
            found.append(f"counts.{name}: needs rule, pinned_by and waits_on, and nothing else")
            continue
        if count["rule"] is not None and not (isinstance(count["rule"], str) and count["rule"].strip()):
            found.append(f"counts.{name}: rule is a sentence, or null while it waits on a decision")
        if count["pinned_by"] is not None and not (root / str(count["pinned_by"])).is_dir():
            found.append(f"counts.{name}: pinned_by {count['pinned_by']} is not a fixture directory")
        if not isinstance(count["waits_on"], list) or any(d not in decisions for d in count["waits_on"]):
            found.append(f"counts.{name}: waits_on names a decision the plan's section 6 does not list")
        elif count["rule"] is None and not count["waits_on"]:
            found.append(f"counts.{name}: a count with no rule waits on a decision")
    rows = table.get("surfaces")
    if not isinstance(rows, list) or not rows:
        return found + ["surfaces: missing"]
    seen = set()
    for row in rows:
        if not isinstance(row, dict):
            found.append("surfaces: a row is not an object")
            continue
        name = rid(row)
        missing, extra = ROW_KEYS - set(row), set(row) - ROW_KEYS - OPTIONAL_KEYS
        if missing or extra:
            found.append(f"{name}: missing {sorted(missing)}, unknown {sorted(extra)}")
            continue
        if name in seen:
            found.append(f"{name}: listed twice")
        seen.add(name)
        if row["client"] not in CLIENTS:
            found.append(f"{name}: unknown client {row['client']}")
        if row["scope"] not in SCOPES:
            found.append(f"{name}: unknown scope {row['scope']}")
        for key in ("surface", "label", "shown_on", "why"):
            if not isinstance(row[key], str) or not row[key].strip():
                found.append(f"{name}: {key} is empty")
        if not isinstance(row["count"], list) or not row["count"] or any(c not in counts for c in row["count"]):
            found.append(f"{name}: count must list known count ids")
            continue
        if row["status"] not in STATUSES:
            found.append(f"{name}: status must be matches or differs")
            continue
        named = row.get("decisions")
        if row["status"] == "matches" and named is not None:
            found.append(f"{name}: a matches row names no decision")
        if row["status"] == "differs":
            if not isinstance(named, list) or not named:
                found.append(f"{name}: a differs row names the decisions that settle it")
                continue
            for decision in named:
                if not isinstance(decision, str) or not _DECISION.fullmatch(decision) or decision not in decisions:
                    found.append(f"{name}: {decision} is not a decision of the plan's section 6")
        for count in row["count"]:
            waits = counts[count]["waits_on"] if isinstance(counts.get(count), dict) else []
            if isinstance(counts.get(count), dict) and counts[count]["rule"] is None:
                if row["status"] != "differs" or not set(waits) <= set(named or []):
                    found.append(f"{name}: {count} has no rule until {', '.join(waits)}, so the row differs naming it")
        for key in ("code", "tests"):
            entries = row[key]
            if not isinstance(entries, list) or (key == "code" and not entries) or any(
                    not isinstance(e, dict) or set(e) != {"file", "symbol"} for e in entries):
                found.append(f"{name}: {key} lists {{file, symbol}} entries" + (", at least one" if key == "code" else ""))
        view = row.get("view")
        if view is not None and not (isinstance(view, dict) and set(view) == {"fallback"}
                                     and isinstance(view["fallback"], dict)
                                     and set(view["fallback"]) == {"file", "symbol"}):
            found.append(f"{name}: view is {{fallback: {{file, symbol}}}}")
    return found


def symbol_problems(table, root):
    found = []
    for row in table["surfaces"]:
        for entry in row["code"]:
            path = root / entry["file"]
            if not path.is_file():
                found.append(f"{rid(row)}: {entry['file']} does not exist")
            elif not declared(path.read_text(errors="replace"), entry["symbol"], path.suffix):
                found.append(f"{rid(row)}: {entry['file']} declares no {entry['symbol']}")
        for entry in row["tests"]:
            path = root / entry["file"]
            if not path.is_file():
                found.append(f"{rid(row)}: test {entry['file']} does not exist")
            elif not re.search(r"(?<![\w])" + re.escape(entry["symbol"]) + r"(?![\w])", path.read_text(errors="replace")):
                found.append(f"{rid(row)}: test {entry['file']} never names {entry['symbol']}")
    return found


def label_problems(table):
    found, drawn = [], {}
    for row in table["surfaces"]:
        if row["status"] == "matches":
            drawn.setdefault(row["label"], {}).setdefault(tuple(row["count"]), []).append(rid(row))
    for label, ids in sorted(drawn.items()):
        if len(ids) > 1:
            found.append(f"label {label!r}: matches rows draw {len(ids)} counts: "
                         + "; ".join(f"{' + '.join(count)} ({', '.join(rows)})" for count, rows in sorted(ids.items())))
    return found


def _cases(root, fixture):
    directory = root / "spec" / "fixtures" / fixture
    files = sorted(directory.glob("*.json")) if directory.is_dir() else []
    return [case for path in files for case in json.loads(path.read_text()).get("cases", [])]


def replay_problems(table, root):
    found = []
    rows = {rid(row) for row in table["surfaces"]}
    for fixture, replay in (table.get("replays") or {}).items():
        cases = _cases(root, fixture)
        if not cases:
            found.append(f"replays.{fixture}: spec/fixtures/{fixture} has no cases")
            continue
        for lane in replay.get("lanes", []):
            if lane not in CLIENTS:
                found.append(f"replays.{fixture}: unknown lane {lane}")
        for surface, read in (replay.get("surfaces") or {}).items():
            for lane in replay.get("lanes", []):
                if f"{lane}.{surface}" not in rows:
                    found.append(f"replays.{fixture}: {lane} reports {surface}, which is not a row of the table")
            for key in (read.get("value"), read.get("keys")):
                if not key or any(key not in case.get("expected", {}) for case in cases):
                    found.append(f"replays.{fixture}.{surface}: not every case's expected has {key}")
            found += mark_problems(table, fixture, cases, replay, surface, read)
    return found


def mark_problems(table, fixture, cases, replay, surface, read):
    """A lane's mark (known_differences.<lane>) that changes a key the surface reads makes the lane's row differ,
    naming the mark's decision: that row cannot match until the decision settles the mark."""
    found = []
    rows = {rid(row): row for row in table["surfaces"]}
    keys = {read.get("value"), read.get("keys")}
    for case in cases:
        expected = case.get("expected", {})
        for lane, mark in (case.get("known_differences") or {}).items():
            row = rows.get(f"{lane}.{surface}")
            if lane not in replay.get("lanes", []) or row is None or not isinstance(mark, dict):
                continue
            replaced = mark.get("replace") or {}
            if not any(key in replaced and replaced[key] != expected.get(key) for key in keys):
                continue
            decision = mark.get("decision")
            if row.get("status") != "differs" or decision not in (row.get("decisions") or []):
                found.append(f"{rid(row)}: {fixture}'s case {case.get('name')} marks it as {decision}, "
                             "which the row's decisions do not name")
    return found


def ratchet_problems(table, readme_text, base_table):
    found = []
    now = differs(table)
    stated = _README_COUNT.search(readme_text)
    if not stated:
        found.append(f"{README}: no \"`differs` rows today: N\" line")
    elif int(stated.group(1)) != len(now):
        found.append(f"{README}: says {stated.group(1)} `differs` rows; the table has {len(now)}")
    if base_table is not None:
        new = sorted(now - differs(base_table))
        if new:
            found.append("the `differs` rows only go down; new against the base commit: " + ", ".join(new))
    return found


def client_sources(root, client):
    """A client's source files, tests left out, as repository paths."""
    roots, suffixes = CLIENTS[client]
    for top in roots:
        for directory, subdirs, names in os.walk(root / top):
            subdirs[:] = sorted(d for d in subdirs if d not in _SKIP_DIRS)
            for name in sorted(names):
                path = (Path(directory) / name).relative_to(root).as_posix()
                if path.endswith(suffixes) and not _TEST_PATH.search(path):
                    yield path


def fallback_problems(table, root):
    found = []
    for row in table["surfaces"]:
        view = row.get("view")
        if not view:
            continue
        fallback = view["fallback"]
        path = root / fallback["file"]
        if not path.is_file() or not declared(path.read_text(errors="replace"), fallback["symbol"], path.suffix):
            found.append(f"{rid(row)}: its fallback {fallback['symbol']} is not declared in {fallback['file']}")
            continue
        name = re.compile(r"(?<![\w])" + re.escape(fallback["symbol"].split(".")[-1]) + r"(?![\w])")
        for source in client_sources(root, row["client"]):
            if source != fallback["file"] and name.search((root / source).read_text(errors="replace")):
                found.append(f"{rid(row)}: {source} names its fallback {fallback['symbol']} outside {fallback['file']}")
    return found


def named_files(table):
    files = {entry["file"] for row in table["surfaces"] for key in ("code", "tests") for entry in row[key]}
    files |= {row["view"]["fallback"]["file"] for row in table["surfaces"] if row.get("view")}
    return sorted(files)


def check_fast_problems(table, checks):
    """check-fast runs this guard for the table, its README, the plan and every file the table names."""
    wanted = "suite:" + SUITE
    return [f"check-fast does not run {SUITE} for {path} (scripts/ci/check_fast_map.py)"
            for path in [TABLE, README, PLAN, *named_files(table)] if wanted not in checks(path)]


def problems(root, table, readme_text, plan_text, base_table=None, checks=None):
    """Everything wrong with a table, as messages; [] when it holds."""
    found = shape_problems(table, plan_decisions(plan_text), root)
    if found:
        return found
    found += symbol_problems(table, root)
    found += label_problems(table)
    found += replay_problems(table, root)
    found += ratchet_problems(table, readme_text, base_table)
    found += fallback_problems(table, root)
    if checks is not None:
        found += check_fast_problems(table, checks)
    return found


def _git(*args):
    return subprocess.run(["git", *args], cwd=ROOT, capture_output=True, text=True, timeout=60)


def base_table():
    """The table at the base commit (CI_BASE_SHA, else the merge base with origin's default branch), or None: no base
    here, or the table is new there."""
    base = os.environ.get("CI_BASE_SHA", "").strip()
    if not base:
        for ref in ("refs/remotes/origin/HEAD", "refs/remotes/origin/main"):
            merge = _git("merge-base", "HEAD", ref)
            if merge.returncode == 0:
                base = merge.stdout.strip()
                break
    if not base:
        return None
    shown = _git("show", f"{base}:{TABLE}")
    return json.loads(shown.stdout) if shown.returncode == 0 else None


def _read(path):
    return (ROOT / path).read_text(encoding="utf-8")


class RealTableTests(unittest.TestCase):
    def test_the_table_holds(self):
        table = json.loads(_read(TABLE))
        self.assertEqual([], problems(ROOT, table, _read(README), _read(PLAN), base_table(), check_fast_map.checks))

    def test_every_surface_of_the_plans_inventory_is_a_row(self):
        # The plan's section 2 names each place a count is drawn; these are its rows' symbols, one per surface.
        symbols = {entry["symbol"] for row in json.loads(_read(TABLE))["surfaces"] for entry in row["code"]}
        for symbol in ("AgentsNeedsYou.badge", "AgentsNeedsYou.rows", "AppModel.countBadges", "InboxStore.unreadCount",
                       "InboxStore.actionCount", "TodoStore.sidebarCount", "AssistantBadges.waiting",
                       "AppIconBadge.count", "WatchPet.status", "IntentPicker.waiting", "VisionShell",
                       "ShellCounts.agentRows", "agentNeedsYouRows", "inboxUnreadCount", "ShellCounts.todos",
                       "ShellCounts.waiting", "assistantRequests", "stateCounts", "useScopedCounts", "badgeCount",
                       "ownerBadge", "waitingAgents", "setBadge", "islandState", "needsYou", "linkAttention",
                       "tellInboxNeedsYou", "Counts", "treeCounts", "counts"):
            self.assertIn(symbol, symbols)


class SeededViolationTests(unittest.TestCase):
    """Each rule, broken on purpose in a small tree of its own."""

    PLAN_TEXT = "# Plan\n\n## 6. Open decisions for maintainers\n\n- **D1** One rule.\n- **D7** The Inbox unit.\n\n## 7. Out\n"
    FILES = {
        "apple/App/Badges.swift": "enum Badges {\n    static func agents() -> Int { 1 }\n}\n",
        "apple/App/Fallback.swift": "enum AgentsFallback {\n    static func badge() -> Int { 1 }\n}\n",
        "apple/App/Other.swift": "let n = 2\n",
        "apple/PersonaTests/BadgesTests.swift": "struct BadgesTests { func a() { _ = AgentsFallback.badge() } }\n",
        "web/src/lib/inbox.ts": "export function badgeCount(items: number[]): number { return items.length; }\n",
        "web/src/lib/inbox.test.ts": "import { badgeCount } from './inbox';\n",
        "server/internal/live/live.go": "package live\n\nfunc (s *Service) needsYou() int { return 1 }\n",
        "spec/fixtures/cases/cases.json": json.dumps({"version": 1, "cases": [
            {"name": "one", "expected": {"rows": [{"id": "a", "cards": []}], "badge": 1}},
            {"name": "marked", "expected": {"rows": [], "badge": 0},
             "known_differences": {"ios": {"decision": "D1", "why": "Bounded lists.", "replace": {"badge": 1}}}}]}),
    }
    TABLE = {
        "version": 1,
        "counts": {
            "agents": {"rule": "Agents that wait.", "pinned_by": "spec/fixtures/cases", "waits_on": []},
            "unread": {"rule": "Unread items.", "pinned_by": None, "waits_on": ["D7"]},
            "persona": {"rule": None, "pinned_by": None, "waits_on": ["D1"]},
        },
        "replays": {"cases": {"lanes": ["ios"], "surfaces": {"tab": {"value": "badge", "keys": "rows"}}}},
        "surfaces": [
            {"client": "ios", "surface": "tab", "label": "Agents", "shown_on": "the tab", "count": ["agents"],
             "scope": "assistant", "code": [{"file": "apple/App/Badges.swift", "symbol": "Badges.agents"}],
             "tests": [{"file": "apple/PersonaTests/BadgesTests.swift", "symbol": "BadgesTests"}],
             "status": "differs", "decisions": ["D1"], "why": "Bounded lists."},
            {"client": "server", "surface": "live", "label": "Agents", "shown_on": "the Live Activity",
             "count": ["agents"], "scope": "default_manager",
             "code": [{"file": "server/internal/live/live.go", "symbol": "needsYou"}], "tests": [],
             "status": "matches", "why": "Its rule."},
            {"client": "web", "surface": "inbox", "label": "Inbox", "shown_on": "the sidebar", "count": ["unread"],
             "scope": "assistant", "code": [{"file": "web/src/lib/inbox.ts", "symbol": "badgeCount"}],
             "tests": [{"file": "web/src/lib/inbox.test.ts", "symbol": "badgeCount"}],
             "status": "matches", "why": "Unread."},
            {"client": "ios", "surface": "persona", "label": "Persona", "shown_on": "the Persona tab",
             "count": ["persona"], "scope": "assistant",
             "code": [{"file": "apple/App/Badges.swift", "symbol": "Badges"}], "tests": [],
             "status": "differs", "decisions": ["D1"], "why": "Waits on D1."},
        ],
    }
    README_TEXT = "Rows.\n\n`differs` rows today: 2.\n"

    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="counts-surfaces-")
        cls.root = Path(cls.temp.name)
        for path, text in cls.FILES.items():
            (cls.root / path).parent.mkdir(parents=True, exist_ok=True)
            (cls.root / path).write_text(text)

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def run_guard(self, table=None, readme=None, base=None, checks=None):
        return problems(self.root, table or self.TABLE, self.README_TEXT if readme is None else readme,
                        self.PLAN_TEXT, base, checks)

    def broken(self, change):
        table = copy.deepcopy(self.TABLE)
        change(table)
        return table

    def assertCaught(self, table, *words, **kwargs):
        found = self.run_guard(table, **kwargs)
        self.assertTrue(any(all(word in line for word in words) for line in found), found)

    def test_the_seed_table_holds(self):
        self.assertEqual([], self.run_guard(checks=lambda path: ["suite:" + SUITE]))

    def test_a_missing_file_or_an_undeclared_symbol(self):
        self.assertCaught(self.broken(lambda t: t["surfaces"][0]["code"][0].update(file="apple/App/Gone.swift")),
                          "Gone.swift", "does not exist")
        self.assertCaught(self.broken(lambda t: t["surfaces"][0]["code"][0].update(symbol="Badges.renamed")),
                          "declares no Badges.renamed")
        # A name that is only used, not declared, is not there.
        self.assertCaught(self.broken(lambda t: t["surfaces"][0]["code"][0].update(file="apple/App/Other.swift",
                                                                                   symbol="Badges")),
                          "declares no Badges")
        self.assertCaught(self.broken(lambda t: t["surfaces"][1]["code"][0].update(symbol="Service.gone")),
                          "declares no Service.gone")
        self.assertCaught(self.broken(lambda t: t["surfaces"][0]["tests"][0].update(symbol="GoneTests")),
                          "never names GoneTests")

    def test_matches_rows_with_one_label_draw_one_count(self):
        def same_label(t):
            t["surfaces"][2]["label"] = "Agents"
        self.assertCaught(self.broken(same_label), "label 'Agents'", "2 counts")
        # A differs row under the label is the known difference, not a conflict.
        def differing(t):
            same_label(t)
            t["surfaces"][2].update(status="differs", decisions=["D7"])
        self.assertEqual([], self.run_guard(self.broken(differing), readme="`differs` rows today: 3"))

    def test_differs_rows_only_go_down(self):
        self.assertCaught(self.TABLE, "says 5", "has 2", readme="`differs` rows today: 5")
        self.assertCaught(self.TABLE, "no \"`differs` rows today: N\" line", readme="Rows.\n")
        def new_difference(t):
            t["surfaces"][2].update(status="differs", decisions=["D7"])
        self.assertCaught(self.broken(new_difference), "only go down", "web.inbox",
                          readme="`differs` rows today: 3", base=self.TABLE)
        # Fewer is fine: a row that came to match leaves the list.
        def fewer(t):
            t["surfaces"][0]["status"] = "matches"
            del t["surfaces"][0]["decisions"]
            t["replays"] = {}  # the seed's mark on ios.tab goes with it, as a real one would
        self.assertEqual([], self.run_guard(self.broken(fewer), readme="`differs` rows today: 1", base=self.TABLE))

    def test_a_differs_row_names_a_decision_of_the_plan(self):
        self.assertCaught(self.broken(lambda t: t["surfaces"][0].pop("decisions")), "names the decisions")
        self.assertCaught(self.broken(lambda t: t["surfaces"][0].update(decisions=["D99"])), "D99")
        self.assertCaught(self.broken(lambda t: t["surfaces"][1].update(decisions=["D1"])), "names no decision")

    def test_a_count_with_no_rule_cannot_match(self):
        def matching(t):
            t["surfaces"][3]["status"] = "matches"
            del t["surfaces"][3]["decisions"]
        self.assertCaught(self.broken(matching), "ios.persona", "no rule until D1")

    def test_the_shape(self):
        self.assertCaught(self.broken(lambda t: t["surfaces"][0].update(client="tvos")), "unknown client tvos")
        self.assertCaught(self.broken(lambda t: t["surfaces"][0].update(scope="planet")), "unknown scope planet")
        self.assertCaught(self.broken(lambda t: t["surfaces"][0].update(count=["agents_typo"])), "known count ids")
        self.assertCaught(self.broken(lambda t: t["surfaces"].append(copy.deepcopy(t["surfaces"][0]))), "listed twice")
        self.assertCaught(self.broken(lambda t: t["surfaces"][0].pop("why")), "missing ['why']")
        self.assertCaught(self.broken(lambda t: t["counts"]["agents"].update(pinned_by="spec/fixtures/gone")),
                          "not a fixture directory")

    def test_the_replays_report_rows_of_the_table(self):
        self.assertCaught(self.broken(lambda t: t["replays"]["cases"]["lanes"].append("android")),
                          "android reports tab")
        self.assertCaught(self.broken(lambda t: t["replays"]["cases"]["surfaces"]["tab"].update(keys="badge_rows")),
                          "badge_rows")

    def test_a_lanes_mark_on_a_surface_is_in_its_rows_decisions(self):
        self.assertCaught(self.broken(lambda t: t["surfaces"][0].update(decisions=["D7"])),
                          "ios.tab", "marked", "D1")

        def matching(t):
            t["surfaces"][0]["status"] = "matches"
            del t["surfaces"][0]["decisions"]
        self.assertCaught(self.broken(matching), "ios.tab", "marked", "D1", readme="`differs` rows today: 1.\n")
        # A mark on a key the surface does not read leaves the row alone.
        self.assertEqual([], mark_problems(self.TABLE, "cases", [
            {"name": "other", "expected": {"rows": [], "badge": 0, "extra": 0},
             "known_differences": {"ios": {"decision": "D7", "replace": {"extra": 1}}}}],
            self.TABLE["replays"]["cases"], "tab", {"value": "badge", "keys": "rows"}))

    def test_a_hub_fed_surface_names_its_fallback_only_in_the_fallback_file(self):
        def fed(t):
            t["surfaces"][0]["view"] = {"fallback": {"file": "apple/App/Fallback.swift", "symbol": "AgentsFallback.badge"}}
        # Its own file and the tests may name it.
        self.assertEqual([], self.run_guard(self.broken(fed)))
        (self.root / "apple/App/Other.swift").write_text("let n = AgentsFallback.badge()\n")
        try:
            self.assertCaught(self.broken(fed), "apple/App/Other.swift names its fallback AgentsFallback.badge")
        finally:
            (self.root / "apple/App/Other.swift").write_text(self.FILES["apple/App/Other.swift"])
        def gone(t):
            t["surfaces"][0]["view"] = {"fallback": {"file": "apple/App/Fallback.swift", "symbol": "AgentsFallback.gone"}}
        self.assertCaught(self.broken(gone), "is not declared")

    def test_check_fast_runs_the_guard_for_every_named_file(self):
        found = self.run_guard(checks=lambda path: [] if path == "web/src/lib/inbox.ts" else ["suite:" + SUITE])
        self.assertEqual([f"check-fast does not run {SUITE} for web/src/lib/inbox.ts (scripts/ci/check_fast_map.py)"],
                         found)

    def test_declarations_by_language(self):
        self.assertTrue(declared("nonisolated extension AgentsNeedsYou {\n static func badge(", "AgentsNeedsYou.badge",
                                 ".swift"))
        self.assertTrue(declared("internal class ShellCounts(\n    fun agentRows(", "ShellCounts.agentRows", ".kt"))
        self.assertTrue(declared("private fun List<NeedsYouRow>.ids(): X", "ids", ".kt"))
        self.assertTrue(declared("export function stateCounts(all) {", "stateCounts", ".ts"))
        self.assertTrue(declared("func (s *Service) tellInboxNeedsYou() {", "tellInboxNeedsYou", ".go"))
        self.assertTrue(declared("type treeCounts struct {", "treeCounts", ".go"))
        self.assertTrue(declared("fn counts(base: &[Row]) -> Counts {", "counts", ".rs"))
        self.assertFalse(declared("let n = stateCounts(all)", "stateCounts", ".ts"))
        self.assertFalse(declared("x := needsYou(a)", "needsYou", ".go"))


if __name__ == "__main__":
    unittest.main()
