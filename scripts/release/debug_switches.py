#!/usr/bin/env python3
"""The DEBUG-only names that must not be in a Release build of the iPhone app, looked for in its binary's `strings`.

Two rules, over the whole `strings` output:

- The DEBUG-only launch arguments (simulator switches): a line equal to a name in debug_switches.txt (#10616). The file
  lists them one name per line, sorted and without duplicates, so a change that adds a switch adds one line instead of
  editing a regex every other pull request edits too. .gitattributes merges the file with merge=union, which keeps the
  lines both sides added but can also bring back a name another branch removed: check union merges that delete names.
- The runtime CLI (apple/Persona/Debug/, docs/operations/runtime-cli.md): a line containing one of RUNTIME_CLI's names,
  its switches, its type and the accessibility hooks it turns on (#9665).

A match means DEBUG code got into the build, or a property, an enum case or a literal of the same name (which
scripts/test/test_apple_release_switch_names.py looks for in the sources), and release.sh stops before the upload.
The rules read all of the output on purpose: under pipefail, `if strings … | grep -q …` passed when the match came
early, because grep stops reading there and `strings`, killed by SIGPIPE, fails the pipeline (#10654,
docs/lessons/workflow.md).

  python3 scripts/release/debug_switches.py check path/to/Persona.app/Persona   # exit 1 when a rule matches
  python3 scripts/release/debug_switches.py check --strings strings.txt          # the same on saved `strings` output
  python3 scripts/release/debug_switches.py sort                                  # sort the list, drop duplicates

The list is kept by hand, not read from the Swift sources: the app reads its switches in many shapes (`forKey:` on
UserDefaults or on a local alias of it, arrays of keys, ProcessInfo's arguments), its DEBUG code also reads settings
keys that do ship, and the list keeps retired switches no source names any more.
"""
import argparse
import re
import subprocess
import sys
from pathlib import Path

LIST = Path(__file__).resolve().with_name("debug_switches.txt")
NAME = re.compile(r"[A-Za-z][A-Za-z0-9_]*")
MESSAGE = "DEBUG switches found in the Release binary"
# Matched anywhere in a line, as release.sh's `grep -qE` did before #10654. No alternative can span a newline, so
# searching the whole output at once finds what a search line by line would.
RUNTIME_CLI = re.compile(rb"runtimeCLI|RuntimeCLI|showActionTray|openNeedsRow|openPushIntercepts"
                         rb"|_AXSSet(?:Automation|ReduceMotion)Enabled")
RUNTIME_MESSAGE = "DEBUG runtime or snapshot switches found in the Release binary"


def is_comment_or_blank(line: str) -> bool:
    stripped = line.strip()
    return not stripped or stripped.startswith("#")


def split(text: str):
    """(header, names): the lines before the first name, comments and blank lines kept as they are, and the names in
    file order. Below the header every line must be a name with nothing around it, so `sort` changes nothing but the
    order: a comment or a blank line there raises ValueError (sort could not keep it in its place), and so does any
    other line that is not a name (a typo or a merge conflict marker must not quietly match nothing)."""
    header, names = [], []
    for number, line in enumerate(text.splitlines(), 1):
        if NAME.fullmatch(line):
            names.append(line)
        elif not names and is_comment_or_blank(line):
            header.append(line)
        elif is_comment_or_blank(line):
            what = "a comment" if line.strip() else "a blank line"
            raise ValueError(f"line {number}: {what} below the first name (comments go in the header; "
                             "no blank lines between names)")
        else:
            raise ValueError(f"line {number}: {line!r} is not a switch name (letters, digits and _)")
    return header, names


def names(path: Path = LIST) -> list:
    return split(path.read_text(encoding="utf-8"))[1]


def problems(text: str) -> list:
    """What keeps the list from being well formed, sorted (byte order, as LC_ALL=C sort) and free of duplicates. A list
    with no problems is one `sort` leaves as it is."""
    try:
        listed = split(text)[1]
    except ValueError as error:
        return [str(error)]
    found = []
    seen = set()
    for previous, name in zip([None] + listed, listed):
        if name in seen:
            found.append(f"{name} is listed twice")
        elif previous is not None and name < previous:
            found.append(f"{name} comes before {previous}")
        seen.add(name)
    if text and not text.endswith("\n"):
        found.append("the last line has no newline")
    return found


def sort_text(text: str) -> str:
    header, listed = split(text)
    return "\n".join(header + sorted(set(listed))) + "\n"


def found(strings_output: bytes, switches) -> list:
    """The switches equal to a whole line of `strings` output, as `grep -x` matches lines."""
    lines = set(strings_output.split(b"\n"))
    return sorted(name for name in set(switches) if name.encode() in lines)


def runtime_found(strings_output: bytes) -> list:
    """The runtime CLI's names that appear in `strings` output, each once."""
    return sorted({match.group(0).decode() for match in RUNTIME_CLI.finditer(strings_output)})


def check(binary: str = None, strings_file: str = None, path: Path = LIST) -> int:
    switches = names(path)
    if not switches:
        print(f"{path}: no DEBUG switches listed", file=sys.stderr)
        return 2
    if strings_file:
        output = Path(strings_file).read_bytes()
    else:
        # Read to the end: see the module docstring for what `strings | grep -q` missed.
        run = subprocess.run(["strings", binary], stdout=subprocess.PIPE)
        if run.returncode:
            print(f"strings failed on {binary} (exit {run.returncode})", file=sys.stderr)
            return 2
        output = run.stdout
    status = 0
    for message, hits in ((MESSAGE, found(output, switches)), (RUNTIME_MESSAGE, runtime_found(output))):
        if hits:
            print(message, file=sys.stderr)
            for name in hits:
                print(f"  {name}", file=sys.stderr)
            status = 1
    return status


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--list", type=Path, default=LIST, help="the list of switches (default: debug_switches.txt)")
    commands = parser.add_subparsers(dest="command", required=True)
    checking = commands.add_parser("check", help="fail when a switch is a line of the binary's strings, or a line "
                                                 "holds a runtime CLI name")
    source = checking.add_mutually_exclusive_group(required=True)
    source.add_argument("binary", nargs="?", help="the Release binary, e.g. Persona.app/Persona")
    source.add_argument("--strings", dest="strings_file", help="saved `strings` output to read instead")
    commands.add_parser("sort", help="sort the list and drop duplicates, keeping its header")
    args = parser.parse_args(argv)
    try:
        if args.command == "sort":
            text = args.list.read_text(encoding="utf-8")
            args.list.write_text(sort_text(text), encoding="utf-8")
            return 0
        return check(args.binary, args.strings_file, args.list)
    except ValueError as error:
        print(f"{args.list}: {error}", file=sys.stderr)
        return 2
    except OSError as error:
        print(error, file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
