"""Keeps .gitattributes' merge and generated-file lines true (#10617).

merge=union lets two PRs that each add a line to the same list merge without a conflict: git keeps both sides' lines. It
never sorts them, drops a duplicate or notices a line one side deleted, so it is safe only for a list whose entries a
test compares exactly. Every merge=union line names one tracked file and has a "# guarded by <test> (<test file>)"
comment right above it, and that file defines the test, which names the list's file.

linguist-generated marks generated outputs (GitHub folds their diffs, and conflicts in them are resolved by rerunning
the generator). Every such pattern still matches a tracked file, so an output that moves is noticed here.
"""
from pathlib import Path
import os
import re
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
ATTRIBUTES = ROOT / ".gitattributes"
GUARD = re.compile(r"#\s*guarded by (\w+) \(([^()\s]+)\)\s*$")
GLOB_CHARS = re.compile(r"[*?\[\\!]")


def git(*args, cwd=ROOT):
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, timeout=60).stdout


def tracked(pattern):
    """The tracked files a .gitattributes pattern (at the repository's root) matches."""
    pattern = pattern.lstrip("/") if "/" in pattern.rstrip("/") else "**/" + pattern
    return [p for p in git("ls-files", "-z", "--", ":(glob)" + pattern).decode().split("\0") if p]


def entries(text):
    """(line number, pattern, attributes, the line right above) for each attribute line."""
    out, above = [], ""
    for number, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if line and not line.startswith("#"):
            pattern, *attributes = line.split()
            out.append((number, pattern, attributes, above))
        above = line
    return out


def definition(source, name):
    """The text of the Go func or Python def called name in source, or None: from its first line up to the next line
    indented no deeper than that one, closing brackets included."""
    start = re.search(rf"^([ \t]*)(?:func|def) {re.escape(name)}\(", source, re.M)
    if not start:
        return None
    lines = source[start.start():].splitlines()
    end = next((i for i, line in enumerate(lines[1:], 1) if line.strip() and not line.lstrip().startswith(("}", ")"))
                and len(line) - len(line.lstrip()) <= len(start.group(1))), len(lines))
    return "\n".join(lines[:end])


def generated(attributes):
    return any(a in ("linguist-generated", "linguist-generated=true") for a in attributes)


def problems(text, files, read):
    """What is wrong with the merge=union and linguist-generated lines of text. files(pattern) lists the tracked files a
    pattern matches; read(path) is a file's text, or None when it is missing."""
    found = []
    for number, pattern, attributes, above in entries(text):
        where = f".gitattributes:{number} {pattern}"
        if "merge=union" in attributes:
            named = None  # the list's file name, once the line names one tracked file
            if GLOB_CHARS.search(pattern):
                found.append(f"{where}: name one file, not a pattern: a new file must not become union-merged unseen")
            elif files(pattern) != [pattern.lstrip("/")]:
                found.append(f"{where}: no tracked file has this path: move the line with the file, or delete it")
            else:
                named = Path(pattern).name
            guard = GUARD.match(above)
            if not guard:
                found.append(f"{where}: put \"# guarded by <test> (<test file>)\" right above it, naming the test that "
                             "compares the list exactly")
            else:
                name, test_file = guard.groups()
                source = read(test_file)
                if source is None:
                    found.append(f"{where}: its guarding test file {test_file} does not exist")
                elif (body := definition(source, name)) is None:
                    found.append(f"{where}: {test_file} defines no {name}")
                elif named and named not in body:
                    found.append(f"{where}: {name} never names {named}: name the test that reads the list")
            if generated(attributes):
                found.append(f"{where}: a generated file is regenerated, not union-merged")
        if generated(attributes) and not files(pattern):
            found.append(f"{where}: the generated pattern matches no tracked file: point it at the generator's output")
    return found


def read(path):
    target = ROOT / path
    return target.read_text(encoding="utf-8") if target.is_file() else None


class GitAttributesTests(unittest.TestCase):
    def test_union_and_generated_lines_hold(self):
        self.assertEqual([], problems(ATTRIBUTES.read_text(encoding="utf-8"), tracked, read))

    def test_attributes_reach_the_files(self):
        """git itself sees each line's attribute on its files: no later line takes it back."""
        union, gen = [], []
        for _, pattern, attributes, _ in entries(ATTRIBUTES.read_text(encoding="utf-8")):
            if "merge=union" in attributes:
                union += tracked(pattern)
            if generated(attributes):
                gen += tracked(pattern)
        self.assertTrue(union and gen)
        out = git("check-attr", "-z", "merge", "linguist-generated", "--", *union, *gen).decode().split("\0")
        seen = {(path, attribute): value for path, attribute, value in zip(out[0::3], out[1::3], out[2::3])}
        self.assertEqual([], [p for p in union if seen[(p, "merge")] != "union"])
        self.assertEqual([], [p for p in gen if seen[(p, "linguist-generated")] not in ("set", "true")])
        self.assertEqual([], [p for p in union if seen[(p, "linguist-generated")] not in ("unspecified", "unset")])

    def test_problems_are_found(self):
        guard = "# guarded by TestList (list_test.go)"
        files = {"list.txt": ["list.txt"], "gen/*.go": ["gen/a.go"]}.get
        sources = {
            "list_test.go": 'func TestList(t *testing.T) {\n\treadList(t, "list.txt")\n}\n\n'
                            "func TestOther(t *testing.T) {\n\tcheck(t)\n}\n",
            "list_test.py": "class T:\n    def test_list(self):\n        read(\n            'list.txt',\n        )\n\n"
                            "    def test_other(self):\n        pass\n",
        }.get
        cases = [  # (.gitattributes text, the problems' ends)
            (f"{guard}\nlist.txt merge=union\ngen/*.go linguist-generated=true\n", []),
            ("# a comment\nlist.txt merge=union\n", ["naming the test that compares the list exactly"]),
            (f"{guard}\n\nlist.txt merge=union\n", ["naming the test that compares the list exactly"]),
            (f"{guard}\ngone.txt merge=union\n", ["move the line with the file, or delete it"]),
            (f"{guard}\n*.txt merge=union\n", ["must not become union-merged unseen"]),
            ("# guarded by TestNone (list_test.go)\nlist.txt merge=union\n", ["list_test.go defines no TestNone"]),
            ("# guarded by TestOther (list_test.go)\nlist.txt merge=union\n", ["name the test that reads the list"]),
            ("# guarded by test_list (list_test.py)\nlist.txt merge=union\n", []),
            ("# guarded by test_other (list_test.py)\nlist.txt merge=union\n", ["name the test that reads the list"]),
            ("# guarded by TestList (gone_test.go)\nlist.txt merge=union\n", ["gone_test.go does not exist"]),
            (f"{guard}\nlist.txt merge=union linguist-generated\n", ["regenerated, not union-merged"]),
            ("old/*.go linguist-generated\n", ["point it at the generator's output"]),
        ]
        for text, ends in cases:
            with self.subTest(text=text):
                found = problems(text, lambda p: files(p, []), sources)
                self.assertEqual(len(ends), len(found), found)
                for problem, end in zip(found, ends):
                    self.assertTrue(problem.endswith(end), problem)

    def test_union_merge_keeps_both_sides_lines(self):
        """Two branches that each add a line to a union-merged list next to the same place merge without a conflict."""
        lines = entries(ATTRIBUTES.read_text(encoding="utf-8"))
        number, pattern, _, _ = next(entry for entry in lines if "merge=union" in entry[2])
        with tempfile.TemporaryDirectory(prefix="gitattributes-union-") as directory:
            repo = Path(directory)
            env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
            env.update(HOME=directory, XDG_CONFIG_HOME=directory, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_ATTR_NOSYSTEM="1", GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")

            def run(*args):
                return subprocess.run(["git", *args], cwd=repo, env=env, capture_output=True, timeout=30)

            def commit(lines, message):
                (repo / pattern).write_text("".join(f"{line}\n" for line in lines))
                self.assertEqual(0, run("commit", "-qam", message).returncode)

            self.assertEqual(0, run("-c", "init.templateDir=", "init", "-q", "-b", "main").returncode)
            (repo / ".gitattributes").write_bytes(ATTRIBUTES.read_bytes())
            (repo / pattern).parent.mkdir(parents=True)
            (repo / pattern).write_text("a\nz\n")
            self.assertEqual(0, run("add", ".").returncode)
            self.assertEqual(0, run("commit", "-qm", "base").returncode)
            self.assertEqual(0, run("checkout", "-qb", "other").returncode)
            commit(["a", "n", "z"], "other adds n")
            self.assertEqual(0, run("checkout", "-q", "main").returncode)
            commit(["a", "m", "z"], "main adds m")
            merge = run("merge", "-q", "--no-edit", "other")
            self.assertEqual(0, merge.returncode, f".gitattributes:{number}: {merge.stdout!r} {merge.stderr!r}")
            self.assertEqual("a\nm\nn\nz\n", (repo / pattern).read_text())


if __name__ == "__main__":
    unittest.main()
