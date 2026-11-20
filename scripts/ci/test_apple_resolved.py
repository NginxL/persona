import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import apple_resolved


ROOT = Path(__file__).resolve().parents[2]
RESOLVED = ROOT / "apple/Packages/PersonaSSH/Package.resolved"
TRACKED_SHA256 = "f50bac69a331777699739a95986393fa9c9a35a5b1087610b8bd4f57db961ae5"
DEFAULT_SHA256 = "d6091eab6c13e65aa7af543970c51bfc4bc7b0f5947b3213277779842dce002b"


class AppleResolvedTests(unittest.TestCase):
    def setUp(self):
        self.original = RESOLVED.read_bytes()
        self.tempdir = tempfile.TemporaryDirectory()
        root = Path(self.tempdir.name)
        self.tracked = root / "tracked.resolved"
        self.staged = root / "staged.resolved"
        self.evidence = root / "evidence.txt"
        self.tracked.write_bytes(self.original)
        self.staged.write_bytes(self.original)

    def tearDown(self):
        self.tempdir.cleanup()

    def test_render_preserves_the_tracked_bytes(self):
        self.assertEqual(hashlib.sha256(self.original).hexdigest(), TRACKED_SHA256)
        self.assertEqual(apple_resolved.render(json.loads(self.original)), self.original)

    def test_default_variant_matches_the_green_candidate_without_touching_tracked(self):
        apple_resolved.stage(self.tracked, self.staged, "NO", self.evidence)
        self.assertEqual(hashlib.sha256(self.staged.read_bytes()).hexdigest(), DEFAULT_SHA256)
        self.assertEqual(hashlib.sha256(self.tracked.read_bytes()).hexdigest(), TRACKED_SHA256)
        document = json.loads(self.staged.read_bytes())
        self.assertEqual(len(document["pins"]), 12)
        self.assertEqual(document["originHash"], json.loads(self.original)["originHash"])
        self.assertFalse(any(pin["identity"] == apple_resolved.META_IDENTITY for pin in document["pins"]))
        evidence = self.evidence.read_text()
        self.assertIn(f"source_sha256={TRACKED_SHA256}", evidence)
        self.assertIn(f"staged_sha256={DEFAULT_SHA256}", evidence)
        self.assertIn("tracked_unchanged=yes", evidence)

    def test_meta_enabled_variant_retains_the_pristine_superset(self):
        apple_resolved.stage(self.tracked, self.staged, "YES", self.evidence)
        self.assertEqual(self.staged.read_bytes(), self.original)
        self.assertEqual(self.tracked.read_bytes(), self.original)
        document = json.loads(self.staged.read_bytes())
        self.assertEqual(len(document["pins"]), 13)
        self.assertEqual(sum(pin["identity"] == apple_resolved.META_IDENTITY for pin in document["pins"]), 1)
        evidence = self.evidence.read_text()
        self.assertIn("meta_wearables=YES", evidence)
        self.assertIn(f"staged_sha256={TRACKED_SHA256}", evidence)
        self.assertIn("tracked_unchanged=yes", evidence)

    def test_staging_requires_a_distinct_disposable_copy(self):
        with self.assertRaisesRegex(ValueError, "disposable copy"):
            apple_resolved.stage(self.tracked, self.tracked, "NO", self.evidence)


if __name__ == "__main__":
    unittest.main()
