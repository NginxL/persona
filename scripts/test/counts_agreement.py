#!/usr/bin/env python3
"""The counts agreement report: what each client's fixture replay counted, compared case by case (#11900).

docs/plans/plan-derived-counts.md (section 3.5) has each client replay the shared count fixtures through the functions
its surfaces call. With the report switch on (spec/fixtures/counts/README.md, "Lane reports"), a replay writes one
JSON line per case and surface to <directory>/<lane>.jsonl:

    {"lane": "android", "fixture": "needs-you", "case": "<case name>", "surface": "agents_tab", "value": 1,
     "keys": [{"id": "agent:<machine>/<session>", "cards": ["<card id>"]}]}

This reads those reports beside the fixture and the table's `replays` (spec/fixtures/counts/surfaces.json: which
lanes report on a fixture, and which of a case's expected keys hold each surface's number and rows). Each lane's value
for a case and surface is:

- agrees: it is the fixture's;
- a known difference: it is what the lane's mark in the case says the lane counts today
  (`known_differences.<lane>.replace`, spec/fixtures/needs-you/README.md "Known differences"), naming the decision;
- unexplained: anything else, or the fixture's value on a case whose mark says otherwise (a stale mark).

A report that misses a case or surface its lane replays, names one the table does not, says it twice or is malformed
fails too. Lanes with no report are listed as not run; --require-all fails on them.

    python3 scripts/test/counts_agreement.py [--require-all] [--json out.json] <report.jsonl | directory> ...

Exit codes: 0 every value agrees or is a known difference, 1 something is unexplained, 2 the reports could not be read.
"""
import argparse
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
TABLE = ROOT / "spec" / "fixtures" / "counts" / "surfaces.json"
FIELDS = {"lane", "fixture", "case", "surface", "value", "keys"}


class ReportError(Exception):
    """A report or the table could not be read."""


def report_files(arguments):
    """The lane report files named, a directory standing for the *.jsonl files in it."""
    files = []
    for argument in arguments:
        path = Path(argument)
        if path.is_dir():
            files += sorted(path.glob("*.jsonl"))
        elif path.is_file():
            files.append(path)
        else:
            raise ReportError(f"{argument}: no such report or directory")
    return files


def read_reports(files):
    """(rows, problems): every JSON line of the reports, and a line for each one that is not a report row."""
    rows, problems = [], []
    for file in files:
        for number, line in enumerate(Path(file).read_text(encoding="utf-8").splitlines(), 1):
            if not line.strip():
                continue
            try:
                row = json.loads(line)
            except ValueError as error:
                problems.append(f"{file}:{number}: not JSON ({error})")
                continue
            if not isinstance(row, dict) or set(row) != FIELDS:
                problems.append(f"{file}:{number}: a row has exactly {', '.join(sorted(FIELDS))}")
            elif not isinstance(row["value"], int) or isinstance(row["value"], bool) or not keys_valid(row["keys"]):
                problems.append(f"{file}:{number}: value is a number and keys a list of {{id, cards}}")
            else:
                rows.append(row)
    return rows, problems


def keys_valid(keys):
    return isinstance(keys, list) and all(
        isinstance(key, dict) and set(key) == {"id", "cards"} and isinstance(key["id"], str)
        and isinstance(key["cards"], list) and all(isinstance(card, str) for card in key["cards"]) for key in keys)


def normal(keys):
    return [(key["id"], list(key["cards"])) for key in keys]


def held(expected, read):
    """A surface's (value, keys) in a case's expected, by the keys the table names: a list's value is its length."""
    value, keys = expected.get(read["value"]), expected.get(read["keys"])
    if isinstance(value, list):
        value = len(value)
    if not isinstance(value, int) or not keys_valid(keys):
        raise ReportError(f"expected.{read['value']} and expected.{read['keys']} do not hold a count and its rows")
    return value, normal(keys)


def fixture_cases(root, fixture):
    directory = root / "spec" / "fixtures" / fixture
    files = sorted(directory.glob("*.json")) if directory.is_dir() else []
    if not files:
        raise ReportError(f"spec/fixtures/{fixture} has no cases")
    return [case for path in files for case in json.loads(path.read_text(encoding="utf-8"))["cases"]]


def compare(table, rows, root=ROOT, require_all=False):
    """The agreement of the lanes' rows with the fixtures: {"fixtures": {...}, "problems": [...]}."""
    problems, result = [], {}
    replays = table.get("replays") or {}
    by_fixture = {}
    for row in rows:
        replay = replays.get(row["fixture"])
        if replay is None:
            problems.append(f"{row['lane']}: reports on {row['fixture']}, which the table's replays do not list")
        elif row["lane"] not in replay["lanes"]:
            problems.append(f"{row['lane']}: does not replay {row['fixture']} in the table")
        elif row["surface"] not in replay["surfaces"]:
            problems.append(f"{row['lane']}: reports {row['surface']} of {row['fixture']}, which the table does not")
        else:
            by_fixture.setdefault(row["fixture"], []).append(row)
    for fixture, replay in replays.items():
        reported = by_fixture.get(fixture, [])
        lanes = sorted({row["lane"] for row in reported})
        missing = [lane for lane in replay["lanes"] if lane not in lanes]
        if require_all and missing:
            problems.append(f"{fixture}: no report from {', '.join(missing)}")
        if not reported:
            result[fixture] = {"lanes": [], "not_run": missing, "rows": []}
            continue
        cases = fixture_cases(root, fixture)
        names = {case["name"] for case in cases}
        got = {}
        for row in reported:
            key = (row["lane"], row["case"], row["surface"])
            if row["case"] not in names:
                problems.append(f"{row['lane']}: reports {row['case']}, which {fixture} does not have")
            elif key in got:
                problems.append(f"{row['lane']}: reports {row['case']} {row['surface']} twice")
            else:
                got[key] = (row["value"], normal(row["keys"]))
        lines = []
        for case in cases:
            marks = case.get("known_differences") or {}
            for surface, read in replay["surfaces"].items():
                want = held(case["expected"], read)
                line = {"case": case["name"], "surface": surface, "fixture": {"value": want[0], "keys": want[1]},
                        "lanes": {}}
                for lane in lanes:
                    mark = marks.get(lane)
                    today = held({**case["expected"], **mark.get("replace", {})}, read) if mark else want
                    seen = got.get((lane, case["name"], surface))
                    if seen is None:
                        verdict = "missing"
                        problems.append(f"{lane}: no result for {case['name']} {surface}")
                    elif seen == want and today == want:
                        verdict = "agrees"
                    elif seen == today:
                        verdict = "known"
                    elif seen == want:
                        verdict = "stale mark"
                        problems.append(f"{lane}: {case['name']} {surface} follows the fixture, but its mark "
                                        f"({mark['decision']}) says otherwise: remove the mark")
                    else:
                        verdict = "unexplained"
                        problems.append(f"{lane}: {case['name']} {surface} is {seen[0]}, the fixture's {want[0]}"
                                        + (f", its mark's {today[0]}" if mark else "") + (
                                            "" if seen[0] != want[0] else " (same number, other rows)"))
                    entry = {"verdict": verdict}
                    if seen is not None:
                        entry.update(value=seen[0], keys=seen[1])
                    if verdict == "known":
                        entry["decision"] = mark["decision"]
                    line["lanes"][lane] = entry
                lines.append(line)
        result[fixture] = {"lanes": lanes, "not_run": missing, "rows": lines}
    return {"fixtures": result, "problems": problems}


def render(result):
    """The agreement report as text lines."""
    out = []
    for fixture, part in result["fixtures"].items():
        lanes = part["lanes"]
        out.append(f"{fixture}: lanes {', '.join(lanes) or 'none'}"
                   + (f"; not run: {', '.join(part['not_run'])}" if part["not_run"] else ""))
        if not lanes:
            continue
        width = max(len(line["case"]) for line in part["rows"])
        surface_width = max(len(line["surface"]) for line in part["rows"])
        out.append((f"  {'case':<{width}}  {'surface':<{surface_width}}  {'fixture':>7}  "
                    + "  ".join(f"{lane:<16}" for lane in lanes)).rstrip())
        for line in part["rows"]:
            cells = []
            for lane in lanes:
                entry = line["lanes"][lane]
                if entry["verdict"] == "missing":
                    cells.append("missing")
                    continue
                note = {"agrees": "", "known": f" known {entry.get('decision')}", "stale mark": " stale mark",
                        "unexplained": " UNEXPLAINED"}[entry["verdict"]]
                cells.append(f"{entry['value']}{note}")
            out.append(f"  {line['case']:<{width}}  {line['surface']:<{surface_width}}  "
                       f"{line['fixture']['value']:>7}  " + "  ".join(f"{cell:<16}" for cell in cells).rstrip())
        for lane in lanes:
            verdicts = [line["lanes"][lane]["verdict"] for line in part["rows"]]
            known = sorted({f"{line['case']} ({line['lanes'][lane]['decision']})" for line in part["rows"]
                            if line["lanes"][lane]["verdict"] == "known"})
            out.append(f"  {lane}: {verdicts.count('agrees')} agree, {verdicts.count('known')} known difference(s)"
                       + (f" ({'; '.join(known)})" if known else "")
                       + f", {len(verdicts) - verdicts.count('agrees') - verdicts.count('known')} unexplained")
        for index, first in enumerate(lanes):
            for second in lanes[index + 1:]:
                apart = sorted({line["case"] for line in part["rows"]
                                if line["lanes"][first].get("value") != line["lanes"][second].get("value")
                                or line["lanes"][first].get("keys") != line["lanes"][second].get("keys")})
                out.append(f"  {first} and {second} differ on {len(apart)} case(s)"
                           + (": " + ", ".join(apart) if apart else ""))
    out.append("")
    if result["problems"]:
        out.append(f"FAIL {len(result['problems'])} unexplained:")
        out += [f"  {problem}" for problem in result["problems"]]
    else:
        out.append("OK every lane agrees with the fixtures, outside their known differences")
    return out


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("reports", nargs="+", help="lane reports (<lane>.jsonl), or directories holding them")
    parser.add_argument("--table", default=str(TABLE), help="the surface table (default: %(default)s)")
    parser.add_argument("--json", help="also write the comparison as JSON here")
    parser.add_argument("--require-all", action="store_true", help="fail when a lane the table names sent no report")
    args = parser.parse_args(argv)
    try:
        table = json.loads(Path(args.table).read_text(encoding="utf-8"))
        rows, problems = read_reports(report_files(args.reports))
        result = compare(table, rows, require_all=args.require_all)
    except (ReportError, OSError, ValueError, KeyError) as error:
        print(f"counts agreement: {error}", file=sys.stderr)
        return 2
    result["problems"] = problems + result["problems"]
    if args.json:
        Path(args.json).write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print("\n".join(render(result)))
    return 1 if result["problems"] else 0


if __name__ == "__main__":
    sys.exit(main())
