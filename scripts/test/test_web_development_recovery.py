"""Malformed recovery manifests and archive extraction stay fail-closed."""
import copy
import gzip
import io
import json
import os
import stat
import subprocess
import sys
from pathlib import Path
import tarfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))  # the suites run from scripts/test, where `scripts` is no package
import test_web_development_update as fixtures  # noqa: E402

webdev = fixtures.webdev


class RecoveryValidation(unittest.TestCase):
    def setUp(self):
        self.fixture = fixtures.DevelopmentFixtures()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.root = self.fixture.root
        self.request = self.fixture.history_fixture()
        self.request.update(from_version="web-dev-saved", from_commit="b" * 40,
                            from_build=202611120001, from_updated_at="2026-11-12T10:00:00Z",
                            platform="linux-amd64")
        nodes = self.root / "nodes"
        nodes.mkdir()
        (nodes / "personad-linux-arm64").write_bytes(b"different native Node")
        self.request["node_directory"] = str(nodes)
        with fixtures.contextlib.redirect_stdout(io.StringIO()):
            receipt = webdev.snapshot(self.request)
        self.path = Path(self.request["runtime"]) / "update/development-backups" / receipt["id"]
        self.manifest = json.loads((self.path / "manifest.json").read_text())

    def save_manifest(self, value):
        (self.path / "manifest.json").write_text(json.dumps(value))

    def test_malformed_schema_is_value_error_in_verify_list_and_prepare(self):
        changes = [
            ("programs", []), ("nodes", []), ("files", []), ("roots", "root"),
            ("databases", True), ("from_build", True), ("created_at", []),
            ("from_version", ["wrong type"]), ("from_commit", "invalid"),
            ("programs.personad", []), ("programs.personad.sha256", []),
            ("programs.personad.file", []), ("programs.personad.file", "../outside.gz"),
            ("programs.personad.file", "/outside.gz"),
            ("programs.personad.file", "C:\\outside.gz"),
            ("programs.personad.target", {"not": "a path"}),
            ("nodes.linux-arm64.sha256", 123), ("files.files.tar.gz", None),
        ]
        values = [None, [], 42]
        for field, value in changes:
            modified = copy.deepcopy(self.manifest)
            # Dotted artifact names are keys, not traversal components.
            keys = ["files", "files.tar.gz"] if field == "files.files.tar.gz" else field.split(".")
            parent = modified
            for key in keys[:-1]:
                parent = parent[key]
            parent[keys[-1]] = value
            values.append(modified)
        wrong = copy.deepcopy(self.manifest)
        wrong["programs"]["personad"].update(file="node-linux-arm64.gz", sha256=wrong["nodes"]["linux-arm64"]["sha256"])
        values.append(wrong)
        for i, value in enumerate(values):
            with self.subTest(case=i):
                self.save_manifest(value)
                with self.assertRaises(ValueError):
                    webdev.verify(self.path)
                with self.assertRaises(ValueError):
                    webdev.backup_version(self.path)
                self.assertEqual(webdev.backups(self.request), {"backups": []})
                work = self.root / f"prepare-{i}"
                work.mkdir()
                with self.assertRaises(ValueError):
                    webdev.prepare_backup(dict(self.request, backup_id=self.path.name, work=str(work), candidate=None))
                self.assertFalse((work / "artifacts").exists())

    def test_staged_native_hash_is_checked_after_compressed_checksum(self):
        work = self.root / "prepare"
        work.mkdir()
        original = webdev.digest
        def change_after_hash(path, on_read=None):
            result = original(path, on_read)
            if path.parent.name == "artifacts":
                path.write_bytes(gzip.compress(b"changed after compressed checksum"))
            return result
        with patch.object(webdev, "digest", change_after_hash), self.assertRaisesRegex(ValueError, "native|program checksum"):
            webdev.prepare_backup(dict(self.request, backup_id=self.path.name, work=str(work), candidate=None))

    def test_manifest_timestamps_keep_utc_and_offsets_but_require_a_timezone(self):
        for field in ("created_at", "from_updated_at"):
            for value in ("2026-11-12T10:00:00Z", "2026-11-12T10:00:00.123456Z", "2026-11-12T18:00:00+08:00"):
                with self.subTest(field=field, value=value):
                    modified = copy.deepcopy(self.manifest)
                    modified[field] = value
                    self.save_manifest(modified)
                    self.assertEqual(webdev.verify(self.path)[field], value)
            for value in ("2026-11-12T10:00:00", "2026-11-12T10:00:00ZZ", "invalid"):
                with self.subTest(field=field, value=value):
                    modified = copy.deepcopy(self.manifest)
                    modified[field] = value
                    self.save_manifest(modified)
                    with self.assertRaisesRegex(ValueError, "timestamp"):
                        webdev.verify(self.path)

    def archive(self, members, metadata=None):
        archive = self.path / "files.tar.gz"
        with tarfile.open(archive, "w:gz") as output:
            for name, kind, content in members:
                entry = tarfile.TarInfo(name)
                for key, value in (metadata or {}).get(name, {}).items():
                    setattr(entry, key, value)
                if kind == "file":
                    data = content.encode(); entry.size = len(data)
                    output.addfile(entry, io.BytesIO(data))
                else:
                    entry.type = {"directory": tarfile.DIRTYPE, "symlink": tarfile.SYMTYPE,
                                  "hardlink": tarfile.LNKTYPE, "fifo": tarfile.FIFOTYPE,
                                  "character": tarfile.CHRTYPE, "block": tarfile.BLKTYPE}[kind]
                    if kind in ("symlink", "hardlink"): entry.linkname = content
                    output.addfile(entry)
        manifest = copy.deepcopy(self.manifest)
        manifest["files"]["files.tar.gz"] = webdev.digest(archive)
        self.save_manifest(manifest)

    def test_unsafe_archive_members_and_links_are_rejected_before_extracting(self):
        normal = [("root-0", "directory", ""), ("root-0/data", "directory", ""), ("root-0/data/history.txt", "file", "saved history")]
        attacks = [[(name, "file", "outside")] for name in ["/escape", "root-0/../../escape", "root-0\\..\\escape", "C:/escape", "//host/share/escape", "root-0/C:escape", "root-0/data/./history.txt"]]
        attacks += [[("root-0/link", kind, target)] for kind, target in [
            ("symlink", "/outside"), ("symlink", "C:outside"), ("symlink", "..\\outside"),
            ("symlink", "../../outside"), ("symlink", "missing"),
            ("hardlink", "/outside"), ("hardlink", "root-0/data/history.txt")]]
        attacks += [[("root-0/link", "symlink", "data"), ("root-0/link/escape", "file", "unexpected")],
                    [("root-0/data/history.txt", "file", "duplicate")],
                    [("root-0/a", "symlink", "b"), ("root-0/b", "symlink", "a")]]
        attacks += [[("root-0/special", kind, "")] for kind in ("fifo", "character", "block")]
        for i, attack in enumerate(attacks):
            with self.subTest(case=i):
                self.archive(normal + attack)
                with self.assertRaises(ValueError): webdev.verify(self.path)
                destination = self.root / f"recover-{i}"
                with self.assertRaises(ValueError): webdev.extract_history(self.path, destination)
                self.assertFalse(destination.exists())
        self.assertFalse((self.root / "escape").exists())

    def test_legal_history_and_relative_link_restore_into_empty_directory(self):
        # Member order cannot make a validated link or child become a write path.
        self.archive([("root-0/latest", "symlink", "data"),
                      ("root-0/data/latest", "symlink", "history.txt"),
                      ("root-0/data/history.txt", "file", "saved history"),
                      ("root-0/data", "directory", ""), ("root-0", "directory", "")])
        destination = self.root / "recovered"
        webdev.extract_history(self.path, destination)
        link = destination / "root-0/data/latest"
        self.assertTrue(link.is_symlink())
        self.assertEqual(link.read_text(), "saved history")
        self.assertTrue(link.resolve().is_relative_to(destination.resolve()))  # macOS temp dirs sit behind /var -> /private/var
        self.assertTrue((destination / "root-0/latest").is_symlink())
        self.assertEqual((destination / "root-0/latest/history.txt").read_text(), "saved history")
        self.assertEqual((Path(self.request["runtime"]) / "data/history.txt").read_text(), "synthetic history")
        with self.assertRaises(ValueError): webdev.extract_history(self.path, destination)
        cli_destination = self.root / "cli-recovered"
        result = subprocess.run([sys.executable, str(fixtures.ROOT / "server/internal/update/webdev.py"),
                                 "extract-history", str(self.path), str(cli_destination)], capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0)
        self.assertEqual((cli_destination / "root-0/data/latest").read_text(), "saved history")

    def test_extraction_preserves_data_and_safe_modes_without_archive_ownership(self):
        stamp = 1600000000
        self.archive([("root-0", "directory", ""), ("root-0/data", "directory", ""),
                      ("root-0/data/history.txt", "file", "saved history"),
                      ("root-0/run", "file", "saved executable")], metadata={
            "root-0": {"mode": 0o2777, "mtime": stamp},
            "root-0/data": {"mode": 0, "mtime": stamp},
            "root-0/data/history.txt": {"mode": 0o6677, "uid": 12345, "gid": 12345,
                                       "uname": "archive-owner", "gname": "archive-group", "mtime": stamp},
            "root-0/run": {"mode": 0o7777, "mtime": stamp},
        })
        destination = self.root / "recovered"
        webdev.extract_history(self.path, destination)
        for name in ("", "root-0", "root-0/data"):
            self.assertEqual(stat.S_IMODE((destination / name).stat().st_mode), 0o700)
        for name, content, mode in (("data/history.txt", "saved history", 0o644),
                                    ("run", "saved executable", 0o755)):
            file = destination / "root-0" / name
            self.assertEqual(file.read_text(), content)
            self.assertEqual(stat.S_IMODE(file.stat().st_mode), mode)
            self.assertEqual(file.stat().st_uid, os.getuid())
            self.assertEqual(file.stat().st_gid, destination.stat().st_gid)
            self.assertEqual(file.stat().st_mtime, stamp)
        self.assertEqual((destination / "root-0/data").stat().st_mtime, stamp)

    def test_extraction_refuses_linked_destination_without_touching_target(self):
        target = self.root / "empty-target"
        target.mkdir()
        destination = self.root / "linked-destination"
        destination.symlink_to(target, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "empty directory"):
            webdev.extract_history(self.path, destination)
        self.assertEqual(list(target.iterdir()), [])
        self.assertTrue(destination.is_symlink())

    def test_snapshot_rejects_hard_linked_history_without_pruning(self):
        data = Path(self.request["runtime"]) / "data/history.txt"
        os.link(data, data.with_name("hard-linked-history"))
        with fixtures.contextlib.redirect_stdout(io.StringIO()), self.assertRaises(ValueError):
            webdev.snapshot(self.request)
        self.assertTrue(self.path.exists())


if __name__ == "__main__":
    unittest.main()
