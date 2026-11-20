"""Apple permission-mode labels agree with spec/fixtures/permission-modes/names.json (#10363)."""
import json
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]
LANGS = ("en", "zh-Hans", "ja")
MODES = ("default", "acceptEdits", "plan", "auto", "bypassPermissions")


class PermissionModeNamesTest(unittest.TestCase):
    def test_localizable_matches_shared_table(self):
        fixture = json.loads((ROOT / "spec/fixtures/permission-modes/names.json").read_text(encoding="utf-8"))
        names = fixture["names"]
        self.assertEqual(set(MODES), set(names))
        catalog = json.loads((ROOT / "apple/Shared/Localization/Localizable.xcstrings").read_text(encoding="utf-8"))["strings"]

        # Resolve the picker keys from their call sites: the old unscoped "Auto" key is also an
        # effort label, so this check does not prescribe how that collision is split.
        session_modes = (ROOT / "apple/Persona/Views/Chat/SessionModes.swift").read_text(encoding="utf-8")
        picker = session_modes.split("static let permissionModes", 1)[1].split("static let effortLevels", 1)[0]
        answered = (ROOT / "apple/Shared/Answered.swift").read_text(encoding="utf-8")
        task_views = (ROOT / "apple/Persona/Views/Assistant/Tasks/TaskViews.swift").read_text(encoding="utf-8")
        planned = task_views.split("static func permissionLabel", 1)[1].split("\n    }", 1)[0]

        exact = {mode: [] for mode in MODES}
        picker_keys = {}
        for mode in MODES:
            match = re.search(
                rf'Option\(id:\s*"{re.escape(mode)}",\s*title:\s*String\(localized:\s*"([^"]+)"', picker)
            self.assertIsNotNone(match, f"SessionModes.permissionModes has no localized title for {mode}")
            picker_keys[mode] = match.group(1)
            exact[mode].append(match.group(1))

        answered_keys = {}
        for mode in ("default", "acceptEdits", "bypassPermissions"):
            match = re.search(rf'"{mode}":\s*String\(localized:\s*"([^"]+)"', answered)
            self.assertIsNotNone(match, f"Answered.modeLabels has no localized title for {mode}")
            answered_keys[mode] = match.group(1)
        # Auto-Accept Edits is an approval-button action and keeps its verb wording. The receipt
        # uses the picker's standalone permission name instead of repurposing that action key.
        self.assertEqual(picker_keys["acceptEdits"], answered_keys["acceptEdits"],
                         "Answered acceptEdits must use the SessionModes permission key")
        for mode in ("default", "bypassPermissions"):
            if answered_keys[mode] not in exact[mode]:
                exact[mode].append(answered_keys[mode])

        # Planned tasks have standalone labels too, not approval-action phrases.
        for mode in MODES:
            pattern = (r'default:\s*String\(localized:\s*"([^"]+)"' if mode == "default"
                       else rf'case\s+"{re.escape(mode)}":\s*String\(localized:\s*"([^"]+)"')
            match = re.search(pattern, planned)
            self.assertIsNotNone(match, f"PlanTaskRow.permissionLabel has no localized title for {mode}")
            if match.group(1) not in exact[mode]:
                exact[mode].append(match.group(1))

        contains = {
            "bypassPermissions": [
                "Allow Everything: it won't ask before running commands or changing files.",
                "Allow Everything?",
                "Leave Allow Everything?",
                "This agent wasn't started with Allow Everything, so it can't be switched back to it from here.",
                "This agent uses its own approval settings unless you choose Allow Everything.",
                "This agent uses its own approval settings unless you choose Allow Everything. You can switch in the chat later.",
            ],
        }

        def value(key, language):
            self.assertIn(key, catalog, f"missing Localizable.xcstrings key: {key}")
            localizations = catalog[key]["localizations"]
            self.assertIn(language, localizations, f"{key}: missing {language}")
            return localizations[language]["stringUnit"]["value"]

        for mode in MODES:
            for key in exact[mode]:
                for language in LANGS:
                    with self.subTest(mode=mode, key=key, language=language):
                        self.assertEqual(names[mode][language], value(key, language))
            for key in contains.get(mode, []):
                for language in LANGS:
                    with self.subTest(mode=mode, key=key, language=language):
                        self.assertIn(names[mode][language], value(key, language))


if __name__ == "__main__":
    unittest.main()
