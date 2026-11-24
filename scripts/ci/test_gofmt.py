"""scripts/ci/gofmt.py takes directories, as CI's Go formatting step passes them, and single files, as check-fast's gofmt
job passes the changed ones (#12894). The cases build a throwaway tree; the gofmt runs need gofmt on PATH."""
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import gofmt  # noqa: E402

FORMATTED = "package foo\n\nfunc F() int {\n\treturn 1\n}\n"
UNFORMATTED = "package foo\n\nfunc F() int {\nreturn 1\n}\n"


class GoFmtTestCase(unittest.TestCase):
    def setUp(self):
        self.tree = Path(tempfile.mkdtemp(prefix="gofmt-test-"))
        self.addCleanup(shutil.rmtree, self.tree)

    def write(self, path, text=FORMATTED):
        file = self.tree / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)
        return str(file)


class PathTests(GoFmtTestCase):
    def test_a_directory_is_every_go_file_below_it_testdata_included(self):
        a = self.write("server/a.go")
        b = self.write("server/internal/foo/testdata/b.go")
        self.write("server/notes.md")
        self.assertEqual(([a, b], []), gofmt.go_files([str(self.tree / "server")]))

    def test_a_file_is_itself_and_each_file_is_listed_once(self):
        a = self.write("server/internal/foo/a.go")
        self.write("server/internal/foo/other.go")
        self.assertEqual(([a], []), gofmt.go_files([a, a]))
        self.assertEqual(([a, str(self.tree / "server/internal/foo/other.go")], []),
                         gofmt.go_files([a, str(self.tree / "server/internal/foo")]))

    def test_a_path_that_is_neither_is_reported(self):
        gone = str(self.tree / "server/gone.go")
        self.assertEqual(([], [gone]), gofmt.go_files([gone]))

    def test_gofmt_runs_on_a_hundred_files_at_a_time(self):
        names = [self.write("server/f%03d.go" % n) for n in range(250)]
        calls = []

        def run(argv, **_):
            calls.append(argv)
            return subprocess.CompletedProcess(argv, 0, "", "")

        with mock.patch.object(gofmt.subprocess, "run", run):
            self.assertEqual(0, gofmt.main(names))
        self.assertEqual([100, 100, 50], [len(argv) - 2 for argv in calls])
        self.assertEqual(sorted(names), [name for argv in calls for name in argv[2:]])


@unittest.skipUnless(shutil.which("gofmt"), "gofmt not found (it comes with Go)")
class GoFmtRunTests(GoFmtTestCase):
    def run_script(self, *paths):
        run = subprocess.run([sys.executable, str(Path(gofmt.__file__)), *paths], cwd=self.tree, capture_output=True,
                             text=True, timeout=60)
        return run.returncode, run.stdout, run.stderr

    def test_formatted_files_pass(self):
        self.write("server/a.go")
        self.assertEqual((0, "", ""), self.run_script("server/a.go"))
        self.assertEqual((0, "", ""), self.run_script("server"))

    def test_an_unformatted_file_is_listed_and_fails(self):
        self.write("server/a.go")
        self.write("server/b.go", UNFORMATTED)
        listed = "gofmt: needs formatting (run gofmt -w): server/b.go\n"
        code, out, _ = self.run_script("server/a.go", "server/b.go")
        self.assertEqual((1, listed), (code, out))
        code, out, _ = self.run_script("server")
        self.assertEqual((1, listed), (code, out))

    def test_a_named_file_that_is_gone_fails(self):
        self.write("server/a.go")
        code, out, err = self.run_script("server/a.go", "server/gone.go")
        self.assertEqual(1, code)
        self.assertIn("no such file or directory: server/gone.go", err)


if __name__ == "__main__":
    unittest.main()
