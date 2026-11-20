"""The private tag consumer never broadens signing credentials or public refs."""
import io
from contextlib import nullcontext
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
import urllib.error
import urllib.request
import zipfile
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import tag_release


class TagReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="tag-release-tests-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "release@example.com")
        self.git("config", "user.name", "Fixture")
        (self.repo / "README.md").write_text("fixture\n")
        self.git("add", "README.md")
        self.git("commit", "-q", "-m", "source")
        self.commit = self.git("rev-parse", "HEAD")
        self.config_path = self.root / "release.json"
        self.config_path.write_text(json.dumps({
            "repo": str(self.repo),
            "worktree": str(self.root / "release-worktree"),
            "state_dir": str(self.root / "state"),
            "repository": "example/project",
            "platform": "android",
            "command": ["/usr/bin/true", "{commit}", "{tag}"],
            "receipt": str(self.root / "state" / "artifacts.json"),
        }))
        self.config_path.chmod(0o600)
        self.build_patcher = patch.object(tag_release, "release_build", return_value=202611110749)
        self.build_patcher.start()
        self.addCleanup(self.build_patcher.stop)

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], text=True).strip()

    def config(self):
        return tag_release.load_config(self.config_path)

    def test_config_is_private_and_commands_are_argv_with_known_fields(self):
        config = self.config()
        self.assertEqual(config["platform"], "android")
        self.config_path.chmod(0o644)
        with self.assertRaisesRegex(tag_release.ConfigError, "0600"):
            self.config()
        self.config_path.chmod(0o600)
        for value in ("true; {commit}", ["/usr/bin/true", "{unknown}"], ["/usr/bin/true", "{commit!r}"]):
            data = json.loads(self.config_path.read_text())
            data["command"] = value
            self.config_path.write_text(json.dumps(data))
            with self.assertRaises(tag_release.ConfigError):
                self.config()

    def test_state_must_stay_outside_the_dedicated_worktree(self):
        data = json.loads(self.config_path.read_text())
        data["state_dir"] = str(self.root / "release-worktree" / "state")
        self.config_path.write_text(json.dumps(data))
        with self.assertRaisesRegex(tag_release.ConfigError, "state directory"):
            self.config()

    def test_existing_clean_detached_worktree_checks_out_exact_tag(self):
        other = self.root / "release-worktree"
        self.git("worktree", "add", "--detach", str(other), self.commit)
        (self.repo / "next").write_text("new\n")
        self.git("add", "next")
        self.git("commit", "-q", "-m", "later source")
        newer = self.git("rev-parse", "HEAD")
        tag_release.prepare_worktree(self.config(), self.commit)
        self.assertEqual(subprocess.check_output(["git", "-C", str(other), "rev-parse", "HEAD"], text=True).strip(), self.commit)
        self.assertNotEqual(newer, self.commit)
        (other / "uncommitted").write_text("do not delete\n")
        with self.assertRaisesRegex(tag_release.ConfigError, "not clean"):
            tag_release.prepare_worktree(self.config(), newer)
        self.assertEqual((other / "uncommitted").read_text(), "do not delete\n")

    def test_lightweight_and_annotated_remote_tags_resolve_to_their_commit(self):
        remote = f"{self.commit}\trefs/tags/v1.0.0\n"
        with patch.object(tag_release, "git", side_effect=[remote, "", self.commit, remote]) as call:
            self.assertEqual(tag_release.resolve_tag(self.repo, "example/project", "v1.0.0"), self.commit)
            self.assertEqual(call.call_args_list[3].args[1:], ("ls-remote", "--tags", "https://github.com/example/project.git",
                                                               "refs/tags/v1.0.0", "refs/tags/v1.0.0^{}"))
        annotated = "a" * 40
        remote = f"{annotated}\trefs/tags/v1.0.0\n{self.commit}\trefs/tags/v1.0.0^{{}}\n"
        with patch.object(tag_release, "git", side_effect=[remote, "", self.commit, remote]):
            self.assertEqual(tag_release.resolve_tag(self.repo, "example/project", "v1.0.0"), self.commit)
        with patch.object(tag_release, "git", side_effect=[remote, "", self.commit, remote + "different\n"]):
            with self.assertRaisesRegex(tag_release.ConfigError, "moved"):
                tag_release.resolve_tag(self.repo, "example/project", "v1.0.0")
        with patch.object(tag_release, "git") as call:
            for invalid in ("main", "v1.0.0^{}", "v1..0", "v1.0.0;push"):
                with self.assertRaises(tag_release.ConfigError):
                    tag_release.resolve_tag(self.repo, "example/project", invalid)
            call.assert_not_called()

    def test_receipt_requires_commit_and_matching_artifact_digest(self):
        artifact = self.root / "signed.apk"
        artifact.write_bytes(b"verified APK")
        digest = tag_release.hashlib.sha256(artifact.read_bytes()).hexdigest()
        receipt = self.root / "receipt.json"
        receipt.write_text(json.dumps({"commit": self.commit, "assets": [{"path": str(artifact), "sha256": digest}]}))
        assets = tag_release.read_receipt(receipt, self.commit)
        self.assertEqual(assets, [{"path": str(artifact), "name": "signed.apk"}])
        artifact.write_bytes(b"replaced")
        with self.assertRaisesRegex(tag_release.ConfigError, "digest"):
            tag_release.read_receipt(receipt, self.commit)
        receipt.write_text(json.dumps({"commit": self.commit, "assets": []}))
        with self.assertRaisesRegex(tag_release.ConfigError, "store_verified"):
            tag_release.read_receipt(receipt, self.commit)
        receipt.write_text(json.dumps({"commit": self.commit, "store_verified": True, "assets": []}))
        self.assertEqual(tag_release.read_receipt(receipt, self.commit), [])

    def test_store_only_receipt_checks_release_without_uploading_an_artifact(self):
        with patch.object(tag_release.subprocess, "check_output", return_value=json.dumps({"tagName": "v1.0.0", "isDraft": False, "assets": []})) as view, patch.object(tag_release.subprocess, "run") as upload:
            tag_release.upload("example/project", "v1.0.0", [])
            upload.assert_not_called()
            self.assertEqual(view.call_args.args[0][:3], ["gh", "release", "view"])

    def test_upload_refuses_a_view_without_an_asset_list(self):
        artifact = self.root / "Persona-1.0.0.apk"
        artifact.write_bytes(b"signed")
        views = [json.dumps({"tagName": "v1.0.0", "isDraft": False, "assets": []})]
        for answer in ("null", json.dumps({"assets": None}), json.dumps("assets")):
            with self.subTest(answer=answer), patch.object(tag_release.subprocess, "run"), \
                    patch.object(tag_release.subprocess, "check_output", side_effect=views + [answer]):
                with self.assertRaisesRegex(tag_release.ConfigError, "no asset list"):
                    tag_release.upload("example/project", "v1.0.0", [{"path": str(artifact), "name": artifact.name}])

    def test_upload_views_existing_release_and_verifies_remote_digests(self):
        artifact = self.root / "Persona-1.0.0.apk"
        artifact.write_bytes(b"signed")
        assets = [{"path": str(artifact), "name": artifact.name}]
        digest = tag_release.hashlib.sha256(artifact.read_bytes()).hexdigest()
        calls = []

        def run(command, **kwargs):
            calls.append(command)
            return subprocess.CompletedProcess(command, 0)

        def output(command, **kwargs):
            calls.append(command)
            if command[-1] == "tagName,isDraft,assets":
                return json.dumps({"tagName": "v1.0.0", "isDraft": False, "assets": []})
            return json.dumps([{"name": artifact.name, "digest": "sha256:" + digest}])

        with patch.object(tag_release.subprocess, "run", side_effect=run), patch.object(tag_release.subprocess, "check_output", side_effect=output):
            tag_release.upload("example/project", "v1.0.0", assets)
        self.assertEqual(calls[0][:3], ["gh", "release", "view"])
        self.assertEqual(calls[1][:3], ["gh", "release", "upload"])
        self.assertNotIn("create", calls[1])
        self.assertEqual(calls[1][-1].endswith("/" + artifact.name), True)
        self.assertNotIn("#", calls[1][-1])
        with patch.object(tag_release.subprocess, "run", side_effect=run), patch.object(tag_release.subprocess, "check_output", side_effect=[json.dumps({"tagName": "v1.0.0", "isDraft": False, "assets": []}), json.dumps({"assets": [{"name": artifact.name, "digest": "sha256:" + digest}]})]):
            tag_release.upload("example/project", "v1.0.0", assets)
        with patch.object(tag_release.subprocess, "run", side_effect=run), patch.object(tag_release.subprocess, "check_output", side_effect=[json.dumps({"tagName": "v1.0.0", "isDraft": False, "assets": []}), json.dumps({"assets": []})]):
            with self.assertRaisesRegex(tag_release.ConfigError, "verification"):
                tag_release.upload("example/project", "v1.0.0", assets)

    def test_old_gh_without_digests_uses_rest_and_downloads_as_a_last_resort(self):
        artifact = self.root / "Persona-1.0.0.apk"
        artifact.write_bytes(b"signed")
        assets = [{"path": str(artifact), "name": artifact.name}]
        digest = tag_release.hashlib.sha256(artifact.read_bytes()).hexdigest()
        without_digest = {"tagName": "v1.0.0", "isDraft": False,
                          "assets": [{"name": artifact.name, "size": len(b"signed")}]}
        with patch.object(tag_release.subprocess, "check_output", side_effect=[
                json.dumps(without_digest),
                json.dumps({"assets": [{"name": artifact.name, "size": len(b"signed"), "digest": "sha256:" + digest}]})]) as api:
            tag_release.upload("example/project", "v1.0.0", assets)
            self.assertEqual(api.call_args.args[0],
                             ["gh", "api", "repos/example/project/releases/tags/v1.0.0"])

        def output(command, **kwargs):
            if command[-1] == "tagName,isDraft,assets":
                return json.dumps({"tagName": "v1.0.0", "isDraft": False, "assets": []})
            if command[-1] == "assets":
                return json.dumps([{"name": artifact.name, "size": len(b"signed")}])
            if command[-1] == "repos/example/project/releases/tags/v1.0.0":
                return json.dumps({"assets": [{"name": artifact.name, "size": len(b"signed"),
                                               "url": "https://api.github.com/repos/example/project/releases/assets/42"}]})
            self.assertEqual(command, ["gh", "api", "-H", "Accept: application/octet-stream",
                                       "https://api.github.com/repos/example/project/releases/assets/42"])
            return artifact.read_bytes()

        with patch.object(tag_release.subprocess, "run"), patch.object(tag_release.subprocess, "check_output", side_effect=output):
            tag_release.upload("example/project", "v1.0.0", assets)

        for bad in (b"replaced", b"short"):
            with self.subTest(body=bad), patch.object(tag_release.subprocess, "run"), \
                    patch.object(tag_release.subprocess, "check_output", side_effect=[
                        json.dumps(without_digest),
                        json.dumps({"assets": [{"name": artifact.name, "size": len(b"signed"),
                                                "url": "https://api.github.com/repos/example/project/releases/assets/42"}]}),
                        bad]):
                with self.assertRaisesRegex(tag_release.ConfigError, "verification"):
                    tag_release.upload("example/project", "v1.0.0", assets)

        with patch.object(tag_release.subprocess, "check_output", side_effect=[
                json.dumps(without_digest),
                json.dumps({"assets": [{"name": artifact.name, "size": len(b"signed"), "digest": "sha256:" + "0" * 64}]})]):
            with self.assertRaisesRegex(tag_release.ConfigError, "verification"):
                tag_release.upload("example/project", "v1.0.0", assets)

    def release_receipt(self, build=202611110749, binary_builds=None, update_url=None, version="0.2.1", channel=None,
                        other_update_url=None, directory_name="receipt"):
        """A hub receipt as publish.sh leaves it: personad for this machine (a script answering version -v), one for
        another platform (bytes with its build number, as -X main.build leaves it) and latest.json over them.
        binary_builds gives a download another build (the earlier run's)."""
        import gzip
        binary_builds = binary_builds or {}
        update_url = update_url or "https://github.com/example/project/releases/latest/download"
        other_update_url = other_update_url or update_url
        here = tag_release.host_platform()
        other = "linux-arm64" if here == "linux-amd64" else "linux-amd64"
        directory = self.root / directory_name
        directory.mkdir(exist_ok=True)
        files = {}
        for plat in (here, other):
            name = "personad-%s.gz" % plat
            n = binary_builds.get(name, build)
            body = ((("#!/bin/sh\n# main.updateURL %s\necho %s\necho 'channel release, build %d'\n"
                      % (update_url, version, n)) if plat == here else
                     "\x7fELF\x00main.build\x00%d\x00main.updateURL\x00%s\x00the rest" % (n, other_update_url)).encode())
            (directory / name).write_bytes(gzip.compress(body, mtime=0))
            data = (directory / name).read_bytes()
            files[plat] = {"name": name, "size": len(data), "sha256": tag_release.hashlib.sha256(data).hexdigest(),
                           "binary_sha256": tag_release.hashlib.sha256(body).hexdigest()}
        manifest = {"version": version, "build": build, "files": files}
        if channel is not None:
            manifest["channel"] = channel
        (directory / "latest.json").write_text(json.dumps(manifest))
        name = "persona-link-windows-amd64.exe.gz"
        body = ("\x00main.build\x00%d\x00the Windows connector" % binary_builds.get(name, build)).encode()
        (directory / name).write_bytes(gzip.compress(body, mtime=0))
        data = (directory / name).read_bytes()
        index = json.loads((directory / "latest.json").read_text())
        index["links"] = {"windows-amd64": {"name": name, "size": len(data),
                          "sha256": tag_release.hashlib.sha256(data).hexdigest(),
                          "binary_sha256": tag_release.hashlib.sha256(body).hexdigest()}}
        (directory / "latest.json").write_text(json.dumps(index))
        return [{"path": str(path), "name": path.name} for path in sorted(directory.iterdir())]

    def with_installer(self, assets, update_url):
        directory = Path(assets[0]["path"]).parent
        installer = directory / "install.sh"
        installer.write_text('#!/bin/sh\nDL="${PERSONA_DL:-%s}"\n' % update_url)
        return assets + [{"path": str(installer), "name": installer.name}]

    def with_ci_indexes(self, assets):
        directory = Path(assets[0]["path"]).parent
        (directory / "ci-latest.json").write_text((directory / "latest.json").read_text())
        (directory / "latest.json").unlink()
        (directory / "VERSION").write_text("0.2.1\n")
        (directory / "ci-SHA256SUMS").write_text("staging checksums\n")
        kept = [item for item in assets if item["name"] != "latest.json"]
        return kept + [{"path": str(directory / "ci-latest.json"), "name": "ci-latest.json"},
                       {"path": str(directory / "ci-SHA256SUMS"), "name": "ci-SHA256SUMS"},
                       {"path": str(directory / "VERSION"), "name": "VERSION"}]

    def with_windows_verification(self, assets):
        import gzip
        directory = Path(assets[0]["path"]).parent
        name = "persona-link-windows-amd64.exe.gz"
        binary = bytearray(128)
        binary[:2], binary[60:64], binary[64:68] = b"MZ", (64).to_bytes(4, "little"), b"PE\0\0"
        binary[68:70] = (0x8664).to_bytes(2, "little")
        latest = next(item for item in assets if item["name"] == "latest.json")
        manifest = json.loads(Path(latest["path"]).read_text())
        binary += b"verified Windows connector\x00main.build\x00%d\x00" % manifest["build"]
        archive = gzip.compress(bytes(binary), mtime=0)
        (directory / name).write_bytes(archive)
        descriptor = manifest["links"]["windows-amd64"]
        descriptor.update(size=len(archive), sha256=tag_release.hashlib.sha256(archive).hexdigest(),
                          binary_sha256=tag_release.hashlib.sha256(binary).hexdigest())
        Path(latest["path"]).write_text(json.dumps(manifest))
        report = {
            "schema": 2, "version": manifest["version"], "build": manifest["build"], "platform": "windows-amd64",
            "channel": "release", "program": "persona-link",
            "archive": {"name": name, "size": len(archive), "sha256": descriptor["sha256"]},
            "binaries": {"persona-link.exe": {"size": len(binary), "sha256": descriptor["binary_sha256"]}},
            "outputs": {"version": manifest["version"],
                        "verbose": "%s\nchannel release, build %d" % (manifest["version"], manifest["build"])},
        }
        report_path = directory / "persona-link-windows-amd64.verification.json"
        report_path.write_text(json.dumps(report))
        return assets + [{"path": str(report_path), "name": report_path.name}]

    def publish_receipt(self, assets, tag, platform, label, remote_manifest=None, prerelease=False, events=None,
                        stable_tags=None, feed_error=None, ci_assets=None, ci_conclusion="success", verify_windows=False):
        """Run the real deterministic publisher with only its build command, GitHub CLI and tag-feed HTTP mocked."""
        data = json.loads(self.config_path.read_text())
        state = self.root / ("publication-" + label)
        data.update(platform=platform, state_dir=str(state), receipt=str(state / "artifacts.json"))
        self.config_path.write_text(json.dumps(data))
        config = self.config()
        state.mkdir(parents=True, exist_ok=True)
        Path(config["receipt"]).write_text(json.dumps({"commit": self.commit, "assets": assets}))
        remote_source = assets if ci_assets is None else ci_assets
        remote = [{"name": item["name"],
                   "digest": "sha256:" + tag_release.hashlib.sha256(Path(item["path"]).read_bytes()).hexdigest()}
                  for item in remote_source]
        remote_bytes = {item["name"]: Path(item["path"]).read_bytes() for item in remote_source}
        events = [] if events is None else events
        stable_tags = [tag] if stable_tags is None else stable_tags
        real_run, real_output = subprocess.run, subprocess.check_output

        def run(command, **kwargs):
            if not command or command[0] != "gh":
                if command and command[0] == "/usr/bin/true":
                    events.append(("build", command))
                return real_run(command, **kwargs)
            events.append(("github", command))
            if command[:3] == ["gh", "release", "download"]:
                name = command[command.index("--pattern") + 1]
                source = next(Path(item["path"]) for item in remote_source if item["name"] == name)
                destination = Path(command[command.index("--dir") + 1])
                destination.mkdir(parents=True, exist_ok=True)
                with destination.joinpath(name).open("xb") as output:
                    output.write(source.read_bytes())
            if command[:3] == ["gh", "release", "upload"]:
                for filename in command[6:]:
                    path = Path(filename)
                    remote_bytes[path.name] = path.read_bytes()
                    remote.append({"name": path.name,
                                   "digest": "sha256:" + tag_release.hashlib.sha256(path.read_bytes()).hexdigest()})
            return subprocess.CompletedProcess(command, 0)

        def output(command, **kwargs):
            if not command or command[0] != "gh":
                return real_output(command, **kwargs)
            events.append(("github", command))
            if command[:2] == ["gh", "run"]:
                return json.dumps([{"databaseId": 123, "status": "completed", "headSha": self.commit}] if ci_assets is not None else [])
            if command[:2] == ["gh", "api"]:
                if "/actions/runs/" in command[2]:
                    return json.dumps({"jobs": [{"name": "Sign Darwin command-line programs", "status": "completed",
                                                 "conclusion": ci_conclusion}]})
                return json.dumps({"assets": remote})
            if command[:3] == ["gh", "release", "list"]:
                return json.dumps([{"tagName": name} for name in stable_tags])
            self.assertEqual(command[:3], ["gh", "release", "view"])
            fields = command[-1].split(",")
            details = {"tagName": tag, "isDraft": False, "isPrerelease": prerelease, "isLatest": False,
                       "createdAt": "2026-11-11T07:49:00Z", "assets": remote if fields == ["assets"] else []}
            return json.dumps({field: details[field] for field in fields})

        def fetch(request, **kwargs):
            url = request.full_url if isinstance(request, urllib.request.Request) else str(request)
            events.append(("http", url))
            if feed_error is not None:
                raise feed_error
            if remote_manifest is None:
                body = remote_bytes["latest.json"]
            else:
                body = json.dumps(remote_manifest).encode()
            return io.BytesIO(body)

        real_upload = tag_release.upload

        def upload(repository, release_tag, release_assets, gh="gh"):
            events.append(("upload", release_tag))
            return real_upload(repository, release_tag, release_assets, gh)

        windows_check = nullcontext() if verify_windows else patch.object(tag_release, "check_windows_artifacts")
        with patch.object(tag_release, "prepare_worktree",
                          side_effect=lambda config, commit: config["worktree"].mkdir(exist_ok=True)), \
                patch.object(tag_release, "upload", side_effect=upload), \
                windows_check, \
                patch.object(tag_release.subprocess, "run", side_effect=run), \
                patch.object(tag_release.subprocess, "check_output", side_effect=output), \
                patch("urllib.request.urlopen", side_effect=fetch):
            tag_release.publish_once(config, tag, self.commit)
        return events

    @staticmethod
    def release_actions(events, action):
        return [command for kind, command in events
                if kind == "github" and command[:3] == ["gh", "release", action]]

    def test_hub_publication_refuses_a_personad_with_another_compiled_update_url(self):
        assets = self.release_receipt(update_url="https://stale.example.test/dl")
        events = []
        with self.assertRaisesRegex(tag_release.ConfigError, "(?i:update[ _]?url)"):
            self.publish_receipt(assets, "v0.2.1", "hub", "wrong-update-url", events=events)
        self.assertEqual(self.release_actions(events, "upload"), [])
        self.assertEqual(self.release_actions(events, "edit"), [])

    def test_hub_publication_refuses_an_installer_with_another_update_url(self):
        assets = self.with_installer(self.release_receipt(), "https://stale.example.test/dl")
        events = []
        with self.assertRaisesRegex(tag_release.ConfigError, "(?i:update[ _]?url)"):
            self.publish_receipt(assets, "v0.2.1", "hub", "wrong-installer-update-url", events=events)
        self.assertEqual(self.release_actions(events, "upload"), [])
        self.assertEqual(self.release_actions(events, "edit"), [])

    def test_hub_publication_refuses_a_longer_compiled_update_url(self):
        expected = "https://github.com/example/project/releases/latest/download"
        assets = self.release_receipt(update_url=expected + "/stale")
        with self.assertRaisesRegex(tag_release.ConfigError, "(?i:update[ _]?url)"):
            self.publish_receipt(assets, "v0.2.1", "hub", "longer-update-url")

    def test_hub_publication_refuses_an_update_url_nested_in_another_url(self):
        expected = "https://github.com/example/project/releases/latest/download"
        assets = self.release_receipt(update_url="https://stale.example.test/path/" + expected)
        with self.assertRaisesRegex(tag_release.ConfigError, "(?i:update[ _]?url)"):
            self.publish_receipt(assets, "v0.2.1", "hub", "nested-update-url")

    def test_hub_publication_refuses_another_platforms_stale_update_url(self):
        assets = self.release_receipt(other_update_url="https://stale.example.test/dl")
        with self.assertRaisesRegex(tag_release.ConfigError, "(?i:update[ _]?url)"):
            self.publish_receipt(assets, "v0.2.1", "hub", "other-platform-update-url")

    def test_older_stable_hub_publication_does_not_move_latest_backward(self):
        stable_tags = ["v0.2.1", "v0.9.0", "v0.10.0"]
        older = self.publish_receipt(self.release_receipt(), "v0.2.1", "hub", "older-stable-hub",
                                     stable_tags=stable_tags)
        self.assertEqual(self.release_actions(older, "edit"), [])
        self.assertFalse(any(kind == "http" for kind, _ in older))
        newer = self.publish_receipt(self.release_receipt(version="0.10.0"), "v0.10.0", "hub", "newer-stable-hub",
                                     stable_tags=stable_tags)
        promotions = self.release_actions(newer, "edit")
        self.assertEqual(len(promotions), 1)
        self.assertEqual(promotions[0][3], "v0.10.0")

    def test_hub_manifest_version_is_rejected_before_upload(self):
        events = []
        with self.assertRaisesRegex(tag_release.ConfigError, "version"):
            self.publish_receipt(self.release_receipt(version="0.2.0"), "v0.2.1", "hub",
                                 "wrong-manifest-version", events=events)
        self.assertFalse(any(kind == "upload" for kind, _ in events))
        self.assertEqual(self.release_actions(events, "edit"), [])

    def test_nonsemver_stable_hub_is_rejected_before_upload_checkpoint(self):
        events = []
        with self.assertRaisesRegex(tag_release.ConfigError, "vX.Y.Z"):
            self.publish_receipt(self.release_receipt(), "v1.0", "hub", "nonsemver-stable", events=events)
        self.assertFalse(any(kind == "upload" for kind, _ in events))
        self.assertEqual(self.release_actions(events, "edit"), [])

    def test_promotion_retry_reuses_the_verified_uploaded_receipt(self):
        events = []
        assets = self.release_receipt()
        with self.assertRaisesRegex(tag_release.ConfigError, "latest.json"):
            self.publish_receipt(assets, "v0.2.1", "hub", "retry-uploaded-receipt",
                                 feed_error=urllib.error.URLError("offline"), events=events)
        with patch.object(tag_release, "check_release_manifest", wraps=tag_release.check_release_manifest) as check:
            self.publish_receipt(assets, "v0.2.1", "hub", "retry-uploaded-receipt", events=events)
        self.assertEqual(check.call_count, 1)
        self.assertEqual(len([command for kind, command in events if kind == "build"]), 1)
        self.assertEqual(len(self.release_actions(events, "edit")), 1)
        state = json.loads((self.root / "publication-retry-uploaded-receipt" / "state.json").read_text())
        self.assertEqual(state["phase"], "complete")

    def test_complete_ci_hub_artifacts_replace_independent_private_builds(self):
        local = self.with_windows_verification(self.with_installer(self.release_receipt(directory_name="local-receipt"),
                                                                  "https://github.com/example/project/releases/latest/download"))
        ci = self.with_ci_indexes(self.with_installer(self.release_receipt(directory_name="ci-receipt"),
                                 "https://github.com/example/project/releases/latest/download"))
        latest = next(item for item in ci if item["name"] == "ci-latest.json")
        manifest = json.loads(Path(latest["path"]).read_text())
        manifest["commit"] = self.commit
        Path(latest["path"]).write_text(json.dumps(manifest))
        events = self.publish_receipt(local, "v0.2.1", "hub", "complete-ci-hub", ci_assets=ci, verify_windows=True)
        downloads = [command for kind, command in events if kind == "github" and command[:3] == ["gh", "release", "download"]]
        self.assertTrue(downloads)
        self.assertFalse(any(command[command.index("--pattern") + 1].endswith(".exe.gz") for command in downloads))
        state = json.loads((self.root / "publication-complete-ci-hub" / "state.json").read_text())
        self.assertEqual(state["phase"], "complete")
        self.assertTrue(any("ci-artifacts-" in item["path"] for item in state["assets"]))
        self.assertTrue(any(item["name"] == "persona-link-windows-amd64.verification.json" for item in state["assets"]))
        self.assertEqual(len(self.release_actions(events, "edit")), 1)

    def test_ci_windows_build_mismatch_is_rejected(self):
        local = self.with_windows_verification(self.release_receipt(directory_name="local-receipt"))
        ci = self.with_ci_indexes(self.with_installer(self.release_receipt(build=202611110800, directory_name="ci-receipt"),
                                                     "https://github.com/example/project/releases/latest/download"))
        latest = next(item for item in ci if item["name"] == "ci-latest.json")
        manifest = json.loads(Path(latest["path"]).read_text())
        manifest["commit"] = self.commit
        Path(latest["path"]).write_text(json.dumps(manifest))
        events = []
        with self.assertRaisesRegex(tag_release.ConfigError, "same CI and local build"):
            self.publish_receipt(local, "v0.2.1", "hub", "mismatched-ci-windows-build",
                                 ci_assets=ci, verify_windows=True, events=events)
        self.assertFalse(any(kind == "upload" for kind, _ in events))

    def test_missing_ci_sha256sums_stops_publication(self):
        local = self.with_windows_verification(self.release_receipt(directory_name="local-receipt"))
        ci = self.with_ci_indexes(self.with_installer(self.release_receipt(directory_name="ci-receipt"),
                                                     "https://github.com/example/project/releases/latest/download"))
        latest = next(item for item in ci if item["name"] == "ci-latest.json")
        manifest = json.loads(Path(latest["path"]).read_text())
        manifest["commit"] = self.commit
        Path(latest["path"]).write_text(json.dumps(manifest))
        ci = [item for item in ci if item["name"] != "ci-SHA256SUMS"]
        events = []
        with self.assertRaisesRegex(tag_release.ConfigError, "CI SHA256SUMS is missing"):
            self.publish_receipt(local, "v0.2.1", "hub", "missing-ci-sha", ci_assets=ci,
                                 verify_windows=True, events=events)
        self.assertFalse(any(kind == "upload" for kind, _ in events))

    def test_failed_ci_signing_stops_hub_publication(self):
        local = self.release_receipt(directory_name="local-receipt")
        ci = self.release_receipt(directory_name="ci-receipt")
        events = []
        with self.assertRaisesRegex(tag_release.ConfigError, "(?i:darwin|signing)"):
            self.publish_receipt(local, "v0.2.1", "hub", "failed-ci-hub", ci_assets=ci,
                                 ci_conclusion="failure", events=events)
        self.assertFalse(any(kind == "upload" for kind, _ in events))
        self.assertEqual(self.release_actions(events, "edit"), [])

    def test_release_build_uses_release_creation_time_not_commit_time(self):
        self.build_patcher.stop()
        with patch.object(tag_release.subprocess, "check_output", return_value='{"createdAt":"2026-11-19T01:02:03Z"}'):
            self.assertEqual(tag_release.release_build("example/project", "v0.2.1"), 202611190102)

    def test_ci_signing_returns_none_when_pending_run_completes_without_job(self):
        responses = iter([
            json.dumps([{"databaseId": 123, "status": "queued"}]),
            json.dumps({"jobs": []}),
            json.dumps({"status": "completed"}),
        ])
        with patch.object(tag_release.subprocess, "check_output", side_effect=responses), \
                patch.object(tag_release.time, "sleep") as sleep:
            self.assertIsNone(tag_release.ci_signing_conclusion("example/project", "v0.2.1"))
        sleep.assert_not_called()

    def test_ci_signing_rejects_run_for_another_commit(self):
        with patch.object(tag_release.subprocess, "check_output", return_value=json.dumps(
                [{"databaseId": 123, "status": "completed", "headSha": "different"}])):
            with self.assertRaisesRegex(tag_release.ConfigError, "another source commit"):
                tag_release.ci_signing_conclusion("example/project", "v0.2.1", commit="a" * 40)

    def test_ci_signing_waits_for_pending_run_before_jobs_exist(self):
        responses = iter([
            json.dumps([{"databaseId": 123, "status": "queued"}]),
            json.dumps({"jobs": []}),
            json.dumps({"status": "queued"}),
            json.dumps({"jobs": [{"name": "Sign Darwin command-line programs", "conclusion": "success"}]}),
        ])
        with patch.object(tag_release.subprocess, "check_output", side_effect=responses), \
                patch.object(tag_release.time, "sleep") as sleep:
            self.assertEqual(tag_release.ci_signing_conclusion("example/project", "v0.2.1"), "success")
        sleep.assert_called_once_with(10)

    def test_uploaded_tag_manifest_must_match_version_and_build_before_latest_promotion(self):
        assets = self.release_receipt()
        local = json.loads(Path(next(item["path"] for item in assets if item["name"] == "latest.json")).read_text())
        expected_url = "https://github.com/example/project/releases/download/v0.2.1/latest.json"
        for label, change in (("version", {"version": "0.2.0"}), ("build", {"build": local["build"] - 1})):
            with self.subTest(wrong=label):
                events = []
                with self.assertRaisesRegex(tag_release.ConfigError, "latest.json|manifest"):
                    self.publish_receipt(assets, "v0.2.1", "hub", "wrong-feed-" + label,
                                         remote_manifest=dict(local, **change), events=events)
                # Verify this immutable tag, not releases/latest, which still names the previous stable Release.
                self.assertIn(("http", expected_url), events)
                self.assertEqual(self.release_actions(events, "edit"), [])

    def test_only_successful_stable_hub_publication_marks_release_latest(self):
        cases = (
            ("stable non-hub", self.android_receipt(), "v0.3.0", "android", False),
            ("prerelease hub", self.release_receipt(version="0.7.0-beta.2", channel="beta"),
             "v0.7.0-beta.2", "hub", True),
        )
        for label, assets, tag, platform, prerelease in cases:
            with self.subTest(case=label):
                events = self.publish_receipt(assets, tag, platform, label.replace(" ", "-"), prerelease=prerelease)
                self.assertEqual(self.release_actions(events, "edit"), [])
                self.assertFalse(any(kind == "http" for kind, _ in events))

        assets = self.with_installer(self.release_receipt(), "https://github.com/example/project/releases/latest/download")
        events = self.publish_receipt(assets, "v0.2.1", "hub", "verified-stable-hub",
                                      stable_tags=["v0.1.0", "v0.2.1"])
        tag_feed = "https://github.com/example/project/releases/download/v0.2.1/latest.json"
        self.assertIn(("http", tag_feed), events)
        promotions = self.release_actions(events, "edit")
        self.assertEqual(len(promotions), 1)
        self.assertEqual(promotions[0][3], "v0.2.1")
        self.assertIn("--latest", promotions[0])
        self.assertNotIn("--latest=false", promotions[0])
        self.assertEqual(events[-1], ("github", promotions[0]))

    def test_release_manifest_must_be_its_binaries_build(self):
        # v0.2.1 (#4522): latest.json said build 202611110749, its binaries were 202611110745 (an earlier run).
        tag_release.check_release_manifest(self.release_receipt())
        here = "personad-%s.gz" % tag_release.host_platform()
        with self.assertRaisesRegex(tag_release.ConfigError, "says .*another build"):
            tag_release.check_release_manifest(self.release_receipt(binary_builds={here: 202611110745}))
        other = next(item["name"] for item in self.release_receipt() if item["name"] not in (here, "latest.json")
                     and not item["name"].startswith("persona-link-"))
        with self.assertRaisesRegex(tag_release.ConfigError, "is not build 202611110749"):
            tag_release.check_release_manifest(self.release_receipt(binary_builds={other: 202611110745}))
        with self.assertRaisesRegex(tag_release.ConfigError, "is not build 202611110749"):
            tag_release.check_release_manifest(self.release_receipt(binary_builds={
                "persona-link-windows-amd64.exe.gz": 202611110745}))
        assets = self.release_receipt()
        Path(next(item["path"] for item in assets if item["name"] == other)).write_bytes(b"replaced")
        with self.assertRaisesRegex(tag_release.ConfigError, "size or sha256"):
            tag_release.check_release_manifest(assets)
        with self.assertRaisesRegex(tag_release.ConfigError, "not in the receipt"):
            tag_release.check_release_manifest([item for item in self.release_receipt() if item["name"] != other])
        # Another platform's receipt, without latest.json, is not a release manifest to check.
        tag_release.check_release_manifest([item for item in self.release_receipt() if item["name"] != "latest.json"])

    def test_helpers_are_checked_without_a_build_number(self):
        # persona-iphone-bridge (#8199): its own version, no build number; its bytes still have to be the receipt's.
        import gzip
        assets = self.release_receipt()
        directory = Path(assets[0]["path"]).parent
        body = b"\x7fELF\x00a helper with no build number"
        name = "persona-iphone-bridge-linux-arm64.gz"
        (directory / name).write_bytes(gzip.compress(body, mtime=0))
        data = (directory / name).read_bytes()
        index = json.loads((directory / "latest.json").read_text())
        index["helpers"] = {"persona-iphone-bridge": {"linux-arm64": {
            "name": name, "size": len(data), "sha256": tag_release.hashlib.sha256(data).hexdigest(),
            "binary_sha256": tag_release.hashlib.sha256(body).hexdigest()}}}
        (directory / "latest.json").write_text(json.dumps(index))
        assets = [{"path": str(path), "name": path.name} for path in sorted(directory.iterdir())]
        tag_release.check_release_manifest(assets)
        with self.assertRaisesRegex(tag_release.ConfigError, "not in the receipt"):
            tag_release.check_release_manifest([item for item in assets if item["name"] != name])
        # A receipt from an upload command that doesn't carry the helper yet goes up without it, never refused.
        self.assertEqual(tag_release.drop_missing_helpers(assets, self.root / "state"), assets)
        without = [item for item in assets if item["name"] != name]
        with patch.object(tag_release.sys, "stderr", new_callable=io.StringIO) as err:
            kept = tag_release.drop_missing_helpers(without, self.root / "state")
        self.assertIn(name, err.getvalue())
        latest = next(item for item in kept if item["name"] == "latest.json")
        self.assertEqual(Path(latest["path"]).parent, self.root / "state")
        self.assertNotIn("helpers", json.loads(Path(latest["path"]).read_text()))
        self.assertEqual(json.loads(Path(latest["path"]).read_text())["files"], index["files"])
        tag_release.check_release_manifest(kept)
        self.assertEqual(sorted(item["name"] for item in kept), sorted(item["name"] for item in without))
        (directory / name).write_bytes(gzip.compress(b"another build", mtime=0))
        with self.assertRaisesRegex(tag_release.ConfigError, "size or sha256"):
            tag_release.check_release_manifest(assets)

    def test_build_number_stands_alone_wherever_the_stream_is_cut(self):
        import gzip
        for body, want in ((b"202611110749", True), (b"x202611110749\x00", True), (b"1202611110749", False),
                           (b"2026111107490", False), (b"202611110745", False), (b"12026111107490 202611110749", True)):
            for size in (1, 5, 13, 1 << 20):
                with self.subTest(body=body, size=size):
                    path = self.root / "stamp.gz"
                    path.write_bytes(gzip.compress(body))
                    real_open = gzip.open

                    def chunked(*args, **kwargs):
                        stream = real_open(*args, **kwargs)
                        read = stream.read
                        stream.read = lambda n=-1: read(min(n, size) if n and n > 0 else size)
                        return stream

                    with patch.object(tag_release.gzip, "open", side_effect=chunked):
                        self.assertEqual(tag_release.scan_gzip(path, b"202611110749")[1], want)

    def test_index_files_go_up_after_the_downloads_they_list(self):
        assets = self.release_receipt()
        directory = Path(assets[0]["path"]).parent
        (directory / "SHA256SUMS").write_text("sums\n")
        assets.append({"path": str(directory / "SHA256SUMS"), "name": "SHA256SUMS"})
        uploads = []

        def run(command, **kwargs):
            uploads.append([Path(arg).name for arg in command[6:]])
            return subprocess.CompletedProcess(command, 0)

        remote = [{"name": item["name"], "digest": "sha256:" + tag_release.hashlib.sha256(Path(item["path"]).read_bytes()).hexdigest()}
                  for item in assets]
        with patch.object(tag_release.subprocess, "run", side_effect=run), patch.object(tag_release.subprocess, "check_output", side_effect=[
                json.dumps({"tagName": "v0.2.1", "isDraft": False, "assets": []}), json.dumps(remote)]):
            tag_release.upload("example/project", "v0.2.1", assets)
        self.assertEqual(uploads[-2:], [["SHA256SUMS"], ["latest.json"]])
        self.assertEqual(sorted(uploads[0]), sorted(item["name"] for item in assets if item["name"].endswith(".gz")))

    def android_receipt(self, **change):
        """An Android receipt as android_tag_release.py leaves it: the signed APK and android.json describing it."""
        directory = self.root / "android"
        directory.mkdir(exist_ok=True)
        apk = directory / "Persona-0.3.0-android.apk"
        apk.write_bytes(b"PK signed")
        index = {"version": "0.3.0", "name": apk.name, "package_name": "org.example.persona", "version_code": 105432,
                 "version_name": "0.3.0", "min_sdk": 29, "certificate_sha256": "c" * 64, "size": apk.stat().st_size,
                 "sha256": tag_release.hashlib.sha256(apk.read_bytes()).hexdigest(), "source_sha": self.commit, "notes": ""}
        index.update(change)
        (directory / "android.json").write_text(json.dumps(index))
        return [{"path": str(directory / name), "name": name} for name in (apk.name, "android.json")]

    def test_beta_platform_receipt_does_not_need_a_hub_manifest(self):
        # Each platform publishes independently to the immutable beta prerelease. Only the hub owns latest.json.
        assets = self.android_receipt()
        tag_release.check_beta_manifest(assets, "0.7.0-beta.2")
        tag_release.check_beta_manifest([], "0.7.0-beta.2")  # a store-only iOS receipt
        path = self.root / "latest.json"
        for manifest in ({"channel": "beta", "version": "0.7.0-beta.2"},):
            path.write_text(json.dumps(manifest))
            tag_release.check_beta_manifest(assets + [{"path": str(path), "name": path.name}], "0.7.0-beta.2")
        for manifest in ({"version": "0.7.0-beta.2"}, {"channel": "release", "version": "0.7.0-beta.2"},
                         {"channel": "beta", "version": "0.7.0-beta.1"}):
            path.write_text(json.dumps(manifest))
            with self.subTest(manifest=manifest), self.assertRaisesRegex(tag_release.ConfigError, "version/channel"):
                tag_release.check_beta_manifest(assets + [{"path": str(path), "name": path.name}], "0.7.0-beta.2")
        with self.assertRaisesRegex(tag_release.ConfigError, "at most one"):
            tag_release.check_beta_manifest([{"path": str(path), "name": "latest.json"},
                                             {"path": str(path), "name": "latest.json"}], "0.7.0-beta.2")

    def test_beta_android_receipt_passes_publication_without_latest_json(self):
        config = self.config()
        config["worktree"].mkdir()
        receipt = Path(config["receipt"])
        receipt.parent.mkdir(parents=True)
        assets = self.android_receipt(version="0.7.0-beta.2")
        receipt.write_text(json.dumps({"commit": self.commit, "assets": assets}))
        with patch.object(tag_release, "resolve_tag", return_value=self.commit), \
                patch.object(tag_release, "prepare_worktree"), patch.object(tag_release, "upload") as upload:
            tag_release.invoke_agent(config, "v0.7.0-beta.2", self.commit)
        self.assertEqual([item["name"] for item in upload.call_args.args[2]],
                         ["Persona-0.3.0-android.apk", "android.json"])

    def test_android_index_must_describe_its_apk_and_version(self):
        tag_release.check_android_index(self.android_receipt(), "0.3.0")
        with self.assertRaisesRegex(tag_release.ConfigError, "not version 0.3.1"):
            tag_release.check_android_index(self.android_receipt(), "0.3.1")
        with self.assertRaisesRegex(tag_release.ConfigError, "size or sha256"):
            tag_release.check_android_index(self.android_receipt(sha256="0" * 64), "0.3.0")
        with self.assertRaisesRegex(tag_release.ConfigError, "not an APK in the receipt"):
            tag_release.check_android_index(self.android_receipt(name="Persona-0.2.1-android.apk"), "0.3.0")
        for change in ({"version_code": 0}, {"version_code": "105432"}, {"package_name": ""}, {"certificate_sha256": "c"}):
            with self.subTest(change=change), self.assertRaisesRegex(tag_release.ConfigError, "no valid package"):
                tag_release.check_android_index(self.android_receipt(**change), "0.3.0")
        # Another platform's receipt, without android.json, is not an Android index to check.
        tag_release.check_android_index(self.release_receipt(), "0.3.0")

    def test_android_index_goes_up_after_its_apk(self):
        assets = self.android_receipt()
        uploads = []

        def run(command, **kwargs):
            uploads.append([Path(arg).name for arg in command[6:]])
            return subprocess.CompletedProcess(command, 0)

        remote = [{"name": item["name"], "digest": "sha256:" + tag_release.hashlib.sha256(Path(item["path"]).read_bytes()).hexdigest()}
                  for item in assets]
        with patch.object(tag_release.subprocess, "run", side_effect=run), patch.object(tag_release.subprocess, "check_output", side_effect=[
                json.dumps({"tagName": "v0.3.0", "isDraft": False, "assets": []}), json.dumps(remote)]):
            tag_release.upload("example/project", "v0.3.0", assets)
        self.assertEqual(uploads, [["Persona-0.3.0-android.apk"], ["android.json"]])

    def test_mismatched_android_index_is_never_uploaded(self):
        config = self.config()
        receipt = Path(config["receipt"])
        receipt.parent.mkdir(parents=True, exist_ok=True)
        receipt.write_text(json.dumps({"commit": self.commit, "assets": self.android_receipt(size=1)}))
        with patch.object(tag_release, "prepare_worktree", side_effect=lambda config, commit: config["worktree"].mkdir()), patch.object(tag_release, "upload") as upload:
            with self.assertRaisesRegex(tag_release.ConfigError, "size or sha256"):
                tag_release.publish_once(config, "v0.3.0", self.commit)
            upload.assert_not_called()

    def test_mismatched_release_manifest_is_never_uploaded(self):
        data = json.loads(self.config_path.read_text())
        data["platform"] = "hub"
        self.config_path.write_text(json.dumps(data))
        config = self.config()
        assets = self.release_receipt(binary_builds={"personad-%s.gz" % tag_release.host_platform(): 202611110745})
        receipt = Path(config["receipt"])
        receipt.parent.mkdir(parents=True, exist_ok=True)
        receipt.write_text(json.dumps({"commit": self.commit, "assets": assets}))
        with patch.object(tag_release, "prepare_worktree", side_effect=lambda config, commit: config["worktree"].mkdir()), \
                patch.object(tag_release, "ci_signing_conclusion", return_value=None), \
                patch.object(tag_release, "upload") as upload, \
                patch.object(tag_release, "check_windows_artifacts"):
            with self.assertRaisesRegex(tag_release.ConfigError, "another build"):
                tag_release.publish_once(config, "v0.2.1", self.commit)
            upload.assert_not_called()
        self.assertEqual(json.loads((config["state_dir"] / "state.json").read_text())["phase"], "publishing")

    def test_failed_command_never_uploads_or_marks_complete(self):
        data = json.loads(self.config_path.read_text())
        data["command"] = ["/usr/bin/false"]
        self.config_path.write_text(json.dumps(data))
        config = self.config()
        with patch.object(tag_release, "prepare_worktree", side_effect=lambda config, commit: config["worktree"].mkdir()), patch.object(tag_release, "upload") as upload:
            with self.assertRaises(subprocess.CalledProcessError):
                tag_release.publish_once(config, "v1.0.0", self.commit)
            upload.assert_not_called()
        state = json.loads((config["state_dir"] / "state.json").read_text())
        self.assertEqual(state["phase"], "publishing")
        self.assertNotIn("complete", state.values())

    def windows_receipt(self, config):
        directory = config["state_dir"]
        directory.mkdir(parents=True, exist_ok=True)
        archive = directory / "Persona-0.5.3-windows-amd64.zip"
        with zipfile.ZipFile(archive, "w") as output:
            for program in ("persona-link.exe", "personad.exe"):
                output.writestr(archive.stem + "/" + program, b"unverified executable")
        metadata = directory / "BUILD.json"
        metadata.write_text(json.dumps({"version": "0.5.3", "build": 202611140822}))
        assets = [{"path": str(path), "name": path.name} for path in (archive, metadata)]
        Path(config["receipt"]).write_text(json.dumps({"commit": self.commit, "assets": assets}))
        return assets

    def test_unverified_windows_archive_is_never_uploaded(self):
        config = self.config()
        self.windows_receipt(config)
        with patch.object(tag_release, "prepare_worktree", side_effect=lambda config, commit: config["worktree"].mkdir()), \
                patch.object(tag_release, "upload") as upload:
            with self.assertRaises(tag_release.ConfigError):
                tag_release.publish_once(config, "v0.5.3", self.commit)
            upload.assert_not_called()
        state = json.loads((config["state_dir"] / "state.json").read_text())
        self.assertEqual(state["phase"], "publishing")

    def test_windows_verification_precedes_upload_and_receives_the_requested_version(self):
        config = self.config()
        assets = self.windows_receipt(config)
        events = []
        with patch.object(tag_release, "prepare_worktree", side_effect=lambda config, commit: config["worktree"].mkdir()), \
                patch.object(tag_release, "check_windows_artifacts", side_effect=lambda *args: events.append("verified")) as verify, \
                patch.object(tag_release, "upload", side_effect=lambda *args: events.append("uploaded")):
            tag_release.publish_once(config, "v0.5.3", self.commit)
        verify.assert_called_once_with(assets, "0.5.3")
        self.assertEqual(events, ["verified", "uploaded"])

    def test_complete_same_tag_is_not_published_twice(self):
        config = self.config()
        tag_release.write_state(config["state_dir"] / "state.json", {
            "tag": "v1.0.0", "commit": self.commit, "phase": "complete",
        })
        with patch.object(tag_release, "prepare_worktree") as prepare:
            tag_release.publish_once(config, "v1.0.0", self.commit)
            prepare.assert_not_called()

    def test_uploaded_checkpoint_skips_the_optional_agent(self):
        data = json.loads(self.config_path.read_text())
        data.update(platform="hub", agent=["/usr/bin/true", "{prompt_file}", "{log_file}"])
        self.config_path.write_text(json.dumps(data))
        config = self.config()
        tag_release.write_state(config["state_dir"] / "state.json", {
            "tag": "v0.2.1", "commit": self.commit, "platform": "hub", "phase": "uploaded",
            "store_verified": True, "assets": [],
        })
        with patch.object(tag_release, "prepare_worktree") as prepare, \
                patch.object(tag_release.subprocess, "run") as run, \
                patch.object(tag_release, "finish_publication", return_value=0) as finish:
            tag_release.invoke_agent(config, "v0.2.1", self.commit)
        prepare.assert_not_called()
        run.assert_not_called()
        finish.assert_called_once()

    def test_agent_runs_under_lock_before_the_deterministic_publisher(self):
        data = json.loads(self.config_path.read_text())
        data["agent"] = ["claude", "--print", "{prompt_file}", "{log_file}"]
        self.config_path.write_text(json.dumps(data))
        config = self.config()
        events = []

        def run(command, **kwargs):
            events.append(command)
            self.assertEqual(command[-2:], [str(Path(tag_release.__file__).with_name("tag_release_prompt.md")), str(config["state_dir"] / "agent.log")])
            self.assertEqual(kwargs["env"]["PERSONA_RELEASE_COMMIT"], self.commit)
            self.assertEqual(kwargs["env"]["PERSONA_RELEASE_PLATFORM"], "android")
            return subprocess.CompletedProcess(command, 0)

        with patch.object(tag_release, "prepare_worktree", side_effect=lambda config, commit: events.append("prepared")), patch.object(tag_release.subprocess, "run", side_effect=run), patch.object(tag_release, "publish_once", side_effect=lambda *args: events.append("published")):
            tag_release.invoke_agent(config, "v1.0.0", self.commit)
        self.assertEqual(events[0], "prepared")
        self.assertEqual(events[-1], "published")
        self.assertEqual(len(events), 3)

    def test_tick_skips_only_the_same_platform_tag_and_keeps_lock(self):
        config = self.config()
        tag_release.write_state(config["state_dir"] / "state.json", {"tag": "v0.9.0", "commit": self.commit, "phase": "complete"})
        with patch.object(tag_release, "latest_tags", return_value=["v0.9.0", "v1.0.0"]), patch.object(tag_release, "resolve_tag", return_value=self.commit), patch.object(tag_release, "invoke_agent") as invoke:
            tag_release.tick(config)
            invoke.assert_called_once_with(config, "v1.0.0", self.commit)

    def test_prompt_template_does_not_require_a_private_config(self):
        prompt = (Path(tag_release.__file__).with_name("tag_release_prompt.md")).read_text()
        self.assertIn("--once", prompt)
        self.assertIn("store_verified", prompt)
        self.assertIn("<owner>/<name>", prompt)
        self.assertIn("frozen native Mac", prompt)


if __name__ == "__main__":
    unittest.main()
