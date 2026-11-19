"""spec/fixtures/todo-detail and spec/fixtures/todo-layout (#10465): the to-do page's elements and the To-dos window
rule as data. No client reads them yet: iOS will replay them from its paper and its layout (#10465), the web and
Android from their stages.

    python3 -B scripts/test/test_todo_fixtures.py

Each set's format and ids; every case follows the rules its README states (written out below as `paper` and
`todo_panes`, so a case and its README cannot drift apart); the proposal's three page columns (3.5) and its table of
windows (3.8), written down apart from those rules; a layout's regions fill its window; and the guard of the
proposal's 7.7: the everyday cases name no engine, runtime, model, path, repository or session id.
"""

import importlib.util
import json
import math
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FIXTURES = ROOT / "spec" / "fixtures"
DETAIL = FIXTURES / "todo-detail"
LAYOUT = FIXTURES / "todo-layout"
SCHEMA = ROOT / "schema" / "wire" / "todo-view.schema.json"
ASSISTANT = ROOT / "server" / "internal" / "assistant"


def load_module(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / "test" / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


ext = load_module("test_extension_schema")  # the schema validator schema/wire's own test uses
wire = load_module("test_wire_schema")  # its check for fields a schema lacks


NAME = re.compile(r"^[a-z0-9]+(?:_[a-z0-9]+)*$")
CASE_KEYS = {"name", "description", "source", "input", "expected"}

# ---- todo-detail: the paper's elements (proposal 3.5) ----

ELEMENTS = ("bar", "title", "state_line", "recap", "ask_card", "ask_door", "ask_pointer", "chips", "notes", "brief",
            "help", "steps", "results", "conversation_block", "conversation_pointer", "work_details", "composer")
ASK_ELEMENTS = ("ask_card", "ask_door", "ask_pointer")
ONE_OF = (("conversation_block", "conversation_pointer"),)
PAGES = ("paper", "as_before")
PRIMARY = ("ask_card", "ask_door", "mark_done", "tick", "none")
AUDIENCES = ("everyday", "agents")
RUN = re.compile(r"^run:([^/\s]+)/(\S+)$")
OPENERS = ("list", "assistant")  # or run:<machine_id>/<session_id>
# What GET /api/v1/todos lists on every row (the hub's listedTodo writes them always); `view` only from a manager that
# names todo_view.
ROW_FIELDS = {"id", "title", "notes", "done_at", "created_at", "updated_at", "spaces", "agents", "quiet"}
# The pages these cases must cover: the proposal's three columns, the states around them, several asks on one page and
# rows a client of this version does not know all of.
DETAIL_NAMED = {"kyoto_needs_your_choice", "login_fix_waits_for_merge", "release_door_from_widget", "fresh_todo",
                "closed_done_by_persona", "failed_start_with_origin", "failed_start_without_origin",
                "just_finished_mark_done", "release_pane_on_the_steward", "release_pane_on_the_widget_run",
                "nara_step_and_plan_both_ask", "nara_pane_on_the_guide_run", "can_wait_decision",
                "row_from_an_older_manager", "section_from_a_newer_hub", "unknown_ask_is_skipped"}
# Cases whose view carries a value of an open enum this version's schema does not list, as a newer hub may send.
NEWER_HUB = {"section_from_a_newer_hub", "unknown_ask_is_skipped"}
# Cases whose row has no view, as from a manager that does not name todo_view.
OLDER_MANAGER = {"row_from_an_older_manager"}

VIEW_SCHEMA = json.loads(SCHEMA.read_text())


def known(definition):
    """The values of a view schema enum: what a client generated from this version of the schema knows."""
    return set(VIEW_SCHEMA["$defs"][definition]["enum"])


PHASES, SECTIONS, REASONS = known("TodoPhase"), known("TodoListSection"), known("TodoReason")
ASK_KINDS, ASK_HOMES = known("OwnerAskKind"), known("OwnerAskHome")


# Proposal 3.5's three columns, closed on a phone: the elements and the primary each page draws, written from the
# table apart from `paper` below, so a rule changed in both `paper` and the cases is still held to the proposal. Its
# everyday column lists notes only; the prototype's iphone/kyoto also draws What you asked, which the cases follow
# (spec/fixtures/todo-detail/README.md "Judgement calls").
COLUMNS = {
    "kyoto_needs_your_choice": (["bar", "title", "state_line", "recap", "ask_card", "chips", "notes", "brief", "steps",
                                 "results", "conversation_block", "composer"], "ask_card"),
    "login_fix_waits_for_merge": (["bar", "title", "state_line", "chips", "brief", "steps", "results",
                                   "conversation_block", "work_details", "composer"], "tick"),
    "release_door_from_widget": (["bar", "title", "state_line", "recap", "ask_door", "chips", "brief", "steps",
                                  "results", "conversation_block", "work_details", "composer"], "ask_door"),
}


def run_ref(ref):
    return f"run:{ref['machine_id']}/{ref['session_id']}"


def runs_of(todo):
    """The runs a page can name: the to-do's own (agents) and those its steps ask from (an ask id starting step:)."""
    asks = (todo.get("view") or {}).get("asks", [])
    return ({run_ref(a) for a in todo["agents"]}
            | {run_ref(a["run"]) for a in asks if a.get("home") == "run" and a["id"].startswith("step:")})


def paper(i):
    """The page, elements, primary action and addressee spec/fixtures/todo-detail/README.md's rules give input i."""
    todo, ctx, timeline = i["item"]["todo"], i["context"], i.get("timeline")
    view = todo.get("view")
    if view is None or view["phase"] not in PHASES or view["section"] not in SECTIONS or view["reason"] not in REASONS:
        return {"page": "as_before"}
    pane = ctx["pane_channel"] if ctx["pane_open"] else None
    out = ["bar", "title"]
    if not (view["phase"] == "todo" and "line" not in view and "inactive" not in view and not todo.get("start_error")):
        out.append("state_line")
    if timeline and timeline["recap"]:
        out.append("recap")
    drawn = []  # the element each ask the page draws takes, in the order of view.asks
    for ask in view.get("asks", []):
        if ask["home"] not in ASK_HOMES or ask["kind"] not in ASK_KINDS:
            continue  # a newer hub's: this version cannot answer it
        if ask["home"] == "run":
            drawn.append("ask_pointer" if pane == run_ref(ask["run"]) else "ask_door")
        elif ask["kind"] != "retry" or todo.get("origin"):  # the retry route refuses a to-do with no origin
            drawn.append("ask_card")
    out += [e for e in ASK_ELEMENTS if e in drawn]
    is_open = view["phase"] != "done"
    if is_open or any(todo.get(k) for k in ("due_at", "due_date", "remind_at", "priority", "project", "part_of")):
        out.append("chips")
    if todo.get("notes") or not todo.get("brief"):
        out.append("notes")
    if todo.get("brief"):
        out.append("brief")
    if view.get("steps", {}).get("total", 0) > 0:
        out.append("steps")
    if timeline and timeline["results"] > 0:
        out.append("results")
    if timeline and timeline["rounds"] > 0:
        out.append("conversation_pointer" if pane == "steward" else "conversation_block")
    if (todo.get("agents") or todo.get("spaces") or todo.get("pr_url") or todo.get("pr_status")
            or todo.get("other_prs") or todo.get("stage_kind") == "code"):
        out.append("work_details")
    if not ctx["pane_open"]:
        out.append("composer")
    if drawn:  # the first ask drawn, the most urgent; its home in the pane holds the primary for a pointer
        primary = "none" if drawn[0] == "ask_pointer" else drawn[0]
    elif view["phase"] == "needs_you":  # nothing on the page to answer, and marking it done is not what it waits for
        primary = "none"
    elif view["phase"] == "ready":
        primary = "mark_done"
    else:
        primary = "tick"
    if pane is not None:
        addressee = pane
    else:
        addressee = ctx["opened_from"] if RUN.match(ctx["opened_from"]) else "steward"
    return {"page": "paper", "elements": out, "primary": primary, "addressee": addressee}


def open_schema(schema):
    """schema with the enums it marks open ("a newer hub may send other values") left open."""
    out = json.loads(json.dumps(schema))
    for d in out["$defs"].values():
        if "enum" in d and "a newer hub may send other values" in d.get("description", ""):
            del d["enum"]
    return out


def go_json_names(path, struct):
    """The JSON names of a Go struct's fields, as encoding/json writes them (a field tagged "-" is never written)."""
    body = path.read_text().split(f"type {struct} struct {{", 1)[1].split("\n}", 1)[0]
    return set(re.findall(r'`json:"([a-z_]+)', body))


def row_keys():
    """Every field GET /api/v1/todos may write on a row: Todo's and those listedTodo adds (ask, decision_review, ...)."""
    return go_json_names(ASSISTANT / "todos.go", "Todo") | go_json_names(ASSISTANT / "todo_ask.go", "listedTodo")


def line_keys():
    """The line keys spec/fixtures/todo-view/README.md's "Line keys" table lists."""
    text = (FIXTURES / "todo-view" / "README.md").read_text()
    table = text.split("### Line keys", 1)[1].split("\n## ", 1)[0]
    return set(re.findall(r"^\| `([a-z_]+)` \|", table, re.M))


# The proposal's 7.7 guard: words an everyday to-do's page never has a reason to carry. Runtimes come from the shared
# names. The engines are docs/integrations/engines.md's and the terminal hosts (docs/integrations/terminal.md,
# docs/integrations/dsh.md): add one when those docs gain an engine. The models are the families the model providers
# name (docs/integrations/launchers.md): add a family when a provider brings a new one.
ENGINES = ("herdr", "orca", "tern", "paseo", "tmux", "dsh")
MODELS = ("gpt", "sonnet", "opus", "haiku", "o1", "o3", "o4", "deepseek", "qwen", "kimi", "glm", "llama", "mistral")
REPOSITORY = (r"github", r"gitlab", r"bitbucket", r"\.git\b", r"\brepo(?:sitory|sitories|s)?\b", r"pull request",
              r"\bPRs?\b", r"/pull/", r"\bworktree")
PATH = r"(?:^|[\s(\"'])(?:~?/|\.\.?/)[\w.~-]|[A-Za-z]:\\|/Users/|/home/"
SESSION = r"\bsession"
# Keys that only work on a computer has: a run's ids, folder and runtime, and code facts.
COMPUTER_KEYS = {"session_id", "machine_id", "cwd", "agent", "model", "launcher", "engine", "runtime", "pr_url",
                 "pr_status", "other_prs", "stage", "stage_kind", "repository", "repo", "worktree"}


def runtime_words():
    """Every coding agent's key and name ("claude", "Claude Code"), as spec/fixtures/agent-names has them."""
    names = json.loads((FIXTURES / "agent-names" / "names.json").read_text())["names"]
    return {w.lower() for k, v in names.items() for w in (k, v)}


def guard_words():
    words = sorted(runtime_words() | set(ENGINES) | set(MODELS), key=len, reverse=True)
    return re.compile(r"\b(?:" + "|".join(map(re.escape, words)) + r")\b" + "|" + "|".join(REPOSITORY) + "|" + PATH
                      + "|" + SESSION, re.I)


def strings(value, at="$"):
    """(where, key or string) of every key and string value in value."""
    if isinstance(value, dict):
        for k, v in value.items():
            yield f"{at}.{k}", k
            yield from strings(v, f"{at}.{k}")
    elif isinstance(value, list):
        for n, v in enumerate(value):
            yield from strings(v, f"{at}[{n}]")
    elif isinstance(value, str):
        yield at, value


def guard_misses(case):
    """What in an everyday case names an engine, runtime, model, path, repository or session id (proposal 7.7)."""
    words, out = guard_words(), []
    for at, s in strings({"description": case["description"], "input": case["input"], "expected": case["expected"]}):
        key = at.rsplit(".", 1)[-1]
        if key == s and s in COMPUTER_KEYS:
            out.append(f"{at}: key {s!r}")
        elif m := words.search(s):
            out.append(f"{at}: {m.group(0)!r} in {s!r}")
    todo = case["input"]["item"]["todo"]
    if todo.get("agents") or todo.get("spaces"):
        out.append("$.input.item.todo: runs or workspaces")
    return out


# ---- todo-layout: the window rule (proposal 3.8) ----

REGIONS = ("sidebar", "list", "paper", "conversation")
FORMS = ("beside", "over", "pushed", "hidden", "rail")
DEVICES = ("iphone", "duo_outer", "duo_inner", "ipad", "web", "android")
POSTURES = ("flat", "half_folded")
DEEPEST = ("list", "paper", "conversation")
CALLED = ("sidebar", "list")
HYSTERESIS = 24
# xSmall to xxxLarge: the interface's text size stops at xxxLarge (Theme.interfaceTypeSize), so its sizeScale does,
# though Theme.sizeScale itself goes on to 1.6 at the accessibility sizes.
SCALE = (14 / 17, 23 / 17)
SIDE_COLUMN = 84  # the safe-area inset of the Duo's side bars (Theme.Shell.sideColumnEdge's comment)
WEB = {"sidebar": 284, "rail": 56, "list": 360, "paper": 400, "conversation": 440}


def todo_panes(i):
    """The regions spec/fixtures/todo-layout/README.md's rule gives input i, as {region: (form, width)}."""
    width, height, s, device = i["width"], i["height"], i["text_scale"], i["device"]
    room = width - i["side_column"]
    deepest, called = i["wants"]["deepest"], i["wants"].get("called")
    out = {r: ("hidden", 0) for r in REGIONS}
    held = set()  # (region, form): a rail held is only the rail
    prev = i.get("previous")
    if prev and prev["text_scale"] == s and (prev["wants"]["deepest"] == "conversation") == (deepest == "conversation"):
        held = {(r["region"], r["form"]) for r in prev["regions"] if r["form"] in ("beside", "rail")}

    def fits(region, need, room=room, form="beside"):
        return need - (HYSTERESIS if (region, form) in held else 0) <= room

    def one_page():
        out[deepest] = ("beside", room) if deepest == "list" else ("pushed", room)

    if device in ("iphone", "duo_outer"):
        one_page()
        if called == "sidebar":
            out["sidebar"] = ("over", min(0.82 * room, 380))  # the phones' drawer
        return out

    if device in ("duo_inner", "android") and i["crease_split"] and height > width:
        half = room // 2  # the crease: the leading half, then the rest
        leading = "list" if deepest != "conversation" else "paper"
        if fits(leading, 320 * s, half):
            trailing = "paper" if deepest != "conversation" else "conversation"
            out[leading], out[trailing] = ("beside", half), ("beside", room - half)
            if called == "sidebar" or (called == "list" and deepest == "conversation"):
                out[called] = ("over", half)
        else:
            one_page()
            if called == "sidebar":
                out["sidebar"] = ("over", 320)
        return out
    if device == "android":
        raise ValueError("Android outside its book posture's halves is not pinned yet")

    if device == "web":
        side, lst, paper_min, conv = WEB["sidebar"], WEB["list"], WEB["paper"], WEB["conversation"]
    else:
        side = 400 if device == "ipad" else 320
        lst = 300 if device == "ipad" and 600 <= width <= 759 else 340 if min(width, height) >= 1032 else 320
        paper_min = 320 if device == "duo_inner" and i["crease_split"] else 400
        conv = 400
    lst, paper_min, conv = lst * s, paper_min * s, conv * s
    beside = {}
    if deepest == "conversation" and not fits("conversation", paper_min + conv):
        out["conversation"] = ("pushed", room)
    elif deepest != "conversation" and not fits("list", lst + paper_min):
        one_page()
    else:
        content = paper_min
        if deepest == "conversation":
            beside["conversation"] = conv
            content += conv
        if fits("list", lst + content):
            beside["list"] = lst
            content += lst
            if fits("sidebar", side + content):
                beside["sidebar"] = side
            elif device == "web" and fits("sidebar", WEB["rail"] + content, form="rail"):
                out["sidebar"] = ("rail", WEB["rail"])
        for r, w in beside.items():
            out[r] = ("beside", w)
        taken = sum(beside.values()) + (WEB["rail"] if out["sidebar"][0] == "rail" else 0)
        out["paper"] = ("beside", room - taken)
    if called == "sidebar" and out["sidebar"][0] == "hidden":
        out["sidebar"] = ("over", side)
    if called == "list" and out["list"][0] == "hidden" and out["paper"][0] == "beside":
        out["list"] = ("over", lst)
    return out


# The windows of proposal 3.8's table: (width, height, device, side column). The iPad window is any of 600-759 pt, so
# its size is checked apart; the web's "≥ 1600" is taken at its narrowest.
WINDOWS = {
    "iphone": (402, 874, "iphone", 0),
    "duo_outer": (466, 678, "duo_outer", SIDE_COLUMN),
    "duo_inner_portrait": (669, 951, "duo_inner", 0),
    "duo_inner_landscape": (951, 669, "duo_inner", SIDE_COLUMN),
    "ipad_mini_portrait": (744, 1133, "ipad", 0),
    "ipad11_portrait": (834, 1194, "ipad", 0),
    "ipad13_portrait": (1032, 1376, "ipad", 0),
    "ipad11_landscape": (1194, 834, "ipad", 0),
    "ipad13_landscape": (1376, 1032, "ipad", 0),
    "ipad_window": (None, None, "ipad", 0),
    "web_1440": (1440, 900, "web", 0),
    "web_1600": (1600, 1000, "web", 0),
    "android_book": (673, 841, "android", 0),
}

# Proposal 3.8's table at the default text size, conversation closed and open: the regions beside (or the web's rail,
# or the one pushed page), in order. Written from the table, apart from the rule above, so the rule is held to it.
# iPad mini: the table says List 320 │ Paper 424; the todoList token's 300 for windows of 600-759 pt gives 300 │ 444
# (spec/fixtures/todo-layout/README.md "Judgement calls").
TABLE = {
    ("iphone", "paper"): [("paper", "pushed", 402)],
    ("iphone", "conversation"): [("conversation", "pushed", 402)],
    ("duo_outer", "paper"): [("paper", "pushed", 382)],
    ("duo_outer", "conversation"): [("conversation", "pushed", 382)],
    ("duo_inner_portrait", "paper"): [("list", "beside", 334), ("paper", "beside", 335)],
    ("duo_inner_portrait", "conversation"): [("paper", "beside", 334), ("conversation", "beside", 335)],
    ("duo_inner_landscape", "paper"): [("list", "beside", 320), ("paper", "beside", 547)],
    ("duo_inner_landscape", "conversation"): [("paper", "beside", 467), ("conversation", "beside", 400)],
    ("ipad_mini_portrait", "paper"): [("list", "beside", 300), ("paper", "beside", 444)],
    ("ipad_mini_portrait", "conversation"): [("conversation", "pushed", 744)],
    ("ipad11_portrait", "paper"): [("list", "beside", 320), ("paper", "beside", 514)],
    ("ipad11_portrait", "conversation"): [("paper", "beside", 434), ("conversation", "beside", 400)],
    ("ipad13_portrait", "paper"): [("list", "beside", 340), ("paper", "beside", 692)],
    ("ipad13_portrait", "conversation"): [("paper", "beside", 632), ("conversation", "beside", 400)],
    ("ipad11_landscape", "paper"): [("sidebar", "beside", 400), ("list", "beside", 320), ("paper", "beside", 474)],
    ("ipad11_landscape", "conversation"): [("list", "beside", 320), ("paper", "beside", 474),
                                           ("conversation", "beside", 400)],
    ("ipad13_landscape", "paper"): [("sidebar", "beside", 400), ("list", "beside", 340), ("paper", "beside", 636)],
    ("ipad13_landscape", "conversation"): [("list", "beside", 340), ("paper", "beside", 636),
                                           ("conversation", "beside", 400)],
    ("web_1440", "paper"): [("sidebar", "beside", 284), ("list", "beside", 360), ("paper", "beside", 796)],
    ("web_1440", "conversation"): [("sidebar", "rail", 56), ("list", "beside", 360), ("paper", "beside", 584),
                                   ("conversation", "beside", 440)],
    ("web_1600", "paper"): [("sidebar", "beside", 284), ("list", "beside", 360), ("paper", "beside", 956)],
    ("web_1600", "conversation"): [("sidebar", "beside", 284), ("list", "beside", 360), ("paper", "beside", 516),
                                   ("conversation", "beside", 440)],
    ("android_book", "paper"): [("list", "beside", 336), ("paper", "beside", 337)],
    ("android_book", "conversation"): [("paper", "beside", 336), ("conversation", "beside", 337)],
}

# The text sizes that first drop a region (proposal 3.8: minimums scale with the text, the sidebar does not), by case:
# the region and the form that show the drop.
BREAKPOINTS = {
    "duo_inner_portrait_halves_at_xlarge": ("paper", "pushed"),
    "duo_inner_portrait_pane_at_xlarge": ("conversation", "pushed"),
    "ipad11_landscape_at_xlarge": ("sidebar", "hidden"),
    "ipad11_landscape_open_at_xlarge": ("list", "hidden"),
    "ipad11_portrait_open_at_xlarge": ("conversation", "pushed"),
    "ipad13_portrait_open_at_xxxlarge": ("conversation", "pushed"),
}
XLARGE = 1.1176  # 19 / 17, body text at xLarge; xxLarge 21 / 17 ≈ 1.2353; xxxLarge 23 / 17 ≈ 1.3529

WIDTH_TOLERANCE = 0.01  # widths are written to two decimals


def regions_of(expected):
    return {r["region"]: (r["form"], r["width"]) for r in expected["regions"]}


def shown(expected):
    """The regions beside, the rail and the pushed page, in order, as (region, form, width)."""
    return [(r["region"], r["form"], r["width"]) for r in expected["regions"]
            if r["form"] in ("beside", "rail", "pushed")]


class TodoDetail(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.doc = json.loads((DETAIL / "todo_detail.json").read_text())
        cls.cases = cls.doc["cases"]
        cls.schema = VIEW_SCHEMA

    def test_format(self):
        self.assertEqual(set(self.doc), {"version", "cases"})
        self.assertEqual(self.doc["version"], 1)
        names = [c["name"] for c in self.cases]
        self.assertEqual(len(names), len(set(names)), "case names are unique")
        for c in self.cases:
            with self.subTest(case=c["name"]):
                self.assertEqual(set(c), CASE_KEYS | {"audience"})
                self.assertRegex(c["name"], NAME)
                self.assertIn(c["audience"], AUDIENCES)
                self.assertTrue(c["description"].strip())
                self.assertTrue(c["source"] and all(isinstance(s, str) and s for s in c["source"]))
                self.assertLessEqual(set(c["input"]), {"item", "context", "timeline"})
                self.assertIn(c["expected"].get("page"), PAGES)
                if c["expected"]["page"] == "paper":
                    self.assertEqual(set(c["expected"]), {"page", "elements", "primary", "addressee"})
                else:
                    self.assertEqual(c["expected"], {"page": "as_before"})

    def test_items_are_what_the_item_route_answers(self):
        strict = ext.Validator({SCHEMA.name: self.schema})
        loose = ext.Validator({SCHEMA.name: open_schema(self.schema)})
        summary = self.schema["$defs"]["TodoSummary"]
        keys, fields = line_keys(), row_keys()
        self.assertLessEqual(ROW_FIELDS | {"ask", "decision_review", "orphaned", "view", "brief"}, fields)
        for c in self.cases:
            with self.subTest(case=c["name"]):
                self.assertEqual(set(c["input"]["item"]), {"todo"})
                todo = c["input"]["item"]["todo"]
                self.assertLessEqual(ROW_FIELDS, set(todo))
                self.assertLessEqual(set(todo), fields, "fields a listed row never has")
                self.assertEqual("view" not in todo, c["name"] in OLDER_MANAGER, "a row without its view")
                if "view" not in todo:
                    continue
                view = todo["view"]
                self.assertEqual(loose.errors(summary, view, SCHEMA.name), [])
                newer = strict.errors(summary, view, SCHEMA.name)
                self.assertEqual(bool(newer), c["name"] in NEWER_HUB, newer or "a value this version does not know")
                self.assertEqual(wire.walk(self.schema, summary, view, "$", set()), [], "fields the schema lacks")
                if "line" in view:
                    self.assertIn(view["line"]["key"], keys)
                own = {run_ref(a) for a in todo["agents"]}
                for ask in view.get("asks", []):
                    if ask.get("home") == "run":
                        self.assertTrue(run_ref(ask["run"]) in own or ask["id"].startswith("step:"),
                                        "a run's ask names one of the to-do's runs, or a step's")
                timeline = c["input"].get("timeline")
                if timeline is not None:
                    self.assertEqual(set(timeline), {"rounds", "results", "recap"})
                    self.assertTrue(isinstance(timeline["rounds"], int) and timeline["rounds"] >= 0)
                    self.assertTrue(isinstance(timeline["results"], int) and timeline["results"] >= 0)
                    self.assertIsInstance(timeline["recap"], bool)

    def test_contexts(self):
        for c in self.cases:
            with self.subTest(case=c["name"]):
                ctx = c["input"]["context"]
                self.assertEqual(set(ctx), {"wide", "pane_open", "pane_channel", "opened_from"})
                self.assertIsInstance(ctx["wide"], bool)
                self.assertIsInstance(ctx["pane_open"], bool)
                runs = runs_of(c["input"]["item"]["todo"])
                if ctx["pane_open"]:
                    self.assertTrue(ctx["wide"], "the conversation is a pane only at regular widths")
                    self.assertTrue(ctx["pane_channel"] == "steward" or ctx["pane_channel"] in runs)
                else:
                    self.assertIsNone(ctx["pane_channel"])
                self.assertTrue(ctx["opened_from"] in OPENERS or ctx["opened_from"] in runs, ctx["opened_from"])

    def test_expected_is_well_formed(self):
        for c in self.cases:
            with self.subTest(case=c["name"]):
                e = c["expected"]
                if e["page"] != "paper":
                    continue
                self.assertEqual(e["elements"][:2], ["bar", "title"])
                self.assertEqual(e["elements"], sorted(set(e["elements"]), key=ELEMENTS.index), "known, in order, once")
                for group in ONE_OF:
                    self.assertLessEqual(len(set(group) & set(e["elements"])), 1, group)
                self.assertIn(e["primary"], PRIMARY)
                if e["primary"].startswith("ask_"):
                    self.assertIn(e["primary"], e["elements"], "the primary is on the page")
                self.assertTrue(e["addressee"] == "steward" or RUN.match(e["addressee"]), e["addressee"])

    def test_cases_follow_the_readme(self):
        for c in self.cases:
            with self.subTest(case=c["name"]):
                self.assertEqual(c["expected"], paper(c["input"]))

    def test_the_proposals_three_columns(self):
        by_name = {c["name"]: c for c in self.cases}
        for name, (elements, primary) in COLUMNS.items():
            with self.subTest(case=name):
                c = by_name[name]
                ctx = c["input"]["context"]
                self.assertEqual((ctx["wide"], ctx["pane_open"]), (False, False), "closed, on a phone")
                self.assertEqual((c["expected"]["elements"], c["expected"]["primary"]), (elements, primary))

    def test_coverage(self):
        names = {c["name"] for c in self.cases}
        self.assertLessEqual(DETAIL_NAMED, names)
        pages = [c["expected"] for c in self.cases if c["expected"]["page"] == "paper"]
        drawn = {e for p in pages for e in p["elements"]}
        # Persona can help waits for the steps routes (#10465): no case draws it yet.
        self.assertEqual(drawn, set(ELEMENTS) - {"help"})
        self.assertEqual({p["primary"] for p in pages}, set(PRIMARY))
        self.assertEqual({c["expected"]["page"] for c in self.cases}, set(PAGES))
        self.assertTrue(any({"ask_card", "ask_door"} <= set(p["elements"]) for p in pages), "a card and a door")
        self.assertTrue(any({"ask_card", "ask_pointer"} <= set(p["elements"]) for p in pages), "a card and a pointer")
        self.assertEqual({c["audience"] for c in self.cases}, set(AUDIENCES))
        self.assertTrue(any(c["input"].get("timeline") is None for c in self.cases), "a hub without todo_timeline")

    def test_everyday_cases_name_no_machinery(self):
        everyday = [c for c in self.cases if c["audience"] == "everyday"]
        self.assertTrue(everyday)
        for c in everyday:
            with self.subTest(case=c["name"]):
                self.assertEqual(guard_misses(c), [])
                self.assertNotIn("work_details", c["expected"].get("elements", []))

    def test_the_guard_finds_machinery(self):
        def case(**todo):
            item = {"id": "t", "title": "Plan the trip", "notes": "", "agents": [], "spaces": [], **todo}
            return {"description": "A trip.", "input": {"item": {"todo": item}}, "expected": {}}

        self.assertEqual(guard_misses(case()), [])
        for todo in ({"notes": "see ~/Documents/trip.md"}, {"notes": "ask Claude Code"}, {"notes": "on opus"},
                     {"notes": "the session s-1"}, {"notes": "PR #12 is open"}, {"notes": "in the herdr tab"},
                     {"pr_url": "https://example.com/x/pull/1"}, {"agents": [{"title": "Trip"}]},
                     {"notes": "in /home/me/x"}, {"notes": "the weather-app repo"}):
            self.assertNotEqual(guard_misses(case(**todo)), [], todo)

    def test_readme_lists_every_case(self):
        text = (DETAIL / "README.md").read_text()
        for c in self.cases:
            self.assertIn(f"`{c['name']}`", text)

    def test_written_as_the_other_fixtures(self):
        text = (DETAIL / "todo_detail.json").read_text()
        self.assertEqual(text, json.dumps(json.loads(text), indent=2, ensure_ascii=False) + "\n")


class TodoLayout(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.doc = json.loads((LAYOUT / "todo_layout.json").read_text())
        cls.cases = cls.doc["cases"]

    def test_format(self):
        self.assertEqual(set(self.doc), {"version", "cases"})
        self.assertEqual(self.doc["version"], 1)
        names = [c["name"] for c in self.cases]
        self.assertEqual(len(names), len(set(names)), "case names are unique")
        for c in self.cases:
            with self.subTest(case=c["name"]):
                self.assertEqual(set(c), CASE_KEYS | {"window"})
                self.assertRegex(c["name"], NAME)
                self.assertTrue(c["window"] in WINDOWS or c["window"] == "other", c["window"])
                self.assertTrue(c["description"].strip())
                self.assertTrue(c["source"] and all(isinstance(s, str) and s for s in c["source"]))
                self.assertEqual(set(c["expected"]), {"regions"})

    def test_inputs(self):
        keys = {"width", "height", "side_column", "text_scale", "posture", "device", "crease_split", "wants",
                "previous"}
        for c in self.cases:
            with self.subTest(case=c["name"]):
                i = c["input"]
                self.assertEqual(set(i), keys)
                self.assertIn(i["device"], DEVICES)
                self.assertIn(i["posture"], POSTURES)
                self.assertTrue(i["width"] > 0 and i["height"] > 0)
                if i["device"] in ("duo_outer", "duo_inner"):
                    self.assertIn(i["side_column"], (0, SIDE_COLUMN))
                else:
                    self.assertEqual(i["side_column"], 0)
                self.assertTrue(SCALE[0] - 0.001 <= i["text_scale"] <= SCALE[1] + 0.001, i["text_scale"])
                self.assertIsInstance(i["crease_split"], bool)
                if i["device"] not in ("duo_inner", "android"):
                    self.assertFalse(i["crease_split"], "only a foldable's inner display has a crease")
                    self.assertEqual(i["posture"], "flat")
                self.assertLessEqual(set(i["wants"]), {"deepest", "called"})
                self.assertIn(i["wants"]["deepest"], DEEPEST)
                if "called" in i["wants"]:
                    self.assertIn(i["wants"]["called"], CALLED)
                if i["previous"] is not None:
                    p = i["previous"]
                    self.assertEqual(set(p), {"text_scale", "wants", "regions"})
                    self.assertIn(p["wants"]["deepest"], DEEPEST)
                    self.assertEqual([r["region"] for r in p["regions"]], list(REGIONS))
                    self.assertTrue(all(r["form"] in FORMS for r in p["regions"]))

    def test_regions_fill_the_window(self):
        for c in self.cases:
            with self.subTest(case=c["name"]):
                i, regions = c["input"], c["expected"]["regions"]
                room = i["width"] - i["side_column"]
                self.assertEqual([r["region"] for r in regions], list(REGIONS))
                for r in regions:
                    self.assertEqual(set(r), {"region", "form", "width"})
                    self.assertIn(r["form"], FORMS)
                    self.assertEqual(r["width"], round(r["width"], 2), "two decimals")
                    if r["form"] == "hidden":
                        self.assertEqual(r["width"], 0)
                    else:
                        self.assertTrue(0 < r["width"] <= room + WIDTH_TOLERANCE, r)
                    if r["form"] == "rail":
                        self.assertEqual((i["device"], r["region"]), ("web", "sidebar"), "only the web's sidebar rails")
                pushed = [r for r in regions if r["form"] == "pushed"]
                across = [r for r in regions if r["form"] in ("beside", "rail")]
                self.assertTrue(pushed or across, "something shows")
                if pushed:
                    self.assertEqual((len(pushed), across), (1, []), "a pushed page is the one page")
                    self.assertAlmostEqual(pushed[0]["width"], room, delta=WIDTH_TOLERANCE)
                else:
                    self.assertAlmostEqual(sum(r["width"] for r in across), room, delta=WIDTH_TOLERANCE)
                deepest = i["wants"]["deepest"]
                forms = regions_of(c["expected"])
                self.assertIn(forms[deepest][0], ("beside", "pushed"), "the deepest region asked for shows")
                if "called" in i["wants"]:
                    self.assertEqual(forms[i["wants"]["called"]][0], "over", "a region called lies over the page")

    def test_cases_follow_the_readme(self):
        for c in self.cases:
            with self.subTest(case=c["name"]):
                want = todo_panes(c["input"])
                got = regions_of(c["expected"])
                for r in REGIONS:
                    self.assertEqual(got[r][0], want[r][0], r)
                    self.assertAlmostEqual(got[r][1], want[r][1], delta=WIDTH_TOLERANCE, msg=r)

    def test_every_window_of_the_table(self):
        default = [c for c in self.cases if c["input"]["text_scale"] == 1 and c["input"]["previous"] is None
                   and "called" not in c["input"]["wants"]]
        for window, (width, height, device, side) in WINDOWS.items():
            with self.subTest(window=window):
                mine = [c for c in self.cases if c["window"] == window]
                for c in mine:
                    i = c["input"]
                    self.assertEqual((i["device"], i["side_column"]), (device, side))
                    if width is not None:
                        self.assertEqual((i["width"], i["height"]), (width, height))
                    else:
                        self.assertTrue(600 <= i["width"] <= 759, i["width"])
                for deepest in ("paper", "conversation"):
                    found = [c for c in default if c["window"] == window and c["input"]["wants"]["deepest"] == deepest]
                    self.assertTrue(found, f"{window}, wanting the {deepest}")
                    if window == "duo_inner_portrait":
                        # the table's halves need owner decision 2 (iv); until then the one-page fallback
                        self.assertEqual({c["input"]["crease_split"] for c in found}, {True, False})
                        found = [c for c in found if c["input"]["crease_split"]]
                    if (window, deepest) in TABLE:
                        for c in found:
                            self.assertEqual(shown(c["expected"]), TABLE[(window, deepest)], c["name"])

    def test_the_text_size_breakpoints(self):
        by_name = {c["name"]: c for c in self.cases}
        for name, (region, form) in BREAKPOINTS.items():
            with self.subTest(case=name):
                c = by_name[name]
                self.assertGreater(c["input"]["text_scale"], 1)
                self.assertEqual(regions_of(c["expected"])[region][0], form)
                # one size smaller keeps the region: the same window at the default size, or xxLarge on the iPad 13"
                smaller = dict(c["input"], text_scale=1 if c["input"]["text_scale"] <= XLARGE else 1.2353)
                self.assertNotEqual(todo_panes(smaller)[region][0], form)

    def test_hysteresis_and_the_crease_are_pinned(self):
        after = [c for c in self.cases if c["input"]["previous"] is not None]
        held = [c for c in after if any(regions_of(c["expected"])[r][0] != form for r, (form, _) in
                                        todo_panes(dict(c["input"], previous=None)).items())]
        self.assertTrue(held, "a case where the previous layout keeps a region inside the 24-pt band")
        let_go = [c for c in after if any(r["form"] == "beside"
                                          and regions_of(c["expected"])[r["region"]][0] != "beside"
                                          for r in c["input"]["previous"]["regions"])]
        self.assertTrue(let_go, "a case where a region leaves once past the band")
        portrait = [c for c in self.cases if c["window"] == "duo_inner_portrait"]
        self.assertEqual({c["input"]["posture"] for c in portrait}, set(POSTURES))

    def test_readme_lists_every_case(self):
        text = (LAYOUT / "README.md").read_text()
        for c in self.cases:
            self.assertIn(f"`{c['name']}`", text)

    def test_written_as_the_other_fixtures(self):
        text = (LAYOUT / "todo_layout.json").read_text()
        self.assertEqual(text, json.dumps(json.loads(text), indent=2, ensure_ascii=False) + "\n")


class Model(unittest.TestCase):
    """The rules above against a few hand-worked inputs, so a slip in them cannot pass every case with it."""

    def test_widths_scale_but_the_sidebar_does_not(self):
        base = {"width": 1194, "height": 834, "side_column": 0, "text_scale": XLARGE, "posture": "flat",
                "device": "ipad", "crease_split": False, "wants": {"deepest": "paper"}, "previous": None}
        r = todo_panes(base)
        self.assertEqual(r["sidebar"], ("hidden", 0))  # 400 + 720 × 1.1176 > 1194
        self.assertTrue(math.isclose(r["list"][1], 357.632) and math.isclose(r["paper"][1], 836.368))

    def test_the_band_holds_a_region_only_for_the_same_text_size_and_conversation(self):
        prev = {"text_scale": 1, "wants": {"deepest": "paper"}, "regions": [
            {"region": "sidebar", "form": "hidden", "width": 0}, {"region": "list", "form": "beside", "width": 300},
            {"region": "paper", "form": "beside", "width": 400},
            {"region": "conversation", "form": "hidden", "width": 0}]}
        base = {"width": 690, "height": 900, "side_column": 0, "text_scale": 1, "posture": "flat", "device": "ipad",
                "crease_split": False, "wants": {"deepest": "paper"}, "previous": prev}
        self.assertEqual(todo_panes(base)["list"], ("beside", 300))
        self.assertEqual(todo_panes(dict(base, wants={"deepest": "list"}))["list"], ("beside", 300))  # a selection
        self.assertEqual(todo_panes(dict(base, width=675))["paper"], ("pushed", 675))
        self.assertEqual(todo_panes(dict(base, previous=dict(prev, text_scale=0.9412)))["paper"], ("pushed", 690))
        self.assertEqual(todo_panes(dict(base, wants={"deepest": "conversation"}))["conversation"], ("pushed", 690))

    def test_the_band_holds_a_region_in_the_form_it_had(self):
        def web(width, sidebar):
            regions = [{"region": "sidebar", "form": sidebar[0], "width": sidebar[1]},
                       {"region": "list", "form": "beside", "width": 360},
                       {"region": "paper", "form": "beside", "width": width - 800 - sidebar[1]},
                       {"region": "conversation", "form": "beside", "width": 440}]
            prev = {"text_scale": 1, "wants": {"deepest": "conversation"}, "regions": regions}
            return {"width": 1460, "height": 900, "side_column": 0, "text_scale": 1, "posture": "flat",
                    "device": "web", "crease_split": False, "wants": {"deepest": "conversation"}, "previous": prev}

        # The full sidebar needs 1484 with the conversation open: held from 1460, back from a rail only once it fits.
        self.assertEqual(todo_panes(web(1484, ("beside", 284)))["sidebar"], ("beside", 284))
        self.assertEqual(todo_panes(web(1440, ("rail", 56)))["sidebar"], ("rail", 56))
        self.assertEqual(todo_panes(dict(web(1440, ("rail", 56)), width=1484))["sidebar"], ("beside", 284))

    CLOSED = {"wide": False, "pane_open": False, "pane_channel": None, "opened_from": "list"}
    RETRY = {"id": "todo:retry@x", "kind": "retry", "home": "todo"}
    DECISION = {"id": "todo:decision@x", "kind": "decision", "home": "todo", "round": "x"}
    RUN_ASK = {"id": "step:t2/run:m1/s1@x", "kind": "question", "home": "run",
               "run": {"machine_id": "m1", "session_id": "s1", "title": "Guide"}}

    def page(self, asks, context=CLOSED, **todo):
        view = {"phase": "needs_you", "section": "needs_you", "reason": "owner_decides", "rank": 0, "asks": asks}
        row = {"view": view, "agents": [], "spaces": [], "notes": "", **todo}
        return paper({"item": {"todo": row}, "context": context})

    def asks_and_primary(self, out):
        return [e for e in out["elements"] if e in ASK_ELEMENTS], out["primary"]

    def test_the_paper_draws_no_retry_the_hub_refuses(self):
        self.assertEqual(self.asks_and_primary(self.page([self.RETRY], start_error="offline")), ([], "none"))
        with_origin = self.page([self.RETRY], start_error="offline", origin="conv_main")
        self.assertEqual(self.asks_and_primary(with_origin), (["ask_card"], "ask_card"))

    def test_the_first_ask_drawn_holds_the_primary(self):
        self.assertEqual(self.asks_and_primary(self.page([self.RUN_ASK, self.DECISION])),
                         (["ask_card", "ask_door"], "ask_door"))
        pane = dict(self.CLOSED, wide=True, pane_open=True, pane_channel="run:m1/s1")
        self.assertEqual(self.asks_and_primary(self.page([self.RUN_ASK, self.DECISION], pane)),
                         (["ask_card", "ask_pointer"], "none"))
        self.assertEqual(self.asks_and_primary(self.page([self.RETRY, self.RUN_ASK], origin="conv_main")),
                         (["ask_card", "ask_door"], "ask_card"))

    def test_values_this_version_does_not_know(self):
        newer = [dict(self.DECISION, id="todo:payment@x", kind="payment"), dict(self.RUN_ASK, home="steward")]
        self.assertEqual(self.asks_and_primary(self.page(newer)), ([], "none"))
        self.assertEqual(self.asks_and_primary(self.page([*newer, self.DECISION])), (["ask_card"], "ask_card"))
        ctx = {"item": {"todo": {"agents": [], "spaces": [], "notes": ""}}, "context": self.CLOSED}
        self.assertEqual(paper(ctx), {"page": "as_before"})
        for key, value in (("phase", "paused"), ("section", "scheduled"), ("reason", "on_hold")):
            view = {"phase": "todo", "section": "todo", "reason": "rest", "rank": 0, key: value}
            ctx["item"]["todo"]["view"] = view
            self.assertEqual(paper(ctx), {"page": "as_before"}, key)


if __name__ == "__main__":
    unittest.main()
