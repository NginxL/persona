"""go_trimpath.sh: GOFLAGS with -trimpath added, keeping the caller's flags (#10614)."""
import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/test/go_trimpath.sh"
SH = shutil.which("sh") or "/bin/sh"
# A fake go answers `go env GOFLAGS` as the real one does: the environment's value, else (unset or empty) the go env
# file's.
FAKE_GO = '#!/bin/sh\n[ "$*" = "env GOFLAGS" ] || exit 2\nprintf "%s\\n" "${GOFLAGS:-$FAKE_GO_ENV_FILE}"\n'
# The Python copies (the PR watcher, the server test launcher) follow the same rule.
COPIES = {"pr_watch_common": ROOT / "scripts/operations/pr_watch_common.py",
          "server_test_env": ROOT / "scripts/test/server_test_env.py"}
CASES = (
    ("", "-trimpath"),
    ("-mod=readonly", "-mod=readonly -trimpath"),
    ("-gcflags=all=-dwarf=false -ldflags=-w", "-gcflags=all=-dwarf=false -ldflags=-w -trimpath"),
    ("-trimpath", "-trimpath"),
    ("-mod=mod --trimpath", "-mod=mod --trimpath"),
    ("-trimpath=true -mod=mod", "-trimpath=true -mod=mod"),
    ("-trimpath=false", "-trimpath=false"),  # an explicit opt-out stays
    ("-tags=trimpathx", "-tags=trimpathx -trimpath"),
)


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class GoTrimpathTests(unittest.TestCase):
    def setUp(self):
        self.bin = Path(tempfile.mkdtemp(prefix="go-trimpath-test-"))
        self.addCleanup(shutil.rmtree, self.bin)
        (self.bin / "go").write_text(FAKE_GO)
        (self.bin / "go").chmod(0o755)

    def run_script(self, path, **env):
        environment = {key: value for key, value in os.environ.items() if key not in ("GOFLAGS", "FAKE_GO_ENV_FILE")}
        environment.update(PATH=path, **env)
        result = subprocess.run([SH, str(SCRIPT)], env=environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.rstrip("\n")

    def test_adds_trimpath_to_the_environment_value(self):
        for value, expected in CASES:
            with self.subTest(value=value):
                self.assertEqual(self.run_script(str(self.bin), GOFLAGS=value), expected)

    def test_a_go_env_file_value_is_kept(self):
        # `go env -w GOFLAGS=...` counts as user-set: an exported GOFLAGS would otherwise replace it.
        self.assertEqual(self.run_script(str(self.bin)), "-trimpath")
        self.assertEqual(self.run_script(str(self.bin), FAKE_GO_ENV_FILE="-mod=mod"), "-mod=mod -trimpath")
        self.assertEqual(self.run_script(str(self.bin), FAKE_GO_ENV_FILE="-trimpath"), "-trimpath")
        # An empty GOFLAGS counts as unset: go reports the env file's value.
        self.assertEqual(self.run_script(str(self.bin), GOFLAGS="", FAKE_GO_ENV_FILE="-mod=mod"), "-mod=mod -trimpath")

    def test_without_go_the_environment_value_is_the_base(self):
        empty = self.bin / "empty"
        empty.mkdir()
        self.assertEqual(self.run_script(str(empty), GOFLAGS="-mod=readonly"), "-mod=readonly -trimpath")
        self.assertEqual(self.run_script(str(empty)), "-trimpath")

    def test_flags_are_not_glob_expanded(self):
        (self.bin / "-trimpath").write_text("")  # a file a glob would match must not count as the flag
        environment = {key: value for key, value in os.environ.items() if key != "GOFLAGS"}
        environment.update(PATH=str(self.bin), GOFLAGS="-*")
        result = subprocess.run([SH, str(SCRIPT)], cwd=self.bin, env=environment, capture_output=True, text=True,
                                timeout=30)
        self.assertEqual(result.stdout, "-* -trimpath\n", result.stderr)

    @unittest.skipUnless(shutil.which("go"), "needs go")
    def test_real_go_reports_the_environment_value(self):
        self.assertEqual(self.run_script(os.environ["PATH"], GOFLAGS="-mod=readonly"), "-mod=readonly -trimpath")

    def test_python_copies_follow_the_same_rule(self):
        for name, path in COPIES.items():
            module = load(name, path)
            for value, expected in CASES:
                with self.subTest(copy=name, value=value):
                    self.assertEqual(module.with_trimpath(value), expected)


if __name__ == "__main__":
    unittest.main()
