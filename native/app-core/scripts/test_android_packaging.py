import importlib.util
import os
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[3]
SPEC = importlib.util.spec_from_file_location('package_report', ROOT / 'native/app-core/scripts/android-package-report.py')
report = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(report)
RUNNER = importlib.util.spec_from_file_location('device_runner', ROOT / 'native/app-core/scripts/android-device-test.py')
runner = importlib.util.module_from_spec(RUNNER)
RUNNER.loader.exec_module(runner)


class AndroidPackageReportTests(unittest.TestCase):
    def test_elf_machine_and_every_load_alignment_are_required(self):
        data = bytearray(0x100)
        data[:8] = b'\x7fELF\x02\x01\x01\x00'
        struct.pack_into('<HHIQQQIHHHHHH', data, 16, 2, 62, 1, 0, 0x40, 0, 0, 64, 56, 2, 0, 0, 0)
        struct.pack_into('<IIQQQQQQ', data, 0x40, 1, 0, 0, 0, 0, 0, 0, 0x4000)
        struct.pack_into('<IIQQQQQQ', data, 0x40 + 56, 1, 0, 0, 0, 0, 0, 0, 0x4000)
        self.assertEqual(report.inspect_elf(bytes(data), 'x86_64'), 2)
        with self.assertRaisesRegex(ValueError, 'wrong ELF machine'):
            report.inspect_elf(bytes(data), 'arm64-v8a')
        struct.pack_into('<Q', data, 0x40 + 56 + 48, 4096)
        with self.assertRaisesRegex(ValueError, 'LOAD alignments'):
            report.inspect_elf(bytes(data), 'x86_64')

    def test_apk_must_store_both_libraries_at_aligned_data_offsets(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            apk = root / 'test.apk'
            with zipfile.ZipFile(apk, 'w') as archive:
                for abi in report.ABIS:
                    info = zipfile.ZipInfo(f'lib/{abi}/libpersona_core.so')
                    info.compress_type = zipfile.ZIP_STORED
                    data = bytearray(0x100)
                    data[:8] = b'\x7fELF\x02\x01\x01\x00'
                    struct.pack_into('<HHIQQQIHHHHHH', data, 16, 2, 62 if abi == 'x86_64' else 183,
                                     1, 0, 0x40, 0, 0, 64, 56, 1, 0, 0, 0)
                    struct.pack_into('<IIQQQQQQ', data, 0x40, 1, 0, 0, 0, 0, 0, 0, 0x4000)
                    archive.writestr(info, data)
            # This check uses a test zipalign stand-in; no SDK or build is claimed.
            align = root / 'zipalign'
            align.write_text('#!/bin/sh\nprintf "Verification successful\\n"\n')
            align.chmod(0o700)
            with self.assertRaisesRegex(ValueError, 'data offset'):
                report.inspect_apk(apk, root)


class DeviceBenchmarkGateTests(unittest.TestCase):
    def test_debug_benchmark_apk_is_rejected_before_adb(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            apk = root / 'android/core/rustcore-android/build/outputs/apk/androidTest/debug/rustcore-android-debug-androidTest.apk'
            apk.parent.mkdir(parents=True)
            apk.write_bytes(b'not an installed test')
            result = subprocess.run([
                'python3', '-I', str(ROOT / 'native/app-core/scripts/android-device-test.py'),
                '--serial', 'emulator-test', '--suite', 'benchmark', '--apk', str(apk),
                '--output', str(root / 'result'),
            ], env=dict(os.environ, ANDROID_HOME=str(root)), capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('non-debuggable release androidTest APK', result.stderr)
            self.assertNotIn('adb', result.stdout)
    def test_release_benchmark_mapping_is_under_outputs(self):
        with tempfile.TemporaryDirectory() as directory:
            apk = Path(directory) / 'build/outputs/apk/androidTest/release/rustcore-android-release-androidTest.apk'
            mapping = apk.parents[3] / 'mapping/releaseAndroidTest/mapping.txt'
            self.assertEqual(mapping, apk.parents[3] / 'mapping/releaseAndroidTest/mapping.txt')
            self.assertNotIn('apk', mapping.parts[-4])

    def test_interrupted_junit_is_rejected(self):
        expected = [('persona.core.rustcore.RustCoreDeviceBenchmark', 'dispatchAndFrameCosts')]
        incomplete = '''INSTRUMENTATION_STATUS: class=persona.core.rustcore.RustCoreDeviceBenchmark
INSTRUMENTATION_STATUS: test=dispatchAndFrameCosts
INSTRUMENTATION_STATUS_CODE: 1
INSTRUMENTATION_CODE: -1
'''
        with self.assertRaisesRegex(ValueError, 'incomplete or unexpected'):
            runner.parse_junit(incomplete, expected)
        complete = incomplete + 'INSTRUMENTATION_STATUS_CODE: 0\nINSTRUMENTATION_CODE: -1\n'
        runner.parse_junit(complete, expected)
        with self.assertRaisesRegex(ValueError, 'duplicate JUnit completion'):
            runner.parse_junit(complete + 'INSTRUMENTATION_STATUS_CODE: 0\n', expected)

    def test_fixture_names_are_validated_like_the_device_reader(self):
        with tempfile.TemporaryDirectory() as directory:
            apk = Path(directory) / 'fixtures.apk'
            with zipfile.ZipFile(apk, 'w') as package:
                package.writestr('assets/continuity/a.json',
                                 '{"version":1,"cases":[{"name":"same"},{"name":"same"}]}')
            with self.assertRaisesRegex(ValueError, 'duplicate packaged continuity'):
                runner.fixture_identities(apk)
            with zipfile.ZipFile(apk, 'w') as package:
                package.writestr('assets/continuity/a.json', '{broken')
            with self.assertRaisesRegex(ValueError, 'invalid packaged continuity'):
                runner.fixture_identities(apk)

    def test_truncated_elf_is_a_bounded_failure(self):
        data = bytearray(64)
        data[:8] = b'\x7fELF\x02\x01\x01\x00'
        struct.pack_into('<HHIQQQIHHHHHH', data, 16, 2, 62, 1, 0, 0x40, 0, 0, 64, 56, 2, 0, 0, 0)
        with self.assertRaisesRegex(ValueError, 'truncated ELF'):
            report.inspect_elf(bytes(data), 'x86_64')


if __name__ == '__main__':
    unittest.main()
