import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

import beta_channel_release as beta
import tag_release


class BetaChannelReleaseTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.commit = "a" * 40
        self.manifest = self.root / "latest.json"
        self.manifest.write_text(json.dumps({"version": "0.7.0-beta.1", "build": 202611171300, "commit": self.commit,
                                             "channel": "beta", "files": {"darwin-arm64": {"name": "personad-darwin-arm64.gz"}}}))
        self.receipt = self.root / "receipt.json"
        self.receipt.write_text(json.dumps({"commit": self.commit, "assets": [
            {"path": str(self.manifest), "name": "latest.json", "sha256": hashlib.sha256(self.manifest.read_bytes()).hexdigest()}]}))
        self.digest = "sha256:" + hashlib.sha256(self.manifest.read_bytes()).hexdigest()

    def test_prerelease_is_required(self):
        with patch.object(tag_release.subprocess, "check_output", return_value=json.dumps(
                {"tagName": "v0.7.0-beta.1", "isDraft": False, "isPrerelease": False, "assets": []})):
            with self.assertRaisesRegex(tag_release.ConfigError, "prerelease"):
                tag_release.upload("example/project", "v0.7.0-beta.1", [])

    def test_pointer_verifies_tag_and_remote_index(self):
        uploaded = []
        calls = []

        def output(gh, path):
            tag = path.rsplit("/", 1)[-1]
            return {"tag_name": tag, "draft": False, "prerelease": True,
                    "target_commitish": self.commit if uploaded else "",
                    "assets": [{"name": "latest.json", "digest": self.digest}] if uploaded else []}

        def run(argv, **kwargs):
            calls.append(argv)
            if argv[:3] == ["gh", "release", "upload"]:
                uploaded.append(True)

        with patch.object(beta, "resolve_tag", return_value=self.commit), \
                patch.object(beta, "get_json", side_effect=output), \
                patch.object(beta.subprocess, "check_output", return_value=json.dumps(
                    {"tag_name": "v0.7.0-beta.1", "draft": False, "prerelease": True,
                     "assets": [{"name": "latest.json", "digest": self.digest}]})), \
                patch.object(beta.subprocess, "run", side_effect=run):
            with patch.object(beta.sys, "argv", ["beta", "--repo", str(self.root), "--repository", "example/project",
                                                "--tag", "v0.7.0-beta.1", "--receipt", str(self.receipt)]):
                beta.main()
        self.assertTrue(any(argv[:3] == ["gh", "release", "upload"] for argv in calls))

    def test_pointer_rejects_stable_manifest(self):
        self.manifest.write_text(json.dumps({"version": "0.7.0-beta.1", "build": 1, "files": {}}))
        self.receipt.write_text(json.dumps({"commit": self.commit, "assets": [{"path": str(self.manifest), "name": "latest.json"}]}))
        with patch.object(beta, "resolve_tag", return_value=self.commit), patch.object(
                beta.sys, "argv", ["beta", "--repo", str(self.root), "--repository", "example/project",
                                   "--tag", "v0.7.0-beta.1", "--receipt", str(self.receipt)]):
            with self.assertRaisesRegex(tag_release.ConfigError, "beta-channel"):
                beta.main()
    def test_pointer_advances_only_from_an_authenticated_ancestor(self):
        previous = "b" * 40
        old = self.root / "old.json"
        old.write_text(json.dumps({"version": "0.7.0-beta.1", "channel": "beta", "commit": previous}))
        digest = "sha256:" + hashlib.sha256(old.read_bytes()).hexdigest()
        pointer = {"tag_name": beta.POINTER, "draft": False, "prerelease": True,
                   "target_commitish": previous, "assets": [{"name": "latest.json", "digest": digest}]}
        def run(argv, **kwargs):
            if argv[:3] == ["gh", "release", "download"]:
                shutil.copyfile(old, Path(argv[-1]))
            return beta.subprocess.CompletedProcess(argv, 0)
        with patch.object(beta, "resolve_tag", return_value=previous), \
                patch.object(beta.subprocess, "run", side_effect=run):
            self.assertEqual(beta.previous_source(self.root, "example/project", pointer, "gh"), (previous, "v0.7.0-beta.1"))
        with patch.object(beta, "resolve_tag", return_value=self.commit), \
                patch.object(beta.subprocess, "run", side_effect=run):
            with self.assertRaisesRegex(tag_release.ConfigError, "source"):
                beta.previous_source(self.root, "example/project", pointer, "gh")

    def test_pointer_replaces_an_old_beta_index_after_authenticating_ancestry(self):
        previous = "b" * 40
        old = self.root / "old.json"
        old.write_text(json.dumps({"version": "0.7.0-beta.1", "channel": "beta", "commit": previous}))
        old_digest = "sha256:" + hashlib.sha256(old.read_bytes()).hexdigest()
        uploaded = []

        def output(gh, path):
            if path.endswith("/" + beta.POINTER):
                return {"tag_name": beta.POINTER, "draft": False, "prerelease": True,
                        "target_commitish": self.commit if uploaded else previous,
                        "assets": [{"name": "latest.json", "digest": self.digest if uploaded else old_digest}]}
            return {"tag_name": "v0.7.0-beta.2", "draft": False, "prerelease": True,
                    "assets": [{"name": "latest.json", "digest": self.digest}]}

        def run(argv, **kwargs):
            if argv[:3] == ["gh", "release", "download"]:
                shutil.copyfile(old, Path(argv[-1]))
            if argv[:3] == ["gh", "release", "upload"]:
                uploaded.append(argv)
            return beta.subprocess.CompletedProcess(argv, 0)

        manifest = json.loads(self.manifest.read_text())
        manifest["version"] = "0.7.0-beta.2"
        self.manifest.write_text(json.dumps(manifest))
        self.receipt.write_text(json.dumps({"commit": self.commit, "assets": [{"path": str(self.manifest),
            "name": "latest.json", "sha256": hashlib.sha256(self.manifest.read_bytes()).hexdigest()}]}))
        self.digest = "sha256:" + hashlib.sha256(self.manifest.read_bytes()).hexdigest()
        with patch.object(beta, "resolve_tag", side_effect=[self.commit, previous]), \
                patch.object(beta, "get_json", side_effect=output), \
                patch.object(beta.subprocess, "check_output", return_value=json.dumps(
                    {"tag_name": "v0.7.0-beta.2", "draft": False, "prerelease": True,
                     "assets": [{"name": "latest.json", "digest": self.digest}]})), \
                patch.object(beta.subprocess, "run", side_effect=run), \
                patch.object(beta.sys, "argv", ["beta", "--repo", str(self.root), "--repository", "example/project",
                                                "--tag", "v0.7.0-beta.2", "--receipt", str(self.receipt)]):
            beta.main()
        self.assertTrue(uploaded)

    def test_pointer_rejects_a_stable_previous_manifest(self):
        old = self.root / "old.json"
        old.write_text(json.dumps({"version": "0.7.0", "commit": "b" * 40}))
        pointer = {"tag_name": beta.POINTER, "draft": False, "prerelease": True, "assets": [{"name": "latest.json"}]}
        with patch.object(beta.subprocess, "run", side_effect=lambda argv, **kw: old.replace(Path(argv[-1]))):
            with self.assertRaisesRegex(tag_release.ConfigError, "immutable beta"):
                beta.previous_source(self.root, "example/project", pointer, "gh")

    def test_receipt_digest_survives_unchanged_helpers(self):
        digest = hashlib.sha256(self.manifest.read_bytes()).hexdigest()
        manifest = json.loads(self.manifest.read_text())
        manifest["helpers"] = {"persona-iphone-bridge": {"darwin-arm64": {"name": "persona-iphone-bridge-darwin-arm64.gz"}}}
        (self.root / "persona-iphone-bridge-darwin-arm64.gz").write_bytes(b"helper")
        assets = [{"path": str(path), "name": path.name,
                   "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}
                  for path in (self.manifest, self.root / "persona-iphone-bridge-darwin-arm64.gz")]
        kept = tag_release.drop_missing_helpers(assets, self.root)
        self.assertEqual(kept, assets)
        latest = next(item for item in kept if item["name"] == "latest.json")
        digest = latest["sha256"]
        with patch.object(tag_release.subprocess, "check_output", return_value=json.dumps(
                {"tag_name": "v0.7.0-beta.1", "draft": False, "prerelease": True,
                 "assets": [{"name": "latest.json", "digest": "sha256:" + digest}]})):
            tag_release.verify_remote_assets("example/project", "v0.7.0-beta.1", [latest],
                                             [{"name": "latest.json", "digest": "sha256:" + digest}])


if __name__ == "__main__":
    unittest.main()
