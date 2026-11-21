"""AGENTS.md is CLAUDE.md through a symlink (#12723).

Agents that read AGENTS.md rather than CLAUDE.md get the repository's rules from the same file, with no second copy to
drift or to conflict on every edit. The test reads what git records for the two paths, not the checkout: a checkout
without symlink support (Windows with core.symlinks off) holds a plain file whose text is the link's target, and git
still records the link, so that checkout passes the same way.
"""
from pathlib import Path
import os
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
LINK = "AGENTS.md"
TARGET = "CLAUDE.md"
SYMLINK = "120000"
REGULAR = "100644"
FIX = f"make it the symlink again: ln -sf {TARGET} {LINK} && git add {LINK}"
# A git hook exports where its repository is; the throwaway repositories below must not write there.
GIT_LOCATION = ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
                "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR")
ENV = {key: value for key, value in os.environ.items() if key not in GIT_LOCATION}


def git(root, *args, data=None):
    return subprocess.run(["git", *args], cwd=root, env=ENV, input=data, check=True, capture_output=True,
                          timeout=30).stdout


def index_entries(root, path):
    """(mode, object, stage) of each index entry git ls-files -s lists for exactly this path (one unless it is
    unmerged)."""
    found = []
    for record in git(root, "ls-files", "-s", "-z", "--", path).decode("utf-8", "surrogateescape").split("\0"):
        meta, _, name = record.partition("\t")
        if name == path:
            mode, blob, stage = meta.split()
            found.append((mode, blob, stage))
    return found


def problems(root=ROOT):
    """What is wrong with AGENTS.md and CLAUDE.md as root's index records them; empty when AGENTS.md is a symlink
    whose target is exactly CLAUDE.md and CLAUDE.md is a regular file."""
    found = []
    link = index_entries(root, LINK)
    if not link:
        found.append(f"{LINK} is not tracked: {FIX}")
    elif len(link) > 1:
        found.append(f"{LINK} is unmerged: {FIX}")
    elif link[0][0] != SYMLINK:
        found.append(f"{LINK} is recorded with mode {link[0][0]}, not as a symlink ({SYMLINK}), so it is a copy "
                     f"that drifts from {TARGET}: {FIX}")
    else:
        target = git(root, "cat-file", "-p", link[0][1])
        if target != TARGET.encode():
            found.append(f"{LINK} points to {target.decode('utf-8', 'replace')!r}, not exactly {TARGET!r}: {FIX}")
    if [mode for mode, _, _ in index_entries(root, TARGET)] != [REGULAR]:
        found.append(f"{TARGET} is not tracked as a regular file ({REGULAR}), so {LINK} points at no rules")
    return found


class AgentsMdTests(unittest.TestCase):
    def test_agents_md_is_a_symlink_to_claude_md(self):
        self.assertEqual([], problems())

    def record(self, root, path, mode, text):
        """Put path into root's index with this mode and text, whatever the working tree holds (so a symlink needs
        no filesystem support)."""
        blob = git(root, "hash-object", "-w", "--stdin", data=text.encode()).decode().strip()
        git(root, "update-index", "--add", "--cacheinfo", f"{mode},{blob},{path}")

    def repository(self, link=None, target=(REGULAR, "# Rules\n")):
        """A throwaway repository whose index holds CLAUDE.md and AGENTS.md as given ((mode, text), None for none)."""
        directory = tempfile.TemporaryDirectory(prefix="agents-md-")
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        git(root, "init", "-q")
        if target:
            self.record(root, TARGET, *target)
            (root / TARGET).write_text(target[1])
        if link:
            self.record(root, LINK, *link)
        return root

    def test_a_checkout_without_symlinks_still_passes(self):
        root = self.repository(link=(SYMLINK, TARGET))
        # What git writes when it cannot make a symlink: a plain file holding the target's name.
        (root / LINK).write_text(TARGET)
        self.assertEqual([], problems(root))

    def test_a_copy_another_target_or_no_link_is_caught(self):
        cases = {
            "a copy": ((REGULAR, "# Rules\n"), "not as a symlink"),
            "an executable file": (("100755", TARGET), "not as a symlink"),
            "a path to it": ((SYMLINK, "./" + TARGET), "not exactly"),
            "a trailing newline": ((SYMLINK, TARGET + "\n"), "not exactly"),
            "another doc": ((SYMLINK, "docs/README.md"), "not exactly"),
            "no AGENTS.md": (None, "not tracked"),
        }
        for name, (link, message) in cases.items():
            with self.subTest(name):
                found = problems(self.repository(link=link))
                self.assertEqual(1, len(found), found)
                self.assertIn(message, found[0])

    def test_an_unmerged_link_is_caught(self):
        root = self.repository()
        blob = git(root, "hash-object", "-w", "--stdin", data=TARGET.encode()).decode().strip()
        git(root, "update-index", "--index-info",
            data="".join(f"{SYMLINK} {blob} {stage}\t{LINK}\n" for stage in (1, 2, 3)).encode())
        self.assertEqual([f"{LINK} is unmerged: {FIX}"], problems(root))

    def test_a_link_to_a_missing_or_linked_claude_md_is_caught(self):
        for name, target in (("no CLAUDE.md", None), ("CLAUDE.md a symlink", (SYMLINK, "docs/README.md"))):
            with self.subTest(name):
                found = problems(self.repository(link=(SYMLINK, TARGET), target=target))
                self.assertEqual(1, len(found), found)
                self.assertIn(f"{TARGET} is not tracked as a regular file", found[0])


if __name__ == "__main__":
    unittest.main()
