import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import apple_release_source
import apple_resolved


ROOT = Path(__file__).resolve().parents[2]
LOCK = ROOT / "apple/Packages/PersonaSSH/Package.resolved"


class AppleReleaseSourceTests(unittest.TestCase):
    def setUp(self):
        self.original = LOCK.read_bytes()
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        self.source = self.root / "checkout"
        self.source.mkdir()
        subprocess.run(["git", "init", "-q", str(self.source)], check=True)
        (self.source / "apple/Packages/PersonaSSH").mkdir(parents=True)
        (self.source / "apple/Packages/PersonaSSH/Package.resolved").write_bytes(self.original)
        (self.source / "tracked.txt").write_text("source\n")
        subprocess.run(["git", "-C", str(self.source), "add", "--", "apple/Packages/PersonaSSH/Package.resolved", "tracked.txt"], check=True)
        subprocess.run([
            "git", "-C", str(self.source), "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
            "commit", "--quiet", "-m", "source",
        ], check=True)
        (self.source / "config.json").write_text("do-not-copy-private-config\n")
        (self.source / ".ci-cache").mkdir()
        (self.source / ".ci-cache/ignored.txt").write_text("ignored\n")

    def tearDown(self):
        self.tempdir.cleanup()

    def test_snapshot_stages_meta_specific_lockfile_and_preserves_tracked(self):
        before = hashlib.sha256(self.original).hexdigest()
        destination = self.root / "release"
        apple_release_source.snapshot(self.source, destination, "NO")
        self.assertTrue((destination / "tracked.txt").is_file())
        self.assertFalse((destination / "config.json").exists())
        self.assertFalse((destination / ".ci-cache").exists())
        self.assertEqual(hashlib.sha256(self.original).hexdigest(), before)
        self.assertEqual(hashlib.sha256((destination / "apple/Packages/PersonaSSH/Package.resolved").read_bytes()).hexdigest(),
                         "d6091eab6c13e65aa7af543970c51bfc4bc7b0f5947b3213277779842dce002b")
        self.assertEqual(len(json.loads((destination / "apple/Packages/PersonaSSH/Package.resolved").read_text())["pins"]), 12)
        self.assertEqual((destination / "resolved-variant.txt").read_text().count("tracked_unchanged=yes"), 1)

    def test_snapshot_meta_enabled_retains_the_pristine_superset(self):
        destination = self.root / "release-meta"
        apple_release_source.snapshot(self.source, destination, "YES")
        self.assertEqual((destination / "apple/Packages/PersonaSSH/Package.resolved").read_bytes(), self.original)
        self.assertEqual(len(json.loads((destination / "apple/Packages/PersonaSSH/Package.resolved").read_text())["pins"]), 13)

    def test_snapshot_requires_a_new_destination(self):
        destination = self.root / "existing"
        destination.mkdir()
        with self.assertRaisesRegex(ValueError, "new empty directory"):
            apple_release_source.snapshot(self.source, destination, "NO")


if __name__ == "__main__":
    unittest.main()
