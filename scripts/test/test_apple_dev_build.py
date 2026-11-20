"""The iOS development build (#10713): the switch that leaves the watch app and the share extension out, the line a lean
build prints when the branch changes what it leaves out, and the commit stamp that runs only in Release. The project
checks need xcodegen (skipped without it); the rest reads the scripts or runs them in disposable git repositories."""
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
APPLE = ROOT / 'apple'
SWITCH = 'PERSONA_LEAN_DEV_BUILD'
# What the iPhone app depends on in every build, in project.yml's order, and what only the whole build adds.
ALWAYS = ['LiveCommunicationKit.framework', 'PersonaWire', 'PersonaWidgets', 'PersonaNotify', 'MarkdownUI', 'BeautifulUI',
          'PersonaSSH', 'PersonaKit', 'PersonaCore', 'MarqueeGlow', 'PersonaScreenKit', 'PersonaInputKit']
WHOLE_ONLY = ['PersonaWatch', 'PersonaShare']
# Scripts that run `xcodegen generate` on a spec other than apple/project.yml, which the switch does not touch.
OTHER_PROJECTS = {
    'native/iphone-bridge/build.sh': 'its simulator test runner (native/iphone-bridge/sim-runner)',
    'scripts/app/island_peer.sh': 'apple/IslandPeer',
}
NOTE = 'scripts/app/lean-build-note.sh'


def dump(**switches):
    # Only the switches given: none of the caller's (persona_config.py env exports every PERSONA_* switch).
    env = {k: v for k, v in os.environ.items() if not k.startswith('PERSONA_')}
    env.setdefault('USER', 'fixture')
    env.update(switches)
    out = subprocess.run(['xcodegen', 'dump', '--spec', str(APPLE / 'project.yml'), '--type', 'json'],
                         env=env, capture_output=True, text=True, timeout=120, check=True).stdout
    return json.loads(out)


def dependencies(project, target='Persona'):
    return [d.get('target') or d.get('package') or d.get('sdk') for d in project['targets'][target]['dependencies']]


@unittest.skipUnless(shutil.which('xcodegen'), 'needs xcodegen')
class LeanDevBuildTests(unittest.TestCase):
    def test_unset_or_no_is_the_whole_project(self):
        # CI, release and the UI tests never set the switch: the app embeds the watch app and the share extension.
        for switches in ({}, {SWITCH: 'NO'}):
            with self.subTest(switches=switches):
                project = dump(**switches)
                self.assertEqual(dependencies(project), WHOLE_ONLY + ALWAYS)
                self.assertIn('PersonaShare', project['targets'])
                self.assertIn({'target': 'PersonaWatchWidgets'}, project['targets']['PersonaWatch']['dependencies'])

    def test_yes_leaves_out_only_the_watch_app_and_the_share_extension(self):
        whole, lean = dump(), dump(**{SWITCH: 'YES'})
        self.assertEqual(dependencies(lean), ALWAYS)
        # Nothing else changes: the targets stay (the PersonaWatch scheme still builds the watch on its own).
        lean_targets = {name: {k: v for k, v in target.items() if not (name == 'Persona' and k == 'dependencies')}
                        for name, target in lean['targets'].items()}
        whole_targets = {name: {k: v for k, v in target.items() if not (name == 'Persona' and k == 'dependencies')}
                         for name, target in whole['targets'].items()}
        self.assertEqual(lean_targets, whole_targets)
        self.assertEqual(lean['schemes'], whole['schemes'])

    def test_a_later_include_keeps_its_own_dependencies(self):
        # The switch empties only what the includes before it added; siri-calls.yml comes after it.
        lean = dump(**{SWITCH: 'YES', 'PERSONA_SIRI_CALLS': 'YES'})
        self.assertEqual(dependencies(lean), ['PersonaCallIntents'] + ALWAYS)
        whole = dump(PERSONA_SIRI_CALLS='YES')
        self.assertEqual(dependencies(whole), WHOLE_ONLY + ['PersonaCallIntents'] + ALWAYS)

    def test_the_switch_include_stays_before_every_include_that_adds_dependencies(self):
        project = (APPLE / 'project.yml').read_text()
        paths = re.findall(r'^  - path: (\S+)$', project.split('\nconfigFiles:')[0], re.M)
        self.assertEqual(paths[:3], ['app-entitlements.yml', 'watch-and-share.yml', 'lean-dev-build.yml'])
        self.assertNotIn('dependencies', (APPLE / 'app-entitlements.yml').read_text())


class LeanDevBuildCallersTests(unittest.TestCase):
    def test_only_sim_sh_turns_the_switch_on(self):
        listed = subprocess.run(['git', '-C', str(ROOT), 'grep', '-l', SWITCH + '=', '--', 'scripts', '.github'],
                                capture_output=True, text=True, timeout=60).stdout.split()
        self.assertEqual([p for p in listed if not p.startswith('scripts/test/')], ['scripts/app/sim.sh'])

    def test_sim_sh_builds_lean_unless_asked_for_the_watch(self):
        sim = (ROOT / 'scripts/app/sim.sh').read_text()
        self.assertIn('--with-watch) LEAN=NO', sim)
        self.assertIn('PERSONA_LEAN_DEV_BUILD=$LEAN xcodegen generate', sim)
        self.assertIn('build-apple.sh --simulator', sim)
        # The launch arguments still reach the app, without the flag.
        self.assertIn('${ARGS[@]+"${ARGS[@]}"}', sim)

    def test_release_ci_and_ui_tests_build_every_slice_of_the_rust_core(self):
        for path in ('scripts/release/release.sh', 'scripts/ci/apple.sh', 'scripts/app/ui-test.sh',
                     'scripts/app/perf-test.sh', 'scripts/diag/stream-scroll/run-ios.sh'):
            with self.subTest(path=path):
                text = (ROOT / path).read_text()
                self.assertIn('build-apple.sh', text)
                self.assertNotIn('--simulator', text)

    def test_every_other_caller_of_xcodegen_generates_the_whole_project(self):
        # Each clears the switch before it generates the project, so a value the shell exported cannot drop the watch
        # app from a TestFlight build or the share extension from the tests that share into it. Search for new callers
        # too, ignoring full-line comments that only describe the command.
        generate = '^[ \t]*([^# \t].*)?xcodegen +generate'
        listed = subprocess.run(['git', '-C', str(ROOT), 'grep', '-lE', generate, '--', '.',
                                 ':!docs', ':!*.md', ':!scripts/test'],
                                capture_output=True, text=True, timeout=60, check=True).stdout.split()
        for path in OTHER_PROJECTS:
            self.assertIn(path, listed, 'stale entry in OTHER_PROJECTS')
        callers = [p for p in listed if p not in OTHER_PROJECTS and p != 'scripts/app/sim.sh']
        self.assertTrue({'scripts/release/release.sh', 'scripts/ci/apple.sh', 'scripts/app/ui-test.sh',
                         'scripts/app/share-from-apps.sh'} <= set(callers), callers)
        for path in callers:
            with self.subTest(path=path):
                text = (ROOT / path).read_text()
                self.assertNotIn(SWITCH + '=', text)
                cleared = text.find('unset ' + SWITCH + '\n')
                self.assertNotEqual(cleared, -1, 'generates the project without clearing ' + SWITCH)
                self.assertLess(cleared, re.search(generate, text, re.MULTILINE).start(), 'clears it too late')

    def test_only_a_lean_build_names_the_check_it_skipped(self):
        sim = (ROOT / 'scripts/app/sim.sh').read_text()
        lean = sim[sim.index('if [ "$LEAN" = YES ]; then'):]
        self.assertIn('../' + NOTE, lean[:lean.index('\nfi\n')])
        self.assertEqual(sim.count('lean-build-note.sh'), 2)  # the call and the header that names it


class LeanBuildNoteTests(unittest.TestCase):
    """scripts/app/lean-build-note.sh in a disposable repository with an origin/main."""

    def setUp(self):
        self.root = Path(tempfile.mkdtemp(prefix='persona-lean-note-'))
        self.addCleanup(shutil.rmtree, self.root)
        (self.root / 'scripts/app').mkdir(parents=True)
        shutil.copy2(ROOT / NOTE, self.root / NOTE)
        for path in ('apple/Shared/Hub.swift', 'apple/PersonaWatch/Watch.swift', 'apple/PersonaWatchWidgets/Face.swift',
                     'apple/PersonaShare/Share.swift', 'apple/Packages/PersonaWire/Sources/PersonaWire/Wire.swift',
                     'apple/Packages/PersonaKit/Sources/PersonaKit/Kit.swift', 'apple/Persona/App.swift',
                     'server/main.go'):
            self.write(path, 'one\n')
        self.git('init', '-q', '-b', 'main')
        self.commit('base')
        self.git('update-ref', 'refs/remotes/origin/main', 'HEAD')
        self.git('checkout', '-q', '-b', 'feature')

    def git(self, *args):
        return subprocess.run(['git', '-C', str(self.root), '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.com',
                               '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null', *args],
                              capture_output=True, text=True, timeout=30, check=True).stdout

    def write(self, path, text):
        (self.root / path).parent.mkdir(parents=True, exist_ok=True)
        (self.root / path).write_text(text)

    def commit(self, message):
        self.git('add', '-A')
        self.git('commit', '-q', '-m', message)

    def note(self):
        run = subprocess.run(['bash', str(self.root / NOTE)], capture_output=True, text=True, timeout=30)
        self.assertEqual((run.returncode, run.stderr), (0, ''))
        self.assertLessEqual(len(run.stdout.splitlines()), 1)
        return run.stdout

    def test_the_folders_it_watches_are_the_real_ones(self):
        text = (ROOT / NOTE).read_text()
        folders = [f for line in re.findall(r'^(?:WATCH|WATCH_AND_SHARE)=\((.*)\)$', text, re.M) for f in line.split()]
        self.assertEqual(sorted(folders), ['apple/Packages/PersonaWire', 'apple/PersonaShare', 'apple/PersonaWatch',
                                           'apple/PersonaWatchWidgets', 'apple/Shared'])
        for folder in folders:
            with self.subTest(folder=folder):
                self.assertTrue((ROOT / folder).is_dir(), folder)
        project = (APPLE / 'project.yml').read_text()
        for folder in ('PersonaWatch', 'PersonaWatchWidgets', 'Shared'):
            self.assertIn('      - path: %s\n' % folder, project)
        # Every package the watch app, its widgets or the share extension builds is watched (#11250).
        packages = dict(re.findall(r'^  (\w+):\n    path: (Packages/\S+)$', project.split('\ntargets:')[0], re.M))
        for target in ('PersonaWatch', 'PersonaWatchWidgets', 'PersonaShare'):
            block = re.search(r'^  %s:\n(.*?)(?=^  \S)' % target, project.split('\ntargets:')[1], re.M | re.S).group(1)
            for name in re.findall(r'^      - package: (\w+)$', block, re.M):
                with self.subTest(target=target, package=name):
                    self.assertIn('apple/' + packages[name], folders)

    def test_nothing_changed_where_the_lean_build_skips_says_nothing(self):
        self.assertEqual(self.note(), '')
        self.write('apple/Persona/App.swift', 'two\n')
        self.write('server/main.go', 'two\n')
        self.commit('elsewhere')
        self.assertEqual(self.note(), '')

    def test_a_change_to_shared_names_the_whole_build(self):
        self.write('apple/Shared/Hub.swift', 'two\n')
        self.commit('shared')
        out = self.note()
        self.assertIn('changes apple/Shared/;', out)
        self.assertTrue(out.rstrip().endswith('scripts/app/sim.sh --with-watch'), out)

    def test_uncommitted_and_new_files_count(self):
        self.write('apple/PersonaShare/New.swift', 'new\n')
        self.assertIn('apple/PersonaShare/', self.note())
        self.git('checkout', '-q', '--', '.')
        (self.root / 'apple/PersonaShare/New.swift').unlink()
        self.write('apple/PersonaWatchWidgets/Face.swift', 'two\n')
        self.assertIn('apple/PersonaWatchWidgets/', self.note())

    def test_a_watch_only_change_names_the_watch_scheme(self):
        self.write('apple/PersonaWatch/Watch.swift', 'two\n')
        self.write('apple/PersonaWatchWidgets/Face.swift', 'two\n')
        self.commit('watch')
        out = self.note()
        self.assertIn('changes apple/PersonaWatch/ apple/PersonaWatchWidgets/;', out)
        self.assertIn("-scheme PersonaWatch -destination 'generic/platform=watchOS Simulator'", out)
        self.assertNotIn('--with-watch', out)
        # As sim.sh builds: the config's Xcode first, then in an xcode slot (#11250), as one command a shell can run.
        command = out.rstrip().split('check them with ', 1)[1]
        self.assertTrue(command.startswith('(eval "$(python3 scripts/persona_config.py env)" && '
                                           'python3 scripts/heavy_slot.py run --kind xcode -- xcodebuild '), command)
        self.assertEqual(subprocess.run(['bash', '-n', '-c', command], capture_output=True, timeout=30).returncode, 0)

    def test_a_wire_package_change_names_the_whole_build(self):
        # The watch app, its widgets and the share extension link PersonaWire too (#11250).
        self.write('apple/Packages/PersonaWire/Sources/PersonaWire/Wire.swift', 'two\n')
        self.commit('wire')
        out = self.note()
        self.assertIn('changes apple/Packages/PersonaWire/;', out)
        self.assertTrue(out.rstrip().endswith('scripts/app/sim.sh --with-watch'), out)
        self.git('reset', '-q', '--hard', 'HEAD~1')
        self.write('apple/Packages/PersonaKit/Sources/PersonaKit/Kit.swift', 'two\n')  # the iPhone app's only
        self.commit('kit')
        self.assertEqual(self.note(), '')

    def test_only_the_branch_counts_not_what_main_did_since(self):
        self.git('checkout', '-q', 'main')
        self.write('apple/Shared/Hub.swift', 'main moved on\n')
        self.commit('main')
        self.git('update-ref', 'refs/remotes/origin/main', 'HEAD')
        self.git('checkout', '-q', 'feature')
        self.assertEqual(self.note(), '')

    def test_without_origin_main_it_stays_quiet(self):
        self.write('apple/Shared/Hub.swift', 'two\n')
        self.git('update-ref', '-d', 'refs/remotes/origin/main')
        self.assertEqual(self.note(), '')


@unittest.skipUnless(shutil.which('xcodegen') and shutil.which('git'), 'needs xcodegen and git')
class CommitStampTests(unittest.TestCase):
    """Release stamps the commit exactly as before; Debug leaves the built Info.plist alone, so an unchanged build
    neither writes it again nor signs the app again."""

    def stamp(self, target, configuration, dirty, **settings):
        script = next(s['script'] for s in dump()['targets'][target]['postBuildScripts'] if s['name'] == 'Stamp the commit')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            git = ['git', '-C', str(root), '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.com',
                   '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null']
            subprocess.run(['git', 'init', '-q', str(root)], check=True)
            (root / 'apple').mkdir()
            (root / 'apple/file.txt').write_text('one\n')
            subprocess.run(git + ['add', '.'], check=True)
            subprocess.run(git + ['commit', '-q', '-m', 'fixture'], check=True)
            if dirty:
                (root / 'apple/file.txt').write_text('two\n')
            head = subprocess.check_output(['git', '-C', str(root), 'rev-parse', '--short=8', 'HEAD'], text=True).strip()
            plist = root / 'build/Persona.app/Info.plist'
            plist.parent.mkdir(parents=True)
            plist.write_bytes(plistlib.dumps({'CFBundleName': 'Persona'}))
            before = plist.read_bytes()
            env = {k: v for k, v in os.environ.items() if k != 'PERSONA_STAMP_COMMIT'}
            env.update(SRCROOT=str(root / 'apple'), TARGET_BUILD_DIR=str(root / 'build'),
                       INFOPLIST_PATH='Persona.app/Info.plist', CONFIGURATION=configuration, **settings)
            run = subprocess.run(['bash', '-e', '-c', script], cwd=root / 'apple', env=env, capture_output=True,
                                 text=True, timeout=30)
            self.assertEqual(run.returncode, 0, run.stdout + run.stderr)
            return head, before, plist.read_bytes()

    def test_release_stamps_the_commit_and_debug_leaves_the_plist_untouched(self):
        for target in ('Persona', 'PersonaVision'):
            for dirty in (False, True):
                with self.subTest(target=target, dirty=dirty):
                    head, _, after = self.stamp(target, 'Release', dirty)
                    self.assertEqual(plistlib.loads(after)['PersonaCommit'], head + ('+' if dirty else ''))
                    _, before, after = self.stamp(target, 'Debug', dirty)
                    self.assertEqual(after, before)
                    # A Debug build that must name its commit (the README's captures) asks for the stamp.
                    head, _, after = self.stamp(target, 'Debug', dirty, PERSONA_STAMP_COMMIT='YES')
                    self.assertEqual(plistlib.loads(after)['PersonaCommit'], head + ('+' if dirty else ''))


if __name__ == '__main__':
    unittest.main()
