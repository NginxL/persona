#!/usr/bin/env python3
"""Refract source archive regressions; run python3 -I scripts/app/test_refract_about_version.py."""
import gzip
import importlib.util
import pathlib
import tempfile
import types
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location(
    "refract_about_version", pathlib.Path(__file__).with_name("refract_about_version.py"))
refract = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(refract)


def archive_with_gzip_os(os_byte):
    compressed = bytearray(gzip.compress(b"canonical tar", compresslevel=9, mtime=0))
    compressed[9] = os_byte
    packaging = types.SimpleNamespace(
        source_files=lambda commit: ["About.swift"],
        run=lambda command: "100644 blob 0000000000000000000000000000000000000000\tAbout.swift",
        run_bytes=lambda command: b"source",
        deterministic_tar=lambda prefix, entries: b"canonical tar",
        ARCHIVE_EPOCH=0,
    )
    with tempfile.TemporaryDirectory() as temporary:
        output = pathlib.Path(temporary) / "source.tar.gz"
        with mock.patch.object(refract.importlib.util, "spec_from_file_location") as spec, \
                mock.patch.object(refract.importlib.util, "module_from_spec", return_value=packaging), \
                mock.patch.object(refract.gzip, "compress", return_value=bytes(compressed)):
            spec.return_value.loader = mock.Mock()
            refract.canonical_source_archive(pathlib.Path("unused"), {"commit": "abc"}, output)
        return output.read_bytes()


class RefractAboutVersionTests(unittest.TestCase):
    def test_canonical_archive_normalizes_gzip_os_byte(self):
        archives = [archive_with_gzip_os(os_byte) for os_byte in (0, 3, 255)]
        self.assertEqual(archives, [archives[1]] * 3)
        self.assertEqual(archives[0][9], 3)
        self.assertEqual([gzip.decompress(archive) for archive in archives], [b"canonical tar"] * 3)


if __name__ == "__main__":
    unittest.main()
