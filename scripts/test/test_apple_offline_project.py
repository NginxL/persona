"""The offline simulator graph retains the real test host and only reachable targets."""

from copy import deepcopy
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'scripts/ci/apple_offline_project.py'
sys.path.insert(0, str(ROOT / 'scripts/ci'))
import apple_offline_project


class AppleOfflineProjectTests(unittest.TestCase):
    def setUp(self):
        self.base = yaml.safe_load((ROOT / 'apple/project.yml').read_text())
        self.project = deepcopy(self.base)
        self.project.pop('include')
        modules = yaml.safe_load((ROOT / 'apple/modules.yml').read_text())['targets']['Persona']
        self.module_sources = modules['sources']
        self.project['targets']['Persona']['sources'] = [
            *self.module_sources, *self.project['targets']['Persona']['sources'],
        ]
        self.project['targets']['Persona']['settings']['base']['SWIFT_ACTIVE_COMPILATION_CONDITIONS'] = \
            modules['settings']['base']['SWIFT_ACTIVE_COMPILATION_CONDITIONS']
        self.project['schemes'] = {name: self.base['schemes'][name] for name in (
            'Persona', 'PersonaOfflineUnitTests', 'AboutVersionVisualTests',
            'AboutVersionAccessibilityHost', 'PersonaWatch', 'PersonaVision')}
        self.project['targets']['Persona']['dependencies'] = [
            {'target': 'PersonaWatch'}, {'target': 'PersonaShare'},
            *self.project['targets']['Persona']['dependencies'],
        ]
        self.result = apple_offline_project.filtered(self.project)

    def test_real_host_dependencies_survive_but_watch_and_share_do_not(self):
        dependencies = self.result['targets']['Persona']['dependencies']
        self.assertEqual(self.result['targets']['Persona']['sources'],
                         self.project['targets']['Persona']['sources'])
        self.assertEqual(self.result['targets']['Persona']['sources'][:len(self.module_sources)],
                         self.module_sources)
        self.assertEqual(self.result['targets']['Persona']['settings'],
                         self.project['targets']['Persona']['settings'])
        expected = self.project['targets']['Persona']['dependencies'][2:]
        self.assertEqual(dependencies, expected)
        self.assertIn({'target': 'PersonaWidgets'}, dependencies)
        self.assertIn({'target': 'PersonaNotify'}, dependencies)
        self.assertTrue(any('package' in dependency for dependency in dependencies))
        self.assertEqual(set(self.result['targets']), {
            'Persona', 'PersonaTests', 'PersonaWidgets', 'PersonaNotify',
        })
        self.assertEqual(set(self.result['schemes']), {'PersonaOfflineUnitTests'})
        self.assertEqual(self.result['schemes']['PersonaOfflineUnitTests'],
                         self.project['schemes']['PersonaOfflineUnitTests'])
        for name in ('PersonaWatch', 'PersonaWatchWidgets', 'PersonaShare', 'PersonaUITests'):
            self.assertNotIn(name, self.result['targets'])
            self.assertNotIn(name, dependencies)

    def test_test_host_and_loader_are_canonical(self):
        tests = self.result['targets']['PersonaTests']
        self.assertEqual(tests, self.base['targets']['PersonaTests'])
        self.assertIn({'target': 'Persona'}, tests['dependencies'])
        self.assertEqual(tests['settings']['base']['TEST_HOST'], '$(BUILT_PRODUCTS_DIR)/Persona.app/Persona')
        self.assertEqual(tests['settings']['base']['BUNDLE_LOADER'], '$(TEST_HOST)')
        self.assertEqual(self.result['packages'], self.project['packages'])

    def test_host_or_selection_changes_fail_closed(self):
        changed = deepcopy(self.project)
        changed['targets']['PersonaTests']['settings']['base']['TEST_HOST'] = 'another.app/Another'
        with self.assertRaisesRegex(ValueError, 'bundle loader'):
            apple_offline_project.filtered(changed)
        changed = deepcopy(self.project)
        changed['targets']['PersonaTests']['dependencies'] = []
        with self.assertRaisesRegex(ValueError, 'host dependency'):
            apple_offline_project.filtered(changed)
        changed = deepcopy(self.project)
        changed['schemes']['PersonaOfflineUnitTests']['test']['targets'] = []
        with self.assertRaisesRegex(ValueError, 'test selection'):
            apple_offline_project.filtered(changed)
        changed = deepcopy(self.project)
        changed['schemes']['PersonaOfflineUnitTests']['build']['targets']['Persona'] = 'none'
        with self.assertRaisesRegex(ValueError, 'build must retain'):
            apple_offline_project.filtered(changed)

    def test_retained_sources_dependencies_and_packages_fail_closed(self):
        cases = {
            'Widgets host dependency': lambda project: project['targets']['Persona']['dependencies'].remove(
                {'target': 'PersonaWidgets'}),
            'Notify host dependency': lambda project: project['targets']['Persona']['dependencies'].remove(
                {'target': 'PersonaNotify'}),
            'test sources': lambda project: project['targets']['PersonaTests'].__setitem__('sources', []),
            'test source path': lambda project: project['targets']['PersonaTests']['sources'][0].__setitem__(
                'path', 'PersonaTests/TypeScaleTests.swift'),
            'test source excludes': lambda project: project['targets']['PersonaTests']['sources'][0]['excludes'].append(
                'AboutVersion*'),
            'host package dependency': lambda project: project['targets']['Persona']['dependencies'].remove(
                {'package': 'PersonaCore'}),
            'test package dependency': lambda project: project['targets']['PersonaTests']['dependencies'].remove(
                {'package': 'PersonaCore', 'product': 'PersonaCoreTestSupport'}),
            'package declaration': lambda project: project['packages']['MarkdownUI'].__setitem__('from', '1.0.0'),
            'host sources': lambda project: project['targets']['Persona'].__setitem__('sources', []),
            'module compilation conditions': lambda project: project['targets']['Persona']['settings']['base'].pop(
                'SWIFT_ACTIVE_COMPILATION_CONDITIONS'),
            'test assertions': lambda project: project['schemes']['PersonaOfflineUnitTests']['test'].__setitem__(
                'gatherCoverageData', False),
        }
        for name, mutate in cases.items():
            with self.subTest(name=name):
                changed = deepcopy(self.project)
                mutate(changed)
                with self.assertRaisesRegex(ValueError, 'canonical'):
                    apple_offline_project.filtered(changed)

    def test_cli_refuses_to_materialize_a_reduced_test_graph(self):
        changed = deepcopy(self.project)
        changed['targets']['PersonaTests']['sources'] = []
        with tempfile.TemporaryDirectory() as directory:
            dump, output = Path(directory) / 'dump.json', Path(directory) / 'offline.json'
            dump.write_text(json.dumps(changed))
            generated = subprocess.run([sys.executable, '-I', str(SCRIPT), str(dump), str(output)],
                                       capture_output=True, text=True, timeout=30)
            self.assertNotEqual(generated.returncode, 0)
            self.assertIn('canonical', generated.stderr)
            self.assertFalse(output.exists())

    def test_cli_preserves_target_graph_in_the_generated_json(self):
        with tempfile.TemporaryDirectory() as directory:
            dump, output = Path(directory) / 'dump.json', Path(directory) / 'offline.json'
            dump.write_text(json.dumps(self.project))
            subprocess.run([sys.executable, '-I', str(SCRIPT), str(dump), str(output)],
                           check=True, capture_output=True, text=True, timeout=30)
            self.assertEqual(json.loads(output.read_text()), self.result)

    @unittest.skipUnless(shutil.which('xcodegen'), 'needs xcodegen')
    def test_xcodegen_materializes_only_the_real_host_and_its_two_extensions(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shutil.copytree(ROOT / 'apple', root / 'source/apple',
                            ignore=shutil.ignore_patterns('*.xcodeproj', 'Config.xcconfig'))
            about = ROOT / 'android/refract/about/AboutVersionRow.swift'
            (root / 'source/android/refract/about').mkdir(parents=True)
            shutil.copy2(about, root / 'source/android/refract/about/AboutVersionRow.swift')
            (root / 'source/apple/Config.xcconfig').write_text('')
            environment = dict(os.environ, USER='fixture', PERSONA_CONFIG=str(root / 'config.json'))
            whole = root / 'whole.json'
            with whole.open('w') as output:
                subprocess.run(['xcodegen', 'dump', '--spec', str(root / 'source/apple/project.yml'),
                                '--type', 'json'], env=environment, check=True, stderr=subprocess.PIPE,
                               text=True, timeout=60, stdout=output)
            spec = root / 'offline.json'
            original = json.loads(whole.read_text())
            self.assertGreaterEqual(set(original['targets']), {
                'Persona', 'PersonaTests', 'PersonaWidgets', 'PersonaNotify', 'PersonaWatch',
                'PersonaWatchWidgets', 'PersonaShare', 'PersonaVision', 'PersonaUITests',
                'AboutVersionAccessibilityHost', 'AboutVersionAccessibilityUITests',
            })
            self.assertEqual(original['targets']['Persona']['dependencies'][:2], [
                {'target': 'PersonaWatch'}, {'target': 'PersonaShare'},
            ])
            self.assertEqual(original['schemes']['Persona']['build']['targets'], {
                'Persona': 'all', 'PersonaTests': ['test'], 'PersonaUITests': ['test'],
            })
            subprocess.run([sys.executable, '-I', str(SCRIPT), str(whole), str(spec)],
                           env=environment, check=True, capture_output=True, text=True, timeout=30)
            generated = subprocess.run(['xcodegen', 'generate', '--spec', str(spec), '--project-root',
                                       str(root / 'source/apple'), '--project', str(root / 'source/apple')],
                                      env=environment, capture_output=True, text=True, timeout=60)
            self.assertEqual(generated.returncode, 0, generated.stdout + generated.stderr)
            project = root / 'source/apple/PersonaOfflineUnitTests.xcodeproj'
            pbx = (project / 'project.pbxproj').read_text()
            native = pbx.split('/* Begin PBXNativeTarget section */', 1)[1].split(
                '/* End PBXNativeTarget section */', 1)[0]
            targets = re.findall(r'/\* (Persona\w*|AboutVersion\w*) \*/ = \{\n\s*isa = PBXNativeTarget;', native)
            self.assertEqual(sorted(targets), ['Persona', 'PersonaNotify', 'PersonaTests', 'PersonaWidgets'])
            copy = pbx.split('/* Begin PBXCopyFilesBuildPhase section */', 1)[1].split(
                '/* End PBXCopyFilesBuildPhase section */', 1)[0]
            for required in ('PersonaWidgets', 'PersonaNotify'):
                self.assertIn(required, copy)
            for excluded in ('PersonaWatch', 'PersonaShare'):
                self.assertNotIn(excluded, copy)
            self.assertIn('TEST_HOST = "$(BUILT_PRODUCTS_DIR)/Persona.app/Persona";', pbx)
            self.assertIn('BUNDLE_LOADER = "$(TEST_HOST)";', pbx)
            scheme = (project / 'xcshareddata/xcschemes/PersonaOfflineUnitTests.xcscheme').read_text()
            self.assertIn('BuildableName = "Persona.app"', scheme)
            self.assertIn('BuildableName = "PersonaTests.xctest"', scheme)
            self.assertNotIn('PersonaWatch', scheme)
            self.assertNotIn('PersonaUITests', scheme)
            whole_project = root / 'source/apple/Persona.xcodeproj'
            generated_whole = subprocess.run(['xcodegen', 'generate', '--spec',
                                              str(root / 'source/apple/project.yml'), '--project-root',
                                              str(root / 'source/apple'), '--project', str(root / 'source/apple')],
                                             env=environment, capture_output=True, text=True, timeout=60)
            self.assertEqual(generated_whole.returncode, 0, generated_whole.stdout + generated_whole.stderr)
            whole_pbx = (whole_project / 'project.pbxproj').read_text()
            whole_native = whole_pbx.split('/* Begin PBXNativeTarget section */', 1)[1].split(
                '/* End PBXNativeTarget section */', 1)[0]
            whole_targets = re.findall(r'/\* (Persona\w*|AboutVersion\w*) \*/ = \{\n\s*isa = PBXNativeTarget;',
                                       whole_native)
            self.assertGreaterEqual(set(whole_targets), {
                'Persona', 'PersonaTests', 'PersonaWidgets', 'PersonaNotify', 'PersonaWatch',
                'PersonaWatchWidgets', 'PersonaShare', 'PersonaVision', 'PersonaUITests',
                'AboutVersionAccessibilityHost', 'AboutVersionAccessibilityUITests',
            })
            whole_copy = whole_pbx.split('/* Begin PBXCopyFilesBuildPhase section */', 1)[1].split(
                '/* End PBXCopyFilesBuildPhase section */', 1)[0]
            for required in ('PersonaWatch', 'PersonaShare', 'PersonaWidgets', 'PersonaNotify'):
                self.assertIn(required, whole_copy)


if __name__ == '__main__':
    unittest.main()
