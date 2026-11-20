import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import stable_release


class StableReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="stable-release-tests-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.manifest = self.root / "latest.json"
        self.manifest.write_text(json.dumps({"version": "0.2.1", "build": 202611110749}))
        self.repository = "example/project"
        self.tag = "v0.2.1"

    def test_newest_stable_manifest_promotes_release(self):
        def output(command, **kwargs):
            if command[:3] == ["gh", "release", "list"]:
                return json.dumps([{"tagName": "v0.1.0"}, {"tagName": self.tag}])
            return json.dumps({"isLatest": True, "isPrerelease": False})

        with patch.object(stable_release, "check_tag_feed") as feed, \
                patch.object(stable_release.subprocess, "check_output", side_effect=output), \
                patch.object(stable_release.subprocess, "run") as edit:
            self.assertTrue(stable_release.promote_stable(self.repository, self.tag, self.manifest, "gh"))
        feed.assert_called_once_with("https://github.com/example/project/releases/download/v0.2.1/latest.json", "0.2.1", 202611110749)
        edit.assert_called_once_with(["gh", "release", "edit", self.tag, "--repo", self.repository, "--latest"],
                                     check=True, timeout=60)

    def test_older_stable_does_not_move_latest_backward(self):
        with patch.object(stable_release.subprocess, "check_output", return_value=json.dumps(
                [{"tagName": "v0.2.1"}, {"tagName": "v0.10.0"}])), \
                patch.object(stable_release, "check_tag_feed") as feed, \
                patch.object(stable_release.subprocess, "run") as edit:
            self.assertFalse(stable_release.promote_stable(self.repository, self.tag, self.manifest, "gh"))
        feed.assert_not_called()
        edit.assert_not_called()

    def test_main_invocation_uses_the_manifest_path(self):
        def output(command, **kwargs):
            if command[:3] == ["gh", "release", "list"]:
                return json.dumps([{"tagName": self.tag}])
            return json.dumps({"isLatest": True, "isPrerelease": False})

        argv = ["stable_release.py", "--repository", self.repository, "--tag", self.tag, "--manifest", str(self.manifest)]
        with patch.object(sys, "argv", argv), patch.object(stable_release, "check_tag_feed"), \
                patch.object(stable_release.subprocess, "check_output", side_effect=output), \
                patch.object(stable_release.subprocess, "run"):
            stable_release.main()

    def test_latest_readback_failure_is_an_error(self):
        def output(command, **kwargs):
            if command[:3] == ["gh", "release", "list"]:
                return json.dumps([{"tagName": self.tag}])
            return json.dumps({"isLatest": False, "isPrerelease": False})

        with patch.object(stable_release, "check_tag_feed"), \
                patch.object(stable_release.subprocess, "check_output", side_effect=output), \
                patch.object(stable_release.subprocess, "run"):
            with self.assertRaisesRegex(stable_release.StableReleaseError, "did not become"):
                stable_release.promote_stable(self.repository, self.tag, self.manifest, "gh")


if __name__ == "__main__":
    unittest.main()
