"""entitlements.py check and the apps' entitlements: time-sensitive notifications on the iPhone and
Apple Vision Pro (#1572, #1655, #2435), Siri only with app.siri_calls, and no binary linking CallKit (#9290).

    python3 scripts/release/test_entitlements.py
"""
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

HERE = Path(__file__).resolve().parent
APPLE = HERE.parent.parent / "apple"
sys.path.insert(0, str(HERE))
import entitlements as ents  # noqa: E402

BUNDLE = "com.example.persona"


class AppsAskForTimeSensitive(unittest.TestCase):
    """The entitlements files xcodegen writes from project.yml (tracked): without the key the export drops it and a push
    the hub marks time-sensitive shows as active, under Focus too."""

    def entitlements(self, path):
        with open(APPLE / path, "rb") as f:
            return plistlib.load(f)

    def test_the_iphone_app(self):
        self.assertIs(self.entitlements("Persona/Resources/Persona.entitlements").get(ents.TIME_SENSITIVE), True)

    def test_the_vision_app(self):
        self.assertIs(self.entitlements("Vision/Resources/PersonaVision.entitlements").get(ents.TIME_SENSITIVE), True)


class MacApp(unittest.TestCase):
    """A Mac app (what a .pkg holds) for `check`, its target asking for time-sensitive notifications."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.app = root / "Persona.app"
        (self.app / "Contents").mkdir(parents=True)
        with open(self.app / "Contents" / "Info.plist", "wb") as f:
            plistlib.dump({"CFBundleIdentifier": BUNDLE, "DTPlatformName": "macosx"}, f)
        with open(root / "PersonaMac.entitlements", "wb") as f:
            plistlib.dump({ents.TIME_SENSITIVE: True, "com.apple.developer.aps-environment": "development"}, f)
        self.targets = {(BUNDLE, "macosx"): {"CODE_SIGN_ENTITLEMENTS": "PersonaMac.entitlements", "PROJECT_DIR": str(root),
                                              "DEVELOPMENT_TEAM": "TEAM", "TARGET_NAME": "PersonaMac"}}

    def tearDown(self):
        self.tmp.cleanup()


class CheckAMacApp(MacApp):
    """`check` on a Mac app: a target that asks for time-sensitive must be signed with it."""

    def check(self, signed):
        with patch.object(ents, "targets", return_value=self.targets), patch.object(ents, "signed", return_value=signed), \
                patch.object(ents.check_no_callkit, "scan", return_value=(1, [])) as linkage:
            ents.check(str(self.app))
        linkage.assert_called_once_with(str(self.app))

    def test_signed_without_it_is_a_problem(self):
        with self.assertRaises(SystemExit) as stop:
            self.check({"application-identifier": "TEAM." + BUNDLE, "com.apple.developer.aps-environment": "development"})
        self.assertIn("1 problem", str(stop.exception))

    def test_signed_with_it_is_ok(self):
        self.check({"application-identifier": "TEAM." + BUNDLE, "com.apple.developer.aps-environment": "development",
                    ents.TIME_SENSITIVE: True})


class SiriCalls(MacApp):
    """The Siri entitlement (config.json app.siri_calls, #9290) is checked only for a target whose entitlements ask for
    it: the tracked ones never do, so a build with the switch off is checked as before."""

    SIGNED = {"application-identifier": "TEAM." + BUNDLE, "com.apple.developer.aps-environment": "development",
              ents.TIME_SENSITIVE: True}

    def ask_for_siri(self):
        path = Path(self.targets[(BUNDLE, "macosx")]["PROJECT_DIR"]) / "PersonaMac.entitlements"
        with open(path, "rb") as f:
            wanted = plistlib.load(f)
        with open(path, "wb") as f:
            plistlib.dump({**wanted, ents.SIRI: True}, f)

    def check(self, signed, printed=None):
        printed = [] if printed is None else printed
        with patch.object(ents, "targets", return_value=self.targets), patch.object(ents, "signed", return_value=signed), \
                patch.object(ents.check_no_callkit, "scan", return_value=(1, [])), \
                patch("builtins.print", lambda *a, **k: printed.append(" ".join(map(str, a)))):
            ents.check(str(self.app))
        return printed

    def test_off_by_default(self):
        with open(APPLE / "Persona/Resources/Persona.entitlements", "rb") as f:
            self.assertNotIn(ents.SIRI, plistlib.load(f))

    def test_off_a_build_without_it_is_ok(self):
        self.assertIn("entitlements ok: 1 bundles in Persona.app", self.check(self.SIGNED))

    def test_on_signed_without_it_is_a_problem(self):
        self.ask_for_siri()
        printed = []
        with self.assertRaises(SystemExit) as stop:
            self.check(self.SIGNED, printed)
        self.assertIn("1 problem", str(stop.exception))
        self.assertEqual(len(printed), 1)
        self.assertTrue(printed[0].startswith("Persona.app: %s missing (Siri can't call Persona" % ents.SIRI), printed)

    def test_on_signed_with_it_is_ok(self):
        self.ask_for_siri()
        self.assertIn("entitlements ok: 1 bundles in Persona.app", self.check({**self.SIGNED, ents.SIRI: True}))


class OptInsInAReleaseIPA(unittest.TestCase):
    """The exported-IPA path checks every entitlement its Release target asks for, not only HealthKit and Siri (#9526)."""
    CASES = {
        'CarPlay voice': 'com.apple.developer.carplay-voice-based-conversation',
        'HomeKit': 'com.apple.developer.homekit',
        'HealthKit background delivery': ents.HEALTH_BACKGROUND,
    }

    def check(self, key, with_opt_in):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            wanted = {'aps-environment': 'development', ents.TIME_SENSITIVE: True, key: True}
            with open(root / 'Persona.entitlements', 'wb') as f:
                plistlib.dump(wanted, f)
            targets = {(BUNDLE, 'iphoneos'): {
                'CODE_SIGN_ENTITLEMENTS': 'Persona.entitlements', 'PROJECT_DIR': str(root),
                'DEVELOPMENT_TEAM': 'TEAM', 'TARGET_NAME': 'Persona'}}
            signed = {'application-identifier': 'TEAM.' + BUNDLE, 'aps-environment': 'production',
                      ents.TIME_SENSITIVE: True}
            if with_opt_in:
                signed[key] = True
            ipa = root / 'Persona.ipa'
            with zipfile.ZipFile(ipa, 'w') as archive:
                archive.writestr('Payload/Persona.app/Info.plist', plistlib.dumps({
                    'CFBundleIdentifier': BUNDLE, 'DTPlatformName': 'iphoneos'}))
            printed, stopped = [], None
            with patch.object(ents, 'targets', return_value=targets), patch.object(ents, 'signed', return_value=signed), \
                    patch.object(ents.check_no_callkit, 'scan', return_value=(1, [])), \
                    patch('builtins.print', lambda *a, **k: printed.append(' '.join(map(str, a)))):
                try:
                    ents.check(str(ipa))
                except SystemExit as stop:
                    stopped = str(stop)
            return printed, stopped

    def test_check_rejects_an_ipa_missing_each_opt_in_entitlement(self):
        for name, key in self.CASES.items():
            with self.subTest(opt_in=name):
                printed, stopped = self.check(key, with_opt_in=False)
                self.assertIsNotNone(stopped, 'an IPA signed without %s must not pass check' % key)
                self.assertIn(key, '\n'.join(printed) + (stopped or ''))

    def test_check_accepts_an_ipa_with_each_opt_in_entitlement(self):
        for name, key in self.CASES.items():
            with self.subTest(opt_in=name):
                printed, stopped = self.check(key, with_opt_in=True)
                self.assertIsNone(stopped, stopped)
                self.assertIn('entitlements ok: 1 bundles in Persona.ipa', printed)


class CheckLinksNoCallKit(MacApp):
    """`check` fails when a binary in the app links CallKit (#9290); otool is faked, so this runs on Linux too."""

    SIGNED = {"application-identifier": "TEAM." + BUNDLE, "com.apple.developer.aps-environment": "development",
              ents.TIME_SENSITIVE: True}

    def setUp(self):
        super().setUp()
        (self.app / "Contents" / "MacOS").mkdir()
        (self.app / "Contents" / "MacOS" / "Persona").write_bytes(b"\xcf\xfa\xed\xfe" + b"\0" * 28)

    def run_check(self, *deps, stderr=""):
        """(printed lines, the exit message or None) of `check` with otool listing deps for every binary."""
        def otool(cmd, **kwargs):
            out = cmd[-1] + ":\n" + "".join("\t%s (compatibility version 1.0.0, current version 1.0.0)\n" % d
                                             for d in deps)
            return subprocess.CompletedProcess(cmd, 1 if stderr else 0, out, stderr)
        printed, stopped = [], None
        with patch.object(ents, "targets", return_value=self.targets), patch.object(ents, "signed", return_value=self.SIGNED), \
                patch.object(ents.check_no_callkit.subprocess, "run", otool), \
                patch("builtins.print", lambda *a, **k: printed.append(" ".join(map(str, a)))):
            try:
                ents.check(str(self.app))
            except SystemExit as stop:
                stopped = str(stop)
        return printed, stopped

    def test_a_clean_app_says_how_many_binaries(self):
        printed, stopped = self.run_check(
            "/usr/lib/libSystem.B.dylib", "/System/Library/Frameworks/LiveCommunicationKit.framework/LiveCommunicationKit")
        self.assertIsNone(stopped)
        self.assertEqual(printed, ["entitlements ok: 1 bundles in Persona.app", "no CallKit: 1 Mach-O files checked"])

    def test_callkit_is_a_problem(self):
        printed, stopped = self.run_check("/usr/lib/libSystem.B.dylib", "/usr/lib/swift/libswiftCallKit.dylib")
        self.assertEqual(printed, ["links CallKit (App Review rejects it for China): "
                                   "Persona.app/Contents/MacOS/Persona → /usr/lib/swift/libswiftCallKit.dylib"])
        self.assertIn("1 problem(s)", stopped)

    def test_a_tool_error_or_no_binary_stops_it(self):
        _, stopped = self.run_check(stderr="can't open file")
        self.assertIn("CallKit check: otool -L", stopped)
        (self.app / "Contents" / "MacOS" / "Persona").write_bytes(b"#!/bin/sh\n")
        _, stopped = self.run_check("/usr/lib/libSystem.B.dylib")
        self.assertIn("CallKit check: no Mach-O file", stopped)


if __name__ == "__main__":
    unittest.main()
