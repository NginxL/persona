"""A version tag's APK goes up only as the tag's own build, signed with the existing key and checked after signing."""
import fcntl
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(HERE.parent / "operations"))
import android_release_worker as worker
import android_tag_release as tag
import tag_release

CERT = "c" * 64


class AndroidTagReleaseTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="android-tag-release-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.worktree = self.root / "worktree"
        self.worktree.mkdir()
        self.git("init", "-q")
        self.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "--allow-empty", "-qm", "source")
        self.commit = self.git("rev-parse", "HEAD")
        self.receipt = self.root / "private" / "artifacts.json"
        self.config = {"repository": "example/app", "application_id": "org.example.persona", "certificate_sha256": CERT,
                       "state_directory": str(self.root / "state"),
                       "local_signing": {"keystore": "/keys/release.jks", "alias": "release", "password_command": ["/bin/echo"]}}

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.worktree), *args], text=True).strip()

    def fakes(self, signed=b"PK signed by the release key", inspected=None):
        """build writes the unsigned APK, the signer the signed one; inspect reads the APK as aapt2 and apksigner do."""
        def build(config, checkout, sha, directory, name, release_only=False):
            (directory / "unsigned.apk").write_bytes(b"PK unsigned")
            return dict(repository=config["repository"], source_sha=sha, unsigned_sha256="u" * 64,
                        package=config["application_id"], version_code=100001, version_name=name)

        def sign(unsigned, output, signer, request):
            self.assertEqual((signer["expected_cert_sha256"], signer["expected_package"]), (CERT, "org.example.persona"))
            output.write_bytes(signed)
            return dict(request, signed_sha256=hashlib.sha256(signed).hexdigest(), certificate_sha256=CERT)

        def inspect(apk, args):
            self.assertEqual((args.version_code, args.version_name, args.expected_cert_sha256, args.source_sha),
                             (100001, "0.3.0", CERT, self.commit))
            data = apk.read_bytes()
            return inspected or dict(package_name="org.example.persona", version_code=100001, version_name="0.3.0",
                                     min_sdk=29, certificate_sha256=CERT, sha256=hashlib.sha256(data).hexdigest(),
                                     size=len(data), notes="", source_sha=self.commit)

        return (patch.object(worker, "build", side_effect=build), patch.object(tag.android_signing_worker, "sign", side_effect=sign),
                patch.object(tag.publish_android_apk, "inspect_apk", side_effect=inspect))

    def release(self, config=None):
        return tag.release(config or self.config, self.worktree, self.commit, "0.3.0", self.receipt)

    def test_receipt_lists_the_checked_apk_and_its_index(self):
        build, sign, inspect = self.fakes()
        with build, sign, inspect:
            index = self.release()
        receipt = json.loads(self.receipt.read_text())
        self.assertEqual(receipt["commit"], self.commit)
        self.assertEqual([item["name"] for item in receipt["assets"]], ["Persona-0.3.0-android.apk", "android.json"])
        for item in receipt["assets"]:
            self.assertEqual(item["sha256"], hashlib.sha256(Path(item["path"]).read_bytes()).hexdigest())
        self.assertEqual(json.loads(Path(receipt["assets"][1]["path"]).read_text()), index)
        self.assertEqual((index["version"], index["name"], index["version_code"]), ("0.3.0", "Persona-0.3.0-android.apk", 100001))
        # What the coordinator checks before it uploads anything.
        assets = tag_release.read_receipt(self.receipt, self.commit)
        tag_release.check_android_index(assets, "0.3.0")

    def test_the_signing_host_signs_when_the_worker_uses_one(self):
        config = dict(self.config, signing_queue=str(self.root / "queue"))
        build, sign, inspect = self.fakes()

        def queue(config, request, directory):
            (directory / "signed.apk").write_bytes(b"PK signed by the release key")
            worker.atomic_json(directory / "signature.json",
                               dict(request, signed_sha256=hashlib.sha256(b"PK signed by the release key").hexdigest()))

        with build, sign as local, inspect, patch.object(worker, "signing", side_effect=queue) as host:
            self.release(config)
        host.assert_called_once()
        local.assert_not_called()

    def test_nothing_goes_up_when_the_signed_apk_is_not_the_tags(self):
        self.receipt.parent.mkdir()
        self.receipt.write_text("{}")  # an earlier attempt's
        build, sign, _ = self.fakes()
        with build, sign, patch.object(tag.publish_android_apk, "inspect_apk", side_effect=ValueError("APK signing certificate differs")):
            with self.assertRaisesRegex(ValueError, "certificate"):
                self.release()
        self.assertFalse(self.receipt.exists())
        build, sign, inspect = self.fakes(inspected=dict(package_name="org.example.persona", version_code=100001, version_name="0.3.0",
                                                         min_sdk=29, certificate_sha256=CERT, sha256="0" * 64, size=1))
        with build, sign, inspect, self.assertRaisesRegex(ValueError, "signing receipt"):
            self.release()
        self.assertFalse(self.receipt.exists())

    def test_only_the_tags_clean_full_checkout_is_built(self):
        build, sign, inspect = self.fakes()
        with build as built, sign, inspect:
            (self.worktree / "stray.txt").write_text("not in the tag")
            with self.assertRaisesRegex(ValueError, "not clean"):
                self.release()
            (self.worktree / "stray.txt").unlink()
            with self.assertRaisesRegex(ValueError, "not at the tag"):
                tag.release(self.config, self.worktree, "f" * 40, "0.3.0", self.receipt)
            built.assert_not_called()

    def test_a_signer_and_the_existing_certificate_are_required(self):
        tag.check_config(self.config)
        tag.check_config(dict({k: v for k, v in self.config.items() if k != "local_signing"}, signing_queue="/queue"))
        with self.assertRaisesRegex(ValueError, "no signer"):
            tag.check_config({k: v for k, v in self.config.items() if k != "local_signing"})
        with self.assertRaisesRegex(ValueError, "certificate_sha256"):
            tag.check_config(dict(self.config, certificate_sha256="C" * 64))
        build, sign, inspect = self.fakes()
        with build, sign, inspect, self.assertRaisesRegex(ValueError, "password_command"):
            self.release(dict(self.config, local_signing={"keystore": "/keys/release.jks", "alias": "release", "password_command": "echo"}))

    def test_waits_for_the_main_branch_worker_then_gives_up(self):
        state = self.root / "state"
        state.mkdir()
        with (state / "worker.lock").open("a") as held:
            fcntl.flock(held.fileno(), fcntl.LOCK_EX)
            with patch.object(tag.time, "sleep") as sleep, self.assertRaisesRegex(TimeoutError, "kept its lock"):
                tag.wait_for_lock(state / "worker.lock", 0)
            sleep.assert_not_called()
        with tag.wait_for_lock(state / "worker.lock", 0):
            pass

    def test_a_tag_build_is_reused_only_under_its_own_version_name(self):
        directory = self.root / "run"
        directory.mkdir()
        (directory / "unsigned.apk").write_bytes(b"PK unsigned")
        built = dict(repository="example/app", source_sha=self.commit, unsigned_sha256=worker.digest(directory / "unsigned.apk"),
                     package="org.example.persona", version_code=100001, version_name="0.3.0", build_commit=self.commit)
        worker.atomic_json(directory / "build.json", built)
        config = dict(self.config, application_id="org.example.persona")

        class Rebuilt(Exception):
            pass

        def run(args, cwd=None, capture=False, env=None):
            if args[0] == "python3":
                raise Rebuilt
            return ""

        with patch.object(worker, "run", side_effect=run), patch.object(worker, "version_code", return_value=100001):
            self.assertEqual(worker.build(config, self.worktree, self.commit, directory, "0.3.0"), built)
            with self.assertRaises(Rebuilt):
                worker.build(config, self.worktree, self.commit, directory, "0.3.1")


if __name__ == "__main__":
    unittest.main()
