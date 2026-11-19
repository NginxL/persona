#!/usr/bin/env python3
"""Check the HarmonyOS NAPI integration's resource and test registration invariants."""
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
ENTRY = ROOT / 'harmony/entry'


class HarmonyCoreIntegrationTest(unittest.TestCase):
    def test_napi_is_one_library_without_a_cpp_bridge(self):
        cpp = ENTRY / 'src/main/cpp'
        cmake = (cpp / 'CMakeLists.txt').read_text()
        core = ROOT / 'native/app-core/target/aarch64-unknown-linux-ohos/release/libpersona_core_ohos.so'
        relative_core = Path(os.path.relpath(core, cpp)).as_posix()
        self.assertIn(f'set(PERSONA_CORE_LIBRARY "${{CMAKE_CURRENT_SOURCE_DIR}}/{relative_core}")', cmake)
        self.assertNotIn('persona_core_napi.cpp', cmake)
        package = (ENTRY / 'oh-package.json5').read_text()
        self.assertIn('"libpersona_core_ohos.so": "file:./src/main/cpp/types/libpersona_core"', package)
        lock = (ENTRY / 'oh-package-lock.json5').read_text()
        self.assertIn('"libpersona_core_ohos.so@src/main/cpp/types/libpersona_core": "libpersona_core_ohos.so@src/main/cpp/types/libpersona_core"', lock)
        self.assertIn('"resolved": "src/main/cpp/types/libpersona_core"', lock)
        self.assertIn('"registryType": "local"', lock)

    def test_real_fixture_runner_and_its_expected_update_are_registered(self):
        runner = (ENTRY / 'src/ohosTest/ets/testability/TestAbility.ets').read_text()
        self.assertIn("fixtureArgument('coreReplay')", runner)
        self.assertIn('nativeCoreTests()', runner)
        replay = (ENTRY / 'src/ohosTest/ets/test/NativeCore.test.ets').read_text()
        self.assertIn("expect(cases).assertEqual(39)", replay)
        self.assertIn("expect(steps).assertEqual(95)", replay)
        self.assertIn('expect(patch.ops.length).assertEqual(1)', replay)
        self.assertIn("expect(patch.ops[0].kind).assertEqual('update')", replay)
        self.assertIn("getRawFileContent('core-fixtures/continuity.json')", replay)
        self.assertIn('finally { native.coreClose(handle); }', replay)

    def test_packaging_has_no_duplicate_fixture_expectations(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            subprocess.run([sys.executable, '-I', str(ROOT / 'native/app-core/scripts/package-ohos-fixtures.py'),
                            '--fixtures', str(ROOT / 'spec/fixtures/continuity'), '--output', str(output)], check=True)
            manifest = json.loads((output / 'manifest.json').read_text())
            self.assertEqual(manifest['cases'], 39)
            self.assertEqual(manifest['steps'], 95)
            documents = json.loads((output / 'continuity.json').read_text())
            self.assertEqual(len(documents), 5)
            self.assertEqual(sum(len(document['document']['cases']) for document in documents), 39)
            self.assertEqual(sum(len(case['input']['events']) for document in documents for case in document['document']['cases']), 95)


if __name__ == '__main__':
    unittest.main()
