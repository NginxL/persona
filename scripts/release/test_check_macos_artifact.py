"""Packaged Mac apps and DMGs retain the macOS 15.0 deployment contract (#230).

The app trees are synthetic and vtool/hdiutil are faked, so these tests need neither a signed build nor a real DMG.
Run against a built app and DMG with:

    python3 scripts/release/check_macos_artifact.py path/to/Persona.app path/to/Persona.dmg
"""
import contextlib
import io
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import check_macos_artifact as cma  # noqa: E402

UNIVERSAL_15 = [("x86_64", "15.0"), ("arm64", "15.0")]


def vtool_output(path, slices):
    """The relevant LC_BUILD_VERSION lines printed by `vtool -show-build` for each architecture."""
    lines = []
    for architecture, minimum in slices:
        lines.extend([
            path + (f" (architecture {architecture}):" if architecture else ":"),
            "Load command 11",
            "      cmd LC_BUILD_VERSION",
            "  cmdsize 32",
            " platform MACOS",
            f"    minos {minimum}",
            "      sdk 26.0",
            "   ntools 1",
            "     tool LD",
            "  version 1000.0",
        ])
    return "\n".join(lines) + "\n"


class FakeTools:
    """Answer vtool by executable name and hdiutil with a configurable result; record every invocation."""

    def __init__(self, by_name, dmg_result=None):
        self.by_name = by_name
        self.dmg_result = dmg_result or subprocess.CompletedProcess([], 0, "checksum valid\n", "")
        self.calls = []

    @staticmethod
    def answer(command, answer, stdout=""):
        if isinstance(answer, Exception):
            raise answer
        if isinstance(answer, subprocess.CompletedProcess):
            return answer
        return subprocess.CompletedProcess(command, 0, stdout, "")

    def __call__(self, command, **kwargs):
        self.calls.append(command)
        assert kwargs.get("timeout"), "every external inspection must be bounded"
        if command[:2] == ["vtool", "-show-build"]:
            path = command[-1]
            answer = self.by_name[os.path.basename(path)]
            stdout = "" if isinstance(answer, (Exception, subprocess.CompletedProcess)) else vtool_output(path, answer)
            return self.answer(command, answer, stdout)
        if command[:2] == ["hdiutil", "verify"]:
            return self.answer(command, self.dmg_result, "checksum valid\n")
        raise AssertionError(command)


class BuildVersions(unittest.TestCase):
    def test_vtool_reports_each_slice_and_does_not_confuse_the_sdk_with_the_minimum(self):
        path = "/fixture/Persona"
        fake = FakeTools({"Persona": UNIVERSAL_15})
        self.assertEqual(cma.build_versions(path, fake), UNIVERSAL_15)
        self.assertEqual(fake.calls, [["vtool", "-show-build", path]])

    def test_missing_or_failed_build_metadata_is_not_a_pass(self):
        path = "/fixture/Persona"
        for result in (
            subprocess.CompletedProcess([], 1, "", "not an object file"),
            subprocess.CompletedProcess([], 0, path + ":\n platform MACOS\n", ""),
            FileNotFoundError("vtool"),
            subprocess.TimeoutExpired(["vtool"], 30),
        ):
            with self.subTest(result=result), self.assertRaises(cma.CheckError):
                cma.build_versions(path, FakeTools({"Persona": result}))


class AppFixture(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.app = self.root / "Persona.app"

    def bundle(self, path, executable, minimum="15.0"):
        info = {"CFBundleExecutable": executable}
        if minimum is not None:
            info["LSMinimumSystemVersion"] = minimum
        contents = path / "Contents"
        (contents / "MacOS").mkdir(parents=True, exist_ok=True)
        (contents / "Info.plist").write_bytes(plistlib.dumps(info, sort_keys=True))
        binary = contents / "MacOS" / executable
        binary.write_bytes(b"synthetic fixture; vtool is injected")
        return binary

    def extension(self, minimum="15.0"):
        extension = self.app / "Contents/PlugIns/PersonaNotify.appex"
        self.bundle(extension, "PersonaNotify", minimum)
        return extension


class CheckApp(AppFixture):
    def test_universal_15_app_without_an_extension_passes(self):
        self.bundle(self.app, "Persona")
        fake = FakeTools({"Persona": UNIVERSAL_15})
        self.assertEqual(cma.check_app(str(self.app), fake), [])
        self.assertEqual(len(fake.calls), 1)  # No .appex is required by the current Electron app.

    def test_a_notification_extension_gets_the_same_checks_when_present(self):
        self.bundle(self.app, "Persona")
        extension = self.extension()
        fake = FakeTools({"Persona": UNIVERSAL_15, "PersonaNotify": UNIVERSAL_15})
        self.assertEqual(cma.check_app(str(self.app), fake), [])
        self.assertEqual(len(fake.calls), 2)
        fake = FakeTools({"Persona": UNIVERSAL_15, "PersonaNotify": [("arm64", "15.0")]})
        failures = cma.check_app(str(self.app), fake)
        self.assertTrue(any("PersonaNotify.appex" in failure and "x86_64" in failure for failure in failures), failures)
        fake = FakeTools({"Persona": UNIVERSAL_15,
                          "PersonaNotify": [("x86_64", "15.0"), ("arm64", "26.0")]})
        failures = cma.check_app(str(self.app), fake)
        self.assertTrue(any("PersonaNotify.appex" in failure and "26.0" in failure and "15.0" in failure
                            for failure in failures), failures)
        self.bundle(extension, "PersonaNotify", "26.0")
        fake = FakeTools({"Persona": UNIVERSAL_15, "PersonaNotify": UNIVERSAL_15})
        failures = cma.check_app(str(self.app), fake)
        self.assertTrue(any("PersonaNotify.appex" in failure and "LSMinimumSystemVersion" in failure
                            for failure in failures), failures)

    def test_info_plist_minimum_is_exactly_15(self):
        for minimum in (None, "14.0", "15.1", "26.0"):
            with self.subTest(minimum=minimum):
                self.bundle(self.app, "Persona", minimum)
                failures = cma.check_app(str(self.app), FakeTools({"Persona": UNIVERSAL_15}))
                self.assertTrue(any("LSMinimumSystemVersion" in failure and "15.0" in failure
                                    for failure in failures), failures)

    def test_both_real_architectures_are_required(self):
        self.bundle(self.app, "Persona")
        for slices, missing in (
            ([("arm64", "15.0")], "x86_64"),
            ([("x86_64", "15.0")], "arm64"),
            ([("x86_64", "15.0"), ("arm64e", "15.0")], "arm64"),
        ):
            with self.subTest(slices=slices):
                failures = cma.check_app(str(self.app), FakeTools({"Persona": slices}))
                self.assertTrue(any(missing in failure for failure in failures), failures)

    def test_every_slice_has_a_15_minimum(self):
        self.bundle(self.app, "Persona")
        for slices, actual_minimum in (
            ([("x86_64", "14.0"), ("arm64", "15.0")], "14.0"),
            ([("x86_64", "15.0"), ("arm64", "26.0")], "26.0"),
        ):
            with self.subTest(slices=slices):
                failures = cma.check_app(str(self.app), FakeTools({"Persona": slices}))
                self.assertTrue(any(actual_minimum in failure and "15.0" in failure
                                    for failure in failures), failures)

    def test_an_uninspectable_binary_fails_closed(self):
        self.bundle(self.app, "Persona")
        result = subprocess.CompletedProcess([], 1, "", "truncated or malformed object")
        with self.assertRaises(cma.CheckError):
            cma.check_app(str(self.app), FakeTools({"Persona": result}))


class CheckDMG(AppFixture):
    def test_hdiutil_verify_must_accept_the_image(self):
        image = self.root / "Persona.dmg"
        image.write_bytes(b"synthetic image; hdiutil is injected")
        fake = FakeTools({})
        self.assertIsNone(cma.check_dmg(str(image), fake))
        self.assertEqual(fake.calls, [["hdiutil", "verify", str(image)]])

    def test_dmg_tool_failure_is_not_a_pass(self):
        image = self.root / "Persona.dmg"
        image.write_bytes(b"synthetic image; hdiutil is injected")
        for result in (
            subprocess.CompletedProcess([], 1, "", "checksum mismatch"),
            FileNotFoundError("hdiutil"),
            subprocess.TimeoutExpired(["hdiutil"], 120),
        ):
            with self.subTest(result=result), self.assertRaises(cma.CheckError):
                cma.check_dmg(str(image), FakeTools({}, dmg_result=result))


class Main(AppFixture):
    def test_cli_can_check_a_real_app_and_dmg_without_signing_or_mounting(self):
        self.bundle(self.app, "Persona")
        image = self.root / "Persona.dmg"
        image.write_bytes(b"synthetic image; hdiutil is injected")
        fake = FakeTools({"Persona": UNIVERSAL_15})
        output = io.StringIO()
        with patch.object(cma.subprocess, "run", fake), contextlib.redirect_stdout(output):
            code = cma.main(["check_macos_artifact.py", str(self.app), str(image)])
        self.assertEqual(code, 0)
        self.assertEqual(len(fake.calls), 2)
        self.assertIn("Persona.app", output.getvalue())
        self.assertIn("Persona.dmg", output.getvalue())

    def test_cli_returns_nonzero_for_a_deployment_regression(self):
        self.bundle(self.app, "Persona", "13.0")
        fake = FakeTools({"Persona": [("x86_64", "13.0"), ("arm64", "13.0")]})
        with patch.object(cma.subprocess, "run", fake), contextlib.redirect_stdout(io.StringIO()), \
                contextlib.redirect_stderr(io.StringIO()):
            code = cma.main(["check_macos_artifact.py", str(self.app)])
        self.assertEqual(code, 1)


if __name__ == "__main__":
    unittest.main()
