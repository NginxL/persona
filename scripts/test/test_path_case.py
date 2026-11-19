"""No two tracked paths may differ only by letter case (#6239).

On a case-insensitive file system, the default on macOS, two such paths are one file: every checkout writes one over the
other, `git status` always shows one of them as modified, and rebases and checkouts stop on it.
"""
from pathlib import Path
import os
import subprocess
import unittest


def _source_root():
    configured = os.environ.get("PERSONA_CI_SOURCE_ROOT")
    if configured:
        if not Path(configured).is_absolute():
            raise ValueError("PERSONA_CI_SOURCE_ROOT must be absolute")
        return Path(configured)
    return Path(__file__).resolve().parents[2]


ROOT = _source_root()


def case_clashes(paths):
    """The pairs among paths that are equal once case is folded."""
    seen, clashes = {}, []
    for path in paths:
        key = path.casefold()
        if key in seen and seen[key] != path:
            clashes.append((seen[key], path))
        else:
            seen.setdefault(key, path)
    return clashes


class PathCaseTest(unittest.TestCase):
    def test_case_clashes(self):
        self.assertEqual(case_clashes(["docs/a.md", "docs/b.md"]), [])
        self.assertEqual(case_clashes(["docs/README.md", "docs/readme.md"]), [("docs/README.md", "docs/readme.md")])

    def test_no_tracked_paths_differ_only_by_case(self):
        out = subprocess.run(["git", "ls-files", "-z"], cwd=ROOT, capture_output=True, check=True).stdout
        paths = [p.decode("utf-8", "surrogateescape") for p in out.split(b"\0") if p]
        clashes = case_clashes(paths)
        self.assertEqual(clashes, [], "paths that differ only by case are one file on macOS: rename one of each pair")


if __name__ == "__main__":
    unittest.main()
