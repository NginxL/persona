#!/usr/bin/env python3
"""The Release archive keeps DEBUG runtime and snapshot facilities out (#9665), wherever in the binary's `strings` they
appear (#10654): scripts/release/debug_switches.py reads all of the output, where `strings | grep -q` under pipefail
passed on an early match."""
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/release/release.sh"
sys.path.insert(0, str(ROOT / "scripts/release"))
import debug_switches as ds

# The runtime CLI's switches, its type and the accessibility hooks it turns on (docs/operations/runtime-cli.md).
RUNTIME_NAMES = ("runtimeCLI", "RuntimeCLI", "showActionTray", "openNeedsRow", "openPushIntercepts",
                 "_AXSSetAutomationEnabled", "_AXSSetReduceMotionEnabled")
# Far more than a pipe holds (64 KiB on macOS and Linux), so a reader that stops at the first match leaves the writer
# blocked on a full pipe until it is killed.
FILLER = b"".join(b"harmlessString%06d\n" % n for n in range(200_000))


def release_check_command() -> str:
    """release.sh's own line that runs the check, to exercise it rather than a copy."""
    lines = [line for line in SCRIPT.read_text(encoding="utf-8").splitlines()
             if line.startswith("python3 scripts/release/debug_switches.py check ")]
    if len(lines) != 1:
        raise AssertionError(f"release.sh should run debug_switches.py check once, found {lines}")
    return lines[0]


class ReleaseDebugGuardTest(unittest.TestCase):
    def test_archive_checks_runtime_and_snapshot_switches(self):
        self.assertEqual(release_check_command(), 'python3 scripts/release/debug_switches.py check "$APP/Persona"')
        self.assertEqual(ds.RUNTIME_MESSAGE, "DEBUG runtime or snapshot switches found in the Release binary")
        for name in RUNTIME_NAMES:
            with self.subTest(name=name):
                self.assertEqual(ds.runtime_found(b"x" + name.encode() + b"y\n"), [name])

    def test_no_strings_output_is_piped_into_an_early_exit(self):
        # Under pipefail, grep -q stopping at a match kills `strings` with SIGPIPE and the `if` reads that as clean.
        text = SCRIPT.read_text(encoding="utf-8")
        self.assertIsNone(re.search(r"^[^#\n]*\bstrings\b[^\n|]*\|\s*grep\s+-[A-Za-z]*q", text, re.MULTILINE))

    def test_a_runtime_name_anywhere_in_a_line_fails_and_near_misses_pass(self):
        output = b"_$s7Persona10RuntimeCLIV3runyyFZ\nRuntimeCLI+Hub.swift\nruntimeCL\nAXSSetAutomationEnabled\n"
        self.assertEqual(ds.runtime_found(output), ["RuntimeCLI"])
        self.assertEqual(ds.runtime_found(b"runtimeCL\nshowAction\n_AXSSetEnabled\n"), [])

    def test_a_match_on_the_first_line_of_a_long_output_fails(self):
        for name in ("runtimeCLI", "_AXSSetAutomationEnabled"):
            with self.subTest(name=name), tempfile.TemporaryDirectory(prefix="persona-release-debug-guard-") as tmp:
                strings = Path(tmp, "strings.txt")
                strings.write_bytes(name.encode() + b"\n" + FILLER)
                result = subprocess.run([sys.executable, str(ds.__file__), "check", "--strings", str(strings)],
                                        capture_output=True, text=True, timeout=60)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(result.stderr.splitlines(), [ds.RUNTIME_MESSAGE, f"  {name}"])

    @unittest.skipUnless(shutil.which("strings"), "needs strings")
    def test_the_release_command_rejects_an_early_match_and_allows_a_clean_binary(self):
        # Release's own command under its own shell options, through the real `strings`: a runtime name first, then
        # far more output than a pipe holds.
        command = "set -euo pipefail\n" + release_check_command().replace("python3", shlex.quote(sys.executable), 1)
        with tempfile.TemporaryDirectory(prefix="persona-release-debug-guard-") as tmp:
            binary = Path(tmp, "Persona")
            env = {"APP": tmp, "PATH": "/usr/bin:/bin"}
            binary.write_bytes(b"\x00runtimeCLI\x00\x01" + FILLER.replace(b"\n", b"\x00"))
            contaminated = subprocess.run(["bash", "-c", command], cwd=ROOT, env=env, capture_output=True, text=True,
                                          timeout=120)
            self.assertEqual(contaminated.returncode, 1, contaminated.stderr)
            self.assertEqual(contaminated.stderr.splitlines(), [ds.RUNTIME_MESSAGE, "  runtimeCLI"])
            binary.write_bytes(b"\x00an ordinary production executable\x00\x01" + FILLER.replace(b"\n", b"\x00"))
            clean = subprocess.run(["bash", "-c", command], cwd=ROOT, env=env, capture_output=True, text=True,
                                   timeout=120)
            self.assertEqual((clean.returncode, clean.stderr), (0, ""))


if __name__ == "__main__":
    unittest.main()
